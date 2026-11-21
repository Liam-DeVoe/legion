package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestBlockedByIsRefusedByNameAndNeedsIsTheOneSpelling is c-blocked-by-gone's server half (R27 of
// dispatch://INFRA-1098): needs is the one spelling of an issue's waits.
//
// The issue read, the summary read and the issue's event answer needs and no blocked_by, and
// model.GraphEdgeKinds holds no blocked_by. The issue read answers no verification_artifact_id and
// no plan_artifact_id either.
//
// A create and a PATCH that carry blocked_by and no needs are each refused 400, the refusal naming
// the member and needs, and neither stores anything: no needs tuple and no issue_links row for that
// target. A route that ignored the member it no longer reads would take the request and drop the
// wait without a word, and the daemon would then start an issue that must wait. A create and a
// PATCH that carry needs store the tuple and its issue_links row. A create and a PATCH that carry
// both are served from needs: the needs targets are stored, and no tuple for a target blocked_by
// alone names.
//
// It also reads the two web files that rendered or read the old spelling, in packages/dispatch:
// web/src/features/refs/ReferencedBy.tsx holds no blocked_by row in RENDERED_EDGE_KINDS and no
// blocked_by label in STRUCTURAL_LABELS, and web/src/api/event-refresh.ts reads no blocked_by event
// member. Each search first finds a blocked_by planted in a scratch copy of its file, so a search
// that cannot find is no pass.
func TestBlockedByIsRefusedByNameAndNeedsIsTheOneSpelling(t *testing.T) {
	t.Run("the web files hold no blocked_by row, label or event read", requireTheWebFilesHoldNoBlockedBy)
	t.Run("the edge kinds hold no blocked_by", func(t *testing.T) {
		if slices.Contains(model.GraphEdgeKinds, blockedByMember) {
			t.Errorf("model.GraphEdgeKinds %v holds blocked_by, want needs alone for an issue's wait", model.GraphEdgeKinds)
		}
		if !slices.Contains(model.GraphEdgeKinds, model.EdgeKindNeeds) {
			t.Errorf("model.GraphEdgeKinds %v holds no needs", model.GraphEdgeKinds)
		}
	})

	handler, database, _ := newTestServer(t, testServerOptions{settle: time.Hour})
	keys := createListedIssues(t, handler, "GONE", 6)
	waiter, first, second, third, refusedTarget, patched := keys[0], keys[1], keys[2], keys[3], keys[4], keys[5]

	// storedWait is whether issue states a needs tuple to target and holds the issue_links row of
	// the pair.
	storedWait := func(issue, target string) (bool, bool) {
		t.Helper()
		tuple := slices.Contains(relationEnds(currentRelations(t, database, issue, graphstore.TupleNeeds)), graphstore.KindIssue+":"+target)
		row, _ := issueLinkMarker(t, database, issue, target)
		return tuple, row
	}
	requireStored := func(what, issue string, stored, absent []string) {
		t.Helper()
		for _, target := range stored {
			if tuple, row := storedWait(issue, target); !tuple || !row {
				t.Errorf("%s: %s holds the needs tuple to %s %t and its issue_links row %t, want both", what, issue, target, tuple, row)
			}
		}
		for _, target := range absent {
			if tuple, row := storedWait(issue, target); tuple || row {
				t.Errorf("%s: %s holds the needs tuple to %s %t and its issue_links row %t, want neither", what, issue, target, tuple, row)
			}
		}
	}

	t.Run("blocked_by alone is refused by name and stores nothing", func(t *testing.T) {
		create := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]any{
			"project": "GONE", "title": "Waits by the old spelling", "blocked_by": []string{refusedTarget}, "force": true,
		}, "alice")
		requireRelationRefused(t, "a create carrying blocked_by and no needs", create, http.StatusBadRequest, codeBlockedByInput, "blocked_by", "needs")
		var made int
		if err := database.Pool.QueryRow(context.Background(), `select count(*) from issues where title = 'Waits by the old spelling'`).Scan(&made); err != nil {
			t.Fatalf("count the issues the refused create made: %v", err)
		}
		if made != 0 {
			t.Errorf("the refused create made %d issue(s), want none", made)
		}
		var waits int
		if err := database.Pool.QueryRow(context.Background(), `
			select (select count(*) from issue_links where target_key = $1) +
			       (select count(*) from graph_tuples where kind = 'needs' and to_kind = 'issue' and to_id = $1)
		`, refusedTarget).Scan(&waits); err != nil {
			t.Fatalf("count the waits on %s: %v", refusedTarget, err)
		}
		if waits != 0 {
			t.Errorf("the refused create left %d needs tuple(s) or issue_links row(s) naming %s, want none", waits, refusedTarget)
		}

		patch := dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+patched,
			map[string]any{"blocked_by": []string{refusedTarget}, "title": "Renamed by a refused write"}, "alice")
		requireRelationRefused(t, "a PATCH carrying blocked_by and no needs", patch, http.StatusBadRequest, codeBlockedByInput, "blocked_by", "needs")
		requireStored("the refused PATCH", patched, nil, []string{refusedTarget})
		if issue := readRelationsIssue(t, handler, patched); issue.Title == "Renamed by a refused write" || len(issue.Needs) != 0 {
			t.Errorf("the refused PATCH left %s titled %q waiting on %v, want nothing of it stored", patched, issue.Title, issue.Needs)
		}
	})

	t.Run("needs stores its tuple and its row", func(t *testing.T) {
		created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]any{
			"project": "GONE", "title": "Waits by needs", "needs": []string{first}, "force": true,
		}, "alice")
		if created.Code != http.StatusCreated {
			t.Fatalf("a create carrying needs: status=%d body=%s", created.Code, created.Body.String())
		}
		createdKey := decodeBody[model.Issue](t, created).Key
		requireStored("the create carrying needs", createdKey, []string{first}, nil)

		response := dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+waiter, map[string]any{"needs": []string{first}}, "alice")
		if response.Code != http.StatusOK {
			t.Fatalf("a PATCH carrying needs: status=%d body=%s", response.Code, response.Body.String())
		}
		requireStored("the PATCH carrying needs", waiter, []string{first}, nil)
	})

	t.Run("both members are served from needs alone", func(t *testing.T) {
		created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]any{
			"project": "GONE", "title": "Waits by both spellings", "needs": []string{second}, "blocked_by": []string{third}, "force": true,
		}, "alice")
		if created.Code != http.StatusCreated {
			t.Fatalf("a create carrying both: status=%d body=%s", created.Code, created.Body.String())
		}
		createdKey := decodeBody[model.Issue](t, created).Key
		requireStored("the create carrying both", createdKey, []string{second}, []string{third})

		response := dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+waiter,
			map[string]any{"needs": []string{first, second}, "blocked_by": []string{third}}, "alice")
		if response.Code != http.StatusOK {
			t.Fatalf("a PATCH carrying both: status=%d body=%s", response.Code, response.Body.String())
		}
		requireStored("the PATCH carrying both", waiter, []string{first, second}, []string{third})
	})

	t.Run("the reads and the event answer needs and no blocked_by", func(t *testing.T) {
		want := []string{first, second}
		issue := readMembers(t, handler, "/api/v1/issues/"+waiter)
		requireNeedsAlone(t, "the issue read", issue, want)

		listed := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues?project=GONE", nil, "alice")
		if listed.Code != http.StatusOK {
			t.Fatalf("list GONE: status=%d body=%s", listed.Code, listed.Body.String())
		}
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(listed.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decode the listing: %v", err)
		}
		found := false
		for _, row := range rows {
			var key string
			_ = json.Unmarshal(row["key"], &key)
			if _, held := row[blockedByMember]; held {
				t.Errorf("the summary read's row %s answers blocked_by %s", key, row[blockedByMember])
			}
			if key == waiter {
				found = true
				requireNeedsAlone(t, "the summary read", row, want)
			}
		}
		if !found {
			t.Fatalf("the summary read lists no row %s", waiter)
		}

		event := newestEventPayload[map[string]json.RawMessage](t, database, "issue.updated", waiter)
		requireNeedsAlone(t, "the issue's event", event, want)
		var changed []string
		if err := json.Unmarshal(event["changed"], &changed); err != nil {
			t.Fatalf("decode the event's changed list: %v", err)
		}
		if slices.Contains(changed, blockedByMember) || !slices.Contains(changed, model.EdgeKindNeeds) {
			t.Errorf("the issue's event names %v as changed, want needs and no blocked_by", changed)
		}
	})

	t.Run("the issue read answers neither artifact field", func(t *testing.T) {
		issue := readMembers(t, handler, "/api/v1/issues/"+waiter)
		for _, member := range []string{"verification_artifact_id", "plan_artifact_id"} {
			if value, held := issue[member]; held {
				t.Errorf("the issue read answers %s %s, want no such member: the query nodes name the issue's verification and plan", member, value)
			}
		}
	})
}

// TestAGoingActionNamingWaitsSendsNeedsAlone: the issue tools' wait argument is blocked_by, the
// flag a person types, and a going action naming it reaches Dispatch as needs alone, which the
// route takes. The control is the same body sent with blocked_by, which the route refuses by name.
func TestAGoingActionNamingWaitsSendsNeedsAlone(t *testing.T) {
	handler, database, _ := newTestServer(t, testServerOptions{settle: time.Hour})
	keys := createListedIssues(t, handler, "GOWAIT", 3)
	catalogue, err := BuildGoingCatalogue()
	if err != nil {
		t.Fatalf("build the catalogue: %v", err)
	}
	doer := &handlerDoer{handler: handler, login: "alice"}
	caller := GoingCaller{Catalogue: catalogue, BaseURL: "https://dispatch.invalid", Doer: doer}

	for _, call := range []struct {
		action    string
		arguments map[string]any
		status    int
	}{
		{"dispatch_issue", map[string]any{"project": "GOWAIT", "title": "Proposed waiting", "blocked_by": []any{keys[1]}, "force": true}, http.StatusCreated},
		{"dispatch_issue_update", map[string]any{"issue": keys[0], "blocked_by": []any{keys[2]}}, http.StatusOK},
	} {
		response, err := caller.Do(context.Background(), call.action, call.arguments)
		if err != nil {
			t.Fatalf("perform %s: %v", call.action, err)
		}
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != call.status {
			t.Fatalf("%s answered %d %s, want %d", call.action, response.StatusCode, body, call.status)
		}
		sent := doer.bodies[len(doer.bodies)-1]
		if _, held := sent[blockedByMember]; held {
			t.Errorf("%s sent a blocked_by member: %v", call.action, sent)
		}
		if _, held := sent[model.EdgeKindNeeds]; !held {
			t.Errorf("%s sent no needs member: %v", call.action, sent)
		}
		var answered model.Issue
		if err := json.Unmarshal(body, &answered); err != nil {
			t.Fatalf("decode %s's answer: %v", call.action, err)
		}
		if len(answered.Needs) != 1 {
			t.Errorf("%s's issue waits on %v, want the one issue its argument named", call.action, answered.Needs)
		}
	}
	if got := relationEnds(currentRelations(t, database, keys[0], graphstore.TupleNeeds)); !slices.Equal(got, []string{graphstore.KindIssue + ":" + keys[2]}) {
		t.Errorf("the update's issue states needs %v, want [issue:%s]", got, keys[2])
	}

	control := dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+keys[0], map[string]any{"blocked_by": []string{keys[1]}}, "alice")
	requireRelationRefused(t, "the same update sent with blocked_by", control, http.StatusBadRequest, codeBlockedByInput, "needs")
}

// handlerDoer performs a going caller's request on the server's own handler as login, and keeps
// the body of each request it performed.
type handlerDoer struct {
	handler http.Handler
	login   string
	bodies  []map[string]any
}

func (d *handlerDoer) Do(request *http.Request) (*http.Response, error) {
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	d.bodies = append(d.bodies, body)
	served := httptest.NewRequest(request.Method, request.URL.Path, strings.NewReader(string(data)))
	served.Header = request.Header.Clone()
	served.Header.Set("X-Dispatch-User", d.login)
	recorder := httptest.NewRecorder()
	d.handler.ServeHTTP(recorder, served)
	return recorder.Result(), nil
}

// readMembers is the answer of path, member by member.
func readMembers(t *testing.T, handler http.Handler, path string) map[string]json.RawMessage {
	t.Helper()
	response := dispatchRequest(t, handler, http.MethodGet, path, nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s", path, response.Code, response.Body.String())
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &members); err != nil {
		t.Fatalf("decode GET %s: %v", path, err)
	}
	return members
}

// requireNeedsAlone fails unless object answers needs as want and answers no blocked_by.
func requireNeedsAlone(t *testing.T, what string, object map[string]json.RawMessage, want []string) {
	t.Helper()
	if value, held := object[blockedByMember]; held {
		t.Errorf("%s answers blocked_by %s, want no such member", what, value)
	}
	var needs []string
	if err := json.Unmarshal(object[model.EdgeKindNeeds], &needs); err != nil {
		t.Fatalf("%s answers needs %s, which is no list of keys: %v", what, object[model.EdgeKindNeeds], err)
	}
	if !slices.Equal(needs, want) {
		t.Errorf("%s answers needs %v, want %v", what, needs, want)
	}
}

// blockedByKey is an object literal's member named blocked_by, quoted or not.
var blockedByKey = regexp.MustCompile(`(?m)^\s*["']?blocked_by["']?\s*:`)

// blockedByWord is the word blocked_by anywhere: a string literal that reads the event member,
// a key of a table of members, or a comment that names it.
var blockedByWord = regexp.MustCompile(`\bblocked_by\b`)

// requireTheWebFilesHoldNoBlockedBy reads ReferencedBy.tsx's two tables of edge kinds and the whole
// of event-refresh.ts, each after its search finds a blocked_by planted in a scratch copy.
func requireTheWebFilesHoldNoBlockedBy(t *testing.T) {
	web := filepath.Join("..", "..", "..", "..", "dispatch", "web", "src")
	referencedBy := readWebFile(t, filepath.Join(web, "features", "refs", "ReferencedBy.tsx"))
	eventRefresh := readWebFile(t, filepath.Join(web, "api", "event-refresh.ts"))
	scratch := t.TempDir()

	tables := func(text string) []string {
		t.Helper()
		var bodies []string
		for _, name := range []string{"RENDERED_EDGE_KINDS", "STRUCTURAL_LABELS"} {
			body, found := objectLiteral(text, name)
			if !found {
				t.Fatalf("ReferencedBy.tsx declares no object %s", name)
			}
			bodies = append(bodies, name+" "+body)
		}
		return bodies
	}
	holdingKey := func(text string) []string {
		var holding []string
		for _, table := range tables(text) {
			if blockedByKey.MatchString(table) {
				holding = append(holding, strings.Fields(table)[0])
			}
		}
		return holding
	}

	for _, name := range []string{"RENDERED_EDGE_KINDS", "STRUCTURAL_LABELS"} {
		opening := "const " + name
		at := strings.Index(referencedBy, opening)
		brace := strings.Index(referencedBy[at:], "{\n")
		planted := referencedBy[:at+brace+2] + "  blocked_by: true,\n" + referencedBy[at+brace+2:]
		copied := writeScratch(t, scratch, "ReferencedBy-"+name+".tsx", planted)
		if holding := holdingKey(readWebFile(t, copied)); !slices.Contains(holding, name) {
			t.Fatalf("the search finds no blocked_by planted in %s of a scratch copy (%s), so it cannot find one", name, copied)
		}
	}
	if holding := holdingKey(referencedBy); len(holding) != 0 {
		t.Errorf("ReferencedBy.tsx holds a blocked_by row or label in %v", holding)
	}

	planted := strings.Replace(eventRefresh, `payloadStrings(event, "needs")`, `payloadStrings(event, "needs") ?? payloadStrings(event, "blocked_by")`, 1)
	if planted == eventRefresh {
		t.Fatal(`event-refresh.ts holds no payloadStrings(event, "needs") to plant a read of blocked_by beside`)
	}
	copied := writeScratch(t, scratch, "event-refresh.ts", planted)
	if !blockedByWord.MatchString(readWebFile(t, copied)) {
		t.Fatalf("the search finds no blocked_by planted in a scratch copy of event-refresh.ts (%s), so it cannot find one", copied)
	}
	for number, line := range strings.Split(eventRefresh, "\n") {
		if blockedByWord.MatchString(line) {
			t.Errorf("event-refresh.ts:%d reads blocked_by: %s", number+1, strings.TrimSpace(line))
		}
	}
}

// objectLiteral is the text between the braces of the object literal `const name ... = {...}`
// declares in text, and whether text declares one.
func objectLiteral(text, name string) (string, bool) {
	at := strings.Index(text, "const "+name)
	if at < 0 {
		return "", false
	}
	open := strings.Index(text[at:], "{")
	if open < 0 {
		return "", false
	}
	start := at + open
	depth := 0
	for index := start; index < len(text); index++ {
		switch text[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start+1 : index], true
			}
		}
	}
	return "", false
}

func readWebFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeScratch(t *testing.T, dir, name, text string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write the scratch copy %s: %v", path, err)
	}
	return path
}
