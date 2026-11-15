package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/dispatch/files/filestest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// listedSession is the conversation the authorization rows list, holding one file written
// straight to its table, and listedFile that file's name.
const (
	listedSession = "s-listed-files"
	listedFile    = "photo.png"
)

// listedFilesPath is GET /api/v1/agents/{session_id}/artifacts for one session.
func listedFilesPath(session string) string {
	return "/api/v1/agents/" + session + "/artifacts"
}

// seedListedFile writes listedSession's one file, with no version: the authorization rows
// compare who is answered, not what a version holds.
func seedListedFile(t *testing.T, database *store.Store) {
	t.Helper()
	if _, err := database.Pool.Exec(context.Background(), `
		insert into artifacts (session_id, slug, name, kind, created_by)
		values ($1, 'photo-png', $2, 'image', '{"kind":"user","id":"alice"}')
	`, listedSession, listedFile); err != nil {
		t.Fatalf("write the conversation's file: %v", err)
	}
}

// A conversation's list is every file and image it holds, newest first by the upload that made
// each, with its versions and the fields a reference to it is built from, and nothing another
// conversation holds: a later version of an older file leaves that file where it was. A
// conversation holding none is answered an empty list, and a session id no reference could carry
// is refused as the conversation's other artifact routes refuse it.
func TestAnAgentsConversationListsItsFilesNewestFirst(t *testing.T) {
	handler, _, _ := newTestServer(t, testServerOptions{files: filestest.NewMemory()})
	const other = "01a1058e-f14f-7684-87eb-3dc885955552"
	upload := func(session, name, contentType string, content []byte) {
		t.Helper()
		response := multipartRequest(t, handler, listedFilesPath(session), map[string]string{"name": name},
			name, contentType, content, "alice")
		if response.Code != http.StatusCreated {
			t.Fatalf("upload %s to %s: status=%d body=%s", name, session, response.Code, response.Body.String())
		}
	}
	upload(agentSession, "shot.png", "image/png", pngBytes("first"))
	upload(agentSession, "notes.txt", "text/plain", []byte("plain"))
	upload(agentSession, "shot.png", "image/png", pngBytes("second"))
	upload(other, "theirs.png", "image/png", pngBytes("theirs"))

	list := func(session string) []model.Artifact {
		t.Helper()
		response := dispatchRequest(t, handler, http.MethodGet, listedFilesPath(session), nil, "alice")
		if response.Code != http.StatusOK {
			t.Fatalf("list %s: status=%d body=%s", session, response.Code, response.Body.String())
		}
		return decodeBody[[]model.Artifact](t, response)
	}

	type listed struct {
		slug, name, kind string
		versions         int
	}
	for session, want := range map[string][]listed{
		agentSession: {{"notes-txt", "notes.txt", "file", 1}, {"shot-png", "shot.png", "image", 2}},
		other:        {{"theirs-png", "theirs.png", "image", 1}},
	} {
		got := list(session)
		if len(got) != len(want) {
			t.Fatalf("%s lists %d files, want %d: %+v", session, len(got), len(want), got)
		}
		for index, artifact := range got {
			expected := want[index]
			if artifact.Slug != expected.slug || artifact.Name != expected.name || artifact.Kind != expected.kind {
				t.Fatalf("%s's file %d = %s %q %s, want %s %q %s", session, index,
					artifact.Slug, artifact.Name, artifact.Kind, expected.slug, expected.name, expected.kind)
			}
			if artifact.SessionID == nil || *artifact.SessionID != session || artifact.IssueKey != nil ||
				artifact.RefKey != agentOwnerKey(session)+"/"+expected.slug {
				t.Fatalf("%s's file %s is owned by session %v, issue %v, at %s; want the conversation alone",
					session, expected.slug, artifact.SessionID, artifact.IssueKey, artifact.RefKey)
			}
			if len(artifact.Versions) != expected.versions ||
				artifact.Versions[len(artifact.Versions)-1].Number != expected.versions {
				t.Fatalf("%s's file %s carries versions %+v, want 1 to %d", session, expected.slug,
					artifact.Versions, expected.versions)
			}
		}
	}

	empty := dispatchRequest(t, handler, http.MethodGet, listedFilesPath("s-holds-nothing"), nil, "alice")
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("a conversation with no file: status=%d body=%s, want 200 []", empty.Code, empty.Body.String())
	}
	invalid := dispatchRequest(t, handler, http.MethodGet, listedFilesPath("s%20one"), nil, "alice")
	if invalid.Code != http.StatusBadRequest || decodeBody[refusalAnswer](t, invalid).Code != "INVALID_SESSION_ID" {
		t.Fatalf("a session id holding a space: status=%d body=%s, want 400 INVALID_SESSION_ID",
			invalid.Code, invalid.Body.String())
	}
}

// A conversation's list answers its newest agentArtifactsListMax files and no more: one file
// past the cap leaves the oldest out of the answer, and the rest come newest first. The files
// are written straight to the table one second apart, so the newest is the one written last.
func TestAnAgentsConversationListsOnlyItsNewestFiles(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	const session = "s-many-files"
	held := agentArtifactsListMax + 1
	if _, err := database.Pool.Exec(context.Background(), `
		insert into artifacts (session_id, slug, name, kind, created_by, created_at)
		select $1, 'file-' || n, 'file-' || n || '.png', 'image', '{"kind":"user","id":"alice"}',
			now() - make_interval(secs => $2::int - n)
		from generate_series(1, $2::int) as n
	`, session, held); err != nil {
		t.Fatalf("write the conversation's %d files: %v", held, err)
	}
	response := dispatchRequest(t, handler, http.MethodGet, listedFilesPath(session), nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("list %s: status=%d body=%s", session, response.Code, response.Body.String())
	}
	got := decodeBody[[]model.Artifact](t, response)
	if len(got) != agentArtifactsListMax {
		t.Fatalf("a conversation holding %d files lists %d, want the newest %d", held, len(got), agentArtifactsListMax)
	}
	for index, artifact := range got {
		if want := fmt.Sprintf("file-%d", held-index); artifact.Slug != want {
			t.Fatalf("file %d of the list is %s, want %s: the list is the newest files, newest first", index, artifact.Slug, want)
		}
	}
}

// A conversation's files are part of the conversation, so every kind of caller GET
// /api/v1/agents/{session_id}/messages tells apart is answered by the list exactly as that read
// answers: no credential, a person's browser, a person's own token with and without the person
// scope, the deployment's shared token, and the broker's tokens for a person, for one project, for
// a service with no person, and for a machine login. Both read `session` as every person route
// does, so the broker's token for a person, which proved a session, naming another there is
// refused both, 403 ACTOR_SESSION_MISMATCH.
func TestAgentFileListAdmitsExactlyTheCallersTheConversationReadAdmits(t *testing.T) {
	rig := newBrokerTokenRig(t)
	seedListedFile(t, rig.database)
	caller := newBrokerCaller(t)

	broker := func(name string, says brokertoken.Introspection) string {
		token := "a-broker-token-" + name
		rig.broker.Says(token, says)
		return token
	}
	personScoped := liveNamedToken(caller.jkt)
	personScoped.ProjectScope, personScoped.Project = brokertoken.ProjectScopePerson, ""
	brokerTokens := map[string]string{
		"a broker token for a person":        broker("person", personScoped),
		"a broker token for one project":     broker("project", liveNamedToken(caller.jkt)),
		"a broker token for a service":       broker("service", serviceOnlyToken(caller.jkt)),
		"a broker token for a machine login": broker("machine", machineLoginToken(caller.jkt)),
	}
	bearers := map[string]string{
		"the deployment's shared token":             "agent-token",
		"a person's token holding the person scope": personalTokenHolding(t, rig.database, model.ScopePerson),
		"a person's token holding no scope":         personalTokenHolding(t, rig.database),
	}

	listed := listedFilesPath(listedSession)
	conversation := "/api/v1/agents/" + listedSession + "/messages"
	type answer struct {
		status int
		code   string
	}
	answerOf := func(response *httptest.ResponseRecorder) answer {
		if response.Code == http.StatusOK {
			return answer{status: http.StatusOK}
		}
		return answer{status: response.Code, code: decodeBody[refusalAnswer](t, response).Code}
	}
	plain := func(path string, set func(*http.Request)) answer {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		set(request)
		return answerOf(rig.serve(request))
	}

	answers := map[string][2]answer{}
	answers["no credential"] = [2]answer{
		plain(conversation, func(*http.Request) {}), plain(listed, func(*http.Request) {}),
	}
	browser := func(request *http.Request) { request.Header.Set("X-Dispatch-User", "alice") }
	answers["a person's browser"] = [2]answer{plain(conversation, browser), plain(listed, browser)}
	for name, token := range bearers {
		bearer := func(request *http.Request) { request.Header.Set("Authorization", "Bearer "+token) }
		answers[name] = [2]answer{plain(conversation, bearer), plain(listed, bearer)}
	}
	for name, token := range brokerTokens {
		answers[name] = [2]answer{
			answerOf(rig.serve(caller.presenting(t, http.MethodGet, conversation, token, nil))),
			answerOf(rig.serve(caller.presenting(t, http.MethodGet, listed, token, nil))),
		}
	}
	mismatched := "a broker token for a person, naming another session"
	answers[mismatched] = [2]answer{
		answerOf(rig.serve(caller.presenting(t, http.MethodGet, conversation+"?session=s-another",
			brokerTokens["a broker token for a person"], nil))),
		answerOf(rig.serve(caller.presenting(t, http.MethodGet, listed+"?session=s-another",
			brokerTokens["a broker token for a person"], nil))),
	}
	if refused := (answer{status: http.StatusForbidden, code: "ACTOR_SESSION_MISMATCH"}); answers[mismatched][1] != refused {
		t.Errorf("%s: the file list answers %+v, want %+v", mismatched, answers[mismatched][1], refused)
	}

	admitted := 0
	for name, pair := range answers {
		if pair[0] != pair[1] {
			t.Errorf("%s: the conversation read answers %+v and the file list %+v, want the same", name, pair[0], pair[1])
		}
		if pair[0].status == http.StatusOK {
			admitted++
		}
	}
	// The comparison proves nothing if every caller is refused both reads, or admitted to both.
	if admitted == 0 || admitted == len(answers) {
		t.Fatalf("the conversation read admits %d of %d callers, want some admitted and some refused: %+v", admitted, len(answers), answers)
	}
}

// A caller the conversation read refuses learns nothing of the conversation's files either: the
// deployment's shared token and a person's token holding no scope are refused the list for the
// scope they lack, 403 OAUTH_SCOPE_REQUIRED, and the refusal names no file, while the person's
// browser is answered the file.
func TestAgentFileListRefusesACallerTheConversationReadRefuses(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	seedListedFile(t, database)

	for name, token := range map[string]string{
		"the deployment's shared token":     "agent-token",
		"a person's token holding no scope": personalTokenHolding(t, database),
	} {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{"/api/v1/agents/" + listedSession + "/messages", listedFilesPath(listedSession)} {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.Header.Set("Authorization", "Bearer "+token)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusForbidden || decodeBody[refusalAnswer](t, response).Code != codeScopeRequired {
					t.Fatalf("GET %s: status=%d body=%s, want 403 %s", path, response.Code, response.Body.String(), codeScopeRequired)
				}
				if strings.Contains(response.Body.String(), listedFile) {
					t.Fatalf("GET %s refused the caller and named the file: %s", path, response.Body.String())
				}
			}
		})
	}

	admitted := dispatchRequest(t, handler, http.MethodGet, listedFilesPath(listedSession), nil, "alice")
	if admitted.Code != http.StatusOK {
		t.Fatalf("the person's browser: status=%d body=%s, want 200", admitted.Code, admitted.Body.String())
	}
	if got := decodeBody[[]model.Artifact](t, admitted); len(got) != 1 || got[0].Name != listedFile {
		t.Fatalf("the person's browser is answered %+v, want %s alone", got, listedFile)
	}
}
