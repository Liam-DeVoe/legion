package sandbox

// R7 of dispatch://INFRA-3271 (spec v20, verification v15's c-forced-replacement-told), the launch's
// side: a launch that replaces an issue pod on its own account takes a disk that may hold work no
// role pushed, so it has the pod's launcher read the workspace first where a process in the pod can
// make the read, and once the suspension that takes the pod is written it hands the daemon what it
// took (Options.LaunchTook, runtime.TakenPod), which tells the issue's architect once (the daemon's
// tellLaunchTook, internal/daemon's launch_took_test.go). One test each for a moved facet
// (movedInPod), a superseded disk shape and a kept pod whose stored template cannot be read, each
// with another role running in the pod and a workspace that holds one commit origin does not hold:
// the pod is replaced and the daemon is handed the commit. A pod whose launchers are not bound, and
// a pod that is not live: the pod is replaced, no read is sent, and the daemon is handed why no read
// could be made. Each fails with the hand-off taken out.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// tookCommit is the one commit origin does not hold that the workspace of each pod here holds, and
// oneCommitRead the launcher's answer that names it.
const tookCommit = "f2f134541b30dfa5160a5d1ce5e17b76c967c661"

var oneCommitRead = shimwire.WorkspaceWorkResult{OK: true, Head: "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d", Count: 1, Commits: []string{tookCommit}}

// tookLaunches is a LaunchTook that keeps every TakenPod the runtime hands it, as the daemon's
// tellLaunchTook takes them.
type tookLaunches struct {
	mu    sync.Mutex
	taken []runtime.TakenPod
}

func (l *tookLaunches) record(_ context.Context, taken runtime.TakenPod) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.taken = append(l.taken, taken)
}

func (l *tookLaunches) all() []runtime.TakenPod {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.taken)
}

// wantRead holds the runtime to having handed the daemon one replacement of pod, why the launch
// made it, and the read through the pod's launcher that found tookCommit.
func (l *tookLaunches) wantRead(t *testing.T, pod string) {
	t.Helper()
	taken := l.all()
	if len(taken) != 1 {
		t.Fatalf("the daemon was handed %d replacements %+v, want the one this launch made", len(taken), taken)
	}
	got := taken[0]
	if got.PodUID != pod || got.Issue != testTree || got.Claim == "" || got.Why == "" || got.Err != nil ||
		got.Work.Count != 1 || !slices.Contains(got.Work.Commits, tookCommit) {
		t.Fatalf("the daemon was handed %+v, want the replacement of pod %s with why it was made and the read that found the commit %s", got, pod, tookCommit)
	}
}

// wantUnread holds the runtime to having handed the daemon one replacement of pod, why the launch
// made it, and why no read could be made, which names because, with no work.
func (l *tookLaunches) wantUnread(t *testing.T, pod, because string) {
	t.Helper()
	taken := l.all()
	if len(taken) != 1 {
		t.Fatalf("the daemon was handed %d replacements %+v, want the one this launch made", len(taken), taken)
	}
	got := taken[0]
	if got.PodUID != pod || got.Issue != testTree || got.Why == "" || got.Err == nil || !strings.Contains(got.Err.Error(), because) || got.Work.HoldsWork() {
		t.Fatalf("the daemon was handed %+v, want the replacement of pod %s with why no read could be made (%q)", got, pod, because)
	}
}

// noReadSent holds every launcher of tokens to having been sent no workspace read.
func noReadSent(t *testing.T, g *rig, tokens ...claim.Token) {
	t.Helper()
	for _, token := range tokens {
		for _, frame := range g.sent(token) {
			if _, ok := frame.(shimwire.WorkspaceWork); ok {
				t.Fatalf("the launcher of %s was sent a workspace read, want none: no process of the pod could make it", token)
			}
		}
	}
}

// The launch replaces a pod whose launchers dial a stream this daemon does not serve (movedInPod):
// no worker in it can be asked to push its work first. The reviewer runs in the pod beside the
// tester's own process. The pod's launcher reads the workspace before the replacement, and the
// daemon is handed the commit it found.
func TestALaunchThatReplacesAPodWhoseStreamMovedSaysWhatItTook(t *testing.T) {
	var took tookLaunches
	g := newRig(t, nil, withWorkspaceRead(oneCommitRead))
	loc := g.spawn(workerSpec(t))
	r2 := restartedOver(t, g, loc, func(o *Options) {
		moveStream(o)
		o.LaunchTook = took.record
	})
	g.connectRunning(otherToken, 1)
	fresh := resumeTester(t, g, r2, loc)
	if fresh.Sandbox.PodUID == loc.Sandbox.PodUID {
		t.Fatalf("Resume kept the pod %s, whose launchers dial a stream this daemon does not serve", loc.Sandbox.PodUID)
	}
	took.wantRead(t, loc.Sandbox.PodUID)
}

// The launch replaces an issue Sandbox of the disk shape before the pod-owned disk, Sandbox and pod
// together (supersededDisk), with the reviewer running in that pod. The pod's launcher reads the
// workspace first, and the daemon is handed the commit it found.
func TestALaunchThatReplacesASandboxOfTheSupersededDiskShapeSaysWhatItTook(t *testing.T) {
	var took tookLaunches
	g := supersededIssue(t, withWorkspaceRead(oneCommitRead), withOptions(func(o *Options) { o.LaunchTook = took.record }))
	g.connectRunning(otherToken, 1)
	loc := g.spawn(workerSpec(t))
	if loc.Sandbox.PodUID == "uid-pod-old" {
		t.Fatalf("the launch kept the pod uid-pod-old of the superseded disk shape, want a pod of the Sandbox it created")
	}
	took.wantRead(t, "uid-pod-old")
}

// The launch replaces a drifted pod whose stored template says nothing this daemon can read of what
// the pod carries (keptBy), with the reviewer running in it. The pod's launcher reads the workspace
// first, and the daemon is handed the commit it found.
func TestALaunchThatCannotReadAKeptPodsTemplateSaysWhatItTook(t *testing.T) {
	var took tookLaunches
	g := newRig(t, nil, withWorkspaceRead(oneCommitRead))
	loc := g.spawn(workerSpec(t))
	r2 := restartedOver(t, g, loc, func(o *Options) {
		withBrokerAndOverlay(o)
		o.LaunchTook = took.record
	})
	g.connectRunning(otherToken, 1)
	if drifted := driftOf(t, g, r2, loc); len(drifted) == 0 {
		t.Fatalf("the pod has drifted from nothing, so this case proves nothing")
	}
	forgetPodContainers(t, g, r2, loc.Sandbox.Name)
	fresh := resumeTester(t, g, r2, loc)
	if fresh.Sandbox.PodUID == loc.Sandbox.PodUID {
		t.Fatalf("Resume kept the pod %s, whose stored template says nothing of what it was built with", loc.Sandbox.PodUID)
	}
	took.wantRead(t, loc.Sandbox.PodUID)
}

// The reviewer's start replaces the pod the tester runs in, because the reviewer's launcher Secret
// binds another pod (launcherBound). No launcher of the pod is bound to it, so no read is sent, and
// the daemon is handed that no read could be made.
func TestALaunchIntoAPodWithUnboundLaunchersSaysNoReadCouldBeMade(t *testing.T) {
	var took tookLaunches
	g := newRig(t, nil, withWorkspaceRead(oneCommitRead), withOptions(func(o *Options) { o.LaunchTook = took.record }))
	testerLoc := g.spawn(workerSpec(t))
	editSecret(g, roleSecretName(testerLoc.Sandbox.Name, claim.RoleReviewer), func(s *corev1.Secret) {
		s.Annotations[launcherPodUIDAnnotation] = "uid-another-pod"
	})
	reviewerLoc := g.spawn(testSpec(t, otherToken, claim.RoleReviewer, testTree))
	if reviewerLoc.Sandbox.PodUID == testerLoc.Sandbox.PodUID {
		t.Fatalf("the reviewer's start kept the pod %s, whose reviewer launcher binds another pod", testerLoc.Sandbox.PodUID)
	}
	took.wantUnread(t, testerLoc.Sandbox.PodUID, "not bound")
	noReadSent(t, g, workerToken, otherToken)
}

// The death path's Resume over a Failed issue pod its Sandbox still runs: the pod is not live, so
// no process in it can read the workspace, and the daemon is handed that no read could be made.
func TestALaunchOverAPodThatIsNotLiveSaysNoReadCouldBeMade(t *testing.T) {
	var took tookLaunches
	name := SandboxName(workerToken)
	labels := claimLabels(claim.RoleTester)
	oldSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName(name), Namespace: testNamespace, OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "agents.x-k8s.io/v1beta1", Kind: "Sandbox", Name: name, UID: "uid-sandbox-kept",
		}}},
		Data: map[string][]byte{provisionTokenKey: []byte("ghs_old")},
	}
	g := newRig(t, []k8sruntime.Object{
		sandboxObject(t, name, "uid-sandbox-kept", modeRunning, labels),
		podObject(name, "uid-pod-dead", "uid-sandbox-kept", labels, corev1.PodStatus{
			Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{terminated(workerContainer, 137, "Error")},
		}),
		oldSecret,
	}, withWorkspaceRead(oneCommitRead), withOptions(func(o *Options) { o.LaunchTook = took.record }))
	g.suspendDelay.Store(int64(50 * time.Millisecond))
	g.launcher(workerToken)
	spec := workerSpec(t)
	spec.Generation, spec.BootToken, spec.ResumeSessionFile = 2, "boot-g2", resumeSession
	dead := sandboxLocator(workerToken, "uid-pod-dead")
	loc, err := g.r.Resume(g.ctx, &dead, spec)
	if err != nil {
		t.Fatal(err)
	}
	if loc.Sandbox.PodUID == "uid-pod-dead" {
		t.Fatalf("the Resume kept the failed pod uid-pod-dead")
	}
	took.wantUnread(t, "uid-pod-dead", "not live")
	noReadSent(t, g, workerToken)
}
