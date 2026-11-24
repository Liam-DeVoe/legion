package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/procgroup"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// restoreBudget bounds the pod's one restore: an unbundle of a pack of unpushed commits, one jj
// import, a bookmark per saved bookmark and one jj edit. A restore that cannot finish in it is
// one the roles start without, with a notice of what was lost, rather than one the pod waits
// longer for.
const restoreBudget = 5 * time.Minute

// restoreRefPrefix is where the restore's temporary branches live. A bundle that only unbundles
// leaves every saved head out of jj's view — jj's visible heads live in .jj/repo, which died with
// the released pod — so each saved head is given a branch under refs/heads, which `jj git import`
// makes a bookmark and a visible head, and the branches are dropped again through jj, which keeps
// the heads visible where deleting the git refs under jj would abandon the commits with them.
const restoreRefPrefix = "legion-restore-"

// bundleRestore is the one workspace-bundle restore of a pod, shared by every role manager of its
// container. The daemon sends the bundle in launcher_bundle slices before it starts any role;
// this holds them in memory — never in a role's private directory, which each start clears — and
// the first role's start applies the whole, digest-checked body once, before any role of the pod
// runs. A pod the daemon sends no bundle to restores nothing and waits for nothing.
//
// The first start also records the workspace in use, on the workspace's own disk
// (workspace.MarkInUse), before any role runs there. From then on every slice that is not one
// already taken is refused, by this process and by any launcher after it over the same disk, so
// no bundle is restored over work a role has done. The record goes with the pod's disk, and a
// workspace provisioning creates again holds none.
//
// A restore that start runs is recorded on the same disk too (workspace.RecordRestore): begun,
// before it runs, and replaced with its outcome, the report the daemon is owed, before the start
// that ran it answers, so before any role starts; the record goes once a connection carries the
// report (sent). So a launcher after it over that disk finds one of four things (read). A restore
// that ended and whose report no connection carried: it refuses the bundle and still owes the
// report. A restore begun and cut off, its container killed in the middle: no role has run there,
// so it takes the copy the daemon sends again, restores it, and starts no role until one does; the
// daemon sends it again only while no role of the pod has registered and its take still offers
// that copy, and otherwise each start is refused until the claim fails. A record it cannot read,
// which says nothing of the roles: it reads the in-use record, and counts the restore as cut off
// only where no role has started on the workspace, and blocks every slice and every start where
// one may have. No record: it refuses the bundle.
type bundleRestore struct {
	mu sync.Mutex
	// sequence is the id of the slice sequence being assembled, with the count, the digest and
	// the release its slices agree on, and slices what has arrived of it. release is the id of
	// the release that saved the body, which the report the daemon acts on carries back (apply).
	sequence string
	count    int
	digest   string
	release  int64
	slices   [][]byte
	// results answers a repeated slice as a repeated launcher_start id is answered: the same
	// result for the same payload, a refusal for a different one under an id already used.
	results map[string]shimwire.LauncherBundleResult
	// body is the whole bundle, joined from the slices and held until a start applies it, and
	// released there: the slices hold the same bytes, and nothing reads the join again (apply).
	body    []byte
	applied bool
	// restored is whether that run put the saved work back, which the generation is told so that
	// the agent's own notice says what its workspace holds.
	restored bool
	notice   string
	// confirm is the report the daemon is owed for the restore that has run and is still unsent:
	// the daemon keeps its saved copy until one arrives, so this is held until a connection
	// carries it and sent again on every connection until one does (manager.reportRestore). It is
	// recorded on the workspace's disk as well (settle), and a launcher restarted over that disk
	// starts owing it (read).
	confirm *shimwire.LauncherRestored
	// begun is the workspace's disk holding the record of a restore this process began and has
	// not yet replaced with its outcome (settle), which no role may start beside. While it is set,
	// the in-use record is this process's own, written for that restore, and no role's use of the
	// workspace (inUse).
	begun bool
	// cutOff is why the workspace takes the copy the daemon sends again though its disk may mark
	// it in use: a restore an earlier launcher began there and never ended, its text naming that
	// restore's release, or a record of one that could not be read on a workspace no role has
	// started on, its text naming why (read). nil when there is neither, and once a restore of
	// this process has ended. No role has run on the workspace since, so a slice is taken (fence),
	// the copy sent again is restored, and a start with no copy to restore runs no role (apply).
	cutOff error
	// blocked is why the workspace takes no slice and starts no role: a record of a restore that
	// could not be read where the workspace is marked in use, or where its in-use record could
	// not be read either (read). It holds until the record reads, which each start and each slice
	// tries again (fence, apply).
	blocked error
	// start starts each jj and git of the restore (restoreWorkspace): the launcher's pidGuard,
	// which holds each one out of PID 1's reaper until its own wait has read its exit.
	start procgroup.Starter
	// workspace is the pod's workspace (Config.Workspace), the one directory the restore works
	// and records in use, and "" for a launcher that works none, which restores nothing.
	workspace string
	// recordRestore, removeRecord and markInUse are the restore's writes to the workspace's disk,
	// workspace.RecordRestore, workspace.RemoveRestoreRecord and workspace.MarkInUse: each is a
	// test's seam for a write that fails.
	recordRestore func(dir string, record workspace.RestoreRecord) error
	removeRecord  func(dir string) error
	markInUse     func(dir string) error
}

// The answers to a slice the restore refuses whatever its sequence: one over a workspace a
// restore or a role has started on, and one to a launcher that works no workspace at all.
const (
	workspaceInUseRefusal = "a restore or a role has started on this pod's workspace, so a bundle is not restored over its work"
	noWorkspaceRefusal    = "this launcher works no workspace, so it restores no bundle"
)

// newBundleRestore is a pod's restore into the workspace at dir, whose jj and git start starts. A
// nil start panics here, at the launcher's start, rather than at the pod's first put-back. It
// reads what an earlier launcher process left on the same disk (read).
func newBundleRestore(start procgroup.Starter, dir string) *bundleRestore {
	if start == nil {
		panic("launcher: the workspace restore was given no starter")
	}
	r := &bundleRestore{results: map[string]shimwire.LauncherBundleResult{}, start: start, workspace: dir,
		recordRestore: workspace.RecordRestore, removeRecord: workspace.RemoveRestoreRecord, markInUse: workspace.MarkInUse}
	if dir != "" {
		r.read()
	}
	return r
}

// read takes what the record of a restore an earlier launcher ran on the workspace says
// (workspace.ReadRestoreRecord): at this launcher's start, and again at each start and each slice
// while the workspace is blocked, so a record removed by hand is read at the next one.
//
// A restore that ended and whose report no connection carried is owed from the start: that roles
// container restarted after its restore ran and before the daemon heard of it, and the daemon
// keeps the copy as the pod's own only on that report. This launcher has applied nothing, but the
// in-use record the earlier one wrote before its restore is on the disk, so every slice is still
// refused (fence). The record can also be one an earlier pod left on a kept volume, which this
// pod's launcher then reports as its own: that is safe, because the daemon keeps nothing for a
// report from a pod the copy was not sent to (store.KeepRestoredWorkspaceCopy reads the copy's
// attempt), and a copy that was sent to this pod as well had its work put on the disk this pod
// mounts by the restore the record reports.
//
// A restore begun and never ended is cutOff: its record was written only where no role had
// started on the workspace, and no role starts beside it. A record that cannot be read proves
// nothing of the roles: on a kept volume it can be an earlier pod's, whose roles worked the disk
// after it. So the in-use record decides. Where it is absent, no role has started there, and the
// record counts as a restore cut off: counted as none, the roles would start on a workspace no
// restore may have put the work back into. Where it is present, or cannot be read, the workspace
// is blocked: a copy restored there could move a role's work back, and a role started there could
// run on a restore left half done, so neither runs until the record reads.
func (r *bundleRestore) read() {
	r.cutOff, r.blocked = nil, nil
	record, ok, err := workspace.ReadRestoreRecord(r.workspace)
	switch {
	case err != nil:
		marked, inUseErr := workspace.InUse(r.workspace)
		if !marked && inUseErr == nil {
			r.cutOff = fmt.Errorf("the record of a restore into the workspace %s could not be read (%v) on a workspace no role has started on, so it counts as a restore cut off before it ended", r.workspace, err)
			return
		}
		inUse := "the workspace is in use"
		if inUseErr != nil {
			inUse = fmt.Sprintf("its in-use record could not be read either (%v)", inUseErr)
		}
		r.blocked = fmt.Errorf("the record of a restore into the workspace %s could not be read (%v), and %s, so neither a restore nor a role start is safe there: the roles do not start and no copy is taken until the record reads, which removing a damaged .jj/legion-restore from the workspace by hand does, and so does putting the workspace's .jj directory back where .jj is a file or a link, or until the pod is replaced, which on a kept volume takes a reset of that volume",
			r.workspace, err, inUse)
	case ok && record.Ended:
		r.confirm = &record.LauncherRestored
	case ok:
		r.cutOff = fmt.Errorf("a restore of release %d into the workspace %s was cut off before it ended", record.Release, r.workspace)
	}
}

// logFound writes, through logf, what this launcher found on the workspace at its start that
// keeps a start from running a role as it is (read): a restore cut off, or a blocked workspace.
func (r *bundleRestore) logFound(logf func(format string, args ...any)) {
	switch {
	case r.blocked != nil:
		logf("launcher: %v", r.blocked)
	case r.cutOff != nil:
		logf("launcher: %v: a copy the daemon sends again is restored, and with none sent the roles do not start", r.cutOff)
	}
}

// restoreResult is what one start's call on the pod's restore answers: whether the workspace
// holds the released work, and the notice the role's log and the agent's own message carry.
// Unrecorded is why the report of the restore could not be recorded on the workspace's disk,
// which the start logs and which stops no role: the report still stands in memory, and only a
// launcher restarted before a connection carries it no longer owes it (settle).
type restoreResult struct {
	Restored   bool
	Notice     string
	Unrecorded error
}

// receive takes one slice of the bundle. The sequence is rebuilt in memory and nothing is written
// anywhere until its digest matches the one every slice declares, so a body that did not arrive
// as it was sent reaches no workspace. The last slice of a whole sequence is answered Held: the
// launcher holds the bundle and will restore it before the pod's first role starts. The daemon
// keeps its own copy past that answer and drops it only on the restore's own outcome, which the
// launcher reports in a launcher_restored of its own (apply, manager.reportRestore).
//
// A slice already taken is answered as it was. Every other slice is refused once a restore or a
// role has started on the workspace (fence), whatever its sequence: the rest of a sequence that
// began before the first start, and every slice to a launcher restarted over the same disk, but
// one restarted over a restore that was cut off before any role ran (cutOff). Every slice to a
// blocked workspace is refused too.
func (r *bundleRestore) receive(frame shimwire.LauncherBundle) shimwire.LauncherBundleResult {
	if err := frame.Validate(); err != nil {
		return shimwire.LauncherBundleResult{ID: frame.ID, Index: frame.Index, Error: err.Error()}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := frame.ID + "/" + strconv.Itoa(frame.Index)
	if prior, ok := r.results[key]; ok {
		if prior.OK && !bytes.Equal(r.sliceAt(frame.ID, frame.Index), frame.Payload) {
			return shimwire.LauncherBundleResult{ID: frame.ID, Index: frame.Index,
				Error: "launcher_bundle reused an id with a different payload"}
		}
		return prior
	}
	if refusal := r.fence(); refusal != "" {
		return shimwire.LauncherBundleResult{ID: frame.ID, Index: frame.Index, Error: refusal}
	}
	if frame.ID != r.sequence {
		r.sequence, r.count, r.digest, r.release = frame.ID, frame.Count, frame.SHA256, frame.Release
		r.slices = make([][]byte, frame.Count)
		clear(r.results)
	}
	if frame.Count != r.count || frame.SHA256 != r.digest || frame.Release != r.release {
		return shimwire.LauncherBundleResult{ID: frame.ID, Index: frame.Index,
			Error: fmt.Sprintf("launcher_bundle %d of %d of release %d does not match its sequence's %d slices of %s of release %d",
				frame.Index, frame.Count, frame.Release, r.count, r.digest, r.release)}
	}
	r.slices[frame.Index] = frame.Payload
	result := shimwire.LauncherBundleResult{ID: frame.ID, Index: frame.Index, OK: true}
	if whole := joined(r.slices); whole != nil {
		if got := shimwire.BundleDigest(whole); got != r.digest {
			r.sequence, r.count, r.digest, r.release, r.slices = "", 0, "", 0, nil
			clear(r.results)
			return shimwire.LauncherBundleResult{ID: frame.ID, Index: frame.Index,
				Error: fmt.Sprintf("launcher_bundle sequence rebuilt to %s, not the %s its slices declared", got, frame.SHA256)}
		}
		r.body = whole
		result.Held = true
	}
	r.results[key] = result
	return result
}

// fence is why the restore refuses a slice it has not taken, and "" when it takes one: a launcher
// with no workspace restores nothing; a workspace this process has applied to takes no bundle; a
// blocked one takes none, read again first (read); and one a role may have worked takes none
// (inUse). It is called under mu.
func (r *bundleRestore) fence() string {
	switch {
	case r.workspace == "":
		return noWorkspaceRefusal
	case r.applied:
		return workspaceInUseRefusal
	}
	if r.blocked != nil {
		r.read()
	}
	switch {
	case r.blocked != nil:
		return r.blocked.Error()
	case r.inUse():
		return workspaceInUseRefusal
	}
	return ""
}

// inUse is whether a role may have worked the workspace, which then takes no restore: its disk
// marks it in use (workspace.InUse), or the mark cannot be read. Neither counts while this process
// holds a restore begun (begun), whose in-use record is its own, nor over a restore cut off
// (cutOff), which no role ran beside. It is called under mu.
func (r *bundleRestore) inUse() bool {
	if r.begun || r.cutOff != nil {
		return false
	}
	marked, err := workspace.InUse(r.workspace)
	return marked || err != nil
}

// sliceAt is the payload already held for one slice of id, nil when that is not the sequence
// being assembled.
func (r *bundleRestore) sliceAt(id string, index int) []byte {
	if id != r.sequence || index >= len(r.slices) {
		return nil
	}
	return r.slices[index]
}

// joined is a slice sequence's whole body once every slice has arrived, and nil while one has
// not: the pod's workspace bundle (bundleRestore) and its leak check (checkInstall) alike.
func joined(slices [][]byte) []byte {
	size := 0
	for _, slice := range slices {
		if slice == nil {
			return nil
		}
		size += len(slice)
	}
	body := make([]byte, 0, size)
	for _, slice := range slices {
		body = append(body, slice...)
	}
	return body
}

// apply records the workspace in use and restores the held bundle into it, once per pod, before
// the start that called it runs its role, and answers what that start has to say: nothing at all
// when no bundle was sent, when one was already restored with nothing to say, when the workspace
// was in use before this process applied, or when the generation or the launcher works no
// workspace. A second role starting while the restore runs waits here for it and is given the
// same notice.
//
// A restore it runs is recorded on the workspace's disk as begun (workspace.RecordRestore) before
// the in-use record and the restore are written, and every answer that lets a role start has the
// record replaced with the restore's outcome first (settled). So no role starts while the disk
// says a restore is under way, and a launcher after this one that finds a restore begun and never
// ended knows no role has run there (cutOff). The begun record comes before the in-use one, so a
// container killed between the two leaves a disk that reads as a restore cut off, never as one a
// role may have worked.
//
// It fails closed. A workspace it cannot record in use, or on which it cannot record the restore
// it begins, is its error, and the start must run no role there: a later launcher over that
// workspace could restore a bundle over the role's work, or take a restore left half done for a
// workspace a role has worked. The error leaves the restore as it was, a held body still held, so
// the next start tries the records again, and slices are still taken, since no role has started;
// an in-use record this process wrote before such an error is its own (begun), so the next start
// restores all the same. A blocked workspace is its error (read). So is a restore cut off on this
// disk whose copy has not arrived again: the workspace may hold part of that restore, the work is
// in the daemon's database alone, and no role starts on it until a start finds the copy held and
// restores it. The daemon sends the copy again only while no role of the pod has registered and
// its take still offers that copy, which a push newer than a running copy refuses; otherwise each
// start is refused until the claim fails, and on a kept volume the record stays for the later pods
// of its Sandbox until the volume is reset.
func (r *bundleRestore) apply(ctx context.Context, env []string) (restoreResult, error) {
	// A manager built without one — a test's, which serves no daemon that could send a bundle —
	// restores nothing and waits for nothing, as does a role or a launcher that works no
	// workspace.
	if r == nil || r.workspace == "" || envValue(env, "LEGION_WORKSPACE") == "" {
		return restoreResult{}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// The restore has run: it is answered from what it left, since the body it read went with
	// it. This is read before the body, which a restore that has run no longer holds. A record of
	// it that still says it is under way keeps the role from starting (settle).
	if r.applied {
		return r.settled()
	}
	if r.blocked != nil {
		if r.read(); r.blocked != nil {
			return restoreResult{}, r.blocked
		}
	}
	if r.cutOff != nil && r.body == nil {
		return restoreResult{}, fmt.Errorf("%w, so the workspace may hold part of it, and no copy of the work has been sent again: the roles do not start until a start finds the copy sent again, which the daemon sends only while no role of the pod has registered and its take still offers that copy, and otherwise each start is refused until the claim fails",
			r.cutOff)
	}
	restoring := r.body != nil && !r.inUse()
	if restoring {
		if err := r.recordRestore(r.workspace, workspace.RestoreRecord{LauncherRestored: shimwire.LauncherRestored{ID: r.sequence, Release: r.release}}); err != nil {
			return restoreResult{}, fmt.Errorf("record on the workspace %s that the restore of release %d begins, before a role starts on it: %w", r.workspace, r.release, err)
		}
		r.begun = true
	}
	if err := r.markInUse(r.workspace); err != nil {
		return restoreResult{}, fmt.Errorf("mark the workspace %s in use before a role starts on it: %w", r.workspace, err)
	}
	r.applied = true
	// The joined body is the bundle's bytes a second time: the slices hold the same bytes, and
	// they stay, since a repeated slice is answered by comparing it with the one held (sliceAt).
	// The join is let go here instead, so a 40 MiB bundle is not resident twice for the pod's
	// whole life; nothing reads it again, since every later apply is answered above. A workspace
	// already in use is restored nothing: the work there is a role's, and the bundle stays in the
	// daemon's database, as for every restore that cannot run.
	body := r.body
	r.body = nil
	if restoring {
		bundle, err := shimwire.ParseWorkspaceBundle(body)
		if err != nil {
			r.notice = fmt.Sprintf("the released workspace's saved work could not be read (%v); this workspace holds only what was pushed,"+
				" and the bundle of release %d stays in the daemon's database, which sends it to no pod again once a role of this pod has registered", err, r.release)
		} else {
			r.restored, r.notice = restoreWorkspace(ctx, r.start, r.workspace, env, r.release, bundle)
		}
		r.confirm = &shimwire.LauncherRestored{ID: r.sequence, Release: r.release, OK: r.restored}
		if !r.restored {
			r.confirm.Error = r.notice
		}
		r.cutOff = nil
	}
	return r.settled()
}

// settled is a start's answer once apply has run, whether or not it restored: the restore's
// outcome, once the record of a restore this process began is settled (settle), whose error is the
// start's. Every answer of apply that lets a role start is this one, so none leaves a begun record
// on the disk behind a role.
func (r *bundleRestore) settled() (restoreResult, error) {
	unrecorded, err := r.settle()
	if err != nil {
		return restoreResult{}, err
	}
	return restoreResult{Restored: r.restored, Notice: r.notice, Unrecorded: unrecorded}, nil
}

// settle replaces the record of the restore this process began (begun) with its outcome, the
// report in confirm, before any role starts: a disk that still read as a restore under way would
// have a launcher after this one take the copy sent again and restore it over the roles' work
// (cutOff). An outcome that cannot be recorded is answered as unrecorded once the begun record is
// removed in its place: the report still stands in memory, and only a launcher restarted before a
// connection carries it no longer owes it. A begun record that can be neither replaced nor removed
// is the error that keeps each start's role from starting, and each later start tries again. It
// is called under mu.
func (r *bundleRestore) settle() (unrecorded, err error) {
	if !r.begun {
		return nil, nil
	}
	if report := r.confirm; report != nil {
		unrecorded = r.recordRestore(r.workspace, workspace.RestoreRecord{LauncherRestored: *report, Ended: true})
	}
	if r.confirm == nil || unrecorded != nil {
		if err := r.removeRecord(r.workspace); err != nil {
			return unrecorded, fmt.Errorf("the restore of release %d has ended, but its record on the workspace %s still says it is under way and could not be removed, so no role starts there: %w",
				r.release, r.workspace, errors.Join(unrecorded, err))
		}
	}
	r.begun = false
	return unrecorded, nil
}

// owed is the report the daemon has not been given yet, and nil when none is: every connection
// of every role of the pod sends it until one of them gets it across, and so does a launcher
// restarted over the same disk (newBundleRestore), since a restore the daemon never heard of
// leaves the saved work in its database for a pod that no longer needs it.
func (r *bundleRestore) owed() *shimwire.LauncherRestored {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.confirm
}

// sent forgets the report once a connection has carried it, and removes the restore's record
// from the workspace's disk, so no launcher after this one over it owes the report again. A
// report the daemon read but could not act on is the daemon's to retry, not the launcher's: it
// keeps its copy of the work, which it sends no pod again once a role of this pod has registered
// (internal/runtime/sandbox's restoreWorkspace). A record that cannot be removed is sent's error,
// which the caller logs: the report is forgotten here all the same, and a launcher restarted over
// the disk sends it again.
func (r *bundleRestore) sent(report *shimwire.LauncherRestored) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.confirm != report {
		return nil
	}
	r.confirm = nil
	if err := r.removeRecord(r.workspace); err != nil {
		return fmt.Errorf("the restore's report reached the daemon, but its record on the workspace %s could not be removed, so a launcher restarted over this disk sends it again: %w", r.workspace, err)
	}
	r.begun = false
	return nil
}

// restoreWorkspace puts one saved bundle back into the workspace at dir and answers the notice of
// what it restored, or of what was lost and why. start starts each of its jj and git commands
// (procgroup.Starter). release is the idle release whose save built the bundle, which the notice
// of a failure names: that is the bundle an operator reads out of the daemon's database, since the
// daemon sends a bundle to no pod again once a role of the pod it was sent to has registered, and
// the roles of this pod start, and register, whatever the restore did. A restore that fails leaves
// the workspace as provisioning made it — what the issue's branch holds — and the roles start on
// that, with the saved work in the database alone for the life of the issue's pods.
func restoreWorkspace(ctx context.Context, start procgroup.Starter, dir string, env []string, release int64, b shimwire.WorkspaceBundle) (bool, string) {
	// Every reason is written as one line (oneLine), however many lines the tool that failed
	// wrote: the notice goes to the container's log, where one line is one notice.
	lost := func(reason string) (bool, string) {
		return false, fmt.Sprintf("the released workspace's saved work was not restored (%s); this workspace holds only what was pushed,"+
			" and the work of %d head(s) and %d bookmark(s) saved at %s stays in the daemon's database as the bundle of release %d, which it sends to no pod again once a role of this pod has registered",
			oneLine(reason), len(b.Tips), len(b.Bookmarks), b.At.UTC().Format(time.RFC3339), release)
	}
	// A bundle is applied to the workspace the pod that replaces the released one provisioned,
	// and to no other. The daemon holds that rule with a record of its own: it records the pod it
	// sends a bundle to, and once a role of that pod has registered it sends that bundle to no pod
	// and no launcher again (store.TakeWorkspaceCopyForRestore). The workspace holds it with records
	// on its own disk (bundleRestore): the in-use record the pod's first role start writes before
	// this restore runs, and the record of this restore, begun before it runs and ended before any
	// role starts. So a roles container that restarts before its pod's first role start is sent the
	// bundle again and restores it over a workspace nothing has touched, and one that restarts
	// after a restore ended refuses every slice, and the report of that restore, which its record
	// keeps until a connection carries it, still reaches the daemon. One that restarts after a
	// restore was killed in the middle finds the restore begun and never ended, so it knows no role
	// has run there: it takes the bundle the daemon sends again and runs this restore again, which
	// first clears the temporary branches the killed one left (clearRestoreBranches), and it starts
	// no role until a restore ends.
	//
	// This check is the workspace's own too, and it catches the one thing the daemon cannot see: a
	// bundle that reached the very workspace it was saved from, whose creation record
	// (workspace.Created) is older than the save. Applying there would `jj edit` back to the saved
	// head and move bookmarks `--allow-backwards` over whatever that workspace holds now.
	created, ok, err := workspace.Created(dir)
	switch {
	case err != nil:
		return lost(err.Error())
	case !ok:
		return lost(fmt.Sprintf("the workspace %s records no creation, so it cannot be told from the one the work was saved from", dir))
	case !created.After(b.At):
		return lost(fmt.Sprintf("the workspace %s was created at %s, before the work was saved, so it is the workspace the work came out of and not one provisioned since",
			dir, created.UTC().Format(time.RFC3339)))
	}
	gitDir, err := workspace.GitDir(dir)
	if err != nil {
		return lost(err.Error())
	}
	file, err := os.CreateTemp("", "legion-restore-*.bundle")
	if err != nil {
		return lost(fmt.Sprintf("make the bundle's file: %v", err))
	}
	path := file.Name()
	defer os.Remove(path)
	_, err = file.Write(b.Bundle)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return lost(fmt.Sprintf("write the bundle: %v", err))
	}
	ctx, cancel := context.WithTimeout(ctx, restoreBudget)
	defer cancel()
	// The restore runs its jj and git through the runner the save that built this bundle ran
	// through (internal/workspace): it is what names a stale git lock in a failure's report and
	// what stops a child holding the pipe past the budget.
	started := time.Now()
	run := func(stdin []byte, args ...string) (string, error) {
		return workspace.RunCommand(ctx, start, restoreBudget, started, env, dir, stdin, args)
	}
	if err := clearRestoreBranches(run, gitDir, dir); err != nil {
		return lost(err.Error())
	}
	// The bundle's own prerequisites are the commits the new pod's workspace fetch was expected
	// to hold; one that is gone — a branch force-pushed away since the release — is what git
	// refuses here, and it is the whole of what the notice has to name.
	if _, err := run(nil, "git", "--git-dir="+gitDir, "bundle", "unbundle", path); err != nil {
		return lost(err.Error())
	}
	if _, err := run(workspace.RefUpdates("create", restoreRefHeadPrefix, b.Tips), "git", "--git-dir="+gitDir, "update-ref", "--stdin"); err != nil {
		return lost(err.Error())
	}
	if _, err := run(nil, "jj", "git", "import", "-R", dir); err != nil {
		return lost(err.Error())
	}
	// A bookmark's name comes from the workspace the agent worked in, so it is an argument no
	// caller chose: `--` keeps jj from reading one that begins with a dash as an option of its
	// own. A name jj will not take costs that one bookmark, named in the notice, and never the
	// heads and the working copy the restore has already put back.
	var skipped []string
	for _, name := range slices.Sorted(maps.Keys(b.Bookmarks)) {
		if _, err := run(nil, "jj", "bookmark", "set", "-R", dir, "--ignore-working-copy", "--allow-backwards",
			"-r", b.Bookmarks[name], "--", name); err != nil {
			skipped = append(skipped, name)
		}
	}
	if _, err := run(nil, "jj", "edit", "-R", dir, b.Head); err != nil {
		return lost(err.Error())
	}
	// The temporary branches go through jj, which keeps each head visible; deleting the git refs
	// under it would have jj abandon the commits they made visible, taking the restored work with
	// them. Whatever jj did not export is deleted after, so none is left in the repository.
	for index := range b.Tips {
		if _, err := run(nil, "jj", "bookmark", "delete", "-R", dir, "--ignore-working-copy", "--", restoreRefPrefix+strconv.Itoa(index)); err != nil {
			return lost(err.Error())
		}
	}
	if _, err := run(workspace.RefUpdates("delete", restoreRefHeadPrefix, b.Tips), "git", "--git-dir="+gitDir, "update-ref", "--stdin"); err != nil {
		return lost(err.Error())
	}
	notice := fmt.Sprintf("the released workspace's saved work is restored: %d head(s), %d bookmark(s) and the working copy on %s, saved at %s",
		len(b.Tips), len(b.Bookmarks)-len(skipped), b.Head, b.At.UTC().Format(time.RFC3339))
	if len(skipped) > 0 {
		notice += fmt.Sprintf("; jj would not take %d bookmark name(s), which are not set: %s",
			len(skipped), strings.Join(skipped, ", "))
	}
	return true, notice
}

// clearRestoreBranches drops the temporary branches a restore killed in the middle left in the
// workspace at dir, whose git directory is gitDir, so the restore that runs after it creates its
// own (restoreRefHeadPrefix) rather than fail at their create, and no role finds them. A killed
// restore leaves git refs under restoreRefHeadPrefix, and once its `jj git import` has run, a jj
// bookmark named with restoreRefPrefix for each of them. They are dropped as a restore that ends
// drops its own: the bookmarks through jj first, which keeps each head visible where deleting the
// git refs under jj would abandon the commits with them, then every such git ref jj did not
// export. A workspace that holds none runs one read of each and changes nothing. run runs one jj
// or git command of the restore (restoreWorkspace).
func clearRestoreBranches(run func(stdin []byte, args ...string) (string, error), gitDir, dir string) error {
	listed, err := run(nil, "jj", "bookmark", "list", "-R", dir, "--ignore-working-copy", "-T", `if(remote, "", name ++ "\n")`)
	if err != nil {
		return err
	}
	var bookmarks []string
	for _, name := range workspace.OutputLines(listed) {
		if strings.HasPrefix(name, restoreRefPrefix) {
			bookmarks = append(bookmarks, name)
		}
	}
	if len(bookmarks) > 0 {
		args := append([]string{"jj", "bookmark", "delete", "-R", dir, "--ignore-working-copy", "--"}, bookmarks...)
		if _, err := run(nil, args...); err != nil {
			return err
		}
	}
	refs, err := run(nil, "git", "--git-dir="+gitDir, "for-each-ref", "--format=%(refname)", restoreRefHeadPrefix+"*")
	if err != nil {
		return err
	}
	var deletes strings.Builder
	for _, ref := range workspace.OutputLines(refs) {
		deletes.WriteString("delete " + ref + "\n")
	}
	if deletes.Len() == 0 {
		return nil
	}
	_, err = run([]byte(deletes.String()), "git", "--git-dir="+gitDir, "update-ref", "--stdin")
	return err
}

// restoreRefHeadPrefix is where the restore's temporary branches live while jj imports them: one
// branch per saved head, named by the head's place in the bundle (workspace.RefUpdates).
const restoreRefHeadPrefix = "refs/heads/" + restoreRefPrefix

// oneLine is a report as a notice carries it: its lines joined with "; ", so a notice is one line
// of the container's log however many lines git wrote.
func oneLine(report string) string {
	return strings.Join(workspace.OutputLines(report), "; ")
}
