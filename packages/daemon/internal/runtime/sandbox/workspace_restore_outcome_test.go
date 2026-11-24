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

// A take whose outcome stays unknown past its caller's bound says that the attempt and this pod's
// first ask may be recorded. The run committed, so the row records the attempt to the pod. No role
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
		t.Fatalf("the take whose outcome stayed unknown = %+v (%v), want an error that is store.ErrCommitOutcomeUnknown and says the attempt may be recorded", take, err)
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

// The restored bundle's confirmation runs on the launcher's one report of a restore, so a
// confirmation whose outcome was unknown waits for the store's answer rather than leaving the row
// to hold the pod: the bundle's row goes, and its work stays as the restoring pod's own copy.
func TestARestoredBundleKeepWhoseReplyIsLostKeepsIt(t *testing.T) {
	st := bundleStore(t)
	saved := savedWorkspaceBundle()
	saveReleaseBundle(t, st, saved)
	const podUID = "pod-that-restored-the-bundle"
	if take, err := st.TakeWorkspaceCopyForRestore(context.Background(), testProject, testTree, podUID); err != nil ||
		take.ReleaseBundle.Take != runtime.WorkspaceBundleSend {
		t.Fatalf("the take of the bundle for %s = %+v, %v; want it sent", podUID, take, err)
	}
	faulted, fault := faultedBundleStore(t, st)
	fired := fault.Cut("delete from workspace_bundles", testdb.CutAfterCommitRefusing)
	met := releaseAfter(fault, fired)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := faulted.KeepRestoredWorkspaceBundle(ctx, testTree, podUID, saved.Release); err != nil {
		t.Fatalf("the restored bundle's confirmation = %v, want nil", err)
	}
	if !met.Load() {
		t.Fatal("the armed fault met no COMMIT of the confirmation")
	}
	if _, held, err := st.WorkspaceBundleSize(context.Background(), testTree); err != nil || held {
		t.Fatalf("the store holds the bundle = %t (%v), want its row gone", held, err)
	}
	var source string
	if err := st.Pool().QueryRow(context.Background(), `select source_pod_uid from workspace_running_copies where issue = $1`, testTree).Scan(&source); err != nil || source != podUID {
		t.Fatalf("the running copy's source = %q (%v), want %s: the work stays as the restoring pod's own copy", source, err, podUID)
	}
}

// The restored running copy's confirmation runs on the same one report, so a confirmation whose
// outcome was unknown waits for the store's answer too: the copy becomes the restoring pod's own,
// under a new restore id, with no attempt left on it.
func TestARestoredRunningCopyKeepWhoseReplyIsLostKeepsIt(t *testing.T) {
	st := bundleStore(t)
	saveRunningCopy(t, st, runningWorkspaceCopy("pod-the-running-copy-came-off"))
	const podUID = "pod-that-restored-the-running-copy"
	take, err := st.TakeWorkspaceCopyForRestore(context.Background(), testProject, testTree, podUID)
	if err != nil || take.Running.Take != runtime.WorkspaceBundleSend {
		t.Fatalf("the take of the running copy for %s = %+v, %v; want it sent", podUID, take, err)
	}
	restoreID := take.Running.Bundle.Release
	faulted, fault := faultedBundleStore(t, st)
	fired := fault.Cut("update workspace_running_copies", testdb.CutAfterCommitRefusing)
	met := releaseAfter(fault, fired)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := faulted.KeepRestoredRunningWorkspaceCopy(ctx, testTree, podUID, restoreID); err != nil {
		t.Fatalf("the restored running copy's confirmation = %v, want nil", err)
	}
	if !met.Load() {
		t.Fatal("the armed fault met no COMMIT of the confirmation")
	}
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
