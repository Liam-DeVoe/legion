package api

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/dispatch/api/apicode"
)

func init() { addTourSteps("asks", asksTourSteps) }

// asksTourSteps are the route tour's steps of the asks (plan t8): each route, each of the four
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
// the same source (the route map's row 171, plan t8's acceptance): one row per question block of
// the source's document - a block of its own and a copy of its ask in a second block alike - each
// naming its block, its ask and the ask's address, with the document beside the page, whatever
// the asks' states; a source on no document is 404 NOT_FOUND in both.
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

	nowhere := tour.send(t, route, tourRequest{Query: url.Values{"source": {"message:" + uuid.NewString()}}, As: alice}, "")
	if code, _ := refusalOf(t, nowhere); nowhere.Code != http.StatusNotFound || code != "NOT_FOUND" {
		t.Fatalf("a source on no document: %d %s, want 404 NOT_FOUND", nowhere.Code, nowhere.Body.String())
	}
	malformed := tour.send(t, route, tourRequest{Query: url.Values{"source": {"issue:" + issue.Key}}, As: alice}, "")
	if code, _ := refusalOf(t, malformed); malformed.Code != http.StatusBadRequest || code != "INVALID_QUERY" {
		t.Fatalf("a source of no source kind: %d %s, want 400 INVALID_QUERY", malformed.Code, malformed.Body.String())
	}
}
