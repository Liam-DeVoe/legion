package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/procgroup"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/shim"
	"github.com/sjawhar/legion/daemon/internal/shim/shimtest"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/testbin"
	"github.com/sjawhar/legion/daemon/internal/testwait"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// The daemon sends a released workspace's bundle in slices under the frame limit, and the
// launcher, which reassembles nothing of its own, puts them back together in memory: a repeated
// slice is answered as a repeated launcher_start id is, and a sequence that did not arrive as it
// was sent reaches no workspace at all.
func TestTheLauncherReassemblesABundleAndRefusesOneThatDidNotArriveAsItWasSent(t *testing.T) {
	saved := manySlices()
	frames, err := shimwire.SplitWorkspaceBundle("restore-1", 11, saved)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) < 3 {
		t.Fatalf("the fixture sliced into %d frames; the test needs several", len(frames))
	}

	t.Run("a whole sequence is held", func(t *testing.T) {
		r := newBundleRestore(procgroup.NoReaper, newWorkspace(t))
		for index, frame := range frames {
			got := r.receive(frame)
			if !got.OK || got.ID != frames[0].ID || got.Index != index {
				t.Fatalf("slice %d answered %#v", index, got)
			}
			if want := index == len(frames)-1; got.Held != want {
				t.Fatalf("slice %d of %d answered held=%v, want %v: the sequence is over at its last slice",
					index, len(frames), got.Held, want)
			}
		}
		read, err := shimwire.ParseWorkspaceBundle(r.body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(read.Bundle, saved.Bundle) || read.Head != saved.Head {
			t.Fatalf("the launcher holds %d bytes on %s, want %d on %s", len(read.Bundle), read.Head, len(saved.Bundle), saved.Head)
		}
	})

	t.Run("a repeated slice is answered as a repeated start id is", func(t *testing.T) {
		r := newBundleRestore(procgroup.NoReaper, newWorkspace(t))
		first := r.receive(frames[0])
		again := r.receive(frames[0])
		if again != first {
			t.Fatalf("the same slice sent twice answered %#v then %#v", first, again)
		}
		different := frames[0]
		different.Payload = append([]byte("not what was sent"), frames[0].Payload[17:]...)
		if got := r.receive(different); got.OK || !strings.Contains(got.Error, "reused an id with a different payload") {
			t.Fatalf("a different payload under a used id answered %#v", got)
		}
		if read, err := shimwire.ParseWorkspaceBundle(r.body); err == nil {
			t.Fatalf("a half-sent sequence left a body of %d bytes behind", len(read.Bundle))
		}
	})

	t.Run("a wrong digest is refused before anything is written", func(t *testing.T) {
		r := newBundleRestore(procgroup.NoReaper, newWorkspace(t))
		corrupt := append([]shimwire.LauncherBundle{}, frames...)
		payload := append([]byte{}, corrupt[1].Payload...)
		payload[0] ^= 0xff
		corrupt[1].Payload = payload
		var last shimwire.LauncherBundleResult
		for _, frame := range corrupt {
			last = r.receive(frame)
		}
		if last.OK || last.Held || !strings.Contains(last.Error, "not the "+frames[0].SHA256) {
			t.Fatalf("a sequence that arrived changed answered %#v, want a refusal naming the digest it declared", last)
		}
		if r.body != nil {
			t.Fatalf("a refused sequence left %d bytes to restore", len(r.body))
		}
		if got, err := r.apply(context.Background(), []string{"LEGION_WORKSPACE=/nowhere"}); got.Restored || got.Notice != "" || err != nil {
			t.Fatalf("a refused sequence restored something: %+v, %v", got, err)
		}
	})

	t.Run("a slice that disagrees with its sequence is refused", func(t *testing.T) {
		r := newBundleRestore(procgroup.NoReaper, newWorkspace(t))
		if got := r.receive(frames[0]); !got.OK {
			t.Fatalf("the first slice answered %#v", got)
		}
		odd := frames[1]
		odd.SHA256 = strings.Repeat("0", 64)
		if got := r.receive(odd); got.OK || !strings.Contains(got.Error, "does not match its sequence") {
			t.Fatalf("a slice of another digest answered %#v", got)
		}
		// The release is as much of the sequence as the digest: the report the daemon acts on
		// names it, and it deletes the bundle of that release alone.
		other := frames[1]
		other.Release = 12
		if got := r.receive(other); got.OK || !strings.Contains(got.Error, "does not match its sequence") {
			t.Fatalf("a slice of another release answered %#v", got)
		}
	})

	t.Run("the report names the release whose work it restored", func(t *testing.T) {
		r := newBundleRestore(procgroup.NoReaper, newWorkspace(t))
		for _, frame := range frames {
			if got := r.receive(frame); !got.OK {
				t.Fatalf("slice %d answered %#v", frame.Index, got)
			}
		}
		// The workspace records no creation, so this is the report of a restore that failed: it
		// still names its release, which is what keeps the daemon's copy where a report of no
		// release at all would have it guess.
		if got, err := r.apply(context.Background(), []string{"LEGION_WORKSPACE=/nowhere"}); got.Restored || err != nil {
			t.Fatalf("the restore answered %+v, %v; want no work restored and no error", got, err)
		}
		report := r.owed()
		if report == nil {
			t.Fatal("the launcher owes the daemon no report of the restore it ran")
		}
		if report.Release != 11 || report.ID != frames[0].ID || report.OK {
			t.Fatalf("the report is %#v, want release 11 of sequence %q and the restore failed", report, frames[0].ID)
		}
	})
}

// manySlices is a bundle whose body takes several launcher_bundle slices to carry.
func manySlices() shimwire.WorkspaceBundle {
	return shimwire.WorkspaceBundle{
		Bundle:    bytes.Repeat([]byte("PACKjj\x00\x02"), (768<<10)/8),
		Head:      "f2f134541b30dfa5160a5d1ce5e17b76c967c661",
		Tips:      []string{"da4f9f492e9f8349389c243743e0a000cd1c676f"},
		Bookmarks: map[string]string{"legion/" + bundleIssue: "dffe88ae2f793e13436cef3921adc988433fbd4e"},
		At:        time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC),
	}
}

// refusingStarter is a restore's start that fails the test if the restore starts anything.
func refusingStarter(t *testing.T) procgroup.Starter {
	return func(cmd *exec.Cmd) (func(), error) {
		t.Errorf("the restore started %v", cmd.Args)
		return func() {}, errors.New("this restore may start nothing")
	}
}

// A role that has started on the workspace leaves its record on the workspace's own disk
// (workspace.MarkInUse), so a launcher that restarts over that disk, holding nothing in memory of
// the start, still refuses a bundle over the work the role has done.
func TestABundleOverAWorkspaceInUseIsRefusedAfterTheLauncherRestarts(t *testing.T) {
	starter := refusingStarter(t)
	dir := newWorkspace(t)
	env := []string{"LEGION_WORKSPACE=" + dir}
	r1 := newBundleRestore(starter, dir)
	if got, err := r1.apply(context.Background(), env); got != (restoreResult{}) || err != nil {
		t.Fatalf("the first start's apply answered %+v, %v; want nothing and no error", got, err)
	}
	if inUse, err := workspace.InUse(dir); !inUse || err != nil {
		t.Fatalf("after the first start InUse = %t, %v; want true", inUse, err)
	}
	frames, err := shimwire.SplitWorkspaceBundle("restore-late", 12, manySlices())
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) < 3 {
		t.Fatalf("the fixture sliced into %d frames; the test needs several", len(frames))
	}
	// The restarted launcher: a restore of its own, which holds nothing of the one before.
	r2 := newBundleRestore(starter, dir)
	for _, frame := range frames {
		if got := r2.receive(frame); got.OK || got.Held || got.Error != workspaceInUseRefusal {
			t.Fatalf("slice %d after the restart answered %#v; want it refused, since a role has started on the workspace", frame.Index, got)
		}
	}
	if got, err := r2.apply(context.Background(), env); got != (restoreResult{}) || err != nil {
		t.Fatalf("the restarted launcher's apply answered %+v, %v; want nothing and no error", got, err)
	}
}

// A sequence the daemon began sending before the pod's first start keeps its id, so the slices
// that arrive after that start are refused as slices of a new sequence would be: only the slice
// already taken is answered again, as it was answered.
func TestTheRestOfASequenceThatBeganBeforeTheFirstStartIsRefusedAfterIt(t *testing.T) {
	starter := refusingStarter(t)
	dir := newWorkspace(t)
	env := []string{"LEGION_WORKSPACE=" + dir}
	frames, err := shimwire.SplitWorkspaceBundle("restore-early", 12, manySlices())
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) < 3 {
		t.Fatalf("the fixture sliced into %d frames; the test needs several", len(frames))
	}
	r := newBundleRestore(starter, dir)
	first := r.receive(frames[0])
	if !first.OK || first.Held {
		t.Fatalf("the first slice answered %#v", first)
	}
	if got, err := r.apply(context.Background(), env); got != (restoreResult{}) || err != nil {
		t.Fatalf("the first start's apply answered %+v, %v; want nothing and no error, since no body was whole", got, err)
	}
	if again := r.receive(frames[0]); again != first {
		t.Fatalf("the slice taken before the start answered %#v, then %#v", first, again)
	}
	for _, frame := range frames[1:] {
		if got := r.receive(frame); got.OK || got.Held || got.Error != workspaceInUseRefusal {
			t.Fatalf("slice %d after the first start answered %#v; want it refused, since a role has started on the workspace", frame.Index, got)
		}
	}
	if got, err := r.apply(context.Background(), env); got != (restoreResult{}) || err != nil {
		t.Fatalf("the second start's apply answered %+v, %v; want nothing and no error", got, err)
	}
}

// A start over a workspace whose .jj is a regular file, a failure root meets too, is refused, so no
// role works a workspace whose records the launcher can neither read nor write. The launcher reads
// the record of a restore through that .jj at its start and cannot, nor the in-use record beside
// it, so it counts a role as possibly at work there (bundleRestore.read): each start is refused,
// saying that neither a restore nor a role start is safe. Once .jj is a directory again, a new
// launcher over the workspace, which nothing has worked, takes the copy the daemon left unspent and
// restores it. This test calls no skip, and no helper it calls skips: jj and git are required
// before the fixture, whose own skip is for a missing one.
func TestAStartOverAJJThatIsAFileIsRefusedAndTheUnspentBundleRestoresAfterItClears(t *testing.T) {
	testbin.RequireJJFilters(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required: %v", err)
	}
	f := newBundleFixture(t)
	f.workOne()
	saved := f.bundleWorkspaceOne()
	ws := f.secondPod("ws6")
	head := f.Run(ws, "jj", "log", "-R", ws, "--ignore-working-copy", "-r", "@", "--no-graph", "-T", "commit_id")

	jj := filepath.Join(ws, ".jj")
	aside := jj + ".aside"
	if err := os.Rename(jj, aside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Lstat(aside); err == nil {
			_ = os.Remove(jj)
			_ = os.Rename(aside, jj)
		}
	})
	if err := os.WriteFile(jj, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := workspace.MarkInUse(ws); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("precondition: MarkInUse = %v, want ENOTDIR from the .jj that is a file", err)
	}
	if inUse, err := workspace.InUse(ws); err == nil {
		t.Fatalf("precondition: InUse = %t and no error, want an error from the .jj that is a file", inUse)
	}

	// The first launcher. The daemon's take sent it the copy, whose slices this test leaves out:
	// they are refused as its starts are, since the workspace's records cannot be read, and the
	// copy stays unspent, since no role of the pod registers.
	g := newSessionRig(t, Config{Workspace: ws})
	// The daemon's retry is a new start, which reads the records again while the failure stands.
	for _, generation := range []uint64{1, 2} {
		g.wantStartRefused(generation, ws, "could not be read", ws, "neither a restore nor a role start is safe", "the roles do not start")
	}

	if err := os.Remove(jj); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(aside, jj); err != nil {
		t.Fatal(err)
	}
	// The replacement must not start while the first launcher still runs.
	g.cancel()
	select {
	case err, ok := <-g.result:
		if !ok {
			t.Fatal("the first launcher's result was taken before its stop")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the first launcher returned %v, want its context's end", err)
		}
	case <-time.After(beforeDeadline(t)):
		t.Fatalf("the first launcher did not stop after its context ended, to 30 s before the test binary's deadline: %s", g.stderr.String())
	}
	if inUse, err := workspace.InUse(ws); inUse || err != nil {
		t.Fatalf("the cleared workspace reads InUse = %t, %v; want false and no error, since no role started on it", inUse, err)
	}
	if at := f.Run(ws, "jj", "log", "-R", ws, "--ignore-working-copy", "-r", "@", "--no-graph", "-T", "commit_id"); at != head {
		t.Fatalf("the cleared workspace is on %s, want %s, where provisioning left it", at, head)
	}

	notice := f.restoreInto(ws, saved, 3)
	if !strings.Contains(notice, "is restored") {
		t.Fatalf("the new launcher's restore notice is %q", notice)
	}
	f.requireRestored(ws, saved)
	if inUse, err := workspace.InUse(ws); !inUse || err != nil {
		t.Fatalf("after the restore InUse = %t, %v; want true", inUse, err)
	}
}

// The launcher's command loop ends its connection on a frame type it does not know, so the bundle
// pair has to be in its switch: a launcher that killed the connection would take the pod's roles
// with it.
func TestTheLauncherAnswersABundleFrameOnItsConnection(t *testing.T) {
	g := newSessionRig(t, Config{Workspace: newWorkspace(t)})
	saved := shimwire.WorkspaceBundle{Bundle: []byte("PACK not a real bundle"), Head: "f2f13454", At: time.Now().UTC()}
	frames, err := shimwire.SplitWorkspaceBundle("restore-2", 12, saved)
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range frames {
		got, ok := g.answer(frame).(shimwire.LauncherBundleResult)
		if !ok || !got.OK {
			t.Fatalf("the launcher answered %#v", got)
		}
	}
	// The connection is still the one the daemon can start a generation on.
	marker := filepath.Join(t.TempDir(), "marker")
	if result := g.start(g.childStart(1, marker, nil)); !result.OK {
		t.Fatalf("after the bundle the start answered %#v", result)
	}
	g.stop(1)
}

// The whole of both halves against real jj and git: one pod's shim bundles the unpushed work of
// its workspace — a working-copy change, an unpushed commit on the issue's branch, an unpushed
// commit on a second head and a moved local bookmark — nothing reaches the remote, and the
// launcher of the pod that replaces it restores all four into a workspace of its own before any
// role starts.
func TestAReleasedWorkspacesWorkIsBundledByTheShimAndRestoredByTheLauncher(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	f.workOne()
	saved := f.bundleWorkspaceOne()

	if remote := f.Run(f.Remote, "git", "--git-dir="+f.Remote, "log", "--all", "--oneline"); strings.Contains(remote, "working copy") ||
		strings.Contains(remote, "second head") || strings.Contains(remote, "issue work") {
		t.Fatalf("the save pushed to the remote, which now holds:\n%s", remote)
	}
	if len(saved.Tips) < 3 {
		t.Fatalf("the save kept %d heads, want the working copy, the issue branch's and the second head: %v", len(saved.Tips), saved.Tips)
	}
	if saved.Bookmarks["legion/"+bundleIssue] != f.Commits["issue work"] || saved.Bookmarks["side"] != f.Commits["second head"] {
		t.Fatalf("the save kept the bookmarks %v, want legion/%s on %s and side on %s",
			saved.Bookmarks, bundleIssue, f.Commits["issue work"], f.Commits["second head"])
	}
	if _, kept := saved.Bookmarks["main"]; kept {
		t.Fatalf("the save kept main, which points at a commit the remote already holds: %v", saved.Bookmarks)
	}
	if saved.Head != f.Commits["working copy"] {
		t.Fatalf("the save recorded the working copy as %s, want %s", saved.Head, f.Commits["working copy"])
	}

	ws := f.secondPod("ws2")
	notice := f.restoreInto(ws, saved, 1)
	if !strings.Contains(notice, "restored") {
		t.Fatalf("the restore's notice is %q", notice)
	}
	f.requireRestored(ws, saved)
	if inUse, err := workspace.InUse(ws); !inUse || err != nil {
		t.Fatalf("after the restore InUse = %t, %v; want true", inUse, err)
	}
}

// A save is read off the shim's own connection, past every connection another process opened to
// the daemon end ahead of it: one that closes at once, one that sends nothing, an HTTP request and
// a hello under another pane's boot token, as a client of another test binary redialing a reused
// loopback port would. Before the daemon end of the exchange (shimtest.Workspace.Bundle) read each
// connection's opening, it took the first connection its listener accepted as the shim's and
// failed on the one that closed.
func TestASaveIsReadOffTheShimsConnectionPastTheConnectionsAheadOfIt(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	f.workOne()
	f.AheadOfTheShim = func(address string) {
		dial := func(open func(net.Conn) error) {
			t.Helper()
			conn, err := net.Dial("tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			if err := open(conn); err != nil {
				t.Fatal(err)
			}
		}
		dial(func(conn net.Conn) error { return conn.Close() })
		dial(func(net.Conn) error { return nil })
		dial(func(conn net.Conn) error {
			_, err := conn.Write([]byte("GET /computeMetadata/v1/instance/service-accounts/default/identity HTTP/1.1\r\nHost: metadata\r\n\r\n"))
			return err
		})
		dial(func(conn net.Conn) error {
			return shimwire.NewWriter(conn).WriteFrame(shimwire.Hello2{BootToken: "another-pane"})
		})
	}
	saved := f.bundleWorkspaceOne()
	if saved.Head != f.Commits["working copy"] || saved.Bookmarks["legion/"+bundleIssue] != f.Commits["issue work"] {
		t.Fatalf("the save is on %s with the bookmarks %v, want the shim's: the working copy %s and legion/%s on %s",
			saved.Head, saved.Bookmarks, f.Commits["working copy"], bundleIssue, f.Commits["issue work"])
	}
}

// A shim that returns before its hello reaches the daemon end ends the save's wait for that hello
// at once, with what shim.Run returned, and not at the exchange's bound: here an enrollment with
// the secrets broker whose keygen cannot start ends the shim before it dials (shim.Config's
// AgentSecrets). Before the daemon end of the exchange (shimtest.Workspace.Bundle) read the shim's
// return, such a save waited out its whole bound and named only that.
func TestASaveWhoseShimReturnsBeforeItsHelloEndsAtOnceWithWhatTheShimReturned(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	keygen := filepath.Join(t.TempDir(), "no-agent-secrets")
	f.ShimConfig = func(config *shim.Config) {
		config.AgentSecrets = &shim.AgentSecrets{KeyDir: t.TempDir(), TokenFile: filepath.Join(t.TempDir(), "token"), Binary: keygen}
	}
	bound := beforeDeadline(t)
	ctx, cancel := context.WithTimeout(f.Ctx, bound)
	defer cancel()
	_, err := f.Bundle(ctx, runtime.MaxWorkspaceBundleBytes)
	const want = "the shim returned (1, agent-secrets keygen --out "
	if ctx.Err() != nil || err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), keygen) {
		t.Fatalf("the save ended with %v (its context: %v, its bound %s), want the shim's return before the bound: %q and the keygen %s it could not start",
			err, ctx.Err(), bound.Round(time.Second), want, keygen)
	}
}

// The launcher is its pod's PID 1, whose reaper reaps every exited child the launcher does not
// hold (reapOrphans), and a put-back's jj and git are the launcher's own children. Each starts
// through the guard the reaper skips (pidGuard.starter, which newManagers gives the pod's
// restore): a command whose exit the reaper took first fails its own wait with `waitid: no child
// processes`, or under gVisor at times `wait: no child processes` or `waitid: no such process`,
// and the roles start without the saved work. The race needs a pod's shape, as
// TestTheLeakCheckInstallSurvivesPID1sReaper says, so the put-backs run in the test binary as PID 1
// of a PID namespace of its own (TestPutBacksPIDOneHelper).
//
// The test binary as PID 1 does the put-backs alone. This test builds the fixture before it, ten
// workspaces of real git and jj, and checks what the put-backs restored after it, so the helper's
// time pays for the put-backs and nothing else: on a dev box under a one-minute load of 1,310 to
// 1,390 the build alone outran the 90 s a helper had under -timeout 150s, and the helper ended on
// its own alarm with no line of the row's. The tree is this test's, removed in its cleanup with
// helperDeadlineGap for it.
//
// Every wait before the helper starts has a bound. The build's every command, the shim's save
// among them, runs under a context that ends at the last moment a helper could start
// (helperStartBy), which kills the command and fails the test naming that moment; jj's filter probe
// and the PID namespace probe are killed 30 s before the test binary's deadline. The checks after
// the helper run under a context that ends 30 s before that deadline.
func TestPutBacksUnderThePodsReaperKeepTheirExitStatus(t *testing.T) {
	testbin.RequireJJFilters(t)
	build, done := context.WithCancel(context.Background())
	if startBy, ok := helperStartBy(t); ok {
		build, done = context.WithDeadlineCause(context.Background(), startBy, errBuildPastHelperStart)
	}
	t.Cleanup(func() {
		// A failure of the build's that is not a command's (the shim's connection closed under
		// the save, say) still says why.
		if errors.Is(context.Cause(build), errBuildPastHelperStart) {
			t.Logf("%v", errBuildPastHelperStart)
		}
	})
	f := newBundleFixtureWithin(t, build)
	f.workOne()
	saved := f.bundleWorkspaceOne()
	plan := putBackPlan{Saved: saved, Bundle: saved.Bundle, Env: f.Env}
	for index := range putBacksUnderReaper + controlPutBacks {
		workspace := f.secondPod(fmt.Sprintf("reaped-%d", index))
		if index < putBacksUnderReaper {
			plan.Guarded = append(plan.Guarded, workspace)
		} else {
			plan.Control = append(plan.Control, workspace)
		}
	}
	done()
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(f.Root, "put-backs.json")
	if err := os.WriteFile(planPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", runAsPIDOne(t, "TestPutBacksPIDOneHelper", putBackPlanEnv+"="+planPath))
	checks, stop := context.WithTimeoutCause(context.Background(), testwait.Bounded(t, 3*365*24*time.Hour, 30*time.Second),
		errors.New("the checks of the restored workspaces ran past 30 s before the test binary's deadline"))
	defer stop()
	f.Ctx = checks
	for _, dir := range plan.Guarded {
		f.requireRestored(dir, saved)
	}
	t.Logf("%d of %d put-backs through the guard restored the whole of the saved work", len(plan.Guarded), len(plan.Guarded))
}

// errBuildPastHelperStart is the cause of the context
// TestPutBacksUnderThePodsReaperKeepTheirExitStatus builds its fixture under, once that context has
// ended at helperStartBy.
var errBuildPastHelperStart = errors.New("the fixture build ran past the last moment a helper could start (the test binary's deadline less helperDeadlineGap and helperLeastTime)")

// putBackPlan is what TestPutBacksUnderThePodsReaperKeepTheirExitStatus hands its PID 1 helper,
// as JSON in the file putBackPlanEnv names: the saved work, its bundle's bytes (which
// WorkspaceBundle leaves out of its JSON), the fixture's environment, and the workspaces to put it
// back into through the guard and with none.
type putBackPlan struct {
	Saved   shimwire.WorkspaceBundle
	Bundle  []byte
	Env     []string
	Guarded []string
	Control []string
}

// putBackPlanEnv names the file of TestPutBacksPIDOneHelper's putBackPlan.
const putBackPlanEnv = "LEGION_LAUNCHER_PUT_BACK_PLAN"

// putBacksUnderReaper is how many put-backs TestPutBacksPIDOneHelper makes through the guard, and
// controlPutBacks how many it makes with no guard. Every command of each meets the reaper
// (metTheReaper), so each put-back is a race the reaper has run, not a chance at one.
const (
	putBacksUnderReaper = 5
	controlPutBacks     = 5
)

// TestPutBacksPIDOneHelper is TestPutBacksUnderThePodsReaperKeepTheirExitStatus's body, run as PID
// 1 under relentlessReaper, which starts a new pass as soon as it ends one. Every command of every
// put-back meets the reaper before its own wait reads its exit (metTheReaper). Each put-back
// through the guard must answer that the whole of what was saved is restored, and each of the
// control's, the same put-back started with procgroup.NoReaper as a put-back outside a launcher
// runs it, must lose its first command's exit to the reaper. The helper runs no command of its
// own: the put-backs' are all it runs, each under a context that ends 30 s before the helper's
// deadline (reaperBound), so one that hangs is killed then and its put-back fails by name.
func TestPutBacksPIDOneHelper(t *testing.T) {
	if os.Getenv(pidOneHelperEnv) != "1" {
		return
	}
	if os.Getpid() != 1 {
		t.Fatal("the put-back helper is not PID 1")
	}
	body, err := os.ReadFile(os.Getenv(putBackPlanEnv))
	if err != nil {
		t.Fatalf("the put-back helper's plan (%s): %v", putBackPlanEnv, err)
	}
	var plan putBackPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatal(err)
	}
	saved := plan.Saved
	saved.Bundle = plan.Bundle
	bound := reaperBound(t)
	if bound <= 0 {
		t.Fatalf("the put-back helper started %s past its deadline less 30 s: it was left no time for the put-backs", (-bound).Round(time.Millisecond))
	}
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	// An issue pod's managers, whose restore is the one newManagers wires to the guard. A failed
	// pass of a namespaced manager's reaper is logged rather than thrown, and the log is read
	// below: a reaper whose passes failed raced nothing.
	reaperLog := &lockedBuffer{}
	guard := &pidGuard{}
	managers, err := newManagers(Config{RolesDir: testerRolesDir(t), Roles: []string{"tester"}, Stdout: reaperLog, Stderr: reaperLog}, guard)
	if err != nil {
		t.Fatal(err)
	}
	stop, passed := relentlessReaper(t, managers, guard)
	type answer struct {
		restored bool
		notice   string
	}
	// Each put-back runs in a goroutine of its own and is waited for until 15 s before the test
	// binary's deadline, after reaperBound's 30 s: a pass that never ends while it holds the
	// guard's lock holds every guarded start with it, and the row then fails here, by name.
	putBack := func(start procgroup.Starter, dir string) answer {
		got := make(chan answer, 1)
		go func() {
			restored, notice := restoreWorkspace(ctx, metTheReaper(t, start, passed), dir, plan.Env, bundleRelease, saved)
			got <- answer{restored, notice}
		}()
		select {
		case a := <-got:
			return a
		case <-time.After(testwait.Bounded(t, 3*365*24*time.Hour, 15*time.Second)):
			t.Fatalf("the put-back into %s never returned, to 15 s before the test binary's deadline: a pass of the reaper that never ends holds its start", dir)
			return answer{}
		}
	}
	answers := make([]answer, len(plan.Guarded))
	for index, dir := range plan.Guarded {
		answers[index] = putBack(managers[0].restore.start, dir)
	}
	controls := make([]answer, len(plan.Control))
	for index, dir := range plan.Control {
		controls[index] = putBack(procgroup.NoReaper, dir)
	}
	stop()

	lost := 0
	for index, got := range controls {
		if !got.restored && (strings.Contains(got.notice, "no child processes") || strings.Contains(got.notice, "no such process")) {
			lost++
			continue
		}
		t.Errorf("the put-back with no guard into %s answered %v, %q; want its command's exit lost to the reaper, which met it", plan.Control[index], got.restored, got.notice)
	}
	t.Logf("control: %d of %d put-backs with no guard lost an exit to the reaper", lost, len(plan.Control))
	for index, got := range answers {
		if !got.restored || !strings.Contains(got.notice, "is restored") || strings.Contains(got.notice, "would not take") ||
			strings.Contains(got.notice, "no child processes") || strings.Contains(got.notice, "no such process") {
			t.Errorf("the put-back into %s answered %v, %q; want all of the saved work restored, each command's exit read by its own wait",
				plan.Guarded[index], got.restored, got.notice)
		}
	}
	if failed := reaperLog.String(); failed != "" {
		t.Errorf("the reaper's passes failed, so they raced nothing:\n%s", failed)
	}
	if !t.Failed() {
		t.Logf("%d of %d put-backs through the guard answered that the saved work is restored", len(plan.Guarded), len(plan.Guarded))
	}
}

// A base the restore needs can be gone — a branch force-pushed away since the release — and the
// bundle is then unusable. The roles still start, in the workspace provisioning made, and the
// notice names what was lost and why.
func TestARestoreWhoseBaseCommitIsGoneStartsTheRolesWithANotice(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	f.workOne()
	saved := f.bundleWorkspaceOne()

	// The remote's branches move back to the first commit, so the pod that replaces the released
	// one clones a repository that lacks the commit the bundle is built on.
	f.Run(f.Remote, "git", "--git-dir="+f.Remote, "update-ref", "refs/heads/main", f.Commits["one"])
	f.Run(f.Remote, "git", "--git-dir="+f.Remote, "update-ref", "refs/heads/legion/"+bundleIssue, f.Commits["one"])

	workspace := f.secondPod("ws3")
	notice := f.restoreInto(workspace, saved, 1)
	if !strings.Contains(notice, "not restored") || !strings.Contains(notice, "prerequisite") {
		t.Fatalf("the notice is %q, want it to name the work that was lost and the base git could not find", notice)
	}
	if !strings.Contains(notice, saved.At.UTC().Format(time.RFC3339)) {
		t.Fatalf("the notice is %q, want it to name when the lost work was saved", notice)
	}
	// The notice names the bundle the work is in, because that is where it stays: the daemon
	// sends it to no pod again once a role of this pod has registered, which the roles that start
	// on what was pushed do.
	const neverAgain = "sends to no pod again once a role of this pod has registered"
	if !strings.Contains(notice, fmt.Sprintf("bundle of release %d", bundleRelease)) || !strings.Contains(notice, neverAgain) {
		t.Fatalf("the notice is %q, want it to name the bundle the work stays in and that no pod is sent it again once a role of this pod registered", notice)
	}
	if log := f.Run(workspace, "jj", "log", "-R", workspace, "--ignore-working-copy", "-r", "all()", "--no-graph", "-T", "commit_id ++ \"\\n\""); strings.Contains(log, saved.Head) {
		t.Fatalf("a restore that failed left the saved working copy in the workspace:\n%s", log)
	}
}

// A bundle is restored into a workspace provisioned after it was saved, and into no other. The
// daemon holds that rule with a record of its own — the pod it sends a bundle to, which it sends
// none again once a role of that pod registers (store.TakeWorkspaceCopyForRestore) — and this is
// the workspace's own half of it, which catches what the daemon cannot see: a bundle that reached
// the very workspace the save was taken from, whose creation record is older than the save
// (workspace.Created). Applying it there would `jj edit` back to the saved head and move each
// bookmark `--allow-backwards` over what the roles have done since.
func TestARestoreRefusesAWorkspaceOlderThanTheWorkItHolds(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	f.workOne()
	saved := f.bundleWorkspaceOne()
	// The roles go on working in the workspace the save came out of, as they do while the pod
	// lives on: its working copy is another commit now.
	f.Run(f.Dir, "jj", "new", "-R", f.Dir, "-m", "work after the save")
	f.Write(filepath.Join(f.Dir, "after.txt"), "after\n")
	after := f.WorkingCopy()

	notice := f.restoreInto(f.Dir, saved, 1)
	if !strings.Contains(notice, "not restored") || !strings.Contains(notice, "before the work was saved") {
		t.Fatalf("the notice is %q, want the restore refused for a workspace created before the work was saved", notice)
	}
	if head := f.Run(f.Dir, "jj", "log", "-R", f.Dir, "--ignore-working-copy", "-r", "@", "--no-graph", "-T", "commit_id"); head != after {
		t.Fatalf("the working copy is on %s, want the %s the roles worked on: the restore edited the workspace back to the saved head %s",
			head, after, saved.Head)
	}
}

// A bookmark's name is written by the agent that worked in the released workspace, so it reaches
// jj as an argument nobody chose. One jj reads as an option of its own must cost that bookmark
// and nothing else: the heads, the working copy and every other bookmark are restored, and the
// notice names the one that was not set.
func TestARestoreSkipsABookmarkNameJJWillNotTakeAndRestoresTheRest(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	f.workOne()
	saved := f.bundleWorkspaceOne()
	// jj reads this one as an option: `jj bookmark set --oops` is an unknown flag, and before
	// the restore passed its names after `--` it ended the whole restore with the workspace
	// half put back.
	const awkward = "--oops"
	saved.Bookmarks[awkward] = saved.Bookmarks["side"]

	workspace := f.secondPod("ws4")
	notice := f.restoreInto(workspace, saved, 1)
	if !strings.Contains(notice, "is restored") || !strings.Contains(notice, awkward) {
		t.Fatalf("the notice is %q, want the restore done and the bookmark it could not set named", notice)
	}
	if head := f.Run(workspace, "jj", "log", "-R", workspace, "--ignore-working-copy", "-r", "@", "--no-graph", "-T", "commit_id"); head != saved.Head {
		t.Fatalf("the restored working copy is on %s, want the saved %s: one bad bookmark name took the rest of the restore with it", head, saved.Head)
	}
	listed := f.Run(workspace, "jj", "bookmark", "list", "-R", workspace, "--ignore-working-copy",
		"-T", `if(remote, "", name ++ " " ++ normal_target.commit_id() ++ "\n")`)
	if !strings.Contains(listed, "side "+f.Commits["second head"]) {
		t.Fatalf("the restored workspace has no side bookmark:\n%s", listed)
	}
	if strings.Contains(listed, awkward) {
		t.Fatalf("the restore set the bookmark jj refused:\n%s", listed)
	}
}

// bundleIssue is the issue the fixture's workspaces belong to, and bundleOwner and bundleRepo
// the repository: the three name the directories a pod that replaces the released one is laid
// out in (secondPod, workspace.Location).
const (
	bundleIssue = "INFRA-945"
	bundleOwner = "acme"
	bundleRepo  = "widgets"
)

// bundleFixture is one remote, the pod that is released and the workspace it worked in, which is
// the fixture of every test that runs the real shim's bundler (shimtest.Workspace), and the pods
// that replace it, into which this package's launcher restores what that shim bundled. Each of its
// commands, and the shim's save, runs under the Workspace's Ctx: a command still running when Ctx
// ends is killed, and its failure names Ctx's cause. The save's daemon end takes the shim's
// connection by its hello under a boot token of the save's own, past whatever a case's
// AheadOfTheShim dials there first, as another process's client on a reused port would.
type bundleFixture struct {
	*shimtest.Workspace
	t *testing.T
}

func newBundleFixture(t *testing.T) *bundleFixture {
	t.Helper()
	return newBundleFixtureWithin(t, context.Background())
}

// newBundleFixtureWithin is newBundleFixture whose commands run under ctx (bundleFixture).
func newBundleFixtureWithin(t *testing.T, ctx context.Context) *bundleFixture {
	t.Helper()
	return &bundleFixture{Workspace: shimtest.NewWithin(t, bundleIssue, ctx), t: t}
}

// secondPod is the pod that replaces the released one: its own clone of the remote as it stands
// now, and its own workspace, holding only what was pushed. name is the state directory the pair
// is laid out under, each pod having one of its own; the clone and the workspace sit where
// provisioning puts them below it, which is where a launcher started in that workspace derives
// the clone from (workspace.Location, workspace.WorkspaceOf).
func (f *bundleFixture) secondPod(name string) string {
	f.t.Helper()
	state := filepath.Join(f.Root, name)
	located, err := workspace.Location(state, ghrepo.MustParse(bundleOwner+"/"+bundleRepo), bundleIssue)
	if err != nil {
		f.t.Fatal(err)
	}
	clone, ws := located.Clone, located.Dir
	for _, dir := range []string{filepath.Dir(clone), filepath.Dir(ws)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			f.t.Fatal(err)
		}
	}
	f.Pod(clone, ws)
	return ws
}

// workOne leaves in the released pod's workspace the four kinds of unpushed work a release has to
// keep: a commit on the issue's branch, the working copy's own change on top of it, a commit on a
// second head, and a local bookmark moved onto that head.
func (f *bundleFixture) workOne() {
	f.t.Helper()
	f.Run(f.Dir, "jj", "new", "-R", f.Dir, `remote_bookmarks(exact:"legion/`+bundleIssue+`")`, "-m", "issue work")
	f.Write(filepath.Join(f.Dir, "issue.txt"), "issue\n")
	f.Run(f.Dir, "jj", "bookmark", "set", "-R", f.Dir, "--allow-backwards", "legion/"+bundleIssue, "-r", "@")
	f.Commits["issue work"] = f.WorkingCopy()
	f.Run(f.Dir, "jj", "new", "-R", f.Dir, "-m", "working copy")
	f.Write(filepath.Join(f.Dir, "wip.txt"), "wip\n")
	working := f.WorkingCopy()
	f.Run(f.Dir, "jj", "new", "-R", f.Dir, "trunk()", "-m", "second head")
	f.Write(filepath.Join(f.Dir, "second.txt"), "second\n")
	f.Run(f.Dir, "jj", "bookmark", "create", "-R", f.Dir, "side", "-r", "@")
	f.Commits["second head"] = f.WorkingCopy()
	f.Run(f.Dir, "jj", "edit", "-R", f.Dir, working)
	f.Commits["working copy"] = f.WorkingCopy()
}

// bundleWorkspaceOne runs the real shim against the released pod's workspace and returns the
// bundle it built (shimtest.Workspace.Bundle), with the bundle's byte bound the daemon asks with,
// failing the test where the shim built none. The exchange runs until beforeDeadline runs out, or
// the fixture's context ends, whichever comes first, as the launcher's other waits do: a save that
// runs a while with real git and jj on a loaded machine is merely slow, and one that never comes
// fails here, naming the bound, and the context's cause where the context ended.
func (f *bundleFixture) bundleWorkspaceOne() shimwire.WorkspaceBundle {
	f.t.Helper()
	bound := beforeDeadline(f.t)
	ctx, cancel := context.WithTimeout(f.Ctx, bound)
	defer cancel()
	saved, err := f.Bundle(ctx, runtime.MaxWorkspaceBundleBytes)
	if err != nil {
		ended := ""
		if f.Ctx.Err() != nil {
			ended = fmt.Sprintf("; the fixture's context ended: %v", context.Cause(f.Ctx))
		}
		f.t.Fatalf("the shim's bundle: %v (the exchange waited up to %s, to 30 s before the test binary's deadline%s)",
			err, bound.Round(time.Second), ended)
	}
	return saved
}

// bundleRelease is the id of the idle release whose save every fixture bundle carries: the daemon
// deletes the bundle of the release a restore report names, so each sequence and each report names
// one.
const bundleRelease int64 = 9

// restoreInto drives one replacement pod's launcher: the bundle in launcher_bundle slices, then
// the generation's start, which applies the restore before its child runs. It returns the notice
// the launcher wrote for the restore, having held it to the report the launcher sent the daemon,
// and stops the generation.
func (f *bundleFixture) restoreInto(workspace string, saved shimwire.WorkspaceBundle, generation uint64) string {
	f.t.Helper()
	g := newSessionRig(f.t, Config{Workspace: workspace})
	frames, err := shimwire.SplitWorkspaceBundle("restore-"+workspace, bundleRelease, saved)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, frame := range frames {
		got, ok := g.answer(frame).(shimwire.LauncherBundleResult)
		if !ok || !got.OK {
			f.t.Fatalf("slice %d answered %#v", frame.Index, got)
		}
		if want := frame.Index == len(frames)-1; got.Held != want {
			f.t.Fatalf("slice %d answered held=%v, want %v", frame.Index, got.Held, want)
		}
	}
	marker := filepath.Join(f.t.TempDir(), "marker")
	command := g.childStart(generation, marker, nil)
	command.Env = append(command.Env, append(f.Env, "LEGION_WORKSPACE="+workspace)...)
	if result := g.start(command); !result.OK {
		f.t.Fatalf("the start after the bundle answered %#v", result)
	}
	published(f.t, marker)
	g.stop(generation)
	// The daemon deletes its saved copy on this report and on nothing else, so the start that
	// ran the restore sends it before it answers.
	report := g.restored
	if report == nil {
		f.t.Fatalf("the launcher reported nothing of the restore it ran:\n%s", g.stderr.String())
	}
	if report.ID != frames[0].ID || report.Release != bundleRelease {
		f.t.Fatalf("the launcher reported the restore of sequence %q of release %d, want %q of release %d",
			report.ID, report.Release, frames[0].ID, bundleRelease)
	}
	for _, line := range strings.Split(g.stderr.String(), "\n") {
		if strings.Contains(line, "released workspace's saved work") {
			if report.OK != strings.Contains(line, "is restored") {
				f.t.Fatalf("the launcher reported ok=%v and wrote %q", report.OK, line)
			}
			return line
		}
	}
	f.t.Fatalf("the launcher wrote no notice for the restore:\n%s", g.stderr.String())
	return ""
}

// requireRestored fails the test unless workspace holds all of saved: every saved head visible,
// each saved bookmark on its commit, the working copy on the saved head with its own change, and
// none of the restore's temporary branches left in the repository.
func (f *bundleFixture) requireRestored(workspace string, saved shimwire.WorkspaceBundle) {
	f.t.Helper()
	log := f.Run(workspace, "jj", "log", "-R", workspace, "--ignore-working-copy", "-r", "all()", "--no-graph",
		"-T", `commit_id ++ " " ++ description.first_line() ++ "\n"`)
	for _, want := range []string{"issue work", "second head", "working copy"} {
		if !strings.Contains(log, f.Commits[want]+" "+want) {
			f.t.Fatalf("the restored workspace %s holds no %s (%s), so that head is not visible:\n%s", workspace, want, f.Commits[want], log)
		}
	}
	listed := f.Run(workspace, "jj", "bookmark", "list", "-R", workspace, "--ignore-working-copy",
		"-T", `if(remote, "", name ++ " " ++ normal_target.commit_id() ++ "\n")`)
	for name, commit := range map[string]string{"legion/" + bundleIssue: f.Commits["issue work"], "side": f.Commits["second head"]} {
		if !strings.Contains(listed, name+" "+commit) {
			f.t.Fatalf("the restored workspace %s has no %s on %s:\n%s", workspace, name, commit, listed)
		}
	}
	if head := f.Run(workspace, "jj", "log", "-R", workspace, "--ignore-working-copy", "-r", "@", "--no-graph", "-T", "commit_id"); head != saved.Head {
		f.t.Fatalf("the restored working copy of %s is on %s, want the saved %s", workspace, head, saved.Head)
	}
	if body, err := os.ReadFile(filepath.Join(workspace, "wip.txt")); err != nil || string(body) != "wip\n" {
		f.t.Fatalf("the restored working copy's own change in %s reads %q (%v)", workspace, body, err)
	}
	// Nothing of the restore is left in the repository: the temporary branches are gone.
	if refs := f.Run(workspace, "git", "--git-dir="+f.GitDir(workspace), "for-each-ref", "--format=%(refname)", "refs/heads/"); strings.Contains(refs, restoreRefPrefix) {
		f.t.Fatalf("the restore left its temporary branches behind in %s:\n%s", workspace, refs)
	}
}
