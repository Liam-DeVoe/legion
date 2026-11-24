//go:build e2e

// The shared clone live proof's driver (scripts/e2e/shared-clone-kind.sh, which builds it with
// `-tags e2e`, owns the throwaway kind cluster, and reads the issue pod's clone and feed with
// kubectl). Its legion.yaml is read by the daemon's own loader, and the Agent Sandbox runtime the
// daemon's own sandboxOptions builds launches one issue's root on a public repository of some
// tens of megabytes, sharedCloneLiveRepository, through Agent Sandbox's controller and the gvisor
// runtime class, as the Burstable and share volumes proofs launch theirs
// (burstable_kind_test.go builds the runtime). The pod's workspace-fetch fills the feed from
// GitHub and its workspace-init provisions the issue's clone from the feed: the code under proof.
// What the proof stands in for lies outside the clone, as the Burstable proof's does: the agent is
// a sleep stub, the store answers as the daemon's does for an issue's first launch, and the roles'
// gh files hold a placeholder token.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// sharedCloneLiveRepository is a public repository, so the pod's workspace-fetch clones it without
// GitHub asking for the placeholder provisioning token, and one large enough that a copy of its
// objects stands far above what the clone writes itself: about 27 MB on GitHub, on 2026-11-24. Its
// default branch is master. It is no repository of the runtime's cache set (burstableLiveRuntime),
// so the pod's feed comes from GitHub, as a pod's does where no cache serves its repository.
var sharedCloneLiveRepository = ghrepo.MustParse("tmux/tmux")

// TestASharedCloneReadsTheFeedInAnIssuePodLive writes the proof's legion.yaml, launches its
// issue's root on sharedCloneLiveRepository, and writes `<namespace>/<pod> <repository>` to
// LEGION_E2E_RESULT for the script, which reads the clone and the feed in the pod.
func TestASharedCloneReadsTheFeedInAnIssuePodLive(t *testing.T) {
	fixtureenv.Live(t)
	env := map[string]string{}
	for _, name := range []string{"LEGION_E2E_KUBECONFIG", "LEGION_E2E_KUBE_CONTEXT", "LEGION_E2E_IMAGE", "LEGION_E2E_STREAM_HOST", "LEGION_E2E_WORK", "LEGION_E2E_RESULT"} {
		if env[name] = os.Getenv(name); env[name] == "" {
			t.Fatalf("%s is unset; scripts/e2e/shared-clone-kind.sh sets it", name)
		}
	}
	if !strings.HasPrefix(env["LEGION_E2E_KUBE_CONTEXT"], "kind-") {
		t.Fatalf("LEGION_E2E_KUBE_CONTEXT %q is no kind cluster's context (kind-<name>); the proof runs only on the cluster its script creates", env["LEGION_E2E_KUBE_CONTEXT"])
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	path := writeBurstableLiveConfig(t, filepath.Join(env["LEGION_E2E_WORK"], "shared-clone"), "CLONE", env["LEGION_E2E_STREAM_HOST"],
		env["LEGION_E2E_IMAGE"], env["LEGION_E2E_KUBECONFIG"], env["LEGION_E2E_KUBE_CONTEXT"], "")
	cfg, err := config.LoadForValidation(path, nil)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	started := time.Now()
	pod := launchLiveRoot(ctx, t, cfg, log, sharedCloneLiveRepository, "master")
	fmt.Printf("ok launched %s/%s on %s in %s: runtimeClassName %s, node %s\n", pod.Namespace, pod.Name, sharedCloneLiveRepository,
		time.Since(started).Round(time.Second), liveString(pod.Spec.RuntimeClassName), pod.Spec.NodeName)
	if liveString(pod.Spec.RuntimeClassName) != "gvisor" {
		t.Errorf("pod %s runs under %q, want gvisor", pod.Name, liveString(pod.Spec.RuntimeClassName))
	}
	result := fmt.Sprintf("%s/%s %s\n", pod.Namespace, pod.Name, sharedCloneLiveRepository)
	if err := os.WriteFile(env["LEGION_E2E_RESULT"], []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
}
