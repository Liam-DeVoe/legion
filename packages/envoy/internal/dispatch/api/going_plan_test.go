package api

// going_plan_test.go holds a run's plan as its person meets it: the agent's real loop, on a fake
// chat model, proposes a plan; the person reads and answers it through the plan routes; and the
// agent runs the approved rows through the server (going_plan.go, going_plan_run.go).

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/going/agent/agenttest"
)

// goingPlanRig is the agent rig with the agent's loop on a fake chat model, whose people are named
// by the header identity, which a plan's approval records as the run's grant.
type goingPlanRig struct {
	goingAgentRig
	loop *goingLoop
}

func newGoingPlanRig(t *testing.T) goingPlanRig {
	t.Helper()
	rig := newGoingAgentRig(t, nil)
	rig.served.deps.Identity = headerIdentity(rig.database)
	return goingPlanRig{goingAgentRig: rig, loop: newGoingLoop(t, rig)}
}

// goingChange is one change of a proposed plan: tool with arguments, needing the changes needs. A
// change that needs none leaves needs out, as propose_plan's schema takes no null for it.
func goingChange(tool string, arguments map[string]any, needs ...int) map[string]any {
	change := map[string]any{"tool": tool, "arguments": arguments}
	if len(needs) > 0 {
		change["needs"] = needs
	}
	return change
}

// proposing is the reply that proposes changes as a plan.
func proposing(changes ...map[string]any) agenttest.Reply {
	return toolCall("propose_plan", map[string]any{"summary": "The changes the words ask for.", "changes": changes})
}

// plan is run's latest plan as alice reads it.
func (rig goingPlanRig) plan(t *testing.T, run string) goingPlanAnswer {
	t.Helper()
	response := dispatchRequest(t, rig.mux, http.MethodGet, "/api/v1/going/runs/"+run+"/plan", nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("GET the plan of %s answered %d %s", run, response.Code, response.Body.String())
	}
	return decodeBody[goingPlanAnswer](t, response)
}

// answer sends alice's answer to run's plan: approve, revise or reject, with body.
func (rig goingPlanRig) answer(t *testing.T, run, verb string, body any) goingPlanAnswer {
	t.Helper()
	response := dispatchRequest(t, rig.mux, http.MethodPost, "/api/v1/going/runs/"+run+"/plan/"+verb, body, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("%s the plan of %s answered %d %s", verb, run, response.Code, response.Body.String())
	}
	return decodeBody[goingPlanAnswer](t, response)
}

// waiting waits for run to end its turn waiting for alice, and answers its plan.
func (rig goingPlanRig) waiting(t *testing.T, run string) goingPlanAnswer {
	t.Helper()
	if record := rig.ended(t, run, time.Minute); record.state != "waiting" {
		t.Fatalf("the run ended %s with %q, want waiting for its person; its calls were told %q", record.state, record.reason, rig.toolResults(t, run))
	}
	return rig.plan(t, run)
}

// title is the title of issue now.
func (rig goingPlanRig) title(t *testing.T, issue string) string {
	t.Helper()
	return rig.issueRead(t, issue).Title
}

// rowStates is the state of each row of plan, in order.
func rowStates(plan goingPlanAnswer) []string {
	states := make([]string, 0, len(plan.Rows))
	for _, row := range plan.Rows {
		states = append(states, row.State)
	}
	return states
}

// retitle is a change of issue's title to title.
func retitle(issue, title string) map[string]any {
	return map[string]any{"issue": issue, "title": title}
}

// TestTheServerRefusesAChangeOutsideTheApprovedPlan (check c5): the model's tools hold no tool
// that changes something; after the person approves a priority change on issue A, the model calls
// call_tool with a write on issue B and with the planned write on A with another value, and the
// server refuses both, each saved as a refused step of the run; A holds the approved priority and
// B none.
func TestTheServerRefusesAChangeOutsideTheApprovedPlan(t *testing.T) {
	rig := newGoingPlanRig(t)
	a, b := rig.issue(t, "Issue A"), rig.issue(t, "Issue B")
	const words = "set the priority of issue A to 1"
	write := func(arguments map[string]any) agenttest.Call {
		return agenttest.Call{Name: "call_tool", Arguments: map[string]any{"tool": "dispatch_issue_update", "arguments": arguments}}
	}
	rig.loop.script.set(words, func(n int) agenttest.Reply {
		switch n {
		case 1:
			return proposing(goingChange("dispatch_issue_update", map[string]any{"issue": a, "priority": 1}))
		case 2:
			return agenttest.Reply{Calls: []agenttest.Call{
				write(map[string]any{"issue": b, "priority": 0}), write(map[string]any{"issue": a, "priority": 3}),
			}}
		}
		return answered("Done.")
	})
	run := rig.begin(t, goingLoopRun{login: "alice", words: words})
	rig.waiting(t, run)
	for _, tool := range goingToolNames(rig.loop.fake.Asking(words)[0]) {
		if _, writes := goingWriteTools[tool]; writes {
			t.Errorf("the model was given %s, a tool that changes something", tool)
		}
	}
	rig.answer(t, run, "approve", map[string]any{"plan": 1})
	if record := rig.ended(t, run, time.Minute); record.state != "done" {
		t.Fatalf("the run ended %s with %q, want done", record.state, record.reason)
	}
	if priority := rig.issueRead(t, a).Priority; priority == nil || *priority != 1 {
		shown := "none"
		if priority != nil {
			shown = strconv.Itoa(*priority)
		}
		t.Errorf("A's priority is %s, want the approved 1", shown)
	}
	if priority := rig.issueRead(t, b).Priority; priority != nil {
		t.Errorf("B's priority is %d, want none: no plan names B", *priority)
	}
	if refused := rig.count(t, `
		select count(*) from going_run_steps where run_id = $1::uuid and kind = 'tool' and (body->>'error')::boolean
		  and body->>'result' like '%call_tool does not run%'`, run); refused != 2 {
		t.Errorf("the run saved %d refused writes of call_tool, want both: %q", refused, rig.toolResults(t, run))
	}
}

// goingToolNames is the name of each tool request gives the model.
func goingToolNames(request *agenttest.Request) []string {
	var names []string
	tools, _ := request.Field("tools").([]any)
	for _, tool := range tools {
		if function, ok := tool.(map[string]any)["function"].(map[string]any); ok {
			names = append(names, function["name"].(string))
		}
	}
	return names
}

// TestWhatWaitsInEachMode (check c6): a new person's mode is Ask, and every row of a plan waits.
// In Auto-review, whose review lets every change through, a field change and a create with only a
// title run, and a field change the words did not ask for waits; and these wait: a review of a
// document, an answer to an ask, a kept decision, a check result, a change of a setting of
// Dispatch, a priority change on an issue routed to a session, the close of an issue whose claim
// a session holds, a message to a session that holds no claim on the issue, and a broadcast.
// Before the write of an approved plan a session takes a claim of its item, and that row waits
// again.
func TestWhatWaitsInEachMode(t *testing.T) {
	rig := newGoingPlanRig(t)
	everyRow := func(rows int) []any {
		asked := make([]any, rows)
		for index := range asked {
			asked[index] = index
		}
		return asked
	}

	t.Run("a new person's mode is Ask, and every row waits", func(t *testing.T) {
		settings := decodeBody[goingSettings](t, dispatchRequest(t, rig.mux, http.MethodGet, "/api/v1/me/going/settings", nil, "alice"))
		if settings.Mode != goingModeAsk {
			t.Fatalf("a new person's mode is %q, want ask", settings.Mode)
		}
		issue := rig.issue(t, "Asked of")
		const words = "rename Asked of"
		rig.loop.script.set(words, func(int) agenttest.Reply {
			return proposing(goingChange("dispatch_issue_update", retitle(issue, "Renamed")))
		})
		plan := rig.waiting(t, rig.begin(t, goingLoopRun{login: "alice", words: words, mode: settings.Mode}))
		if states := rowStates(plan); !slices.Equal(states, []string{"waiting"}) || rig.title(t, issue) != "Asked of" {
			t.Fatalf("in Ask the plan's rows are %v and the title %q, want the row waiting and nothing changed", states, rig.title(t, issue))
		}
	})

	t.Run("in Auto-review a field change and a create with only a title run", func(t *testing.T) {
		issue := rig.issue(t, "Reviewed")
		const words = "rename Reviewed and file Made by review"
		rig.loop.script.set(words, func(n int) agenttest.Reply {
			switch n {
			case 1:
				return proposing(goingChange("dispatch_issue_update", retitle(issue, "Renamed by review")),
					goingChange("dispatch_issue", map[string]any{"project": "DEMO", "title": "Made by review", "force": true}))
			case 2:
				return toolCall(toolReviewPlanName, map[string]any{"asked": everyRow(2)})
			}
			return answered("Both are done.")
		})
		run := rig.begin(t, goingLoopRun{login: "alice", words: words, mode: goingModeAutoReview})
		if record := rig.ended(t, run, time.Minute); record.state != "done" {
			t.Fatalf("the run ended %s with %q, want done with both rows run", record.state, record.reason)
		}
		if states := rowStates(rig.plan(t, run)); !slices.Equal(states, []string{"done", "done"}) || rig.title(t, issue) != "Renamed by review" {
			t.Fatalf("the plan's rows are %v and the title %q, want both done", states, rig.title(t, issue))
		}
	})

	t.Run("in Auto-review a field change the words did not ask for waits", func(t *testing.T) {
		asked, unasked := rig.issue(t, "Asked for"), rig.issue(t, "Not asked for")
		const words = "rename Asked for"
		rig.loop.script.set(words, func(n int) agenttest.Reply {
			switch n {
			case 1:
				return proposing(goingChange("dispatch_issue_update", retitle(asked, "Renamed as asked")),
					goingChange("dispatch_issue_update", retitle(unasked, "Renamed unasked")))
			case 2:
				return toolCall(toolReviewPlanName, map[string]any{"asked": []int{0}})
			}
			return answered("One is done, and one waits for you.")
		})
		plan := rig.waiting(t, rig.begin(t, goingLoopRun{login: "alice", words: words, mode: goingModeAutoReview}))
		if states := rowStates(plan); !slices.Equal(states, []string{"done", "waiting"}) || rig.title(t, unasked) != "Not asked for" {
			t.Fatalf("the plan's rows are %v and the unasked title %q, want the asked row done and the other waiting", states, rig.title(t, unasked))
		}
	})

	t.Run("the decisions of the person, a setting and a change delivered to a session wait in Auto-review", func(t *testing.T) {
		fixtures := newGoingRowFixtures(t, rig.goingWritesRig)
		cases := goingRowCases(fixtures)
		routed, claimed := rig.issue(t, "Routed to a session"), rig.issue(t, "Claimed by a session")
		rig.exec(t, `update issues set route = 'session:s-route' where key = $1`, routed)
		rig.exec(t, `update issues set claimed_by = '{"kind": "session", "id": "s-claim"}', claimed_at = now() where key = $1`, claimed)
		rows := []struct {
			change map[string]any
			waits  string
		}{
			{goingChange("dispatch_plan_approve", cases["dispatch_plan_approve POST /api/v1/artifacts/{id}/plan-reviews"].arguments), string(goingClassReview)},
			{goingChange("dispatch_resolve_ask", cases["dispatch_resolve_ask POST /api/v1/asks/{id}/resolve"].arguments), string(goingClassAskAnswer)},
			{goingChange("dispatch_keep_decision", map[string]any{
				"document": fixtures.notesID, "block": "b1", "text_sha256": strings.Repeat("0", 64),
			}), string(goingClassDecision)},
			{goingChange("dispatch_result", cases["dispatch_result POST /api/v1/results/{id}/retract"].arguments), string(goingClassResult)},
			{goingChange("dispatch_architecture_sync", cases["dispatch_architecture_sync POST /api/v1/projects/{key}/architecture-sources/sync"].arguments), string(goingClassSetting)},
			{goingChange("dispatch_issue_update", map[string]any{"issue": routed, "priority": 2}), goingWaitsDelivery},
			{goingChange("dispatch_issue_update", map[string]any{"issue": claimed, "status": "done", "reason": "Finished."}), goingWaitsClaimed},
			{goingChange("dispatch_message", cases["dispatch_message POST /api/v1/issues/{key}/messages"].arguments), string(goingClassMessage)},
			{goingChange("dispatch_broadcast", map[string]any{"session_ids": []string{"s-route"}, "body": "The deploy is paused.", "delivery": "aside"}),
				string(goingClassBroadcast)},
		}
		changes := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			changes = append(changes, row.change)
		}
		const words = "do all of these"
		rig.loop.script.set(words, func(n int) agenttest.Reply {
			if n == 1 {
				return proposing(changes...)
			}
			return toolCall(toolReviewPlanName, map[string]any{"asked": everyRow(len(changes))})
		})
		plan := rig.waiting(t, rig.begin(t, goingLoopRun{login: "alice", words: words, mode: goingModeAutoReview}))
		if len(plan.Rows) != len(rows) {
			t.Fatalf("the plan holds %d rows, want %d", len(plan.Rows), len(rows))
		}
		for index, row := range plan.Rows {
			if row.State != "waiting" || !slices.Contains(row.Waits, rows[index].waits) {
				t.Errorf("row %d, %s, is %s waiting for %v, want it waiting for %s", index, row.Tool, row.State, row.Waits, rows[index].waits)
			}
		}
		if requests := len(rig.loop.fake.Asking(words)); requests != 2 {
			t.Errorf("the run made %d model calls, want the plan's and the review's alone", requests)
		}
	})

	t.Run("a claim a session takes before the write makes the row wait again", func(t *testing.T) {
		issue := rig.issue(t, "Claimed meanwhile")
		const words = "rename Claimed meanwhile"
		rig.loop.script.set(words, func(n int) agenttest.Reply {
			if n == 1 {
				return proposing(goingChange("dispatch_issue_update", retitle(issue, "Renamed meanwhile")))
			}
			return answered("It waits for you again.")
		})
		run := rig.begin(t, goingLoopRun{login: "alice", words: words})
		rig.waiting(t, run)
		rig.loop.stop()
		rig.answer(t, run, "approve", map[string]any{"plan": 1})
		rig.exec(t, `update issues set claimed_by = '{"kind": "session", "id": "s-late"}', claimed_at = now() where key = $1`, issue)
		rig.loop.start(rig.goingAgentRig)
		plan := rig.waiting(t, run)
		if row := plan.Rows[0]; row.State != "waiting" || !row.Changed || rig.title(t, issue) != "Claimed meanwhile" {
			t.Fatalf("the row is %s (changed %v) and the title %q, want it waiting again with nothing written", row.State, row.Changed, rig.title(t, issue))
		}
	})
}

// toolReviewPlanName is the tool the review's model call names its rows with (going/agent).
const toolReviewPlanName = "review_plan"

// TestThePlanRunsInOrder: a plan whose row 2 sets a title that row 3 sets again ends with row 3's
// title, and its rows land in their order.
func TestThePlanRunsInOrder(t *testing.T) {
	rig := newGoingPlanRig(t)
	issue := rig.issue(t, "In order")
	const words = "comment, then rename In order twice"
	rig.loop.script.set(words, func(n int) agenttest.Reply {
		if n == 1 {
			return proposing(goingChange("dispatch_comment", map[string]any{"issue": issue, "body": "First of three."}),
				goingChange("dispatch_issue_update", retitle(issue, "Second")), goingChange("dispatch_issue_update", retitle(issue, "Third"), 1))
		}
		return answered("Done in order.")
	})
	run := rig.begin(t, goingLoopRun{login: "alice", words: words})
	rig.waiting(t, run)
	rig.answer(t, run, "approve", map[string]any{"plan": 1})
	if record := rig.ended(t, run, time.Minute); record.state != "done" {
		t.Fatalf("the run ended %s with %q, want done", record.state, record.reason)
	}
	if title := rig.title(t, issue); title != "Third" {
		t.Errorf("the issue is titled %q, want row 3's Third", title)
	}
	rows, err := rig.database.Pool.Query(context.Background(), `
		select method from going_landings where run_id = $1::uuid order by landed_at, step, request`, run)
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	for rows.Next() {
		var method string
		if err := rows.Scan(&method); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, method)
	}
	if !slices.Equal(methods, []string{http.MethodPost, http.MethodPatch, http.MethodPatch}) {
		t.Errorf("the rows landed as %v, want the comment, then the two renames", methods)
	}
	if steps := rig.count(t, `
		select count(*) from going_run_steps a join going_run_steps b on a.run_id = b.run_id
		where a.run_id = $1::uuid and (a.body->'row'->>'row')::int < (b.body->'row'->>'row')::int and a.step > b.step`, run); steps != 0 {
		t.Errorf("%d pairs of rows ran out of their order", steps)
	}
}

// TestAPlanUploadsANewVersionOfADocument: a row that uploads under the name of a document its issue
// already holds at version 2 shows that document as its item, and once approved lands as the
// document's version 3.
func TestAPlanUploadsANewVersionOfADocument(t *testing.T) {
	rig := newGoingPlanRig(t)
	issue := rig.issue(t, "Holds notes")
	owner := "/api/v1/issues/" + issue
	uploadDocument(t, rig.mux, owner, "notes.md", "# Notes\n\nOne.\n")
	notes := uploadDocument(t, rig.mux, owner, "notes.md", "# Notes\n\nTwo.\n")
	const words = "replace notes.md on Holds notes"
	rig.loop.script.set(words, func(n int) agenttest.Reply {
		if n == 1 {
			return proposing(goingChange("dispatch_artifact", map[string]any{
				"issue": issue, "name": "notes.md", "content": "# Notes\n\nThe plan's.\n", "force": true,
			}))
		}
		return answered("Replaced.")
	})
	run := rig.begin(t, goingLoopRun{login: "alice", words: words})
	if plan := rig.waiting(t, run); len(plan.Rows) != 1 || plan.Rows[0].Item != notes.Artifact.ID {
		t.Fatalf("the plan shows %+v, want its one row on %s, the document the upload writes over", plan.Rows, notes.Artifact.ID)
	}
	rig.answer(t, run, "approve", map[string]any{"plan": 1})
	if record := rig.ended(t, run, time.Minute); record.state != "done" {
		t.Fatalf("the run ended %s with %q, want done", record.state, record.reason)
	}
	if versions := rig.count(t, `select max(number) from artifact_versions where artifact_id = $1::uuid`, notes.Artifact.ID); versions != 3 {
		t.Errorf("%s holds %d versions after the plan ran, want 3, the plan's upload its next version; the row's call was told %q",
			notes.Artifact.ID, versions, rig.toolResults(t, run))
	}
}

// TestAFailedChangeSkipsWhatNeedsIt: a row whose write its route refuses fails, the row that needs
// it is skipped and says so, and a row that needs neither runs.
func TestAFailedChangeSkipsWhatNeedsIt(t *testing.T) {
	rig := newGoingPlanRig(t)
	held, free := rig.issue(t, "Held by bob"), rig.issue(t, "Free")
	rig.fixture(t, "bob", http.MethodPost, "/api/v1/issues/"+held+"/claim", map[string]any{})
	const words = "take Held by bob, rename it, and rename Free"
	rig.loop.script.set(words, func(n int) agenttest.Reply {
		if n == 1 {
			return proposing(goingChange("dispatch_claim", map[string]any{"issue": held}),
				goingChange("dispatch_issue_update", retitle(held, "Renamed held"), 0), goingChange("dispatch_issue_update", retitle(free, "Renamed free")))
		}
		return answered("One failed.")
	})
	run := rig.begin(t, goingLoopRun{login: "alice", words: words})
	rig.waiting(t, run)
	rig.answer(t, run, "approve", map[string]any{"plan": 1})
	if record := rig.ended(t, run, time.Minute); record.state != "done" {
		t.Fatalf("the run ended %s with %q, want done", record.state, record.reason)
	}
	plan := rig.plan(t, run)
	if states := rowStates(plan); !slices.Equal(states, []string{"failed", "skipped", "done"}) {
		t.Fatalf("the rows are %v, want failed, skipped, done", states)
	}
	if skipped := plan.Rows[1].Result; skipped == nil || skipped.Code != "ROW_SKIPPED" || !strings.Contains(skipped.Message, "row 0") {
		t.Errorf("the skipped row's result is %+v, want ROW_SKIPPED naming the row it needs", skipped)
	}
	if rig.title(t, held) != "Held by bob" || rig.title(t, free) != "Renamed free" {
		t.Errorf("the titles are %q and %q, want the skipped row's left and the free row's written", rig.title(t, held), rig.title(t, free))
	}
}

// TestARowWhoseItemChangedWaitsAgain: a value another person changed after the approval and
// before the row's write makes the row wait again, showing the new value, while the other rows
// run; the person's approval of it again runs it. And where two rows of a plan write one item,
// another writer's change right after the first lands and before the second runs makes the second
// wait again, though the plan's own first write changed the item too.
func TestARowWhoseItemChangedWaitsAgain(t *testing.T) {
	rig := newGoingPlanRig(t)
	moved, still := rig.issue(t, "Moved meanwhile"), rig.issue(t, "Still")
	const words = "rename Moved meanwhile and Still"
	rig.loop.script.set(words, func(n int) agenttest.Reply {
		if n == 1 {
			return proposing(goingChange("dispatch_issue_update", retitle(moved, "Ours")), goingChange("dispatch_issue_update", retitle(still, "Ours too")))
		}
		return answered("Done, or waiting.")
	})
	run := rig.begin(t, goingLoopRun{login: "alice", words: words})
	rig.waiting(t, run)
	rig.loop.stop()
	rig.answer(t, run, "approve", map[string]any{"plan": 1})
	rig.fixture(t, "bob", http.MethodPatch, "/api/v1/issues/"+moved, map[string]any{"title": "Bob's"})
	rig.loop.start(rig.goingAgentRig)
	plan := rig.waiting(t, run)
	if row := plan.Rows[0]; row.State != "waiting" || !row.Changed || !strings.Contains(string(row.Now), "Bob's") {
		t.Fatalf("the changed row is %s (changed %v) showing %s, want it waiting again with Bob's title", row.State, row.Changed, row.Now)
	}
	if plan.Rows[1].State != "done" || rig.title(t, still) != "Ours too" || rig.title(t, moved) != "Bob's" {
		t.Fatalf("the other row is %s and the titles %q and %q, want it run and Bob's title kept", plan.Rows[1].State, rig.title(t, still), rig.title(t, moved))
	}
	rig.answer(t, run, "approve", map[string]any{"plan": 1})
	if record := rig.ended(t, run, time.Minute); record.state != "done" || rig.title(t, moved) != "Ours" {
		t.Fatalf("after approving it again the run is %s and the title %q, want done with Ours", record.state, rig.title(t, moved))
	}

	twice := rig.issue(t, "Written twice")
	const again = "comment on Written twice, and rename it twice"
	rig.loop.script.set(again, func(n int) agenttest.Reply {
		if n == 1 {
			return proposing(goingChange("dispatch_comment", map[string]any{"issue": twice, "body": "First of three."}),
				goingChange("dispatch_issue_update", retitle(twice, "Ours first")), goingChange("dispatch_issue_update", retitle(twice, "Ours second")))
		}
		return answered("Two ran, and one waits.")
	})
	var renames atomic.Int32
	rig.hooks.set(t, http.MethodPatch, "/api/v1/issues/"+twice, func(w http.ResponseWriter, r *http.Request, inner http.Handler) {
		inner.ServeHTTP(w, r)
		if renames.Add(1) == 1 {
			rig.fixture(t, "bob", http.MethodPatch, "/api/v1/issues/"+twice, map[string]any{"title": "Bob's between"})
		}
	})
	run = rig.begin(t, goingLoopRun{login: "alice", words: again})
	rig.waiting(t, run)
	rig.answer(t, run, "approve", map[string]any{"plan": 1})
	plan = rig.waiting(t, run)
	if states := rowStates(plan); !slices.Equal(states, []string{"done", "done", "waiting"}) || !plan.Rows[2].Changed || rig.title(t, twice) != "Bob's between" {
		t.Fatalf("the rows are %v (the last changed %v) and the title %q, want the last waiting again over Bob's title", states, plan.Rows[2].Changed, rig.title(t, twice))
	}
}

// TestThePlanStates: the states of a plan and of its rows, each reached through the routes and
// the agent - shown (waiting), approved, running, waiting again, done and rejected - and each
// event: approve, untick (which unticks each row that needs it), edit, revise, item changed and
// row failed.
func TestThePlanStates(t *testing.T) {
	rig := newGoingPlanRig(t)
	start := func(t *testing.T, words string, changes func(n int) agenttest.Reply) string {
		t.Helper()
		rig.loop.script.set(words, changes)
		return rig.begin(t, goingLoopRun{login: "alice", words: words})
	}

	t.Run("shown, untick, edit, approved, running, done", func(t *testing.T) {
		x, y, z := rig.issue(t, "X"), rig.issue(t, "Y"), rig.issue(t, "Z")
		run := start(t, "rename X, Y and Z", func(n int) agenttest.Reply {
			if n == 1 {
				return proposing(goingChange("dispatch_issue_update", retitle(x, "X2")),
					goingChange("dispatch_issue_update", retitle(y, "Y2"), 0), goingChange("dispatch_issue_update", retitle(z, "Z2")))
			}
			return answered("Renamed Z.")
		})
		if plan := rig.waiting(t, run); plan.State != "waiting" || !slices.Equal(rowStates(plan), []string{"waiting", "waiting", "waiting"}) {
			t.Fatalf("the shown plan is %s with rows %v, want it and every row waiting", plan.State, rowStates(plan))
		}
		rig.loop.stop()
		plan := rig.answer(t, run, "approve", map[string]any{"plan": 1, "rows": []map[string]any{
			{"row": 0, "tick": false}, {"row": 2, "tick": true, "values": map[string]any{"title": "Z edited"}},
		}})
		if plan.State != "approved" || !slices.Equal(rowStates(plan), []string{"unticked", "unticked", "approved"}) {
			t.Fatalf("the approved plan is %s with rows %v, want approved with the untick's two rows unticked", plan.State, rowStates(plan))
		}
		hold := make(chan struct{})
		rig.hooks.set(t, http.MethodPatch, "/api/v1/issues/"+z, func(w http.ResponseWriter, r *http.Request, inner http.Handler) {
			<-hold
			inner.ServeHTTP(w, r)
		})
		rig.loop.start(rig.goingAgentRig)
		goingEventually(t, "the row to run", func() bool { return rig.plan(t, run).State == "running" })
		close(hold)
		if record := rig.ended(t, run, time.Minute); record.state != "done" {
			t.Fatalf("the run ended %s with %q, want done", record.state, record.reason)
		}
		if plan := rig.plan(t, run); plan.State != "done" || rig.title(t, z) != "Z edited" || rig.title(t, x) != "X" || rig.title(t, y) != "Y" {
			t.Fatalf("the plan is %s and the titles %q %q %q, want done with the edited title alone", plan.State, rig.title(t, x), rig.title(t, y), rig.title(t, z))
		}
	})

	t.Run("item changed: waiting again", func(t *testing.T) {
		x := rig.issue(t, "Changed at the approval")
		run := start(t, "rename Changed at the approval", func(int) agenttest.Reply {
			return proposing(goingChange("dispatch_issue_update", retitle(x, "Ours")))
		})
		rig.waiting(t, run)
		rig.fixture(t, "bob", http.MethodPatch, "/api/v1/issues/"+x, map[string]any{"title": "Bob's first"})
		plan := rig.answer(t, run, "approve", map[string]any{"plan": 1})
		if plan.State != "waiting" || plan.Rows[0].State != "waiting" || !plan.Rows[0].Changed {
			t.Fatalf("the plan approved over a changed item is %s with its row %s (changed %v), want the row waiting again", plan.State, plan.Rows[0].State, plan.Rows[0].Changed)
		}
	})

	t.Run("row failed", func(t *testing.T) {
		x := rig.issue(t, "Claimed by bob")
		rig.fixture(t, "bob", http.MethodPost, "/api/v1/issues/"+x+"/claim", map[string]any{})
		run := start(t, "take Claimed by bob", func(n int) agenttest.Reply {
			if n == 1 {
				return proposing(goingChange("dispatch_claim", map[string]any{"issue": x}))
			}
			return answered("Bob holds it.")
		})
		rig.waiting(t, run)
		rig.answer(t, run, "approve", map[string]any{"plan": 1})
		rig.ended(t, run, time.Minute)
		if plan := rig.plan(t, run); plan.Rows[0].State != "failed" || plan.Rows[0].Result == nil || plan.Rows[0].Result.Status != http.StatusConflict {
			t.Fatalf("the refused row is %s with %+v, want failed with its route's 409", plan.Rows[0].State, plan.Rows[0].Result)
		}
	})

	t.Run("revise: rejected, and the run proposes the next plan", func(t *testing.T) {
		x := rig.issue(t, "Revised")
		const words = "rename Revised"
		run := start(t, words, func(n int) agenttest.Reply {
			if n == 1 {
				return proposing(goingChange("dispatch_issue_update", retitle(x, "First try")))
			}
			return proposing(goingChange("dispatch_issue_update", retitle(x, "As the person said")))
		})
		rig.waiting(t, run)
		if plan := rig.answer(t, run, "revise", map[string]any{"plan": 1, "words": "call it As the person said"}); plan.State != "rejected" {
			t.Fatalf("the revised plan is %s, want rejected", plan.State)
		}
		plan := rig.waiting(t, run)
		if plan.Plan != 2 || plan.State != "waiting" || !strings.Contains(string(plan.Rows[0].Arguments), "As the person said") {
			t.Fatalf("after the revision the plan is %d, %s, %s; want plan 2 waiting with the revised change", plan.Plan, plan.State, plan.Rows[0].Arguments)
		}
		if text := rig.loop.fake.Asking(words)[1].Text(); !strings.Contains(text, "call it As the person said") || !strings.Contains(text, "First try") {
			t.Errorf("the model's next request does not hold the person's words beside the plan's rows: %s", text)
		}
	})

	t.Run("reject: rejected, and the run is done with nothing run", func(t *testing.T) {
		x := rig.issue(t, "Rejected")
		run := start(t, "rename Rejected", func(int) agenttest.Reply {
			return proposing(goingChange("dispatch_issue_update", retitle(x, "Never")))
		})
		rig.waiting(t, run)
		if plan := rig.answer(t, run, "reject", map[string]any{"plan": 1}); plan.State != "rejected" {
			t.Fatalf("the rejected plan is %s", plan.State)
		}
		if record := rig.record(t, run); record.state != "done" || record.reason != goingPlanRejected || rig.title(t, x) != "Rejected" {
			t.Fatalf("the run is %s with %q and the title %q, want done with nothing run", record.state, record.reason, rig.title(t, x))
		}
	})
}
