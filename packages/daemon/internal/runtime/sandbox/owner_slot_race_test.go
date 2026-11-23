package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// A launch of the owner into the pod it keeps reads the owner slot's Secret, which holds a lease the
// daemon has since re-minted, so the launch has a write to make, and mints the bundle of the role it
// judged, the architect's own; in that mint the owner's step starts, and the outbox writes the slot
// for the step's App. The launch's write, made on the Secret it read before, is refused as the API
// server refuses it, since the Secret's resourceVersion moved, and the launch writes again on a
// fresh read. The slot ends on the step's App, and the launch's start frame waits for the token the
// slot holds: the launch judges the role on each read of the Secret it writes, so a role judged
// before the step's write never lands after it. The control is the same launch with no step
// starting in its mint, which writes the architect's own.
func TestALaunchOvertakenByAStepsWriteLeavesTheSlotOnTheStepsApp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		starts bool
	}{
		{name: "control: no step starts in the launch's mint"},
		{name: "the owner's step starts in the launch's mint", starts: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var acting atomic.Value
			acting.Store(claim.Role(""))
			var g *rig
			var launching, landing, landed, refused atomic.Bool
			g = newRig(t, nil, withOptions(func(o *Options) {
				o.ActsAs = func(_ context.Context, token claim.Token) (claim.Role, bool, error) {
					if role := acting.Load().(claim.Role); token == rootToken && role != "" {
						return role, true, nil
					}
					return "", false, nil
				}
				o.GitHubCredential = func(ctx context.Context, issue string, role claim.Role, repo ghrepo.Repository) (ghconfig.Bundle, error) {
					if tc.starts && launching.Load() && role == claim.RoleArchitect && landing.CompareAndSwap(false, true) {
						acting.Store(claim.RoleImplementer)
						if err := g.r.WriteSlotCredential(ctx, rootToken); err != nil {
							t.Errorf("the step's WriteSlotCredential of the owner's slot = %v", err)
						}
						landed.Store(true)
					}
					return staticCredential(ctx, issue, role, repo)
				}
			}))
			g.spawn(rootSpec(t))
			issue := SandboxName(rootToken)
			slot := roleSecretName(issue, claim.RoleArchitect)
			g.store.mu.Lock()
			g.store.claims = append(g.store.claims, supervise.Claim{Token: rootToken, Project: testProject, Tree: testTree, Issue: testTree, Role: claim.RoleArchitect})
			g.store.mu.Unlock()
			editSecret(g, slot, func(s *corev1.Secret) { s.Data[GitHubHostsKey("sjawhar")] = []byte(ghconfig.Hosts("ghs_stale_lease")) })
			// The API server refuses a write of the owner slot's Secret read before the step's
			// write, as its resourceVersion says: here the launch's first write.
			g.kube.PrependReactor("update", "secrets", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
				secret := a.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
				if secret.Name == slot && launching.Load() && landed.Load() && refused.CompareAndSwap(false, true) {
					return true, nil, apierrors.NewConflict(corev1.Resource("secrets"), slot, errors.New("the object has been modified"))
				}
				return false, nil, nil
			})
			before := g.pod(issue).UID
			again := rootSpec(t)
			again.Generation++
			launching.Store(true)
			g.spawn(again)
			launching.Store(false)
			if after := g.pod(issue).UID; after != before {
				t.Fatalf("the owner's launch replaced the pod (%s, then %s), want it kept", before, after)
			}
			want := staticCredentialToken(claim.RoleArchitect)
			if tc.starts {
				if !landed.Load() || !refused.Load() {
					t.Fatalf("the step's write landed %t and the launch's write was refused %t: the case's premise, a launch write the step's overtook", landed.Load(), refused.Load())
				}
				want = staticCredentialToken(claim.RoleImplementer)
			}
			token, err := ghconfig.TokenFromHosts(g.secret(slot).Data[GitHubHostsKey("sjawhar")])
			if err != nil {
				t.Fatalf("the owner slot's hosts.yml: %v", err)
			}
			if token != want {
				t.Fatalf("after the owner's launch its slot holds %q, want %q: the launch's write, judged before the step started, landed after the step's", token, want)
			}
			sum := sha256.Sum256([]byte(want))
			if start := lastStart(t, g, rootToken); start.GitHubCredential == nil || start.GitHubCredential.TokenDigest != hex.EncodeToString(sum[:]) {
				t.Fatalf("the owner's start frame waits for the credential %+v, want the digest of %q, the token its slot holds", start.GitHubCredential, want)
			}
		})
	}
}

// retroSteps is the plan rows of task a written as words (`role` or `role:key`), each of its
// built-in role's type.
func retroSteps(words ...string) []record.PlanStep {
	roles := plan.BuiltinRoles()
	steps := make([]record.PlanStep, 0, len(words))
	for i, word := range words {
		role, key, _ := strings.Cut(word, ":")
		steps = append(steps, record.PlanStep{Task: "a", Position: i + 1, Role: role, Type: roles[role].Type, AgentKey: key})
	}
	return steps
}

// The owner's retro of a task whose last step of the type implementation is the owner's is done as
// that step's role, and every write of the owner's slot judges that on the Secret it writes: the
// rig's ActsAs is the daemon's rule (record.OwnerActsAs) over the issue as the record would read
// it, the task's plan rows `implementer:owner reviewer`. While the issue stands at the reviewer's
// step the owner's slot holds the review App, its own. At the retro the outbox's write puts the
// implement App in the slot's Secret; a refresh tick during the retro leaves it there, and so does
// a launch of the owner into the pod it keeps, whose start frame waits for the implement App's
// token; once the retro ended and the issue stands in merging, the retro's write-back puts the
// review App back.
//
// The controls: at the retro of `acceptance:owner` alone, which the owner does as itself, and at the
// retro of `implementer reviewer`, which its implementer does, the same write, tick and launch
// leave the owner's slot on the review App.
func TestTheOwnersRetroOfItsOwnImplementationHoldsTheImplementApp(t *testing.T) {
	implement, review := staticCredentialToken(claim.RoleImplementer), staticCredentialToken(claim.RoleArchitect)
	for _, tc := range []struct {
		name  string
		steps []record.PlanStep
		// placeRole is the role of the claim that works the retro (record.Issue.PlaceRole).
		placeRole claim.Role
		want      string
	}{
		{name: "implementer:owner reviewer", steps: retroSteps("implementer:owner", "reviewer"), placeRole: claim.RoleArchitect, want: implement},
		{name: "control: acceptance:owner alone", steps: retroSteps("acceptance:owner"), placeRole: claim.RoleArchitect, want: review},
		{name: "control: the retro of a fresh implementer", steps: retroSteps("implementer", "reviewer"), placeRole: claim.RoleImplementer, want: review},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var issue atomic.Pointer[record.Issue]
			issue.Store(&record.Issue{Key: testTree, Phase: phase.Reviewing, PlaceRole: claim.RoleReviewer, StepRole: claim.RoleReviewer})
			g := newRig(t, nil, withOptions(func(o *Options) {
				o.ActsAs = func(_ context.Context, token claim.Token) (claim.Role, bool, error) {
					if token != rootToken {
						return "", false, nil
					}
					role, acting := record.OwnerActsAs(*issue.Load(), tc.steps)
					return role, acting, nil
				}
			}))
			g.spawn(rootSpec(t))
			storeOwner(g)
			sandbox := SandboxName(rootToken)
			before := g.pod(sandbox).UID
			if got := slotToken(t, g, sandbox, claim.RoleArchitect); got != review {
				t.Fatalf("the owner's slot holds %q at the reviewer's step, want %q", got, review)
			}

			issue.Store(&record.Issue{Key: testTree, Phase: phase.Retro, PlaceRole: tc.placeRole})
			if err := g.r.WriteSlotCredential(g.ctx, rootToken); err != nil {
				t.Fatalf("WriteSlotCredential of the owner's slot at the retro = %v", err)
			}
			if got := slotToken(t, g, sandbox, claim.RoleArchitect); got != tc.want {
				t.Fatalf("the owner's slot holds %q at the retro once written, want %q", got, tc.want)
			}
			g.r.refreshGitHubCredentials(g.ctx)
			if got := slotToken(t, g, sandbox, claim.RoleArchitect); got != tc.want {
				t.Fatalf("the owner's slot holds %q after a refresh tick during the retro, want %q", got, tc.want)
			}
			again := rootSpec(t)
			again.Generation++
			g.spawn(again)
			if after := g.pod(sandbox).UID; after != before {
				t.Fatalf("the owner's launch during the retro replaced the pod (%s, then %s), want it kept", before, after)
			}
			if got := slotToken(t, g, sandbox, claim.RoleArchitect); got != tc.want {
				t.Fatalf("the owner's slot holds %q after its launch during the retro, want %q", got, tc.want)
			}
			sum := sha256.Sum256([]byte(tc.want))
			if start := lastStart(t, g, rootToken); start.GitHubCredential == nil || start.GitHubCredential.TokenDigest != hex.EncodeToString(sum[:]) {
				t.Fatalf("the owner's start frame during the retro waits for the credential %+v, want the digest of %q", start.GitHubCredential, tc.want)
			}

			issue.Store(&record.Issue{Key: testTree, Phase: phase.Merging})
			if err := g.r.WriteSlotCredential(g.ctx, rootToken); err != nil {
				t.Fatalf("the retro's write-back of the owner's slot = %v", err)
			}
			if got := slotToken(t, g, sandbox, claim.RoleArchitect); got != review {
				t.Fatalf("the owner's slot holds %q after the retro's write-back in merging, want %q", got, review)
			}
		})
	}
}
