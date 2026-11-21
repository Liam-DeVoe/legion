package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/api/apicode"
)

func init() { addTourSteps("asks", asksTourSteps) }

// asksTourSteps are the route tour's steps of the asks: each route, each of the four
// states of PUT /api/v2/asks/{id}/state, and the reads with what include= and the filters add.
func asksTourSteps() []tourStep {
	alice := tourPerson("alice")
	askMissing := []tourMissing{{Param: "id", Value: uuid.NewString(), Code: apicode.AskNotFound}}
	issueMissing := []tourMissing{{Param: "key", Value: "NOPE-404", Code: apicode.IssueNotFound}}
	onAsk := func(query url.Values, body any) func(*testing.T, *routeTour) tourRequest {
		return func(t *testing.T, tour *routeTour) tourRequest {
			return tourRequest{Path: map[string]string{"id": tour.ask(t, tour.issue(t))}, Query: query, Body: body, As: alice}
		}
	}
	follower := func(session func(*testing.T, *routeTour) string) func(*testing.T, *routeTour) tourRequest {
		return func(t *testing.T, tour *routeTour) tourRequest {
			return tourRequest{Path: map[string]string{"id": tour.ask(t, tour.issue(t)), "session_id": session(t, tour)}, As: alice}
		}
	}
	state := func(name string, body map[string]any) tourStep {
		return tourStep{
			Method: http.MethodPut, Pattern: "/asks/{id}/state", Name: name, Status: http.StatusOK,
			Setup: onAsk(nil, body), Missing: askMissing,
		}
	}
	return []tourStep{
		{
			Method: http.MethodGet, Pattern: "/asks", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				tour.ask(t, tour.issue(t))
				return tourRequest{As: alice}
			},
		},
		{
			Method: http.MethodGet, Pattern: "/asks", Name: "waiting on me, with the thread and the mark", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				issue := tour.issue(t)
				tour.ask(t, issue)
				project, _, _ := strings.Cut(issue.Key, "-")
				return tourRequest{Query: url.Values{
					"waiting_on": {"me"}, "include": {"thread,mark"}, "state": {"all"},
					"project": {project}, "sort": {"-created_at"},
				}, As: alice}
			},
		},
		{
			Method: http.MethodGet, Pattern: "/asks", Name: "a source's question map", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				return tourRequest{Query: url.Values{"source": {"artifact:" + tour.issue(t).Document}}, As: alice}
			},
		},
		{
			Method: http.MethodGet, Pattern: "/asks/{id}", Status: http.StatusOK,
			Setup: onAsk(nil, nil), Missing: askMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/asks/{id}", Name: "with the thread and the mark", Status: http.StatusOK,
			Setup: onAsk(url.Values{"include": {"thread,mark"}}, nil), Missing: askMissing,
		},
		{
			Method: http.MethodPatch, Pattern: "/asks/{id}", Name: "a reword", Status: http.StatusOK,
			Setup: onAsk(nil, map[string]any{"question": "Which way does the tour go now?", "urgency": "high"}), Missing: askMissing,
		},
		{
			Method: http.MethodPatch, Pattern: "/asks/{id}", Name: "a hand-on", Status: http.StatusOK,
			Setup: onAsk(nil, map[string]any{"asked_of": "alice"}), Missing: askMissing,
		},
		{
			Method: http.MethodDelete, Pattern: "/asks/{id}/followers/{session_id}", Status: http.StatusNoContent,
			// The asking session follows its own ask, so there is a follower to remove.
			Setup:   follower(func(*testing.T, *routeTour) string { return tourSession }),
			Missing: askMissing,
		},
		{
			Method: http.MethodPut, Pattern: "/asks/{id}/followers/{session_id}", Status: http.StatusOK,
			Setup:   follower(func(*testing.T, *routeTour) string { return "session-tour-follower-" + uuid.NewString()[:8] }),
			Missing: askMissing,
		},
		state("answered", map[string]any{"state": "answered", "text": "Yes.", "expected_edited_at": nil}),
		state("resolved", map[string]any{"state": "resolved", "reason": "Settled on the issue."}),
		state("retracted", map[string]any{"state": "retracted", "reason": "Asked in the wrong place."}),
		state("declined", map[string]any{"state": "declined", "reason": "Decide it yourself."}),
		{
			Method: http.MethodGet, Pattern: "/issues/{key}/asks", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				issue := tour.issue(t)
				tour.ask(t, issue)
				return tourRequest{Path: map[string]string{"key": issue.Key}, As: alice}
			},
			Missing: issueMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/issues/{key}/asks", Name: "the open ones, with the thread and the mark", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				issue := tour.issue(t)
				tour.ask(t, issue)
				return tourRequest{
					Path: map[string]string{"key": issue.Key}, Query: url.Values{"state": {"open"}, "include": {"thread,mark"}}, As: alice,
				}
			},
			Missing: issueMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/issues/{key}/asks", Status: http.StatusCreated,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				return tourRequest{
					Path: map[string]string{"key": tour.issue(t).Key},
					Body: map[string]any{"question": "Does the tour open asks?", "options": []map[string]any{{"label": "Yes"}, {"label": "No"}}},
					As:   alice,
				}
			},
			Missing: issueMissing,
		},
	}
}

// questionRow is a question of the map, as both versions name one: its block, its ask and that
// ask's address.
type questionRow struct{ block, ask, ref string }

// GET /api/v2/asks?source=<kind>:<id> answers the asks GET /api/v1/references/questions answers for
// the same source (the route map's row 171): one row per question block of the source's document -
// a block of its own and a copy of its ask in a second block alike - each naming its block, its ask
// and the ask's address, with the document beside the page, whatever the asks' states; a source on
// no document is 404 NOT_FOUND in both.
func TestTheAskListOfASourceAnswersItsQuestionMap(t *testing.T) {
	tour := newRouteTour(t)
	alice := tourPerson("alice")
	issue := tour.issue(t)
	awaitSettled(t, tour.database, issue.Document)
	blockAsk := chosenID("ca000001", 1)
	seedExec(t, tour.database, "a version declaring a question block and a copy of it", `
		insert into artifact_versions (artifact_id, number, markdown)
		values ($1::uuid, (select coalesce(max(number), 0) + 1 from artifact_versions where artifact_id = $1::uuid), $2)
	`, issue.Document, "# Spec\n\n:::ask{#q1}\nWhich way?\n:::\n\n:::ask{#q2 copied_from=\""+blockAsk+"\"}\nWhich way?\n:::\n")
	seedExec(t, tour.database, "the ask settled for the question block", `
		insert into asks (id, issue_key, author, question, block_artifact_id, block_id)
		values ($1::uuid, $2, $3::jsonb, 'Which way?', $4::uuid, 'q1')
	`, blockAsk, issue.Key, idPrefixAuthor, issue.Document)
	seedAskOpenedEvent(t, tour.database, issue.Key, blockAsk)
	// An answered ask is in the map too: the map holds every state.
	answered := tour.ask(t, issue)
	seedExec(t, tour.database, "an answered ask of a third block", `
		update asks set state = 'answered', block_artifact_id = $2::uuid, block_id = 'q3',
			answer = '{"user":"alice","selected":[],"text":"Left.","at":"2026-11-17T09:00:00Z"}'::jsonb
		where id = $1::uuid
	`, answered, issue.Document)
	seedExec(t, tour.database, "the third block", `
		update artifact_versions set markdown = markdown || $2
		where artifact_id = $1::uuid and number = (select max(number) from artifact_versions where artifact_id = $1::uuid)
	`, issue.Document, "\n:::ask{#q3}\nLeft or right?\n:::\n")

	v1 := dispatchRequest(t, tour.handler, http.MethodGet, "/api/v1/references/questions?kind=artifact&id="+issue.Document, nil, "alice")
	if v1.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/references/questions: %d %s", v1.Code, v1.Body.String())
	}
	map1 := decodeBody[referenceQuestions](t, v1)
	var want []questionRow
	for _, question := range map1.Questions {
		want = append(want, questionRow{question.BlockID, question.Ask, question.Reference})
	}
	if len(want) != 3 {
		t.Fatalf("the v1 question map holds %+v, want the block, its copy and the answered ask's block", want)
	}

	route := tour.requireMounted(t, http.MethodGet, "/asks")
	for _, source := range []string{"artifact:" + issue.Document, "ask:" + blockAsk[:8]} {
		response := tour.send(t, route, tourRequest{Query: url.Values{"source": {source}}, As: alice}, "")
		if response.Code != http.StatusOK {
			t.Fatalf("GET /api/v2/asks?source=%s: %d %s", source, response.Code, response.Body.String())
		}
		list := decodeBody[askListV2](t, response)
		var got []questionRow
		for _, row := range list.Items {
			got = append(got, questionRow{row.QuestionBlockID, row.ID, row.Ref})
		}
		slices.SortFunc(got, func(left, right questionRow) int {
			return strings.Compare(left.block, right.block)
		})
		if !slices.Equal(got, want) || list.Total != len(want) || list.Document == nil || *list.Document != map1.Document {
			t.Fatalf("source=%s: rows %+v (total %d, document %v), want the v1 map %+v of document %s",
				source, got, list.Total, list.Document, want, map1.Document)
		}
	}

	message := uuid.NewString()
	if v1Nowhere := dispatchRequest(t, tour.handler, http.MethodGet, "/api/v1/references/questions?kind=message&id="+message, nil, "alice"); v1Nowhere.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/references/questions of a source on no document: %d %s, want 404 NOT_FOUND", v1Nowhere.Code, v1Nowhere.Body.String())
	} else if code, _ := refusalOf(t, v1Nowhere); code != "NOT_FOUND" {
		t.Fatalf("GET /api/v1/references/questions of a source on no document: refused %s, want NOT_FOUND", code)
	}
	nowhere := tour.send(t, route, tourRequest{Query: url.Values{"source": {"message:" + message}}, As: alice}, "")
	if code, _ := refusalOf(t, nowhere); nowhere.Code != http.StatusNotFound || code != "NOT_FOUND" {
		t.Fatalf("a source on no document: %d %s, want 404 NOT_FOUND", nowhere.Code, nowhere.Body.String())
	}
	malformed := tour.send(t, route, tourRequest{Query: url.Values{"source": {"issue:" + issue.Key}}, As: alice}, "")
	if code, _ := refusalOf(t, malformed); malformed.Code != http.StatusBadRequest || code != "INVALID_QUERY" {
		t.Fatalf("a source of no source kind: %d %s, want 400 INVALID_QUERY", malformed.Code, malformed.Body.String())
	}
}

// TestAV2AskListWithItsThreadsTakesAPageOfTwenty: include=thread answers each ask's thread, its
// replies, rewordings, answers and followers, so a page of asks that carries it holds at most 20
// (includedPageMax), as a list of artifacts with include=blocks does
// (TestAV2ArtifactListWithItsBlocksTakesAPageOfTwenty; the architect's ruling of 2026-11-21 on
// dispatch://INFRA-4071). On each of the three lists of asks, with 21 asks on an issue and 21 on a
// project document: limit=21 or limit=200 with include=thread is 400 INVALID_LIMIT naming 1 to 20,
// a missing issue's or document's list too, before the owner is read; include=thread naming no
// limit reads a page of 20, each with its thread, and total counts every ask; and limit=200 with
// include=mark alone, or with no include, still reads a page of 200, no row with a thread.
func TestAV2AskListWithItsThreadsTakesAPageOfTwenty(t *testing.T) {
	handler := newTestHandler(t)
	createReferenceAPIProject(t, handler, "CORE")
	issue := createReferenceAPIIssue(t, handler, "CORE")
	document := createProjectDocument(t, handler, "CORE", "Design notes", "# Design notes\n")
	const asked = includedPageMax + 1
	for index := range asked {
		for _, owner := range []string{"/api/v1/issues/" + issue.Key + "/asks", "/api/v1/artifacts/" + document.ID + "/asks"} {
			created := dispatchRequest(t, handler, http.MethodPost, owner, map[string]any{"question": fmt.Sprintf("Which way, %d?", index)}, "alice")
			if created.Code != http.StatusCreated {
				t.Fatalf("POST %s: %d %s", owner, created.Code, created.Body.String())
			}
		}
	}
	for list, total := range map[string]int{
		"/api/v2/asks": 2 * asked, "/api/v2/issues/" + issue.Key + "/asks": asked, "/api/v2/artifacts/" + document.ID + "/asks": asked,
	} {
		for _, target := range []string{list + "?limit=21&include=thread", list + "?limit=200&include=thread"} {
			refused := dispatchRequest(t, handler, http.MethodGet, target, nil, "alice")
			if code, text := refusalOf(t, refused); refused.Code != http.StatusBadRequest || code != "INVALID_LIMIT" || !strings.Contains(text, "1 to 20 with include=thread") {
				t.Errorf("GET %s answered %d %s, want 400 INVALID_LIMIT naming 1 to 20 with include=thread", target, refused.Code, refused.Body.String())
			}
		}
		for query, limit := range map[string]int{"include=thread": 20, "limit=200&include=mark": 200, "limit=200": 200} {
			answered := dispatchRequest(t, handler, http.MethodGet, list+"?"+query, nil, "alice")
			if answered.Code != http.StatusOK {
				t.Errorf("GET %s?%s answered %d %s, want 200", list, query, answered.Code, answered.Body.String())
				continue
			}
			got := decodeBody[page[askV2]](t, answered)
			if got.Limit != limit || len(got.Items) != min(limit, total) || got.Total != total {
				t.Errorf("GET %s?%s read a page of %d holding %d asks of %d, want %d holding %d of %d",
					list, query, got.Limit, len(got.Items), got.Total, limit, min(limit, total), total)
			}
			threaded := query == "include=thread"
			for _, row := range got.Items {
				if (row.Thread != nil) != threaded {
					t.Errorf("GET %s?%s answered ask %s with a thread %t, want one only with include=thread", list, query, row.ID, row.Thread != nil)
					break
				}
			}
		}
	}
	for _, missing := range []string{"/api/v2/issues/CORE-404/asks", "/api/v2/artifacts/" + uuid.NewString() + "/asks"} {
		if code, _ := refusalOf(t, dispatchRequest(t, handler, http.MethodGet, missing+"?limit=21&include=thread", nil, "alice")); code != "INVALID_LIMIT" {
			t.Errorf("GET %s?limit=21&include=thread answered %s, want INVALID_LIMIT before the owner is read", missing, code)
		}
	}
}

// TestAV2AskWhoseAnswerCannotBeReadOpensNothing: both /api/v2 ask creates read the ask row they
// answer on the write's own transaction before it commits (openAsk's readBack), so a read that
// fails rolls the ask back - answered 500, with no ask stored - and a retry under the same
// Idempotency-Key opens the ask once. A trigger makes the read fail: it leaves each new ask with
// no actor for its author, which no read the write makes before its answer decodes, as the
// /api/v1 create, which reads nothing back, shows.
func TestAV2AskWhoseAnswerCannotBeReadOpensNothing(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	createReferenceAPIProject(t, handler, "CORE")
	issue := createReferenceAPIIssue(t, handler, "CORE")
	document := createProjectDocument(t, handler, "CORE", "Design notes", "# Design notes\n")
	stored := func(t *testing.T, question string) int {
		t.Helper()
		var count int
		if err := database.Pool.QueryRow(t.Context(), `select count(*) from asks where question = $1`, question).Scan(&count); err != nil {
			t.Fatalf("count the asks of %q: %v", question, err)
		}
		return count
	}
	creates := []struct{ name, route, question, key string }{
		{"the issue's ask", "/api/v2/issues/" + issue.Key + "/asks", "Which way does the issue go?", uuid.NewString()},
		{"the document's ask", "/api/v2/artifacts/" + document.ID + "/asks", "Which way does the design go?", uuid.NewString()},
	}
	seedExec(t, database, "a trigger leaving a new ask's author unreadable", `
		create function dispatch_test_unreadable_ask_author() returns trigger language plpgsql as $$
		begin
			update asks set author = '[]'::jsonb where id = new.id;
			return null;
		end $$;
		create trigger dispatch_test_unreadable_ask_author after insert on asks
		for each row execute function dispatch_test_unreadable_ask_author();`)
	for _, create := range creates {
		failed := keyedRequest(t, handler, http.MethodPost, create.route, map[string]any{"question": create.question}, "alice", create.key)
		if failed.Code != http.StatusInternalServerError {
			t.Fatalf("%s, whose answer cannot be read: %d %s, want 500", create.name, failed.Code, failed.Body.String())
		}
		if count := stored(t, create.question); count != 0 {
			t.Fatalf("%s failed and left %d asks, want none", create.name, count)
		}
	}
	control := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/asks",
		map[string]any{"question": "Which way does the control go?"}, "alice")
	if control.Code != http.StatusCreated {
		t.Fatalf("the /api/v1 create, which reads nothing back, under the same trigger: %d %s, want 201", control.Code, control.Body.String())
	}

	seedExec(t, database, "the trigger dropped", `drop trigger dispatch_test_unreadable_ask_author on asks`)
	for _, create := range creates {
		retried := keyedRequest(t, handler, http.MethodPost, create.route, map[string]any{"question": create.question}, "alice", create.key)
		if retried.Code != http.StatusCreated || retried.Header().Get(contracts.IdempotencyReplayedHeader) != "" {
			t.Fatalf("the retry of %s under its key: %d (replayed %q) %s, want the ask opened afresh, 201", create.name,
				retried.Code, retried.Header().Get(contracts.IdempotencyReplayedHeader), retried.Body.String())
		}
		if count := stored(t, create.question); count != 1 {
			t.Fatalf("the retry of %s left %d asks, want one", create.name, count)
		}
	}
}

// TestAV2AskStateWriteWhoseAnswerCannotBeReadChangesNothing: PUT /api/v2/asks/{id}/state reads the
// ask it answers on the transition's transaction before it commits (writtenAskV2), so a read that
// fails rolls the transition back - answered 500, the ask open still and no ask.resolved event
// appended - and a retry under the same Idempotency-Key resolves it once. A trigger makes the read
// fail: once an ask's row is updated it leaves its options no option, which no read the transition
// makes before its answer decodes, as the /api/v1 resolve, which reads nothing back, shows.
func TestAV2AskStateWriteWhoseAnswerCannotBeReadChangesNothing(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	createReferenceAPIProject(t, handler, "CORE")
	issue := createReferenceAPIIssue(t, handler, "CORE")
	open := func(t *testing.T, question string) string {
		t.Helper()
		created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/asks", map[string]any{"question": question}, "alice")
		if created.Code != http.StatusCreated {
			t.Fatalf("open %q: %d %s", question, created.Code, created.Body.String())
		}
		return decodeBody[struct {
			ID string `json:"id"`
		}](t, created).ID
	}
	asked, control := open(t, "Which way does the issue go?"), open(t, "Which way does the control go?")
	held := func(t *testing.T) (state string, resolutions int) {
		t.Helper()
		if err := database.Pool.QueryRow(t.Context(), `
			select (select state from asks where id = $1),
			       (select count(*) from events where issue_key = $2 and type = 'ask.resolved')
		`, asked, issue.Key).Scan(&state, &resolutions); err != nil {
			t.Fatalf("read the ask's state and the issue's resolutions: %v", err)
		}
		return state, resolutions
	}
	seedExec(t, database, "a trigger leaving an updated ask's options unreadable", `
		create function dispatch_test_unreadable_ask_options() returns trigger language plpgsql as $$
		begin
			if new.options <> '[1]'::jsonb then
				update asks set options = '[1]'::jsonb where id = new.id;
			end if;
			return null;
		end $$;
		create trigger dispatch_test_unreadable_ask_options after update on asks
		for each row execute function dispatch_test_unreadable_ask_options();`)
	resolve := map[string]any{"state": "resolved", "reason": "Settled on the issue."}
	key := uuid.NewString()
	route := "/api/v2/asks/" + asked + "/state"
	if failed := keyedRequest(t, handler, http.MethodPut, route, resolve, "alice", key); failed.Code != http.StatusInternalServerError {
		t.Fatalf("the state write whose answer cannot be read: %d %s, want 500", failed.Code, failed.Body.String())
	}
	if state, resolutions := held(t); state != "open" || resolutions != 0 {
		t.Fatalf("the failed state write left the ask %s and the issue %d ask.resolved events, want open and none", state, resolutions)
	}
	if resolved := dispatchRequest(t, handler, http.MethodPost, "/api/v1/asks/"+control+"/resolve",
		map[string]any{"kind": "resolved", "reason": "Settled on the issue."}, "alice"); resolved.Code != http.StatusOK {
		t.Fatalf("the /api/v1 resolve, which reads nothing back, under the same trigger: %d %s, want 200", resolved.Code, resolved.Body.String())
	}

	seedExec(t, database, "the trigger dropped", `drop trigger dispatch_test_unreadable_ask_options on asks`)
	retried := keyedRequest(t, handler, http.MethodPut, route, resolve, "alice", key)
	if retried.Code != http.StatusOK || retried.Header().Get(contracts.IdempotencyReplayedHeader) != "" {
		t.Fatalf("the retry under the failed write's key: %d (replayed %q) %s, want the ask resolved afresh, 200",
			retried.Code, retried.Header().Get(contracts.IdempotencyReplayedHeader), retried.Body.String())
	}
	if state, resolutions := held(t); state != "resolved" || resolutions != 2 {
		t.Fatalf("the retry left the ask %s and the issue %d ask.resolved events, want resolved and two (the control's and its own)", state, resolutions)
	}
}

// TestAV2ApprovalRequestWhoseAnswerCannotBeReadOpensNothing: POST
// /api/v2/artifacts/{id}/approval-requests reads the ask it answers on the request's transaction
// before it commits (writtenAskV2), so a read that fails rolls the request back - answered 500,
// no approval request opened - and a retry under the same Idempotency-Key opens it once. A trigger
// makes the read fail: it leaves each new ask's options no option, which no read the request makes
// before its answer decodes, as the /api/v1 request, which reads nothing back, shows.
func TestAV2ApprovalRequestWhoseAnswerCannotBeReadOpensNothing(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	issue := createInteractionIssue(t, handler, "APPRF", "Request the spec's approval", "A spec the human approves.")
	control := createInteractionIssue(t, handler, "APPRC", "Request the control's approval", "A spec the control asks about.")
	requests := func(t *testing.T, key string) int {
		t.Helper()
		var count int
		if err := database.Pool.QueryRow(t.Context(), `select count(*) from asks where issue_key = $1 and kind = 'approval'`, key).Scan(&count); err != nil {
			t.Fatalf("count the approval requests of %s: %v", key, err)
		}
		return count
	}
	seedExec(t, database, "a trigger leaving a new ask's options unreadable", `
		create function dispatch_test_unreadable_new_ask_options() returns trigger language plpgsql as $$
		begin
			update asks set options = '[1]'::jsonb where id = new.id;
			return null;
		end $$;
		create trigger dispatch_test_unreadable_new_ask_options after insert on asks
		for each row execute function dispatch_test_unreadable_new_ask_options();`)
	body := map[string]any{"summary": "The spec as it stands."}
	key := uuid.NewString()
	route := "/api/v2/artifacts/" + issue.PrimaryArtifactID + "/approval-requests"
	if failed := keyedRequest(t, handler, http.MethodPost, route, body, "alice", key); failed.Code != http.StatusInternalServerError {
		t.Fatalf("the approval request whose answer cannot be read: %d %s, want 500", failed.Code, failed.Body.String())
	}
	if count := requests(t, issue.Key); count != 0 {
		t.Fatalf("the failed approval request left %d approval asks, want none", count)
	}
	if requested := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+control.PrimaryArtifactID+"/approval-requests",
		body, "alice"); requested.Code != http.StatusCreated {
		t.Fatalf("the /api/v1 request, which reads nothing back, under the same trigger: %d %s, want 201", requested.Code, requested.Body.String())
	}

	seedExec(t, database, "the trigger dropped", `drop trigger dispatch_test_unreadable_new_ask_options on asks`)
	retried := keyedRequest(t, handler, http.MethodPost, route, body, "alice", key)
	if retried.Code != http.StatusCreated || retried.Header().Get(contracts.IdempotencyReplayedHeader) != "" {
		t.Fatalf("the retry under the failed request's key: %d (replayed %q) %s, want the request opened afresh, 201",
			retried.Code, retried.Header().Get(contracts.IdempotencyReplayedHeader), retried.Body.String())
	}
	if count := requests(t, issue.Key); count != 1 {
		t.Fatalf("the retry left %d approval asks, want one", count)
	}
}
