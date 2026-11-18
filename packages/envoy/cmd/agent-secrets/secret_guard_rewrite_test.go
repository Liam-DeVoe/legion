package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/smithy-go"

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
// names: "untag" (a removal retag's removal) or "tag" (a tag write: a set retag's write, or a
// removal retag's second write, the requesters rewrite). failReadBack names the kind of write
// after which every DescribeSecret fails with readBackErr (a plain error when nil), as a read-back
// the network lost, or IAM refused, does. refuseUntag refuses the removal itself, so it never
// lands.
type raceOnTag struct {
	*secrets.Local
	racing, failReadBack    string
	readBackErr             error
	refuseUntag             bool
	raced, tagged, untagged bool
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
	if r.refuseUntag {
		return nil, errors.New("UntagResource refused for this test")
	}
	if r.racing == "untag" {
		r.race()
	}
	r.untagged = true
	return r.Local.UntagResource(ctx, in, o...)
}

func (r *raceOnTag) DescribeSecret(ctx context.Context, in *secretsmanager.DescribeSecretInput, o ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error) {
	if (r.failReadBack == "tag" && r.tagged) || (r.failReadBack == "untag" && r.untagged) {
		if r.readBackErr != nil {
			return nil, r.readBackErr
		}
		return nil, errors.New("DescribeSecret lost for this test")
	}
	return r.Local.DescribeSecret(ctx, in, o...)
}

// TestAGuardRewriteAfterAnAWSRaceSaysWhatLanded: on AWS a tag write is made first and checked
// after (awsBackend.raceCheck), so a removal retag's two writes can fail after they landed. The
// secret keeps a dispatch-project tag whose requesters list lacks the rollback guard, and
// `--max-grant none` removes the max-grant, then writes the list with the guard after it.
//   - The rewrite raced, or its read-back failed: the rewrite landed. The requesters tag holds the
//     guard, and the message says it was written, never that the tag "does not hold" the guard
//     "yet".
//   - The removal raced, or its read-back failed: it landed, and the rewrite was never made, since
//     the retag writes nothing more on tags it cannot trust. The message says the second write was
//     not made and names the command that reads the tags as they now stand; the requesters tag is
//     untouched.
//   - The removal was refused: it never landed, so the message claims no landed write, the
//     max-grant is still there, and nothing asks the broker to reread.
//
// Every landed row asks the broker to reread the secret: its tags changed. After a race the
// broker refuses the github-app the other writer left with no owners (github-owners-missing), as
// its loader does, and the retag prints that refusal.
func TestAGuardRewriteAfterAnAWSRaceSaysWhatLanded(t *testing.T) {
	rewrite := guarded("ada@example.com")
	written := "the removal stands, and API_KEY's requesters were written as " + `"` + rewrite + `"`
	notMade := "its second write, API_KEY's requesters as " + `"` + rewrite + `"` + ", was not made"
	for _, c := range []struct {
		name            string
		store           func(*secrets.Local) *raceOnTag
		want, notWant   []string
		requesters      string
		removed, reread bool
	}{
		{
			name:       "the rewrite raced",
			store:      func(l *secrets.Local) *raceOnTag { return &raceOnTag{Local: l, racing: "tag"} },
			want:       []string{written, "this retag's write landed beside another writer's", "(reason=" + policy.ReasonGitHubOwnersMissing + ")"},
			requesters: rewrite, removed: true, reread: true,
		},
		{
			name:       "the rewrite's read-back failed",
			store:      func(l *secrets.Local) *raceOnTag { return &raceOnTag{Local: l, failReadBack: "tag"} },
			want:       []string{written, "reading its tags back failed"},
			requesters: rewrite, removed: true, reread: true,
		},
		{
			name:       "the removal raced",
			store:      func(l *secrets.Local) *raceOnTag { return &raceOnTag{Local: l, racing: "untag"} },
			want:       []string{"this retag's write landed beside another writer's", notMade, "agent-secrets secret show API_KEY", "(reason=" + policy.ReasonGitHubOwnersMissing + ")"},
			requesters: "ada@example.com", removed: true, reread: true,
		},
		{
			name:       "the removal's read-back failed",
			store:      func(l *secrets.Local) *raceOnTag { return &raceOnTag{Local: l, failReadBack: "untag"} },
			want:       []string{"reading its tags back failed", notMade, "agent-secrets secret show API_KEY"},
			requesters: "ada@example.com", removed: true, reread: true,
		},
		{
			name:       "the removal was refused",
			store:      func(l *secrets.Local) *raceOnTag { return &raceOnTag{Local: l, refuseUntag: true} },
			want:       []string{"UntagResource refused for this test"},
			notWant:    []string{"the removal landed", "the removal stands"},
			requesters: "ada@example.com", removed: false, reread: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			secret := policytest.Secret("API_KEY", "ada@example.com", policy.TierHuman, "v1")
			secret.Tags[policy.TagRequesters] = "ada@example.com"
			secret.Tags[policy.TagProject] = "PRIV"
			secret.Tags[policy.TagMaxGrant] = "1h"
			local := secrets.NewLocal(secret)
			broker := startSecretBroker(t, servedBy(local))
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
			for _, claim := range append([]string{"does not hold " + policy.RequestersRollbackGuard + " yet", "still holds " + policy.RequestersRollbackGuard}, c.notWant...) {
				if strings.Contains(stderr, claim) {
					t.Fatalf("retag --max-grant none, %s: stderr %q claims %q, which is not what happened", c.name, stderr, claim)
				}
			}
			tags := secrets.TagMap(describe(t, local, "API_KEY").Tags)
			if _, kept := tags[policy.TagMaxGrant]; kept == c.removed {
				t.Fatalf("tags after %s = %v; want the max-grant removed %v", c.name, tags, c.removed)
			}
			if tags[policy.TagRequesters] != c.requesters {
				t.Fatalf("requesters after %s = %q; want %q", c.name, tags[policy.TagRequesters], c.requesters)
			}
			if rereads, _ := broker.recorded(); (len(rereads) > 0) != c.reread {
				t.Fatalf("rereads after %s = %v; want a reread %v: a write that landed changed the tags", c.name, rereads, c.reread)
			}
		})
	}
}

// TestALandedRetagOfASharedSecretIsNotCalledRefused: a person's retag of a shared secret whose
// write landed and whose read-back IAM then refused (AccessDeniedException on DescribeSecret)
// was not refused, so the retag does not add that only an administrator changes a shared secret's
// owner and tier. It says the write landed and its read-back failed, and asks the broker to reread
// the secret, whose tier did change.
func TestALandedRetagOfASharedSecretIsNotCalledRefused(t *testing.T) {
	local := secrets.NewLocal(policytest.Secret("API_KEY", policy.OwnerShared, policy.TierAgent, "v1"))
	broker := startSecretBroker(t, servedBy(local))
	useAWS(t, &raceOnTag{Local: local, failReadBack: "tag", readBackErr: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform: secretsmanager:DescribeSecret"}}, testAccount, adaSignIn)
	_, stderr, code := runSecret("retag", "API_KEY", "--tier", "human")
	if code != 1 || !strings.Contains(stderr, "reading its tags back failed") {
		t.Fatalf("retag --tier human of a shared secret whose read-back IAM refused: exit %d stderr %q; want 1, saying the read-back failed", code, stderr)
	}
	if strings.Contains(stderr, sharedRetagRefused) {
		t.Fatalf("stderr %q says %q, but the write landed: nothing refused it", stderr, sharedRetagRefused)
	}
	if tier := secrets.TagMap(describe(t, local, "API_KEY").Tags)[policy.TagTier]; tier != policy.TierHuman {
		t.Fatalf("tier after the retag = %q; want %q: the write landed", tier, policy.TierHuman)
	}
	if rereads, _ := broker.recorded(); len(rereads) != 1 || rereads[0] != "API_KEY" {
		t.Fatalf("rereads = %v; want one of API_KEY: a write that landed changed the tags", rereads)
	}
}
