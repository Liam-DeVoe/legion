package replay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// TriggerReady is the merger's ready move of the release at 9542c6fb, which took an issue from
// merging into PhaseAwaitingMerge once the merger had readied its pull request. Today's workflow
// has no such move: a window of that release holds it, and the trace reads it as a mark inside
// merging (MoveSources, assumption 20), where the rule infra-w2 gives the slot back.
const TriggerReady workflow.TriggerKind = "ready"

// PhaseAwaitingMerge is the phase of the release at 9542c6fb in which an issue waited for its
// merge once the merger had readied it. Today's workflow has no such phase: the trace folds the
// time an issue spent in it into merging (MoveSources, assumption 19), so a window of that
// release is replayed on today's phases.
const PhaseAwaitingMerge phase.Phase = "awaiting_merge"

// MoveWay is how a replay makes one kind of recorded phase move. The daemon makes some moves
// itself, from facts a replay feeds it, and a replay must leave those to it; the rest come from
// the record, at the end of the work segment the record shows ending in them.
type MoveWay string

const (
	// MoveTransition is a move made at the end of its recorded work segment through
	// workflow.(*Engine).ReplayTransition: the row lookup and the transition the daemon runs for
	// the trigger, refused unless the issue stands where the record says it stood. It is the way
	// of the completions a handoff or a review settles, whose checks the record shows passed.
	MoveTransition MoveWay = "transition"
	// MoveFact is a move made at the end of its recorded work segment through the daemon's own
	// handler of a fact (MoveSource.Fact), so the guards that handler keeps — a sign-off refused
	// while a child is in the workflow, a merge that waits for the issues beneath it — hold in a
	// replay as they held in the record.
	MoveFact MoveWay = "fact"
	// MovePass is a move the daemon's own admission pass makes from the state a replay reaches,
	// as gate_opened is the promotion of an issue whose first phase the line lets start. A replay
	// never injects it: the recorded move only measures how far the replay's moment is from the
	// record's.
	MovePass MoveWay = "pass"
	// MoveDraw is a move a replay's conflict draw makes, at the rate fitted to the window. The
	// recorded moves of the trigger are not injected, or each conflict would count twice.
	MoveDraw MoveWay = "draw"
	// MoveMark is a move that moves no phase in today's workflow: a fact of the replay's own
	// (MoveSource.Fact) writes a mark inside the phase the issue stands in, which a rule may read.
	MoveMark MoveWay = "mark"
)

// MoveSource is how the trace builder reads one trigger's recorded moves and how a replay makes
// them: the line the record holds each move in, the way the replay makes it and, for a trigger of
// a recorded release that today's table lacks, how the move is mapped onto today's phases and the
// assumptions the mapping rests on.
type MoveSource struct {
	// Line is the msg of the recorded line each move of the trigger is read from, whose trigger
	// attribute is the key the source stands under.
	Line string
	// Way is how the replay makes the move.
	Way MoveWay
	// Fact names the fact the replay applies for the move where Way is MoveFact or MoveMark, as
	// <package>.<type>: a fact of package intake, which the daemon's own handler applies, or one
	// of this package's.
	Fact string
	// To is the phase the replay moves the issue to where it is one phase whatever phase the
	// issue left, as a release's checks_red moved an issue back to implementing from any phase;
	// empty where each move goes where its row of the table goes.
	To phase.Phase
	// Folds is a phase a recorded release entered by this trigger and today's workflow lacks,
	// which the trace reads as the phase the move left: the time an issue spent in it is time in
	// that phase, and a move out of it is a move out of that phase. Empty for a trigger that
	// enters no such phase.
	Folds phase.Phase
	// Assumptions is the numbers of the assumptions (Assumptions) the mapping of the move rests
	// on, for a move of a recorded release today's table lacks; none for a move today's table
	// makes as the record shows it made.
	Assumptions []int
}

// MoveSources is the trace builder's map from a recorded move to the move a replay makes, by the
// trigger the record's line names: one entry for each trigger of a row of the table any rule a
// replay runs carries (workflow.TableFor), and for each trigger of a recorded release that
// today's table lacks, which the entry maps onto today's phases. A recorded move whose trigger
// has no entry here has no source in the replay, and the inventory test of the table
// (TestEveryRowOfTheTableHasASourceInTheRecord) names it.
var MoveSources = map[workflow.TriggerKind]MoveSource{}

// LineHead is what every daemon log line the trace reads carries whatever its kind: the msg the
// daemon wrote it under and the time the daemon wrote it, the line's own time attribute. A Cloud
// Logging export's timestamp is when the log store took the line in, a moment later, and is not
// read.
type LineHead struct {
	Msg  string
	Time time.Time
}

// Head is h. Every line type embeds its head, so a reader of a LogLine of any kind reads the
// line's msg and time through it.
func (h LineHead) Head() LineHead { return h }

// logLine seals LogLine: a type is one only by embedding LineHead.
func (LineHead) logLine() {}

// LogLine is one daemon log line of a kind the trace reads, as the type of its kind
// (ParseLogLine). Each kind is a type that embeds LineHead and holds the attributes the trace reads
// from it, so the trace builder reads a line by its type and fields, and the msgs and attribute
// names of the daemon's lines are read in one place.
type LogLine interface {
	Head() LineHead
	logLine()
}

// ClaimRef is the claim a supervise: line is about, read from the line's claim attribute: the
// claim's token, and the issue and role the token names. A supervise: line names its issue and
// role through the token alone (the machine's logger carries the token, and no such line carries
// an issue), so the trace joins a turn to its issue and role by these. Issue is the key as
// Dispatch writes it, ACME-37 for legion-acme-acme-37-tester; the controller's token names no
// issue and the role claim.RoleController.
type ClaimRef struct {
	Token claim.Token
	Issue string
	Role  claim.Role
}

// PhaseChanged is a `workflow: phase changed` line (workflow's transition): an issue of a tree
// moved from one phase to another by a trigger, and Status is the issue's Dispatch status then.
// From and To are the phases the line names, as written: a phase of a recorded release that
// today's workflow lacks (PhaseAwaitingMerge) is kept for the trace to fold (MoveSources), never
// refused.
type PhaseChanged struct {
	LineHead
	Issue, Tree string
	From, To    phase.Phase
	Trigger     workflow.TriggerKind
	Status      string
}

// SlotTaken is an `admission: slot taken` line (admit's logSlot): the admission pass gave an issue
// a slot, after which the project held Slots of its Cap. A window recorded before the daemon wrote
// a record line for each pass that moves slots reads its slots from this line and SlotReleased
// alone.
type SlotTaken struct {
	LineHead
	Issue string
	// Slots is the slots in use once this one was taken.
	Slots int
	// Cap is the admission cap the pass ran with.
	Cap int
}

// SlotReleased is an `admission: slot released` line (admit's logSlot): the admission pass took an
// issue's slot back, after which the project held Slots of its Cap.
type SlotReleased struct {
	LineHead
	Issue string
	// Slots is the slots in use once this one was released.
	Slots int
	// Cap is the admission cap the pass ran with.
	Cap int
}

// WaitsOnIssues is an `admission waits for what this issue waits on` line: the admission pass
// passed over a member of its waiting line because it waits on the issues Blockers names
// (blockers.LineWaitsOn), in that order.
type WaitsOnIssues struct {
	LineHead
	Issue    string
	Blockers []string
}

// SlotBackWorkReturned is an `admission gives a slot back to an issue whose work came back` line:
// the admission pass gave a slot to an issue whose work a move sent back to it while it held none
// (record.LineReturning), standing in Phase.
type SlotBackWorkReturned struct {
	LineHead
	Issue string
	Phase phase.Phase
}

// SlotBackWaitEnded is an `admission gives a slot back to an issue whose wait ended` line: the
// admission pass gave a slot back to an issue that had given its own up to wait on another issue
// (record.LineWaitingOnIssue), standing in Phase, once that wait was over.
type SlotBackWaitEnded struct {
	LineHead
	Issue string
	Phase phase.Phase
}

// WaitsForChildren is an `admission: an issue waits for the Dispatch children under it` line: an
// issue's own first phase is held while Dispatch children of its own are open (the children-first
// rule), in its tree at the tree's generation.
type WaitsForChildren struct {
	LineHead
	Issue, Tree string
	Generation  uint64
	// Children is how many of the issue's Dispatch children were open.
	Children int
}

// WaitsOnPerson is a `workflow: root waits on a person and keeps its admission slot` line: a root
// entered a wait on a person (an ask, or the review of its design gate's document) for Reason,
// the wait standing since the issue's Dispatch event of seq SinceSeq.
type WaitsOnPerson struct {
	LineHead
	Issue, Tree string
	Reason      string
	SinceSeq    int64
}

// GateOpened is a `workflow: design gate opened` line: an issue's design gate opened at a Dispatch
// event of its document's artifact, of kind Event (an intake.DispatchArtifactKind: approved,
// version, …), at Version.
type GateOpened struct {
	LineHead
	Issue    string
	Artifact string
	Event    string
	Version  int
}

// GateClosed is a `workflow: design gate closed` line: an issue's design gate closed at a Dispatch
// event of its document's artifact, of kind Event (an intake.DispatchArtifactKind: a new version,
// …), at Version.
type GateClosed struct {
	LineHead
	Issue    string
	Artifact string
	Event    string
	Version  int
}

// GateRegistered is a `workflow: design gate registered` line (workflow's gateRegistered): an
// issue's architect registered version Version of its design document Artifact at the design
// step Step, Open whether that version stood approved and ChangesRequested whether a changes
// request stood on it, under the deployment's gated steps GatedSteps (the empty list gates none,
// and the registration opens the gate at once). A replay rebuilds intake.GateRegistered from it
// and the Dispatch shadow, since the registration is an API fact and no Dispatch event carries it.
type GateRegistered struct {
	LineHead
	Issue            string
	Artifact         string
	Step             record.DesignStep
	Version          int
	Open             bool
	ChangesRequested bool
	GatedSteps       []record.DesignStep
}

// HoldReleased is a `workflow: a held phase is released` line (workflow's retryPhase): an issue
// held where its phase's worker failed was put back in Phase, for Reason.
type HoldReleased struct {
	LineHead
	Issue  string
	Phase  phase.Phase
	Reason string
}

// LeftWorkflow is a `workflow: issue left the workflow` line (workflow's leave): an issue of a tree
// left the workflow at the Dispatch status Status.
type LeftWorkflow struct {
	LineHead
	Issue, Tree string
	Status      string
}

// TreeLingers is a `workflow: tree lingers` line: a tree whose work is done lingers, at its
// generation, until Until, when its linger closes it.
type TreeLingers struct {
	LineHead
	Tree       string
	Generation uint64
	Until      time.Time
}

// LingerClosed is a `workflow: linger closed the tree` line: a tree's linger ended at its
// generation and closed the tree's issues, Issues of them.
type LingerClosed struct {
	LineHead
	Tree       string
	Generation uint64
	Issues     int
}

// TurnStarted is a `supervise: a turn started` line: a claim's session began a turn at the claim's
// generation.
type TurnStarted struct {
	LineHead
	Claim      ClaimRef
	Generation uint64
	// Delivery is the task the turn is the turn of (outbox:<id>); "" for a turn that is no task's,
	// whose line carries no delivery.
	Delivery string
}

// TurnEnded is a `supervise: a turn ended` line: a turn of a claim's session at the claim's
// generation is over. The daemon writes the msg in two lines: the turn's own end line, and, where
// the turn's producer reports its outcome (supervise's turnOutcome), the outcome's line, which
// carries the turn's sequence. The trace ends the turn at the first and counts no turn at the
// second.
type TurnEnded struct {
	LineHead
	Claim      ClaimRef
	Generation uint64
	// Outcome is a line that carries sequence: the turn outcome's line; false for the turn's own
	// end line, which carries none.
	Outcome bool
}

// Launched is a `supervise: launched` line: the runtime launched a claim's session at the claim's
// generation, resuming the session the claim recorded where Resumed.
type Launched struct {
	LineHead
	Claim      ClaimRef
	Generation uint64
	Resumed    bool
}

// Suspended is a `supervise: suspended` line: a claim's session was suspended by the act By names
// (released, …) for Reason.
type Suspended struct {
	LineHead
	Claim  ClaimRef
	By     string
	Reason string
}

// ClaimFailed is a `supervise: claim failed` line (supervise's machine): a claim's worker ran out
// of a budget the machine keeps (launches, deaths, prompts) for Cause (died, launch_refused, …),
// Why being the error it gives. A phase worker's failed claim is what puts its issue in the
// phase held (workflow's claimFailed), which writes no line of its own, so the trace reads the
// hold's start from this line and a replay injects intake.ClaimFailed at its offset.
type ClaimFailed struct {
	LineHead
	Claim ClaimRef
	Cause string
	Why   string
}

// DaemonStarted is a `legion daemon started` line: a daemon process of Project finished its boot
// and serves, under the admission cap AdmissionCap.
type DaemonStarted struct {
	LineHead
	Project      string
	AdmissionCap int
}

// ProcessStart is one of the lines a daemon process writes once as it starts, before it opens its
// store: the deployment leak check's line (configured or not), `daemon: stale-plugin relaunches
// install` on the kubernetes runtime, and `daemon: opening the store; …`. A release that predates
// some of them starts with the others, so the trace reads a process's start, and the downtime
// before it, at the first of them after the previous process's lines.
type ProcessStart struct {
	LineHead
}

// BootStage is a `legion workflow boot stage` line: a booting daemon reached Stage (prompts,
// store, launcher, admission, api). It writes the first, prompts, before it opens its store.
type BootStage struct {
	LineHead
	Stage string
}

// QueueLeftNothingRead is a `workflow: a pull request left the merge queue where its issue does
// not merge at that head; nothing is read` line (workflow's dequeued): an issue's pull request left
// GitHub's merge queue at the head HeadSHA (the line's head) while the record no longer merges the
// issue there, for the reason Why, and the daemon read nothing. The trace reads it as a drop from
// the merge queue (SendBack.Drop).
type QueueLeftNothingRead struct {
	LineHead
	Issue       string
	PullRequest int
	HeadSHA     string
	Why         string
}

// QueueLeftReadOnce is a `workflow: a pull request in merging left the merge queue; the daemon
// reads it from GitHub once` line (workflow's dequeued): the pull request of an issue in merging
// left GitHub's merge queue at the head HeadSHA (the line's head), and the daemon reads it from
// GitHub once to learn what became of it. The trace reads it as a drop from the merge queue
// (SendBack.Drop).
type QueueLeftReadOnce struct {
	LineHead
	Issue       string
	PullRequest int
	HeadSHA     string
}

// ParseLogLine reads one daemon log line as the daemon's JSON handler writes it on stderr (one
// JSON object of time, level, msg and the attributes; a Cloud Logging export's jsonPayload) as the
// type of its kind. A line of a msg the trace does not read is nil with no error: the trace skips
// it. A line that is not one JSON object, or has no time, is refused, and so is a line of a kind it
// reads that lacks an attribute its type reads or carries one of another JSON type, so an attribute
// the daemon renames fails the read rather than reading as a zero value. The two attributes whose
// absence a field's doc gives a meaning, TurnStarted.Delivery and TurnEnded.Outcome, are the
// exceptions.
func ParseLogLine(line []byte) (LogLine, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil || raw == nil {
		return nil, fmt.Errorf("replay: a log line is not one JSON object: %s", line)
	}
	attrs := &lineAttrs{raw: raw}
	at, msg := attrs.at("time"), attrs.str("msg")
	if attrs.err != nil {
		return nil, fmt.Errorf("replay: the log line %s %w", line, attrs.err)
	}
	read := lineReaderOf(msg)
	if read == nil {
		return nil, nil
	}
	parsed := read(LineHead{Msg: msg, Time: at}, attrs)
	if attrs.err != nil {
		return nil, fmt.Errorf("replay: the %q line at %s %w", msg, at.Format(time.RFC3339Nano), attrs.err)
	}
	return parsed, nil
}

// TraceReads says whether the trace reads the lines the daemon writes under msg: ParseLogLine
// reads a line of msg as its kind's type, and collect keeps exactly the lines of such a msg.
func TraceReads(msg string) bool { return lineReaderOf(msg) != nil }

// processStartLines is the msgs of the lines a daemon process writes once as it starts, before it
// opens its store (ProcessStart), but the leak check's: the kubernetes runtime's stale-plugin line
// and the store's opening.
var processStartLines = []string{
	"daemon: stale-plugin relaunches install",
	"daemon: opening the store; a connection error that the next attempt can clear is tried again",
}

// leakCheckLine begins both lines of the deployment leak check a daemon process writes as it
// starts (internal/daemon's leak_check.go): configured, or not configured with the settings that
// would name one. Each is a ProcessStart.
const leakCheckLine = "daemon: the deployment leak check is "

// lineReaders is the trace's reader of each line kind it reads, by the msg the daemon writes the
// kind under: the one place the msgs and the attribute names of the daemon's lines are read. Each
// reader takes the line's head and its attributes and reads every attribute its type holds.
var lineReaders = map[string]func(LineHead, *lineAttrs) LogLine{
	"workflow: phase changed": func(h LineHead, a *lineAttrs) LogLine {
		return PhaseChanged{LineHead: h, Issue: a.str("issue"), Tree: a.str("tree"), From: phase.Phase(a.str("from")),
			To: phase.Phase(a.str("to")), Trigger: workflow.TriggerKind(a.str("trigger")), Status: a.str("status")}
	},
	"admission: slot taken": func(h LineHead, a *lineAttrs) LogLine {
		return SlotTaken{LineHead: h, Issue: a.str("issue"), Slots: a.count("slots"), Cap: a.count("cap")}
	},
	"admission: slot released": func(h LineHead, a *lineAttrs) LogLine {
		return SlotReleased{LineHead: h, Issue: a.str("issue"), Slots: a.count("slots"), Cap: a.count("cap")}
	},
	"admission waits for what this issue waits on": func(h LineHead, a *lineAttrs) LogLine {
		return WaitsOnIssues{LineHead: h, Issue: a.str("issue"), Blockers: a.strs("blockers")}
	},
	"admission gives a slot back to an issue whose work came back": func(h LineHead, a *lineAttrs) LogLine {
		return SlotBackWorkReturned{LineHead: h, Issue: a.str("issue"), Phase: phase.Phase(a.str("phase"))}
	},
	"admission gives a slot back to an issue whose wait ended": func(h LineHead, a *lineAttrs) LogLine {
		return SlotBackWaitEnded{LineHead: h, Issue: a.str("issue"), Phase: phase.Phase(a.str("phase"))}
	},
	"admission: an issue waits for the Dispatch children under it": func(h LineHead, a *lineAttrs) LogLine {
		return WaitsForChildren{LineHead: h, Issue: a.str("issue"), Tree: a.str("tree"), Generation: a.generation("generation"),
			Children: a.count("children")}
	},
	"workflow: root waits on a person and keeps its admission slot": func(h LineHead, a *lineAttrs) LogLine {
		return WaitsOnPerson{LineHead: h, Issue: a.str("issue"), Tree: a.str("tree"), Reason: a.str("reason"), SinceSeq: a.seq("since_seq")}
	},
	"workflow: design gate opened": func(h LineHead, a *lineAttrs) LogLine {
		return GateOpened{LineHead: h, Issue: a.str("issue"), Artifact: a.str("artifact"), Event: a.str("event"), Version: a.count("version")}
	},
	"workflow: design gate closed": func(h LineHead, a *lineAttrs) LogLine {
		return GateClosed{LineHead: h, Issue: a.str("issue"), Artifact: a.str("artifact"), Event: a.str("event"), Version: a.count("version")}
	},
	"workflow: design gate registered": func(h LineHead, a *lineAttrs) LogLine {
		var gated []record.DesignStep
		for _, step := range a.strs("gated_steps") {
			gated = append(gated, record.DesignStep(step))
		}
		return GateRegistered{LineHead: h, Issue: a.str("issue"), Artifact: a.str("artifact"), Step: record.DesignStep(a.str("step")),
			Version: a.count("version"), Open: a.flag("open"), ChangesRequested: a.flag("changes_requested"), GatedSteps: gated}
	},
	"workflow: a held phase is released": func(h LineHead, a *lineAttrs) LogLine {
		return HoldReleased{LineHead: h, Issue: a.str("issue"), Phase: phase.Phase(a.str("phase")), Reason: a.str("reason")}
	},
	"workflow: issue left the workflow": func(h LineHead, a *lineAttrs) LogLine {
		return LeftWorkflow{LineHead: h, Issue: a.str("issue"), Tree: a.str("tree"), Status: a.str("status")}
	},
	"workflow: tree lingers": func(h LineHead, a *lineAttrs) LogLine {
		return TreeLingers{LineHead: h, Tree: a.str("tree"), Generation: a.generation("generation"), Until: a.at("until")}
	},
	"workflow: linger closed the tree": func(h LineHead, a *lineAttrs) LogLine {
		return LingerClosed{LineHead: h, Tree: a.str("tree"), Generation: a.generation("generation"), Issues: a.count("issues")}
	},
	"supervise: a turn started": func(h LineHead, a *lineAttrs) LogLine {
		return TurnStarted{LineHead: h, Claim: a.claim("claim"), Generation: a.generation("generation"), Delivery: a.optionalStr("delivery")}
	},
	"supervise: a turn ended": func(h LineHead, a *lineAttrs) LogLine {
		outcome := a.has("sequence")
		if outcome {
			a.seq("sequence")
		}
		return TurnEnded{LineHead: h, Claim: a.claim("claim"), Generation: a.generation("generation"), Outcome: outcome}
	},
	"supervise: launched": func(h LineHead, a *lineAttrs) LogLine {
		return Launched{LineHead: h, Claim: a.claim("claim"), Generation: a.generation("generation"), Resumed: a.flag("resumed")}
	},
	"supervise: suspended": func(h LineHead, a *lineAttrs) LogLine {
		return Suspended{LineHead: h, Claim: a.claim("claim"), By: a.str("by"), Reason: a.str("reason")}
	},
	"supervise: claim failed": func(h LineHead, a *lineAttrs) LogLine {
		return ClaimFailed{LineHead: h, Claim: a.claim("claim"), Cause: a.str("cause"), Why: a.str("why")}
	},
	"legion daemon started": func(h LineHead, a *lineAttrs) LogLine {
		return DaemonStarted{LineHead: h, Project: a.str("project"), AdmissionCap: a.count("admissionCap")}
	},
	"legion workflow boot stage": func(h LineHead, a *lineAttrs) LogLine {
		return BootStage{LineHead: h, Stage: a.str("stage")}
	},
	"workflow: a pull request left the merge queue where its issue does not merge at that head; nothing is read": func(h LineHead, a *lineAttrs) LogLine {
		return QueueLeftNothingRead{LineHead: h, Issue: a.str("issue"), PullRequest: a.count("pullRequest"), HeadSHA: a.str("head"), Why: a.str("why")}
	},
	"workflow: a pull request in merging left the merge queue; the daemon reads it from GitHub once": func(h LineHead, a *lineAttrs) LogLine {
		return QueueLeftReadOnce{LineHead: h, Issue: a.str("issue"), PullRequest: a.count("pullRequest"), HeadSHA: a.str("head")}
	},
}

// readProcessStart reads one of the lines a daemon process starts with (ProcessStart), which
// carries nothing the trace reads but its head.
func readProcessStart(h LineHead, _ *lineAttrs) LogLine { return ProcessStart{LineHead: h} }

// lineReaderOf is the reader of the lines of msg (lineReaders, the process's start lines and the
// leak check's two lines by their common start), nil for a msg the trace does not read.
func lineReaderOf(msg string) func(LineHead, *lineAttrs) LogLine {
	if read, ok := lineReaders[msg]; ok {
		return read
	}
	if slices.Contains(processStartLines, msg) || strings.HasPrefix(msg, leakCheckLine) {
		return readProcessStart
	}
	return nil
}

// lineAttrs is the attributes of one log line, read by name and JSON type. The first attribute
// that is missing, null or of another type is kept in err and every read after it answers a zero
// value, so a reader reads its type's attributes in one expression and the line is refused once.
type lineAttrs struct {
	raw map[string]json.RawMessage
	err error
}

// has says whether the line carries the attribute name at all.
func (a *lineAttrs) has(name string) bool {
	_, ok := a.raw[name]
	return ok
}

// read decodes the attribute name into into, which what names for a refusal.
func (a *lineAttrs) read(name, what string, into any) {
	if a.err != nil {
		return
	}
	raw, ok := a.raw[name]
	switch {
	case !ok:
		a.err = fmt.Errorf("has no %s", name)
	case bytes.Equal(bytes.TrimSpace(raw), []byte("null")):
		a.err = fmt.Errorf("has a null %s, where %s is read", name, what)
	default:
		if err := json.Unmarshal(raw, into); err != nil {
			a.err = fmt.Errorf("has %s %s, where %s is read", name, raw, what)
		}
	}
}

func (a *lineAttrs) str(name string) (value string) {
	a.read(name, "a string", &value)
	return value
}

// optionalStr is the string attribute name, "" where the line carries none.
func (a *lineAttrs) optionalStr(name string) string {
	if !a.has(name) {
		return ""
	}
	return a.str(name)
}

func (a *lineAttrs) strs(name string) (value []string) {
	a.read(name, "a list of strings", &value)
	return value
}

func (a *lineAttrs) count(name string) (value int) {
	a.read(name, "a whole number", &value)
	return value
}

func (a *lineAttrs) seq(name string) (value int64) {
	a.read(name, "a whole number", &value)
	return value
}

func (a *lineAttrs) generation(name string) (value uint64) {
	a.read(name, "a whole number not below zero", &value)
	return value
}

func (a *lineAttrs) flag(name string) (value bool) {
	a.read(name, "true or false", &value)
	return value
}

// at is the attribute name as a time, written as RFC 3339 with or without a fraction of a second.
func (a *lineAttrs) at(name string) time.Time {
	text := a.str(name)
	if a.err != nil {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		a.err = fmt.Errorf("has %s %q, where an RFC 3339 time is read", name, text)
	}
	return at
}

// claim is the claim token in the attribute name, with the issue and role it names (ClaimRef).
func (a *lineAttrs) claim(name string) ClaimRef {
	token := a.str(name)
	if a.err != nil {
		return ClaimRef{}
	}
	ref, err := claimRefOf(claim.Token(token))
	if err != nil {
		a.err = fmt.Errorf("has %s %q: %w", name, token, err)
	}
	return ref
}

// claimRefOf reads the issue and role a claim token names: the role it ends in and the issue
// between the project's part and the role, `legion-<project>-<issue project>-<number>-<role>`
// (claim.NewToken), upper-cased as Dispatch writes a key; or the controller's role and no issue
// for the controller's token (claim.ControllerToken). A token of neither shape is refused.
func claimRefOf(token claim.Token) (ClaimRef, error) {
	role, ok := token.Role()
	if !ok {
		return ClaimRef{}, errors.New("it names no role of a claim")
	}
	ref := ClaimRef{Token: token, Role: role}
	if role == claim.RoleController {
		return ref, nil
	}
	issuePart, _, _ := token.Cut()
	rest, prefixed := strings.CutPrefix(issuePart, "legion-")
	_, issue, cut := strings.Cut(rest, "-")
	if key := strings.ToUpper(issue); prefixed && cut && claim.IsIssueKey(key) {
		ref.Issue = key
		return ref, nil
	}
	return ClaimRef{}, errors.New("it names no issue key between its project and its role")
}

// DispatchEvent is one event of an issue's Dispatch event log as the trace reads it
// (ParseDispatchEvent): the envelope every event carries, and the part of its payload the trace
// reads, in the one field its type fills. An event of a type the trace does not read carries its
// envelope alone.
type DispatchEvent struct {
	// ID is the event's id, which no other event of Dispatch has; Seq is its place in its issue's
	// log.
	ID  int64
	Seq int64
	// Key is the event's issue_key, and Project its project.
	Key     string
	Project string
	// Type is the event's type: issue.created, issue.updated, artifact.approved, ….
	Type string
	// Actor is who Dispatch names as having made the change: a person (user), a session, the
	// daemon's own session (dispatch.DaemonSession) or Dispatch itself (system).
	Actor dispatch.Actor
	// At is the event's created_at.
	At time.Time
	// Issue is the issue as an issue.created, issue.updated or issue.closed event shows it; nil for
	// an event of another type.
	Issue *DispatchIssueState
	// Verdict is an artifact.approved or artifact.changes_requested event's verdict on its
	// document; nil for an event of another type.
	Verdict *DispatchVerdict
	// Ask is the ask an ask.opened or ask.answered event opens or answers; nil for an event of
	// another type.
	Ask *DispatchAsk
	// Handoff is a handoff.recorded event's handoff; nil for an event of another type.
	Handoff *DispatchHandoff
}

// DispatchIssueState is an issue as an issue event shows it once the event applied: the fields the
// event changed, and the state the trace reads the issue's Dispatch facts from.
type DispatchIssueState struct {
	// Changed is the names of the fields the event changed (its payload's changed), in
	// Dispatch's order.
	Changed []string
	Status  string
	// BlockedBy is the keys of the issues the issue waits on, in Dispatch's order.
	BlockedBy []string
	// Parent is the key of the issue's Dispatch parent; "" for an issue under none.
	Parent string
	// Rank is Dispatch's fractional key of the issue, compared as bytes.
	Rank   string
	Labels []string
}

// DispatchVerdict is a verdict on a version of an issue's document: its artifact, the document's
// name (spec.md, …), the version the verdict is on, and the reason a changes request gives ("" for
// an approval).
type DispatchVerdict struct {
	Artifact string
	Name     string
	Version  int
	Reason   string
}

// DispatchAsk is the ask an ask event opens or answers: its id, its kind (question, approval), its
// state once the event applied (open, answered), and the artifact of the document an approval asks
// about ("" for a question).
type DispatchAsk struct {
	ID       string
	Kind     string
	State    string
	Artifact string
}

// DispatchHandoff is a handoff a role recorded on its issue: its id, the phase it hands over (plan,
// …) and the version of that phase's handoff.
type DispatchHandoff struct {
	ID      string
	Phase   string
	Version int
}

// ParseDispatchEvent reads one event as Dispatch's issue event log serves it
// (GET /api/v1/issues/<KEY>/events, the shape intake.DecodeDispatchEvent reads), of any project.
// It refuses an event that is not one JSON object, that lacks its id, seq, issue_key, type, actor
// or created_at, or whose payload lacks a field its type's part reads: an issue event's status, a
// verdict's artifact_id and version, an ask's id, a handoff's handoff_id, phase and version. An
// issue event's changed, blocked_by, parent, rank and labels are read where it carries them, as
// intake reads them, since an event retained from an older Dispatch lacks some; a field present
// with another JSON type is refused all the same. An event without its project is of the project
// its key begins with.
func ParseDispatchEvent(event []byte) (DispatchEvent, error) {
	var envelope struct {
		ID        *int64     `json:"id"`
		Seq       *int64     `json:"seq"`
		IssueKey  *string    `json:"issue_key"`
		Project   *string    `json:"project"`
		Type      *string    `json:"type"`
		CreatedAt *time.Time `json:"created_at"`
		Actor     *struct {
			Kind *string `json:"kind"`
			ID   *string `json:"id"`
		} `json:"actor"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(event, &envelope); err != nil {
		return DispatchEvent{}, fmt.Errorf("replay: a Dispatch event is not one JSON object of an event's fields: %w", err)
	}
	switch {
	case envelope.ID == nil, envelope.Seq == nil, envelope.IssueKey == nil, envelope.Type == nil, envelope.CreatedAt == nil,
		envelope.Actor == nil, envelope.Actor.Kind == nil, envelope.Actor.ID == nil:
		return DispatchEvent{}, fmt.Errorf("replay: the Dispatch event %s lacks its id, seq, issue_key, type, actor or created_at", event)
	}
	parsed := DispatchEvent{ID: *envelope.ID, Seq: *envelope.Seq, Key: *envelope.IssueKey, Type: *envelope.Type,
		Actor: dispatch.Actor{Kind: *envelope.Actor.Kind, ID: *envelope.Actor.ID}, At: *envelope.CreatedAt}
	if envelope.Project != nil {
		parsed.Project = *envelope.Project
	} else {
		parsed.Project, _, _ = strings.Cut(parsed.Key, "-")
	}
	refuse := func(err error) (DispatchEvent, error) {
		return DispatchEvent{}, fmt.Errorf("replay: the %s event of %s at seq %d: %w", parsed.Type, parsed.Key, parsed.Seq, err)
	}
	switch parsed.Type {
	case "issue.created", "issue.updated", "issue.closed":
		var payload struct {
			Changed   []string `json:"changed"`
			Status    *string  `json:"status"`
			BlockedBy []string `json:"blocked_by"`
			Parent    *string  `json:"parent"`
			Rank      *string  `json:"rank"`
			Labels    []string `json:"labels"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return refuse(err)
		}
		if payload.Status == nil {
			return refuse(errors.New("its payload has no status"))
		}
		state := &DispatchIssueState{Changed: payload.Changed, Status: *payload.Status, BlockedBy: payload.BlockedBy, Labels: payload.Labels}
		if payload.Parent != nil {
			state.Parent = *payload.Parent
		}
		if payload.Rank != nil {
			state.Rank = *payload.Rank
		}
		parsed.Issue = state
	case "artifact.approved", "artifact.changes_requested":
		var payload struct {
			ArtifactID *string `json:"artifact_id"`
			Name       *string `json:"name"`
			Version    *int    `json:"version"`
			Reason     *string `json:"reason"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return refuse(err)
		}
		if payload.ArtifactID == nil || payload.Version == nil {
			return refuse(errors.New("its payload has no artifact_id or no version"))
		}
		verdict := &DispatchVerdict{Artifact: *payload.ArtifactID, Version: *payload.Version}
		if payload.Name != nil {
			verdict.Name = *payload.Name
		}
		if payload.Reason != nil {
			verdict.Reason = *payload.Reason
		}
		parsed.Verdict = verdict
	case "ask.opened", "ask.answered", "ask.resolved":
		var payload struct {
			ID       *string `json:"id"`
			Kind     string  `json:"kind"`
			State    string  `json:"state"`
			Approval *struct {
				ArtifactID string `json:"artifact_id"`
			} `json:"approval"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return refuse(err)
		}
		if payload.ID == nil {
			return refuse(errors.New("its payload has no id"))
		}
		ask := &DispatchAsk{ID: *payload.ID, Kind: payload.Kind, State: payload.State}
		if payload.Kind == record.AskApproval && payload.Approval != nil {
			ask.Artifact = payload.Approval.ArtifactID
		}
		parsed.Ask = ask
	case "handoff.recorded":
		var payload struct {
			ID      *string `json:"handoff_id"`
			Phase   *string `json:"phase"`
			Version *int    `json:"version"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return refuse(err)
		}
		if payload.ID == nil || payload.Phase == nil || payload.Version == nil {
			return refuse(errors.New("its payload has no handoff_id, phase or version"))
		}
		parsed.Handoff = &DispatchHandoff{ID: *payload.ID, Phase: *payload.Phase, Version: *payload.Version}
	}
	return parsed, nil
}

// ParseMerges reads pages of GitHub's commit listing of one base branch
// (GET /repos/<owner>/<name>/commits?sha=<branch>&since=<t>&until=<t>, each page newest first, as
// GitHub serves them) and returns the branch's first-parent commits among them, oldest first, each
// at its committer's time, which is when a merge landed it on the branch, and with branch as its
// Branch: the walk from the first page's first commit along each commit's first parent, which ends
// at the first commit whose first parent no page holds. No page, or an empty first page, is a
// branch that did not move in the span. A page that is not a list of commits, or a commit without
// its sha or committer's time, is refused.
func ParseMerges(branch string, pages ...[]byte) ([]Merge, error) {
	type listed struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	bySHA := make(map[string]listed)
	var head string
	for i, page := range pages {
		var commits []listed
		if err := json.Unmarshal(page, &commits); err != nil {
			return nil, fmt.Errorf("replay: page %d of the commits of %s is not a list of commits: %w", i+1, branch, err)
		}
		for _, commit := range commits {
			if commit.SHA == "" || commit.Commit.Committer.Date.IsZero() {
				return nil, fmt.Errorf("replay: page %d of the commits of %s lists a commit without its sha or its committer's time", i+1, branch)
			}
			bySHA[commit.SHA] = commit
		}
		if i == 0 && len(commits) > 0 {
			head = commits[0].SHA
		}
	}
	var merges []Merge
	for sha := head; sha != ""; {
		commit, listed := bySHA[sha]
		if !listed {
			break
		}
		delete(bySHA, sha) // a commit is walked once, whatever a page says of its parents
		merges = append(merges, Merge{SHA: commit.SHA, At: commit.Commit.Committer.Date, Branch: branch})
		sha = ""
		if len(commit.Parents) > 0 {
			sha = commit.Parents[0].SHA
		}
	}
	slices.Reverse(merges)
	return merges, nil
}
