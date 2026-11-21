package daemon

// The Kubernetes runtime's half of the saved copies of an issue's workspace (R1 of
// dispatch://INFRA-1117): the store's take, keep and drop that the runtime the daemon builds
// (sandboxRuntime) restores a new pod's workspace through. kubernetes.go holds the rest of that
// runtime's options.

import (
	"context"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/sandbox"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// workspaceCopyOptions hands opts the store's three calls on the saved copies of an issue's
// workspace, bound to opts.Project, the runtime's project token.
func workspaceCopyOptions(opts *sandbox.Options, st *store.Store) {
	// The restore of a new pod and the two copies of an issue workspace it chooses between:
	// an idle release puts the issue's unpushed work in one (outbox's release) and the
	// cadence of a running pod in the other. The take reads both and the newest push of the
	// issue's branch the daemon recorded, offers the newer copy to the launcher of the pod
	// that replaces the one the work came off before any role of that pod starts, refuses a
	// running copy older than that push, and closes the older copy unsent, all in one
	// transaction. A refused or closed copy is spent there; a sent one is spent once a role of
	// the pod it was sent to registers (store.Store.RecordPodRegistration, which the
	// supervisor's Deps.PodRegistered calls), and is sent again to the next start of the issue
	// while none has. The take is bound to this daemon's project (savedWorkspaceCopy).
	opts.SavedWorkspaceBundle = savedWorkspaceCopy(st, opts.Project)
	// A launcher's restore report keeps the copy it names as the restoring pod's own running
	// copy, so the work outlives a second end of that pod before its first turn. Which of the
	// two tables the report names is read off its id, and that rule belongs to the store that
	// owns both (store.KeepRestoredWorkspaceCopy).
	opts.WorkspaceBundleRestored = st.KeepRestoredWorkspaceCopy
	// A pod whose Sandbox owns the issue's kept volume mounts the volume every copy taken
	// since that Sandbox was created came off. The runtime asks this drop first for such a
	// pod, and the store deletes each such copy the take would answer, release bundle and
	// running copy alike, without its body, so the pod is sent nothing of it and a release
	// bundle holds the issue's pod no longer.
	opts.HeldWorkspaceCopies = heldWorkspaceCopies(st, opts.Project)
}

// savedWorkspaceCopy is st's take of an issue's saved workspace copies for a pod of project, the
// runtime's project token (sandbox.Options.Project), in the shape the runtime calls it
// (runtime.SavedWorkspaceBundle). A copy is project's to take only where project records its issue
// (store.Store.TakeWorkspaceCopyForRestore): on a database shared with another project's daemon, a
// copy of an issue project does not record is that daemon's, and it is withheld from every pod of
// this daemon and stays in the database, since a row names no project. This daemon's cadence
// writes no running copy of such an issue and drops none, an operator's spawn of it included,
// under the same rule and the same token (store.Store.PutRunningWorkspaceCopy,
// store.Store.DeleteRunningWorkspaceCopy, workflowRuntime.storeRunningCopy), so the spawn's pods
// are answered nothing and the other daemon's copy stands for that daemon's pod.
func savedWorkspaceCopy(st *store.Store, project string) runtime.SavedWorkspaceBundle {
	return func(ctx context.Context, issue, podUID string) (runtime.WorkspaceRestore, error) {
		return st.TakeWorkspaceCopyForRestore(ctx, project, issue, podUID)
	}
}

// heldWorkspaceCopies is st's drop of the saved copies of an issue's workspace that the kept
// volume of a pod of project already holds, in the shape the runtime calls it
// (runtime.HeldWorkspaceCopiesDrop). project is the runtime's project token
// (sandbox.Options.Project). The drop follows the take's project rule
// (store.Store.DropHeldWorkspaceCopies): a pod of this daemon drops no copy of an issue that
// project does not record, and the copy stays in the database for the pod of the project that
// records the issue, since this daemon's cadence writes no running copy of such an issue, an
// operator's spawn of it included (store.Store.PutRunningWorkspaceCopy).
func heldWorkspaceCopies(st *store.Store, project string) runtime.HeldWorkspaceCopiesDrop {
	return func(ctx context.Context, issue, podUID string, keptSince time.Time) (runtime.WorkspaceRestore, error) {
		return st.DropHeldWorkspaceCopies(ctx, project, issue, podUID, keptSince)
	}
}
