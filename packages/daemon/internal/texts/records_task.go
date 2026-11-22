package texts

import (
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/phase"
)

// This file holds the records of the tasks: the prompts that the daemon sends a session. Render
// writes each of them. A task record holds the words of another author in Words or *Words fields,
// which Render draws as blocks after the fields.

// DesignApprovedWake is the sentence that a resume of a waiting root carries when the root's
// design gate is open. The daemon sends no design-approved notice to a root that waits on a
// person, so this resume is that root's design-approved wake. The architect's skill and the root
// architect's prompt name the sentence by its words, and a sentence a reader's guidance names by
// its words stays word for word (dispatch://INFRA-3686/spec R8).
const DesignApprovedWake = "this resume is your design-approved wake"

// ControllerStartMessage is the controller's first prompt at every start (the daemon's
// controllerKeeper). The controller's skill names it by its words, so it stays word for word
// (dispatch://INFRA-3686/spec R8).
const ControllerStartMessage = "Legion controller start: follow skill://legion-controller's start procedure now (\"What happened before you started\"), then end the turn."

// The words of the act that answered a root's wait (RootResumeRecord.Act), as record.Answer
// names them, and the word of an act that none of them names.
const (
	ActApproved         = "approved"
	ActChangesRequested = "changes_requested"
	ActVersion          = "version"
	ActRetracted        = "retracted"
	ActClosed           = "closed"
	ActAsk              = "ask"
	ActReply            = "reply"
	ActTurn             = "turn"
	ActDeclined         = "declined"
	ActActed            = "acted"
)

// PhaseAssignmentRecord is the task of a phase worker that the daemon starts on a phase
// (workflow's task): the issue's title and the phase, the handoff that ended the phase before it
// (its phase word and its version, which dispatch handoff-read reads), the commit that handoff
// names, the tester's verdict, and the pull request.
//
// A move back names the phase it came from (MovedBackFrom), and the words of the role that moved
// it go in the reason block, by that role. A review's rejection names the head it reviewed and
// GitHub's id of the review (ReviewID, absent where the decision an older daemon recorded holds
// none), and the review's body goes in the body block, by its author: where the block names a
// rest, or that more follows, the whole body is that review's on the pull request, the item's
// reference dispatch://INFRA-3686's R1 has a cut text carry. A merger's task at a head that a merge
// forward left after the review, while the review that landing asked of the reviewer stands for
// that head, names the base the merge brought forward (MergedForward) and that head as the one
// whose approval it waits for (ApprovalOwed). A retry names who retried (architect, operator, or a
// todo written on the held issue), and the architect's own words go in the reason block. A retry
// of a review round that nothing on its way ends names the round's cause (StuckRound, a
// ReviewStuckRecord cause), its head, the head the reviewer approved where that is other code
// (ReviewedHead), the review that ends it (EndsRound), and why the daemon set aside a review that
// would otherwise have decided it (SetAside). A round GitHub will not merge from names, as its
// review-stuck notice does, the merge state GitHub reports for the head, each check and each
// commit status of that head that did not pass, each check of that head GitHub skipped (skipped)
// with the field that says once that a skipped check ran nothing on the head (each_skipped_check,
// CheckConclusion), the count of the checks and statuses that passed and the command that lists
// them.
type PhaseAssignmentRecord struct {
	TaskHead
	Title             string            `json:"title"`
	Phase             phase.Phase       `json:"phase"`
	HandoffPhase      string            `json:"handoff_phase,omitempty"`
	HandoffVersion    int               `json:"handoff_version,omitempty"`
	HandoffCommit     string            `json:"handoff_commit,omitempty"`
	TesterVerdict     string            `json:"tester_verdict,omitempty"`
	PullRequest       string            `json:"pull_request,omitempty"`
	MovedBackFrom     phase.Phase       `json:"moved_back_from,omitempty"`
	ReviewedHead      string            `json:"reviewed_head,omitempty"`
	ReviewID          int64             `json:"review_id,omitempty"`
	MergedForward     string            `json:"merged_forward,omitempty"`
	ApprovalOwed      string            `json:"approval_owed,omitempty"`
	Head              string            `json:"head,omitempty"`
	Retried           string            `json:"retried,omitempty"`
	StuckRound        string            `json:"stuck_round,omitempty"`
	EndsRound         string            `json:"ends_round,omitempty"`
	SetAside          string            `json:"set_aside,omitempty"`
	MergeState        string            `json:"merge_state,omitempty"`
	NotPassed         []CheckConclusion `json:"not_passed,omitempty"`
	Skipped           []string          `json:"skipped,omitempty"`
	EachSkippedCheck  string            `json:"each_skipped_check,omitempty"`
	NotPassedStatuses []StatusState     `json:"not_passed_statuses,omitempty"`
	Passed            *int              `json:"passed,omitempty"`
	ListPassed        string            `json:"list_passed,omitempty"`
	Reason            *Words            `json:"reason"`
	Body              *Words            `json:"body"`
}

// PhaseResumeRecord is the task of a worker started for a phase already under way
// (workflow's ResumePhaseTask): the issue's title and the phase.
type PhaseResumeRecord struct {
	TaskHead
	Title string      `json:"title"`
	Phase phase.Phase `json:"phase"`
}

// RootResumeRecord is the task that an answered root's architect resumes with (workflow's
// resumeTask).
//
// The act: what answered the wait (Act, one of the Act words), who acted and when, the ask it
// answered as a dispatch:// link, and the version the act was about. GateOffAtAnswer says that
// the design gate was off when the wait was answered. ClosedBy names who closed the ask. Options
// are the options the answer chose. DispatchWords are the sentences Dispatch wrote for the asking
// agent on a decline.
//
// The gate of the step in question now, wherever that step has a gate, whatever the act: its
// document as a dispatch:// link (Document), its latest version, whether it is open, and whether
// the design gate is armed. A step with no gate, as a question asked before any step of the gate
// was registered has, names no gate state: GateOpen is nil, as main's resume wrote no gate
// sentence for it, and so is GateArmed, but on an approval (ActApproved). An approval of such a
// step names GateArmed all the same, since whether the design gate is armed is what says whether
// a person approved (main's "Your %s %s on %s was approved." under an armed gate, and "The design
// gate on %s is off, so no approval of your %s %s is asked for." under one that is not). A
// document an approval, a changes request or a version answered the wait on is that document too.
// LaterChangesVersion is a changes request made after the wait began that did not answer it, made
// on that document, whose reason goes in the later_reason block with its author and time.
//
// What is still with the person: the asks (dispatch:// links), and ApprovalRequest, the version of
// the document whose approval request is with the person. What waits on the architect: the asks
// (dispatch:// links), and RequestOpen, that the approval request's thread is open.
//
// The words of the act go in blocks: the answer, the reply, the reason of a changes request and
// the note of a decline, each bounded at OneAct, or at EachOfSeveral each where the task also
// carries a later changes request's reason. Where the gate is open, the task keeps
// DesignApprovedWake word for word.
type RootResumeRecord struct {
	TaskHead
	Act                 string    `json:"act"`
	By                  string    `json:"by,omitempty"`
	At                  time.Time `json:"at,omitzero"`
	Ask                 string    `json:"ask,omitempty"`
	Document            string    `json:"document,omitempty"`
	Version             int       `json:"version,omitempty"`
	GateOffAtAnswer     bool      `json:"gate_off_at_answer,omitempty"`
	ClosedBy            string    `json:"closed_by,omitempty"`
	Options             []string  `json:"options,omitempty"`
	DispatchWords       string    `json:"dispatch_words,omitempty"`
	DocumentVersion     int       `json:"document_version,omitempty"`
	GateOpen            *bool     `json:"gate_open,omitempty"`
	GateArmed           *bool     `json:"gate_armed,omitempty"`
	LaterChangesVersion int       `json:"later_changes_version,omitempty"`
	WithPerson          []string  `json:"with_person,omitempty"`
	ApprovalRequest     int       `json:"approval_request,omitempty"`
	WaitingOnYou        []string  `json:"waiting_on_you,omitempty"`
	RequestOpen         bool      `json:"request_open,omitempty"`
	Answer              *Words    `json:"answer"`
	Reply               *Words    `json:"reply"`
	Reason              *Words    `json:"reason"`
	Note                *Words    `json:"note"`
	LaterReason         *Words    `json:"later_reason"`
}

// Kept is DesignApprovedWake where the design gate is open at the resume.
func (r RootResumeRecord) Kept() []string {
	if r.GateOpen == nil || !*r.GateOpen {
		return nil
	}
	return []string{DesignApprovedWake}
}

// The words of what woke a look of a production check (CheckLookRecord.WokenBy).
const (
	WokenByMerge = "merge"
	WokenByBound = "bound"
	WokenByNamed = "named"
	WokenByRetry = "retry"
)

// The rule of a look's verdict, in the terms of dispatch://INFRA-904's R7, which
// dispatch://INFRA-4533's R1 has every look's task state: a requirement of the issue that only the
// deploy can show and that a look observes as not met is a fail, on every look, the last among them
// (CheckLookRecord.NotMetRequirement); one that is still unobserved at the last look is a fail too,
// which every look states, as main's task said it on every look ("or still unobserved when no look
// follows this one ... is a defect") and as INFRA-4533's R3 holds it for each look before the last
// (CheckLookRecord.UnobservedRequirement); the last look's pending is only for an issue with no
// such requirement (CheckLookRecord.Pending); and a last look that nobody reported gives no verdict,
// and the daemon closes the check as not run, which is no pass (CheckLookRecord.Unreported, R7's
// second sentence).
const (
	LookNotMetRequirement     = "fail"
	LookUnobservedRequirement = "fail"
	LastLookPending           = "only_with_no_deploy_only_requirement"
	LastLookUnreported        = "not_run"
)

// CheckLookRecord is the task of a look of a closed issue's production check (workflow's
// checkTask). Its first line is its lead, kept word for word, since the legion tool's
// description names it; the lead names the issue and its title, so they are no fields.
//
// The fields: the look, what woke it (one of the WokenBy words), the cause of a retried look
// that nobody reported, whether it is the last look, the rule of every look's verdict
// (NotMetRequirement and UnobservedRequirement, the Look words) and, beside it on the last look
// alone, the rest of that look's rule (Pending and Unreported, the LastLook words), the pull
// request, the merge commit (absent where the record holds none), and the check's bound with the
// time it comes due (LastLook, the check's bound_at). The last look starts at that time, or, while
// a look is still open then, a look's bound later each time (workflow's checkBound), so LastLook
// is when the bound comes due rather than when the last look starts. The bound is in whole days
// (BoundDays) where it is whole days, and otherwise as Go writes the duration (BoundDuration,
// such as 36h0m0s, 1h30m0s or 0s), exact to the nanosecond, as main named such a bound (its
// boundText): cut to whole days it would be no bound the check has, and a decimal number of hours
// names most bounds under an hour only nearly. The pending summary that named the wake goes in
// the summary block, by the checking role.
type CheckLookRecord struct {
	Issue                 string    `json:"-"`
	Title                 string    `json:"-"`
	Kind                  Kind      `json:"kind"`
	Look                  int       `json:"look"`
	WokenBy               string    `json:"woken_by"`
	RetryOf               string    `json:"retry_of,omitempty"`
	Last                  bool      `json:"last,omitempty"`
	NotMetRequirement     string    `json:"not_met_requirement,omitempty"`
	UnobservedRequirement string    `json:"unobserved_requirement,omitempty"`
	Pending               string    `json:"pending,omitempty"`
	Unreported            string    `json:"unreported,omitempty"`
	PullRequest           string    `json:"pull_request"`
	MergeCommit           string    `json:"merge_commit,omitempty"`
	BoundDays             int       `json:"bound_days,omitempty"`
	BoundDuration         string    `json:"bound_duration,omitempty"`
	LastLook              time.Time `json:"last_look,omitzero"`
	Summary               *Words    `json:"summary"`
}

// Lead is the first line of the look task: Production check of <KEY>: <title>. The title is a
// person's or a session's, and Dispatch keeps a line break inside a title, so the lead writes each
// line break of it as a space (oneLine): the lead stays one line, and no part of a title starts a
// line of the task, such as a second field or the opening tag of a block.
func (r CheckLookRecord) Lead() string {
	return "Production check of " + r.Issue + ": " + oneLine(r.Title) + "."
}

// oneLine is text with each character that breaks a line written as a space: the line feed, the
// carriage return, the vertical tab, the form feed, the next line (U+0085), and the line and
// paragraph separators (U+2028, U+2029). Text with none is returned as it is.
func oneLine(text string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\v', '\f', '\r', '\u0085', '\u2028', '\u2029':
			return ' '
		}
		return r
	}, text)
}

// The shapes of a root architect's start task (ArchitectStartRecord.Shape), one for each shape of
// workflow's ArchitectStartTask.
const (
	ShapeFreshTree         = "fresh-tree"
	ShapeCarriedPhase      = "carried-phase"
	ShapeGateStanding      = "gate-standing"
	ShapeGateOpen          = "gate-open"
	ShapeEarlierGeneration = "earlier-generation"
)

// ArchitectStartRecord is the first task of a root architect (workflow's ArchitectStartTask),
// in one of its shapes. The kept decisions that a start names go before it, as a task of their own
// (KeptDecisionsRecord).
//
// The facts and the first act that dispatch://INFRA-2614's R3 gives each shape: the shape, the
// generation the tree is open at, whether the root holds an admission slot, the phase the root
// stands in, whether the hand-over that carried a root in brought its design gate (GateCarried)
// and whether its pull request has merged (Merged), whether the daemon starts the worker of that
// phase, whether a member of the tree runs, the gate step by step, the steps the deployment gates,
// the first act as words (FirstAct, such as claim, write_spec, decompose, request_approval and
// register_gate), whether the architect decomposes the tree, what the root's own first phase waits
// for (such as gate, slot, children and blockers), and whether nothing else starts the tree.
type ArchitectStartRecord struct {
	TaskHead
	Shape              string          `json:"shape"`
	Generation         uint64          `json:"generation"`
	HoldsSlot          bool            `json:"holds_slot,omitempty"`
	Phase              phase.Phase     `json:"phase"`
	GateCarried        *bool           `json:"gate_carried,omitempty"`
	Merged             bool            `json:"merged,omitempty"`
	DaemonStartsWorker *bool           `json:"daemon_starts_worker,omitempty"`
	MembersRun         bool            `json:"members_run,omitempty"`
	Gates              []ArchitectGate `json:"gates,omitempty"`
	Gated              []string        `json:"gated,omitempty"`
	FirstAct           []string        `json:"first_act"`
	Decompose          bool            `json:"decompose"`
	FirstPhaseWaitsFor []string        `json:"first_phase_waits_for,omitempty"`
	NothingElseStarts  bool            `json:"nothing_else_starts_it,omitempty"`
}

// The states of a design-gate step in a start task (ArchitectGate.State).
const (
	GateUnregistered     = "unregistered"
	GateRegistered       = "registered"
	GateApproved         = "approved"
	GateChangesRequested = "changes-requested"
	GateUnanswered       = "unanswered"
)

// ArchitectGate is one step of a root's design gate in a start task: the step, its document as a
// dispatch:// link, the version registered (0 where the step is not registered), and its state
// (one of the Gate words).
type ArchitectGate struct {
	Step     string `json:"step"`
	Document string `json:"document"`
	Version  int    `json:"version"`
	State    string `json:"state"`
}

// KeptDecisionsRecord is the task that names what a person decided on a tree while nobody in it
// could hear it (workflow's KeptDecisionsTask): each decision, and the approval that a
// re-admission kept (the step, its document as a dispatch:// link, the version a person approved
// and the version the document has reached since).
type KeptDecisionsRecord struct {
	TaskHead
	Kept             []KeptDecision `json:"kept,omitempty"`
	ApprovalStep     string         `json:"approval_step,omitempty"`
	ApprovalDocument string         `json:"approval_document,omitempty"`
	Approved         int            `json:"approved,omitempty"`
	Latest           int            `json:"latest,omitempty"`
}

// KeptDecision is one decision a kept-decisions task names: its reference (a dispatch:// link),
// the issue it was made on, what the person did (a record.DecisionKind word), who and when, the
// options the answer chose, joined by a comma and a space, and the words of the act in the answer
// block, bounded at OneAct where the task names one decision and at EachOfSeveral each where it
// names several. The block names its path, such as kept[1].answer.
type KeptDecision struct {
	Ref      string    `json:"ref"`
	Issue    string    `json:"issue"`
	Decision string    `json:"decision"`
	By       string    `json:"by"`
	At       time.Time `json:"at"`
	Options  string    `json:"options"`
	Answer   *Words    `json:"answer"`
}

// PodWakeRecord is the task of a role whose released pod a wake brought back (workflow's
// WakeTask). Wake is the wake's word (pods.WakeChecks and the others), and StoppedBy what stopped
// the session the wake brings back: an idle release, which took the pod, or a project's pause,
// which kept the pod, its workspace and the work not pushed (StoppedByRelease, StoppedByPause).
//
// A CI settlement names the pull request, the head, the issue's phase, the checks and the commit
// statuses that did not pass, the checks GitHub skipped (skipped) with the field that says once
// that a skipped check ran nothing on the head (each_skipped_check, CheckConclusion), the count of
// the checks and statuses that passed and the command that lists them. A message to the role's
// topic names the topic and the facts dispatch://INFRA-2846's R7 requires: how long Envoy keeps a
// role message, the field a kept card shows, that a kept card can come before this task, that a
// kept card whose time ended before the session came back does not reach it (ExpiredDoesNotReach),
// that Envoy tells the sender when it drops one, the setting a message that is not kept is sent
// with, and that its sender sends it again. A message to the session names the session and the
// facts dispatch://INFRA-3360's R3 has the woken role's task give: a message that Envoy or
// Dispatch stored for the session reaches it in this session (StoredReachesSession), and a direct
// Envoy message that Envoy refused reaches it only when its sender sends it again
// (RefusedSenderSendsAgain). The backstop names the role it was armed for. A wake of another word
// gives its detail.
type PodWakeRecord struct {
	TaskHead
	Wake                    string            `json:"wake"`
	PullRequest             string            `json:"pull_request,omitempty"`
	Head                    string            `json:"head,omitempty"`
	Phase                   phase.Phase       `json:"phase,omitempty"`
	NotPassed               []CheckConclusion `json:"not_passed,omitempty"`
	Skipped                 []string          `json:"skipped,omitempty"`
	EachSkippedCheck        string            `json:"each_skipped_check,omitempty"`
	NotPassedStatuses       []StatusState     `json:"not_passed_statuses,omitempty"`
	Passed                  *int              `json:"passed,omitempty"`
	ListPassed              string            `json:"list_passed,omitempty"`
	Topic                   string            `json:"topic,omitempty"`
	KeptFor                 string            `json:"kept_for,omitempty"`
	KeptCardField           string            `json:"kept_card_field,omitempty"`
	KeptCardMayComeFirst    bool              `json:"kept_card_may_come_first,omitempty"`
	ExpiredDoesNotReach     bool              `json:"expired_does_not_reach,omitempty"`
	SenderToldOfDrop        bool              `json:"sender_told_of_drop,omitempty"`
	NotKeptWith             string            `json:"not_kept_with,omitempty"`
	SenderSendsAgain        bool              `json:"sender_sends_again,omitempty"`
	Session                 string            `json:"session,omitempty"`
	StoredReachesSession    bool              `json:"stored_reaches_session,omitempty"`
	RefusedSenderSendsAgain bool              `json:"refused_sender_sends_again,omitempty"`
	ArmedFor                string            `json:"armed_for,omitempty"`
	Detail                  string            `json:"detail,omitempty"`
	StoppedBy               string            `json:"stopped_by"`
}

// The words of what stopped the session a wake brings back (PodWakeRecord.StoppedBy).
const (
	StoppedByRelease = "release"
	StoppedByPause   = "pause"
)

// ControllerStartRecord is the controller's start message. It has no field: its text is
// ControllerStartMessage, word for word.
type ControllerStartRecord struct{}

// Kept is ControllerStartMessage.
func (ControllerStartRecord) Kept() []string {
	return []string{ControllerStartMessage}
}
