package daemon

import (
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// planRecordID is the id of the plan every plan record here writes, a version 5 id of the form the
// workflow names a plan by.
const planRecordID = "5d6e7f80-91a2-5b3c-8d4e-5f6a7b8c9d0e"

// planRefusedLine is the one line that says a plan record ended unwritten.
const planRefusedLine = "outbox plan record refused by Dispatch and dropped"

// planPut is one plan write as Dispatch received it.
type planPut struct {
	Issue string
	ID    string
	Plan  dispatch.PlanWrite
}

// planRecordOf is the default plan of the run 2 of the issue.
func planRecordOf() record.PlanRecord {
	return record.PlanRecord{ID: planRecordID, Generation: 2, Source: "default",
		Tasks: []record.PlanRecordTask{{ID: "default", Steps: []string{"planner", "implementer", "tester", "reviewer", "acceptance:owner"}}}}
}

// ratingRecordOf is the write that rates the plan on the record of the issue's retro.
func ratingRecordOf() record.StepRecord {
	return record.StepRecord{ID: stepRecordID, Task: "default", Step: 0, Kind: "retro", Role: "implementer",
		Agent: "legion-legion-legion-1-implementer", Generation: 2,
		PlanRating: &record.PlanRating{Rating: 2, Reason: "The plan put the tests after the review."}}
}

// A plan record is written once, under the row's issue and the plan's own id, with the run, the
// source, the owner's document and its version where the plan has them, and every task with its
// steps in order, and the row is done. The owner's plan carries every field a default plan leaves
// empty, a version that is not its run, and two tasks out of the order of their ids, so a field the
// copy to Dispatch drops, or a sort of the tasks, changes what Dispatch receives.
func TestAPlanRecordIsWrittenUnderItsIssueAndItsID(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan record.PlanRecord
		want dispatch.PlanWrite
	}{
		{name: "the default plan", plan: planRecordOf(), want: dispatch.PlanWrite{Generation: 2, Source: "default",
			Tasks: []dispatch.PlanTask{{ID: "default", Steps: []string{"planner", "implementer", "tester", "reviewer", "acceptance:owner"}}}}},
		{
			name: "an owner's plan of two tasks",
			plan: record.PlanRecord{ID: planRecordID, Generation: 3, Source: "owner", PlanDocument: &record.PlanDocument{ArtifactID: "6f1d2c3b-4a5e-4f60-8a7b-9c0d1e2f3a4b", Version: 4},
				Tasks: []record.PlanRecordTask{{ID: "ui", Steps: []string{"tester", "implementer:ui"}}, {ID: "api", Steps: []string{"reviewer", "implementer"}}}},
			want: dispatch.PlanWrite{Generation: 3, Source: "owner", PlanDocument: &dispatch.PlanDocument{ArtifactID: "6f1d2c3b-4a5e-4f60-8a7b-9c0d1e2f3a4b", Version: 4},
				Tasks: []dispatch.PlanTask{{ID: "ui", Steps: []string{"tester", "implementer:ui"}}, {ID: "api", Steps: []string{"reviewer", "implementer"}}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newStepRecordRigOf(t, tc.plan, nil)
			rig.run()
			if left := outboxRowsOf(t, rig.pool, stepIssue); left != 0 {
				t.Fatalf("rows left after Dispatch took the plan = %d, want none", left)
			}
			plans, steps := rig.client.receivedPlans(), rig.client.received()
			want := planPut{Issue: stepIssue, ID: planRecordID, Plan: tc.want}
			if len(plans) != 1 || len(steps) != 0 || !reflect.DeepEqual(plans[0], want) {
				t.Fatalf("Dispatch received the plans %+v and the step writes %+v, want the one plan %+v", plans, steps, want)
			}
		})
	}
}

// The rating a retro gave the plan reaches Dispatch as a write of the retro's record carrying the
// rating, its reason and the run's identity, which the client sends to the record's rating route
// (dispatch.HTTPClient.PutStep).
func TestTheRetrosRatingReachesDispatch(t *testing.T) {
	rig := newStepRecordRigOf(t, ratingRecordOf(), nil)
	rig.run()
	if left := outboxRowsOf(t, rig.pool, stepIssue); left != 0 {
		t.Fatalf("rows left after Dispatch took the rating = %d, want none", left)
	}
	steps := rig.client.received()
	want := stepPut{Issue: stepIssue, ID: stepRecordID, Write: dispatch.StepWrite{Task: "default", Step: 0, Kind: "retro",
		Role: "implementer", Agent: "legion-legion-legion-1-implementer", Generation: 2,
		PlanRating: &dispatch.PlanRating{Rating: 2, Reason: "The plan put the tests after the review."}}}
	if len(steps) != 1 || !reflect.DeepEqual(steps[0], want) {
		t.Fatalf("Dispatch received %+v, want the one rating %+v", steps, want)
	}
}

// A Dispatch older than the plan route or the rating route answers its router's 404 under the
// code NOT_FOUND, "no route for …", which is no refusal of the write: the row waits rather than
// fails, as every row does whose write meets that answer (the outbox's execute), until the upgraded
// Dispatch takes it, which is what lets the daemon and Dispatch deploy in either order. Each run
// keeps the row and logs `outbox row waits`, at INFO on its first run and DEBUG on the next, and no
// ERROR line, and nothing says the write was dropped. 404 ISSUE_NOT_FOUND, Dispatch holding no such
// issue, ends the row instead (TestAPlanRecordThatDispatchRefusesEndsWithAWarning, and
// TestAStepRecordThatDispatchRefusesEndsWithAWarning for the step record a rating is), and so does
// a 400 INVALID_JSON naming the rating (TestARatingThatDispatchRefusesByNameEndsWithAWarning).
func TestAPlanAndARatingWaitForADispatchThatHasNoRouteForThem(t *testing.T) {
	notFound := &dispatch.Error{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "no route for PUT /api/v1/issues/LEGION-1/…"}
	for _, route := range []struct {
		name    string
		payload record.OutboxPayload
		dropped string
	}{
		{name: "the plan route", payload: planRecordOf(), dropped: planRefusedLine},
		{name: "the rating route", payload: ratingRecordOf(), dropped: stepRefusedLine},
	} {
		t.Run(route.name, func(t *testing.T) {
			rig := newStepRecordRigOf(t, route.payload, answersInTurn(notFound, notFound))
			rig.runner.log = slog.New(slog.NewJSONHandler(rig.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			for attempt := 1; attempt <= 2; attempt++ {
				rig.run()
				if left := outboxRowsOf(t, rig.pool, stepIssue); left != 1 {
					t.Fatalf("rows left after attempt %d = %d, want the write kept for its next run", attempt, left)
				}
			}
			rig.run()
			if left := outboxRowsOf(t, rig.pool, stepIssue); left != 0 {
				t.Fatalf("rows left after Dispatch took the write = %d, want none", left)
			}
			plans, steps := rig.client.receivedPlans(), rig.client.received()
			if len(plans)+len(steps) != 3 {
				t.Fatalf("PUTs = %d plans and %d step writes, want three of one: two the old router refused, one stored", len(plans), len(steps))
			}
			logs := rig.logs.String()
			if errored := loggedLines(t, logs, `"level":"ERROR"`); len(errored) != 0 {
				t.Errorf("logged %d ERROR lines while Dispatch had no route, want none: %s", len(errored), logs)
			}
			if dropped := loggedLines(t, logs, route.dropped); len(dropped) != 0 {
				t.Errorf("logged the write dropped %d times, want never: %s", len(dropped), logs)
			}
			var levels []string
			for _, line := range loggedLines(t, logs, `"msg":"outbox row waits"`) {
				level, _ := line["level"].(string)
				levels = append(levels, level)
			}
			if want := []string{"INFO", "DEBUG"}; !slices.Equal(levels, want) {
				t.Fatalf("the waits logged at %v, want %v:\n%s", levels, want, logs)
			}
		})
	}
}

// A 400 INVALID_JSON naming the rating ends the rating's row after its one run, as every refusal no
// retry changes ends a step record's (TestAStepRecordThatDispatchRefusesEndsWithAWarning): a
// Dispatch ignores a body member it does not declare, so no Dispatch refuses the rating by name,
// and an answer that did would come again at every retry and hold the record lane behind it
// (record.LaneRecord). The one Warn line names the issue, the record's id and Dispatch's code, and
// the row's failure is not logged as a failed attempt besides.
func TestARatingThatDispatchRefusesByNameEndsWithAWarning(t *testing.T) {
	refusal := &dispatch.Error{Status: http.StatusBadRequest, Code: "INVALID_JSON", Message: `invalid JSON body: json: unknown field "rating"`}
	rig := newStepRecordRigOf(t, ratingRecordOf(), answersInTurn(refusal))
	rig.run()
	if left := rig.left(); left != 0 {
		t.Fatalf("rows left after Dispatch refused the rating = %d, want none", left)
	}
	if puts := rig.client.received(); len(puts) != 1 {
		t.Fatalf("PUTs of the rating = %d, want the one Dispatch refused", len(puts))
	}
	logs := rig.logs.String()
	warned := loggedLines(t, logs, `"level":"WARN"`)
	if len(warned) != 1 || warned[0]["msg"] != stepRefusedLine {
		t.Fatalf("warnings = %v, want the one %q: %s", warned, stepRefusedLine, logs)
	}
	for key, want := range map[string]any{"issue": stepIssue, "step": stepRecordID, "code": refusal.Code, "status": float64(refusal.Status)} {
		if warned[0][key] != want {
			t.Errorf("%q names %s %v, want %v", stepRefusedLine, key, warned[0][key], want)
		}
	}
	if failed := loggedLines(t, logs, "outbox row failed"); len(failed) != 0 {
		t.Fatalf("logged %d failed attempts, want none for a refusal no retry changes: %s", len(failed), logs)
	}
}

// What no retry changes ends a plan record after its one run, and the one Warn line says the plan
// was dropped, naming the issue, the plan's id and Dispatch's code, since nothing else would: an
// issue Dispatch does not have, a plan Dispatch will never take, a writer the route refuses, and an
// id of another issue's plan. The row's failure is not logged as a failed attempt besides.
func TestAPlanRecordThatDispatchRefusesEndsWithAWarning(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *dispatch.Error
	}{
		{name: "404 ISSUE_NOT_FOUND", err: &dispatch.Error{Status: http.StatusNotFound, Code: dispatch.IssueNotFound, Message: "no issue LEGION-1"}},
		{name: "400 PLAN_INPUT", err: &dispatch.Error{Status: http.StatusBadRequest, Code: dispatch.PlanInput, Message: "source must be one of default owner"}},
		{name: "400 INVALID_JSON", err: &dispatch.Error{Status: http.StatusBadRequest, Code: "INVALID_JSON", Message: "invalid JSON body: unexpected EOF"}},
		{name: "403 STEP_WRITER", err: &dispatch.Error{Status: http.StatusForbidden, Code: dispatch.StepWriter, Message: "only the session legion-daemon:LEGION writes a plan of LEGION-1"}},
		{name: "409 PLAN_OTHER_ISSUE", err: &dispatch.Error{Status: http.StatusConflict, Code: dispatch.PlanOtherIssue, Message: "the plan belongs to LEGION-9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newStepRecordRigOf(t, planRecordOf(), answersInTurn(tc.err))
			rig.run()
			if left := outboxRowsOf(t, rig.pool, stepIssue); left != 0 {
				t.Fatalf("rows left after Dispatch refused the plan = %d, want none", left)
			}
			if plans := rig.client.receivedPlans(); len(plans) != 1 {
				t.Fatalf("PUTs of the plan = %d, want the one Dispatch refused", len(plans))
			}
			logs := rig.logs.String()
			warned := loggedLines(t, logs, `"level":"WARN"`)
			if len(warned) != 1 || warned[0]["msg"] != planRefusedLine {
				t.Fatalf("warnings = %v, want the one %q: %s", warned, planRefusedLine, logs)
			}
			for key, want := range map[string]any{"issue": stepIssue, "plan": planRecordID, "code": tc.err.Code,
				"status": float64(tc.err.Status)} {
				if warned[0][key] != want {
					t.Errorf("%q names %s %v, want %v", planRefusedLine, key, warned[0][key], want)
				}
			}
			if failed := loggedLines(t, logs, "outbox row failed"); len(failed) != 0 {
				t.Fatalf("logged %d failed attempts, want none for a refusal no retry changes: %s", len(failed), logs)
			}
		})
	}
}
