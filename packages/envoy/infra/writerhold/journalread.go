package writerhold

import (
	"context"
	"sort"
	"strings"

	"github.com/sjawhar/envoy/internal/holdrecord"
)

// This file holds every reader the writer hold's commands have of the hold journal's format, which
// internal/holdrecord writes and does not export yet: the states of an entry, the output in which a
// stop records what its command left, and the inputs in which abort's intent carries each stop's
// record. Each literal of that format is here and nowhere else in the code of writerhold and
// buckets (buckets' tests, in package buckets, spell the same literals and change with these).
// Each goes when holdrecord exports its own reader beside the code that writes the format:
//   - stateIntent, stateDone, stateStop and latestState: holdrecord's latestIsStop (journal.go),
//     exported as a reader of a command's latest state, with stateIntent, stateDone and stateStop
//     (journal.go) exported;
//   - priorLeft's cut-run rule: holdrecord's cutRun (journal.go);
//   - stopLeftOutput: holdrecord's stopLeftKey (journal.go);
//   - abortLeftPrefix and stopRecords: a reader of the keys holdrecord's withStopRecords
//     (client.go, Admit's abort inputs) writes;
//   - journalKind and hasJournal: a typed answer from holdrecord's Journal for a hold with no
//     journal, which today answers that with an untyped error.

// The states of a journal entry, as holdrecord's stateIntent, stateDone and stateStop write them.
const (
	stateIntent = "intent"
	stateDone   = "done"
	stateStop   = "stop"
)

// stopLeftOutput is the output of a stop entry that holds what its command left (holdrecord's
// stopLeftKey, which AppendStopLeft writes).
const stopLeftOutput = "left"

// abortLeftPrefix begins each input of abort's intent that carries one stop's record of what its
// command left, `left.<the stop's index in the journal>` (holdrecord's withStopRecords).
const abortLeftPrefix = "left."

// journalKind is the record kind of a hold's journal (holdrecord's journalKind).
const journalKind = "done"

// hasJournal reports whether hold holdID has a journal: false when the broker lists no record of the
// journal's kind for it, which startHold leaves when its journal insert after the row's move did not
// land. A journal the broker has seen and that is gone outside its routes reads below its floor,
// holdrecord.ErrUnknown, never as none.
func hasJournal(ctx context.Context, records *holdrecord.Client, holdID string) (bool, error) {
	found, err := records.Records(ctx, holdID, journalKind)
	if err != nil {
		return false, err
	}
	return len(found) > 0, nil
}

// latestState is the state of command's latest entry in the journal, or "".
func latestState(d holdrecord.Done, command string) string {
	state := ""
	for _, op := range d.Ops {
		if op.Command == command {
			state = op.State
		}
	}
	return state
}

// endedBy is the command whose done entry ended the hold, `hold release` or `hold abort`, or ""
// while neither has one (holdrecord's EndIsFinal).
func endedBy(d holdrecord.Done) string {
	for _, op := range d.Ops {
		if op.State == stateDone && (op.Command == holdrecord.CommandRelease || op.Command == holdrecord.CommandAbort) {
			return op.Command
		}
	}
	return ""
}

// LatestDone is command's latest done entry in the journal, and false when it has none. `envoy-infra
// buckets recreate` reads the backup's done entry of its hold through it.
func LatestDone(d holdrecord.Done, command string) (holdrecord.Operation, bool) {
	for i := len(d.Ops) - 1; i >= 0; i-- {
		if d.Ops[i].Command == command && d.Ops[i].State == stateDone {
			return d.Ops[i], true
		}
	}
	return holdrecord.Operation{}, false
}

// priorLeft is what earlier runs of command in d left, or "" when every one recorded
// LeftUnchanged: a run whose intent has no stop after it was cut and may have changed anything its
// inputs name, and a stop that recorded a change, or nothing, counts as changed.
func priorLeft(d holdrecord.Done, command string) string {
	var left []string
	pending := false
	for _, op := range d.Ops {
		if op.Command != command {
			continue
		}
		switch op.State {
		case stateIntent:
			if pending {
				left = append(left, "an earlier run was cut after its intent, and may have changed what its inputs name")
			}
			pending = true
		case stateStop:
			pending = false
			switch recorded := op.Outputs[stopLeftOutput]; recorded {
			case holdrecord.LeftUnchanged:
			case "":
				left = append(left, "an earlier run stopped recording nothing, so counted as changed")
			default:
				left = append(left, "an earlier run left: "+recorded)
			}
		case stateDone:
			pending = false
		}
	}
	if pending {
		left = append(left, "an earlier run was cut after its intent, and may have changed what its inputs name")
	}
	return strings.Join(left, "; ")
}

// stopRecords are the records of what each stopped command left, as abort's latest intent carries
// them (holdrecord.Admit's left.<index> inputs), in the order of their keys.
func stopRecords(d holdrecord.Done) []string {
	for i := len(d.Ops) - 1; i >= 0; i-- {
		op := d.Ops[i]
		if op.Command != holdrecord.CommandAbort || op.State != stateIntent {
			continue
		}
		keys := make([]string, 0, len(op.Inputs))
		for k := range op.Inputs {
			if strings.HasPrefix(k, abortLeftPrefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		records := make([]string, len(keys))
		for j, k := range keys {
			records[j] = op.Inputs[k]
		}
		return records
	}
	return nil
}
