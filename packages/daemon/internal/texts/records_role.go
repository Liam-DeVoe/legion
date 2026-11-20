package texts

import (
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// This file holds the records of the role messages: the messages that the daemon publishes to a
// role. Each is published as an object of its fields, apart from the hook prompt, which is
// published as its text (HookPromptRecord.Text).

// CISettledRecord is the CI settlement on a pull request's head (workflow's tellChecks): the pull
// request, the head, the issue's phase (which the urgency rule reads), each check that did not
// pass with its conclusion, each check GitHub skipped, each commit status that did not pass with
// its state, the count of the checks and statuses that passed, and the command that lists them.
type CISettledRecord struct {
	RoleHead
	PullRequest       string            `json:"pull_request"`
	Head              string            `json:"head"`
	Phase             phase.Phase       `json:"phase"`
	NotPassed         []CheckConclusion `json:"not_passed,omitempty"`
	Skipped           []string          `json:"skipped,omitempty"`
	NotPassedStatuses []StatusState     `json:"not_passed_statuses,omitempty"`
	Passed            int               `json:"passed"`
	ListPassed        string            `json:"list_passed"`
}

// The words of a design gate's move (GateMovedRecord.Gate) and of what it does to a merge
// (GateMovedRecord.Merge).
const (
	GateOpened    = "open"
	GateClosed    = "closed"
	MergeHeld     = "held"
	MergeReleased = "released"
)

// GateMovedRecord is the message that the design gate of the tree opened or closed (workflow's
// tellGateMoved): the gate (a Gate word), the step, the step's document (a dispatch:// link that
// names the tree's root), the artifact id, the version, and what the move does to a merge the
// gate holds (a Merge word).
type GateMovedRecord struct {
	RoleHead
	Gate     string `json:"gate"`
	Step     string `json:"step"`
	Document string `json:"document"`
	Artifact string `json:"artifact"`
	Version  int    `json:"version"`
	Merge    string `json:"merge"`
}

// ApprovedRoundStuckRecord is the message to the reviewer that the round it approved is stuck
// because GitHub will not merge the head (workflow's tellStuckMergeState): the pull request, the
// head, its merge state, each check of that head that did not pass, each check of that head GitHub
// skipped, each commit status of that head that did not pass, the count of the checks and statuses
// that passed, and the command that lists them.
type ApprovedRoundStuckRecord struct {
	RoleHead
	PullRequest       string            `json:"pull_request"`
	Head              string            `json:"head"`
	MergeState        string            `json:"merge_state"`
	NotPassed         []CheckConclusion `json:"not_passed,omitempty"`
	Skipped           []string          `json:"skipped,omitempty"`
	NotPassedStatuses []StatusState     `json:"not_passed_statuses,omitempty"`
	Passed            int               `json:"passed"`
	ListPassed        string            `json:"list_passed"`
}

// The words of what stopped a process for memory (MemoryKillRecord.Cause).
const (
	KillGuard           = "guard"
	KillKernel          = "kernel"
	KillKernelProtected = "kernel-protected"
)

// MemoryKillRecord is the message to a role whose process was stopped for memory (workflow's
// MemoryKillMessage): what stopped it (a Kill word), the
// process's executable and pid, how much it held, the pod's memory reading and its limit, each in
// binary units.
type MemoryKillRecord struct {
	RoleHead
	Cause     string `json:"cause"`
	Process   string `json:"process"`
	PID       int    `json:"pid"`
	Size      string `json:"size"`
	PodMemory string `json:"pod_memory"`
	Limit     string `json:"limit"`
}

// MergeForwardAskRecord is the merge-forward ask to the implementer (record.MergeForwardMessage,
// its ask): the pull request, the head that conflicts with its base, the base, the phase the issue
// stays in, and why the daemon acted (record.ConflictWhy's word). The base is absent where the
// daemon has not recorded it (record.PullRequest.Base).
type MergeForwardAskRecord struct {
	RoleHead
	PullRequest   string      `json:"pull_request"`
	Head          string      `json:"head"`
	ConflictsWith string      `json:"conflicts_with,omitempty"`
	Phase         phase.Phase `json:"phase"`
	Why           string      `json:"why"`
}

// MergeForwardHoldRecord is the message to the tester, the reviewer or the merger that holds the
// phase that the implementer merges the base forward in the workspace they share
// (record.MergeForwardMessage, its hold): the facts of the ask.
type MergeForwardHoldRecord struct {
	RoleHead
	PullRequest   string      `json:"pull_request"`
	Head          string      `json:"head"`
	ConflictsWith string      `json:"conflicts_with,omitempty"`
	Phase         phase.Phase `json:"phase"`
	Why           string      `json:"why"`
}

// MergeForwardLandedRecord is the message to that same worker that the merge forward landed
// (record.MergeForwardMessage, its landed): the pull request, the head the merge replaced, the
// head it left (Landed), the base it brought forward, absent where the daemon has not recorded it,
// and the phase the issue stays in.
type MergeForwardLandedRecord struct {
	RoleHead
	PullRequest   string      `json:"pull_request"`
	Head          string      `json:"head"`
	Landed        string      `json:"landed"`
	ConflictsWith string      `json:"conflicts_with,omitempty"`
	Phase         phase.Phase `json:"phase"`
}

// MergeForwardReviewRecord is the ask to the reviewer of an issue past its review to read the
// merge forward that landed and approve the new head or request changes (record.MergeForwardMessage,
// its review): the facts of the landed, the phase being the one the issue stands in.
type MergeForwardReviewRecord struct {
	RoleHead
	PullRequest   string      `json:"pull_request"`
	Head          string      `json:"head"`
	Landed        string      `json:"landed"`
	ConflictsWith string      `json:"conflicts_with,omitempty"`
	Phase         phase.Phase `json:"phase"`
}

// HookPromptRecord is a hook of the deployment that fired on a move back of the issue
// (record.HookPrompt). It is published as text (Text), in an order that the hook's own words fix
// and that dispatch://INFRA-3686/spec R8 keeps word for word: its first line names the hook, the
// issue and the move, then come the hook's words as written, then its facts.
type HookPromptRecord struct {
	Hook   string      `json:"-"`
	Issue  string      `json:"-"`
	From   phase.Phase `json:"-"`
	To     phase.Phase `json:"-"`
	Prompt string      `json:"-"`
	HookFacts
}

// HookFacts is the facts of a hook prompt, below the hook's words: the count of the move that
// fired the hook, every backward-move count of the issue in phase.BackwardPairs order, and the
// issue's pull request where it has one.
type HookFacts struct {
	Count       int         `json:"count"`
	Counts      []MoveCount `json:"counts"`
	PullRequest string      `json:"pull_request,omitempty"`
}

// MoveCount is one backward-move count: the move as <from>-<to> and its count.
type MoveCount struct {
	Move  string `json:"move"`
	Count int    `json:"count"`
}

// Text is the hook prompt as it is published: its first line, kept word for word, a blank line,
// the hook's words exactly as written, a blank line, and the facts in the form Render writes.
func (r HookPromptRecord) Text() (string, error) {
	facts, err := Render(r.HookFacts)
	if err != nil {
		return "", err
	}
	return "Legion hook " + r.Hook + " on " + r.Issue + ": work went back from " + string(r.From) + " to " + string(r.To) + ".\n\n" +
		r.Prompt + "\n\n" + facts, nil
}

// The facts that every update round ask gives, which dispatch://INFRA-2981's R4 keeps: the
// tool op that answers the round, what goes with the pod when the round ends, and where the
// daemon starts the role's process again. Two more are dispatch://INFRA-4203's, which
// dispatch://INFRA-3686's R8 keeps as fields: UpdateBackgroundStep is its R2's step 2, done
// before the answer (let each subagent and each background job finish, or stop one that cannot
// end before the round ends, then commit and push what it made), and UpdateEarlyEnd is when every
// answer brings the round's end forward, its R1's rule: only once no role is in a turn or reports
// background work.
const (
	UpdateAnswerOp       = "ready_for_update"
	UpdateRestartsIn     = "same_session"
	UpdateBackgroundStep = "finish_or_stop_then_push"
	UpdateEarlyEnd       = "when_no_turn_or_background_work"
)

// updateGoesWithPod is what goes with the pod when an update round ends: the turn in progress,
// each subagent, each background job and each named service that still runs, each commit and
// each file the role did not push, the pod's test database and its caches. The round's end waits
// for no named service, since no activity report names one (dispatch://INFRA-4203).
var updateGoesWithPod = []string{"turn", "subagents", "background_jobs", "named_services", "unpushed_work", "test_database", "caches"}

// UpdateAskRecord is an update round's ask to one role of the issue (supervise's UpdateTask):
// the round, the pod it was opened over, each changed image by its short digest and each changed
// setting by its key, the text of the drift that a round an older daemon opened records, and the
// moment the round ends. The facts that dispatch://INFRA-2981's R4 keeps: the branch to push to,
// the op that answers, when every answer brings the end forward and that no answer delays it,
// that only the role's own push keeps its work, what goes with the pod, where the process starts
// again, and that the daemon holds the next task until it runs again. Between the push and the
// answer is the step of the role's background work (background_work, dispatch://INFRA-4203 R2's
// step 2), and every_answer_brings_it_forward says that every answer brings the end forward only
// once no role is in a turn or reports background work (dispatch://INFRA-4203 R1 and R3).
// NewUpdateAsk sets them all. The readers' prompts give the steps, and name these two fields there.
type UpdateAskRecord struct {
	RoleHead
	Round                      string    `json:"round"`
	Pod                        string    `json:"pod,omitempty"`
	Images                     []string  `json:"images,omitempty"`
	Settings                   []string  `json:"settings,omitempty"`
	Reasons                    string    `json:"reasons,omitempty"`
	EndsAt                     time.Time `json:"ends_at"`
	PushTo                     string    `json:"push_to"`
	BackgroundWork             string    `json:"background_work"`
	Answer                     string    `json:"answer"`
	EveryAnswerBringsItForward string    `json:"every_answer_brings_it_forward"`
	NoAnswerDelaysIt           bool      `json:"no_answer_delays_it"`
	KeptByPushOnly             bool      `json:"kept_by_push_only"`
	GoesWithPod                []string  `json:"goes_with_pod"`
	Restart                    string    `json:"restart"`
	NextTaskHeld               bool      `json:"next_task_held"`
}

// NewUpdateAsk is the ask of round, opened over pod, to the role of issue: the changed images
// (each by ShortDigest), the changed settings by their keys, and the moment the round ends, with
// the facts every ask gives.
func NewUpdateAsk(issue, round, pod string, images, settings []string, endsAt time.Time) UpdateAskRecord {
	return UpdateAskRecord{
		RoleHead:                   RoleHead{Kind: UpdateAsk, Issue: issue},
		Round:                      round,
		Pod:                        pod,
		Images:                     images,
		Settings:                   settings,
		EndsAt:                     endsAt,
		PushTo:                     claim.IssueBranch(issue),
		BackgroundWork:             UpdateBackgroundStep,
		Answer:                     UpdateAnswerOp,
		EveryAnswerBringsItForward: UpdateEarlyEnd,
		NoAnswerDelaysIt:           true,
		KeptByPushOnly:             true,
		GoesWithPod:                append([]string(nil), updateGoesWithPod...),
		Restart:                    UpdateRestartsIn,
		NextTaskHeld:               true,
	}
}

// MergeQueueRemovalRecord is the message to the merger that its pull request left the merge
// queue (record.MergeQueueRemoval): the pull request, the head that left the queue, and
// whether no read of GitHub succeeded, so the daemon does not know whether GitHub merged it.
type MergeQueueRemovalRecord struct {
	RoleHead
	PullRequest string `json:"pull_request"`
	Head        string `json:"head"`
	Unread      bool   `json:"unread,omitempty"`
}
