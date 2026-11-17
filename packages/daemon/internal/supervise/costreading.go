package supervise

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
)

// The cost reading (R7 and R9 of dispatch://INFRA-2811): what a window of the daemon's own log
// says a claim's tasks cost, read from the lines this package writes and nothing else. `legion
// task-cost` is its one door (cmd/legion), which reads the daemon container's log from its
// standard input, as the documentation site's page says to pipe it
// (docs/site/src/content/docs/legion/task-delivery-cost.md), and prints TaskCost.Text.
//
// The lines are the daemon's JSON lines (cmd/legion's start writes its log through slog's JSON
// handler): one object per line, with the record's time, its message and its attributes, the
// machine's own claim among them. A line that is no such object — a refusal the command printed
// before its logger existed, a line kubectl added — is passed over, and so is every line outside
// the window or of another claim than the one asked for.
//
// What is counted, and from which line:
//
//   - the turns, from the two turn lines (turnStartedLine, turnEndedLine), by their cause: a task
//     number, or none. The turn-outcome report writes the end message too, with a sequence of its
//     own (turnoutcome.go); that line is the report, not the turn's end, and is passed over;
//   - the turns each number took, from the start lines that name it;
//   - the numbers written, by the origin of the task each names, from the value writes
//     (valueWrittenLine); and of those, the numbers written to a claim that had finished the last
//     task written to it in the session it ran (finished), which is a task written to a claim
//     whose phase waited (R7 and R9's waited figure). The silence repair's writes (OriginSilence)
//     are an origin of their own, read apart from a resume's;
//   - the numbers taken, from the accepted answers to a take (takeAnsweredLine), less an answer
//     that repeats one from the record (repeat), which applied nothing;
//   - the numbers no take reached within the first bound of R5's first wait (TaskCostArgs.Take),
//     each time listed with its claim: R5's stranded task, which check c9 reads as none, and which
//     fails the third verdict below as well (the rule is the paragraphs below this list);
//   - the numbers finished, from taskFinishedLine, by what finished each;
//   - the charges, from the two charge lines (promptFailureLine, promptRetiredLine), by wait, and
//     the retirements among them.
//
// A number is owed from a value write of it (valueWrittenLine), unless an accepted take of it
// stands, until a take of it is accepted or dropped (takeAnsweredLine), it finishes
// (taskFinishedLine), its value is deleted (valueDeletedLine), which a drop makes, or a newer
// number is written to its claim, which replaces it. While it is owed, its clock counts only time
// in which R5's first wait runs, so a worker the daemon would not charge is not counted:
//
//   - the clock starts at the line that reports the number's latest written revision seen
//     (taskSeenLine), and a newer revision of it written stops it until that one is reported seen;
//   - a turn's start stops it, and that turn's end starts it again, the bound whole, a turn that
//     began before the window included. A take-back (takenBackLine) ends the turn its claim stood
//     in, since it is that turn's end or the let-go of its process, which writes no turn end;
//   - a take answered refused, a take the daemon could not decide or record (takeUndecidedLine),
//     and a take-back each stop it until the next value write of the number, whose seen report
//     starts it again;
//   - a death of the claim's process (processDiedLine) ends its turn and stops the clock until
//     the claim's next registration (agentRegisteredLine);
//   - a daemon's start (DaemonStartedLine) stops every clock as of the line before it, since
//     nothing says the daemon before it lived any longer, until the claim's worker next acts in
//     the log: a seen report, a take, a turn line or a registration.
//
// A clock that runs a whole bound has passed it, and that time owed is listed, however it ends
// (a late take included). The bound is judged at every line of the claim the reading decodes but a
// value write of the number itself: the first bound's own end writes the number again after a
// daemon's start, over a seen report that daemon read from the store (takeBoundOver), so a clock
// whose bound ends at a write of its number has not passed it. A time owed whose bound has not
// passed at To is not listed, since the window does not show whether a take came in time. The
// daemon writes no line that always carries the bound — only a charge of the first wait and the
// write of a value nobody reported seeing name it — so the command is told it (TaskCostArgs.Take).
//
// What this figure cannot see can count a number too many or too few. Too many: no line marks
// when a process the daemon started again after a release, a suspension or a relaunch is ready,
// when a compaction that took a task back ends, or when a hold stands over a task its worker asked
// nothing of, and a turn that began before the window's first line and is still running shows no
// start, so a clock that runs through any of them can list a number no worker could have taken.
// Too few: a let-go other than a death that cuts a turn off with no take-back writes no turn end,
// so a clock stopped by that turn starts again only at the end of the claim's next turn or at its
// next registration; and the shim's reconnect hello writes no line, so after a daemon's start a
// clock starts again only at the worker's next act the log shows.
//
// And R9's three verdicts, each of which lists what fails it:
//
//   - no charge of a worker whose turn began within the second bound after its take: a charge made
//     while an accepted take of the claim stood, where a turn of the claim started after that take
//     and no later than the bound after it (the charge's own bound, for a charge of the second
//     wait that names one) or, for every other charge, than the charge itself;
//   - no write of origin resume to a claim that had finished its last task in its session (a value
//     write of origin resume whose finished is true); a silence repair's write is not one;
//   - each number taken once for each time the record owed it: a number is owed once when it is
//     written, and once more at each take-back of it (takenBackLine); no number may be taken
//     more times in the window than it was owed, and none may be owed with no take within the
//     first bound (the figure above, which reads a time owed that no take reached as taken zero
//     times).
//
// Every figure is a lower bound but the numbers no take reached, which can also count too many
// (above): the log reaches back only as far as the node kept the daemon container's log, and a
// take, a write or a turn that fell before the window's first line is not seen.

// TaskCostArgs is what `legion task-cost` is asked: the window of the daemon's log to read, From
// included and To not, the one claim to read it for, "" for every claim, and Take, R5's first
// bound (worker_take_timeout_seconds) as the deployment sets it, which the figure of the numbers
// no take reached is judged by.
type TaskCostArgs struct {
	From  time.Time
	To    time.Time
	Claim string
	Take  time.Duration
}

// taskCostTakeTimeout is --take-timeout's default, in seconds: worker_take_timeout_seconds's own
// (config's key table), which cmd/legion's task-cost test holds it to.
const taskCostTakeTimeout = 300

// ParseTaskCostArgs parses `legion task-cost`'s arguments: --from <RFC 3339> --to <RFC 3339>
// [--claim <token>] [--take-timeout <seconds>]. Both times are required, To must be after From,
// the first bound is a positive number of seconds, 300 when not given, and nothing else may be
// given. A help flag answers flag.ErrHelp, which the command answers with its usage.
func ParseTaskCostArgs(args []string) (TaskCostArgs, error) {
	flags := flag.NewFlagSet("task-cost", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	from := flags.String("from", "", "")
	to := flags.String("to", "", "")
	claim := flags.String("claim", "", "")
	take := flags.Int("take-timeout", taskCostTakeTimeout, "")
	if err := flags.Parse(args); err != nil {
		return TaskCostArgs{}, err
	}
	if flags.NArg() > 0 {
		return TaskCostArgs{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	var parsed TaskCostArgs
	for _, bound := range []struct {
		name, value string
		into        *time.Time
	}{{"from", *from, &parsed.From}, {"to", *to, &parsed.To}} {
		if bound.value == "" {
			return TaskCostArgs{}, fmt.Errorf("--%s is required", bound.name)
		}
		at, err := time.Parse(time.RFC3339, bound.value)
		if err != nil {
			return TaskCostArgs{}, fmt.Errorf("--%s %q is not an RFC 3339 time", bound.name, bound.value)
		}
		*bound.into = at
	}
	if !parsed.To.After(parsed.From) {
		return TaskCostArgs{}, fmt.Errorf("--to %s is not after --from %s", *to, *from)
	}
	if *take <= 0 {
		return TaskCostArgs{}, fmt.Errorf("--take-timeout %d is not a positive number of seconds", *take)
	}
	parsed.Claim, parsed.Take = *claim, time.Duration(*take)*time.Second
	return parsed, nil
}

// TaskCost is what one window of the daemon's log says the claims' tasks cost (ReadTaskCost).
// A task is a number of one claim: every claim counts its numbers from 1, so a number is always
// read beside its claim.
type TaskCost struct {
	Args TaskCostArgs
	// Lines is how many of the daemon's lines fell in the window (and were of the claim asked
	// for), and FirstLine and LastLine the times of the first and the last of them.
	Lines               int
	FirstLine, LastLine time.Time
	// TurnsStarted and TurnsEnded are the turns by their cause.
	TurnsStarted, TurnsEnded TurnCauses
	// TurnsOfTask is how many turns each number took, by claim and then number.
	TurnsOfTask []TaskTurns
	// WrittenByOrigin is how many numbers were written, by origin; WaitedByOrigin how many of them
	// were written to a claim whose phase waited.
	WrittenByOrigin, WaitedByOrigin map[string]int
	// Taken and Finished are how many numbers were taken and finished, and FinishedBy the finished
	// by what finished each.
	Taken, Finished int
	FinishedBy      map[string]int
	// Unreached is each time a number was owed and its first-wait clock passed the first bound
	// (Args.Take) with no take, by claim, number and when the bound passed: when the clock last
	// started, when the bound passed, and how the time owed ended.
	Unreached []string
	// ChargesByWait is how many charges were made, by wait ("none" for a line that names none),
	// and Retirements how many of them retired the process.
	ChargesByWait map[string]int
	Retirements   int
	// ChargedWithinBound, ResumeToFinished and TakenNotOncePerOwed are what fails each of R9's
	// three verdicts, in that order: empty for a verdict that passes. The third lists each number
	// taken more times than it was owed, and then each time owed in Unreached.
	ChargedWithinBound, ResumeToFinished, TakenNotOncePerOwed []string
}

// TurnCauses is a count of turns by cause: a task number, or none.
type TurnCauses struct {
	Task, None int
}

// TaskTurns is how many turns the task Task of the claim Claim took.
type TaskTurns struct {
	Claim string
	Task  uint64
	Turns int
}

// costLine is the part of a costed line the reading asks beyond its head (costReading.line): its
// time, its message, and the attributes of the lines it counts. Task and Sequence are pointers
// because their absence is the answer (a turn of no cause, a turn's end that is no report).
type costLine struct {
	Time     time.Time   `json:"time"`
	Msg      string      `json:"msg"`
	Task     *uint64     `json:"task"`
	Sequence *uint64     `json:"sequence"`
	Revision uint64      `json:"revision"`
	Origin   string      `json:"origin"`
	Finished bool        `json:"finished"`
	Outcome  TakeOutcome `json:"outcome"`
	Repeat   bool        `json:"repeat"`
	By       string      `json:"by"`
	Wait     string      `json:"wait"`
	Bound    int64       `json:"bound"`
}

// costedLines are the messages of a claim the reading decodes whole. A daemon's start
// (DaemonStartedLine) is read too, from its message alone, and it names no claim.
var costedLines = map[string]bool{
	turnStartedLine: true, turnEndedLine: true, valueWrittenLine: true, taskSeenLine: true,
	valueDeletedLine: true, takeAnsweredLine: true, takeUndecidedLine: true, takenBackLine: true,
	taskFinishedLine: true, processDiedLine: true, agentRegisteredLine: true, promptFailureLine: true,
	promptRetiredLine: true,
}

// workerActs are the messages of the worker's own acts the log shows, which start a clock again
// after a daemon's start (claimClock.awaitsAct): its seen report, its take, its turn, its
// registration.
var workerActs = map[string]bool{
	taskSeenLine: true, takeAnsweredLine: true, takeUndecidedLine: true, turnStartedLine: true,
	turnEndedLine: true, agentRegisteredLine: true,
}

// costKey is one task: a number of one claim.
type costKey struct {
	claim  string
	number uint64
}

// openTake is a claim's accepted take whose time owed is running: the number and when it was
// accepted.
type openTake struct {
	number uint64
	at     time.Time
}

// costCharge is one charge, with the take that stood when it was made.
type costCharge struct {
	claim string
	at    time.Time
	wait  string
	bound time.Duration
	take  openTake
}

// owedNumber is a number the record owes a claim, with the clock of R5's first wait on it (the
// rule is the reading's doc comment above): the latest revision written of it and whether that
// revision was reported seen; whether a take refused or undecided, or a take-back, stopped the
// clock until the number's next write; when the clock last started, zero while it stands still;
// when its bound passed, zero while it has not; and, once the time owed ended, how.
type owedNumber struct {
	key          costKey
	revision     uint64
	seen         bool
	stopped      bool
	from, passed time.Time
	ended        string
}

// claimClock is what the reading knows of one claim's first wait: whether its turn lines show a
// turn in progress, whether a daemon's start waits on its worker's next act, whether a death
// waits on its next registration, and the number it owes, nil for none.
type claimClock struct {
	inTurn, awaitsAct, awaitsRegistration bool
	owed                                  *owedNumber
}

// runs is whether the claim's clock runs now: it owes a number whose latest revision was
// reported seen and whose bound has not passed, and nothing stops the clock.
func (k *claimClock) runs() bool {
	o := k.owed
	return o != nil && o.seen && !o.stopped && o.passed.IsZero() && !k.inTurn && !k.awaitsAct && !k.awaitsRegistration
}

// costReading is a reading under way.
type costReading struct {
	cost       TaskCost
	turns      map[costKey]int
	written    map[string]map[costKey]bool
	waited     map[string]map[costKey]bool
	taken      map[costKey]int
	owed       map[costKey]int
	finished   map[costKey]string
	resume     map[costKey]time.Time
	open       map[string]openTake
	turnStarts map[string][]time.Time
	charges    []costCharge
	// clocks is each claim's first wait, unreached each time owed whose clock passed the bound,
	// once that time owed ended, and alive the time of the last line of any claim in the window,
	// which is the last the reading knows a daemon to have lived.
	clocks    map[string]*claimClock
	unreached []owedNumber
	alive     time.Time
}

// ReadTaskCost reads the daemon's JSON lines from r and answers what the window from args.From
// (included) to args.To (not included) says the tasks cost, for the claim args.Claim, or for every
// claim when it is "", with args.Take as the first bound. It fails only when r does.
func ReadTaskCost(r io.Reader, args TaskCostArgs) (TaskCost, error) {
	reading := costReading{
		cost: TaskCost{
			Args:            args,
			WrittenByOrigin: map[string]int{}, WaitedByOrigin: map[string]int{},
			FinishedBy: map[string]int{}, ChargesByWait: map[string]int{},
		},
		turns: map[costKey]int{}, written: map[string]map[costKey]bool{}, waited: map[string]map[costKey]bool{},
		taken: map[costKey]int{}, owed: map[costKey]int{}, finished: map[costKey]string{},
		resume: map[costKey]time.Time{}, open: map[string]openTake{}, turnStarts: map[string][]time.Time{},
		clocks: map[string]*claimClock{},
	}
	lines := bufio.NewReader(r)
	for {
		raw, err := lines.ReadBytes('\n')
		reading.line(raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return TaskCost{}, fmt.Errorf("read the daemon's log: %w", err)
		}
	}
	return reading.done(), nil
}

// line reads one line of the log.
func (c *costReading) line(raw []byte) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return
	}
	var head struct {
		Time  time.Time `json:"time"`
		Msg   string    `json:"msg"`
		Claim any       `json:"claim"`
	}
	if json.Unmarshal(raw, &head) != nil || head.Time.IsZero() {
		return
	}
	claim, _ := head.Claim.(string)
	args := c.cost.Args
	if head.Time.Before(args.From) || !head.Time.Before(args.To) {
		return
	}
	if head.Msg == DaemonStartedLine {
		c.daemonStarted()
	}
	c.alive = head.Time
	if args.Claim != "" && claim != args.Claim {
		return
	}
	if c.cost.Lines == 0 {
		c.cost.FirstLine = head.Time
	}
	c.cost.Lines++
	c.cost.LastLine = head.Time
	if !costedLines[head.Msg] {
		return
	}
	var l costLine
	if json.Unmarshal(raw, &l) != nil {
		return
	}
	c.costed(l, claim)
}

// costed counts one line of the messages the reading decodes whole.
func (c *costReading) costed(l costLine, claim string) {
	if l.Msg == turnEndedLine && l.Sequence != nil {
		// The turn-outcome report's line (turnoutcome.go): the report, not the turn's end.
		return
	}
	var key costKey
	if l.Task != nil {
		key = costKey{claim: claim, number: *l.Task}
	}
	k := c.clocks[claim]
	if k == nil {
		k = &claimClock{}
		c.clocks[claim] = k
	}
	// The bound is judged at every line of the claim but a write of the number its clock is on,
	// which is the first bound's own end after a daemon's start (the reading's doc comment).
	if o := k.owed; o == nil || l.Msg != valueWrittenLine || l.Task == nil || *l.Task != o.key.number {
		c.boundPassed(k, l.Time)
	}
	if k.awaitsAct && workerActs[l.Msg] {
		k.awaitsAct = false
		c.restart(k, l.Time)
	}
	switch l.Msg {
	case turnStartedLine:
		c.turnStarts[claim] = append(c.turnStarts[claim], l.Time)
		c.turnStarted(k)
		if l.Task == nil {
			c.cost.TurnsStarted.None++
			return
		}
		c.cost.TurnsStarted.Task++
		c.turns[key]++
	case turnEndedLine:
		if l.Task == nil {
			c.cost.TurnsEnded.None++
		} else {
			c.cost.TurnsEnded.Task++
		}
		c.turnEnded(k, l.Time)
	case valueWrittenLine:
		if l.Task == nil {
			return
		}
		// A claim's value names its one pending task, so a write of another number than the open
		// take's is of a task queued after that take's task went, whether or not a line said so: a
		// queue retires a task whose turn has run with no take-back or finish line (retirePending).
		if open, ok := c.open[claim]; ok && open.number != key.number {
			delete(c.open, claim)
		}
		mark(c.written, l.Origin, key)
		if l.Finished {
			mark(c.waited, l.Origin, key)
			if _, listed := c.resume[key]; l.Origin == OriginResume && !listed {
				c.resume[key] = l.Time
			}
		}
		c.owe(k, key, l.Revision, l.Time)
	case taskSeenLine:
		if o := k.owed; o != nil && l.Task != nil && *l.Task == o.key.number && l.Revision == o.revision {
			o.seen = true
			c.restart(k, l.Time)
		}
	case valueDeletedLine:
		if l.Task != nil {
			c.endOwed(k, key.number, l.Time, "its value deleted")
		}
	case takeAnsweredLine:
		if l.Task == nil {
			return
		}
		switch l.Outcome {
		case TakeOutcomeRefused:
			c.stop(k, key.number)
			return
		case TakeOutcomeAccepted:
			c.endOwed(k, key.number, l.Time, "taken")
		default:
			c.endOwed(k, key.number, l.Time, "a take answered dropped")
			return
		}
		if !l.Repeat {
			c.taken[key]++
			c.open[claim] = openTake{number: key.number, at: l.Time}
		}
	case takeUndecidedLine:
		if l.Task != nil {
			c.stop(k, key.number)
		}
	case takenBackLine:
		if l.Task == nil {
			return
		}
		c.owed[key] = max(c.owed[key], 1) + 1
		c.closeTake(claim, key.number)
		// No turn outlives a take-back: it is the turn's end, or its process's let-go.
		k.inTurn = false
		c.stop(k, key.number)
	case taskFinishedLine:
		if l.Task == nil {
			return
		}
		c.finished[key] = l.By
		c.closeTake(claim, key.number)
		c.endOwed(k, key.number, l.Time, "finished")
	case processDiedLine:
		// The process and its turn are gone; the next process is the next registration's.
		k.inTurn, k.awaitsRegistration = false, true
		if o := k.owed; o != nil {
			o.from = time.Time{}
		}
	case agentRegisteredLine:
		k.inTurn, k.awaitsRegistration = false, false
		c.restart(k, l.Time)
	case promptFailureLine, promptRetiredLine:
		wait := cmp.Or(l.Wait, "none")
		c.cost.ChargesByWait[wait]++
		if l.Msg == promptRetiredLine {
			c.cost.Retirements++
		}
		c.charges = append(c.charges, costCharge{claim: claim, at: l.Time, wait: wait,
			bound: time.Duration(l.Bound), take: c.open[claim]})
	}
}

// closeTake ends the time owed of the claim's accepted take of number, if that take stands.
func (c *costReading) closeTake(claim string, number uint64) {
	if c.open[claim].number == number {
		delete(c.open, claim)
	}
}

// owe is a write of key's number at revision, at at (the reading's doc comment says what it
// owes). A write of the number the claim owes already is a newer revision of it: the clock stands
// still until that revision is reported seen, and a refusal's stop ends. Otherwise the number the
// claim owed, if any, is replaced, which ends its time owed, and key's number is owed from here,
// unless an accepted take of it stands.
func (c *costReading) owe(k *claimClock, key costKey, revision uint64, at time.Time) {
	if o := k.owed; o != nil && o.key == key {
		o.revision, o.seen, o.stopped, o.from = revision, false, false, time.Time{}
		return
	}
	if o := k.owed; o != nil {
		c.endOwed(k, o.key.number, at, fmt.Sprintf("replaced by task %d", key.number))
	}
	if open, ok := c.open[key.claim]; ok && open.number == key.number {
		return
	}
	k.owed = &owedNumber{key: key, revision: revision}
}

// boundPassed records that the claim's clock passed its bound before at, where it did. It is
// asked at every line of the claim the reading decodes (costed), at a daemon's start, and at the
// window's end.
func (c *costReading) boundPassed(k *claimClock, at time.Time) {
	o := k.owed
	if o == nil || o.from.IsZero() || !o.passed.IsZero() {
		return
	}
	if bound := o.from.Add(c.cost.Args.Take); at.After(bound) {
		o.passed = bound
	}
}

// restart starts the claim's clock from at, the bound whole, where nothing stops it.
func (c *costReading) restart(k *claimClock, at time.Time) {
	if k.runs() {
		k.owed.from = at
	}
}

// stop stops the clock of the claim's number until that number's next write (owe), if it is the
// number the claim owes: a take refused or undecided at its revision, or a take-back of it.
func (c *costReading) stop(k *claimClock, number uint64) {
	if o := k.owed; o != nil && o.key.number == number {
		o.stopped, o.from = true, time.Time{}
	}
}

// turnStarted is the start of a turn of the claim, which stops its clock.
func (c *costReading) turnStarted(k *claimClock) {
	k.inTurn = true
	if o := k.owed; o != nil {
		o.from = time.Time{}
	}
}

// turnEnded is the end of the claim's turn at at: its clock starts again from here, the bound
// whole.
func (c *costReading) turnEnded(k *claimClock, at time.Time) {
	k.inTurn = false
	c.restart(k, at)
}

// daemonStarted is a daemon's start: every clock stops as of the last line before it, and stands
// still until its claim's worker next acts in the log (workerActs).
func (c *costReading) daemonStarted() {
	for _, k := range c.clocks {
		c.boundPassed(k, c.alive)
		k.awaitsAct = true
		if o := k.owed; o != nil {
			o.from = time.Time{}
		}
	}
}

// endOwed ends at at, as how says, the time the claim was owed number, if it owes that number, and
// keeps that time owed for the figure where its clock passed the bound. The line ending it has
// judged the bound already (costed).
func (c *costReading) endOwed(k *claimClock, number uint64, at time.Time, how string) {
	o := k.owed
	if o == nil || o.key.number != number {
		return
	}
	if !o.passed.IsZero() {
		o.ended = how + " " + lineTime(at)
		c.unreached = append(c.unreached, *o)
	}
	k.owed = nil
}

// mark records key under origin in sets.
func mark(sets map[string]map[costKey]bool, origin string, key costKey) {
	if sets[origin] == nil {
		sets[origin] = map[costKey]bool{}
	}
	sets[origin][key] = true
}

// done derives the figures and the verdicts from what the lines left.
func (c *costReading) done() TaskCost {
	cost := c.cost
	for _, key := range slices.SortedFunc(maps.Keys(c.turns), cmpKey) {
		cost.TurnsOfTask = append(cost.TurnsOfTask, TaskTurns{Claim: key.claim, Task: key.number, Turns: c.turns[key]})
	}
	for origin, keys := range c.written {
		cost.WrittenByOrigin[origin] = len(keys)
	}
	for origin, keys := range c.waited {
		cost.WaitedByOrigin[origin] = len(keys)
	}
	cost.Taken, cost.Finished = len(c.taken), len(c.finished)
	for _, by := range c.finished {
		cost.FinishedBy[by]++
	}
	// A number still owed at the window's end is judged there: listed where its clock passed the
	// bound before To, and left out while it has not. The list is sorted below, so the order the
	// claims are read in here does not show.
	for _, k := range c.clocks {
		c.boundPassed(k, c.cost.Args.To)
		if o := k.owed; o != nil && !o.passed.IsZero() {
			o.ended = "still owed at the window's end"
			c.unreached = append(c.unreached, *o)
		}
	}
	slices.SortFunc(c.unreached, func(a, b owedNumber) int {
		return cmp.Or(cmpKey(a.key, b.key), a.passed.Compare(b.passed))
	})
	for _, owed := range c.unreached {
		cost.Unreached = append(cost.Unreached, fmt.Sprintf("%s task %d: the clock started %s, the bound passed %s, %s",
			owed.key.claim, owed.key.number, lineTime(owed.passed.Add(-c.cost.Args.Take)), lineTime(owed.passed), owed.ended))
	}
	for _, charge := range c.charges {
		if started, ok := c.turnWithinBound(charge); ok {
			wait := "wait " + charge.wait
			if charge.bound > 0 {
				wait += ", bound " + charge.bound.String()
			}
			cost.ChargedWithinBound = append(cost.ChargedWithinBound, fmt.Sprintf(
				"%s task %d: taken %s, a turn started %s, charged %s (%s)",
				charge.claim, charge.take.number, lineTime(charge.take.at), lineTime(started),
				lineTime(charge.at), wait))
		}
	}
	for _, key := range slices.SortedFunc(maps.Keys(c.resume), cmpKey) {
		cost.ResumeToFinished = append(cost.ResumeToFinished,
			fmt.Sprintf("%s task %d: written %s", key.claim, key.number, lineTime(c.resume[key])))
	}
	for _, key := range slices.SortedFunc(maps.Keys(c.taken), cmpKey) {
		owed := max(c.owed[key], 1)
		if c.taken[key] > owed {
			cost.TakenNotOncePerOwed = append(cost.TakenNotOncePerOwed,
				fmt.Sprintf("%s task %d: taken %d times, owed %d", key.claim, key.number, c.taken[key], owed))
		}
	}
	for _, owed := range c.unreached {
		cost.TakenNotOncePerOwed = append(cost.TakenNotOncePerOwed, fmt.Sprintf("%s task %d: owed, and no take within the first bound, which passed %s",
			owed.key.claim, owed.key.number, lineTime(owed.passed)))
	}
	return cost
}

// turnWithinBound is the first turn of the charged claim that started after the take the charge
// was made under and no later than the bound after it — the charge's own bound, for a charge of
// the second wait that names one, and the charge itself otherwise — and whether there is one.
func (c *costReading) turnWithinBound(charge costCharge) (time.Time, bool) {
	if charge.take.number == 0 {
		return time.Time{}, false
	}
	limit := charge.at
	if charge.wait == WaitTurnStart && charge.bound > 0 {
		limit = charge.take.at.Add(charge.bound)
	}
	for _, started := range c.turnStarts[charge.claim] {
		if !started.Before(charge.take.at) && !started.After(limit) {
			return started, true
		}
	}
	return time.Time{}, false
}

// cmpKey orders tasks by claim and then by number.
func cmpKey(a, b costKey) int {
	return cmp.Or(strings.Compare(a.claim, b.claim), cmp.Compare(a.number, b.number))
}

// lineTime is a line's time as the daemon's log writes it, to the millisecond.
func lineTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Text is the reading as `legion task-cost` prints it: one figure to a line, each named as the
// documentation site's page defines it, and each verdict with what fails it.
func (c TaskCost) Text() string {
	var b strings.Builder
	whose := "every claim"
	if c.Args.Claim != "" {
		whose = "claim " + c.Args.Claim
	}
	fmt.Fprintf(&b, "window: %s to %s, %s, first bound %s\n", c.Args.From.UTC().Format(time.RFC3339),
		c.Args.To.UTC().Format(time.RFC3339), whose, c.Args.Take)
	if c.Lines == 0 {
		fmt.Fprintln(&b, "daemon lines in the window: 0")
	} else {
		fmt.Fprintf(&b, "daemon lines in the window: %d, the first at %s, the last at %s\n",
			c.Lines, lineTime(c.FirstLine), lineTime(c.LastLine))
	}
	for _, turns := range []struct {
		name   string
		causes TurnCauses
	}{{"turns started", c.TurnsStarted}, {"turns ended", c.TurnsEnded}} {
		fmt.Fprintf(&b, "%s: %d (by a task number %d, with no cause %d)\n", turns.name,
			turns.causes.Task+turns.causes.None, turns.causes.Task, turns.causes.None)
	}
	if len(c.TurnsOfTask) == 0 {
		fmt.Fprintln(&b, "turns each number took: none")
	} else {
		fmt.Fprintln(&b, "turns each number took:")
		for _, t := range c.TurnsOfTask {
			fmt.Fprintf(&b, "  %s task %d: %d\n", t.Claim, t.Task, t.Turns)
		}
	}
	fmt.Fprintf(&b, "numbers written by origin: %s\n", byKey(c.WrittenByOrigin))
	fmt.Fprintf(&b, "numbers written to a claim whose phase waited, by origin: %s\n", byKey(c.WaitedByOrigin))
	fmt.Fprintf(&b, "silence repair (origin %s), counted apart from resume: numbers written %d, to a claim whose phase waited %d\n",
		OriginSilence, c.WrittenByOrigin[OriginSilence], c.WaitedByOrigin[OriginSilence])
	fmt.Fprintf(&b, "numbers taken: %d\n", c.Taken)
	fmt.Fprintf(&b, "numbers owed that no take reached within the first bound: %d\n", len(c.Unreached))
	for _, owed := range c.Unreached {
		fmt.Fprintf(&b, "  %s\n", owed)
	}
	fmt.Fprintf(&b, "numbers finished, by what finished each: %s\n", byKey(c.FinishedBy))
	fmt.Fprintf(&b, "charges by wait: %s\n", byKey(c.ChargesByWait))
	fmt.Fprintf(&b, "retirements: %d\n", c.Retirements)
	for _, verdict := range []struct {
		name  string
		fails []string
	}{
		{"no charge of a worker whose turn began within the second bound after its take", c.ChargedWithinBound},
		{"no write of origin resume to a claim that finished its last task in its session", c.ResumeToFinished},
		{"each number taken once for each time the record owed it", c.TakenNotOncePerOwed},
	} {
		if len(verdict.fails) == 0 {
			fmt.Fprintf(&b, "verdict: %s: pass\n", verdict.name)
			continue
		}
		fmt.Fprintf(&b, "verdict: %s: fail\n", verdict.name)
		for _, fail := range verdict.fails {
			fmt.Fprintf(&b, "  %s\n", fail)
		}
	}
	return b.String()
}

// byKey is counts as `key n, key n` in key order, with their sum, or "none".
func byKey(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	var parts []string
	total := 0
	for _, key := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s %d", key, counts[key]))
		total += counts[key]
	}
	return fmt.Sprintf("%s (%d in all)", strings.Join(parts, ", "), total)
}
