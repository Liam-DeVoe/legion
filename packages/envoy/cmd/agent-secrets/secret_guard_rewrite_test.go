package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
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

// raceOnTag is a secrets.Local on which another writer retags the secret API_KEY a github-app
// with no owners, a pair the broker refuses, just before the first write of the kind racing
// names: "untag" (a removal retag's removal) or "tag" (its second write, the requesters
// rewrite). failReadBackAfterTag instead makes every DescribeSecret after the first TagResource
// fail, as a read-back the network lost does.
type raceOnTag struct {
	*secrets.Local
	racing               string
	failReadBackAfterTag bool
	raced, tagged        bool
}

func (r *raceOnTag) race() {
	if r.raced {
		return
	}
	r.raced = true
	if _, err := r.Local.TagResource(context.Background(), &secretsmanager.TagResourceInput{
		SecretId: aws.String(policytest.ID("API_KEY")), Tags: secrets.TagList(map[string]string{policy.TagType: policy.TypeGitHubApp}),
	}); err != nil {
		panic(err)
	}
}

func (r *raceOnTag) TagResource(ctx context.Context, in *secretsmanager.TagResourceInput, o ...func(*secretsmanager.Options)) (*secretsmanager.TagResourceOutput, error) {
	if r.racing == "tag" {
		r.race()
	}
	r.tagged = true
	return r.Local.TagResource(ctx, in, o...)
}

func (r *raceOnTag) UntagResource(ctx context.Context, in *secretsmanager.UntagResourceInput, o ...func(*secretsmanager.Options)) (*secretsmanager.UntagResourceOutput, error) {
	if r.racing == "untag" {
		r.race()
	}
	return r.Local.UntagResource(ctx, in, o...)
}

func (r *raceOnTag) DescribeSecret(ctx context.Context, in *secretsmanager.DescribeSecretInput, o ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error) {
	if r.failReadBackAfterTag && r.tagged {
		return nil, errors.New("DescribeSecret lost for this test")
	}
	return r.Local.DescribeSecret(ctx, in, o...)
}

// TestAGuardRewriteAfterAnAWSRaceSaysWhatLanded: on AWS a tag write is made first and checked
// after (awsBackend.raceCheck), so a removal retag's two writes can fail after they landed. The
// secret keeps a dispatch-project tag whose requesters list lacks the rollback guard, and
// `--max-grant none` removes the max-grant, then writes the list with the guard after it.
//   - The rewrite raced: another writer's retag left the secret a github-app with no owners. The
//     rewrite landed: the requesters tag holds the guard, and the message says it was written,
//     never that the tag "does not hold" the guard "yet".
//   - The rewrite landed and its read-back failed: the same, with the read-back's failure.
//   - The removal raced: it landed, and the rewrite was never made, since the retag writes nothing
//     more on tags another writer changed. The message says the second write was not made, and
//     names the command that reads the tags as they now stand; the requesters tag is untouched.
func TestAGuardRewriteAfterAnAWSRaceSaysWhatLanded(t *testing.T) {
	rewrite := guarded("ada@example.com")
	for _, c := range []struct {
		name       string
		store      func(*secrets.Local) *raceOnTag
		want       []string
		requesters string
	}{
		{
			name:       "the rewrite raced",
			store:      func(l *secrets.Local) *raceOnTag { return &raceOnTag{Local: l, racing: "tag"} },
			want:       []string{"the removal stands, and API_KEY's requesters were written as " + `"` + rewrite + `"`, "this retag's write landed beside another writer's"},
			requesters: rewrite,
		},
		{
			name:       "the rewrite's read-back failed",
			store:      func(l *secrets.Local) *raceOnTag { return &raceOnTag{Local: l, failReadBackAfterTag: true} },
			want:       []string{"the removal stands, and API_KEY's requesters were written as " + `"` + rewrite + `"`, "reading its tags back failed"},
			requesters: rewrite,
		},
		{
			name:       "the removal raced",
			store:      func(l *secrets.Local) *raceOnTag { return &raceOnTag{Local: l, racing: "untag"} },
			want:       []string{"this retag's write landed beside another writer's", "its second write, API_KEY's requesters as " + `"` + rewrite + `"` + ", was not made", "agent-secrets secret show API_KEY"},
			requesters: "ada@example.com",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			secret := policytest.Secret("API_KEY", "ada@example.com", policy.TierHuman, "v1")
			secret.Tags[policy.TagRequesters] = "ada@example.com"
			secret.Tags[policy.TagProject] = "PRIV"
			secret.Tags[policy.TagMaxGrant] = "1h"
			local := secrets.NewLocal(secret)
			startSecretBroker(t, servedBy(local))
			useAWS(t, c.store(local), testAccount, adaSignIn)
			_, stderr, code := runSecret("retag", "API_KEY", "--max-grant", "none")
			if code != 1 {
				t.Fatalf("retag --max-grant none, %s: exit %d stderr %q; want 1", c.name, code, stderr)
			}
			for _, said := range c.want {
				if !strings.Contains(stderr, said) {
					t.Fatalf("retag --max-grant none, %s: stderr %q does not say %q", c.name, stderr, said)
				}
			}
			for _, claim := range []string{"does not hold " + policy.RequestersRollbackGuard + " yet", "still holds " + policy.RequestersRollbackGuard} {
				if strings.Contains(stderr, claim) {
					t.Fatalf("retag --max-grant none, %s: stderr %q claims %q, which is not what the store holds", c.name, stderr, claim)
				}
			}
			tags := secrets.TagMap(describe(t, local, "API_KEY").Tags)
			if _, kept := tags[policy.TagMaxGrant]; kept {
				t.Fatalf("tags after %s = %v; want the max-grant removed: the removal landed", c.name, tags)
			}
			if tags[policy.TagRequesters] != c.requesters {
				t.Fatalf("requesters after %s = %q; want %q", c.name, tags[policy.TagRequesters], c.requesters)
			}
		})
	}
}
