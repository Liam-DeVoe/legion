package store

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// planRatingsRelease names the release whose way back the header of planRecordKindVersion
// (20261117023932_outbox_kinds_with_the_plan_record) carries, as a failure to read that block
// names it. docs/kubernetes.md carries the same block under "Rolling back the release that rates
// each plan", and internal/textcheck's TestThePlanRatingsWayBackIsOneTextInTheHeaderAndTheGuide
// holds the two to one text. This file reads the header alone, so the guide stays out of this
// package's CI filter: a test that read the guide would start the whole daemon Go lane on an edit
// of the guide alone.
const planRatingsRelease = "the release that rates each plan"

// stepRecordBeforeTheRating is record.StepRecord as the release that records each step declares
// it (commit aa36bcbe4e), the release this rollback goes back to: every field of this release's
// shape but PlanRating. It is a copy because that release's type is not in the tree.
// That release decodes a step_record row strictly (record.DecodeOutboxPayload,
// DisallowUnknownFields), so a row this shape refuses is a row it fails at every attempt.
type stepRecordBeforeTheRating struct {
	ID         string     `json:"id"`
	Task       string     `json:"task"`
	Step       int        `json:"step"`
	Kind       string     `json:"kind"`
	Role       string     `json:"role"`
	Type       string     `json:"type"`
	Agent      string     `json:"agent"`
	Generation uint64     `json:"generation"`
	Session    *string    `json:"session,omitempty"`
	Model      *string    `json:"model,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	Result     *string    `json:"result,omitempty"`
	ReturnedTo *int       `json:"returned_to,omitempty"`
	Report     *string    `json:"report,omitempty"`
}

// decodedBeforeTheRating is the error the release before's strict decode of a step_record payload
// gives, nil where it takes the payload.
func decodedBeforeTheRating(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value stepRecordBeforeTheRating
	return decoder.Decode(&value)
}

// The way back of the release that rates each plan deletes the two kinds of row an earlier image
// cannot take, and nothing else: the plan records, and the step records that carry a retro's
// rating. The rows here are an issue's pending writes as this release queues them
// (record.NewOutboxRow): a plan, the retro's rating, the start and the end of a step, the retro's
// own end, whose record id is the rating's, and a status write of the issue. A plan, a rating and a
// step's start and end of an earlier run stand again as rows waiting to retry, as an older
// Dispatch's NOT_FOUND leaves a row (the daemon's RetryOutbox): attempts counted, the last error
// kept and a retry time ahead. The block runs as the migration's header carries it, as one exec
// (the simple protocol a psql run uses). Then the plans and the ratings are gone, waiting or not,
// and every other row is still there with its payload and its retry state: none of them is a
// write the earlier image is owed less than this one.
//
// The control comes first, on the same rows: the strict decode of the release before refuses the
// rating row and takes the ordinary step records. After the block every step record left takes
// that decode, and every row left takes this release's decoder too, which is what the roll forward
// reads. The other kinds left carry payloads this release did not change, so the earlier image
// decodes them with the types it shares with this one.
//
// What this cannot show is the earlier daemon running: its binary is not here, so its decode is
// the copy above (stepRecordBeforeTheRating).
func TestThePlanRatingsWayBackLeavesWhatTheReleaseBeforeReads(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	const issue = "LEGION-208"
	started := time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)
	ended := started.Add(97*time.Minute + 13*time.Second)
	agent := "legion-legion-legion-208-implementer"
	retro := "7a1b2c3d-4e5f-5a6b-8c7d-9e0f1a2b3c4d"
	step := "1f2e3d4c-5b6a-5978-8a9b-0c1d2e3f4a5b"
	earlierRetro := "2b3c4d5e-6f70-5a8b-9c0d-1e2f3a4b5c6d"
	earlierStep := "3c4d5e6f-7081-5b9c-8d0e-2f3a4b5c6d7e"
	// retryAt and retryError are the retry state an older Dispatch's NOT_FOUND leaves on a row:
	// a retry time ahead, and the error of the attempt it answered.
	retryAt := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	retryError := "write the step record of LEGION-208: NOT_FOUND (404): no route for PUT /api/v1/issues/LEGION-208/…"
	queued := []struct {
		name    string
		payload record.OutboxPayload
		kept    bool
		// waiting is a row an attempt has met already, now waiting to retry.
		waiting bool
	}{
		{name: "the plan", payload: record.PlanRecord{ID: "5d6e7f80-91a2-5b3c-8d4e-5f6a7b8c9d0e", Generation: 2, Source: "default",
			Tasks: []record.PlanRecordTask{{ID: "default", Steps: []string{"planner", "implementer", "tester", "reviewer", "acceptance:owner"}}}}},
		{name: "the start of a step", kept: true, payload: record.StepRecord{ID: step, Task: "default", Step: 2, Kind: "step",
			Role: "implementer", Type: "implementation", Agent: agent, Generation: 2, Session: new("s1"), StartedAt: &started}},
		{name: "the retro's rating", payload: record.StepRecord{ID: retro, Task: "default", Step: 0, Kind: "retro", Role: "implementer",
			Agent: agent, Generation: 2, PlanRating: &record.PlanRating{Rating: 4, Reason: "The plan put the tests after the review."}}},
		{name: "the end of a step", kept: true, payload: record.StepRecord{ID: step, Task: "default", Step: 2, Kind: "step",
			Role: "implementer", Type: "implementation", Agent: agent, Generation: 2, Session: new("s1"), Model: new("first/model"),
			StartedAt: &started, EndedAt: &ended, Result: new("passed")}},
		{name: "the retro's end", kept: true, payload: record.StepRecord{ID: retro, Task: "default", Step: 0, Kind: "retro",
			Role: "implementer", Agent: agent, Generation: 2, StartedAt: &started, EndedAt: &ended, Result: new("passed")}},
		{name: "a status write of the issue", kept: true, payload: record.OrdinaryStatusWrite("retro", "in_progress", "")},
		{name: "an earlier run's plan, waiting to retry", waiting: true, payload: record.PlanRecord{ID: "4d5e6f70-8192-5cad-9e0f-3a4b5c6d7e8f",
			Generation: 1, Source: "default", Tasks: []record.PlanRecordTask{{ID: "default", Steps: []string{"planner", "implementer"}}}}},
		{name: "an earlier run's rating, waiting to retry", waiting: true, payload: record.StepRecord{ID: earlierRetro, Task: "default", Step: 0,
			Kind: "retro", Role: "implementer", Agent: agent, Generation: 1, PlanRating: &record.PlanRating{Rating: 2, Reason: "The plan was late."}}},
		{name: "an earlier run's start of a step, waiting to retry", kept: true, waiting: true, payload: record.StepRecord{ID: earlierStep,
			Task: "default", Step: 2, Kind: "step", Role: "implementer", Type: "implementation", Agent: agent, Generation: 1, StartedAt: &started}},
		{name: "an earlier run's end of a step, waiting to retry", kept: true, waiting: true, payload: record.StepRecord{ID: earlierStep,
			Task: "default", Step: 2, Kind: "step", Role: "implementer", Type: "implementation", Agent: agent, Generation: 1,
			StartedAt: &started, EndedAt: &ended, Result: new("failed")}},
	}
	ids := make([]int64, len(queued))
	for i, q := range queued {
		row, err := record.NewOutboxRow(issue, q.payload, started)
		if err != nil {
			t.Fatalf("queue %s: %v", q.name, err)
		}
		attempts, nextAt, lastError := 0, row.NextAt, ""
		if q.waiting {
			attempts, nextAt, lastError = 3, retryAt, retryError
		}
		if err := store.Pool().QueryRow(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error)
			values ($1, $2, $3, $4, $5, $6) returning id`, string(row.Kind), row.Issue, row.Payload, attempts, nextAt, lastError).Scan(&ids[i]); err != nil {
			t.Fatalf("insert %s: %v", q.name, err)
		}
		if row.Kind != record.OutboxKindStepRecord {
			continue
		}
		write, _ := q.payload.(record.StepRecord)
		if err := decodedBeforeTheRating(row.Payload); (write.PlanRating != nil) != (err != nil) {
			t.Fatalf("the release before's decode of %s = %v; the control wants it to refuse the rating row alone, or this test cannot see the row the way back exists for",
				q.name, err)
		}
	}

	if _, err := store.Pool().Exec(ctx, wayBackOf(t, planRecordKindVersion, planRatingsRelease), pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatalf("run the way back the header of migration %d carries: %v", planRecordKindVersion, err)
	}

	rows, err := store.Pool().Query(ctx, `select id, kind, issue, payload, attempts, next_at, last_error from outbox order by id`)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	defer rows.Close()
	// stood is a row left after the way back, with its retry state.
	type stood struct {
		row       record.OutboxRow
		attempts  int
		nextAt    time.Time
		lastError string
	}
	left := map[int64]stood{}
	for rows.Next() {
		var s stood
		var kind string
		if err := rows.Scan(&s.row.ID, &kind, &s.row.Issue, &s.row.Payload, &s.attempts, &s.nextAt, &s.lastError); err != nil {
			t.Fatalf("read an outbox row: %v", err)
		}
		s.row.Kind = record.OutboxKind(kind)
		left[s.row.ID] = s
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	var wantLeft []int64
	for i, q := range queued {
		if q.kept {
			wantLeft = append(wantLeft, ids[i])
		}
		s, stands := left[ids[i]]
		row := s.row
		switch {
		case q.kept && !stands:
			t.Errorf("the way back deleted %s (row %d), which the release before reads and is owed", q.name, ids[i])
			continue
		case !q.kept && stands:
			t.Errorf("the way back left %s (row %d): %s", q.name, ids[i], row.Payload)
		case !stands:
			continue
		}
		decoded, err := record.DecodeOutboxPayload(row)
		if err != nil {
			t.Errorf("this release's decode of %s after the way back: %v", q.name, err)
		} else if !reflect.DeepEqual(decoded, q.payload) {
			t.Errorf("%s reads %#v after the way back, want %#v as it was queued", q.name, decoded, q.payload)
		}
		if q.waiting && (s.attempts != 3 || !s.nextAt.Equal(retryAt) || s.lastError != retryError) {
			t.Errorf("%s waits to retry after the way back with %d attempts, at %s, after %q; want its 3, %s and %q as they were",
				q.name, s.attempts, s.nextAt.UTC(), s.lastError, retryAt, retryError)
		}
		if row.Kind != record.OutboxKindStepRecord {
			continue
		}
		if err := decodedBeforeTheRating(row.Payload); err != nil {
			t.Errorf("the release before refuses %s after the way back, and retries it at every attempt: %v", q.name, err)
		}
	}
	if len(left) != len(wantLeft) {
		t.Errorf("the outbox holds %d rows after the way back, want the %d rows %v", len(left), len(wantLeft), wantLeft)
	}
	// The way back restores no kind check: the rollback keeps this release's, which must take every
	// kind the release before writes, its list (20261117000528's) whole, or that release's first
	// write of a kind left out fails its transaction.
	for _, kind := range stepRecordReleaseKinds {
		if _, err := store.Pool().Exec(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error)
			values ($1, 'LEGION-209', '{}', 0, now(), '')`, kind); err != nil {
			t.Errorf("the release before queues a %s row after the way back: %v", kind, err)
		}
	}
}
