package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// agentComment is a writing_check_events row of an agent's comment read by alice, with outcome.
func agentComment(outcome string) WritingCheckEvent {
	return WritingCheckEvent{Route: "/api/v1/issues/{key}/comments", Method: "POST", ActorKind: "session", ActorID: "s-agent", Outcome: outcome, Reader: "alice"}
}

// insertWritingCheckEvents records events, each dated at its time.
func insertWritingCheckEvents(t *testing.T, store *Store, at map[time.Time][]WritingCheckEvent) {
	t.Helper()
	ctx := context.Background()
	for when, events := range at {
		for _, event := range events {
			if err := store.InsertWritingCheckEvent(ctx, event); err != nil {
				t.Fatalf("record %s: %v", event.Outcome, err)
			}
			if _, err := store.Pool.Exec(ctx, `update writing_check_events set created_at = $1 where id = (select max(id) from writing_check_events)`, when); err != nil {
				t.Fatalf("date %s: %v", event.Outcome, err)
			}
		}
	}
}

// A text posted over a finding is counted once, whatever rows record a finding of it: the writing
// check's findings row of a comment edit and the reviewer's reviewed_after of it, which name one
// write, count one text, on the project the reviewer's row names; so do a held text's
// posted_as_written beside the writing check's row of its write. A row of a write no other row
// names is a text of its own, and a review after the post that found nothing posts over no
// finding.
func TestTheWritingCheckCountsCountATextWithBothRowsOnce(t *testing.T) {
	store := openWordRecordsStore(t)
	day := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	checked, reviewed := agentComment("findings"), agentComment("reviewed_after")
	checked.Rules, checked.WriteID = []string{"phrasal-verb"}, "write-1"
	reviewed.Rules, reviewed.Project, reviewed.WriteID = []string{"noslop"}, "WCK", "write-1"
	heldCheck, held, posted := agentComment("findings"), agentComment("held"), agentComment("posted_as_written")
	heldCheck.Rules, heldCheck.WriteID = []string{"phrasal-verb"}, "held-1"
	held.Project, held.WriteID = "WCK", "held-1"
	posted.Rules, posted.Project, posted.WriteID = []string{"phrases"}, "WCK", "held-1"
	alone := agentComment("findings")
	alone.Rules = []string{"phrasal-verb"}
	clean := agentComment("reviewed_after")
	clean.Project, clean.WriteID = "WCK", "write-2"
	insertWritingCheckEvents(t, store, map[time.Time][]WritingCheckEvent{
		day:                      {checked, heldCheck, held, alone},
		day.Add(time.Minute):     {reviewed, clean},
		day.Add(2 * time.Minute): {posted},
	})
	counts, err := store.CountWritingCheck(context.Background(), day, day, 50, 0)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	want := []WritingCheckRow{
		{Day: "2026-10-20", Project: "", Person: "alice", PostedOverFinding: 1},
		{Day: "2026-10-20", Project: "WCK", Person: "alice", ReviewerHeld: 1, PostedOverFinding: 2},
	}
	if !slices.Equal(counts.Items, want) {
		t.Errorf("rows = %+v\nwant %+v: the comment and the held text once each, and the write no other row names", counts.Items, want)
	}
	if got := counts.Days[0].Outcomes; got["findings"] != 3 || got["reviewed_after"] != 2 || got["posted_as_written"] != 1 {
		t.Errorf("the day's outcomes = %v, want every row counted", got)
	}
}

// The sweep deletes the rows past WritingCheckRetention that R8's counts do not read, and keeps
// those they read whatever their age, so the counts of a day past the retention are whole; a row
// inside the retention stays.
func TestTheWritingCheckSweepKeepsTheRowsTheCountsRead(t *testing.T) {
	store := openWordRecordsStore(t)
	now := time.Now().UTC()
	old := now.Add(-WritingCheckRetention - 48*time.Hour)
	findings, reviewed := agentComment("findings"), agentComment("reviewed_after")
	findings.Rules, reviewed.Rules, reviewed.Project = []string{"phrasal-verb"}, []string{"noslop"}, "WCK"
	insertWritingCheckEvents(t, store, map[time.Time][]WritingCheckEvent{
		old: {agentComment("clean"), agentComment("exempt_no_reader"), findings, agentComment("held"), agentComment("rewrite_requested"), reviewed},
		now: {agentComment("clean")},
	})
	deleted, err := store.SweepWritingCheckEvents(context.Background())
	if err != nil || deleted != 2 {
		t.Fatalf("sweep = %d, %v; want the old clean and exempt_no_reader rows deleted", deleted, err)
	}
	counts, err := store.CountWritingCheck(context.Background(), old, old, 50, 0)
	if err != nil {
		t.Fatalf("count the old day: %v", err)
	}
	want := []WritingCheckRow{
		{Day: old.Format(time.DateOnly), Project: "", Person: "alice", RewriteUses: 1, ReviewerHeld: 1, PostedOverFinding: 1},
		{Day: old.Format(time.DateOnly), Project: "WCK", Person: "alice", PostedOverFinding: 1},
	}
	if !slices.Equal(counts.Items, want) {
		t.Errorf("the old day's rows = %+v\nwant %+v", counts.Items, want)
	}
	if today, err := store.CountWritingCheck(context.Background(), now, now, 50, 0); err != nil || today.Days[0].Outcomes["clean"] != 1 {
		t.Errorf("today's outcomes = %+v (%v), want its clean row kept", today.Days, err)
	}
}
