package daemon

// podkeep.go is R7 of dispatch://INFRA-3271: an update round replaces a pod only over a clean read
// of its workspace, and a replacement that still takes a disk says what it took. The read itself
// is readPodWorkspace (podupdate.go), made before every replacement a round makes; this file is
// what the read decides, the record of a pod a round kept, the read that may end that record, and
// the architect's notice of what a replacement took.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/store/storetext"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// roundBreaksPod is whether a round whose recorded reasons are reasons (supervise.Claim's
// UpdateReasons, the one string a round records at its open, roundReasons) is over a drift that
// breaks the pod: the string holds runtime.BreakingDrift, the mark a reason of a drift kind that
// dispatch://INFRA-4754 declares breaks the pod, or of a pod below a declared contract floor,
// starts with. Every round INFRA-4754 opens at once is such a round, whatever its spec names as
// the cause, and every other round is one the pod's roles can keep working under. No reason that
// PodDrift gives today carries the mark, so until INFRA-4754 lands no round breaks its pod.
func roundBreaksPod(reasons string) bool {
	return strings.Contains(reasons, runtime.BreakingDrift)
}

// roundKeeps is whether read, the read of the workspace before one of round's replacements, keeps
// the pod: it found work that no branch or tag on origin holds, or it could not be made, the round
// is over a drift the pod's roles can keep working under (roundBreaksPod), and the pod's disk is no
// kept volume that the issue's next pod mounts (roundKeptVolume), which no replacement deletes. A
// round over a drift that breaks the pod is replaced whatever the read found, since no role can
// keep working in that pod, and the issue's architect is told what the replacement took
// (roundReplaced).
func (s *supervision) roundKeeps(issue string, group []supervise.Claim, round, reasons string, read workspaceRead) bool {
	return !read.clean() && !roundBreaksPod(reasons) && !s.roundKeptVolume(issue, group, round)
}

// keepPodForRound ends round with no replacement, because the read before its replacement found
// work that no branch or tag on origin holds, or could not be made, over a drift the pod's roles
// can keep working under (roundKeeps). It first records the keep as a release's decline is
// recorded (recordPodKept): the pod, the work and the moment, and whom it told. Only once that row
// is written does it end the round (endPodUpdates): each claim that answered gets the task the
// round held (supervise.PodUpdateEnded), and the pod and its roles keep working on the old image
// and settings, a turn that runs at the bound included. No new round opens over the pod while the
// record stands (podKeptForWork). A write that fails ends nothing: the round stays open, and the
// next sweep reads the workspace again. group is the issue's claims, the round's members among
// them.
func (s *supervision) keepPodForRound(ctx context.Context, issue, round, pod, reasons string, read workspaceRead, group []supervise.Claim) {
	keep, err := s.supervisor.store.PodKeepOf(ctx, s.plan.project, issue)
	if err == nil {
		var told bool
		if told, err = s.recordPodKept(ctx, issue, round, pod, reasons, read, group, keep.Phase); err == nil {
			s.log.Info("supervise: the read of this update round's pod found work that no branch or tag on origin holds, or could not be made; the round ends with no replacement, and the pod and its roles keep working on the old image and settings",
				append([]any{"issue", issue, "round", round, "pod", pod, "reasons", reasons, "told", told}, read.attrs()...)...)
			s.endPodUpdates(issue, group, round)
			return
		}
	}
	if ctx.Err() == nil {
		s.log.Error("supervise: record the update round's keep of its pod; nothing was replaced, the round stays open, and the next sweep reads the workspace again",
			append([]any{"issue", issue, "round", round, "pod", pod, "error", err}, read.attrs()...)...)
	}
}

// recordPodKept writes the record of a round's keep of pod, the issue's pod of round, for read
// (store.Store.DeclinePodReplacement), and answers whether it queued the message that tells of the
// keep. The record is the issue's decline row (store's idle_release_declines), as #4869 records a
// release's decline: the pod, the work the read found or that it failed, the moment (keptMoment),
// and the role told. The idle rule reads the same row, so the idle release of the kept pod waits
// as for its own decline (pods.IdleDecline).
//
// The open phase's worker (workflow.OpenPhaseRole of issuePhase) is told, where its claim has a
// process in the pod and is not gone, and the read could read: by a role message that the pod
// waits for the push of the work (podKeptMessage). Otherwise the issue's architect is told, by the
// workspace-kept notice (record.WorkspaceKeptNotice, roundKeptMessage), with each claim's state
// and what the read found or why it failed. Each is told once: the store queues a role's message
// only where the decline names other work than the row on record, and the architect's notice
// once for each pod (store's recordDecline). So a read after a turn that finds the same work moves
// the row's moment and tells nobody.
func (s *supervision) recordPodKept(ctx context.Context, issue, round, pod, reasons string, read workspaceRead, group []supervise.Claim, issuePhase phase.Phase) (bool, error) {
	at := keptMoment(read.at, group)
	decline := declineOf(claim.RoleArchitect, pod, at, read.work, read.err)
	if worker, live := openPhaseWorker(group, round, issuePhase); live && read.err == nil {
		message, err := record.NewOutboxRow(issue, record.RoleMessage{Role: string(worker.Token), Body: podKeptMessage(issue, pod, reasons, read.work)}, at)
		if err != nil {
			return false, fmt.Errorf("encode the message of %s's kept pod: %w", issue, err)
		}
		decline.Role, decline.Message = worker.Role, &store.IdleReleaseRow{Kind: string(message.Kind), Payload: message.Payload}
		return s.supervisor.store.DeclinePodReplacement(ctx, s.plan.project, issue, decline)
	}
	claims, err := s.supervisor.store.ClaimStatesIn(ctx, s.plan.project, issue)
	if err != nil {
		return false, err
	}
	notice := record.Notice{Kind: record.WorkspaceKeptNotice, Role: claim.RoleArchitect,
		Reason: roundKeptMessage(issue, pod, reasons, claims, read.work, read.err)}
	message, err := record.NewOutboxRow(issue, notice, at)
	if err != nil {
		return false, fmt.Errorf("encode the notice of %s's kept pod: %w", issue, err)
	}
	decline.Kept, decline.Message = true, &store.IdleReleaseRow{Kind: string(message.Kind), Payload: message.Payload}
	return s.supervisor.store.DeclinePodReplacement(ctx, s.plan.project, issue, decline)
}

// keptMoment is the moment a round's keep records (recordPodKept): the read's own, readAt, or,
// where a claim of the issue was in a turn at the read, a microsecond before the start of the
// earliest such turn. A turn that runs past the bound runs on in the kept pod and can change the
// workspace after the read, so it is a turn after the record, and the read that may end the record
// is made once it has ended (keptPodReadDue).
func keptMoment(readAt time.Time, group []supervise.Claim) time.Time {
	at := readAt
	for _, c := range group {
		if c.State == supervise.StateWorking && !c.LastTurnStart.IsZero() && !c.LastTurnStart.After(at) {
			at = c.LastTurnStart.Add(-time.Microsecond)
		}
	}
	return at
}

// openPhaseWorker is the claim of the open phase's worker of an issue in issuePhase
// (workflow.OpenPhaseRole), where that claim records a process in round's pod and is not gone: the
// one role a round's keep tells by a message of its own (recordPodKept).
func openPhaseWorker(group []supervise.Claim, round string, issuePhase phase.Phase) (supervise.Claim, bool) {
	role := workflow.OpenPhaseRole(issuePhase)
	if role == "" {
		return supervise.Claim{}, false
	}
	for _, c := range group {
		if c.Role == role && inRoundPod(c, round) && !c.Gone() {
			return c, true
		}
	}
	return supervise.Claim{}, false
}

// podKeptForWork is whether an earlier round's keep of pod, the pod the issue's claims record, still
// stands, so that no round opens over the pod at this sweep (reconcileIssuePodUpdate). The keep is
// the issue's decline row naming pod (store.Store.PodKeepOf): a row naming another pod, or none,
// holds nothing here, since the runtime holds another pod for the issue. reasons is the drift this
// sweep read of the pod, which a message of the read below names.
//
// The read that may end the keep is made here, without a round, once it is due
// (keptPodReadDue): through a live role in the pod where one runs, else through the pod's
// launcher (readPodWorkspace). A read that finds nothing that origin does not hold forgets the row
// (store.Store.ForgetIdleDecline), and a round may open over the pod at this same sweep. A read
// that finds work, or that cannot be made, writes the row again (recordPodKept): the same work
// moves its moment and tells nobody, so each turn of the issue costs one read. A store that cannot
// be read keeps the pod for this sweep: a reading nobody took may not open a round that deletes a
// disk.
func (s *supervision) podKeptForWork(ctx context.Context, issue string, group []supervise.Claim, pod, reasons string) bool {
	keep, err := s.supervisor.store.PodKeepOf(ctx, s.plan.project, issue)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("supervise: read whether an update round kept this issue's pod for its disk; no round opens over the pod at this sweep",
				"issue", issue, "pod", pod, "error", err)
		}
		return true
	}
	declined := keep.Decline
	if declined.At.IsZero() || declined.PodUID == "" || declined.PodUID != pod {
		return false
	}
	if !keptPodReadDue(declined, group, s.plan.clock.Now()) {
		s.log.Info("supervise: this issue's pod is kept for work on its disk that no branch or tag on origin holds, or for a read that could not be made; no update round opens over it until a read finds nothing that origin does not hold",
			"issue", issue, "pod", pod, "reason", declined.Reason)
		return true
	}
	round := supervise.PodRound(pod)
	process, running := podProcess(group, round)
	by := viaIssue
	if running {
		by = viaProcess
	}
	read := s.readPodWorkspace(ctx, issue, round, process, by)
	if read.clean() {
		if err := s.supervisor.store.ForgetIdleDecline(ctx, s.plan.project, issue); err != nil {
			if ctx.Err() == nil {
				s.log.Error("supervise: forget the keep of a pod whose read found nothing that origin does not hold; no round opens over the pod at this sweep",
					"issue", issue, "pod", pod, "error", err)
			}
			return true
		}
		s.log.Info("supervise: the read of a kept pod found nothing that no branch or tag on origin holds; the pod is kept no longer, and an update round may open over it",
			append([]any{"issue", issue, "pod", pod}, read.attrs()...)...)
		return false
	}
	told, err := s.recordPodKept(ctx, issue, round, pod, reasons, read, group, keep.Phase)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("supervise: record the read of a kept pod; no round opens over the pod, and the next sweep reads the workspace again",
				append([]any{"issue", issue, "pod", pod, "error", err}, read.attrs()...)...)
		}
		return true
	}
	s.log.Info("supervise: the read of a kept pod found work that no branch or tag on origin holds, or could not be made; the pod stays kept, and no update round opens over it",
		append([]any{"issue", issue, "pod", pod, "told", told}, read.attrs()...)...)
	return true
}

// keptPodReadDue is whether the read that may end a round's keep of a pod (podKeptForWork) is due
// at now, over declined, the keep's row, and group, the issue's claims: no claim of the issue is
// in a turn, and since the row's last read a claim of the issue started a turn (that turn has
// ended), or a process of the issue was started (a start row above the row's started_row), or,
// for a read that could not be made, pods.DeclineUnreadRetry has passed. A role changes the
// workspace only in a turn, so the read is made once for each such turn, once that turn has
// ended.
func keptPodReadDue(declined pods.IdleDecline, group []supervise.Claim, now time.Time) bool {
	if len(workingClaims(group)) > 0 {
		return false
	}
	checked := declined.CheckedAt
	if checked.Before(declined.At) {
		checked = declined.At
	}
	if declined.Reason == pods.DeclineWorkspaceUnread && !now.Before(checked.Add(pods.DeclineUnreadRetry)) {
		return true
	}
	for _, c := range group {
		if c.LastStartRow > declined.StartedRow || c.LastTurnStart.After(checked) {
			return true
		}
	}
	return false
}

// turnsSince is the role of every claim of group that started a turn after at, the moment a read
// of the workspace began: such a turn can have made work the read did not see, so the last word
// on a replacement refuses it (replacementDue, issueReplacementDue).
func turnsSince(group []supervise.Claim, at time.Time) []string {
	var turned []string
	for _, c := range group {
		if c.LastTurnStart.After(at) {
			turned = append(turned, string(c.Role))
		}
	}
	return turned
}

// podKeptMessage is what the open phase's worker is told when a round kept the issue's pod, pod,
// because the read of its workspace found work that no branch or tag on origin holds (work): what
// the read found, that the round ended with no replacement and the role keeps working in the pod,
// what the round was opened for, and that the pod waits for the push of the work. The role reads
// it in its session and people read it in its history, so it follows ASD-STE100.
func podKeptMessage(issue, pod, reasons string, work runtime.WorkspaceWork) string {
	found := append([]string{fmt.Sprintf("The daemon did not replace this issue's pod (pod %s) for an update round. "+
		"The read of the workspace before the replacement found work that no branch or tag on origin holds.", pod)},
		workFound(work)...)
	found = append(found,
		"The round ended with no replacement. The pod and its disk stay, and you keep working in this pod on its old image and settings. "+
			roundOpenedFor(reasons),
		fmt.Sprintf("The daemon does not push for you. Push the work that you keep to legion/%s, and abandon the work that you do not keep. "+
			"The daemon reads the workspace again after each turn of a role of this issue. When a read finds nothing that origin does not hold, a later round replaces the pod. "+
			"You are told this once.", issue))
	return strings.Join(found, " ")
}

// roundKeptMessage is the issue's architect's workspace-kept notice (record.WorkspaceKeptNotice)
// for a pod a round kept (recordPodKept): the read of its workspace before the round's replacement
// found work that no branch or tag on origin holds (failed nil), or could not be made (failed). It
// names the pod, what the read found or why it failed, the state of each claim of the issue, what
// the round was opened for, that the daemon pushes and saves nothing, what ends the keep, and how
// the work is pushed. The architect reads it in its session and people read it in its history, so
// it follows ASD-STE100. The read's error is the far side's text as the daemon stores it
// (storetext.FarSideError).
func roundKeptMessage(issue, pod, reasons string, claims store.ClaimStates, work runtime.WorkspaceWork, failed error) string {
	found := []string{fmt.Sprintf("The daemon did not replace the pod of %s (pod %s) for an update round. The round read the workspace before the replacement.", issue, pod)}
	if failed != nil {
		found = append(found, "The read failed, so the daemon cannot show that the workspace holds nothing that no branch or tag on origin holds. The read failed: "+
			storetext.FarSideError(failed.Error())+".")
	} else {
		found = append(found, "The workspace holds work that no branch or tag on origin holds.")
		found = append(found, workFound(work)...)
	}
	states := "The claims of " + issue + " are: " + claims.String() + "."
	if len(claims) == 0 {
		states = "The daemon holds no claim of " + issue + "."
	}
	found = append(found, states,
		"The round ended with no replacement. The pod and its disk stay, and the roles keep working in the pod on its old image and settings. "+roundOpenedFor(reasons),
		"The daemon does not push this work and does not save it. No update round opens over the pod until a later read finds nothing that origin does not hold, or the runtime makes another pod for the issue. "+
			"The daemon reads the workspace again after a role of the issue starts or runs a turn.",
		fmt.Sprintf("To keep the work, have a role of %s push it to legion/%s. If no role of %s runs, start one in this pod, for example with `legion claims resume` or `legion claims retry`. "+
			"You are told this once for this pod.", issue, issue, issue))
	if failed != nil {
		found = append(found, "Because the read failed, the daemon also reads the workspace again after an hour.")
	}
	return strings.Join(found, " ")
}

// tellWorkspaceTaken tells the issue's architect what a replacement of round's pod took, where the
// read before the replacement found work that no branch or tag on origin holds or could not be
// made (roundReplaced): a round over a drift that breaks the pod (roundBreaksPod), or a pod whose
// disk is a kept volume that the issue's next pod mounts (kept), where the work stays on that
// volume. named is the claims the replacement's line names (notWaitedFor, or lostClaims for a
// replacement by issue or at a launch). It is the workspace-taken notice
// (record.WorkspaceTakenNotice, workspaceTakenMessage), queued once for the replacement made.
func (s *supervision) tellWorkspaceTaken(ctx context.Context, issue, round, pod, reasons string, named []supervise.Claim, read workspaceRead, kept bool) {
	s.tellArchitectTaken(ctx, issue, workspaceTakenMessage(issue, round, pod, reasons, named, read.work, read.err, kept),
		"issue", issue, "round", round, "pod", pod)
}

// tellLaunchTook is the daemon's word of a launch's own replacement of an issue pod
// (runtime.LaunchTook, sandbox.Options.LaunchTook), which the runtime hands it once the suspension
// that takes the pod is written: where the read through the pod's launcher before the replacement
// found work that no branch or tag on origin holds, or no read could be made
// (runtime.TakenPod.Tells), the issue's architect is told once what the replacement took, each
// commit and the working-copy change by name, or that no read could be made and why
// (launchTakenMessage). A read that found nothing tells nobody.
func (s *supervision) tellLaunchTook(ctx context.Context, taken runtime.TakenPod) {
	attrs := []any{"issue", taken.Issue, "pod", taken.PodUID, "claim", taken.Claim, "why", taken.Why}
	if !taken.Tells() {
		s.log.Info("supervise: a launch replaced its issue's pod, and the read of the workspace before it found nothing that no branch or tag on origin holds",
			attrs...)
		return
	}
	s.tellArchitectTaken(ctx, taken.Issue, launchTakenMessage(taken), attrs...)
}

// tellArchitectTaken queues reason as the issue's architect's workspace-taken notice
// (record.WorkspaceTakenNotice), and logs what came of it with attrs.
func (s *supervision) tellArchitectTaken(ctx context.Context, issue, reason string, attrs ...any) {
	if s.enqueue == nil {
		s.log.Warn("supervise: a replacement took a pod whose disk may hold work, and this deployment has no workflow to tell the issue's architect through",
			attrs...)
		return
	}
	written, err := s.enqueue(ctx, issue, record.Notice{Kind: record.WorkspaceTakenNotice, Role: claim.RoleArchitect, Reason: reason})
	switch {
	case err != nil:
		s.log.Error("supervise: tell the issue's architect what a replacement of its pod took", append(attrs, "error", err)...)
	case !written:
		s.log.Info("supervise: the issue's architect is not told what a replacement of its pod took: its issue is another project's, whose rows this daemon writes none of",
			attrs...)
	default:
		s.log.Info("supervise: the issue's architect is told what a replacement of its pod took", attrs...)
	}
}

// workspaceTakenMessage is the issue's architect's workspace-taken notice for a replacement round
// made of pod although the read of its workspace before it found work that no branch or tag on
// origin holds (work, failed nil), or failed (failed): why the round replaced the pod, the roles of
// named with their states, what the read found, and whether the work is gone or on a kept volume
// (kept). The architect reads it in its session and people read it in its history, so it follows
// ASD-STE100. The read's error is the far side's text as the daemon stores it
// (storetext.FarSideError).
func workspaceTakenMessage(issue, round, pod, reasons string, named []supervise.Claim, work runtime.WorkspaceWork, failed error, kept bool) string {
	why := "The round was over a drift that breaks the pod, so no role could keep working in the pod."
	if !roundBreaksPod(reasons) {
		why = "The pod's disk is a kept volume that the issue's next pod mounts, so the replacement did not delete the disk."
	}
	gone, lost := "The pod and its disk are gone.", "Work that is not on a branch at origin is lost."
	if kept {
		gone = "The pod is gone. Its disk is a kept volume, and the volume is still there."
		lost = "Work that is not on a branch at origin is on that volume. The next pod of this issue mounts the volume."
	}
	parts := []string{fmt.Sprintf("The daemon replaced the pod of %s (pod %s) for the update round %s. %s %s %s", issue, pod, round, roundOpenedFor(reasons), why, gone)}
	if len(named) > 0 {
		parts = append(parts, "These roles had not answered the round, and no process of these roles ran in the pod: "+roleStates(named)+".")
	}
	parts = append(parts, readFound(work, failed), lost,
		"Each role must read its issue branch at origin to see the work that it pushed. You are told this once for this replacement.")
	return strings.Join(parts, " ")
}

// launchTakenMessage is the issue's architect's workspace-taken notice for a launch's own
// replacement of an issue pod (runtime.TakenPod): which pod, why the launch replaced it, and what
// the read of its workspace through the pod's launcher found before the replacement, or why no
// read could be made. The architect reads it in its session and people read it in its history, so
// it follows ASD-STE100.
func launchTakenMessage(taken runtime.TakenPod) string {
	role := string(taken.Claim)
	if named, ok := taken.Claim.Role(); ok {
		role = string(named)
	}
	why := taken.Why
	if why == "" {
		why = "the launch could not start the role in that pod"
	}
	return strings.Join([]string{
		fmt.Sprintf("A launch of the %s replaced the pod of %s (pod %s) on its own account, because %s.", role, taken.Issue, taken.PodUID, why),
		"The pod and its disk are gone. A role that ran in the pod lost what it had not pushed.",
		readFound(taken.Work, taken.Err),
		"Work that is not on a branch at origin is lost. Each role must read its issue branch at origin to see the work that it pushed. You are told this once for this replacement.",
	}, " ")
}

// readFound is what a workspace-taken notice says the read before the replacement found
// (workspaceTakenMessage, launchTakenMessage): the error it failed with, or that no read could be
// made and why, or the change in the working copy and the work commits, by id up to
// shimwire.MaxWorkspaceWorkCommits of them and by count all of them.
func readFound(work runtime.WorkspaceWork, failed error) string {
	if failed != nil {
		// The notice is stored and read by the issue's architect, so the read's error, which the
		// shim's or the launcher's jj wrote, is kept in it as any far side's error text is
		// (storetext.FarSideError).
		return fmt.Sprintf("No read of the workspace could be made before the replacement: %s. The daemon does not know if the workspace held work that no branch or tag at origin holds.",
			storetext.FarSideError(failed.Error()))
	}
	found := []string{"Before the replacement, the daemon read the issue workspace. The read found work that no branch or tag at origin holds."}
	if work.Changed {
		found = append(found, fmt.Sprintf("The working copy held a change (commit %s).", work.Head))
	}
	commits := strings.Join(work.Commits, ", ")
	switch {
	case work.Count > len(work.Commits):
		found = append(found, fmt.Sprintf("The workspace held %d commits that no branch or tag at origin holds. The first %d of these commits are: %s.",
			work.Count, len(work.Commits), commits))
	case work.Count == 1:
		found = append(found, fmt.Sprintf("The workspace held one commit that no branch or tag at origin holds: %s.", commits))
	case work.Count > 1:
		found = append(found, fmt.Sprintf("The workspace held %d commits that no branch or tag at origin holds: %s.", work.Count, commits))
	}
	return strings.Join(found, " ")
}
