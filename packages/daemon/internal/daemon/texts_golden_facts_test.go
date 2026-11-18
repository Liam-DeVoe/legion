package daemon

import (
	"strings"

	"github.com/sjawhar/legion/daemon/internal/texts"
)

// goldenMainFacts is, for each kind, the facts its builder on main (b4a566cb) put in its text:
// each id, reference, version, commit, phase and gate state, with the builder each was read from
// (packages/daemon/internal/ at that commit). A sample of the kind must carry each with the value
// the test gave the builder (goldenFactsOf.problems); a Dispatch item among them is a dispatch://
// link. Where main named a pull request by its number alone, the sample names it owner/name#n.
var goldenMainFacts = []goldenFactsOf{
	// Tasks.
	{kind: texts.PhaseAssignment, from: "workflow/effects.go:task, continueLine",
		keys: []string{"issue", "title", "phase"}},
	{kind: texts.PhaseAssignment, variants: []string{"handoff"}, from: "workflow/effects.go:task (Handoff:, Handoff commit:, the tester's verdict, Pull request:)",
		keys: []string{"handoff_phase", "handoff_version", "handoff_commit", "tester_verdict", "pull_request"}},
	{kind: texts.PhaseAssignment, variants: []string{"moved-back"}, from: "workflow/engine.go:transition (a backward move's reason, by the role)",
		keys: []string{"moved_back_from", "reason", "pull_request"}},
	{kind: texts.PhaseAssignment, variants: []string{"review-rejected"}, from: "workflow/review.go:settleRound (the review body as the reason)",
		keys: []string{"body", "pull_request"}},
	{kind: texts.PhaseAssignment, variants: []string{"conflict"}, from: "workflow/review.go:mergeability (the base the head conflicts with)",
		keys: []string{"conflicts_with", "pull_request"}},
	{kind: texts.PhaseAssignment, variants: []string{"retried"}, from: "workflow/engine.go:retryOrEscalate, retryPhase (the architect's words)",
		keys: []string{"reason"}},
	{kind: texts.PhaseAssignment, variants: []string{"stuck-undecided"}, from: "workflow/review.go:undecidedReason (pull request, head, the review set aside)",
		keys: []string{"head", "pull_request", "set_aside"}},
	{kind: texts.PhaseAssignment, variants: []string{"stuck-approved-other-code"}, from: "workflow/review.go:reviewRound (the approved head, the head)",
		keys: []string{"head", "reviewed_head", "pull_request"}},
	{kind: texts.PhaseAssignment, variants: []string{"stuck-merge-state"}, from: "workflow/review.go:stuckMergeStateReason, headChecks (the head, the pull request, the merge state, each check)",
		keys: []string{"head", "pull_request", "merge_state", "not_passed#", "passed", "list_passed"}},
	{kind: texts.PhaseResume, from: "workflow/effects.go:ResumePhaseTask, continueLine",
		keys: []string{"issue", "title", "phase"}},
	{kind: texts.RootResume, from: "workflow/awaiting_reconcile.go:resumeTask (the root, the gate's version, open or closed, armed)",
		keys: []string{"issue", "document_version", "gate_open", "gate_armed"}},
	{kind: texts.RootResume, variants: []string{"approved", "changes-requested", "version"}, from: "workflow/awaiting_reconcile.go:resumeTask (the document and the version answered)",
		keys: []string{"version"}, links: []string{"document"}},
	{kind: texts.RootResume, variants: []string{"retracted", "closed", "ask", "reply", "turn", "declined"}, from: "workflow/awaiting_reconcile.go:resumeTask (the ask)",
		links: []string{"ask"}},
	{kind: texts.RootResume, variants: []string{"closed"}, from: "workflow/awaiting_reconcile.go:resumeTask (who closed the ask)",
		keys: []string{"closed_by"}},
	{kind: texts.RootResume, variants: []string{"approved", "ask"}, from: "workflow/awaiting_reconcile.go:resumeTask (Still with the person:)",
		links: []string{"with_person[0]"}},
	{kind: texts.RootResume, variants: []string{"changes-requested", "version"}, from: "workflow/awaiting_reconcile.go:resumeTask (the approval request's version)",
		keys: []string{"approval_request"}},
	{kind: texts.RootResume, variants: []string{"changes-requested", "turn"}, from: "workflow/awaiting_reconcile.go:resumeTask (Waiting on you:)",
		links: []string{"waiting_on_you[0]"}},
	{kind: texts.RootResume, variants: []string{"declined"}, from: "workflow/awaiting_reconcile.go:resumeTask (the sentences Dispatch wrote, the note)",
		keys: []string{"dispatch_words", "note"}},
	{kind: texts.CheckLook, from: "workflow/check.go:checkTask, wokenBy",
		keys: []string{"look", "woken_by", "pull_request"}},
	{kind: texts.CheckLook, variants: []string{"merge", "named", "retry"}, from: "workflow/check.go:checkTask (merge commit)",
		keys: []string{"merge_commit"}},
	{kind: texts.CheckLook, variants: []string{"named"}, from: "workflow/check.go:wokenBy (the wake named at the last look)",
		keys: []string{"summary"}},
	{kind: texts.CheckLook, variants: []string{"bound-last"}, from: "workflow/check.go:checkTask (This is the last look)",
		keys: []string{"last"}},
	{kind: texts.ArchitectStart, from: "workflow/architect_start.go:ArchitectStartTask (the root)",
		keys: []string{"issue"}},
	{kind: texts.ArchitectStart, variants: []string{"fresh-tree", "earlier-generation"}, from: "workflow/architect_start.go:writeFreshTree, writeEarlierGeneration (the generation)",
		keys: []string{"generation"}},
	{kind: texts.ArchitectStart, variants: []string{"carried-phase"}, from: "workflow/architect_start.go:writeCarriedPhase (the phase, the gate the hand-over brought)",
		keys: []string{"phase", "gate_carried"}},
	{kind: texts.ArchitectStart, variants: []string{"gate-standing"}, from: "workflow/architect_start.go:gateStanding (each step's version and state)",
		keys: []string{"gates[0].version", "gates[0].state", "gates[1].state"}},
	{kind: texts.ArchitectStart, variants: []string{"gate-open"}, from: "workflow/architect_start.go:gateStanding (each step's version and state)",
		keys: []string{"gates[0].version", "gates[0].state"}},
	{kind: texts.KeptDecisions, from: "workflow/parked_decision.go:KeptDecisionsTask (each decision's reference, what the person did, who)",
		keys: []string{"kept[0].decision"}, links: []string{"kept[0].ref"}},
	{kind: texts.KeptDecisions, variants: []string{"several"}, from: "workflow/parked_decision.go:KeptDecisionsTask (the approval kept: step, versions)",
		keys: []string{"kept[1].decision", "approval_step", "approved", "latest"}, links: []string{"kept[1].ref"}},
	{kind: texts.PodWake, variants: []string{"checks"}, from: "workflow/wake.go:WakeTask, settlementDetail (the head, the checks that did not pass)",
		keys: []string{"head", "pull_request", "not_passed#"}},
	{kind: texts.PodWake, variants: []string{"role-message"}, from: "workflow/wake.go:WakeTask (the role topic)",
		keys: []string{"topic"}},
	{kind: texts.PodWake, variants: []string{"agent-message"}, from: "workflow/wake.go:WakeTask (the session)",
		keys: []string{"session"}},
	{kind: texts.PodWake, variants: []string{"backstop"}, from: "workflow/wake.go:podBackstopDue (the role the backstop was armed for)",
		keys: []string{"armed_for"}},
	{kind: texts.RelaunchNote, from: "supervise/relaunch.go:relaunchSentence (the cause)",
		keys: []string{"cause"}},
	{kind: texts.AbandonedSessionNote, from: "supervise/delivery.go:abandonedTask (the session, why)",
		keys: []string{"session", "why"}},
	{kind: texts.AbandonedSessionNote, variants: []string{"worker"}, from: "supervise/delivery.go:abandonedTask (the branch)",
		keys: []string{"branch"}},
	{kind: texts.OutOfMemoryNote, from: "supervise/delivery.go:outOfMemoryTask (OOMKilled)",
		keys: []string{"reason"}},
	{kind: texts.ProviderErrorNote, variants: []string{"status"}, from: "supervise/providererror.go:providerErrorCause (the HTTP status)",
		keys: []string{"http_status"}},

	// Notices to the architect that owns an issue.
	{kind: texts.PhaseFinished, from: "workflow/engine.go:noticeFor (the phase)",
		keys: []string{"issue", "phase"}},
	{kind: texts.PhaseFinished, variants: []string{"handoff"}, from: "workflow/engine.go:noticeFor (the worker's summary and the tester's verdict)",
		keys: []string{"summary", "verdict"}},
	{kind: texts.PhaseFinished, variants: []string{"backward"}, from: "workflow/engine.go:noticeFor (moved back to <to> by the <role>: <words>)",
		keys: []string{"to", "reason"}},
	{kind: texts.WorkerDied, from: "workflow/engine.go:claimFailed",
		keys: []string{"issue", "role"}},
	{kind: texts.WorkerDied, variants: []string{"failure", "tree-architect", "working"}, from: "workflow/engine.go:claimFailed (the issue's phase)",
		keys: []string{"phase"}},
	{kind: texts.WorkerDied, variants: []string{"failure"}, from: "workflow/engine.go:noticeFailure (the cause and its counts)",
		keys: []string{"failure.cause", "failure.launchFailures.count"}},
	{kind: texts.WorkerDied, variants: []string{"working"}, from: "workflow/engine.go:claimFailed (the phase the role does not work)",
		keys: []string{"working"}},
	{kind: texts.WorkerDied, variants: []string{"look"}, from: "workflow/engine.go:claimFailed (a check's look)",
		keys: []string{"look"}},
	{kind: texts.WorkerStalled, from: "daemon/stallbackstop.go:stallRecordInWords (the last step, the end)",
		keys: []string{"issue", "role", "last_step", "end"}},
	{kind: texts.WorkerStalled, variants: []string{"call"}, from: "daemon/stallbackstop.go:stallRecordInWords (the last call)",
		keys: []string{"call_id"}},
	{kind: texts.WorkerSilent, from: "workflow/silence.go:workerSilentRecord, workerSilentDid (the phase entry, the entry given)",
		keys: []string{"issue", "role", "phase", "last_step", "entry", "given"}},
	{kind: texts.WorkerBackgroundStalled, from: "workflow/silence.go:workerBackgroundRecord (the report's time)",
		keys: []string{"issue", "role", "phase", "reported_at"}},
	{kind: texts.Held, from: "workflow/engine.go:claimFailed, retryOrEscalate; effects.go:HeldNotice (the phase held from)",
		keys: []string{"issue", "phase"}},
	{kind: texts.Held, variants: []string{"failure"}, from: "workflow/engine.go:claimFailed (the failure)",
		keys: []string{"failure.cause"}},
	{kind: texts.Held, variants: []string{"escalation"}, from: "workflow/engine.go:retryOrEscalate (escalated, the architect's words)",
		keys: []string{"reason", "escalation"}},
	{kind: texts.PRBlocked, from: "workflow/review.go:recordRound (review_round_cap, rounds counted, conflict rounds)",
		keys: []string{"issue", "cap", "counted", "conflict_rounds"}},
	{kind: texts.PRMerged, from: "workflow/pullrequest.go:merged, waitingForTree (the pull request)",
		keys: []string{"issue", "pull_request"}},
	{kind: texts.PRMerged, variants: []string{"outside-merging"}, from: "workflow/pullrequest.go:merged (the phase)",
		keys: []string{"phase"}},
	{kind: texts.PRMerged, variants: []string{"waiting"}, from: "workflow/pullrequest.go:waitingForTree (each issue it waits for)",
		keys: []string{"waiting_for"}},
	{kind: texts.PRClosedUnmerged, from: "workflow/pullrequest.go:closed (the pull request, the phase)",
		keys: []string{"issue", "pull_request", "phase"}},
	{kind: texts.PullRequestMissing, from: "workflow/pullrequest.go:pullRequestMissing (the commit, the repository, the branch)",
		keys: []string{"issue", "handoff_commit", "repo", "branch"}},
	{kind: texts.MessageRefused, from: "daemon/outbox.go:messagePostRefused, statusWriteRefused",
		keys: []string{"issue", "refused"}},
	{kind: texts.MessageRefused, variants: []string{"message"}, from: "daemon/outbox.go:messagePostRefused (its length)",
		keys: []string{"length"}},
	{kind: texts.MessageRefused, variants: []string{"status"}, from: "daemon/outbox.go:statusWriteRefused (the status, whether it is set)",
		keys: []string{"status", "status_set"}},
	{kind: texts.DesignApproved, from: "workflow/gate.go:gateRegistered, applyArtifact (version, actor, ref)",
		keys: []string{"issue", "version", "actor"}, links: []string{"ref"}},
	{kind: texts.DesignChangesRequested, from: "workflow/gate.go:tellChanges (version, reason, actor, ref)",
		keys: []string{"issue", "version", "actor", "reason"}, links: []string{"ref"}},
	{kind: texts.CarriedGateOpened, from: "workflow/gate.go:applyArtifact (version, actor, the approved issue's ref)",
		keys: []string{"issue", "version", "actor"}, links: []string{"ref"}},
	{kind: texts.ChildClosed, from: "workflow/pullrequest.go:closeMerged, linger.go:leave (<child> is done)",
		keys: []string{"issue"}},
	{kind: texts.ChildStatus, from: "workflow/effects.go:ChildReenteredNotice, ChildStartedNotice; accepted_parent.go; adopt.go; linger.go",
		keys: []string{"issue"}},
	{kind: texts.ChildStatus, variants: []string{"todo", "started", "joined"}, from: "workflow/effects.go:ChildReenteredNotice, ChildStartedNotice; adopt.go:joinedTheTree (the tree)",
		keys: []string{"tree"}},
	{kind: texts.ChildStatus, variants: []string{"started"}, from: "workflow/effects.go:ChildStartedNotice (every issue it waited on)",
		keys: []string{"waited_on"}},
	{kind: texts.ChildStatus, variants: []string{"beneath-accepted"}, from: "workflow/accepted_parent.go:ChildUnderAcceptedParentNotice (the accepted ancestor)",
		keys: []string{"accepted_ancestor"}},
	{kind: texts.ChildStatus, variants: []string{"joined"}, from: "workflow/adopt.go:joinedTheTree (the tree it came from)",
		keys: []string{"from_tree"}},
	{kind: texts.ChildStatus, variants: []string{"reparented"}, from: "workflow/adopt.go:tellReparented (the root, its new parent)",
		keys: []string{"child", "parent"}},
	{kind: texts.ChildStatus, variants: []string{"left"}, from: "workflow/linger.go:leave (the status it left with)",
		keys: []string{"status"}},
	{kind: texts.ChildrenFirst, from: "workflow/children_first_notice.go:ChildrenFirstNotice (each open child and its status)",
		keys: []string{"issue", "children#", "children[0].key", "children[0].status", "children[29].key"}},
	{kind: texts.ChildReleaseStalled, from: "api/architect.go:tellReleaseWait and the release retry; daemon/repositories.go:tellReleaseWaits",
		keys: []string{"issue", "stall"}},
	{kind: texts.ChildReleaseStalled, variants: []string{"unrecorded-blocker"}, from: "api/architect.go:releaseRepository (the child, the blocker, its repository)",
		keys: []string{"child", "blocker", "repo"}},
	{kind: texts.ChildReleaseStalled, variants: []string{"unrecorded-ancestor"}, from: "api/architect.go:releaseChildren (the child, the issue above it)",
		keys: []string{"child", "parent", "ancestor"}},
	{kind: texts.ChildReleaseStalled, variants: []string{"attempts-spent"}, from: "api/architect.go:retryChildRelease (the child, the attempts)",
		keys: []string{"child", "attempts"}},
	{kind: texts.ChildReleaseStalled, variants: []string{"recovered-unrecorded"}, from: "daemon/repositories.go:tellReleaseWaits (the repositories, each child)",
		keys: []string{"repos", "unrecorded[0].child", "unrecorded[1].child"}},
	{kind: texts.ChildReleaseStalled, variants: []string{"let-go-no-todo"}, from: "daemon/repositories.go:tellLetGoWaits at 237f31c7 (the repositories let go, each child with its Dispatch status and its unrecorded parent)",
		keys: []string{"repos", "let_go[0].child", "let_go[0].status", "let_go[1].child", "let_go[1].parent"}},
	{kind: texts.ChildReleaseStalled, variants: []string{"let-go-answered"}, from: "daemon/repositories.go:tellLetGoWaits at 237f31c7 (the repositories let go, each child with its Dispatch status and the mapping's refusal)",
		keys: []string{"repos", "let_go[0].child", "let_go[0].status", "let_go[0].refusal"}},
	{kind: texts.ChildNotRecorded, from: "workflow/repository.go:refuseChild (the child, the refusal)",
		keys: []string{"issue", "child", "refusal"}},
	{kind: texts.CatchUp, from: "workflow/catch_up.go:QueueCatchUp, treeCatchUp (generation, launch, each issue)",
		keys: []string{"issue", "catch_up.generation", "catch_up.launch", "catch_up.issues[0].key"}},
	{kind: texts.CatchUp, variants: []string{"root"}, from: "workflow/catch_up.go:treeCatchUp (each gate step's version and state); parked_decision.go:ParkedDecisionTask",
		keys: []string{"catch_up.gates[0].step", "catch_up.gates[0].version", "catch_up.gates[0].open"}, links: []string{"catch_up.kept.decisions[0].ref"}},
	{kind: texts.ChecksRed, from: "workflow/engine.go:noticeFor, review.go:mergeability (the phase, the base, the pull request)",
		keys: []string{"issue", "phase", "conflicts_with", "pull_request"}},
	{kind: texts.ReviewStuck, from: "workflow/review.go:settleRound, tellStuckMergeState (the reviewer, reviewing, the pull request, the head)",
		keys: []string{"issue", "role", "phase", "pull_request", "head"}},
	{kind: texts.ReviewStuck, variants: []string{"undecided"}, from: "workflow/review.go:settleRound, undecidedReason (the reviewer's topic, the review set aside)",
		keys: []string{"topic", "set_aside"}},
	{kind: texts.ReviewStuck, variants: []string{"approved-other-code"}, from: "workflow/review.go:reviewRound (the head the reviewer approved)",
		keys: []string{"approved_head"}},
	{kind: texts.ReviewStuck, variants: []string{"merge-state"}, from: "workflow/review.go:stuckMergeStateReason, headChecks (the merge state, each check)",
		keys: []string{"merge_state", "passed"}},
	{kind: texts.StatusReasserted, from: "workflow/engine.go:sessionStatusWrite (the session, the status it wrote, the status set back)",
		keys: []string{"issue", "session", "wrote", "status"}},
	{kind: texts.SessionLost, from: "workflow/engine.go:sessionLost (the role, the phase, the detail)",
		keys: []string{"issue", "role", "phase", "detail"}},
	{kind: texts.SessionStoreFault, from: "workflow/engine.go:sessionStoreFault (the role, the phase, the detail)",
		keys: []string{"issue", "role", "phase", "detail"}},

	// Notices to the controller.
	{kind: texts.Triage, from: "admit/admit.go:applyFact (the issue, on the summary)", keys: []string{"issue"}},
	{kind: texts.SlotFree, from: "admit/admit.go:wakeForFreeSlot (the issue, on the summary)", keys: []string{"issue"}},
	{kind: texts.Todo, from: "admit/admit.go:applyFact (the issue, on the summary)", keys: []string{"issue"}},
	{kind: texts.Tick, from: "admit/admit.go:applyFact, Admission.ReportCapabilities (held, blocked, silent whole)",
		keys: []string{"issue", "held#", "blocked#", "silent#", "held[0].issue", "blocked[0].issue", "silent[0].issue"}},
	{kind: texts.Unslotted, from: "admit/admit.go:releaseUnslotted (the status)", keys: []string{"issue", "status"}},
	{kind: texts.Unslotted, variants: []string{"awaiting"}, from: "admit/admit.go:releaseUnslotted (why an awaiting root cannot resume)",
		keys: []string{"unresumable"}},
	{kind: texts.AwaitingPerson, variants: []string{"ask"}, from: "workflow/awaiting_reconcile.go:enterAwaiting (the ask)",
		keys: []string{"issue"}, links: []string{"ask"}},
	{kind: texts.AwaitingPerson, variants: []string{"document"}, from: "workflow/awaiting_reconcile.go:enterAwaiting (the document's version)",
		keys: []string{"issue", "version"}, links: []string{"document"}},
	{kind: texts.ParkedDecision, from: "record/parked_decision.go:RelayParkedDecision (decision, root, ref, actor)",
		keys: []string{"issue", "decision", "root"}, links: []string{"ref"}},
	{kind: texts.ParkedDecision, variants: []string{"changes"}, from: "record/parked_decision.go:RelayParkedDecision (version, reason)",
		keys: []string{"version", "reason"}},
	{kind: texts.UpdateStalled, from: "daemon/podupdate.go:tellUpdateStalled (the round, the pod)",
		keys: []string{"issue", "holds", "update.round"}},
	{kind: texts.UpdateStalled, variants: []string{"drift"}, from: "daemon/podupdate.go:tellUpdateStalled (the round's pod, its drift)",
		keys: []string{"update.pod", "update.images", "update.settings#"}},
	{kind: texts.UpdateStalled, variants: []string{"refused"}, from: "daemon/podupdate.go:tellLateRound (the runtime's refusal)",
		keys: []string{"refusal"}},
	{kind: texts.ProviderErrors, from: "supervise/providererror.go:tellControllerOfProviderErrors (the run of turns)",
		keys: []string{"issue", "role", "turns"}},
	{kind: texts.TreeStalled, from: "admit/stalled.go:stalledTreeReason (each member, its phase, its pull request)",
		keys: []string{"issue", "members[0].key", "members[0].phase", "members[0].pull_request", "members[1].key", "members[1].phase"}},
	{kind: texts.ArchitectStalled, from: "admit/architect_stall.go:architectStalls (the root's phase)",
		keys: []string{"issue", "arm", "phase"}},
	{kind: texts.ArchitectStalled, variants: []string{"took-nothing"}, from: "admit/architect_stall.go:architectTookNothing (the phase entry)",
		keys: []string{"entry"}},
	{kind: texts.ArchitectStalled, variants: []string{"claim-unreachable"}, from: "admit/architect_stall.go:architectClaimUnreachable (the claim's state)",
		keys: []string{"claim_state"}},
	{kind: texts.ArchitectStalled, variants: []string{"slow-before-gate"}, from: "admit/architect_stall.go:architectSlowBeforeGate (since when)",
		keys: []string{"since"}},

	// Role messages.
	{kind: texts.CISettled, from: "workflow/review.go:tellChecks, namedConclusions (the pull request, each check)",
		keys: []string{"issue", "pull_request", "head", "not_passed#"}},
	{kind: texts.GateMoved, from: "workflow/gate.go:tellGateMoved (the gate, the version, the document)",
		keys: []string{"issue", "gate", "version", "artifact"}, links: []string{"document"}},
	{kind: texts.ApprovedRoundStuck, from: "workflow/review.go:tellStuckMergeState, stuckMergeStateReason (the head, the pull request, its merge state)",
		keys: []string{"issue", "pull_request", "head", "merge_state", "passed"}},
	{kind: texts.MemoryKill, from: "workflow/engine.go:memoryGuardNote, kernelKillNote (the process, its pid)",
		keys: []string{"issue", "cause", "process", "pid"}},
	{kind: texts.PhaseEnded, from: "record/outbox.go:PhaseEnded.Message; workflow/engine.go:phaseEnded (from, to, the conflict)",
		keys: []string{"issue", "from", "to", "conflicts_with", "pull_request"}},
	{kind: texts.PhaseEnded, variants: []string{"task-follows"}, from: "record/outbox.go:PhaseEnded.Message (Your <to> task follows)",
		keys: []string{"task_follows"}},
	{kind: texts.HookPrompt, from: "record/outbox.go:HookPrompt.Message (the count, the pull request)",
		keys: []string{"count", "pull_request"}},
	{kind: texts.UpdateAsk, from: "supervise/table.go:UpdateTask (the round, the moment it ends)",
		keys: []string{"issue", "round", "ends_at"}},
	{kind: texts.MergeQueueRemoval, from: "record/outbox.go:MergeQueueRemoval.Message (the pull request, the head)",
		keys: []string{"issue", "pull_request", "head"}},
	{kind: texts.MergeQueueRemoval, variants: []string{"unread"}, from: "record/outbox.go:MergeQueueRemoval.Message (no read of GitHub)",
		keys: []string{"unread"}},
}

// goldenCutTurnNote is the cut-turn note of a task taken back, word for word as main's
// interruptedTask wrote it (supervise/delivery.go and relaunch.go's relaunchSentence at b4a566cb,
// with a record of a turn and the task's turn cut off, no background work named and no report).
func goldenCutTurnNote(cause string) string {
	because := "Your process ended, and the daemon recorded no cause for it."
	if cause != "" {
		because = "Your process ended because " + cause + "."
	}
	return "Your previous turn on this task was interrupted. " + because + " Your tool calls in flight were never answered. " +
		"The daemon has no report of the background work of that turn. So the subagents and background jobs of that turn are gone. " +
		"Every other subagent and background job that you started in that process is gone too. No result of that work will come to you. " +
		"Your transcript does not say so. Before you do any of that work again, check what it already did. Then continue the task."
}

// goldenControllerStart is the controller's start message as main wrote it
// (daemon/controller.go:ControllerStartMessage at b4a566cb).
const goldenControllerStart = "Legion controller start: follow skill://legion-controller's start procedure now (\"What happened before you started\"), then end the turn."

// goldenKept is what R8 keeps of the daemon's texts, each with the prompt, skill or requirement that
// keeps it. A sample of the kind lacks none of it (goldenKeptOf.problems).
var goldenKept = []goldenKeptOf{
	{kind: texts.CutTurnNote, what: "the cut-turn note of a task taken back, word for word as on main",
		keptBy: "dispatch://INFRA-3353's R2",
		lines:  func(want map[string]string) []string { return []string{goldenCutTurnNote(want["cause"])} }},
	{kind: texts.ControllerStart, what: "the controller's start message, word for word",
		keptBy: "skills/legion-controller/SKILL.md, which names it by its words",
		lines:  func(map[string]string) []string { return []string{goldenControllerStart} }},
	{kind: texts.CheckLook, what: "the look task's first line, Production check of <KEY>: <title>., and the look on a line of its own",
		keptBy: "the legion tool's description (packages/pi-shared/src/legion-phase-ops.ts) and dispatch://INFRA-3518",
		lines: func(want map[string]string) []string {
			return []string{"Production check of " + want["issue"] + ": " + want["title"] + "."}
		},
		keys: []string{"look"}, values: func(want map[string]string) map[string]string { return map[string]string{"look": want["look"]} }},
	{kind: texts.RootResume, what: "the sentence `this resume is your design-approved wake` in a resume whose design gate is open",
		keptBy: "skills/legion-architect/references/design-gate.md and prompts/roles/architect-root.md, which name it by its words",
		lines: func(want map[string]string) []string {
			if want["gate_open"] != "true" {
				return nil
			}
			return []string{"this resume is your design-approved wake"}
		}},
	{kind: texts.PodWake, variants: []string{"role-message"}, what: "the facts of the role-message wake: how long Envoy keeps a role message, the field a kept card shows, " +
		"that a kept card can come before the task, that Envoy tells the sender of a drop, the setting of a message that is not kept, and that its sender sends it again",
		keptBy: "dispatch://INFRA-2846's R7",
		keys:   []string{"topic", "kept_for", "kept_card_field", "kept_card_may_come_first", "sender_told_of_drop", "not_kept_with", "sender_sends_again"},
		values: func(map[string]string) map[string]string {
			return map[string]string{"topic": "", "kept_for": "1h0m0s", "kept_card_field": "kept_at", "kept_card_may_come_first": "true",
				"sender_told_of_drop": "true", "not_kept_with": "keep: false", "sender_sends_again": "true"}
		}},
	{kind: texts.UpdateAsk, what: "the round's id, the push instruction, the moment the round ends, that every role's answer brings it forward and no answer delays it, " +
		"that only the role's own push keeps its work, and what goes with the pod (the push check and its rescue branch are on no daemon at this head)",
		keptBy: "dispatch://INFRA-2981's R4",
		keys: []string{"round", "push_to", "ends_at", "answer", "every_answer_brings_it_forward", "no_answer_delays_it", "kept_by_push_only",
			"goes_with_pod", "restart", "next_task_held"},
		values: func(want map[string]string) map[string]string {
			return map[string]string{"round": want["round"], "push_to": want["push_to"], "ends_at": want["ends_at"], "answer": "ready_for_update",
				"every_answer_brings_it_forward": "true", "no_answer_delays_it": "true", "kept_by_push_only": "true",
				"goes_with_pod": "turn,background_jobs,unpushed_work,test_database,caches", "restart": "same_session", "next_task_held": "true"}
		}},
	{kind: texts.ReviewStuck, variants: []string{"undecided", "approved-other-code"}, what: "the phrase `APPROVE of head <sha>` in the reason of a round a review ends",
		keptBy: "dispatch://INFRA-3498's R4",
		keys:   []string{"ends_round"},
		values: func(want map[string]string) map[string]string {
			return map[string]string{"ends_round": "APPROVE of head " + want["head"]}
		}},
	{kind: texts.ReviewStuck, what: "the summary words that name the fact that wrote the notice (texts.StuckBy*)",
		keptBy: "dispatch://INFRA-3498's R4",
		keys:   []string{"summary"},
		values: func(map[string]string) map[string]string { return map[string]string{"summary": ""} }},
	{kind: texts.PhaseAssignment, variants: []string{"stuck-undecided", "stuck-approved-other-code"}, what: "the phrase `APPROVE of head <sha>` in the task of a stuck round a review ends",
		keptBy: "dispatch://INFRA-3498's R4",
		keys:   []string{"ends_round"},
		values: func(want map[string]string) map[string]string {
			return map[string]string{"ends_round": want["ends_round"]}
		}},
	{kind: texts.HookPrompt, what: "the hook's words as they are written, after its Legion hook first line and before the facts as key: value lines",
		keptBy: "dispatch://INFRA-2551's R3, R5 and R7",
		lines: func(want map[string]string) []string {
			return []string{"Legion hook " + want["hook"] + " on " + want["issue"] + ": work went back from " + want["from"] + " to " + want["to"] + "."}
		},
		keys:   []string{"count", "counts"},
		values: func(want map[string]string) map[string]string { return map[string]string{"count": want["count"]} }},
}

// goldenShapeKept is, for each shape of the root architect's start, the facts and the first act
// dispatch://INFRA-2614's R3 gives it, which the shape's sample states with the values the test
// derived from its inputs.
var goldenShapeKept = map[string][]string{
	texts.ShapeFreshTree:         {"shape", "holds_slot", "decompose", "first_act", "nothing_else_starts_it", "gated"},
	texts.ShapeCarriedPhase:      {"shape", "phase", "gate_carried", "daemon_starts_worker", "decompose", "first_act"},
	texts.ShapeGateStanding:      {"shape", "decompose", "first_act", "first_phase_waits_for", "gated", "gates[0].state", "gates[1].state"},
	texts.ShapeGateOpen:          {"shape", "decompose", "first_act", "first_phase_waits_for", "gated", "gates[0].state"},
	texts.ShapeEarlierGeneration: {"shape", "holds_slot", "decompose", "first_act", "first_phase_waits_for"},
}

func init() {
	for shape, keys := range goldenShapeKept {
		keys := keys
		var exempt []string
		for _, key := range keys {
			exempt = append(exempt, strings.SplitN(key, "[", 2)[0])
		}
		goldenKept = append(goldenKept, goldenKeptOf{kind: texts.ArchitectStart, variants: []string{shape},
			what:   "the facts and the first act of the " + shape + " shape",
			keptBy: "dispatch://INFRA-2614's R3",
			keys:   exempt,
			values: func(want map[string]string) map[string]string {
				values := map[string]string{}
				for _, key := range keys {
					if value, ok := want[key]; ok {
						values[key] = value
					}
				}
				return values
			}})
	}
}
