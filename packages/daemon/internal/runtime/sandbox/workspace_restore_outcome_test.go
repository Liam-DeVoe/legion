package sandbox

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// bundleTake is the take's statement as the fault proxy matches it.
const bundleTake = "update workspace_bundles"

// faultedBundleStore is st's database through a commit-fault proxy, opened as a store of its
// own: the take runs on it, and st reads what committed.
func faultedBundleStore(t *testing.T, st *store.Store) (*store.Store, *testdb.CommitFault) {
	t.Helper()
	dsn, fault := testdb.CommitFaults(t, st.Pool().Config().ConnString())
	faulted, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open the store through the fault proxy: %v", err)
	}
	t.Cleanup(faulted.Close)
	return faulted, fault
}

// faultedBundleRig is a store holding savedWorkspaceBundle of an issue the rig's project records,
// the same database through the fault proxy, and a rig whose hop takes the issue's copies through
// the proxy and keeps the restored one straight.
func faultedBundleRig(t *testing.T) (*store.Store, *testdb.CommitFault, runtime.WorkspaceBundle, *rig) {
	t.Helper()
	st := bundleStore(t)
	saved := savedWorkspaceBundle()
	saveReleaseBundle(t, st, saved)
	faulted, fault := faultedBundleStore(t, st)
	// The hop runs inside the start, whose bound is BootTimeout: the rig's two seconds would end
	// a hop that waits for the store before the store answers.
	g := newRig(t, nil, withOptions(func(o *Options) {
		o.SavedWorkspaceBundle, o.WorkspaceBundleRestored = takeCopies(faulted), st.KeepRestoredWorkspaceCopy
		o.BootTimeout = 15 * time.Second
	}))
	return st, fault, saved, g
}

// releaseAfter lets the refused store answer again two seconds after fired closes, while the hop
// it refused still waits inside its bound. The answer says whether fired closed at all.
func releaseAfter(fault *testdb.CommitFault, fired <-chan struct{}) *atomic.Bool {
	met := &atomic.Bool{}
	go func() {
		select {
		case <-fired:
			met.Store(true)
			time.Sleep(2 * time.Second)
		case <-time.After(30 * time.Second):
		}
		fault.Refuse(false)
	}()
	return met
}

// requireSentOnce wants exactly one whole slice sequence of saved before the role's start, the
// restore reported (the bundle's row gone on the report, its work kept as the restoring pod's own
// copy), and no slice at a second hop of that pod.
func requireSentOnce(t *testing.T, st *store.Store, g *rig, spec runtime.SpawnSpec, saved runtime.WorkspaceBundle) {
	t.Helper()
	frames := g.sent(spec.Claim)
	start := slices.IndexFunc(frames, func(f shimwire.Frame) bool { _, ok := f.(shimwire.LauncherStart); return ok })
	if start < 1 {
		t.Fatalf("the launcher was sent %v, want the bundle's slices and then the role's start", frameTypes(frames))
	}
	var body []byte
	var sequence string
	for i, frame := range frames[:start] {
		slice, ok := frame.(shimwire.LauncherBundle)
		if !ok {
			t.Fatalf("the launcher was sent %v, want only the bundle's slices before the role's start", frameTypes(frames))
		}
		if i == 0 {
			sequence = slice.ID
		}
		if slice.ID != sequence || slice.Index != i || slice.Count != start {
			t.Fatalf("slice %d of %d under %q arrived at position %d of the %d sent before the start: want one whole sequence",
				slice.Index, slice.Count, slice.ID, i, start)
		}
		body = append(body, slice.Payload...)
	}
	read, err := shimwire.ParseWorkspaceBundle(body)
	if err != nil {
		t.Fatalf("the %d slices rebuilt no workspace bundle: %v", start, err)
	}
	if !bytes.Equal(read.Bundle, saved.Bundle) {
		t.Fatalf("the bundle crossed as %d bytes, want the %d saved", len(read.Bundle), len(saved.Bundle))
	}
	if _, held, err := st.WorkspaceBundleSize(g.ctx, testTree); err != nil || held {
		t.Fatalf("after the launcher reported its restore the store holds the bundle = %t (%v), want its row gone", held, err)
	}
	before := len(g.sent(spec.Claim))
	g.connect(spec.Claim)
	g.r.launchers.restoreWorkspace(g.ctx, spec.Claim, string(g.pod(SandboxName(spec.Claim)).UID))
	for _, frame := range g.sent(spec.Claim)[before:] {
		if slice, ok := frame.(shimwire.LauncherBundle); ok {
			t.Fatalf("a second hop was sent slice %d of %d, want nothing: the restore was reported and the bundle's row is gone", slice.Index, slice.Count)
		}
	}
}

// A take whose commit landed and whose reply was lost is read from the store, and the hop sends
// the body that run read: one attempt, one send.
func TestABundleTakeWhoseReplyIsLostIsSentOnce(t *testing.T) {
	st, fault, saved, g := faultedBundleRig(t)
	fired := fault.Cut(bundleTake, testdb.CutAfterCommit)
	spec := workerSpec(t)
	g.spawn(spec)
	testdb.Met(t, fired)
	requireSentOnce(t, st, g, spec, saved)
}

// A take that did not commit leaves the attempt unspent, so the hop takes again for the same pod
// and sends what that run read.
func TestABundleTakeThatRolledBackIsTakenAgainInItsHop(t *testing.T) {
	st, fault, saved, g := faultedBundleRig(t)
	fired := fault.Cut(bundleTake, testdb.CutBeforeCommit)
	spec := workerSpec(t)
	g.spawn(spec)
	testdb.Met(t, fired)
	requireSentOnce(t, st, g, spec, saved)
}

// A take whose outcome the store cannot say waits inside the hop's bound for the store to answer,
// and then sends the body of the run that committed.
func TestABundleTakeWhoseOutcomeIsUnknownIsSentOnceTheStoreAnswers(t *testing.T) {
	st, fault, saved, g := faultedBundleRig(t)
	fired := fault.Cut(bundleTake, testdb.CutAfterCommitRefusing)
	met := releaseAfter(fault, fired)
	spec := workerSpec(t)
	g.spawn(spec)
	if !met.Load() {
		t.Fatal("the armed fault met no COMMIT of the take")
	}
	requireSentOnce(t, st, g, spec, saved)
}

// A take whose outcome stays unknown past its caller's bound says that the take, with this pod's
// first ask, may be recorded. The run committed, so the row records the attempt to the pod. No role
// of that pod has registered, so the copy is unspent: a later take by the same pod, which is what
// its restarted container asks, is sent the same copy under the same restore id. Once a role of the
// pod registers, the copy is spent and a take by the pod answers nothing, while the work stays in
// the database.
func TestABundleTakeWhoseOutcomeStaysUnknownLeavesTheCopyUnspentUntilARoleRegisters(t *testing.T) {
	st := bundleStore(t)
	saved := savedWorkspaceBundle()
	saveReleaseBundle(t, st, saved)
	faulted, fault := faultedBundleStore(t, st)
	const podUID = "pod-whose-take-stays-unknown"
	fired := fault.Cut(bundleTake, testdb.CutAfterCommitRefusing)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	take, err := faulted.TakeWorkspaceCopyForRestore(ctx, testProject, testTree, podUID)
	testdb.Met(t, fired)
	if !errors.Is(err, store.ErrCommitOutcomeUnknown) || !strings.Contains(err.Error(), "may be recorded") {
		t.Fatalf("the take whose outcome stayed unknown = %+v (%v), want an error that is store.ErrCommitOutcomeUnknown and says the take may be recorded", take, err)
	}
	fault.Refuse(false)
	var attempt *string
	if err := st.Pool().QueryRow(context.Background(), `select attempt_pod_uid from workspace_bundles where issue = $1`, testTree).Scan(&attempt); err != nil {
		t.Fatalf("read the bundle's attempt: %v", err)
	}
	if attempt == nil || *attempt != podUID {
		t.Fatalf("the bundle's attempt names %v, want %s: the run committed", attempt, podUID)
	}
	again, err := st.TakeWorkspaceCopyForRestore(context.Background(), testProject, testTree, podUID)
	if err != nil || again.ReleaseBundle.Take != runtime.WorkspaceBundleSend ||
		!bytes.Equal(again.ReleaseBundle.Bundle.Bundle, saved.Bundle) || again.ReleaseBundle.Bundle.Release != saved.Release {
		t.Fatalf("a later take by the same pod before any role of it registered = %+v, %v; want the copy sent again under restore id %d", again.ReleaseBundle, err, saved.Release)
	}
	if err := st.RecordPodRegistration(context.Background(), testTree, podUID); err != nil {
		t.Fatalf("record the registration of a role of %s: %v", podUID, err)
	}
	if spent, err := st.TakeWorkspaceCopyForRestore(context.Background(), testProject, testTree, podUID); err != nil ||
		spent.ReleaseBundle.Take != runtime.WorkspaceBundleNone || len(spent.ReleaseBundle.Bundle.Bundle) != 0 ||
		spent.Running.Take != runtime.WorkspaceBundleNone {
		t.Fatalf("a take by the same pod after a role of it registered = %+v, %v; want nothing: the copy is spent", spent, err)
	}
	if _, held, err := st.WorkspaceBundleSize(context.Background(), testTree); err != nil || !held {
		t.Fatalf("the store holds the bundle = %t (%v), want the work kept", held, err)
	}
}

// A restored copy's confirmation runs on the launcher's one report of a restore, so it runs until
// one run commits, for either kind of copy: a confirmation whose reply was lost waits for the
// store's answer, and one that rolled back runs again, rather than leaving a release bundle's row
// to hold the pod or a running copy owed to a pod that already holds its work. Each row ends with
// the work kept as the restoring pod's own running copy.
func TestARestoredCopyKeepIsKeptWhateverBecameOfItsFirstCommit(t *testing.T) {
	kinds := []struct {
		name      string
		statement string
		// save stores the copy, takes it for podUID and answers the keep of it, with what the
		// row the keep leaves must hold.
		save func(t *testing.T, st *store.Store, podUID string) (keep func(context.Context, *store.Store) error, kept func(t *testing.T))
	}{
		{"release bundle", "delete from workspace_bundles", func(t *testing.T, st *store.Store, podUID string) (func(context.Context, *store.Store) error, func(*testing.T)) {
			saved := savedWorkspaceBundle()
			saveReleaseBundle(t, st, saved)
			if take, err := st.TakeWorkspaceCopyForRestore(context.Background(), testProject, testTree, podUID); err != nil ||
				take.ReleaseBundle.Take != runtime.WorkspaceBundleSend {
				t.Fatalf("the take of the bundle for %s = %+v, %v; want it sent", podUID, take, err)
			}
			return func(ctx context.Context, s *store.Store) error {
					return s.KeepRestoredWorkspaceBundle(ctx, testTree, podUID, saved.Release)
				}, func(t *testing.T) {
					if _, held, err := st.WorkspaceBundleSize(context.Background(), testTree); err != nil || held {
						t.Fatalf("the store holds the bundle = %t (%v), want its row gone", held, err)
					}
					var source string
					if err := st.Pool().QueryRow(context.Background(), `select source_pod_uid from workspace_running_copies where issue = $1`, testTree).Scan(&source); err != nil || source != podUID {
						t.Fatalf("the running copy's source = %q (%v), want %s: the work stays as the restoring pod's own copy", source, err, podUID)
					}
				}
		}},
		{"running copy", "update workspace_running_copies", func(t *testing.T, st *store.Store, podUID string) (func(context.Context, *store.Store) error, func(*testing.T)) {
			saveRunningCopy(t, st, runningWorkspaceCopy("pod-the-running-copy-came-off"))
			take, err := st.TakeWorkspaceCopyForRestore(context.Background(), testProject, testTree, podUID)
			if err != nil || take.Running.Take != runtime.WorkspaceBundleSend {
				t.Fatalf("the take of the running copy for %s = %+v, %v; want it sent", podUID, take, err)
			}
			restoreID := take.Running.Bundle.Release
			return func(ctx context.Context, s *store.Store) error {
					return s.KeepRestoredRunningWorkspaceCopy(ctx, testTree, podUID, restoreID)
				}, func(t *testing.T) {
					var source string
					var id int64
					var attempt *string
					if err := st.Pool().QueryRow(context.Background(), `select source_pod_uid, restore_id, attempt_pod_uid from workspace_running_copies where issue = $1`, testTree).
						Scan(&source, &id, &attempt); err != nil {
						t.Fatalf("read the running copy: %v", err)
					}
					if source != podUID || id == restoreID || attempt != nil {
						t.Fatalf("the running copy's source = %q, restore id %d (sent as %d), attempt %v; want %s, a new id and no attempt: the work stays as the restoring pod's own copy", source, id, restoreID, attempt, podUID)
					}
				}
		}},
	}
	cuts := []struct {
		name string
		// arm cuts the keep's statement and answers whether the cut met the keep's COMMIT.
		arm func(t *testing.T, fault *testdb.CommitFault, statement string) (met func() bool)
	}{
		{"whose reply is lost", func(t *testing.T, fault *testdb.CommitFault, statement string) func() bool {
			return releaseAfter(fault, fault.Cut(statement, testdb.CutAfterCommitRefusing)).Load
		}},
		{"that rolled back", func(t *testing.T, fault *testdb.CommitFault, statement string) func() bool {
			fired := fault.Cut(statement, testdb.CutBeforeCommit)
			return func() bool {
				select {
				case <-fired:
					return true
				default:
					return false
				}
			}
		}},
	}
	for _, kind := range kinds {
		for _, cut := range cuts {
			t.Run(kind.name+" "+cut.name, func(t *testing.T) {
				st := bundleStore(t)
				keep, kept := kind.save(t, st, "pod-that-restored-the-copy")
				faulted, fault := faultedBundleStore(t, st)
				met := cut.arm(t, fault, kind.statement)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := keep(ctx, faulted); err != nil {
					t.Fatalf("the restored %s's confirmation = %v, want nil", kind.name, err)
				}
				if !met() {
					t.Fatal("the armed fault met no COMMIT of the confirmation")
				}
				kept(t)
			})
		}
	}
}

// The drop of a bundle a pod's kept volume holds answers, in the same hop, whether the take still
// runs, and a drop that committed leaves nothing for a second run to find. So a drop whose
// outcome was unknown waits for the store's answer, and answers the run that committed.
func TestADropWhoseOutcomeIsUnknownAnswersTheDropOnceTheStoreAnswers(t *testing.T) {
	st := bundleStore(t)
	saved := savedWorkspaceBundle()
	// The kept volume's Sandbox was made before the save, so its pod mounts the disk the work was
	// taken off (runtime.DecideKeptVolumeDrop).
	keptSince := time.Now().Add(-time.Minute)
	saveReleaseBundle(t, st, saved)
	faulted, fault := faultedBundleStore(t, st)
	fired := fault.Cut("delete from workspace_bundles", testdb.CutAfterCommitRefusing)
	met := releaseAfter(fault, fired)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dropped, err := faulted.DropHeldWorkspaceCopies(ctx, testProject, testTree, "pod-of-the-kept-volume", keptSince)
	if held := dropped.ReleaseBundle; err != nil || held.Take != runtime.WorkspaceBundleHeldByVolume || held.Bundle.Release != saved.Release {
		t.Fatalf("the drop whose outcome was unknown = %+v, %v; want release %d dropped", held, err, saved.Release)
	}
	if !met.Load() {
		t.Fatal("the armed fault met no COMMIT of the drop")
	}
	if _, kept, err := st.WorkspaceBundleSize(context.Background(), testTree); err != nil || kept {
		t.Fatalf("the store holds the bundle = %t (%v), want it dropped", kept, err)
	}
}

// A take of the issue's copies that the store does not answer holds the role's start, for either
// kind of copy: the start fails with the take's error, which is the daemon's own store's
// (runtime.ErrWorkspaceCopyStore, which the supervisor retries charging nothing), and sends the
// launcher no launcher_start, so no role starts on what was pushed while a copy is owed; and since
// no Start was sent, it sends no launcher_stop either (stopIssuedChild). The copy is left as it
// was, and the next launch, whose take the store answers, sends it and starts the role. A role
// started beside the failed take would register and spend the copy, and a running copy would then
// be replaced by the next copy of its pod's workspace, which never held the work. The store here
// opens no connection (refuseEveryConnection), as one that is down.
//
// The controls (the pull request records the runs): the take's error not wrapped in the sentinel
// fails the errors.Is; and stopIssuedChild without its startHeld case sends a launcher_stop.
func TestAStartWhoseTakeOfTheCopiesFailsRunsNoRoleUntilATakeSendsTheCopy(t *testing.T) {
	for _, kind := range []struct {
		name string
		save func(t *testing.T, st *store.Store) runtime.WorkspaceBundle
		of   runtime.WorkspaceCopyKind
	}{
		{"release bundle", func(t *testing.T, st *store.Store) runtime.WorkspaceBundle {
			saved := savedWorkspaceBundle()
			saveReleaseBundle(t, st, saved)
			return saved
		}, runtime.WorkspaceCopyReleaseBundle},
		{"running copy", func(t *testing.T, st *store.Store) runtime.WorkspaceBundle {
			saved := runningWorkspaceCopy("pod-the-running-copy-came-off")
			saveRunningCopy(t, st, saved)
			return saved
		}, runtime.WorkspaceCopyRunning},
	} {
		t.Run(kind.name, func(t *testing.T) {
			st := bundleStore(t)
			saved := kind.save(t, st)
			faulted, fault := faultedBundleStore(t, st)
			refuseEveryConnection(faulted, fault)
			g := newRig(t, nil, withOptions(func(o *Options) {
				o.SavedWorkspaceBundle, o.WorkspaceBundleRestored = takeCopies(faulted), st.KeepRestoredWorkspaceCopy
			}))
			spec := workerSpec(t)
			requireStoreHeld(t, g, spec, "take", "could not be taken")

			again := newRig(t, nil, withOptions(func(o *Options) {
				o.SavedWorkspaceBundle, o.WorkspaceBundleRestored = takeCopies(st), st.KeepRestoredWorkspaceCopy
			}))
			again.spawn(spec)
			var body []byte
			frames := again.sent(spec.Claim)
			for _, frame := range frames {
				if slice, ok := frame.(shimwire.LauncherBundle); ok {
					body = append(body, slice.Payload...)
				}
			}
			read, err := shimwire.ParseWorkspaceBundle(body)
			if err != nil || !bytes.Equal(read.Bundle, saved.Bundle) {
				t.Fatalf("the next launch sent %v; want the %s it was owed (%v)", frameTypes(frames), kind.of, err)
			}
		})
	}
}

// A drop of the copies a pod's kept volume holds that the store does not answer holds the role's
// start as such a take does: the start fails with the drop's error, which is the daemon's own
// store's (runtime.ErrWorkspaceCopyStore, which the supervisor retries charging nothing), and the
// take, which the store would answer, does not run. So the launcher is sent no launcher_start, and
// no role starts while the drop has not answered which copies the volume holds; and since no Start
// was sent, it sends no launcher_stop either (stopIssuedChild). The drop is wired as the daemon
// hands it to the runtime (withKeptVolumeDrop), on a store that opens no connection
// (refuseEveryConnection).
//
// The control (the pull request records the run): the drop's error not wrapped in the sentinel
// fails the errors.Is.
func TestAStartWhoseDropOfTheKeptVolumesCopiesFailsRunsNoRole(t *testing.T) {
	st := bundleStore(t)
	faulted, fault := faultedBundleStore(t, st)
	refuseEveryConnection(faulted, fault)
	var takes atomic.Int32
	take := takeCopies(st)
	g := newRig(t, nil, withOptions(keptVolumes(SandboxMicroVM)), withOptions(func(o *Options) {
		o.SavedWorkspaceBundle = func(ctx context.Context, issue, podUID string) (runtime.WorkspaceRestore, error) {
			takes.Add(1)
			return take(ctx, issue, podUID)
		}
		o.WorkspaceBundleRestored = st.KeepRestoredWorkspaceCopy
		o.HeldWorkspaceCopies = func(ctx context.Context, issue, podUID string, keptSince time.Time) (runtime.WorkspaceRestore, error) {
			return faulted.DropHeldWorkspaceCopies(ctx, testProject, issue, podUID, keptSince)
		}
	}))
	requireStoreHeld(t, g, workerSpec(t), "drop", "the drop of the saved workspace copies")
	if n := takes.Load(); n != 0 {
		t.Fatalf("the start whose drop failed took the copies %d times; want the take never run", n)
	}
}

// Only a take the daemon's store did not answer holds the start as the store's
// (runtime.ErrWorkspaceCopyStore, which the supervisor retries charging nothing): here one whose
// outcome stayed unknown at the start's bound, the commit landed and its reply lost while the store
// answered nothing more. A store that opens no connection is
// TestAStartWhoseTakeOfTheCopiesFailsRunsNoRoleUntilATakeSendsTheCopy's, and a statement cut short
// in its middle is internal/store's
// TestAWorkspaceCopyTakeCutShortInTheMiddleOfAStatementIsTheStoresAndRecordsNothing. A take the
// store answered with a definite error, here a trigger that raises (SQLSTATE P0001), is met again
// by every take after it, so retried uncharged it would hold the claim for ever: its start fails
// with the take's error, which is not the sentinel, and the supervisor charges it as a failed
// launch. It is held all the same, since no copy owed to the pod reached its launcher: no
// launcher_start is sent.
//
// The control (the pull request records the run): the hop wrapping every error of the take in the
// sentinel, as before, which fails the first row.
func TestATakeTheStoreAnsweredWithAnErrorIsNoStoreHoldAndOneItDidNotAnswerIs(t *testing.T) {
	t.Run("a take the store refused", func(t *testing.T) {
		st := bundleStore(t)
		saveReleaseBundle(t, st, savedWorkspaceBundle())
		refuseRestoreAsks(t, st)
		g := newRig(t, nil, withSavedWorkspace(st))
		if err := spawnHeld(t, g, workerSpec(t), "take"); !strings.Contains(err.Error(), "could not be taken") ||
			!strings.Contains(err.Error(), "SQLSTATE P0001") || errors.Is(err, runtime.ErrWorkspaceCopyStore) {
			t.Fatalf("the start whose take the store refused answered %v; want the take's refusal, which is not runtime.ErrWorkspaceCopyStore", err)
		}
	})
	t.Run("a take whose outcome stays unknown", func(t *testing.T) {
		st := bundleStore(t)
		saveReleaseBundle(t, st, savedWorkspaceBundle())
		faulted, fault := faultedBundleStore(t, st)
		fired := fault.Cut(bundleTake, testdb.CutAfterCommitRefusing)
		g := newRig(t, nil, withOptions(func(o *Options) {
			o.SavedWorkspaceBundle, o.WorkspaceBundleRestored = takeCopies(faulted), st.KeepRestoredWorkspaceCopy
		}))
		requireStoreHeld(t, g, workerSpec(t), "take", "the store did not say whether the transaction committed")
		testdb.Met(t, fired)
	})
}

// refuseEveryConnection has faulted, a store through fault's proxy, answer nothing: the proxy
// closes every connection it holds and each new one at once, and the pool lets go of the ones it
// held, so each ask meets a connection that never opens, as on a store that is down.
func refuseEveryConnection(faulted *store.Store, fault *testdb.CommitFault) {
	fault.Refuse(true)
	faulted.Pool().Reset()
}

// refuseRestoreAsks has st's database refuse the record of every pod's first ask for a restore
// with a trigger that raises (SQLSTATE P0001), a definite error every later take meets again,
// until the trigger refuse_restore_ask is dropped.
func refuseRestoreAsks(t *testing.T, st *store.Store) {
	t.Helper()
	for _, statement := range []string{
		`create function refuse_restore_ask() returns trigger language plpgsql as $$ begin raise exception 'the store cannot be read'; end $$`,
		`create trigger refuse_restore_ask before insert on workspace_restore_asks for each row execute function refuse_restore_ask()`,
	} {
		if _, err := st.Pool().Exec(context.Background(), statement); err != nil {
			t.Fatalf("make the pod's take fail: %v", err)
		}
	}
}

// spawnHeld spawns spec on g, wants the start held — it fails, and the launcher is sent neither a
// launcher_start nor a launcher_stop (stopIssuedChild skips a held start) — and answers its error.
// what names the act that failed.
func spawnHeld(t *testing.T, g *rig, spec runtime.SpawnSpec, what string) error {
	t.Helper()
	g.launcher(spec.Claim)
	_, err := g.r.Spawn(g.ctx, spec)
	if err == nil {
		t.Fatalf("the start whose %s failed answered no error", what)
	}
	sent := g.sent(spec.Claim)
	for _, held := range []string{shimwire.TypeLauncherStart, shimwire.TypeLauncherStop} {
		if countOf(sent, held) != 0 {
			t.Fatalf("the start whose %s failed sent the launcher %v; want no %s", what, frameTypes(sent), held)
		}
	}
	return err
}

// requireStoreHeld spawns spec on g and wants the start held by the daemon's own store: held
// (spawnHeld), with an error naming want that is runtime.ErrWorkspaceCopyStore.
func requireStoreHeld(t *testing.T, g *rig, spec runtime.SpawnSpec, what, want string) {
	t.Helper()
	if err := spawnHeld(t, g, spec, what); !strings.Contains(err.Error(), want) || !errors.Is(err, runtime.ErrWorkspaceCopyStore) {
		t.Fatalf("the start whose %s failed answered %v; want it failed with the %s's error, runtime.ErrWorkspaceCopyStore", what, err, what)
	}
}
