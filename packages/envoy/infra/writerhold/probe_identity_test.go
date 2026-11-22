package writerhold

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/streams"
	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
	"github.com/sjawhar/envoy/natsgrant"
)

// The hub-restore case of round 4's H-1 (shape B): a target restored onto another disk, and then
// the probe's credential file rewritten with a token for the same identity, as the kubelet rewrites
// a projected token at 80% of its life and as the auth service's one-hour cap on a service token
// forces; the stack's auth service admits it as a real one admits a rewritten token. `hold prove`
// counts each restarted server's refusal of the rewritten token, since its signature verifies
// against the recorded issuer's keys for the recorded subject and audience, and completes; the
// census, its cutover row written, completes; and `hold release` ends the hold. Control:
// 9793f8e294, whose check took the whole token's hash alone, as 449251ffc3's did: the prove exited
// 2, "(b)", and no command ended the hold (abort refused the restored target, release the record).
func TestHoldAProveAfterTheProbeTokenIsRewrittenCompletes(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	ctx := context.Background()
	id := h.hubRestoreHold()
	h.disks.Remake(0)
	rewritten := h.s.MintProbeToken(holdtest.ProbeAccount, []string{holdtest.ProbeAudience}, time.Now().Add(time.Hour))
	h.s.AdmitToken(rewritten, holdtest.ProbeAccount)
	h.probeWith(rewritten)

	code, _, stderr := h.run(actionProve, "--hold", id, "--restored", holdtest.Claim(0)+"=pvc-"+holdtest.Pod(0))
	require.Zero(t, code, "the prove after the probe's token is rewritten: %s", stderr)
	require.Equal(t, holdrecord.StateDone, latestState(h.journal(id), holdrecord.CommandProve))

	_, err := h.s.Records.PutCutover(ctx, "session", "done", id)
	require.NoError(t, err)
	recorded := func(context.Context, *nats.Conn) ([]streams.ConsumerShape, error) { return nil, nil }
	names := func(context.Context, *nats.Conn, DaemonDeployments, []string, string, []string) (FreshNamesRead, error) {
		return FreshNamesRead{Names: map[string][]string{"session": {}}}, nil
	}
	code, stdout, stderr := h.streamsRunReaders(CensusReaders{Recorded: recorded, FreshNames: names, Stores: noStores}, "census", "--hold", id)
	require.Zero(t, code, "the census after the prove: %s%s", stdout, stderr)

	code, stdout, stderr = h.run(actionRelease, "--hold", id, "--reason", "the hub restore is done")
	require.Zero(t, code, "release after the census: %s%s", stdout, stderr)
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
}

// A token other than the recorded one counts a refusal as the hold's only when it proves the
// recorded probe identity: internal/oidc's Verifier, the one both of the auth service's token
// readers wrap, checks the same issuer, audience, signature and expiry the auth service does,
// against the recorded issuer's published keys, which the operator reads from the issuer's
// discovery document and key set with no credential; its subject and audience are the recorded
// ones; and it lapses after now plus the probe's timeout. A prove whose probe holds a token for
// another subject, for another audience, from another issuer, or under the issuer's key id with a
// signature that key did not make, is UNKNOWN naming (b) and the failed check, and one that lapses
// within the timeout is UNKNOWN naming (c). With the issuer's discovery document or key set
// unreachable a rewrite is UNKNOWN (b), never a pass. Each writes nothing and the row stays held.
// The stack's own token and a rewrite of the same identity count. Control: 9793f8e294's whole-token
// hash (449251ffc3's check), which refused the rewrite and named none of the identity's checks.
func TestHoldAProveCountsARewrittenTokenOnlyForTheRecordedIdentity(t *testing.T) {
	type token func(h *heldStack) string
	identity := func(subject, audience string, life time.Duration) token {
		return func(h *heldStack) string {
			return h.s.MintProbeToken(subject, []string{audience}, time.Now().Add(life))
		}
	}
	for _, row := range []struct {
		name  string
		token token
		why   []string
	}{
		{"the token hold set admitted", func(h *heldStack) string { return h.s.ProbeToken() }, nil},
		{"a rewrite of the same identity", identity(holdtest.ProbeAccount, holdtest.ProbeAudience, time.Hour), nil},
		{"another subject", identity("system:serviceaccount:envoy-operator:another-probe", holdtest.ProbeAudience, time.Hour),
			[]string{"(b)", "system:serviceaccount:envoy-operator:another-probe", "subject"}},
		{"another audience", identity(holdtest.ProbeAccount, "another-audience", time.Hour), []string{"(b)", "audience"}},
		{"another issuer", func(h *heldStack) string {
			other := oidctest.New(h.t)
			key := other.PublishKey(h.t, "another-issuers-key")
			return other.Mint(h.t, key, map[string]any{"iss": other.URL(), "sub": holdtest.ProbeAccount, "aud": []string{holdtest.ProbeAudience},
				"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix()})
		}, []string{"(b)", "issuer"}},
		{"a signature the issuer's key did not make", func(h *heldStack) string {
			forged := oidctest.NewKey(h.t, "holdtest-probe-key")
			return h.s.ProbeIssuer.Mint(h.t, forged, map[string]any{"iss": h.s.ProbeIssuer.URL(), "sub": holdtest.ProbeAccount,
				"aud": []string{holdtest.ProbeAudience}, "iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix()})
		}, []string{"(b)", "signature"}},
		{"a rewrite that lapses within the probe's timeout", identity(holdtest.ProbeAccount, holdtest.ProbeAudience, ProbeTimeout/2), []string{"(c)"}},
		{"a rewrite while the issuer's discovery document cannot be read", func(h *heldStack) string {
			h.s.ProbeIssuer.FailDiscovery(http.StatusServiceUnavailable)
			return h.s.MintProbeToken(holdtest.ProbeAccount, []string{holdtest.ProbeAudience}, time.Now().Add(time.Hour))
		}, []string{"(b)", "published keys"}},
		{"a rewrite while the issuer's key set cannot be read", func(h *heldStack) string {
			h.s.ProbeIssuer.FailKeys(http.StatusServiceUnavailable)
			return h.s.MintProbeToken(holdtest.ProbeAccount, []string{holdtest.ProbeAudience}, time.Now().Add(time.Hour))
		}, []string{"(b)"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHeldStack(t)
			h.seedProtectedStreams()
			id := h.hubRestoreHold()
			h.disks.Remake(0)
			raw := row.token(h)
			h.s.AdmitToken(raw, holdtest.ProbeAccount)
			h.probeWith(raw)
			before := h.journalCommands(id)
			code, _, stderr := h.run(actionProve, "--hold", id, "--restored", holdtest.Claim(0)+"=pvc-"+holdtest.Pod(0))
			if row.why == nil {
				require.Zero(t, code, "the prove with %s: %s", row.name, stderr)
				return
			}
			require.Equal(t, 2, code, "the prove with %s: %s", row.name, stderr)
			for _, want := range row.why {
				require.Contains(t, stderr, want)
			}
			require.Equal(t, before, h.journalCommands(id), "the refused prove wrote an entry")
			require.Equal(t, natsgrant.OperatorPhaseHeld, h.row().Phase)
		})
	}
}
