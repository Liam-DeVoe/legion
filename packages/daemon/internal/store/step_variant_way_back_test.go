package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// stepVariantWayBackExtract is the copy of the way back docs/kubernetes.md gives under "Rolling
// back the release that names the variant on each step record", which internal/textcheck's
// TestTheStepVariantWayBackExtractIsTheGuidesBlock holds to the guide's block, byte for byte. This
// file reads the copy beside it rather than the guide, so the guide stays out of this package's
// CI filter: a test that read the guide would start the whole daemon Go lane on an edit of the
// guide alone.
const stepVariantWayBackExtract = "step_variant_way_back_extract.txt"

// stepVariantWayBack is the block the extract carries: everything under its run of `#` lines. A
// copy with nothing under its header stops the test by name: a block this cannot find is a block
// it would pass while running nothing.
func stepVariantWayBack(t *testing.T) string {
	t.Helper()
	text, err := os.ReadFile(stepVariantWayBackExtract)
	if err != nil {
		t.Fatalf("read the way back of the release that names the variant on each step record: %v", err)
	}
	body := string(text)
	for strings.HasPrefix(body, "#") {
		_, body, _ = strings.Cut(body, "\n")
	}
	if body = strings.TrimSpace(body); body == "" {
		t.Fatalf("%s carries nothing under its `#` header", stepVariantWayBackExtract)
	}
	return body
}

// stepRecordBeforeTheVariant is record.StepRecord as the release that rates each plan declares
// it, the release this rollback goes back to: every field of this release's shape but
// StepExperiment. It is a copy because that release's type is not in the tree. That release
// decodes a step_record row strictly (record.DecodeOutboxPayload, DisallowUnknownFields), so a row
// this shape refuses is a row it fails at every attempt.
type stepRecordBeforeTheVariant struct {
	stepRecordBeforeTheRating
	*record.PlanRating
}

// decodedBeforeTheVariant is the error the release before's strict decode of a step_record
// payload gives, nil where it takes the payload.
func decodedBeforeTheVariant(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value stepRecordBeforeTheVariant
	return decoder.Decode(&value)
}

// statementsOf is block cut into its statements, each ending at a line that ends with `;`, a
// comment above a statement kept with it.
func statementsOf(block string) []string {
	var statements, lines []string
	for _, line := range strings.Split(block, "\n") {
		lines = append(lines, line)
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			statements, lines = append(statements, strings.Join(lines, "\n")), nil
		}
	}
	return statements
}

// stepVariantQueued is one pending row of an issue as this release queues it, and whether an
// attempt has met it already, so that it waits to retry.
type stepVariantQueued struct {
	name    string
	payload record.OutboxPayload
	waiting bool
}

// stepVariantWayBackRows is an issue's pending writes as this release queues them: the start and
// the end of a step and the retro's rating, each naming the issue's variant, the end waiting to
// retry as an older Dispatch's NOT_FOUND of the experiment route leaves it; a step record that
// names none; a plan; and a status write.
func stepVariantWayBackRows() []stepVariantQueued {
	started := time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC)
	ended := started.Add(42 * time.Minute)
	agent := "legion-legion-legion-311-implementer"
	step := "1f2e3d4c-5b6a-5978-8a9b-0c1d2e3f4a5b"
	drawn := &record.StepExperiment{Experiment: "planning", Variant: "b"}
	return []stepVariantQueued{
		{name: "the start of a step, naming the variant", payload: record.StepRecord{ID: step, Task: "default", Step: 2, Kind: "step",
			Role: "implementer", Type: "implementation", Agent: agent, Generation: 1, Session: new("s1"), StartedAt: &started,
			StepExperiment: drawn}},
		{name: "the end of that step, naming the variant and waiting to retry", waiting: true, payload: record.StepRecord{ID: step,
			Task: "default", Step: 2, Kind: "step", Role: "implementer", Type: "implementation", Agent: agent, Generation: 1,
			Session: new("s1"), Model: new("first/model"), StartedAt: &started, EndedAt: &ended, Result: new("passed"),
			StepExperiment: drawn}},
		{name: "the retro's rating, naming the variant", payload: record.StepRecord{ID: "7a1b2c3d-4e5f-5a6b-8c7d-9e0f1a2b3c4d",
			Task: "default", Step: 0, Kind: "retro", Role: "implementer", Agent: agent, Generation: 1,
			PlanRating: &record.PlanRating{Rating: 4, Reason: "The plan fit the work."}, StepExperiment: drawn}},
		{name: "a step record that names no variant", payload: record.StepRecord{ID: "3c4d5e6f-7081-5b9c-8d0e-2f3a4b5c6d7e",
			Task: "default", Step: 1, Kind: "step", Role: "planner", Type: "planning", Agent: agent, Generation: 1, StartedAt: &started}},
		{name: "the plan", payload: record.PlanRecord{ID: "5d6e7f80-91a2-5b3c-8d4e-5f6a7b8c9d0e", Generation: 1, Source: "default",
			Tasks: []record.PlanRecordTask{{ID: "default", Steps: []string{"planner", "implementer"}}}}},
		{name: "a status write of the issue", payload: record.OrdinaryStatusWrite("retro", "in_progress", "")},
	}
}

// stepVariantWayBackProblems runs block over stepVariantWayBackRows on a store of its own, as one
// exec (the simple protocol a psql run uses), and answers each way the rows it leaves fall short:
// a row deleted, a step record the release before still refuses, a row that this release's
// decoder reads other than as it was queued with its variant taken out, and a waiting row whose
// retry state moved.
func stepVariantWayBackProblems(t *testing.T, block string) []string {
	t.Helper()
	ctx := context.Background()
	store := emptyStore(t)
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	const issue = "LEGION-311"
	queuedAt := time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC)
	retryAt := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	retryError := "write the step record of LEGION-311: NOT_FOUND (404): no route for PUT /api/v1/issues/LEGION-311/…/experiment"
	queued := stepVariantWayBackRows()
	ids := make([]int64, len(queued))
	for i, q := range queued {
		row, err := record.NewOutboxRow(issue, q.payload, queuedAt)
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
	}
	if strings.TrimSpace(block) != "" {
		if _, err := store.Pool().Exec(ctx, block, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("run the way back %s carries: %v", stepVariantWayBackExtract, err)
		}
	}

	var problems []string
	for i, q := range queued {
		var row record.OutboxRow
		var kind string
		var attempts int
		var nextAt time.Time
		var lastError string
		err := store.Pool().QueryRow(ctx, `select id, kind, issue, payload, attempts, next_at, last_error from outbox where id = $1`,
			ids[i]).Scan(&row.ID, &kind, &row.Issue, &row.Payload, &attempts, &nextAt, &lastError)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s (row %d) is gone after the way back: %v", q.name, ids[i], err))
			continue
		}
		row.Kind = record.OutboxKind(kind)
		want := q.payload
		if write, isStep := want.(record.StepRecord); isStep {
			if err := decodedBeforeTheVariant(row.Payload); err != nil {
				problems = append(problems, fmt.Sprintf("the release before refuses %s after the way back, and retries it at every attempt: %v", q.name, err))
			}
			write.StepExperiment = nil
			want = write
		}
		decoded, err := record.DecodeOutboxPayload(row)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("this release's decode of %s after the way back: %v", q.name, err))
		case !reflect.DeepEqual(decoded, want):
			problems = append(problems, fmt.Sprintf("%s reads %#v after the way back, want %#v: the row as it was queued, its variant taken out", q.name, decoded, want))
		}
		if q.waiting && (attempts != 3 || !nextAt.Equal(retryAt) || lastError != retryError) {
			problems = append(problems, fmt.Sprintf("%s waits to retry after the way back with %d attempts, at %s, after %q; want its 3, %s and %q as they were",
				q.name, attempts, nextAt.UTC(), lastError, retryAt, retryError))
		}
	}
	return problems
}

// The way back of the release that names the variant on each step record takes the experiment and
// the variant out of each queued step_record row, and nothing else: every row stays, with its
// retry state, each step record then takes the strict decode of the release before, the release
// that rates each plan, and this release reads each row as it was queued but for its variant. The
// block is the guide's, as the extract beside this file copies it.
//
// The control comes first: the release before refuses each queued row that names the variant and
// takes the one that names none, so this test sees the rows the way back exists for. Then the
// block runs again once for each of its statements, with that statement taken out, and each such
// run must leave a row short: a statement this test could drop without failing is one it does not
// check.
//
// What this cannot show is the earlier daemon running: its binary is not here, so its decode is the
// copy above (stepRecordBeforeTheVariant).
func TestTheStepVariantWayBackLeavesWhatTheReleaseBeforeReads(t *testing.T) {
	for _, q := range stepVariantWayBackRows() {
		write, isStep := q.payload.(record.StepRecord)
		if !isStep {
			continue
		}
		row, err := record.NewOutboxRow("LEGION-311", q.payload, time.Now())
		if err != nil {
			t.Fatalf("queue %s: %v", q.name, err)
		}
		if err := decodedBeforeTheVariant(row.Payload); (write.StepExperiment != nil) != (err != nil) {
			t.Fatalf("the release before's decode of %s = %v; the control wants it to refuse a row naming the variant and "+
				"take one naming none, or this test cannot see the rows the way back exists for", q.name, err)
		}
	}

	block := stepVariantWayBack(t)
	if problems := stepVariantWayBackProblems(t, block); len(problems) > 0 {
		t.Fatalf("after the way back:\n%s", strings.Join(problems, "\n"))
	}
	statements := statementsOf(block)
	if len(statements) == 0 {
		t.Fatalf("%s carries no statement ending in `;`", stepVariantWayBackExtract)
	}
	for i, statement := range statements {
		without := strings.Join(slices.Delete(slices.Clone(statements), i, i+1), "\n")
		if problems := stepVariantWayBackProblems(t, without); len(problems) == 0 {
			t.Errorf("the way back without its statement %d leaves the rows as the whole block does, so this test does not check it:\n%s",
				i+1, statement)
		}
	}
}
