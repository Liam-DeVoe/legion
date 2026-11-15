// K8sPodVerifier verifies a projected service-account token with the shared internal/oidc
// verifiers (issuer, audience, signature, expiry, subject) and then reads the pod UID claim from
// the same, already-verified token — the check Hawk's token broker makes (_is_pod_bound). A pod's
// enrollment needs the token bound to its pod (Verify); a service's token login reads the account
// and, when the token has one, its pod (VerifyAccount), and the issuer of BROKER_K8S_OIDC_ISSUER
// that signed the token, which an account entry's issuers are judged by.
package enroll

import (
	"context"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/sjawhar/envoy/internal/oidc"
)

// IssuerVerifier checks a token against the issuer its iss names, among several, and answers
// that issuer beside the claims: *oidc.IssuerSet, over the issuers BROKER_K8S_OIDC_ISSUER names.
// It is an interface so that a test can verify through a key set it configures itself, which a
// set builds for each of its issuers out of reach.
type IssuerVerifier interface {
	Verify(ctx context.Context, raw string) (oidc.Claims, oidc.NamedIssuer, error)
}

// K8sPodVerifier is the broker's PodVerifier: Issuers checks every token against the issuer it
// names, so a token of an issuer the set does not name is oidc.ErrIssuer and one of an issuer
// the broker cannot read yet is oidc.ErrIssuerUnavailable.
type K8sPodVerifier struct{ Issuers IssuerVerifier }

// AccountClaims are a verified ServiceAccount token's subject
// (system:serviceaccount:<namespace>:<name>), its expiry, when it was issued (zero for a token with
// no iat), the UID of the pod it is bound to ("" for a token bound to no pod, such as one
// `kubectl create token` mints), and the issuer that signed it.
type AccountClaims struct {
	Subject  string
	PodUID   string
	Expiry   time.Time
	IssuedAt time.Time
	Issuer   oidc.NamedIssuer
}

// VerifyAccount verifies raw as Verify does but accepts a token bound to no pod. Every error wraps
// one of the oidc sentinels (oidc.ErrExpired for an expired token), or is a malformed private claim.
func (k K8sPodVerifier) VerifyAccount(ctx context.Context, raw string) (AccountClaims, error) {
	claims, issuer, err := k.Issuers.Verify(ctx, raw)
	if err != nil {
		return AccountClaims{}, err
	}
	parsed, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		return AccountClaims{}, err
	}
	var registered jwt.Claims
	var private struct {
		Kubernetes struct {
			Pod struct {
				UID string `json:"uid"`
			} `json:"pod"`
		} `json:"kubernetes.io"`
	}
	// The signature was verified by the issuer's oidc.Verifier on this exact token; reading the
	// claims without a second verification is safe here and only here.
	if err := parsed.UnsafeClaimsWithoutVerification(&registered, &private); err != nil {
		return AccountClaims{}, err
	}
	var issuedAt time.Time
	if registered.IssuedAt != nil {
		issuedAt = registered.IssuedAt.Time()
	}
	return AccountClaims{Subject: claims.Subject, PodUID: private.Kubernetes.Pod.UID, Expiry: claims.Expiry, IssuedAt: issuedAt, Issuer: issuer}, nil
}

func (k K8sPodVerifier) Verify(ctx context.Context, raw string) (PodClaims, error) {
	claims, err := k.VerifyAccount(ctx, raw)
	if err != nil {
		return PodClaims{}, err
	}
	if claims.PodUID == "" {
		return PodClaims{}, fmt.Errorf("projected token is not bound to a pod")
	}
	return PodClaims{Subject: claims.Subject, PodUID: claims.PodUID}, nil
}
