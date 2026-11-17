package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/dispatch/api/apicode"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

func init() { addTourSteps("documents", documentsTourSteps) }

// documentsTourSteps are the route tour's steps of the document routes (plan t7): each route, the
// reads with what include= and the filters add, a file's version beside a document's, and the
// review with and without the version it guards.
func documentsTourSteps() []tourStep {
	alice := tourPerson("alice")
	documentMissing := []tourMissing{{Param: "id", Value: uuid.NewString(), Code: apicode.ArtifactNotFound}}
	issueMissing := []tourMissing{{Param: "key", Value: "NOPE-404", Code: apicode.IssueNotFound}}
	projectMissing := []tourMissing{{Param: "key", Value: "NOPE", Code: apicode.ProjectNotFound}}
	onSpec := func(query url.Values, body func(*testing.T, *routeTour, tourIssue) any) func(*testing.T, *routeTour) tourRequest {
		return func(t *testing.T, tour *routeTour) tourRequest {
			issue := tour.issue(t)
			request := tourRequest{Path: map[string]string{"id": issue.Document}, Query: query, As: alice}
			if body != nil {
				request.Body = body(t, tour, issue)
			}
			return request
		}
	}
	onProjectDocument := func(query url.Values, body any) func(*testing.T, *routeTour) tourRequest {
		return func(t *testing.T, tour *routeTour) tourRequest {
			return tourRequest{Path: map[string]string{"id": tour.settledDocument(t)}, Query: query, Body: body, As: alice}
		}
	}
	agentFiles := func(query func(slug string) url.Values) func(*testing.T, *routeTour) tourRequest {
		return func(t *testing.T, tour *routeTour) tourRequest {
			session := "session-tour-files-" + uuid.NewString()[:8]
			file := tour.agentFile(t, session)
			return tourRequest{Path: map[string]string{"session_id": session}, Query: query(file.Slug), As: alice}
		}
	}
	upload := func(name string) tourUpload {
		return tourUpload{
			Fields: map[string]string{"name": name}, Filename: name, ContentType: "text/plain", Content: []byte("A file the tour uploads.\n"),
		}
	}
	return []tourStep{
		{
			Method: http.MethodGet, Pattern: "/agents/{session_id}/artifacts", Status: http.StatusOK,
			Setup: agentFiles(func(string) url.Values { return nil }),
		},
		{
			Method: http.MethodGet, Pattern: "/agents/{session_id}/artifacts", Name: "one file by its slug", Status: http.StatusOK,
			Setup: agentFiles(func(slug string) url.Values { return url.Values{"slug": {slug}} }),
		},
		{
			Method: http.MethodPost, Pattern: "/agents/{session_id}/artifacts", Status: http.StatusCreated,
			Setup: func(*testing.T, *routeTour) tourRequest {
				return tourRequest{
					Path: map[string]string{"session_id": "session-tour-files-" + uuid.NewString()[:8]},
					Body: upload("tour-report.txt"), As: alice,
				}
			},
		},
		{
			Method: http.MethodDelete, Pattern: "/artifacts/{id}", Status: http.StatusNoContent,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				document := emptyDocument(t, tour.handler, tour.issue(t).Key, "Tour scratch notes")
				awaitSettled(t, tour.database, document.ID)
				return tourRequest{Path: map[string]string{"id": document.ID}, As: alice}
			},
			Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}", Status: http.StatusOK,
			Setup: onSpec(nil, nil), Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}", Name: "with its text and its blocks", Status: http.StatusOK,
			Setup: onSpec(url.Values{"include": {"text,blocks"}}, nil), Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}", Name: "a file of an agent's conversation", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				file := tour.agentFile(t, "session-tour-files-"+uuid.NewString()[:8])
				return tourRequest{Path: map[string]string{"id": file.ID}, Query: url.Values{"include": {"text"}}, As: alice}
			},
			Missing: documentMissing,
		},
		{
			Method: http.MethodPatch, Pattern: "/artifacts/{id}", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				document := tour.issueDocument(t, tour.issue(t).Key, "Tour notes.md", "# Notes\n\nWhat the tour saw.\n")
				return tourRequest{Path: map[string]string{"id": document.ID}, Body: map[string]any{"name": "Tour notes, renamed.md"}, As: alice}
			},
			Missing: documentMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/artifacts/{id}/approval-requests", Status: http.StatusCreated,
			Setup: onSpec(nil, func(*testing.T, *routeTour, tourIssue) any {
				return map[string]any{"summary": "The tour asks for the spec's approval."}
			}),
			Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/asks", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				document := tour.document(t)
				tour.documentAsk(t, document)
				return tourRequest{Path: map[string]string{"id": document}, As: alice}
			},
			Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/asks", Name: "the open ones, with the thread and the mark", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				document := tour.document(t)
				tour.documentAsk(t, document)
				return tourRequest{
					Path: map[string]string{"id": document}, Query: url.Values{"state": {"open"}, "include": {"thread,mark"}}, As: alice,
				}
			},
			Missing: documentMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/artifacts/{id}/asks", Status: http.StatusCreated,
			Setup:   onProjectDocument(nil, map[string]any{"question": "Does the tour ask on a document?"}),
			Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/blocks/{block_id}", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				issue := tour.issue(t)
				return tourRequest{Path: map[string]string{"id": issue.Document, "block_id": tour.firstBlock(t, issue.Document)}, As: alice}
			},
			Missing: append([]tourMissing{{Param: "block_id", Value: "no-such-block", Code: apicode.BlockNotFound}}, documentMissing...),
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/comments", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				document := tour.document(t)
				tour.documentComment(t, document)
				return tourRequest{Path: map[string]string{"id": document}, As: alice}
			},
			Missing: documentMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/artifacts/{id}/comments", Status: http.StatusCreated,
			Setup:   onProjectDocument(nil, map[string]any{"body": "A comment the tour leaves on a document."}),
			Missing: documentMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/artifacts/{id}/edits", Status: http.StatusOK,
			Setup: onSpec(nil, func(*testing.T, *routeTour, tourIssue) any {
				return map[string]any{"ops": []map[string]any{{"op": "insert", "after": "end", "markdown": "A line the tour adds."}}}
			}),
			Missing: documentMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/artifacts/{id}/plan-reviews", Status: http.StatusCreated,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				plan := tour.issueDocument(t, tour.issue(t).Key, "plan.md", "# Plan\n\nThe tour's plan.\n")
				return tourRequest{
					Path: map[string]string{"id": plan.ID}, Body: map[string]any{"version": tour.latestVersion(t, plan.ID)}, As: alice,
				}
			},
			Missing: documentMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/artifacts/{id}/rebuilds", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				return tourRequest{Path: map[string]string{"id": tour.unloadableDocument(t)}, Body: map[string]any{}, As: alice}
			},
			Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/references", Status: http.StatusOK,
			Setup: onProjectDocument(nil, nil), Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/references", Name: "what it cites", Status: http.StatusOK,
			Setup: onProjectDocument(url.Values{"direction": {"out"}}, nil), Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/reviews", Status: http.StatusOK,
			Setup: onSpec(nil, nil), Missing: documentMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/artifacts/{id}/reviews", Status: http.StatusCreated,
			Setup: onSpec(nil, func(*testing.T, *routeTour, tourIssue) any {
				return map[string]any{"state": "approved"}
			}),
			Missing: documentMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/artifacts/{id}/reviews", Name: "guarded by the version read", Status: http.StatusCreated,
			Setup: onSpec(nil, func(t *testing.T, tour *routeTour, issue tourIssue) any {
				return map[string]any{"state": "changes_requested", "reason": "The tour wants more.", "version": tour.latestVersion(t, issue.Document)}
			}),
			Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/subscribers", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				document := tour.document(t)
				tour.subscribeToDocument(t, document)
				return tourRequest{Path: map[string]string{"id": document}, As: alice}
			},
			Missing: documentMissing,
		},
		{
			Method: http.MethodDelete, Pattern: "/artifacts/{id}/subscribers/{session_id}", Status: http.StatusNoContent,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				document := tour.settledDocument(t)
				return tourRequest{Path: map[string]string{"id": document, "session_id": tour.subscribeToDocument(t, document)}, As: alice}
			},
			Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/versions", Status: http.StatusOK,
			Setup: onSpec(nil, nil), Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/versions", Name: "newest first, a page of one", Status: http.StatusOK,
			Setup: onSpec(url.Values{"sort": {"-number"}, "limit": {"1"}}, nil), Missing: documentMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/artifacts/{id}/versions", Status: http.StatusCreated,
			Setup: onSpec(nil, func(*testing.T, *routeTour, tourIssue) any {
				return map[string]any{"summary": "The version the tour names."}
			}),
			Missing: documentMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/versions/{number}", Status: http.StatusOK,
			Setup:   tourVersion(nil),
			Missing: append([]tourMissing{{Param: "number", Value: "999", Code: apicode.VersionNotFound}}, documentMissing...),
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/versions/{number}", Name: "its Markdown, downloaded", Status: http.StatusOK,
			Setup:   tourVersion(url.Values{"download": {"true"}}),
			Missing: append([]tourMissing{{Param: "number", Value: "999", Code: apicode.VersionNotFound}}, documentMissing...),
		},
		{
			Method: http.MethodGet, Pattern: "/artifacts/{id}/versions/{number}", Name: "a file's bytes", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				file := tour.issueFile(t, tour.issue(t).Key)
				return tourRequest{Path: map[string]string{"id": file.ID, "number": "1"}, As: alice}
			},
			Missing: append([]tourMissing{{Param: "number", Value: "2", Code: apicode.VersionNotFound}}, documentMissing...),
		},
		{
			Method: http.MethodGet, Pattern: "/issues/{key}/artifacts", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				return tourRequest{Path: map[string]string{"key": tour.issue(t).Key}, As: alice}
			},
			Missing: issueMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/issues/{key}/artifacts", Name: "the spec by its slug, with its text and its blocks", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				issue := tour.issue(t)
				return tourRequest{
					Path:  map[string]string{"key": issue.Key},
					Query: url.Values{"slug": {tour.slugOf(t, issue.Document)}, "include": {"text,blocks"}},
					As:    alice,
				}
			},
			Missing: issueMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/issues/{key}/artifacts", Status: http.StatusCreated,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				return tourRequest{
					Path: map[string]string{"key": tour.issue(t).Key},
					Body: map[string]any{"name": "tour-findings.md", "content": "# Findings\n\nWhat the tour found.\n"}, As: alice,
				}
			},
			Missing: issueMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/issues/{key}/artifacts", Name: "a file", Status: http.StatusCreated,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				return tourRequest{Path: map[string]string{"key": tour.issue(t).Key}, Body: upload("tour-log.txt"), As: alice}
			},
			Missing: issueMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/projects/{key}/artifacts", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				project := tour.project(t)
				createProjectDocument(t, tour.handler, project, "tour-notes.md", "# Notes\n\nA passage the tour reads.")
				return tourRequest{Path: map[string]string{"key": project}, As: alice}
			},
			Missing: projectMissing,
		},
		{
			Method: http.MethodGet, Pattern: "/projects/{key}/artifacts", Name: "its own document by its slug, with its text", Status: http.StatusOK,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				project := tour.project(t)
				document := createProjectDocument(t, tour.handler, project, "tour-notes.md", "# Notes\n\nA passage the tour reads.")
				return tourRequest{
					Path:  map[string]string{"key": project},
					Query: url.Values{"issue": {"none"}, "slug": {document.Slug}, "include": {"text"}},
					As:    alice,
				}
			},
			Missing: projectMissing,
		},
		{
			Method: http.MethodPost, Pattern: "/projects/{key}/artifacts", Status: http.StatusCreated,
			Setup: func(t *testing.T, tour *routeTour) tourRequest {
				return tourRequest{
					Path: map[string]string{"key": tour.project(t)},
					Body: map[string]any{"name": "tour-design.md", "content": "# Design\n\nThe tour's design.\n"}, As: alice,
				}
			},
			Missing: projectMissing,
		},
	}
}

// tourVersion is the setup of a read of the spec's newest version, with query.
func tourVersion(query url.Values) func(*testing.T, *routeTour) tourRequest {
	return func(t *testing.T, tour *routeTour) tourRequest {
		issue := tour.issue(t)
		number := strconv.Itoa(tour.latestVersion(t, issue.Document))
		return tourRequest{Path: map[string]string{"id": issue.Document, "number": number}, Query: query, As: tourPerson("alice")}
	}
}

// settledDocument is a project document (tour.document) that owes no settlement, as an issue's
// spec owes none once tour.issue answers it.
func (tour *routeTour) settledDocument(t *testing.T) string {
	t.Helper()
	document := tour.document(t)
	awaitSettled(t, tour.database, document)
	return document
}

// issueDocument uploads a markdown document named name to the issue key, as alice, answering it
// once it owes no settlement.
func (tour *routeTour) issueDocument(t *testing.T, key, name, content string) model.Artifact {
	t.Helper()
	created := dispatchRequest(t, tour.handler, http.MethodPost, "/api/v1/issues/"+key+"/artifacts", map[string]string{
		"name": name, "content": content,
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("upload %s to %s: %d %s", name, key, created.Code, created.Body.String())
	}
	document := decodeBody[struct {
		Artifact model.Artifact `json:"artifact"`
	}](t, created).Artifact
	awaitSettled(t, tour.database, document.ID)
	return document
}

// issueFile uploads a text file to the issue key, as alice, answering it.
func (tour *routeTour) issueFile(t *testing.T, key string) model.Artifact {
	t.Helper()
	created := multipartRequest(t, tour.handler, "/api/v1/issues/"+key+"/artifacts", map[string]string{"name": "tour-log.txt"},
		"tour-log.txt", "text/plain", []byte("A file the tour keeps.\n"), "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("upload a file to %s: %d %s", key, created.Code, created.Body.String())
	}
	return decodeBody[struct {
		Artifact model.Artifact `json:"artifact"`
	}](t, created).Artifact
}

// agentFile uploads a text file to session's conversation, as alice, answering it.
func (tour *routeTour) agentFile(t *testing.T, session string) model.Artifact {
	t.Helper()
	created := multipartRequest(t, tour.handler, "/api/v1/agents/"+session+"/artifacts", map[string]string{"name": "tour-report.txt"},
		"tour-report.txt", "text/plain", []byte("A report the tour hands an agent.\n"), "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("upload a file to %s: %d %s", session, created.Code, created.Body.String())
	}
	return decodeBody[struct {
		Artifact model.Artifact `json:"artifact"`
	}](t, created).Artifact
}

// documentAsk opens an ask on the project document, as alice, answering its id.
func (tour *routeTour) documentAsk(t *testing.T, document string) string {
	t.Helper()
	created := dispatchRequest(t, tour.handler, http.MethodPost, "/api/v1/artifacts/"+document+"/asks", map[string]any{
		"question": "Which way does the document go?",
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("ask on %s: %d %s", document, created.Code, created.Body.String())
	}
	return decodeBody[struct {
		ID string `json:"id"`
	}](t, created).ID
}

// documentComment comments on the project document, as alice.
func (tour *routeTour) documentComment(t *testing.T, document string) {
	t.Helper()
	created := dispatchRequest(t, tour.handler, http.MethodPost, "/api/v1/artifacts/"+document+"/comments", map[string]any{
		"body": "A comment the tour reads back.",
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("comment on %s: %d %s", document, created.Code, created.Body.String())
	}
}

// latestVersion is the number of the artifact's newest version.
func (tour *routeTour) latestVersion(t *testing.T, artifact string) int {
	t.Helper()
	var number int
	if err := tour.database.Pool.QueryRow(t.Context(), `select max(number) from artifact_versions where artifact_id = $1`, artifact).Scan(&number); err != nil {
		t.Fatalf("read the newest version of %s: %v", artifact, err)
	}
	return number
}

// slugOf is the artifact's slug.
func (tour *routeTour) slugOf(t *testing.T, artifact string) string {
	t.Helper()
	var slug string
	if err := tour.database.Pool.QueryRow(t.Context(), `select slug from artifacts where id = $1`, artifact).Scan(&slug); err != nil {
		t.Fatalf("read the slug of %s: %v", artifact, err)
	}
	return slug
}

// firstBlock is the id of the document's first block, as GET /api/v1/artifacts/{id}/blocks lists it.
func (tour *routeTour) firstBlock(t *testing.T, document string) string {
	t.Helper()
	read := dispatchRequest(t, tour.handler, http.MethodGet, "/api/v1/artifacts/"+document+"/blocks", nil, "alice")
	if read.Code != http.StatusOK {
		t.Fatalf("read the blocks of %s: %d %s", document, read.Code, read.Body.String())
	}
	blocks := decodeBody[[]model.ArtifactBlock](t, read)
	if len(blocks) == 0 {
		t.Fatalf("the document %s holds no block", document)
	}
	return blocks[0].ID
}

// subscribeToDocument tells the listener a session of its own follows the project document's
// topics, answering the session.
func (tour *routeTour) subscribeToDocument(t *testing.T, document string) string {
	t.Helper()
	var project, slug string
	if err := tour.database.Pool.QueryRow(t.Context(), `select project_key, slug from artifacts where id = $1`, document).Scan(&project, &slug); err != nil {
		t.Fatalf("read the document %s: %v", document, err)
	}
	session := "session-tour-subscriber-" + uuid.NewString()[:8]
	tour.listener.mu.Lock()
	tour.listener.interests[session] = []string{documentSubscriberBase(project, slug) + ".>"}
	tour.listener.mu.Unlock()
	return session
}

// unloadableDocument is a project document whose stored history no longer decodes, with no room
// holding it and no settlement owed: the document a rebuild is for (makeDocumentUnloadable).
func (tour *routeTour) unloadableDocument(t *testing.T) string {
	t.Helper()
	document := tour.document(t)
	awaitSettled(t, tour.database, document)
	ctx := context.Background()
	if err := tour.served.deps.Docs.Evict(ctx, document); err != nil {
		t.Fatalf("close the document's room: %v", err)
	}
	seedExec(t, tour.database, "the document's owed settlement cleared", `delete from doc_settlements_pending where artifact_id = $1`, document)
	corrupted, err := tour.database.Pool.Exec(ctx, `update doc_updates set update = '\xff'::bytea where artifact_id = $1`, document)
	if err != nil || corrupted.RowsAffected() == 0 {
		t.Fatalf("corrupt the document's history: %d rows, %v", corrupted.RowsAffected(), err)
	}
	return document
}

// sameDocumentRow reports what differs between a /api/v1 read of an artifact, v1 as that read
// answers it, and the /api/v2 row of the same artifact: every member of v1 but versions, which the
// /api/v2 row answers as latest_version, the number of the newest of them. members are the members
// of v1 to compare, every one where nil.
func sameDocumentRow(t *testing.T, v1, v2 map[string]json.RawMessage, members []string) {
	t.Helper()
	var versions []model.Version
	if raw, held := v1["versions"]; held {
		if err := json.Unmarshal(raw, &versions); err != nil {
			t.Fatalf("read the /api/v1 versions: %v", err)
		}
		latest := 0
		for _, version := range versions {
			latest = max(latest, version.Number)
		}
		if got := string(v2["latest_version"]); got != strconv.Itoa(latest) {
			t.Errorf("the /api/v2 row's latest_version is %s, want %d, the newest of the /api/v1 read's versions", got, latest)
		}
		if _, still := v2["versions"]; still {
			t.Errorf("the /api/v2 row carries versions[], which GET /api/v2/artifacts/{id}/versions lists")
		}
	}
	if members == nil {
		members = make([]string, 0, len(v1))
		for member := range maps.Keys(v1) {
			if member != "versions" {
				members = append(members, member)
			}
		}
	}
	for _, member := range members {
		var want, got any
		_ = json.Unmarshal(v1[member], &want)
		_ = json.Unmarshal(v2[member], &got)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("member %s: the /api/v2 row answers %s, the /api/v1 read %s", member, v2[member], v1[member])
		}
	}
}

// TestEachV2ArtifactListAnswersTheDocumentItsV1SlugReadAnswers is plan t7's slug acceptance: each
// /api/v1 read by slug - an issue's document by its slug and by its exact file name, a project's
// own, a conversation's file, and each one's text, blocks, summary and version - answers the
// document the /api/v2 list filtered by that slug answers, as one row, with what include= adds;
// and a project's slug, which never reached an issue's document of the project, matches none
// there under issue=none either.
func TestEachV2ArtifactListAnswersTheDocumentItsV1SlugReadAnswers(t *testing.T) {
	tour := newRouteTour(t)
	get := func(t *testing.T, target string) []byte {
		t.Helper()
		response := dispatchRequest(t, tour.handler, http.MethodGet, target, nil, "alice")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", target, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	object := func(t *testing.T, body []byte) map[string]json.RawMessage {
		t.Helper()
		var members map[string]json.RawMessage
		if err := json.Unmarshal(body, &members); err != nil {
			t.Fatalf("read %s: %v", body, err)
		}
		return members
	}
	only := func(t *testing.T, target string) map[string]json.RawMessage {
		t.Helper()
		listed := decodeBody[page[json.RawMessage]](t, dispatchRequest(t, tour.handler, http.MethodGet, target, nil, "alice"))
		if listed.Total != 1 || len(listed.Items) != 1 {
			t.Fatalf("GET %s answered %d of %d rows, want the one document", target, len(listed.Items), listed.Total)
		}
		return object(t, listed.Items[0])
	}

	issue := tour.issue(t)
	notes := tour.issueDocument(t, issue.Key, "Tour Notes.md", "# Notes\n\n- one\n- two\n")
	awaitSettled(t, tour.database, issue.Document, notes.ID)
	v1Issue, v2Issue := "/api/v1/issues/"+issue.Key+"/artifacts/", "/api/v2/issues/"+issue.Key+"/artifacts?"

	t.Run("an issue's document by its slug and by its file name", func(t *testing.T) {
		for _, reference := range []string{notes.Slug, notes.Name} {
			v1 := object(t, get(t, v1Issue+url.PathEscape(reference)))
			sameDocumentRow(t, v1, only(t, v2Issue+url.Values{"slug": {reference}}.Encode()), nil)
		}
	})

	t.Run("its text and its blocks from one tree", func(t *testing.T) {
		text := object(t, get(t, v1Issue+notes.Slug+"/text?blocks=true"))
		row := only(t, v2Issue+url.Values{"slug": {notes.Slug}, "include": {"text,blocks"}}.Encode())
		sameDocumentRow(t, text, row, []string{"markdown", "token", "version", "blocks"})
		blocks := map[string]json.RawMessage{"blocks": get(t, v1Issue+notes.Slug+"/blocks")}
		sameDocumentRow(t, blocks, only(t, v2Issue+url.Values{"slug": {notes.Slug}, "include": {"blocks"}}.Encode()), []string{"blocks"})
		if _, carried := row["approval"]; !carried {
			t.Errorf("the /api/v2 row of a document carries no approval: %v", row)
		}
	})

	t.Run("its version", func(t *testing.T) {
		v1 := get(t, v1Issue+notes.Slug+"/versions/1")
		if v2 := get(t, "/api/v2/artifacts/"+notes.ID+"/versions/1"); string(v1) != string(v2) {
			t.Fatalf("version 1 by slug answered %s, and by id under /api/v2 %s", v1, v2)
		}
	})

	project := tour.project(t)
	design := createProjectDocument(t, tour.handler, project, "Tour design.md", "# Design\n\nA passage.\n")
	awaitSettled(t, tour.database, design.ID)
	v1Project, v2Project := "/api/v1/projects/"+project+"/artifacts/", "/api/v2/projects/"+project+"/artifacts?issue=none&"

	t.Run("a project's own document, its summary, its text, its blocks and its version", func(t *testing.T) {
		for _, reference := range []string{design.Slug, design.Name} {
			v1 := object(t, get(t, v1Project+url.PathEscape(reference)))
			sameDocumentRow(t, v1, only(t, v2Project+url.Values{"slug": {reference}}.Encode()), nil)
		}
		summary := object(t, get(t, v1Project+design.Slug+"/summary"))
		sameDocumentRow(t, summary, only(t, v2Project+url.Values{"slug": {design.Slug}}.Encode()), nil)
		text := object(t, get(t, v1Project+design.Slug+"/text"))
		sameDocumentRow(t, text, only(t, v2Project+url.Values{"slug": {design.Slug}, "include": {"text"}}.Encode()),
			[]string{"markdown", "token", "version"})
		blocks := map[string]json.RawMessage{"blocks": get(t, v1Project+design.Slug+"/blocks")}
		sameDocumentRow(t, blocks, only(t, v2Project+url.Values{"slug": {design.Slug}, "include": {"blocks"}}.Encode()), []string{"blocks"})
		if v1, v2 := get(t, v1Project+design.Slug+"/versions/1"), get(t, "/api/v2/artifacts/"+design.ID+"/versions/1"); string(v1) != string(v2) {
			t.Fatalf("version 1 by slug answered %s, and by id under /api/v2 %s", v1, v2)
		}
	})

	t.Run("a project's slug reaches none of its issues' documents", func(t *testing.T) {
		inProject := tour.issue(t)
		key := inProject.Key
		var owner string
		if err := tour.database.Pool.QueryRow(t.Context(), `select project_key from issues where key = $1`, key).Scan(&owner); err != nil {
			t.Fatalf("read the issue's project: %v", err)
		}
		spec := tour.slugOf(t, inProject.Document)
		plan := tour.issueDocument(t, key, "plan.md", "# Plan\n")
		refused := dispatchRequest(t, tour.handler, http.MethodGet, "/api/v1/projects/"+owner+"/artifacts/"+plan.Slug, nil, "alice")
		if code, _ := refusalOf(t, refused); refused.Code != http.StatusNotFound || code != "ARTIFACT_NOT_FOUND" {
			t.Fatalf("the /api/v1 project read of an issue's document: %d %s, want 404 ARTIFACT_NOT_FOUND", refused.Code, refused.Body.String())
		}
		none := decodeBody[page[json.RawMessage]](t, dispatchRequest(t, tour.handler, http.MethodGet,
			"/api/v2/projects/"+owner+"/artifacts?"+url.Values{"issue": {"none"}, "slug": {plan.Slug}}.Encode(), nil, "alice"))
		if none.Total != 0 || len(none.Items) != 0 {
			t.Fatalf("issue=none&slug=%s answered %d rows, want none: the document is an issue's", plan.Slug, none.Total)
		}
		ofIssue := decodeBody[page[json.RawMessage]](t, dispatchRequest(t, tour.handler, http.MethodGet,
			"/api/v2/projects/"+owner+"/artifacts?"+url.Values{"issue": {key}, "slug": {plan.Slug}}.Encode(), nil, "alice"))
		if ofIssue.Total != 1 {
			t.Fatalf("issue=%s&slug=%s answered %d rows, want the issue's plan", key, plan.Slug, ofIssue.Total)
		}
		specs := decodeBody[page[json.RawMessage]](t, dispatchRequest(t, tour.handler, http.MethodGet,
			"/api/v2/projects/"+owner+"/artifacts?"+url.Values{"slug": {spec}}.Encode(), nil, "alice"))
		if specs.Total != 0 {
			t.Fatalf("slug=%s answered %d rows of the project's list, which leaves out every issue's spec", spec, specs.Total)
		}
	})

	t.Run("a conversation's file and its version", func(t *testing.T) {
		session := "session-tour-files-" + uuid.NewString()[:8]
		file := tour.agentFile(t, session)
		v1 := object(t, get(t, "/api/v1/agents/"+session+"/artifacts/"+file.Slug))
		sameDocumentRow(t, v1, only(t, "/api/v2/agents/"+session+"/artifacts?"+url.Values{"slug": {file.Slug}}.Encode()), nil)
		if v1, v2 := get(t, "/api/v1/agents/"+session+"/artifacts/"+file.Slug+"/versions/1"), get(t, "/api/v2/artifacts/"+file.ID+"/versions/1"); string(v1) != string(v2) {
			t.Fatalf("the file's version 1 by slug answered %q, and by id under /api/v2 %q", v1, v2)
		}
	})
}
