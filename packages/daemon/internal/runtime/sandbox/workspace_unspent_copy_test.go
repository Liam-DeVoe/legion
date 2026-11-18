package sandbox

import (
	"bytes"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// A pod whose launcher is ended after the copy was sent and before it reported, with no role of
// the pod registered, leaves the work for the next start of the issue: its restarted roles
// container is sent the copy again, and so is the pod after it, each over a workspace no role has
// touched. Once a role of a pod the copy was sent to registers, that role may change the
// workspace, and neither that pod's restarted container nor the pod after it is sent the copy.
// The runtime's restore hop runs over the real store, and the registration is the store's own
// record of it, which the daemon makes before it answers a registration.
func TestAnUnspentCopyComesBackAfterAnUnregisteredPodEndsAndNotOnceARoleRegistered(t *testing.T) {
	st := migratedStore(t)
	g := newRig(t, nil, withSavedWorkspace(st))
	spec := workerSpec(t)
	// The issue holds nothing at its pod's first role start, and the copy of the pod before that
	// one is written after it, so the first pod the copy is sent to is the one after.
	g.spawn(spec)
	if frames := g.sent(spec.Claim); countOf(frames, shimwire.TypeLauncherBundle) != 0 {
		t.Fatalf("the first pod was sent %v, want no slice: the issue held nothing", frameTypes(frames))
	}
	taken := runningWorkspaceCopy("pod-one")
	saveRunningCopy(t, st, taken)
	requireTheWork := func(who string, read shimwire.WorkspaceBundle) {
		t.Helper()
		if want := sentBookmarks(taken); !bytes.Equal(read.Bundle, taken.Bundle) || read.Head != taken.Head ||
			!slices.Equal(read.Tips, taken.Tips) || !maps.Equal(read.Bookmarks, want) {
			t.Fatalf("%s was sent %d bytes at head %s with tips %v and bookmarks %v, want pod-one's %d at %s with %v and %v",
				who, len(read.Bundle), read.Head, read.Tips, read.Bookmarks, len(taken.Bundle), taken.Head, taken.Tips, want)
		}
	}

	// The restore hop alone: the copy reaches the launcher whole, and no start follows to apply
	// it and report, which is a launcher ended between the two.
	three := secondEnd(t, g, spec, time.Now().UTC())
	requireTheWork("the pod after the first", sentTo(t, g, spec, three))
	g.connect(spec.Claim)
	requireTheWork("that pod's restarted container", sentTo(t, g, spec, three))
	four := secondEnd(t, g, spec, time.Now().UTC())
	requireTheWork("the pod after the one that ended unregistered", sentTo(t, g, spec, four))

	// A role of that pod registers before its launcher ends.
	if err := st.RecordPodRegistration(g.ctx, testTree, four); err != nil {
		t.Fatal(err)
	}
	sentNothing := func(who, pod string) {
		t.Helper()
		before := len(g.sent(spec.Claim))
		g.r.launchers.restoreWorkspace(g.ctx, spec.Claim, pod)
		for _, frame := range g.sent(spec.Claim)[before:] {
			if slice, ok := frame.(shimwire.LauncherBundle); ok {
				t.Fatalf("%s was sent slice %d of %d of copy %d, want nothing: a role of the pod the copy was sent to registered",
					who, slice.Index, slice.Count, slice.Release)
			}
		}
	}
	g.connect(spec.Claim)
	sentNothing("that pod's restarted container", four)
	five := secondEnd(t, g, spec, time.Now().UTC())
	sentNothing("the pod after it", five)
	if restore, err := st.TakeWorkspaceCopyForRestore(g.ctx, testTree, "pod-six"); err != nil || restore.Running.Take != runtime.WorkspaceBundleNone {
		t.Fatalf("the store answered a later pod %v (%v), want nothing", restore.Running.Take, err)
	}
}
