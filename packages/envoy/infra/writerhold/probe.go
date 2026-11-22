package writerhold

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/natscred"
)

// probeAdmitted is the probe's first run, outside the hold, with the row still prepare: it reads the
// probe's credential file once, verifies the token as the NATS auth service does, against the
// published keys of the issuer it names (probeKeys.credentialOf), dials the hub presenting that
// token, and must be admitted, so the probe's refusal in the hold (step 2 and each restarted
// server's proof) cannot come from its own credential, an expired token or a probe entry the auth
// service does not list (plan §3a step 2). It answers the admitted-probe record of that token for
// `hold set`'s intent (probeRecordInputs). A credential it cannot read or verify is UNKNOWN; any
// dial error refuses the hold, naming it.
func probeAdmitted(ctx context.Context, op *Operator) (map[string]string, error) {
	raw, err := op.probeToken()
	if err != nil {
		return nil, err
	}
	credential, err := op.probeKeys.credentialOf(ctx, raw)
	if err != nil {
		return nil, unknownf(StepProbe, err, "verify the probe's credential as the auth service does, against its issuer's published keys")
	}
	if err := op.dialProbe(ctx, strings.Join(op.URLs, ","), raw); err != nil {
		return nil, refusef(StepProbe, "the hold's probe was not admitted outside the hold, so its refusal in the hold would not show the hold's: %v", err)
	}
	return probeRecordInputs(credential), nil
}

// The inputs of `hold set`'s intent that record its admitted probe: the identity the hub admitted
// under prepare (the token's issuer, the subject the auth service's verifier answers for it, and
// its audiences, a JSON array, so an audience holding a space reads back whole), and the sha256 of
// the whole token it presented then with that token's expiry, the exact match each later dial
// tries first. The token itself is never recorded.
const (
	probeTokenInput    = "probe_token_sha256"
	probeIssuerInput   = "probe_issuer"
	probeSubjectInput  = "probe_subject"
	probeAudienceInput = "probe_audience"
	probeExpiryInput   = "probe_expiry"
)

// probeRecordInputs are credential's record as `hold set`'s intent holds it.
func probeRecordInputs(credential ProbeCredential) map[string]string {
	audience, err := json.Marshal(credential.Audience)
	if err != nil {
		panic(err) // a []string always encodes
	}
	return map[string]string{
		probeTokenInput:    credential.TokenSHA256,
		probeIssuerInput:   credential.Issuer,
		probeSubjectInput:  credential.Subject,
		probeAudienceInput: string(audience),
		probeExpiryInput:   credential.Expiry.UTC().Format(time.RFC3339),
	}
}

// useProbeRecord makes the admitted-probe record of d, this hold's signed journal, the one Probe
// counts a refusal against: the one the latest set intent that recorded one holds, or none. A
// record whose expiry or audiences do not read is UNKNOWN. Each command calls it before its first
// dial, and it drops the issuer's keys an earlier command kept, so each command reads them afresh.
func (op *Operator) useProbeRecord(d holdrecord.Done) error {
	op.probe = nil
	op.probeKeys = probeKeys{}
	for _, o := range d.Ops {
		if o.Command != holdrecord.CommandSet || o.State != holdrecord.StateIntent || o.Inputs[probeTokenInput] == "" {
			continue
		}
		expiry, err := time.Parse(time.RFC3339, o.Inputs[probeExpiryInput])
		if err != nil {
			return unknownf(StepProbe, err, "read this hold's admitted-probe record: its %s", probeExpiryInput)
		}
		var audience []string
		if err := json.Unmarshal([]byte(o.Inputs[probeAudienceInput]), &audience); err != nil {
			return unknownf(StepProbe, err, "read this hold's admitted-probe record: its %s", probeAudienceInput)
		}
		op.probe = &ProbeCredential{TokenSHA256: o.Inputs[probeTokenInput], Issuer: o.Inputs[probeIssuerInput],
			Subject: o.Inputs[probeSubjectInput], Audience: audience, Expiry: expiry}
	}
	return nil
}

// probeToken reads the probe's credential file once, UNKNOWN when there is no reader or no token.
func (op *Operator) probeToken() (string, error) {
	if op.ProbeToken == nil {
		return "", unknownf(StepProbe, nil, "the hold has no reader of the probe's credential")
	}
	raw, err := op.ProbeToken()
	if err != nil {
		return "", unknownf(StepProbe, err, "read the probe's credential")
	}
	return raw, nil
}

// dialProbe dials url once, presenting token as the probe's credential in CONNECT's password
// (natscred.ServiceUser), and answers the dial's error: nil when the server admitted it, one
// wrapping nats.ErrAuthorization when it refused it. Only Probe and the probe's first run read it;
// a test's probeDial stands in for it.
func (op *Operator) dialProbe(ctx context.Context, url, token string) error {
	if op.probeDial != nil {
		return op.probeDial(ctx, url, token)
	}
	nc, err := nats.Connect(url, nats.UserInfo(natscred.ServiceUser, token), nats.Name("envoy-infra hold probe"), nats.NoReconnect(), nats.Timeout(ProbeTimeout))
	if err == nil {
		nc.Close()
	}
	return err
}

// Probe dials url once as the hold's probe (Restart's probe), reading the probe's
// credential file at this dial and presenting the token it read: nil when the server admitted it,
// an error wrapping nats.ErrAuthorization when it refused it and that refusal is the hold's. A
// refusal is the hold's only when the token proves the probe identity this hold's set admitted
// under prepare (unproven): (a) this hold's signed journal holds the admitted-probe record
// (useProbeRecord); (b) the token is the admitted one, or one whose signature verifies against the
// recorded issuer's published keys, as the auth service verifies it, naming the recorded issuer,
// subject and audience, as the kubelet's rewrite of a projected token does; and (c) the token
// lapses after now plus ProbeTimeout. Otherwise the dial is UNKNOWN, naming which, and no caller
// counts it. Every path that counts a refusal of the probe as the hold's dials through Probe:
// `hold set`'s step 2 and the restart's proof of each server, in `hold set`, `hold prove` and
// RestartProveAndBarrier. The check proves the same probe identity signed by its issuer, not that
// the hub would admit it now: a probe entry dropped from the auth service's list is the README's
// stated residual. Probe is not safe for concurrent use: it keeps the issuer's keys it read.
func (op *Operator) Probe(ctx context.Context, url string) error {
	raw, err := op.probeToken()
	if err != nil {
		return err
	}
	err = op.dialProbe(ctx, url, raw)
	if !errors.Is(err, nats.ErrAuthorization) {
		return err
	}
	if why := op.unproven(ctx, raw, time.Now()); why != "" {
		return unknownf(StepProbe, nil, "the server refused the hold's probe (%v), %s", err, why)
	}
	return err
}

// probeProven is Probe's check with no dial, for a command to make before it writes anything: the
// token the probe would present now proves this hold's admitted probe identity, within its life.
func (op *Operator) probeProven(ctx context.Context) error {
	raw, err := op.probeToken()
	if err != nil {
		return err
	}
	if why := op.unproven(ctx, raw, time.Now()); why != "" {
		return unknownf(StepProbe, nil, "%s", why)
	}
	return nil
}

// unproven is why a refusal of a probe that presented raw is not the hold's under op.probe, this
// hold's admitted-probe record (nil for none), naming which check fails; "" when it is the hold's.
// The admitted token itself is taken by its hash; any other is verified (probeKeys.prove).
func (op *Operator) unproven(ctx context.Context, raw string, now time.Time) string {
	const why = "a refusal of the probe is the hold's only for the probe identity this hold's set admitted under prepare"
	p := op.probe
	if p == nil {
		return why + ", and (a) this hold's journal holds no admitted-probe record"
	}
	expiry := p.Expiry
	if tokenSHA256(raw) != p.TokenSHA256 {
		proved, err := op.probeKeys.prove(ctx, *p, raw)
		switch {
		case errors.Is(err, oidc.ErrExpired):
			return fmt.Sprintf("%s, and (c) the token presented has lapsed: %v", why, err)
		case err != nil:
			return fmt.Sprintf("%s, and (b) the token presented is not the admitted one and does not prove its identity (issuer %s, subject %s, audience %q): %v",
				why, p.Issuer, p.Subject, p.Audience, err)
		}
		expiry = proved.Expiry
	}
	if !expiry.After(now.Add(ProbeTimeout)) {
		return fmt.Sprintf("%s, and (c) the token presented lapses at %s, not after now plus the probe's timeout (%s)", why, expiry.UTC().Format(time.RFC3339), ProbeTimeout)
	}
	return ""
}

// ProbeTimeout bounds one dial of the hold's probe.
const ProbeTimeout = 10 * time.Second

// ProbeCredential is what the hold records of the probe's credential, never the secret: the sha256
// of the whole token, and the identity it proved: its issuer, the subject the auth service's
// verifier answers for it, its audiences (sorted) and when it lapses.
type ProbeCredential struct {
	TokenSHA256 string
	Issuer      string
	Subject     string
	Audience    []string
	Expiry      time.Time
}

// ProbeCredentialOf is raw's record: the probe's platform identity token as read from its file,
// verified as the NATS auth service verifies it, against the published keys of the issuer it
// names, for the first audience it names. A token that names no issuer or audience, or does not
// verify, is an error.
func ProbeCredentialOf(ctx context.Context, raw string) (ProbeCredential, error) {
	return new(probeKeys).credentialOf(ctx, raw)
}

// probeKeys verifies probe tokens over one issuer's published keys, read once and kept: the
// Operator's, across a command's dials, and a fresh one for ProbeCredentialOf.
type probeKeys struct {
	issuer string
	keys   *oidc.Keys
}

// credentialOf is ProbeCredentialOf over k's keys.
func (k *probeKeys) credentialOf(ctx context.Context, raw string) (ProbeCredential, error) {
	issuer, audience, err := tokenIssuerAudience(raw)
	if err != nil {
		return ProbeCredential{}, err
	}
	if issuer == "" || len(audience) == 0 {
		return ProbeCredential{}, errors.New("the token names no issuer or no audience")
	}
	return k.verify(ctx, issuer, audience[0], raw)
}

// prove is the identity raw proves under p, a record of an admitted probe: raw must name p's issuer
// and verify, as the auth service verifies it, against that issuer's published keys for p's first
// audience, naming p's subject and p's audiences. Its error names the check that fails; an expired
// token's wraps oidc.ErrExpired.
func (k *probeKeys) prove(ctx context.Context, p ProbeCredential, raw string) (ProbeCredential, error) {
	if p.Issuer == "" || p.Subject == "" || len(p.Audience) == 0 {
		return ProbeCredential{}, errors.New("the record names no issuer, subject or audience to prove")
	}
	issuer, _, err := tokenIssuerAudience(raw)
	if err != nil {
		return ProbeCredential{}, err
	}
	if issuer != p.Issuer {
		return ProbeCredential{}, fmt.Errorf("its issuer is %q, not the recorded issuer", issuer)
	}
	proved, err := k.verify(ctx, p.Issuer, p.Audience[0], raw)
	switch {
	case errors.Is(err, oidc.ErrAudience):
		return ProbeCredential{}, fmt.Errorf("it is not for the recorded audience %q: %w", p.Audience[0], err)
	case errors.Is(err, oidc.ErrSignature):
		return ProbeCredential{}, fmt.Errorf("its signature does not verify against the recorded issuer's published keys: %w", err)
	case err != nil:
		return ProbeCredential{}, err
	case proved.Subject != p.Subject:
		return ProbeCredential{}, fmt.Errorf("it names the subject %q, not the recorded subject", proved.Subject)
	case !slices.Equal(proved.Audience, p.Audience):
		return ProbeCredential{}, fmt.Errorf("its audience is %q, not the recorded audience %q", proved.Audience, p.Audience)
	}
	return proved, nil
}

// verify verifies raw as the NATS auth service verifies a service's platform token, with the
// verifier both of its token readers wrap (natscallout.PlatformTokens hands a projected Kubernetes
// token to enroll.K8sPodVerifier and a Google identity token to api.GoogleTokens, each a few lines
// over it): internal/oidc's Verifier of issuer for audience, over issuer's published keys read from
// its discovery document as the auth service reads them (oidc.NewKeys), which checks raw's issuer,
// audience, signature and expiry. The wrappers are not called themselves: they live in the
// broker's packages, which would link the broker's store and its embedded migrations into
// envoy-infra. The discovery read is bounded as the auth service bounds it
// (oidc.DiscoveryAttemptTimeout), and the key-set read by the key source's own fetch bound
// (oidc.DefaultKeysFetchTimeout), so an issuer that accepts and never answers ends the check
// within those bounds, an error naming the issuer's address, whatever the command's context
// allows. It answers the identity raw proves: the issuer, the subject the verifier answers (a
// Google token's verified email, which natscallout requires, for Google's issuer), the audiences
// the verified token names, sorted, and its expiry.
func (k *probeKeys) verify(ctx context.Context, issuer, audience, raw string) (ProbeCredential, error) {
	if k.keys == nil || k.issuer != issuer {
		discover, cancel := context.WithTimeout(ctx, oidc.DiscoveryAttemptTimeout)
		keys, err := oidc.NewKeys(discover, issuer)
		cancel()
		if err != nil {
			return ProbeCredential{}, fmt.Errorf("read the published keys of the issuer %s within %s: %w", issuer, oidc.DiscoveryAttemptTimeout, err)
		}
		k.issuer, k.keys = issuer, keys
	}
	verifier := oidc.NewWithKeys(issuer, audience, k.keys)
	var claims oidc.Claims
	var err error
	if issuer == oidc.GoogleIssuer {
		claims, err = verifier.VerifyGoogleIdentity(ctx, raw)
		if err == nil && (claims.Email == "" || !claims.EmailVerified) {
			err = errors.New("the Google identity token carries no verified email")
		}
		claims.Subject = claims.Email
	} else {
		claims, err = verifier.Verify(ctx, raw)
	}
	if err != nil {
		return ProbeCredential{}, err
	}
	return ProbeCredential{TokenSHA256: tokenSHA256(raw), Issuer: issuer, Subject: claims.Subject,
		Audience: slices.Sorted(slices.Values(claims.Audience)), Expiry: claims.Expiry}, nil
}

// tokenIssuerAudience are the issuer and the audiences, sorted, that raw's payload names, read
// without verifying it: they only pick the verifier and the audience it is built for. The audience
// is a string or a list of strings.
func tokenIssuerAudience(raw string) (string, []string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", nil, errors.New("the token is no JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", nil, errors.New("the token's claims do not decode")
	}
	var claims struct {
		Issuer   string          `json:"iss"`
		Audience json.RawMessage `json:"aud"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", nil, errors.New("the token's claims do not decode")
	}
	var audience []string
	if len(claims.Audience) > 0 {
		var one string
		if json.Unmarshal(claims.Audience, &one) == nil {
			audience = []string{one}
		} else if err := json.Unmarshal(claims.Audience, &audience); err != nil {
			return "", nil, errors.New("the token's audience is neither a string nor a list of strings")
		}
	}
	slices.Sort(audience)
	return claims.Issuer, audience, nil
}

// tokenSHA256 is the hex sha256 of raw, a whole token.
func tokenSHA256(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
