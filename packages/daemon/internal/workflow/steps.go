package workflow

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// defaultTask is the one task of today's fixed phases: until an issue carries a plan of its own,
// every step of it is a step of this task.
const defaultTask = "default"

// stepOf is the step a phase runs as a step of the task default: the place its count of starts is
// kept under, its place in the task (from 1 for a step, 0 for a retro or a merge, which no plan
// names, so the kind tells those two apart), its role and its type ("" for a retro or a merge).
// A phase that runs no step — admitted, awaiting a person, held, done — answers ok false; a held
// issue's step is the step of the phase its hold is from (record.StandsAt), which the caller reads.
func stepOf(p phase.Phase) (place record.StepPlace, step int, role, typ string, ok bool) {
	at := func(kind string, step int, role, typ string) (record.StepPlace, int, string, string, bool) {
		return record.StepPlace{Task: defaultTask, Kind: kind, Place: step}, step, role, typ, true
	}
	switch p {
	case phase.Planning:
		return at("step", 1, "planner", "planning")
	case phase.Implementing:
		return at("step", 2, "implementer", "implementation")
	case phase.Testing:
		return at("step", 3, "tester", "testing")
	case phase.Reviewing:
		return at("step", 4, "reviewer", "review")
	case phase.Accepting:
		return at("step", 5, "acceptance", "review")
	case phase.Retro:
		return at("retro", 0, "implementer", "")
	case phase.Merging:
		return at("merge", 0, "merger", "")
	}
	return record.StepPlace{}, 0, "", "", false
}

// endOf is how a transition on trigger ends the step it moves the issue out of. A move back a role
// or a report asked for — the tester's fail, a review's request for changes, a role's or the
// architect's backward move — ends it failed; a move back nobody asked for, a head that conflicts
// with its base, ends it stopped. Both return the work to the step the issue moves into, which the
// record names. Every other move ends the step passed and returns nowhere.
func endOf(trigger TriggerKind) (result string, returns bool) {
	switch trigger {
	case TriggerTesterFailed, TriggerReviewRejected, TriggerBackward:
		return "failed", true
	case TriggerConflict:
		return "stopped", true
	}
	return "passed", false
}

// stepRecordNamespace is the namespace of every step record's id.
var stepRecordNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("legion-step-record"))

// StepID is the id of the record of one run of one step: a version 5 uuid of the issue, its run
// (record.Issue.Generation), the task, the kind, the place and the count of starts of that place
// (record.Store.StepStarts). The start of the run raises the count and the end reads it unchanged,
// so the two writes compute one id apart, across a restart too, and the next start of the same
// place, which raises the count again, writes another record.
func StepID(issue string, generation uint64, task, kind string, step int, starts int64) string {
	name := fmt.Sprintf("%s/%d/%s/%s/%d/%d", issue, generation, task, kind, step, starts)
	return uuid.NewSHA1(stepRecordNamespace, []byte(name)).String()
}
