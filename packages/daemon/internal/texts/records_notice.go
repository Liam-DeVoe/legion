package texts

import (
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// This file holds the records of the notices to the architect that owns an issue. A notice stays
// one record.Notice on the wire, so the json name of each field is a key of record.Notice, and a
// key has one Go type in every notice record. A key keeps the shape its readers take. A nested
// shape (the failure, the catch-up) is declared here, and record either names it as an alias
// (record.NoticeFailure) or keeps a copy of the same shape (record.CatchUp).
//
// A field that holds another author's words is a string with the struct tag texts:"words". A
// record with such a field declares the words key, the payload's words object, which names each
// words field that is set by its path (WordsPaths) with its author and its time.

// NoticeFailure is why a claim failed, as the notices of that failure name it
// (record.NoticeFailure is this type): the one word the site that failed the claim
// chose, the sentence beside it, the four counts against their limits, and what the runtime and the
// launcher last said of the claim's death and its start.
type NoticeFailure struct {
	Cause          string        `json:"cause"`
	Detail         string        `json:"detail,omitempty"`
	LaunchFailures FailureBudget `json:"launchFailures"`
	Deaths         FailureBudget `json:"deaths"`
	PromptFailures FailureBudget `json:"promptFailures"`
	PromptRetires  FailureBudget `json:"promptRetires"`
	Death          string        `json:"death,omitempty"`
	StartFailure   string        `json:"startFailure,omitempty"`
}

// FailureBudget is one count of a failed claim beside the limit it is measured against
// (record.FailureBudget is this type).
type FailureBudget struct {
	Count int `json:"count"`
	Limit int `json:"limit"`
}

// PhaseFinishedRecord is the notice that a phase ended (workflow's NoticeFor): the role whose
// phase ended, the phase, the phase the issue moved to, the worker's summary (words, bounded at
// Summary) with the handoff's reference (its phase word, its version and its commit), and the
// tester's verdict. A worker's own move back gives its words in reason, by that role.
type PhaseFinishedRecord struct {
	NoticeHead
	Role           claim.Role           `json:"role,omitempty"`
	Phase          phase.Phase          `json:"phase,omitempty"`
	To             phase.Phase          `json:"to,omitempty"`
	Summary        string               `json:"summary,omitempty" texts:"words"`
	Verdict        string               `json:"verdict,omitempty"`
	HandoffPhase   string               `json:"handoff_phase,omitempty"`
	HandoffVersion int                  `json:"handoff_version,omitempty"`
	HandoffCommit  string               `json:"handoff_commit,omitempty"`
	Reason         string               `json:"reason,omitempty" texts:"words"`
	Words          map[string]WordsMeta `json:"words,omitempty"`
}

// WorkerDiedRecord is the notice that a role's claim failed (workflow's claimFailed): the role,
// the issue's phase, the phase the issue works where the role does not work it (Working), the
// look of a production check that lost its checking role, and why the claim failed. Detail is
// the sentence the failing site wrote, for a failure that recorded no cause and so has no Failure
// (a fact an older daemon recorded).
type WorkerDiedRecord struct {
	NoticeHead
	Role    claim.Role     `json:"role,omitempty"`
	Phase   phase.Phase    `json:"phase,omitempty"`
	Working phase.Phase    `json:"working,omitempty"`
	Look    int            `json:"look,omitempty"`
	Failure *NoticeFailure `json:"failure,omitempty"`
	Detail  string         `json:"detail,omitempty"`
}

// The words of what became of a stalled process (WorkerStalledRecord.Stop).
const (
	StopStopped     = "stopped"
	StopUnconfirmed = "unconfirmed"
	StopNotMade     = "not-made"
)

// WorkerStalledRecord is the notice of the stall backstop (the daemon's stallBackstopRecord): the
// role, when its agent last stepped and for how long it has been silent, the bound the deployment
// gives the role (worker_stall_backstop_hours), when the turn began, the last call the daemon saw
// (its tool, its id, when it started and when it ended), how the backstop's recovery ended (a
// supervise.ProcessEnd word), when a queued launch is due, what became of the stalled process (one
// of the Stop words) and the launch failures the replacement charged.
type WorkerStalledRecord struct {
	NoticeHead
	Role           claim.Role `json:"role,omitempty"`
	LastStep       time.Time  `json:"last_step"`
	SilentFor      string     `json:"silent_for"`
	Bound          string     `json:"bound,omitempty"`
	TurnStarted    time.Time  `json:"turn_started,omitzero"`
	CallTool       string     `json:"call_tool,omitempty"`
	CallID         string     `json:"call_id,omitempty"`
	CallStarted    time.Time  `json:"call_started,omitzero"`
	CallEnded      time.Time  `json:"call_ended,omitzero"`
	End            string     `json:"end"`
	WakeDue        time.Time  `json:"wake_due,omitzero"`
	Stop           string     `json:"stop,omitempty"`
	LaunchFailures int        `json:"launch_failures,omitempty"`
}

// The words of the start a worker-silent notice says the daemon wrote (WorkerSilentRecord.Sent).
const (
	SentOwedEntry = "owed-entry"
	SentPhaseTask = "phase-task"
)

// WorkerSilentRecord is the notice of a phase worker that nothing will prompt (workflow's
// repairSilence): the role, the phase, when its agent last stepped and for how long it has been
// silent, the bound (worker_silence_minutes), the start the daemon wrote (one of the Sent words),
// the phase entry the phase row names and the entry the claim was last given.
type WorkerSilentRecord struct {
	NoticeHead
	Role      claim.Role  `json:"role,omitempty"`
	Phase     phase.Phase `json:"phase,omitempty"`
	LastStep  time.Time   `json:"last_step"`
	SilentFor string      `json:"silent_for"`
	Bound     string      `json:"bound,omitempty"`
	Sent      string      `json:"sent"`
	Entry     int64       `json:"entry,omitempty"`
	Given     int64       `json:"given,omitempty"`
}

// WorkerBackgroundStalledRecord is the notice of a phase worker that waits on background work of
// its own past its bound (workflow's workerBackgroundStalled): the role, the phase, each kind of
// work its session reported, when it reported it, how long the report has stood, and the bound
// (worker_background_hours).
type WorkerBackgroundStalledRecord struct {
	NoticeHead
	Role       claim.Role  `json:"role,omitempty"`
	Phase      phase.Phase `json:"phase,omitempty"`
	Work       []string    `json:"work,omitempty"`
	ReportedAt time.Time   `json:"reported_at"`
	StoodFor   string      `json:"stood_for"`
	Bound      string      `json:"bound,omitempty"`
}

// HeldRecord is the notice of an issue's hold (workflow's claimFailed, retryOrEscalate and
// HeldNotice): the role, the phase the hold came from, what held it (a Hold word, which the
// urgency rule reads), the hold's reason word (escalated for an escalation, which both agents'
// texts branch on), the architect's own words for its escalation, and why the claim failed.
type HeldRecord struct {
	NoticeHead
	Role       claim.Role           `json:"role,omitempty"`
	Phase      phase.Phase          `json:"phase,omitempty"`
	Hold       string               `json:"hold"`
	Reason     string               `json:"reason,omitempty"`
	Escalation string               `json:"escalation,omitempty" texts:"words"`
	Failure    *NoticeFailure       `json:"failure,omitempty"`
	Words      map[string]WordsMeta `json:"words,omitempty"`
}

// PRBlockedRecord is the notice that an issue reached the review round cap (workflow's
// recordRound): the cap, the rounds counted and the conflict rounds not counted.
type PRBlockedRecord struct {
	NoticeHead
	Cap            int `json:"cap"`
	Counted        int `json:"counted"`
	ConflictRounds int `json:"conflict_rounds,omitempty"`
}

// PRMergedRecord is the notice that an issue's pull request merged (workflow's merged and
// closeMergedTelling): the pull request, the phase the issue stood in where it was not merging,
// and each issue the close waits for.
type PRMergedRecord struct {
	NoticeHead
	PullRequest string      `json:"pull_request"`
	Phase       phase.Phase `json:"phase,omitempty"`
	WaitingFor  []string    `json:"waiting_for,omitempty"`
}

// PRClosedUnmergedRecord is the notice that an issue's pull request closed without merging
// (workflow's closed): the pull request and the issue's phase.
type PRClosedUnmergedRecord struct {
	NoticeHead
	PullRequest string      `json:"pull_request"`
	Phase       phase.Phase `json:"phase,omitempty"`
}

// PullRequestMissingRecord is the notice that the implementer completed and GitHub shows no open
// pull request on its branch (workflow's pullRequestMissing): the handoff commit, the repository
// and the branch.
type PullRequestMissingRecord struct {
	NoticeHead
	HandoffCommit string `json:"handoff_commit,omitempty"`
	Repo          string `json:"repo"`
	Branch        string `json:"branch"`
}

// The words of what Dispatch refused (MessageRefusedRecord.Refused).
const (
	RefusedMessage = "message"
	RefusedStatus  = "status"
)

// MessageRefusedRecord is the notice of a Dispatch write the daemon could not make (the daemon's
// messagePostRefused and statusWriteRefused): what Dispatch refused (one of the Refused words) and
// its answer. A refused message gives its length in characters and its first line. A refused
// status write gives the status, whether the status is set, whether the reason posted beside it
// is on the issue, and, where it is not, the reason's length and first line.
type MessageRefusedRecord struct {
	NoticeHead
	Refused        string `json:"refused"`
	DispatchAnswer string `json:"dispatch_answer"`
	Length         int    `json:"length,omitempty"`
	Begins         string `json:"begins,omitempty"`
	Status         string `json:"status,omitempty"`
	StatusSet      *bool  `json:"status_set,omitempty"`
	ReasonPosted   *bool  `json:"reason_posted,omitempty"`
}

// GateDecision is a person's decision on a design-gate step that a notice reports: the version
// decided, who decided (a Dispatch actor, user:<login> for a person, absent where nobody decided),
// the reference of the step's document (workflow's stepRef) and when.
type GateDecision struct {
	Version int       `json:"version,omitempty"`
	Actor   string    `json:"actor,omitempty"`
	Ref     string    `json:"ref,omitempty"`
	At      time.Time `json:"at,omitzero"`
}

// DesignApprovedRecord is the notice that a design-gate step opened (workflow's gateRegistered
// and applyArtifact).
type DesignApprovedRecord struct {
	NoticeHead
	GateDecision
}

// DesignChangesRequestedRecord is the notice of a person's changes request on a design-gate step
// (workflow's tellChanges): the decision, and the person's reason (words, bounded at OneAct).
type DesignChangesRequestedRecord struct {
	NoticeHead
	GateDecision
	Reason string               `json:"reason,omitempty" texts:"words"`
	Words  map[string]WordsMeta `json:"words,omitempty"`
}

// CarriedGateOpenedRecord is the notice that a person's approval opened the gate a carried root
// brought with it (workflow's applyArtifact): the decision, whose reference names the issue whose
// document the person approved.
type CarriedGateOpenedRecord struct {
	NoticeHead
	GateDecision
}

// ChildClosedRecord is the notice that a child is done (workflow's leave and closeMerged). The
// issue is the child.
type ChildClosedRecord struct {
	NoticeHead
}

// The words of why a reparented root stays a tree of its own (ChildStatusRecord.StaysOwn), one for
// each reason of workflow's RanAsRoot.
const (
	StaysOwnOperator   = "operator"
	StaysOwnRan        = "ran"
	StaysOwnCarriesRun = "carries-run"
	StaysOwnHandedOver = "handed-over"
)

// ChildStatusRecord is the notice of a change of a child (workflow's leave, ChildReenteredNotice,
// ChildStartedNotice, ChildUnderAcceptedParentNotice, JoinedTheTreeNotice and ReparentedNotice):
// the change (a Change word, which the urgency rule reads), the tree it runs under, every issue it
// waited on, the root of the closed tree it came from (absent for the admission queue), the status
// it left with, the ancestor whose reviewed head the daemon accepted, and, for a reparented root,
// the root, its new Dispatch parent and why it stays a tree of its own (a StaysOwn word).
type ChildStatusRecord struct {
	NoticeHead
	Change           string   `json:"change"`
	Child            string   `json:"child,omitempty"`
	Tree             string   `json:"tree,omitempty"`
	WaitedOn         []string `json:"waited_on,omitempty"`
	FromTree         string   `json:"from_tree,omitempty"`
	Status           string   `json:"status,omitempty"`
	AcceptedAncestor string   `json:"accepted_ancestor,omitempty"`
	Parent           string   `json:"parent,omitempty"`
	StaysOwn         string   `json:"stays_own,omitempty"`
}

// ChildrenFirstRecord is the notice that a parent's own first phase waits for its open children
// (workflow's ChildrenFirstNotice): every open child.
type ChildrenFirstRecord struct {
	NoticeHead
	Children []OpenChildFacts `json:"children"`
}

// OpenChildFacts is one open child of a children-first notice: its key, the Dispatch status it
// holds its parent with, whether the daemon released it, and the issue above it on its own parent
// chain that a blocked_by link of it names (empty where there is no such cycle).
type OpenChildFacts struct {
	Key      string `json:"key"`
	Status   string `json:"status"`
	Released bool   `json:"released"`
	Cycle    string `json:"cycle"`
}

// The words of why a released child is not recorded (ChildReleaseStalledRecord.Stall).
const (
	StallUnrecordedAncestor  = "unrecorded-ancestor"
	StallUnrecordedBlocker   = "unrecorded-blocker"
	StallAttemptsSpent       = "attempts-spent"
	StallRecoveredUnrecorded = "recovered-unrecorded"
	// StallLetGoNoTodo is a repository the daemon no longer waits for GitHub to answer for,
	// because no todo issue labelled for Legion or under a parent names it now.
	StallLetGoNoTodo = "let-go-no-todo"
	// StallLetGoAnswered is a repository the daemon no longer waits for GitHub to answer for,
	// because Dispatch's repository mapping, or a read GitHub answered for it at another issue's
	// preparation, answers for it now.
	StallLetGoAnswered = "let-go-answered"
)

// ChildReleaseStalledRecord is the notice that a child a release released is not recorded (api's
// tellReleaseWait and release retry, the daemon's recovery of an unread repository, and the
// daemon's letting go of one): why (a Stall word), the child, its parent, the unrecorded issue
// above it, the unrecorded issue on its parent chain that blocks it with that issue's status, the
// repository GitHub did not answer for, the releases tried, the repositories recovered or let go,
// each child still not recorded after a recovery, and each child a letting go leaves that no
// recovery will record.
type ChildReleaseStalledRecord struct {
	NoticeHead
	Stall         string            `json:"stall"`
	Child         string            `json:"child,omitempty"`
	Parent        string            `json:"parent,omitempty"`
	Ancestor      string            `json:"ancestor,omitempty"`
	Blocker       string            `json:"blocker,omitempty"`
	BlockerStatus string            `json:"blocker_status,omitempty"`
	Repo          string            `json:"repo,omitempty"`
	Attempts      int               `json:"attempts,omitempty"`
	Repos         []string          `json:"repos,omitempty"`
	Unrecorded    []UnrecordedChild `json:"unrecorded,omitempty"`
	LetGo         []LetGoChild      `json:"let_go,omitempty"`
}

// UnrecordedChild is one child a recovery left unrecorded: the child, its parent and whether the
// parent is recorded now.
type UnrecordedChild struct {
	Child          string `json:"child"`
	Parent         string `json:"parent"`
	ParentRecorded bool   `json:"parent_recorded"`
}

// LetGoChild is one child a release_children left to the recovery of a repository the daemon let
// go, which no recovery will record now: the child, its parent and whether the parent is recorded
// now, the status Dispatch's listing gives the child (empty where the listing no longer holds
// it), and the repository its wait was filed under. Where the parent is recorded and Dispatch's
// mapping leaves that repository out, it gives that refusal, and whether the refusal is for now,
// a read GitHub did not answer, which the mapping's own recovery reads again (for_now), or for
// good, a role App not installed. Refusal is empty where nothing refuses the repository.
type LetGoChild struct {
	Child          string `json:"child"`
	Parent         string `json:"parent"`
	ParentRecorded bool   `json:"parent_recorded"`
	Status         string `json:"status"`
	Repo           string `json:"repo"`
	Refusal        string `json:"refusal"`
	ForNow         bool   `json:"for_now"`
}

// ChildNotRecordedRecord is the notice that a child was not recorded under the tree (workflow's
// refuseChild): the child, the repository it resolved to, and the refusal.
type ChildNotRecordedRecord struct {
	NoticeHead
	Child    string `json:"child"`
	Resolved string `json:"resolved,omitempty"`
	Refusal  string `json:"refusal"`
}

// CatchUpRecord is the notice that tells an architect at a launch the issues it owns (workflow's
// QueueCatchUp): the account of the tree, and what the last let-go of the architect's process
// ended where the launch's ready carried it.
type CatchUpRecord struct {
	NoticeHead
	CatchUp  *CatchUpFacts        `json:"catch_up,omitempty"`
	Relaunch *RelaunchFacts       `json:"relaunch,omitempty"`
	Words    map[string]WordsMeta `json:"words,omitempty"`
}

// CatchUpFacts is a catch-up's account of the issues an architect owns (the shape of
// record.CatchUp, whose keys it keeps). It names each issue that is not done with its fields, each
// done issue by its key alone, each child not yet released whole, and the keys of the issues that
// wait in accepting for this architect's acceptance. Kept is what a person decided on the tree
// while nobody could hear it, with the words of each decision. Decided is the text of those
// decisions that an older daemon wrote, which this daemon writes no more.
type CatchUpFacts struct {
	Generation uint64            `json:"generation"`
	Launch     uint64            `json:"launch"`
	HandedOver bool              `json:"handed_over,omitempty"`
	Carried    bool              `json:"carried,omitempty"`
	Gates      []CatchUpGate     `json:"gates"`
	Issues     []CatchUpIssue    `json:"issues"`
	Done       []string          `json:"done,omitempty"`
	Unreleased []UnreleasedChild `json:"unreleased,omitempty"`
	Accepting  []string          `json:"accepting,omitempty"`
	Decided    string            `json:"decided,omitempty"`
	Kept       *KeptFacts        `json:"kept,omitempty"`
}

// CatchUpGate is one design-gate step as a catch-up states it (the shape of record.CatchUpGate).
type CatchUpGate struct {
	Step         string `json:"step"`
	Gated        bool   `json:"gated"`
	Unregistered bool   `json:"unregistered,omitempty"`
	Artifact     string `json:"artifact,omitempty"`
	Version      int    `json:"version,omitempty"`
	Approved     int    `json:"approved,omitempty"`
	Open         bool   `json:"open"`
}

// CatchUpIssue is one issue of the tree as a catch-up names it (record.CatchUpIssue is this
// type): an issue that is not done.
type CatchUpIssue struct {
	Key    string      `json:"key"`
	Parent string      `json:"parent,omitempty"`
	Title  string      `json:"title"`
	Phase  phase.Phase `json:"phase"`
	Status string      `json:"status"`
}

// UnreleasedChild is one child that its architect filed and has not released: its key, its parent
// and its Dispatch status.
type UnreleasedChild struct {
	Key    string `json:"key"`
	Parent string `json:"parent"`
	Status string `json:"status"`
}

// KeptFacts is what one architect start task or catch-up names of what its tree is owed (the
// shape of record.KeptDecisions): each decision, and the approval a re-admission kept.
type KeptFacts struct {
	Decisions []DecisionMark `json:"decisions,omitempty"`
	KeptApproval
}

// KeptApproval is the approval a root's re-admission kept for its next architect start (the shape
// of record.KeptApproval): the step whose document a person approved, the version approved and the
// version the document has reached since.
type KeptApproval struct {
	Step     string `json:"step,omitempty"`
	Approved int    `json:"approved,omitempty"`
	Latest   int    `json:"latest,omitempty"`
}

// DecisionMark is one kept decision (the shape of record.DecisionMark): its reference, the time of
// the decision, the issue it was made on, what the person did, who, the options the answer chose,
// the words of the act (bounded at EachOfSeveral where a catch-up names several), when the person
// wrote them, and whether more follows than the act's event carried (Carry's more).
type DecisionMark struct {
	Ref      string    `json:"ref"`
	At       time.Time `json:"at"`
	Issue    string    `json:"issue,omitempty"`
	Decision string    `json:"decision,omitempty"`
	Actor    string    `json:"actor,omitempty"`
	Options  []string  `json:"options,omitempty"`
	Answer   string    `json:"answer,omitempty" texts:"words"`
	AnswerAt time.Time `json:"answer_at,omitzero"`
	More     bool      `json:"more,omitempty"`
}

// ChecksRedRecord is the notice that the head conflicts with its base and the daemon moved the
// issue (workflow's NoticeFor for a conflict): the phase it left, the base, the head and the pull
// request.
type ChecksRedRecord struct {
	NoticeHead
	Phase         phase.Phase `json:"phase,omitempty"`
	ConflictsWith string      `json:"conflicts_with"`
	Head          string      `json:"head"`
	PullRequest   string      `json:"pull_request"`
}

// The summary words of a review-stuck notice: the fact that wrote it. dispatch://INFRA-3498's R4
// keeps them, and the architect's prompt reads them.
const (
	StuckByCompletion  = "the reviewer's completion"
	StuckByAnswer      = "the reviewer's review"
	StuckByOtherReview = "a review that is not the reviewer's answer"
	StuckByPush        = "a push"
	StuckByMergeState  = "GitHub's merge state for the head"
	StuckByMerge       = "the pull request's merge"
)

// The causes of a stuck review round (ReviewStuckRecord.Cause).
const (
	CauseUndecided         = "undecided"
	CauseApprovedOtherCode = "approved-other-code"
	CauseMergeState        = "merge-state"
)

// ReviewStuckRecord is the notice of a review round that nothing on its way ends (workflow's
// settleRound and tellStuckMergeState): the reviewer's role and the phase, the summary words (a
// StuckBy constant), the reviewer's role topic where the reviewer decides it, the cause (a Cause
// word), the pull request and the head, the head the reviewer approved where it is not the head,
// the review that ends the round (EndsRound, which keeps APPROVE of head <sha>), the reason the
// daemon set a review aside, and, for a head GitHub will not merge, its merge state, the checks of
// that head that did not pass, the count of those that passed and the command that lists them.
type ReviewStuckRecord struct {
	NoticeHead
	Role         claim.Role        `json:"role,omitempty"`
	Phase        phase.Phase       `json:"phase,omitempty"`
	Summary      string            `json:"summary,omitempty"`
	Topic        string            `json:"topic,omitempty"`
	Cause        string            `json:"cause"`
	PullRequest  string            `json:"pull_request"`
	Head         string            `json:"head"`
	ApprovedHead string            `json:"approved_head,omitempty"`
	EndsRound    string            `json:"ends_round,omitempty"`
	SetAside     string            `json:"set_aside,omitempty"`
	MergeState   string            `json:"merge_state,omitempty"`
	NotPassed    []CheckConclusion `json:"not_passed,omitempty"`
	Passed       *int              `json:"passed,omitempty"`
	ListPassed   string            `json:"list_passed,omitempty"`
}

// StatusReassertedRecord is the notice that a session wrote a lifecycle status and the daemon set
// its own back (workflow's StatusReassertedNotice): the session, the status it wrote and the
// status the daemon set.
type StatusReassertedRecord struct {
	NoticeHead
	Session string `json:"session"`
	Wrote   string `json:"wrote"`
	Status  string `json:"status"`
}

// SessionLostRecord is the notice that a worker's recorded session is in no session store
// (workflow's sessionLost): the role, the phase and the runtime's account of the failed resume.
type SessionLostRecord struct {
	NoticeHead
	Role   claim.Role  `json:"role,omitempty"`
	Phase  phase.Phase `json:"phase,omitempty"`
	Detail string      `json:"detail"`
}

// SessionStoreFaultRecord is the notice that no worker of the issue can start because the session
// store cannot be asked (workflow's sessionStoreFault): the role, the phase, the runtime's account,
// and how often the daemon tries again.
type SessionStoreFaultRecord struct {
	NoticeHead
	Role       claim.Role  `json:"role,omitempty"`
	Phase      phase.Phase `json:"phase,omitempty"`
	Detail     string      `json:"detail"`
	RetryEvery string      `json:"retry_every"`
}
