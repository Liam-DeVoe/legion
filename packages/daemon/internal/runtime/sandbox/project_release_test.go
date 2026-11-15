package sandbox

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// heldRig is a rig whose runtime reads held as the daemon's project release mark (Options.Held).
func heldRig(t *testing.T) (*rig, *atomic.Bool) {
	t.Helper()
	held := &atomic.Bool{}
	g := newRig(t, nil, withOptions(func(o *Options) { o.Held = held.Load }))
	return g, held
}

// madePods is every write since the last clearActions that would make a pod of the project: a
// Sandbox created, or one patched to Running.
func madePods(g *rig) []string {
	var made []string
	for _, w := range g.writes() {
		switch {
		case w.verb == "create" && w.resource == "sandboxes":
			made = append(made, w.String())
		case w.verb == "patch" && w.resource == "sandboxes" && strings.Contains(w.patch, `"`+modeRunning+`"`):
			made = append(made, w.String())
		}
	}
	return made
}

// wantReleased holds err to the runtime's refusal under the project release, and the rig to no
// write that makes a pod.
func wantReleased(t *testing.T, g *rig, what string, err error) {
	t.Helper()
	if !errors.Is(err, runtime.ErrProjectReleased) {
		t.Fatalf("%s while the project release holds = %v, want runtime.ErrProjectReleased", what, err)
	}
	if made := madePods(g); len(made) != 0 {
		t.Fatalf("%s while the project release holds wrote %v, want nothing that makes a pod", what, made)
	}
}

// R7: while the daemon's project release holds every pod, the Sandbox runtime makes none, whatever
// path asks — a launch into an issue with no Sandbox, a relaunch into a Suspended one, a repository
// cache made or its pod made again, an image probe, a pod update round's replacement — and each
// refuses with runtime.ErrProjectReleased before it writes. With the mark clear the same act goes
// through.
func TestProjectReleaseHoldsEverySandboxTheRuntimeWouldMake(t *testing.T) {
	t.Run("a launch into an issue with no sandbox", func(t *testing.T) {
		g, held := heldRig(t)
		held.Store(true)
		g.clearActions()
		_, err := g.r.Spawn(g.ctx, workerSpec(t))
		wantReleased(t, g, "the launch", err)
		held.Store(false)
		if loc := g.spawn(workerSpec(t)); g.sandbox(loc.Sandbox.Name) == nil {
			t.Fatal("the launch with the mark clear made no Sandbox")
		}
	})

	t.Run("a relaunch into a suspended sandbox", func(t *testing.T) {
		g, held := heldRig(t)
		spec := workerSpec(t)
		loc := g.spawn(spec)
		name := loc.Sandbox.Name
		if err := g.r.setMode(g.ctx, g.sandbox(name), modeSuspended); err != nil {
			t.Fatal(err)
		}
		g.eventually("the suspended Sandbox's pod to go", func() bool { return g.pod(name) == nil })
		held.Store(true)
		g.clearActions()
		// The next generation of the same role, as a relaunch after the pod's suspension starts it.
		spec.Generation, spec.BootToken = 2, "boot-"+string(spec.Claim)+"-g2"
		_, err := g.r.Spawn(g.ctx, spec)
		wantReleased(t, g, "the relaunch", err)
		if mode := g.sandbox(name).mode(); mode != modeSuspended {
			t.Fatalf("the refused relaunch left the Sandbox %s, want it suspended", mode)
		}
		held.Store(false)
		if _, err := g.r.Spawn(g.ctx, spec); err != nil {
			t.Fatalf("the relaunch with the mark clear: %v", err)
		}
		if mode := g.sandbox(name).mode(); mode != modeRunning {
			t.Fatalf("the relaunch with the mark clear left the Sandbox %s, want it running", mode)
		}
	})

	t.Run("a repository cache", func(t *testing.T) {
		g, held := heldRig(t)
		held.Store(true)
		g.clearActions()
		wantReleased(t, g, "the cache", g.r.EnsureRepositoryCache(g.ctx, cacheRepo))
		held.Store(false)
		if err := g.r.EnsureRepositoryCache(g.ctx, cacheRepo); err != nil {
			t.Fatalf("the cache with the mark clear: %v", err)
		}
		if g.sandbox(CacheSandboxName(testProject, cacheRepo)) == nil {
			t.Fatal("the cache with the mark clear made no Sandbox")
		}
	})

	t.Run("a repository cache's pod made again", func(t *testing.T) {
		g, held := heldRig(t)
		if err := g.r.EnsureRepositoryCache(g.ctx, cacheRepo); err != nil {
			t.Fatal(err)
		}
		name := CacheSandboxName(testProject, cacheRepo)
		if err := g.r.setMode(g.ctx, g.sandbox(name), modeSuspended); err != nil {
			t.Fatal(err)
		}
		g.eventually("the suspended cache's pod to go", func() bool { return g.pod(name) == nil })
		held.Store(true)
		g.clearActions()
		wantReleased(t, g, "the cache pod made again", g.r.reconcileCachePod(g.ctx, g.sandbox(name), cacheRepo))
		held.Store(false)
		if err := g.r.reconcileCachePod(g.ctx, g.sandbox(name), cacheRepo); err != nil {
			t.Fatalf("the cache pod made again with the mark clear: %v", err)
		}
		if mode := g.sandbox(name).mode(); mode != modeRunning {
			t.Fatalf("the cache pod made again with the mark clear left its Sandbox %s, want it running", mode)
		}
	})

	t.Run("an image probe", func(t *testing.T) {
		g, held := heldRig(t)
		const name = "legion-probe-project-release"
		manifest := func() (*unstructured.Unstructured, error) {
			u := &unstructured.Unstructured{}
			u.SetAPIVersion(sandboxGVR.GroupVersion().String())
			u.SetKind("Sandbox")
			u.SetNamespace(testNamespace)
			u.SetName(name)
			u.SetLabels(map[string]string{labelProject: testProject})
			return u, nil
		}
		held.Store(true)
		g.clearActions()
		_, outcome, created := g.r.createProbe(g.ctx, name, "sha256:probe", manifest, time.Second)
		if created {
			t.Fatal("the probe Sandbox was created while the project release holds")
		}
		wantReleased(t, g, "the probe", outcome.Refusal)
		held.Store(false)
		if _, outcome, created := g.r.createProbe(g.ctx, name, "sha256:probe", manifest, time.Second); !created {
			t.Fatalf("the probe Sandbox with the mark clear was not created: %+v", outcome)
		}
	})

	t.Run("a pod update round's replacement", func(t *testing.T) {
		g, held := heldRig(t)
		loc := g.spawn(workerSpec(t))
		name := loc.Sandbox.Name
		known := runtime.Known{Claim: workerToken, Locator: &loc}
		held.Store(true)
		asked := false
		err := g.r.ReplacePod(g.ctx, known, func(context.Context) error {
			asked = true
			return nil
		})
		if !errors.Is(err, runtime.ErrProjectReleased) || asked {
			t.Fatalf("the replacement while the project release holds = %v (asked whether due: %t), want the refusal before anything", err, asked)
		}
		if mode := g.sandbox(name).mode(); mode != modeRunning {
			t.Fatalf("the refused replacement left the Sandbox %s, want it running", mode)
		}
		held.Store(false)
		if err := g.r.ReplacePod(g.ctx, known, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("the replacement with the mark clear: %v", err)
		}
		if mode := g.sandbox(name).mode(); mode != modeSuspended {
			t.Fatalf("the replacement with the mark clear left the Sandbox %s, want it suspended", mode)
		}
	})
}
