package launcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/procgroup"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/testbin"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// The daemon sends a copy again to a pod whose roles container restarted before any role of the
// pod registered (store's heldCopy), and the restarted container's launcher is a new process over
// the same disk. Where the first launcher's restore ran to its end, that disk holds the in-use
// record the pod's first role start wrote and the restore's record with its report, which no
// connection carried before the container restarted. So the restarted launcher refuses every
// slice of the copy sent again, the workspace stays as the first restore left it, and the new
// launcher's first connection carries the first restore's report, read off the disk: the report
// the daemon keeps the copy as the pod's own on.
func TestALauncherRestartedAfterItsRestoreEndedRefusesTheCopySentAgainAndSendsTheFirstReport(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	f.workOne()
	saved := f.bundleWorkspaceOne()
	ws := f.secondPod("ws-restarted")
	frames, err := shimwire.SplitWorkspaceBundle("restore-"+ws, bundleRelease, saved)
	if err != nil {
		t.Fatal(err)
	}

	// The first launcher, whose container is killed after its restore ended and before any
	// connection carried the report: it never had one.
	first := newBundleRestore(procgroup.NoReaper, ws)
	for _, frame := range frames {
		if got := first.receive(frame); !got.OK {
			t.Fatalf("slice %d to the first launcher answered %#v", frame.Index, got)
		}
	}
	env := mergeEnv(os.Environ(), append(append([]string{}, f.Env...), "LEGION_WORKSPACE="+ws))
	if got, err := first.apply(context.Background(), env); err != nil || !got.Restored {
		t.Fatalf("the first restore answered %+v, %v; want the saved work restored", got, err)
	}
	f.requireRestored(ws, saved)
	restored := f.workspaceState(ws)
	report := first.owed()
	if report == nil || report.ID != frames[0].ID || report.Release != bundleRelease || !report.OK {
		t.Fatalf("the first launcher owes %+v, want the restore of sequence %q of release %d that put the work back", report, frames[0].ID, bundleRelease)
	}

	// The restarted container: a launcher process of its own, sent the same copy.
	g := newSessionRig(t, Config{Workspace: ws})
	for _, frame := range frames {
		if got, ok := g.answer(frame).(shimwire.LauncherBundleResult); !ok || got.OK || got.Held || got.Error != workspaceInUseRefusal {
			t.Fatalf("slice %d to the restarted launcher answered %#v; want it refused, since a restore has run on the workspace", frame.Index, got)
		}
	}
	if g.restored == nil || g.restored.ID != report.ID || g.restored.Release != report.Release || g.restored.OK != report.OK {
		t.Fatalf("the restarted launcher reported %#v, want the first launcher's report %+v", g.restored, *report)
	}
	if again := f.workspaceState(ws); again != restored {
		t.Fatalf("after the copy was sent again the workspace is\n%s\nwant it as the first restore left it\n%s", again, restored)
	}
	// The connection carried the report, so the record that held it is gone.
	if record, ok, err := workspace.ReadRestoreRecord(ws); ok || err != nil {
		t.Fatalf("after the report was carried the restore's record is %+v, %t, %v; want none", record, ok, err)
	}
	// A role starts there on what the first restore put back, and nothing is restored again.
	marker := filepath.Join(t.TempDir(), "marker")
	command := g.childStart(1, marker, nil)
	command.Env = append(command.Env, append(f.Env, "LEGION_WORKSPACE="+ws)...)
	if result := g.start(command); !result.OK {
		t.Fatalf("the restarted launcher's start answered %#v", result)
	}
	published(t, marker)
	g.stop(1)
	if again := f.workspaceState(ws); again != restored {
		t.Fatalf("after a role started the workspace is\n%s\nwant it as the first restore left it\n%s", again, restored)
	}
	if strings.Contains(g.stderr.String(), "released workspace's saved work") {
		t.Fatalf("the restarted launcher wrote a restore notice, want none: it restored nothing\n%s", g.stderr.String())
	}
}

// One live launcher sent the same slice sequence twice — a second role start of the pod before any
// role registered, which the daemon sends the unspent copy again — answers the second from what it
// holds and applies the bundle once: the work done in the workspace after the first restore stays,
// and the daemon is sent no second report.
func TestAnUnspentCopySentTwiceToOneLiveLauncherIsAppliedOnce(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	f.workOne()
	saved := f.bundleWorkspaceOne()
	workspace := f.secondPod("ws-live")
	frames, err := shimwire.SplitWorkspaceBundle("restore-"+workspace, bundleRelease, saved)
	if err != nil {
		t.Fatal(err)
	}
	g := newSessionRig(t, Config{Workspace: workspace})
	sendAndStart := func(generation uint64) {
		t.Helper()
		for _, frame := range frames {
			got, ok := g.answer(frame).(shimwire.LauncherBundleResult)
			if !ok || !got.OK || got.Held != (frame.Index == len(frames)-1) {
				t.Fatalf("slice %d of the sequence sent for generation %d answered %#v", frame.Index, generation, got)
			}
		}
		marker := filepath.Join(t.TempDir(), "marker")
		command := g.childStart(generation, marker, nil)
		command.Env = append(command.Env, append(f.Env, "LEGION_WORKSPACE="+workspace)...)
		if result := g.start(command); !result.OK {
			t.Fatalf("the start of generation %d answered %#v", generation, result)
		}
		published(t, marker)
		g.stop(generation)
	}

	sendAndStart(1)
	if g.restored == nil || !g.restored.OK {
		t.Fatalf("the first restore reported %#v", g.restored)
	}
	f.requireRestored(workspace, saved)
	// Work in the workspace after the restore, which a second apply would `jj edit` back over.
	f.Run(workspace, "jj", "new", "-R", workspace, "-m", "work after the restore")
	f.Write(filepath.Join(workspace, "after.txt"), "after\n")
	// The read snapshots the working copy, so the file written above is in the commit it names.
	after := f.Run(workspace, "jj", "log", "-R", workspace, "-r", "@", "--no-graph", "-T", "commit_id")
	g.restored = nil

	sendAndStart(2)
	if g.restored != nil {
		t.Fatalf("the launcher reported %#v of the sequence sent again, want no second restore to report", g.restored)
	}
	if head := f.Run(workspace, "jj", "log", "-R", workspace, "--ignore-working-copy", "-r", "@", "--no-graph", "-T", "commit_id"); head != after {
		t.Fatalf("after the sequence sent again the working copy is on %s, want %s, the work after the first restore", head, after)
	}
	if body, err := os.ReadFile(filepath.Join(workspace, "after.txt")); err != nil || string(body) != "after\n" {
		t.Fatalf("the work after the first restore reads %q (%v) after the sequence sent again", body, err)
	}
}

// workspaceState is what a restore leaves in workspace, as one string a test compares: every
// commit jj shows with its description, every local bookmark with its commit, and the working
// copy's commit.
func (f *bundleFixture) workspaceState(workspace string) string {
	f.t.Helper()
	log := f.Run(workspace, "jj", "log", "-R", workspace, "--ignore-working-copy", "-r", "all()", "--no-graph",
		"-T", `commit_id ++ " " ++ description.first_line() ++ "\n"`)
	bookmarks := f.Run(workspace, "jj", "bookmark", "list", "-R", workspace, "--ignore-working-copy",
		"-T", `if(remote, "", name ++ " " ++ normal_target.commit_id() ++ "\n")`)
	head := f.Run(workspace, "jj", "log", "-R", workspace, "--ignore-working-copy", "-r", "@", "--no-graph", "-T", "commit_id")
	return "commits:\n" + log + "bookmarks:\n" + bookmarks + "working copy: " + head
}
