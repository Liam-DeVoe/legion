package store

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store/migrations"
)

// planRatingsMigration is the migration whose header carries the way back of the release that rates
// each plan. docs/kubernetes.md carries the same block under "Rolling back the release that rates
// each plan", and internal/textcheck's TestThePlanRatingsWayBackIsOneTextInTheHeaderAndTheGuide
// holds the two to one text. This file reads the header alone, so the guide stays out of this
// package's CI filter: a test that read the guide would start the whole daemon Go lane on an edit
// of the guide alone.
const planRatingsMigration = "20261117023932_outbox_kinds_with_the_plan_record.up.sql"

// planRatingsWayBack is the way back's block as the header of planRatingsMigration carries it, read
// from the embedded migration (migrations.All), which is that file's bytes as the binary ships them:
// the one run of header lines indented two spaces past the comment's own, with the comment marker
// and the indent taken off each. A header with no such run, or with two, stops the test by name: a
// block this cannot find is a block it would pass while running nothing.
func planRatingsWayBack(t *testing.T) string {
	t.Helper()
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("read the embedded migrations: %v", err)
	}
	i := slices.IndexFunc(all, func(m migrations.Migration) bool { return m.Name == planRatingsMigration })
	if i < 0 {
		t.Fatalf("the embedded migrations hold no %s, whose header carries the way back this test runs", planRatingsMigration)
	}
	var block []string
	ended := false
	for _, line := range strings.Split(all[i].SQL, "\n") {
		comment, isComment := strings.CutPrefix(line, "--")
		if !isComment {
			// The first line that is no comment is the migration's first statement, so the
			// header is over.
			break
		}
		held, inBlock := strings.CutPrefix(comment, "   ")
		switch {
		case inBlock && ended:
			t.Fatalf("%s carries two indented blocks in its header, want the one way back", planRatingsMigration)
		case inBlock:
			block = append(block, held)
		case len(block) > 0:
			ended = true
		}
	}
	if len(block) == 0 {
		t.Fatalf("%s carries no indented block in its header: that block is the way back an operator runs", planRatingsMigration)
	}
	return strings.Join(block, "\n")
}

// stepRecordBeforeTheRating is record.StepRecord as the release that records each step declares
// it (commit aa36bcbe4e), the release this rollback goes back to: every field of this release's
// shape but Rating and RatingReason. It is a copy because that release's type is not in the tree.
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
// own end, whose record id is the rating's, and a status write of the issue. The block runs as the
// migration's header carries it, as one exec (the simple protocol a psql run uses). Then the plan
// and the rating are gone, and every other row is still there with its payload: none of them is a
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
	queued := []struct {
		name    string
		payload record.OutboxPayload
		kept    bool
	}{
		{name: "the plan", payload: record.PlanRecord{ID: "5d6e7f80-91a2-5b3c-8d4e-5f6a7b8c9d0e", Generation: 2, Source: "default",
			Tasks: []record.PlanRecordTask{{ID: "default", Steps: []string{"planner", "implementer", "tester", "reviewer", "acceptance:owner"}}}}},
		{name: "the start of a step", kept: true, payload: record.StepRecord{ID: step, Task: "default", Step: 2, Kind: "step",
			Role: "implementer", Type: "implementation", Agent: agent, Generation: 2, Session: new("s1"), StartedAt: &started}},
		{name: "the retro's rating", payload: record.StepRecord{ID: retro, Task: "default", Step: 0, Kind: "retro", Role: "implementer",
			Agent: agent, Generation: 2, Rating: new(4), RatingReason: new("The plan put the tests after the review.")}},
		{name: "the end of a step", kept: true, payload: record.StepRecord{ID: step, Task: "default", Step: 2, Kind: "step",
			Role: "implementer", Type: "implementation", Agent: agent, Generation: 2, Session: new("s1"), Model: new("first/model"),
			StartedAt: &started, EndedAt: &ended, Result: new("passed")}},
		{name: "the retro's end", kept: true, payload: record.StepRecord{ID: retro, Task: "default", Step: 0, Kind: "retro",
			Role: "implementer", Agent: agent, Generation: 2, StartedAt: &started, EndedAt: &ended, Result: new("passed")}},
		{name: "a status write of the issue", kept: true, payload: record.OrdinaryStatusWrite("retro", "in_progress", "")},
	}
	ids := make([]int64, len(queued))
	for i, q := range queued {
		row, err := record.NewOutboxRow(issue, q.payload, started)
		if err != nil {
			t.Fatalf("queue %s: %v", q.name, err)
		}
		if err := store.Pool().QueryRow(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error)
			values ($1, $2, $3, 0, $4, '') returning id`, string(row.Kind), row.Issue, row.Payload, row.NextAt).Scan(&ids[i]); err != nil {
			t.Fatalf("insert %s: %v", q.name, err)
		}
		if row.Kind != record.OutboxKindStepRecord {
			continue
		}
		write, _ := q.payload.(record.StepRecord)
		if err := decodedBeforeTheRating(row.Payload); (write.Rating != nil) != (err != nil) {
			t.Fatalf("the release before's decode of %s = %v; the control wants it to refuse the rating row alone, or this test cannot see the row the way back exists for",
				q.name, err)
		}
	}

	if _, err := store.Pool().Exec(ctx, planRatingsWayBack(t), pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatalf("run the way back %s's header carries: %v", planRatingsMigration, err)
	}

	rows, err := store.Pool().Query(ctx, `select id, kind, issue, payload from outbox order by id`)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	defer rows.Close()
	left := map[int64]record.OutboxRow{}
	for rows.Next() {
		var row record.OutboxRow
		var kind string
		if err := rows.Scan(&row.ID, &kind, &row.Issue, &row.Payload); err != nil {
			t.Fatalf("read an outbox row: %v", err)
		}
		row.Kind = record.OutboxKind(kind)
		left[row.ID] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	var wantLeft []int64
	for i, q := range queued {
		if q.kept {
			wantLeft = append(wantLeft, ids[i])
		}
		row, stands := left[ids[i]]
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
}
