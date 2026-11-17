package legionfacts

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/legion"
)

// TestAMoveOfTheAdmissionBlockAloneRewritesTheWaitingReasonItMoves: a stored row's waiting reason
// reads the daemon's admission block (reasonOf), which the sampler records for the project under
// legion.AdmissionKey and on no issue. An admitted issue with no slot of its own whose blocker
// finishes is moved from the block's Blocked to its Waiting, and nothing of the issue itself is
// observed. The pass that reads the new block must rewrite that issue's reason from `blocked` to
// `ready`, as a computation of the issue answers it, and rewrite no other row: every other open
// row's reason is the same under both blocks.
func TestAMoveOfTheAdmissionBlockAloneRewritesTheWaitingReasonItMoves(t *testing.T) {
	run := newConsumer(t, 3)
	issue, other, blocker := run.fixture.Open[0], run.fixture.Open[1], run.fixture.Keys[1]
	observe := func(key, field string, value any, at time.Time) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode the %s observation: %v", field, err)
		}
		run.exec(t, "observe the "+field+" of "+run.fixture.Project+" "+key, `
			insert into legion_observations (project, issue_key, field, value, observed_at)
			values ($1, $2, $3, $4::jsonb, $5)
		`, run.fixture.Project, key, field, string(encoded), at)
	}
	observe(issue, legion.FieldPhase, legion.PhaseAdmitted, run.at.Add(-time.Hour))
	observe(legion.AdmissionKey, legion.FieldAdmission, legion.Admission{
		Blocked: []legion.BlockedIssue{{Issue: issue, BlockedBy: []string{blocker}}},
	}, run.at.Add(-time.Hour))
	run.pass(t)
	before := run.rows(t)
	if reason := before[issue].WaitingReason; reason != legion.WaitingBlocked {
		t.Fatalf("%s is admitted with no slot and the block holds it on %s, and its row waits for %q, want %q: this test examined nothing",
			issue, blocker, reason, legion.WaitingBlocked)
	}
	if row, has := before[other]; !has || row.ClosedAt != nil {
		t.Fatalf("%s holds no open row, and this test needs one whose reason the block does not move", other)
	}

	run.at = run.at.Add(time.Minute)
	observe(legion.AdmissionKey, legion.FieldAdmission, legion.Admission{Waiting: []string{issue}}, run.at)
	changed := run.pass(t)
	after := run.rows(t)
	fresh := computedPlacement(t, run, issue)

	if reason := after[issue].WaitingReason; reason != legion.WaitingReady {
		t.Errorf("the block moved %s from Blocked to Waiting and its row waits for %q, want %q", issue, reason, legion.WaitingReady)
	}
	if difference := rowDifference(after[issue], fresh); difference != "" {
		t.Errorf("%s's stored row stands apart from its computation: %s", issue, difference)
	}
	if !slices.Equal(changed, []string{issue}) {
		t.Errorf("the pass that read the block alone changed %v, want %s alone: no other row's values moved", changed, issue)
	}
	if !after[other].UpdatedAt.Equal(before[other].UpdatedAt) {
		t.Errorf("%s is open and its reason did not move, and its row was rewritten at %s (it was written at %s)",
			other, after[other].UpdatedAt.UTC(), before[other].UpdatedAt.UTC())
	}
	t.Logf("%s waits for %q, then %q after the block alone moved it (the computation answers %q); the pass changed %v, and %s (open) was written at %s both times",
		issue, before[issue].WaitingReason, after[issue].WaitingReason, fresh.WaitingReason, changed, other, after[other].UpdatedAt.UTC())
}
