package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The held-target guard (Decision 4 of INFRA-3231's plan): every write whose path names an issue,
// an ask, a comment or a message either takes part when that text is held - it is accepted,
// kept beside the held text and runs right after it posts - or is listed in heldTargetSelf with
// why, as writing_check_route_guard_test.go holds every prose route to the writing check.

// heldTargetWrites are the writes of the served table whose path names a text a write may wait on.
func heldTargetWrites() []apiRoute {
	var writes []apiRoute
	for _, route := range (&server{deps: Deps{TestHooksEnabled: true}}).routes() {
		if route.Method != http.MethodGet && heldTargetParam(route.Pattern) != "" {
			writes = append(writes, route)
		}
	}
	return writes
}

// TestEveryWriteNamingAHeldTextTakesPartOrSaysWhyNot: each write whose path names an issue, an
// ask, a comment or a message is held by awaitHeldTarget or named in heldTargetSelf with a reason,
// and heldTargetSelf names no route the table does not serve.
func TestEveryWriteNamingAHeldTextTakesPartOrSaysWhyNot(t *testing.T) {
	writes := heldTargetWrites()
	served := map[string]bool{}
	held := 0
	for _, route := range writes {
		name := route.Method + " " + route.Pattern
		served[name] = true
		if _, takesPart := awaitsHeldTarget(route.Method, route.Pattern); takesPart {
			held++
			continue
		}
		if strings.TrimSpace(heldTargetSelf[name]) == "" {
			t.Errorf("%s names a text by its path and neither waits on a held one nor says why not", name)
		}
	}
	for name := range heldTargetSelf {
		if !served[name] {
			t.Errorf("heldTargetSelf names %s, which is no write of the table naming a text", name)
		}
	}
	if held < 20 {
		t.Errorf("%d writes wait on a held text, want every write naming one", held)
	}
	t.Logf("%d writes name a text by their path; %d wait on a held one", len(writes), held)
}

// TestEveryWriteNamingAHeldTextIsAcceptedAndKept walks every write awaitHeldTarget holds with
// the id of a held issue, ask, comment or message in its path: each answers 202 with the held
// text it waits on and is kept beside it. The three creates of heldTargetSelf on a held issue are
// held by the reviewer's hook under ids of their own (201).
func TestEveryWriteNamingAHeldTextIsAcceptedAndKept(t *testing.T) {
	rig := newReviewRig(t, ReviewerConfig{Asks: ReviewHold, Issues: ReviewHold, Comments: ReviewHold, Messages: ReviewHold, WriterWindow: time.Hour}, nil)
	targets := map[string]string{
		"/api/v1/issues/{key}": "RVW-900", "/api/v1/asks/{id}": uuid.NewString(),
		"/api/v1/comments/{id}": uuid.NewString(), "/api/v1/messages/{id}": uuid.NewString(),
	}
	kinds := map[string]string{
		"/api/v1/issues/{key}": "issue", "/api/v1/asks/{id}": "ask", "/api/v1/comments/{id}": "comment", "/api/v1/messages/{id}": "message",
	}
	for prefix, id := range targets {
		if _, err := rig.database.Pool.Exec(context.Background(), `
			insert into held_texts (id, kind, route, request, original, actor, writer, reader, project, state, deadline)
			values ($1, $2, 'POST /fixture', '{}', '{}', '{"kind":"session","id":"s-writer"}', 's-writer', 'alice', 'RVW',
				'awaiting_writer', now() + interval '1 hour')`, id, kinds[prefix]); err != nil {
			t.Fatalf("hold the fixture %s: %v", id, err)
		}
	}
	fill := func(pattern string) string {
		path := pattern
		for prefix, id := range targets {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				path = strings.Replace(path, "{"+heldTargetParam(prefix)+"}", id, 1)
				break
			}
		}
		for placeholder, value := range map[string]string{
			"{slug}": "spec", "{phase}": "implement", "{session_id}": "s-other", "{attempt}": "1", "{block_id}": "b1",
		} {
			path = strings.ReplaceAll(path, placeholder, value)
		}
		return path
	}
	for _, route := range heldTargetWrites() {
		name := route.Method + " " + route.Pattern
		if _, takesPart := awaitsHeldTarget(route.Method, route.Pattern); !takesPart {
			continue
		}
		t.Run(name, func(t *testing.T) {
			target := fill(route.Pattern)
			response := dispatchRequest(t, rig.handler, route.Method, target, map[string]any{}, "alice")
			if response.Code != http.StatusAccepted {
				t.Fatalf("%s %s: %d %s, want 202", route.Method, target, response.Code, response.Body.String())
			}
			answer := decodeBody[heldDeferred](t, response)
			row := rig.heldRow(answer.ID)
			if answer.Held.WaitsOn == "" || row.dependsOn != answer.Held.WaitsOn || row.kind != heldRequestKind || row.route != name {
				t.Fatalf("the kept write: answer %+v row depends on %q kind %s route %s", answer, row.dependsOn, row.kind, row.route)
			}
		})
	}
	for _, create := range []struct{ path, body string }{
		{"/api/v1/issues/RVW-900/asks", "question"}, {"/api/v1/issues/RVW-900/comments", "body"}, {"/api/v1/issues/RVW-900/messages", "body"},
	} {
		answer := rig.held(rig.asWriter(http.MethodPost, create.path, map[string]any{create.body: "A write on the held issue."}), create.path)
		if answer.Held.WaitsOn != "RVW-900" || slices.Contains([]string{"", "RVW-900"}, answer.ID) {
			t.Errorf("%s: %+v, want an id of its own waiting on RVW-900", create.path, answer)
		}
	}
}
