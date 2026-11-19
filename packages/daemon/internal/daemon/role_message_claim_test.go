package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// putPhaseRow writes row to the phases record of pool.
func putPhaseRow(t *testing.T, pool *store.Pool, records record.Store, row record.PhaseRow) {
	t.Helper()
	if err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		return records.PutPhase(context.Background(), tx, row)
	}); err != nil {
		t.Fatalf("write the %s's phases row: %v", row.Role, err)
	}
}

// outboxRowsOfKind is every row of kind the outbox holds for issue, oldest first.
func outboxRowsOfKind(t *testing.T, pool *store.Pool, issue string, kind record.OutboxKind) []record.OutboxRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select id, kind, issue, payload from outbox
		where kind = $1 and issue = $2 order by id`, string(kind), issue)
	if err != nil {
		t.Fatalf("read the %s rows of %s: %v", kind, issue, err)
	}
	defer rows.Close()
	var held []record.OutboxRow
	for rows.Next() {
		var row record.OutboxRow
		if err := rows.Scan(&row.ID, &row.Kind, &row.Issue, &row.Payload); err != nil {
			t.Fatalf("scan a %s row of %s: %v", kind, issue, err)
		}
		held = append(held, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the %s rows of %s: %v", kind, issue, err)
	}
	return held
}

// Key a's agent, named implementer after its first step, works the tester's step of LEGION-208 in
// the claim it started on the implementer's step, and the tester's phases row names it. The head
// starts conflicting with its base, and the end of the tester's phase is told on the topic of the
// agent that worked it, also where a later entry of the tester's step names another agent before
// the row runs (the agent `…-reviewer`, modelled as the write that entry makes): the conflict's row
// names the claim it judged, and the outbox reads nothing again. The control is the row run
// before anything re-points the tester's row.
func TestAPhaseEndedMessageReachesTheAgentThatWorkedThePhase(t *testing.T) {
	for _, tc := range []struct {
		name    string
		repoint bool
	}{
		{name: "control: the row runs before a later entry re-points the tester's row"},
		{name: "a later entry of the tester's step names another agent before the row runs", repoint: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			publisher := &outboxPublisher{}
			runner, _, pool := conflictRunner(t, phase.Testing, "testing", false, publisher)
			worked := mustClaimToken(t, "LEGION-208", claim.RoleImplementer)
			later := mustClaimToken(t, "LEGION-208", claim.RoleReviewer)
			putPhaseRow(t, pool, runner.records, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleTester, Claim: worked, StepTask: "t", StepPlace: 2, Entry: 2})

			applyTheConflict(t, ctx, pool, runner)
			ended := outboxRowsOfKind(t, pool, "LEGION-208", record.OutboxKindPhaseEnded)
			if len(ended) != 1 {
				t.Fatalf("the conflict wrote the phase-ended rows %+v, want one", ended)
			}
			if tc.repoint {
				putPhaseRow(t, pool, runner.records, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleTester, Claim: later, StepTask: "t", StepPlace: 4, Entry: 3})
			}
			if err := runner.execute(ctx, ended[0]); err != nil {
				t.Fatalf("the end of the tester's phase: %v", err)
			}
			published := publisher.publishes()
			if want := notify.RoleTopicPrefix + string(worked); len(published) != 1 || published[0].topic != want {
				t.Fatalf("the end of the tester's phase was published as %+v, want once on %s, the topic of the agent that worked it", published, want)
			}
		})
	}
}

// A hook prompt names the claim of the agent that works its role's step in the round the move
// back opened, and the outbox publishes on that claim's topic, also where a later entry of the
// role's step names another agent by the time the row runs. A prompt the release before wrote
// names no claim, and reaches the claim of the worker on its role's step when it runs.
func TestAHookPromptReachesTheClaimItNames(t *testing.T) {
	ctx := context.Background()
	publisher := &outboxPublisher{}
	runner, _, pool := hookRunner(t, publisher)
	named := mustClaimToken(t, "LEGION-208", claim.RoleImplementer)
	later := mustClaimToken(t, "LEGION-208", claim.RoleReviewer)
	putPhaseRow(t, pool, runner.records, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: later, StepTask: "t", StepPlace: 3, Entry: 4})
	payload := map[string]any{}
	encoded, err := json.Marshal(firedHook)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "claim")
	before, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	payload["claim"] = string(named)
	naming, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	row := record.OutboxRow{ID: 70, Issue: "LEGION-208", Kind: record.OutboxKindHookPrompt, NextAt: time.Now(), Payload: naming}
	if err := runner.execute(ctx, row); err != nil {
		t.Fatalf("the hook prompt naming %s: %v", named, err)
	}
	if published := publisher.publishes(); len(published) != 1 || published[0].topic != notify.RoleTopicPrefix+string(named) {
		t.Fatalf("the hook prompt naming %s was published as %+v, want once on its topic", named, published)
	}

	old := record.OutboxRow{ID: 71, Issue: "LEGION-208", Kind: record.OutboxKindHookPrompt, NextAt: time.Now(), Payload: before}
	if err := runner.execute(ctx, old); err != nil {
		t.Fatalf("the hook prompt of the release before: %v", err)
	}
	if published := publisher.publishes(); len(published) != 2 || published[1].topic != notify.RoleTopicPrefix+string(later) {
		t.Fatalf("the hook prompt of the release before was published as %+v, want it on %s, the worker on the implementer's step", published, later)
	}
}
