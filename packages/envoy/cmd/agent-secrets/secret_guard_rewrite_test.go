package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/policy/policytest"
	"github.com/sjawhar/envoy/internal/broker/secrets"
)

// refusedTagWrites is a secrets.Local whose every TagResource is refused, so a removal retag's
// second write, the requesters rewrite, fails after its untag landed.
type refusedTagWrites struct {
	*secrets.Local
}

func (r *refusedTagWrites) TagResource(context.Context, *secretsmanager.TagResourceInput, ...func(*secretsmanager.Options)) (*secretsmanager.TagResourceOutput, error) {
	return nil, errors.New("TagResource refused for this test")
}

// TestAFailedGuardRewriteSaysWhatTheRequestersTagHolds: a removal retag whose second write, the
// requesters rewrite, fails says what the requesters tag holds now, which is what it held before.
// Where the guard was leaving the tag (the last project tag removed), it still holds the guard,
// which only a broker from legion-envoy-v29.23.0 up to the release before the project tags reads.
// Where the guard was joining it (a secret that keeps a dispatch-project tag written without the
// guard, and a removal of another tag), the tag does not hold it yet, and a broker from before the
// project tags would serve the secret: the message must not claim the guard is there.
func TestAFailedGuardRewriteSaysWhatTheRequestersTagHolds(t *testing.T) {
	for _, c := range []struct {
		name, held, args, want, notWant string
		tags                            map[string]string
	}{
		{
			name: "the guard leaving", held: guarded("ada@example.com"), args: "--project none",
			tags: map[string]string{policy.TagProject: "PRIV"},
			want: "still holds " + policy.RequestersRollbackGuard, notWant: "does not hold",
		},
		{
			name: "the guard joining", held: "ada@example.com", args: "--max-grant none",
			tags: map[string]string{policy.TagProject: "PRIV", policy.TagMaxGrant: "1h"},
			want: "does not hold " + policy.RequestersRollbackGuard + " yet", notWant: "still holds",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			secret := policytest.Secret("API_KEY", "ada@example.com", policy.TierHuman, "v1")
			secret.Tags[policy.TagRequesters] = c.held
			for k, v := range c.tags {
				secret.Tags[k] = v
			}
			local := secrets.NewLocal(secret)
			startSecretBroker(t, servedBy(local))
			useAWS(t, &refusedTagWrites{Local: local}, testAccount, adaSignIn)
			_, stderr, code := runSecret(append([]string{"retag", "API_KEY"}, strings.Fields(c.args)...)...)
			if code != 1 || !strings.Contains(stderr, "the removal stands") {
				t.Fatalf("retag %s with the rewrite refused: exit %d stderr %q; want exit 1 and the removal standing", c.args, code, stderr)
			}
			if !strings.Contains(stderr, c.want) || strings.Contains(stderr, c.notWant) {
				t.Fatalf("retag %s with the rewrite refused said %q; want it to say %q and not %q", c.args, stderr, c.want, c.notWant)
			}
		})
	}
}
