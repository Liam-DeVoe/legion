package replay

// Assumption is one assumption a replay makes about a recorded window under a rule other than the
// one that made it: what it assumes, and the direction in which it moves a result if it is wrong.
// Each run names the assumptions its measures rest on by number (Scorecard.Assumptions), and the
// tool's page in the docs (docs/site/src/content/docs/legion/replay.md) gives each one a section
// with its evidence in a recorded window, its direction for each measure and the reading of a
// window that would test it, so a reader of a result can weigh what it rests on.
type Assumption struct {
	// Number is the assumption's number: 1 to 18 are the list of the tool's spec, in its order,
	// and the numbers after them were added by its plan.
	Number int `json:"number"`
	// Text is what the replay assumes.
	Text string `json:"text"`
	// Direction is the way the assumption moves a result if it is wrong, with the verdict the
	// review of the first replay gave it where it gave one, and what the tool does about it.
	Direction string `json:"direction"`
}

// Assumptions is every assumption a replay makes, by number, in order: the spec's eighteen, then
// the seven the plan added, from the mapping of the release at 9542c6fb onto today's workflow (19
// to 22), the conflict path of the rule main (23), a daemon's downtime (24) and its reading of
// outside blockers (25), then the tool's own: each a reason a fact is left out of a replay or an
// outbox row is finished without acting (26 to 33: FactSources, OutboxHandling), the trace's
// inference of a slot an issue held from before the window's reach (34: SlotSpan.Inferred), the
// recorded conflict a stretch keeps whose loop runs to the window's end (35: keepConflict), and the
// rank a tree's root is shown at under the recorded order of first starts (36: firstStartRanks). A
// run names the ones its measures rest on by these numbers.
var Assumptions = []Assumption{
	{Number: 1,
		Text:      "A work segment takes its recorded time at any start and at any number in flight.",
		Direction: "Doubtful: if load slows work, a rule with more issues in flight is favoured. The tool takes the slowdown factor f, which scales a segment by the difference between the replay's issues in flight and the record's."},
	{Number: 2,
		Text:      "A stretch past the review keeps its recorded length and its outcome.",
		Direction: "Doubtful: if architects and mergers are slower with more issues past review, a rule that keeps slots past review is favoured. The tool keeps it, and the comparison says so."},
	{Number: 3,
		Text:      "Send-backs keep their count and cause.",
		Direction: "Doubtful for conflicts: the first replay saw more conflicts after a longer slot wait, so if a longer wait brings more conflicts than the fitted rate gives, a rule under which branches wait longer is favoured. The tool draws conflicts from the fitted rate, with a branch that sets its merges term to zero, and keeps the other loops."},
	{Number: 4,
		Text:      "A link removal, a child moved out, a gate closing and an outside blocker's close happen at their recorded time, or at their recorded offset from the wait's start.",
		Direction: "Doubtful, and the direction is unknown. The tool runs both as branches."},
	{Number: 5,
		Text:      "A person answers at the recorded time, or at the recorded offset.",
		Direction: "Doubtful, and the direction is unknown. The tool runs both as branches."},
	{Number: 6,
		Text:      "The issues in flight at the window's start enter in their recorded state.",
		Direction: "Sound for one window, and not for a steady state: the start state carries the recorded rule's choices into every rule's first day, which favours the recorded rule. The tool reports the first day apart."},
	{Number: 7,
		Text:      "An issue may start once its todo, its links, its gate, its ancestors and its tree allow it, at the times recorded.",
		Direction: "Wrong in the first replay, which read only todo and links: a hold the replay misses starts an issue early, which favours a rule that starts more issues. The tool reads every hold."},
	{Number: 8,
		Text:      "The work of an issue that never started in the window is unknown.",
		Direction: "A scenario that gives such an issue work that finishes favours a rule that starts more issues, and the scenario that gives it none disfavours that rule. The tool names the scenario and never calls it a bound."},
	{Number: 9,
		Text:      "The line orders returned work first and then by rank.",
		Direction: "Sound: a replay runs the line's own code, so it orders the line as the daemon does, and a rule that orders it otherwise is that rule's own code."},
	{Number: 10,
		Text:      "A link or a child has the lifetime that its events give it.",
		Direction: "Wrong in the first replay, which merged them over the window: a lifetime merged over the window lengthens each wait on it, which favours a rule under which a wait gives its slot back. The tool keeps each one's lifetime."},
	{Number: 11,
		Text:      "A slot that comes free is given out in the same pass.",
		Direction: "Sound for the record, where the median was 18 microseconds and the longest 88 seconds: a slower hand-out would lengthen every rule's waits for a slot alike."},
	{Number: 12,
		Text:      "A waiting slot costs no pod after the idle release.",
		Direction: "Sound after 10 minutes: a pod kept longer would cost a rule that holds idle slots more than the replay counts."},
	{Number: 13,
		Text:      "Work after a send-back takes its recorded time, or that time less its wait for a slot.",
		Direction: "Doubtful: the recorded time holds the recorded rule's own wait for a slot, which favours that rule. The tool runs both as branches."},
	{Number: 14,
		Text:      "A cover under spec v4's rule.",
		Direction: "Wrong in the first replay, which left out some covers and kept others too long, and either misstates the rule with covers. The tool runs the rule's own code."},
	{Number: 15,
		Text:      "An issue that stops working at the window's end would not have finished.",
		Direction: "Wrong: it counts the end of the window as a stop, which disfavours a rule that leaves more work under way at the end. The tool reports issues finished and work left, apart."},
	{Number: 16,
		Text:      "An issue that reopens keeps its later work.",
		Direction: "Wrong in the first replay, which dropped it: dropping the later work understates the issues finished and the slots held under every rule. The tool keeps it."},
	{Number: 17,
		Text:      "The links and children of an issue are the same under every rule.",
		Direction: "Doubtful, because a planner files children after its parent starts: children filed at their recorded times favour a rule that starts parents later. The tool moves a child's filing with its parent's start."},
	{Number: 18,
		Text:      "A conflict under the release that a project runs next behaves as recorded.",
		Direction: "Wrong for a project that moves to a release with cybermonklabs/legion#4464, where a conflicting head keeps its phase, and its recorded conflict loops overstate the conflicts of that release. The tool takes the conflict behaviour of the release as part of the rule."},
	{Number: 19,
		Text:      "Under the release at 9542c6fb, whose workflow has the phase awaiting_merge, the time in awaiting_merge is folded into merging.",
		Direction: "A slot held in awaiting_merge counts as held in merging, so a rule that keeps a slot in merging is charged that time and a rule that keeps none is not."},
	{Number: 20,
		Text:      "Under the release at 9542c6fb, the merger's ready move is a mark inside merging, where the rule legion-v19.57.3 gives the slot back.",
		Direction: "If that release gave the slot back at another moment of merging, the slot-hours past the review under legion-v19.57.3 move by the time between the two."},
	{Number: 21,
		Text:      "Under the release at 9542c6fb, checks_red from any phase is a move back to implementing.",
		Direction: "A checks_red that did not send the work back would count a send-back loop that never ran, which disfavours that release's rule."},
	{Number: 22,
		Text:      "Under the release at 9542c6fb, red checks and conflicts are one cause of a send-back.",
		Direction: "A window of that release cannot tell how a rule changes conflicts, so a forecast across the change scores the cause only inside the total of all send-back loops, and the result says so."},
	{Number: 23,
		Text:      "Merge-forward work under main lasts as long as the implementing round of a recorded conflict loop.",
		Direction: "A merge forward shorter than a conflict's implementing round overstates the conflict cost of main, which disfavours main."},
	{Number: 24,
		Text:      "A daemon down between two boots runs no pass.",
		Direction: "Sound for the record: what arrives while it is down waits for the next boot, so a replay that ran a pass then would start issues earlier than any daemon could."},
	{Number: 25,
		Text:      "An outside blocker's close is seen at the next tick or boot, as the daemon sees it.",
		Direction: "A close the daemon saw sooner would end link waits sooner, so reading it at the tick lengthens link waits, which disfavours a rule that keeps its slot through a wait."},
	{Number: assumeNoticesChangeNothing,
		Text:      "A notice, a message or a prompt the daemon sends a worker or posts on Dispatch changes nothing the record's workers did after it: their work is the record's segments.",
		Direction: "A notice that changed what a worker did, or how soon, would move that work by the replay's own timing of the notice; under the recorded rule each notice falls where the record's did, and under another rule the work it would have moved stays where the record put it, which favours the recorded rule's timing."},
	{Number: assumePublicationsDecideNothing,
		Text:      "What the daemon publishes on Dispatch for people and tools to read, the gated steps, the design gates, the role holders and the step and plan records, is read back by no decision a replay runs.",
		Direction: "Sound: the daemon decides from its own record and Dispatch's events, and a person who acted on a publication did so in a recorded event the replay applies."},
	{Number: assumeWorkspaceInSegment,
		Text:      "The work of a pod or a workspace, a branch made, a pod stopped, bundled, released or removed, and a role that a pause or an idle pod's release stops, lies inside the recorded work segments and the record's own pause.",
		Direction: "A rule that starts more issues at once may wait longer for pods and workspaces than the record did, which the recorded lengths do not carry, and that favours such a rule; the slowdown factor f is the tool's reading of it."},
	{Number: assumeNoGitHub,
		Text:      "GitHub moves a replay only by the moves the record shows: a pull request's opening, reviews, checks, mergeability, merge queue and pushes are read from the recorded moves, and the replay's own pull request record stands in for the issue's.",
		Direction: "A rule that changes how long a head waits may change what GitHub reports of it, a conflict above all; the tool draws conflicts from the fitted rate (assumption 3) and keeps every other report as the record's moves, which favours a rule under which heads wait longer."},
	{Number: assumeNoProductionCheck,
		Text:      "A production check after a merge, its looks and its wakes, changes nothing of a replay: it runs after the issue is done.",
		Direction: "Sound for the measures, which end at an issue's done; a check that reopened the issue is the record's own reopen, which the replay applies."},
	{Number: assumeSupervisionSettles,
		Text:      "A worker's claim settles as the record shows it: it launches, is ready and confirms its task at its start, its turns are the record's, and no silence, lost session, memory kill or full disk of supervision's moves the issue.",
		Direction: "A supervision fault the record holds shows as the claim failure, retry or hold it led to, which the replay enters from the record; a fault a rule would bring on more or less often is not drawn, which favours a rule that runs more at once."},
	{Number: assumeCompletionPassed,
		Text:      "A worker's completion passes the checks a live one passes before its move, its claim's run, its handoff record and its review's head, since the record shows it passed them; the move enters at the end of its segment.",
		Direction: "Sound under the recorded rule; under another, a completion whose check would have failed on a later head is entered as passed, which favours a rule under which more completions meet a moved head."},
	{Number: assumeBootRepairsNothing,
		Text:      "The repairs a boot or a read of the repository mapping makes, a merger the cutover left released, a design gate's request found again, a repository's mapping changed, change nothing of a window.",
		Direction: "Sound for a window recorded under one release and one mapping: a repair the record made shows as its own move or start, which the replay enters as recorded."},
	{Number: assumeSlotFromBeforeTheReach,
		Text:      "An issue the record first shows in a phase that keeps a slot, whose first slot line gives a slot back or which no slot line names, held a slot from before the window's reach, until that line or, with none, until the rule gives the slot back; and an issue that stands at the window's start in a worker's phase that keeps a slot, with no slot line holding it there and no wait that gives the slot back, whose worker started in that run of phases after its last slot went back, held one from its last entry into that run until the rule gives it back.",
		Direction: "Doubtful where the reach begins with slots in use and no line names them, or a slot is taken again by a way that writes no slot line: the slot is read from the phase, and at the window's start from the worker's start, alone. If such an issue held no slot, the slots held, with no worker in a turn above all, are overstated by its span; started work that waits for a slot is understated, its time read as held; in a replay its slot keeps another issue from starting until it goes back, which delays that issue and understates the issues finished; the priority taken by holders is overstated where it ranks below an issue that waits; and of the other delays, a link wait it opens counts from its slot, which lengthens the waits, the loops being read from their own lines. A holder the inference misses moves each the other way. The reading that would test it is the slots in use the slot line at the window's start carries, the first at or after it, against the slots the trace holds right after that line: the start state reports the difference and the inferred holders it wrote, and the run carries both in its divergence."},
	{Number: assumeKeptOpenLoop,
		Text:      "A stretch whose recorded conflict loop runs to the window's end keeps that conflict, and its issue runs its own loop.",
		Direction: "Doubtful, and the direction is unknown: the record holds no work of the issue after such a stretch but its loop, so a draw of no conflict, or of another loop, would leave the issue idle to the window's end; kept, the stretch conflicts in every replay, more often than the fitted rate gives, which overstates the conflicts of every rule alike. The tool counts the conflicts it keeps."},
	{Number: assumeRootRanksByNextStart,
		Text:      "Under the recorded order of first starts, a tree's root ranks at the earliest recorded first start among the issues of its tree that have not yet taken their first slot.",
		Direction: "A choice of the main branch, not a fact of the record: a tree's next child takes the place its own recorded start gives it among every tree's work, so the branch starts children in the order the record started them, ahead of a root's later work where the record did so. The branches under rank keep the board's ranks and show how much the choice moves the result."},
}

// assumeSlotFromBeforeTheReach is the assumption the trace's own inference rests on, that an issue
// the record first shows in a phase that keeps a slot held one from before the window's reach
// (inferSlots), and that an issue standing at the window's start in a worker's phase that keeps a
// slot, whose worker started there with no slot line holding it, held one (inferStartSlots), each
// a SlotSpan.Inferred, which the start state and a run name where they rest on it.
const assumeSlotFromBeforeTheReach = 34

// assumeKeptOpenLoop is the assumption a stretch that keeps its recorded conflict rests on, its loop
// running to the window's end in the record (driver.keepsOpenLoop), which a run names where it
// keeps one (Divergence.KeptOpenLoops).
const assumeKeptOpenLoop = 35

// assumeRootRanksByNextStart is the assumption the main branch's order of first starts rests on,
// that a tree's root ranks at its tree's next recorded first start (firstStartRanks, driver.rerank),
// which a run under FirstStarts recorded names where any issue ranks by its first start.
const assumeRootRanksByNextStart = 36
