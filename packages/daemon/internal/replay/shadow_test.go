package replay

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// answersApproval is a person answering key's approval request ask at at (ask.answered), the
// close of the request Dispatch writes beside the approval that answers it.
func (b *windowBuilder) answersApproval(at time.Time, key, ask string) {
	b.t.Helper()
	architect := eventActor{Kind: "session", ID: string(b.token(key, claim.RoleArchitect))}
	b.event(at, key, "ask.answered", builtPerson, map[string]any{
		"id": ask, "issue_key": key, "author": architect, "kind": record.AskApproval, "state": "answered",
		"approval": map[string]any{"artifact_id": b.artifact(key), "version": 1, "requested_version": 1},
		"answer":   map[string]any{"at": at, "user": builtPerson.ID},
	})
}

// midwayWindow is a window from 10:00 to 12:00, its inputs reaching back to 08:00, that starts
// half-way through a tree's work, and the id of its root's design document. ACME-1, a root, is
// promoted at 08:05; its architect writes version 1 of its spec at 08:15, asks a person to approve
// it at 08:17 and registers it at 08:20, which opens the gate of a deployment that gates no step,
// and ACME-1 plans from then. At 08:30 the architect files ACME-2 under it, which starts at once and
// implements from 09:00. Both stand in the workflow at the window's start, the approval request
// open. At 10:30 the person approves the version, answering the request; at 11:00 ACME-2's
// implementer's turn ends and the person sets ACME-2 done, and it leaves the workflow; at 11:30
// ACME-1 moves on to implementing.
func midwayWindow(t *testing.T) (Window, string) {
	b := newWindowBuilder(t, clock(10, 0), clock(12, 0))
	b.reaches(2 * time.Hour)
	b.boot(clock(8, 0))
	b.file(clock(8, 5), "ACME-1", filing{Title: "Export the widget list", Status: "todo", Rank: "a0", Labels: handedToLegion})
	b.rootPromoted(clock(8, 5), "ACME-1")
	b.specWritten(clock(8, 15), "ACME-1")
	b.asksApproval(clock(8, 17), "ACME-1", approvalAsk)
	b.rootDesigned(clock(8, 20), "ACME-1")
	b.file(clock(8, 30), "ACME-2", filing{Title: "Write the export's header row", Parent: "ACME-1", Tree: "ACME-1", Status: "todo", Rank: "a1"})
	b.childStarts(clock(8, 30), "ACME-2")
	b.segmentEnds(clock(9, 0), "ACME-2", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	b.approves(clock(10, 30), "ACME-1", approvalAsk)
	b.answersApproval(clock(10, 30), "ACME-1", approvalAsk)
	b.turnEnds(clock(11, 0), "ACME-2", claim.RoleImplementer)
	b.edit(clock(11, 0), "ACME-2", setStatus(record.DoneStatus))
	b.leaves(clock(11, 0), "ACME-2", record.DoneStatus)
	b.segmentEnds(clock(11, 30), "ACME-1", phase.Planning, phase.Implementing, workflow.TriggerPlannerCompleted)
	return b.build(), b.artifact("ACME-1")
}

// newestBefore is, by issue key, the recorded sequence of each issue's newest Dispatch event before
// w's start.
func newestBefore(t *testing.T, w Window) map[string]int64 {
	t.Helper()
	newest := map[string]int64{}
	for key, events := range w.Events {
		for _, raw := range events {
			event, err := ParseDispatchEvent(raw)
			if err != nil {
				t.Fatalf("read an event of %s: %v", key, err)
			}
			if event.At.Before(w.From) {
				newest[key] = max(newest[key], event.Seq)
			}
		}
	}
	return newest
}

// approvalRead is what one settle of a replay reads of a document's approval from its copy of
// Dispatch (shadow.approvalOf).
type approvalRead struct {
	at              time.Time
	latest          int
	ask, waitingOn  string
	approvedVersion int
	approvedBy      string
}

// A replay of a window that starts half-way starts its copy of Dispatch where the record stands at
// the start, so a Dispatch write of an issue placed at the start applies after it, and an answer
// after the start is read against the ask from before it. The test replays midwayWindow under main:
// at the start ACME-1 and ACME-2 each stand in the copy at the recorded sequence of their newest
// event before it, and the copy holds the approval request ACME-1's architect opened at 08:17, open
// on the person's turn; every settle from the start to the person's approval at 10:30 reads that
// request open on version 1 of the document, written at 08:15, and every settle from then reads the
// version approved by the person and no request open, the request from before the start being the
// one the answer closed; and the person's done of ACME-2 at 11:00 is applied, no fence dropping it,
// so ACME-2 leaves the workflow then.
func TestAPlacedIssuesDispatchWritesApplyAfterAMidwayStart(t *testing.T) {
	dsn := replayDSN(t)
	window, artifact := midwayWindow(t)
	if trace, err := BuildTrace(window); err != nil || len(trace.Unplaced) != 0 {
		t.Fatalf("the trace of midwayWindow: %v, could not place %s", err, describeReading(trace.Unplaced))
	}
	newest := newestBefore(t, window)
	for _, key := range []string{"ACME-1", "ACME-2"} {
		if newest[key] == 0 {
			t.Fatalf("the window holds no event of %s before its start, which the test needs", key)
		}
	}

	var copied *shadow
	started := map[string]int64{}
	var opened shadowAsk
	var reads []approvalRead
	out := t.TempDir()
	cfg := RunConfig{Window: window, Rule: mainRule(t), Branch: MainBranch, Seed: 1, Slowdown: 1, PGDSN: dsn, Out: out}
	cfg.atStart = func(s *shadow) {
		copied = s
		for key, iss := range s.issues {
			started[key] = iss.seq
		}
		if iss, ok := s.issues["ACME-1"]; ok && iss.asks[approvalAsk] != nil {
			opened = *iss.asks[approvalAsk]
		}
	}
	cfg.atSettled = func(_ context.Context, _ *store.Store, at time.Time) error {
		if copied == nil {
			return nil
		}
		read := copied.approvalOf("ACME-1", artifact)
		got := approvalRead{at: at, latest: read.latest, ask: read.ask, waitingOn: read.waitingOn}
		if read.approved != nil {
			got.approvedVersion, got.approvedBy = read.approved.version, read.approved.actor.Kind+":"+read.approved.actor.ID
		}
		reads = append(reads, got)
		return nil
	}
	result, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("replay midwayWindow under main: %v", err)
	}
	if copied == nil {
		t.Fatal("the run never handed the test its copy of Dispatch at the window's start")
	}
	for _, key := range slices.Sorted(maps.Keys(newest)) {
		if started[key] != newest[key] {
			t.Errorf("at the window's start the copy of Dispatch holds %s at sequence %d, want %d, the recorded sequence of its newest event before the start",
				key, started[key], newest[key])
		}
	}
	if opened.kind != record.AskApproval || opened.artifact != artifact || !opened.open || opened.waitingOn != "human" {
		t.Errorf("at the window's start the copy of Dispatch holds ACME-1's ask %s as %+v, want the approval request of %s opened at 08:17, open on the person's turn",
			approvalAsk, opened, artifact)
	}
	person := builtPerson.Kind + ":" + builtPerson.ID
	before, after := 0, 0
	for _, read := range reads {
		switch {
		case read.at.Before(clock(10, 30)):
			before++
			if read.latest != 1 || read.ask != approvalAsk || read.waitingOn != "human" || read.approvedVersion != 0 {
				t.Errorf("at %s the copy of Dispatch reads ACME-1's document as %+v, want version 1 under the request %s from before the start, open on the person's turn, and no approval",
					stamp(read.at), read, approvalAsk)
			}
		default:
			after++
			if read.latest != 1 || read.ask != "" || read.approvedVersion != 1 || read.approvedBy != person {
				t.Errorf("at %s the copy of Dispatch reads ACME-1's document as %+v, want version 1 approved by %s and no request open", stamp(read.at), read,
					person)
			}
		}
	}
	if before == 0 || after == 0 {
		t.Errorf("the settles read ACME-1's document %d times before the approval at 10:30 and %d from it, want some of each: %+v", before, after, reads)
	}
	asks := copied.issues["ACME-1"].asks
	if len(asks) != 1 || asks[approvalAsk] == nil || asks[approvalAsk].open {
		var held []shadowAsk
		for _, id := range slices.Sorted(maps.Keys(asks)) {
			held = append(held, *asks[id])
		}
		t.Errorf("after the run the copy of Dispatch holds ACME-1's asks %+v, want the one request from before the start, closed by the answer", held)
	}

	if result.Divergence.Fenced != 0 {
		t.Errorf("a fence of the daemon's dropped %d of the replay's Dispatch events, want none", result.Divergence.Fenced)
	}
	left := linesAt(replayLog(t, out), func(line replayLine) bool {
		return line.Msg == "workflow: issue left the workflow" && line.Issue == "ACME-2"
	})
	if want := []time.Time{clock(11, 0)}; !slices.EqualFunc(left, want, time.Time.Equal) {
		t.Errorf("ACME-2 left the workflow at %s, want once, at the person's done, %s", describeTimes(left), describeTimes(want))
	}
}
