package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/going"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// goingRunsRig is a server on the harness's database: its handler, which the route is read
// through, and the same server called directly, which is how the run writes its record.
type goingRunsRig struct {
	handler  http.Handler
	server   *server
	database *store.Store
}

func newGoingRunsRig(t *testing.T) goingRunsRig {
	t.Helper()
	handler, database, deps := newTestServer(t, testServerOptions{})
	return goingRunsRig{handler: handler, server: directServer(deps), database: database}
}

// run records one run for login and answers its id.
func (rig goingRunsRig) run(t *testing.T, login, words string) string {
	t.Helper()
	id := uuid.NewString()
	chip := &goingContext{Path: "/issues/DEMO-12", Project: "DEMO", Issue: "DEMO-12"}
	if err := rig.server.recordGoingRun(context.Background(), id, login, words, chip); err != nil {
		t.Fatalf("record the run %q: %v", words, err)
	}
	return id
}

// age moves a run's created_at back by age.
func (rig goingRunsRig) age(t *testing.T, id string, age time.Duration) {
	t.Helper()
	if _, err := rig.database.Pool.Exec(context.Background(), `
		update going_runs set created_at = now() - $2::interval where id = $1::uuid
	`, id, age.String()); err != nil {
		t.Fatalf("age the run: %v", err)
	}
}

// touched adds issue to the run's issues, as a request of the run that landed on it does
// (goingRunLanding.step).
func (rig goingRunsRig) touched(t *testing.T, id, issue string) {
	t.Helper()
	if _, err := rig.database.Pool.Exec(context.Background(), `
		update going_runs set issues = array_append(issues, $2) where id = $1::uuid
	`, id, issue); err != nil {
		t.Fatalf("add %s to the run's issues: %v", issue, err)
	}
}

// list reads GET /api/v1/me/going-runs as login.
func (rig goingRunsRig) list(t *testing.T, login, query string) goingRunsPage {
	t.Helper()
	response := dispatchRequest(t, rig.handler, http.MethodGet, "/api/v1/me/going-runs"+query, nil, login)
	if response.Code != http.StatusOK {
		t.Fatalf("list %s's going runs%s: status=%d body=%s", login, query, response.Code, response.Body.String())
	}
	return decodeBody[goingRunsPage](t, response)
}

func goingRunIDs(page goingRunsPage) []string {
	ids := make([]string, 0, len(page.Rows))
	for _, run := range page.Rows {
		ids = append(ids, run.ID)
	}
	return ids
}

// A run is stored under its caller and read back by that caller alone, newest first; a second
// person reads none of it, and the route takes no login that could name another.
func TestGoingRunsAreReadByTheirCallerAloneNewestFirst(t *testing.T) {
	rig := newGoingRunsRig(t)
	older := rig.run(t, "alice", "what moved yesterday?")
	rig.age(t, older, time.Hour)
	newer := rig.run(t, "alice", "close DEMO-12")

	page := rig.list(t, "alice", "")
	if got := goingRunIDs(page); !slices.Equal(got, []string{newer, older}) || page.Total != 2 || page.Limit != 50 || page.Offset != 0 {
		t.Fatalf("alice's runs = %v (total %d, limit %d, offset %d), want [%s %s] newest first on the default page",
			got, page.Total, page.Limit, page.Offset, newer, older)
	}
	if run := page.Rows[0]; run.AuthorLogin != "alice" || run.Words != "close DEMO-12" ||
		run.Context == nil || run.Context.Issue != "DEMO-12" || run.Context.Path != "/issues/DEMO-12" {
		t.Fatalf("the newest run reads %+v, want alice's words and context", run)
	}
	if got := rig.list(t, "alice", "?limit=1&offset=1"); !slices.Equal(goingRunIDs(got), []string{older}) || got.Total != 2 {
		t.Fatalf("alice's second page of one = %v (total %d), want [%s] of 2", goingRunIDs(got), got.Total, older)
	}
	if got := rig.list(t, "bob", ""); len(got.Rows) != 0 || got.Total != 0 {
		t.Fatalf("bob reads %v of alice's runs, want none", goingRunIDs(got))
	}
	if _, err := rig.server.readGoingRun(context.Background(), newer, "bob"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("bob reading alice's run by id: %v, want pgx.ErrNoRows", err)
	}
}

// ?issue= keeps the caller's runs whose landed requests or hits touched the issue, and a second
// person who has no such run reads nothing over the same issue.
func TestGoingRunsIssueFilterReadsThePersonsRunAlone(t *testing.T) {
	rig := newGoingRunsRig(t)
	ctx := context.Background()
	wrote := rig.run(t, "alice", "start DEMO-12")
	rig.touched(t, wrote, "DEMO-12")
	found := rig.run(t, "alice", "what blocks DEMO-7?")
	if err := rig.server.fillGoingRun(ctx, found, goingRunParts{Hits: []going.Hit{
		{Kind: "issue", ID: "DEMO-7", Title: "Seven", Href: "/issues/DEMO-7"},
		{Kind: "document", ID: "doc-1", Title: "A plan", Href: "/documents/doc-1"},
	}}); err != nil {
		t.Fatalf("fill the hits: %v", err)
	}
	rig.run(t, "alice", "nothing about either")
	bobs := rig.run(t, "bob", "start DEMO-99")
	rig.touched(t, bobs, "DEMO-99")

	if got := goingRunIDs(rig.list(t, "alice", "?issue=DEMO-12")); !slices.Equal(got, []string{wrote}) {
		t.Fatalf("alice's runs over DEMO-12 = %v, want the run that wrote it [%s]", got, wrote)
	}
	if got := goingRunIDs(rig.list(t, "alice", "?issue=DEMO-7")); !slices.Equal(got, []string{found}) {
		t.Fatalf("alice's runs over DEMO-7 = %v, want the run that found it [%s]", got, found)
	}
	if got := goingRunIDs(rig.list(t, "bob", "?issue=DEMO-12")); len(got) != 0 {
		t.Fatalf("bob's runs over DEMO-12 = %v, want none: the run over it is alice's", got)
	}
	if got := goingRunIDs(rig.list(t, "alice", "?issue=DEMO-99")); len(got) != 0 {
		t.Fatalf("alice's runs over DEMO-99 = %v, want none: the run over it is bob's", got)
	}
}

// A run nobody finished keeps every part it stored and reads unfinished, and reads finished,
// with every part kept, once it is finished.
func TestGoingRunsAnAbandonedRunKeepsItsOutcomesAndReadsUnfinished(t *testing.T) {
	rig := newGoingRunsRig(t)
	ctx := context.Background()
	id := rig.run(t, "alice", "assign DEMO-12 and DEMO-13 to me")
	structured := "refused"
	confidence := 0.93
	decision := goingDecision{
		Actions: []goingDecidedAction{{Action: "dispatch_issue_update", Confidence: &confidence}},
		Act:     true, Context: going.ContextNamed, Display: goingDecidedDisplay{Choice: going.DisplayProse},
	}
	proposal := goingProposal{Offers: []goingOffer{{Action: "dispatch_issue_update", Offered: true, Invocations: []goingInvocation{
		{Arguments: map[string]any{"issue": "DEMO-12", "assignee": "alice"}, TargetArgument: "issue"},
		{Arguments: map[string]any{"issue": "DEMO-13", "assignee": "alice"}, TargetArgument: "issue"},
	}}}}
	if err := rig.server.fillGoingRun(ctx, id, goingRunParts{
		Hits:             []going.Hit{{Kind: "issue", ID: "DEMO-13", Title: "Thirteen", Href: "/issues/DEMO-13"}},
		Decision:         &decision,
		Proposal:         &proposal,
		StructuredOutput: &structured,
	}); err != nil {
		t.Fatalf("fill the run: %v", err)
	}

	page := rig.list(t, "alice", "")
	if len(page.Rows) != 1 {
		t.Fatalf("alice's runs = %v, want the one", goingRunIDs(page))
	}
	run := page.Rows[0]
	if run.Finished || run.FinishedAt != nil {
		t.Fatalf("the abandoned run reads finished %v at %v, want unfinished", run.Finished, run.FinishedAt)
	}
	if !slices.Equal(run.Issues, []string{"DEMO-13"}) {
		t.Fatalf("the run's issues = %v, want the hit's, DEMO-13", run.Issues)
	}
	if len(run.Hits) != 1 || run.Hits[0].ID != "DEMO-13" || run.StructuredOutput == nil || *run.StructuredOutput != "refused" ||
		run.Decision == nil || !reflect.DeepEqual(*run.Decision, decision) ||
		run.Proposal == nil || !reflect.DeepEqual(*run.Proposal, proposal) {
		t.Fatalf("the run's parts read hits %+v, decision %+v, proposal %+v, structured_output %v; want what it stored",
			run.Hits, run.Decision, run.Proposal, run.StructuredOutput)
	}

	if err := rig.server.fillGoingRun(ctx, id, goingRunParts{Finished: true}); err != nil {
		t.Fatalf("finish the run: %v", err)
	}
	finished, err := rig.server.readGoingRun(ctx, id, "alice")
	if err != nil {
		t.Fatalf("read the finished run: %v", err)
	}
	if !finished.Finished || finished.FinishedAt == nil || len(finished.Hits) != 1 || finished.Proposal == nil {
		t.Fatalf("the finished run reads %+v, want it finished with every part kept", finished)
	}
}

// ?q= keeps the runs whose words hold the text in any case.
func TestGoingRunsWordsFilter(t *testing.T) {
	rig := newGoingRunsRig(t)
	for _, words := range []string{"Start DEMO-1", "start DEMO-2", "start DEMO-3", "start DEMO-4", "what is DEMO-5?"} {
		rig.run(t, "alice", words)
	}
	if got := rig.list(t, "alice", "?q=start%20demo"); len(got.Rows) != 4 || got.Total != 4 {
		t.Fatalf("alice's runs whose words hold 'start demo' = %v, want the four in any case", goingRunIDs(got))
	}
	if got := goingRunIDs(rig.list(t, "alice", "?q=DEMO-5")); len(got) != 1 {
		t.Fatalf("alice's runs whose words hold DEMO-5 = %v, want one", got)
	}
}

// A run older than seven days is not read, even before the sweep deletes it; one inside them is.
func TestGoingRunsOlderThanSevenDaysAreNotReturned(t *testing.T) {
	rig := newGoingRunsRig(t)
	old := rig.run(t, "alice", "eight days ago")
	rig.touched(t, old, "DEMO-12")
	rig.age(t, old, 8*24*time.Hour)
	kept := rig.run(t, "alice", "six days ago")
	rig.touched(t, kept, "DEMO-12")
	rig.age(t, kept, 6*24*time.Hour)

	for _, query := range []string{"", "?issue=DEMO-12"} {
		if got := goingRunIDs(rig.list(t, "alice", query)); !slices.Equal(got, []string{kept}) {
			t.Fatalf("alice's runs%s = %v, want the 6-day run alone [%s]", query, got, kept)
		}
	}
	if _, err := rig.server.readGoingRun(context.Background(), old, "alice"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("reading the 8-day run: %v, want pgx.ErrNoRows", err)
	}
}

// The maintenance pass deletes a run older than seven days and keeps one inside them.
func TestGoingRunsSweepDeletesARunPastSevenDays(t *testing.T) {
	rig := newGoingRunsRig(t)
	old := rig.run(t, "alice", "eight days ago")
	rig.age(t, old, 8*24*time.Hour)
	kept := rig.run(t, "alice", "six days ago")
	rig.age(t, kept, 6*24*time.Hour)

	stored := func(id string) bool {
		var count int
		if err := rig.database.Pool.QueryRow(context.Background(), `select count(*) from going_runs where id = $1::uuid`, id).Scan(&count); err != nil {
			t.Fatalf("count the run: %v", err)
		}
		return count == 1
	}
	leases := sweepLeases(t, rig.database)
	runSweep(t, func(ctx context.Context) {
		RunMaintenanceSweep(ctx, rig.database.Pool, maintenanceTestInterval, leases, nil)
	})
	waitUntil(t, 30*time.Second, "the 8-day run deleted by a maintenance pass", func() bool { return !stored(old) })
	if !stored(kept) {
		t.Fatal("the maintenance pass deleted the 6-day run, want it kept")
	}
}
