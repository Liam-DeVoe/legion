package texts

// This file is the one list. It holds the kind constants and the list of entries, and nothing
// else. A script reads each quoted lower-case word of this file as a kind. So the file has no
// import, no struct tag, and no quoted word that is not the name of a kind. The entry type, the
// forms, the urgencies and the rules are in the other files of the package.

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

// list is the one list: one entry for each kind, in the order of the constants above.
var list = []Entry{
	{Kind: PhaseFinished, Form: Notice},
	{Kind: WorkerDied, Form: Notice},
	{Kind: WorkerStalled, Form: Notice},
	{Kind: WorkerSilent, Form: Notice},
	{Kind: WorkerBackgroundStalled, Form: Notice},
	{Kind: Held, Form: Notice},
	{Kind: PRBlocked, Form: Notice},
	{Kind: PRMerged, Form: Notice},
	{Kind: PRClosedUnmerged, Form: Notice},
	{Kind: PullRequestMissing, Form: Notice},
	{Kind: MessageRefused, Form: Notice},
	{Kind: DesignApproved, Form: Notice},
	{Kind: DesignChangesRequested, Form: Notice},
	{Kind: CarriedGateOpened, Form: Notice},
	{Kind: ChildClosed, Form: Notice},
	{Kind: ChildStatus, Form: Notice},
	{Kind: ChildrenFirst, Form: Notice},
	{Kind: ChildReleaseStalled, Form: Notice},
	{Kind: ChildNotRecorded, Form: Notice},
	{Kind: CatchUp, Form: Notice},
	{Kind: ChecksRed, Form: Notice},
	{Kind: ReviewStuck, Form: Notice},
	{Kind: StatusReasserted, Form: Notice},
	{Kind: SessionLost, Form: Notice},
	{Kind: SessionStoreFault, Form: Notice},

	{Kind: Triage, Form: ControllerNotice},
	{Kind: SlotFree, Form: ControllerNotice},
	{Kind: Todo, Form: ControllerNotice},
	{Kind: Tick, Form: ControllerNotice},
	{Kind: Unslotted, Form: ControllerNotice},
	{Kind: AwaitingPerson, Form: ControllerNotice},
	{Kind: ParkedDecision, Form: ControllerNotice},
	{Kind: UpdateStalled, Form: ControllerNotice},
	{Kind: ProviderErrors, Form: ControllerNotice},
	{Kind: TreeStalled, Form: ControllerNotice},
	{Kind: ArchitectStalled, Form: ControllerNotice},
}
