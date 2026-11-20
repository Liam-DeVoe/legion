package api

// going_plan_run.go runs a command-bar run's approved plan (R4). The agent runs the rows of its
// run's plan that the person approved, or that Auto-review let through, in order, each as a route
// call that names its row (agentwire.CallRequest.Row), through the same in-process path and the
// same landings as every call of the run (going_agent_call.go), as the person the run's grant
// resolves to at that call. The server holds each such call to its row (rowGate): a write that
// names no row, or a row with another tool or other arguments, or a row that is not approved, is
// refused WRITE_NOT_APPROVED, so a run writes nothing outside its approved rows. Before a row's
// first write its item is read again as the person: a row whose item changed after its approval -
// another value, or another holder of its issue's claim - waits for the person again, ROW_WAITS,
// and a row whose needed row failed or was skipped is skipped, ROW_SKIPPED; the rows that need
// neither run on. A row's call key is kept on the row, so a second try of that key - the agent
// died after the call, or the server between the write's commit and its answer - answers what the
// first landed and sends nothing twice, and no other key runs the row.
//
// Auto-review (going_modes.go) is a review call of the agent's (agentwire.KindReview), once its
// model call named the rows the person's words ask for: the server lets through those its mode
// policy lets through and answers the rows to run.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/going/agentwire"
)

// goingRowNotApproved is the refusal of a write that names no approved row of its run's plan.
func goingRowNotApproved(tool, why string) *agentwire.Error {
	return goingAgentRefusal(agentwire.CodeWriteNotApproved,
		"%s was not sent: %s; a change runs only as a row of a plan the person approved, which propose_plan proposes", tool, why)
}

// rowGate holds a route call of a write tool to the row of its run's latest plan it names, and
// answers nil where the call may send: an approved row whose item reads as it did at its approval
// and whose needed rows ran, which it marks running under the call's key; or a row that key already
// ran, whose second try answers what it landed. A row the key marked waiting or skipped is answered
// that refusal again. doer reads the row's item as the run's person (login).
func (a *AgentListener) rowGate(ctx context.Context, request agentwire.CallRequest, login string, doer GoingDoer) *agentwire.Error {
	if request.Row == nil {
		return goingRowNotApproved(request.Tool, "call_tool runs no tool that changes something")
	}
	plan, err := readGoingPlan(ctx, a.s.deps.Store.Pool, request.Run, 0)
	if err != nil {
		return goingAgentFailed(agentwire.CallCall, err)
	}
	if plan == nil || plan.plan != request.Row.Plan {
		return goingRowNotApproved(request.Tool, fmt.Sprintf("plan %d is not this run's latest plan", request.Row.Plan))
	}
	at := slices.IndexFunc(plan.rows, func(row goingPlanRowStored) bool { return row.row == request.Row.Row })
	if at < 0 {
		return goingRowNotApproved(request.Tool, fmt.Sprintf("plan %d holds no row %d", plan.plan, request.Row.Row))
	}
	row := plan.rows[at]
	if row.tool != request.Tool || !goingJSONEqual(row.arguments, request.Arguments) {
		return goingRowNotApproved(request.Tool, fmt.Sprintf("row %d of plan %d is %s with other arguments", row.row, plan.plan, row.tool))
	}
	ours := row.result != nil && row.result.Step == request.Step && row.result.Call == request.Call
	switch row.state {
	case "approved":
		return a.beforeRowWrite(ctx, request, plan, row, login, doer)
	case "running", "done", "failed":
		if !ours {
			return goingRowNotApproved(request.Tool, fmt.Sprintf("row %d of plan %d ran under another call", row.row, plan.plan))
		}
		return nil
	case "waiting", "skipped":
		if ours && row.result.Code != "" {
			return goingAgentRefusal(row.result.Code, "%s", row.result.Message)
		}
	}
	return goingRowNotApproved(request.Tool, fmt.Sprintf("row %d of plan %d is %s", row.row, plan.plan, row.state))
}

// beforeRowWrite is the gate of an approved row's first try: a needed row that failed or was
// skipped skips it, and one that waits makes it wait; its item read again as the person that no
// longer reads as it did at its approval makes it wait again, showing the new read; and otherwise
// it is marked running under the call's key, in a statement that holds the run's lease.
func (a *AgentListener) beforeRowWrite(ctx context.Context, request agentwire.CallRequest, plan *goingPlanStored, row goingPlanRowStored,
	login string, doer GoingDoer,
) *agentwire.Error {
	key := goingRowRecord{Step: request.Step, Call: request.Call}
	for _, need := range row.needs {
		at := slices.IndexFunc(plan.rows, func(stored goingPlanRowStored) bool { return stored.row == need })
		if at < 0 {
			continue
		}
		needed := plan.rows[at]
		switch needed.state {
		case "done":
		case "failed", "skipped":
			why := fmt.Sprintf("row %d was skipped: it needs row %d, which %s", row.row, need, needed.state)
			if needed.result != nil && needed.result.Message != "" {
				why += ": " + needed.result.Message
			}
			key.Code, key.Message = agentwire.CodeRowSkipped, why
			return a.settleRow(ctx, request, row, "skipped", key, nil)
		case "waiting":
			key.Code, key.Message = agentwire.CodeRowWaits, fmt.Sprintf("row %d waits for the person: it needs row %d, which waits for them", row.row, need)
			return a.settleRow(ctx, request, row, "waiting", key, nil)
		default:
			return goingRowNotApproved(request.Tool, fmt.Sprintf("row %d needs row %d, which has not run", row.row, need))
		}
	}
	if row.approval != nil {
		arguments, err := goingRowArguments(row.arguments)
		if err != nil {
			return goingAgentFailed(agentwire.CallCall, err)
		}
		// A read the person may no longer make is the write's own to refuse, as its route does.
		if now, err := a.s.goingReadRow(ctx, doer, login, row.tool, arguments); err == nil && !row.approval.Seen.same(now) {
			now.Changed = true
			key.Code, key.Message = agentwire.CodeRowWaits, fmt.Sprintf(
				"row %d waits for the person's approval again: its item %s changed after they approved it", row.row, now.Item)
			return a.settleRow(ctx, request, row, "waiting", key, &now)
		}
	}
	tag, err := a.s.deps.Store.Pool.Exec(ctx, `
		update going_plan_rows set state = 'running', result = $4::jsonb
		where run_id = $1::uuid and plan = $2 and row = $3 and state = 'approved'
		  and exists (select 1 from going_runs where id = $1::uuid and lease_token = $5::uuid and state = 'working')
	`, request.Run, plan.plan, row.row, goingRecordJSON(key), request.LeaseToken)
	if err != nil {
		return goingAgentFailed(agentwire.CallCall, err)
	}
	if tag.RowsAffected() == 0 {
		if _, stale := a.callRun(ctx, request); stale != nil {
			return stale
		}
		return goingAgentRefusal(agentwire.CodeCallUnknown, "row %d of plan %d was taken by another call meanwhile", row.row, plan.plan)
	}
	return a.refreshPlanState(ctx, request.Run, plan.plan)
}

// settleRow sets row of request's plan to state, waiting or skipped, with record and, for a row
// that waits again, what it shows now, under the run's lease, and answers the record's refusal.
func (a *AgentListener) settleRow(ctx context.Context, request agentwire.CallRequest, row goingPlanRowStored, state string,
	record goingRowRecord, shown *goingRowShown,
) *agentwire.Error {
	var shownJSON *string
	if shown != nil {
		encoded, err := json.Marshal(shown)
		if err != nil {
			return goingAgentFailed(agentwire.CallCall, err)
		}
		text := string(encoded)
		shownJSON = &text
	}
	tag, err := a.s.deps.Store.Pool.Exec(ctx, `
		update going_plan_rows set state = $4, result = $5::jsonb, shown = coalesce($6::jsonb, shown),
		  approval = case when $4 = 'waiting' then null else approval end
		where run_id = $1::uuid and plan = $2 and row = $3 and state = 'approved'
		  and exists (select 1 from going_runs where id = $1::uuid and lease_token = $7::uuid and state = 'working')
	`, request.Run, request.Row.Plan, row.row, state, goingRecordJSON(record), shownJSON, request.LeaseToken)
	if err != nil {
		return goingAgentFailed(agentwire.CallCall, err)
	}
	if tag.RowsAffected() == 0 {
		if _, stale := a.callRun(ctx, request); stale != nil {
			return stale
		}
	}
	if refusal := a.refreshPlanState(ctx, request.Run, request.Row.Plan); refusal != nil {
		return refusal
	}
	return goingAgentRefusal(record.Code, "%s", record.Message)
}

// goingRecordJSON is record as going_plan_rows.result holds it.
func goingRecordJSON(record goingRowRecord) string {
	encoded, _ := json.Marshal(record)
	return string(encoded)
}

// refreshPlanState sets plan's state from its rows (goingPlanStateSQL).
func (a *AgentListener) refreshPlanState(ctx context.Context, run string, plan int) *agentwire.Error {
	if _, err := a.s.deps.Store.Pool.Exec(ctx, goingPlanStateSQL, run, plan); err != nil {
		return goingAgentFailed(agentwire.CallCall, err)
	}
	return nil
}

// recordRow keeps what a row's call came to on its row, under the call's key: done where its
// route answered below 300, and failed where it answered a refusal, or where the tool's schema or
// translation refused it before anything was sent. A call that failed in the server, or whose
// lease is not the run's, keeps nothing: its next try runs the row on under the same key. The
// record keeps how the row's upload was sent (goingRowUpload), which a second try of the key sends
// again. A row done carries what its write left to the later rows of its item (carryRowWrite).
func (a *AgentListener) recordRow(ctx context.Context, request agentwire.CallRequest, answer agentwire.CallAnswer, refusal *agentwire.Error) {
	record := goingRowRecord{Step: request.Step, Call: request.Call}
	state := "done"
	switch {
	case refusal != nil && refusal.Code != agentwire.CodeCallRefused:
		return
	case refusal != nil:
		state, record.Code, record.Message = "failed", refusal.Code, refusal.Message
	case answer.Status >= 300:
		state, record.Status = "failed", answer.Status
		var refused errorBody
		if json.Unmarshal(answer.Body, &refused) == nil {
			record.Code, record.Message = refused.Code, refused.Error
		}
	default:
		record.Status = answer.Status
	}
	if _, err := a.s.deps.Store.Pool.Exec(ctx, `
		update going_plan_rows set state = $4, result = $5::jsonb || jsonb_strip_nulls(jsonb_build_object('upload', result->'upload'))
		where run_id = $1::uuid and plan = $2 and row = $3 and state in ('running', 'done', 'failed')
		  and (result->>'step')::int = $6 and (result->>'call')::int = $7
	`, request.Run, request.Row.Plan, request.Row.Row, state, goingRecordJSON(record), request.Step, request.Call); err != nil {
		slog.Error("dispatch: record what a row of a command-bar plan came to", "run", request.Run, "plan", request.Row.Plan,
			"row", request.Row.Row, "error", err)
		return
	}
	if state == "done" {
		a.carryRowWrite(ctx, request, answer)
	}
	_ = a.refreshPlanState(ctx, request.Run, request.Row.Plan)
}

// rowUpload is how the upload of request's row is sent (goingRowUpload): as a try of its call's
// key decided it before, or, on that key's first try to reach it, as the upload's read before now
// finds its name - over the document of that name its owner holds, naming the token of the
// version it replaces (goingObserver.uploadBase), or as a create alone - kept on the row under the
// call's key before anything of it is sent. So a second try of the key sends the request the first
// sent, and answers what that one landed. A document another writer changed after the row's
// approval made the row wait again at its gate (beforeRowWrite); one changed between that gate and
// this read lands over the version read here, and one changed after it is refused by the upload's
// route, 409 PRECONDITION_FAILED.
func (a *AgentListener) rowUpload(ctx context.Context, request agentwire.CallRequest, prepared goingPrepared, read goingReader) (goingRowUpload, *agentwire.Error) {
	var decided *goingRowUpload
	if err := a.s.deps.Store.Pool.QueryRow(ctx, `
		select result->'upload' from going_plan_rows
		where run_id = $1::uuid and plan = $2 and row = $3 and (result->>'step')::int = $4 and (result->>'call')::int = $5
	`, request.Run, request.Row.Plan, request.Row.Row, request.Step, request.Call).Scan(&decided); err != nil {
		return goingRowUpload{}, goingAgentFailed(agentwire.CallCall, fmt.Errorf("read how the row's upload is sent: %w", err))
	}
	if decided != nil {
		return *decided, nil
	}
	observer := goingObserver{read: read, s: a.s}
	before, err := observer.observe(ctx, prepared, nil, 0, goingObservation{})
	if err != nil {
		return goingRowUpload{}, goingCallRefusal(request.Tool, err)
	}
	upload := goingRowUpload{Over: before.held != nil}
	if upload.Over {
		if upload.Base, err = observer.uploadBase(ctx, before.held); err != nil {
			return goingRowUpload{}, goingCallRefusal(request.Tool, err)
		}
	}
	encoded, err := json.Marshal(upload)
	if err != nil {
		return goingRowUpload{}, goingAgentFailed(agentwire.CallCall, err)
	}
	tag, err := a.s.deps.Store.Pool.Exec(ctx, `
		update going_plan_rows set result = jsonb_set(result, '{upload}', $6::jsonb)
		where run_id = $1::uuid and plan = $2 and row = $3 and state = 'running'
		  and (result->>'step')::int = $4 and (result->>'call')::int = $5
		  and exists (select 1 from going_runs where id = $1::uuid and lease_token = $7::uuid and state = 'working')
	`, request.Run, request.Row.Plan, request.Row.Row, request.Step, request.Call, string(encoded), request.LeaseToken)
	if err != nil {
		return goingRowUpload{}, goingAgentFailed(agentwire.CallCall, err)
	}
	if tag.RowsAffected() == 0 {
		if _, stale := a.callRun(ctx, request); stale != nil {
			return goingRowUpload{}, stale
		}
		return goingRowUpload{}, goingAgentRefusal(agentwire.CodeCallUnknown, "row %d of plan %d was taken by another call meanwhile", request.Row.Row, request.Row.Plan)
	}
	return upload, nil
}

// carryRowWrite moves each later approved row of request's plan on the item, or the issue, that the
// done row of request wrote, to what that write left of it as the write's own answer says (the
// route answers the item as its transaction left it, which goingObserver reads after a write): the
// change an earlier row of the plan makes is the plan's own and makes no later row wait again,
// while a change another writer made after it still does. Nothing is read again: a later row whose
// values or claim the answer does not say keeps the read it was approved over, so its gate waits
// it again where the earlier write changed them.
func (a *AgentListener) carryRowWrite(ctx context.Context, request agentwire.CallRequest, answer agentwire.CallAnswer) {
	plan, err := readGoingPlan(ctx, a.s.deps.Store.Pool, request.Run, request.Row.Plan)
	if err != nil || plan == nil {
		slog.Error("dispatch: read a command-bar plan after a row ran", "run", request.Run, "plan", request.Row.Plan, "error", err)
		return
	}
	at := slices.IndexFunc(plan.rows, func(row goingPlanRowStored) bool { return row.row == request.Row.Row })
	if at < 0 || plan.rows[at].approval == nil {
		return
	}
	done := plan.rows[at]
	after, read := a.s.goingRowAfter(ctx, done, answer)
	if !read {
		return
	}
	wrote := done.approval.Seen
	for _, row := range plan.rows[at+1:] {
		if row.state != "approved" || row.approval == nil {
			continue
		}
		seen := row.approval.Seen
		if (wrote.Item == "" || seen.Item != wrote.Item) && (wrote.Issue == "" || seen.Issue != wrote.Issue) {
			continue
		}
		carried, moved := goingCarryWrite(seen, after)
		if !moved {
			continue
		}
		approval := *row.approval
		approval.Seen = carried
		encoded, err := json.Marshal(approval)
		if err != nil {
			continue
		}
		if _, err := a.s.deps.Store.Pool.Exec(ctx, `
			update going_plan_rows set approval = $4::jsonb where run_id = $1::uuid and plan = $2 and row = $3 and state = 'approved'
		`, request.Run, plan.plan, row.row, string(encoded)); err != nil {
			slog.Error("dispatch: carry a row's write to the later rows of its command-bar plan", "run", request.Run, "plan", plan.plan,
				"row", row.row, "error", err)
		}
	}
}

// goingNoReads is the Doer of the read after a row's write that carryRowWrite makes: it refuses
// every request, so what a write left is taken from its route's answer alone.
type goingNoReads struct{}

var errGoingNoReads = errors.New("what a row's write left is read from its answer alone")

func (goingNoReads) Do(*http.Request) (*http.Response, error) { return nil, errGoingNoReads }

// goingRowAfter is what the done row's write left of its item, as its route's answer says
// (goingObserver's read after a write, made without a request), and whether the answer said it:
// an answer that was cut, that does not read, or whose tool reads what it left with a request
// says nothing.
func (s *server) goingRowAfter(ctx context.Context, done goingPlanRowStored, answer agentwire.CallAnswer) (goingObservation, bool) {
	if answer.Cut {
		return goingObservation{}, false
	}
	arguments, err := goingRowArguments(done.arguments)
	if err != nil {
		return goingObservation{}, false
	}
	seen := done.approval.Seen
	before := goingObservation{target: seen.Item, fields: seen.Fields, issue: seen.Issue}
	if len(seen.Now) > 0 {
		if err := json.Unmarshal(seen.Now, &before.value); err != nil {
			return goingObservation{}, false
		}
	}
	observer := goingObserver{read: goingReader{doer: goingNoReads{}}, s: s,
		landed: func(context.Context) (bool, error) { return false, errGoingNoReads }}
	after, err := observer.observe(ctx, goingPrepared{action: done.tool, arguments: arguments}, answer.Body, answer.Status, before)
	if err != nil || after.beforeUnread {
		return goingObservation{}, false
	}
	return after, true
}

// goingCarryWrite is seen, a later row's read of the item an earlier row wrote, with what that
// write left (after) in place of the values it read: each of its fields the write's answer says,
// and the claim a claim's or a release's answer says. moved reports whether anything was put in.
func goingCarryWrite(seen goingRowShown, after goingObservation) (goingRowShown, bool) {
	moved := false
	if left, fields := after.value.(map[string]any); fields {
		var now map[string]any
		if json.Unmarshal(seen.Now, &now) == nil && now != nil {
			for _, field := range seen.Fields {
				if value, said := left[field]; said {
					now[field], moved = value, true
				}
			}
			if moved {
				if encoded, err := json.Marshal(now); err == nil {
					seen.Now = encoded
				} else {
					moved = false
				}
			}
		}
	}
	if slices.Equal(after.fields, goingFields(goingWriteFields["dispatch_claim"])) && !after.unchanged {
		seen.Claim, moved = nil, true
		if holder, held := after.value.(*goingHolder); held && holder != nil {
			copied := *holder
			seen.Claim = &copied
		}
		if slices.Equal(seen.Fields, after.fields) {
			seen.Now = goingJSONOrNull(seen.Claim)
		}
	}
	return seen, moved
}

// reviewCall is a review call (agentwire.KindReview) of a run in Auto-review: Auto-review's
// model call named the rows of the run's latest plan that the person's words ask for (Asked), and
// the server approves each of them that Auto-review may let through (goingAutoReviewLets) whose
// needed rows it approves too, once for the plan, in one transaction that holds the run's lease.
// It answers the rows to run, the same a claim of the run would (goingPendingRows), and how many
// rows wait for the person. A second review of the plan approves nothing more.
func (a *AgentListener) reviewCall(ctx context.Context, request agentwire.CallRequest, run goingCallRun) (agentwire.CallAnswer, *agentwire.Error) {
	var mode string
	if err := a.s.deps.Store.Pool.QueryRow(ctx, `select coalesce(mode, '') from going_runs where id = $1::uuid`, run.id).Scan(&mode); err != nil {
		return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
	}
	if mode != goingModeAutoReview {
		return agentwire.CallAnswer{}, goingAgentRefusal(agentwire.CodeCallRefused, "this run's mode is %s, which no review lets a change through in", mode)
	}
	tx, err := a.s.deps.Store.Pool.Begin(ctx)
	if err != nil {
		return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if held, err := goingLeaseHeld(ctx, tx, run.id, request.LeaseToken); err != nil {
		return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
	} else if !held {
		return agentwire.CallAnswer{}, goingStaleLease(run.id)
	}
	var plan int
	var reviewed bool
	err = tx.QueryRow(ctx, `
		select plan, approved_by is not null from going_plans where run_id = $1::uuid order by plan desc limit 1 for update
	`, run.id).Scan(&plan, &reviewed)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentwire.CallAnswer{}, goingAgentRefusal(agentwire.CodeCallRefused, "this run has proposed no plan to review")
	}
	if err != nil {
		return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
	}
	if !reviewed {
		stored, err := readGoingPlan(ctx, tx, run.id, plan)
		if err != nil {
			return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
		}
		lets := map[int]bool{}
		for _, row := range stored.rows {
			if row.state != "waiting" || !slices.Contains(request.Asked, row.row) || !goingAutoReviewLets(row.tool, row.shown.Waits) {
				continue
			}
			if slices.ContainsFunc(row.needs, func(need int) bool { return !lets[need] }) {
				continue
			}
			lets[row.row] = true
			approval, err := json.Marshal(goingRowApproval{By: goingApprovedByAutoReview, Seen: row.shown})
			if err != nil {
				return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
			}
			if _, err := tx.Exec(ctx, `
				update going_plan_rows set state = 'approved', approval = $4::jsonb where run_id = $1::uuid and plan = $2 and row = $3
			`, run.id, plan, row.row, string(approval)); err != nil {
				return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
			}
		}
		if _, err := tx.Exec(ctx, `
			update going_plans set approved_at = now(), approved_by = '{"by": "auto_review"}' where run_id = $1::uuid and plan = $2
		`, run.id, plan); err != nil {
			return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
		}
		if _, err := tx.Exec(ctx, goingPlanStateSQL, run.id, plan); err != nil {
			return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
		}
	}
	review := agentwire.ReviewAnswer{}
	if review.Run, err = goingPendingRows(ctx, tx, run.id); err != nil {
		return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
	}
	if err := tx.QueryRow(ctx, `
		select count(*) from going_plan_rows where run_id = $1::uuid and plan = $2 and state = 'waiting'
	`, run.id, plan).Scan(&review.Waiting); err != nil {
		return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
	}
	var state string
	if err := tx.QueryRow(ctx, `select state from going_plans where run_id = $1::uuid and plan = $2`, run.id, plan).Scan(&state); err != nil {
		return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
	}
	if !reviewed {
		if err := appendGoingPlanFrame(ctx, tx, run.id, plan, state); err != nil {
			return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
	}
	if review.Run == nil {
		review.Run = []agentwire.PlanRun{}
	}
	body, err := json.Marshal(review)
	if err != nil {
		return agentwire.CallAnswer{}, goingAgentFailed(agentwire.CallCall, err)
	}
	return agentwire.CallAnswer{Status: 200, Body: body, ContentType: "application/json"}, nil
}

// goingRowsWaitSQL reports whether run $1's latest plan, unless rejected, holds a row that waits
// for the person: a run whose turn ends with one waits for them, whatever its agent ended it as.
const goingRowsWaitSQL = `
	select exists (
	  select 1 from going_plan_rows r join going_plans p on p.run_id = r.run_id and p.plan = r.plan
	  where r.run_id = $1::uuid and p.state <> 'rejected' and r.state = 'waiting'
	    and r.plan = (select max(plan) from going_plans where run_id = $1::uuid)
	)
`
