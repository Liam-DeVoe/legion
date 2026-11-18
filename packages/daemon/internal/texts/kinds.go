package texts

// This file is the one list. It holds the kind constants and the list of entries, and nothing
// else. A script reads each quoted lower-case word of this file as a kind. So the file has no
// import, no struct tag, and no quoted word that is not the name of a kind. The entry type, the
// forms, the readers, the urgencies and the rules are in the other files of the package.

// The tasks: the prompts that the daemon sends a session.
const (
	PhaseAssignment Kind = "phase-assignment"
	PhaseResume     Kind = "phase-resume"
	RootResume      Kind = "root-resume"
	CheckLook       Kind = "check-look"
	ArchitectStart  Kind = "architect-start"
	KeptDecisions   Kind = "kept-decisions"
	PodWake         Kind = "pod-wake"
	ControllerStart Kind = "controller-start"
)

// The parts that a start puts before a task. Each is a task too.
const (
	RelaunchNote         Kind = "relaunch-note"
	CutTurnNote          Kind = "cut-turn-note"
	AbandonedSessionNote Kind = "abandoned-session-note"
	OutOfMemoryNote      Kind = "out-of-memory-note"
	ProviderErrorNote    Kind = "provider-error-note"
)

// The notices to the architect that owns an issue.
const (
	PhaseFinished           Kind = "phase-finished"
	WorkerDied              Kind = "worker-died"
	WorkerStalled           Kind = "worker-stalled"
	WorkerSilent            Kind = "worker-silent"
	WorkerBackgroundStalled Kind = "worker-background-stalled"
	Held                    Kind = "held"
	PRBlocked               Kind = "pr-blocked"
	PRMerged                Kind = "pr-merged"
	PRClosedUnmerged        Kind = "pr-closed-unmerged"
	PullRequestMissing      Kind = "pull-request-missing"
	MessageRefused          Kind = "message-refused"
	DesignApproved          Kind = "design-approved"
	DesignChangesRequested  Kind = "design-changes-requested"
	CarriedGateOpened       Kind = "carried-gate-opened"
	ChildClosed             Kind = "child-closed"
	ChildStatus             Kind = "child-status"
	ChildrenFirst           Kind = "children-first"
	ChildReleaseStalled     Kind = "child-release-stalled"
	ChildNotRecorded        Kind = "child-not-recorded"
	CatchUp                 Kind = "catch-up"
	ChecksRed               Kind = "checks-red"
	ReviewStuck             Kind = "review-stuck"
	StatusReasserted        Kind = "status-reasserted"
	SessionLost             Kind = "session-lost"
	SessionStoreFault       Kind = "session-store-fault"
)

// The notices to the controller.
const (
	Triage           Kind = "triage"
	SlotFree         Kind = "slot-free"
	Todo             Kind = "todo"
	Tick             Kind = "tick"
	Unslotted        Kind = "unslotted"
	AwaitingPerson   Kind = "awaiting-person"
	ParkedDecision   Kind = "parked-decision"
	UpdateStalled    Kind = "update-stalled"
	ProviderErrors   Kind = "provider-errors"
	TreeStalled      Kind = "tree-stalled"
	ArchitectStalled Kind = "architect-stalled"
)

// The role messages: the messages that the daemon publishes to a role.
const (
	CISettled          Kind = "ci-settled"
	GateMoved          Kind = "gate-moved"
	ApprovedRoundStuck Kind = "approved-round-stuck"
	MemoryKill         Kind = "memory-kill"
	PhaseEnded         Kind = "phase-ended"
	HookPrompt         Kind = "hook-prompt"
	UpdateAsk          Kind = "update-ask"
	MergeQueueRemoval  Kind = "merge-queue-removal"
)

// list is the one list: one entry for each kind, in the order of the constants above.
var list = []Entry{
	{Kind: PhaseAssignment, Form: Task, Readers: toPhaseWorkers},
	{Kind: PhaseResume, Form: Task, Readers: toPhaseWorkers},
	{Kind: RootResume, Form: Task, Readers: toArchitect},
	{Kind: CheckLook, Form: Task, Readers: toCheckRoles},
	{Kind: ArchitectStart, Form: Task, Readers: toArchitect},
	{Kind: KeptDecisions, Form: Task, Readers: toArchitect},
	{Kind: PodWake, Form: Task, Readers: toPodRoles},
	{Kind: ControllerStart, Form: Task, Readers: toController},

	{Kind: RelaunchNote, Form: Task, Readers: toEveryRole},
	{Kind: CutTurnNote, Form: Task, Readers: toEveryRole},
	{Kind: AbandonedSessionNote, Form: Task, Readers: toEveryRole},
	{Kind: OutOfMemoryNote, Form: Task, Readers: toPodRoles},
	{Kind: ProviderErrorNote, Form: Task, Readers: toEveryRole},

	{Kind: PhaseFinished, Form: Notice, Readers: toArchitect, Urgency: phaseFinishedUrgency},
	{Kind: WorkerDied, Form: Notice, Readers: toArchitectAndController},
	{Kind: WorkerStalled, Form: Notice, Readers: toArchitectAndController},
	{Kind: WorkerSilent, Form: Notice, Readers: toArchitect},
	{Kind: WorkerBackgroundStalled, Form: Notice, Readers: toArchitect},
	{Kind: Held, Form: Notice, Readers: toArchitectAndController, Urgency: heldUrgency},
	{Kind: PRBlocked, Form: Notice, Readers: toArchitect},
	{Kind: PRMerged, Form: Notice, Readers: toArchitect, Urgency: lowToArchitect},
	{Kind: PRClosedUnmerged, Form: Notice, Readers: toArchitect},
	{Kind: PullRequestMissing, Form: Notice, Readers: toArchitect},
	{Kind: MessageRefused, Form: Notice, Readers: toArchitect},
	{Kind: DesignApproved, Form: Notice, Readers: toArchitect},
	{Kind: DesignChangesRequested, Form: Notice, Readers: toArchitect},
	{Kind: CarriedGateOpened, Form: Notice, Readers: toArchitect},
	{Kind: ChildClosed, Form: Notice, Readers: toArchitect},
	{Kind: ChildStatus, Form: Notice, Readers: toArchitect, Urgency: childStatusUrgency},
	{Kind: ChildrenFirst, Form: Notice, Readers: toArchitect},
	{Kind: ChildReleaseStalled, Form: Notice, Readers: toArchitect},
	{Kind: ChildNotRecorded, Form: Notice, Readers: toArchitect},
	{Kind: CatchUp, Form: Notice, Readers: toArchitect},
	{Kind: ChecksRed, Form: Notice, Readers: toArchitect, Urgency: lowToArchitect},
	{Kind: ReviewStuck, Form: Notice, Readers: toArchitect},
	{Kind: StatusReasserted, Form: Notice, Readers: toArchitect},
	{Kind: SessionLost, Form: Notice, Readers: toArchitectAndController},
	{Kind: SessionStoreFault, Form: Notice, Readers: toController},

	{Kind: Triage, Form: ControllerNotice, Readers: toController},
	{Kind: SlotFree, Form: ControllerNotice, Readers: toController},
	{Kind: Todo, Form: ControllerNotice, Readers: toController},
	{Kind: Tick, Form: ControllerNotice, Readers: toController},
	{Kind: Unslotted, Form: ControllerNotice, Readers: toController},
	{Kind: AwaitingPerson, Form: ControllerNotice, Readers: toController, Urgency: lowToController},
	{Kind: ParkedDecision, Form: ControllerNotice, Readers: toController},
	{Kind: UpdateStalled, Form: ControllerNotice, Readers: toController},
	{Kind: ProviderErrors, Form: ControllerNotice, Readers: toController},
	{Kind: TreeStalled, Form: ControllerNotice, Readers: toController},
	{Kind: ArchitectStalled, Form: ControllerNotice, Readers: toController},

	{Kind: CISettled, Form: RoleMessage, Readers: toPodRoles, Urgency: ciSettledUrgency},
	{Kind: GateMoved, Form: RoleMessage, Readers: toPodRoles},
	{Kind: ApprovedRoundStuck, Form: RoleMessage, Readers: toReviewer},
	{Kind: MemoryKill, Form: RoleMessage, Readers: toPodRoles},
	{Kind: PhaseEnded, Form: RoleMessage, Readers: toPhaseWorkers},
	{Kind: HookPrompt, Form: RoleMessage, Readers: toPhaseWorkers},
	{Kind: UpdateAsk, Form: RoleMessage, Readers: toPodRoles},
	{Kind: MergeQueueRemoval, Form: RoleMessage, Readers: toMerger},
}
