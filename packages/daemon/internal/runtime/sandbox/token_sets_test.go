package sandbox

import (
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/claim"
)

// withARoleOutsideTheSets gives claim.Roles, for t's life, one role more that appauth.SetFor keeps
// no token set for, and answers it: the one way a pod's kind holds a role with no set is a claim
// role Legion added before giving it one, since an issue pod's roles are claim.Roles
// (issuePod.roles). Call it before the test builds its rig: cleanups run last-first, so the rig
// has stopped everything it started before claim.Roles is put back. A test that calls it never
// runs in parallel.
func withARoleOutsideTheSets(t *testing.T) claim.Role {
	t.Helper()
	held := claim.Roles
	t.Cleanup(func() { claim.Roles = held })
	const role claim.Role = "deployer"
	claim.Roles = append(slices.Clone(held), role)
	return role
}

// A pod whose kind holds a role with no token set is never built, and no role of it starts: its
// launch is refused with appauth.ErrNoTokenSet naming that role before anything of the cluster is
// touched (prepare, podTokenSets). A pod built without that role's credential would start the
// role with none, and nothing would say why.
func TestAPodWithARoleOutsideTheSetsIsNotBuilt(t *testing.T) {
	role := withARoleOutsideTheSets(t)
	g := newRig(t, nil)
	spec := workerSpec(t)
	g.clearActions()

	_, err := g.r.Spawn(g.ctx, spec)
	if !errors.Is(err, appauth.ErrNoTokenSet) || !strings.Contains(err.Error(), `"`+string(role)+`"`) {
		t.Fatalf("Spawn = %v, want appauth.ErrNoTokenSet naming the role %s", err, role)
	}
	if written := g.writes(); len(written) != 0 {
		t.Errorf("the refused launch wrote %v, want nothing of the cluster touched", written)
	}
	if s := g.sandbox(SandboxName(spec.Claim)); s != nil {
		t.Errorf("the refused launch left the Sandbox %s, want none built", s.Name)
	}
}

// A pod already running whose kind now holds a role with no token set is no pod a launch hands
// now, so the drift check reads it as moved and names the refusal: the pod is not kept running a
// role with no credential, and its next launch is refused
// (TestAPodWithARoleOutsideTheSetsIsNotBuilt). The pod projects every directory the daemon
// serves, so nothing else of it has moved.
func TestAPodWhoseKindHoldsARoleOutsideTheSetsHasMoved(t *testing.T) {
	role := withARoleOutsideTheSets(t)
	g := newRig(t, nil)
	pod := &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
		Name:         roleVolume(ghVolume, claim.RoleTester),
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{Items: ghItemsFor(staticDirs...)}},
	}}}}

	moved, ok := g.r.movedGhDirs(pod, claim.RoleTester)
	if !ok || !strings.Contains(moved.String(), `"`+string(role)+`"`) {
		t.Errorf("movedGhDirs = %t, %q; want the pod moved, naming the role %s that has no token set", ok, moved.String(), role)
	}
}
