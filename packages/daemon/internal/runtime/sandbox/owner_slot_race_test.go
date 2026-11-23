package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
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
