package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reearth/ygo/crdt"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/processes"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// holdIssuesUnconverted makes a process that lists no issue_room live beside the test's server for
// the rest of the test - a server_processes row with no capabilities, as a build before the column
// registers none - so every issue the test creates is created as before the issue room, one room
// per document (decision 7 of dispatch://INFRA-1253's plan). The process registers this build's
// schema, so it moves no other rule the fleet decides: the ask roll window, which a process of an
// older schema opens (docs' anotherBuildServes), stays shut as with this server alone.
func holdIssuesUnconverted(t *testing.T, database *store.Store) {
	t.Helper()
	if _, err := database.Pool.Exec(context.Background(), `
		insert into server_processes (instance, signing_keys, schema_version, schema_creatable, heartbeat_at)
		values (gen_random_uuid(), '{fingerprint}', $1, $2, now() + interval '1 day')`,
		pmdoc.SchemaVersion(), pmdoc.SchemaCreatable()); err != nil {
		t.Fatalf("add a live server process that lists no issue room: %v", err)
	}
}

// createRoomIssue creates, as alice, an issue of project with spec, and answers it with its tree
// artifact's id, "" where it holds none.
func createRoomIssue(t *testing.T, handler http.Handler, database *store.Store, project, title, spec, parent string) (model.Issue, string) {
	t.Helper()
	body := map[string]any{"project": project, "title": title, "spec": spec, "force": true}
	if parent != "" {
		body["parent"] = parent
	}
	response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", body, "alice")
	if response.Code != http.StatusCreated {
		t.Fatalf("create issue %q: status=%d body=%s", title, response.Code, response.Body.String())
	}
	issue := decodeBody[model.Issue](t, response)
	tree, err := issueTreeOf(context.Background(), database.Pool, issue.Key)
	if err != nil {
		t.Fatalf("read the tree of %s: %v", issue.Key, err)
	}
	return issue, tree
}

// createRoomProject creates project as alice.
func createRoomProject(t *testing.T, handler http.Handler, project string) {
	t.Helper()
	if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects", map[string]string{"key": project, "name": project}, "alice"); response.Code != http.StatusCreated {
		t.Fatalf("create project %s: status=%d body=%s", project, response.Code, response.Body.String())
	}
}

// artifactTextOf reads GET /api/v1/artifacts/{id}/text's markdown.
func artifactTextOf(t *testing.T, handler http.Handler, id string) string {
	t.Helper()
	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/artifacts/"+id+"/text", nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read the text of %s: status=%d body=%s", id, response.Code, response.Body.String())
	}
	return decodeBody[artifactText](t, response).Markdown
}

// documentRoomOf is the room artifactID's document is edited in (docs.DocumentRoomOfStatement) and
// the document's fragment of that room, the room and fragment the artifact read answers
// (model.Artifact.Room, Fragment): a test that reads a document's stored history reads its room's,
// and renders the document through its own fragment.
func documentRoomOf(t *testing.T, q queryer, artifactID string) (room, fragment string) {
	t.Helper()
	if err := q.QueryRow(context.Background(), docs.DocumentRoomOfStatement, artifactID).Scan(&room); err != nil {
		t.Fatalf("read the room of %s: %v", artifactID, err)
	}
	if room == artifactID {
		return room, model.ArtifactFragmentOwn
	}
	return room, artifactID
}

// A criterion written through POST /api/v1/artifacts/{tree}/edits on a converted issue's tree
// artifact lands in the tree's own fragment of the issue's room and reads back through
// GET /api/v1/artifacts/{tree}/text, the tree artifact reads as a document whose room is itself
// and whose fragment is the room's own, and the issue read's documents name no tree artifact (R8 of
// dispatch://INFRA-1253, T12b of its plan). The read of the criterion through the tree read
// (GET /api/v1/issues/{key}/tree) is T16a's, which adds that half of this test when it lands.
func TestACriterionWrittenThroughEditsOnTheTreeArtifactReadsBackInTheTreeRead(t *testing.T) {
	handler, database, _ := newTestServer(t, testServerOptions{})
	createRoomProject(t, handler, "TREE")
	issue, tree := createRoomIssue(t, handler, database, "TREE", "An issue with a tree", "The spec's words.\n", "")
	if tree == "" {
		t.Fatalf("issue %s was created with no tree while no live process lacks the issue room", issue.Key)
	}

	const criterion = ":::criterion{#k1}\nI want the urgent asks first.\n:::\n"
	edited := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+tree+"/edits", map[string]any{
		"ops": []map[string]any{{"op": "insert", "after": "end", "markdown": criterion}},
	}, "alice")
	if edited.Code != http.StatusOK {
		t.Fatalf("write a criterion into the tree: status=%d body=%s", edited.Code, edited.Body.String())
	}
	if text := artifactTextOf(t, handler, tree); !strings.Contains(text, "I want the urgent asks first.") {
		t.Errorf("the tree's text reads %q, want the criterion it was written", text)
	}
	if text := artifactTextOf(t, handler, issue.PrimaryArtifactID); strings.Contains(text, "urgent asks") || !strings.Contains(text, "The spec's words.") {
		t.Errorf("the spec's text reads %q, want its own words and none of the tree's", text)
	}

	read := dispatchRequest(t, handler, http.MethodGet, "/api/v1/artifacts/"+tree, nil, "alice")
	if read.Code != http.StatusOK {
		t.Fatalf("read the tree artifact: status=%d body=%s", read.Code, read.Body.String())
	}
	if artifact := decodeBody[model.Artifact](t, read); artifact.Kind != "tree" || artifact.Room != tree || artifact.Fragment != model.ArtifactFragmentOwn {
		t.Errorf("the tree reads kind %q room %q fragment %q, want kind tree, room %s, fragment %s", artifact.Kind, artifact.Room, artifact.Fragment, tree, model.ArtifactFragmentOwn)
	}

	detail := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue.Key, nil, "alice")
	if detail.Code != http.StatusOK {
		t.Fatalf("read the issue: status=%d body=%s", detail.Code, detail.Body.String())
	}
	documents := decodeBody[struct {
		Artifacts []model.Artifact `json:"artifacts"`
	}](t, detail).Artifacts
	for _, document := range documents {
		if document.Kind == "tree" || document.ID == tree {
			t.Errorf("the issue read's documents name the tree artifact %s", document.ID)
		}
		if document.ID == issue.PrimaryArtifactID && (document.Room != tree || document.Fragment != document.ID) {
			t.Errorf("the spec reads room %q fragment %q, want the issue's room %s and its own fragment %s", document.Room, document.Fragment, tree, document.ID)
		}
	}
}

// An issue created while no live process lacks the issue room is born converted: its spec has its
// document_rooms row naming the issue's tree from its first write, which is stored in the room,
// none under the spec's own id; the spec is a query node of query spec; and the issue holds its
// decisions query node, a document with an empty version 1 whose role reads decisions, which
// INFRA-1150's decisions step does not select (decisions_documents()). One created while a process
// of the release before is live is created as today, with no tree and no document_rooms row
// (decision 7 of dispatch://INFRA-1253's plan).
func TestAnIssueIsBornConvertedOnlyWhileEveryLiveProcessListsTheIssueRoom(t *testing.T) {
	ctx := context.Background()
	handler, database, _ := newTestServer(t, testServerOptions{})
	createRoomProject(t, handler, "BORN")
	// A live process of this build, which lists the issue room as a server registers it
	// (processes.Register), so the create reads a fleet that serves the room rather than an empty one.
	if _, err := database.Pool.Exec(ctx, `
		insert into server_processes (instance, signing_keys, schema_version, schema_creatable, heartbeat_at, capabilities)
		values (gen_random_uuid(), '{fingerprint}', $1, $2, now() + interval '1 day', $3::text[])`,
		pmdoc.SchemaVersion(), pmdoc.SchemaCreatable(), processes.Capabilities); err != nil {
		t.Fatalf("add a live server process that lists the issue room: %v", err)
	}
	converted, tree := createRoomIssue(t, handler, database, "BORN", "Born converted", "The first spec.\n", "")
	if tree == "" {
		t.Fatalf("issue %s holds no tree", converted.Key)
	}
	var room *string
	var ownUpdates, roomUpdates int
	if err := database.Pool.QueryRow(ctx, `
		select (select room::text from document_rooms where artifact_id = $1::uuid),
		       (select count(*) from doc_updates where artifact_id = $1::uuid),
		       (select count(*) from doc_updates where artifact_id = $2::uuid)`,
		converted.PrimaryArtifactID, tree).Scan(&room, &ownUpdates, &roomUpdates); err != nil {
		t.Fatalf("read the spec's room: %v", err)
	}
	if room == nil || *room != tree || ownUpdates != 0 || roomUpdates == 0 {
		t.Errorf("the spec's room is %v with %d updates of its own and the tree %d, want %s and every update in the room", room, ownUpdates, roomUpdates, tree)
	}
	if text := artifactTextOf(t, handler, converted.PrimaryArtifactID); !strings.Contains(text, "The first spec.") {
		t.Errorf("the spec reads %q, want its first text", text)
	}
	specKey := converted.Key + "/spec"
	if query, held, err := graphstore.CurrentValue(ctx, database.Pool, graphstore.KindArtifact, specKey, graphstore.AttributeQuery); err != nil || !held || query != graphstore.QuerySpec {
		t.Errorf("the spec's node states query %q (held %t, %v), want %s", query, held, err, graphstore.QuerySpec)
	}
	var decisions, decisionsMarkdown string
	var decisionsRole *string
	if err := database.Pool.QueryRow(ctx, `
		select a.id::text, a.role, v.markdown from artifacts a join artifact_versions v on v.artifact_id = a.id and v.number = 1
		where a.issue_key = $1 and a.slug = $2`, converted.Key, issueDecisionsName).Scan(&decisions, &decisionsRole, &decisionsMarkdown); err != nil {
		t.Fatalf("read the decisions query node: %v", err)
	}
	if decisionsRole != nil || decisionsMarkdown != "" {
		t.Errorf("the decisions query's row holds role %v and version 1 %q, want no role and an empty version", decisionsRole, decisionsMarkdown)
	}
	readDecisions := dispatchRequest(t, handler, http.MethodGet, "/api/v1/artifacts/"+decisions, nil, "alice")
	if artifact := decodeBody[model.Artifact](t, readDecisions); artifact.Role != graphstore.QueryDecisions || artifact.Room != decisions || artifact.Fragment != model.ArtifactFragmentOwn {
		t.Errorf("the decisions query reads role %q room %q fragment %q, want role decisions, its own room and fragment", artifact.Role, artifact.Room, artifact.Fragment)
	}
	var selected int
	if err := database.Pool.QueryRow(ctx, `select count(*) from decisions_documents(include_retired => true) where issue_key = $1`, converted.Key).Scan(&selected); err != nil {
		t.Fatalf("read the decisions documents: %v", err)
	}
	if selected != 0 {
		t.Errorf("the decisions step selects %d documents of %s, want none", selected, converted.Key)
	}

	holdIssuesUnconverted(t, database)
	unconverted, unconvertedTree := createRoomIssue(t, handler, database, "BORN", "Created as before", "The second spec.\n", "")
	var rooms, ownSeed int
	if err := database.Pool.QueryRow(ctx, `
		select (select count(*) from document_rooms d join artifacts a on a.id = d.artifact_id where a.issue_key = $1),
		       (select count(*) from doc_updates where artifact_id = $2::uuid)`, unconverted.Key, unconverted.PrimaryArtifactID).Scan(&rooms, &ownSeed); err != nil {
		t.Fatalf("read the unconverted issue's rooms: %v", err)
	}
	if unconvertedTree != "" || rooms != 0 || ownSeed == 0 {
		t.Errorf("issue %s created beside a process of the release before holds tree %q, %d document_rooms rows and %d updates of the spec's own, want no tree, none and its seed", unconverted.Key, unconvertedTree, rooms, ownSeed)
	}
}

// A plan.md uploaded to a converted issue lands in its own fragment of the issue's room, with its
// document_rooms row from its first write and no role in its row, the role read from its node's
// query value; its text reads back; a later upload of its name and an edit of it each reach that
// fragment alone, leaving the spec's text as it was; and the room's pass the upload runs causes
// nothing of the spec's blocks under the upload's event: an unchanged sibling fragment's facts
// append nothing (T12b of dispatch://INFRA-1253's plan).
func TestAPlanUploadedToAConvertedIssueLandsInItsFragment(t *testing.T) {
	ctx := context.Background()
	handler, database, _ := newTestServer(t, testServerOptions{})
	createRoomProject(t, handler, "PLAN")
	// The spec's blocks carry their ids from the create, so the create's own pass indexes them, and
	// what a later pass states of them is a restatement or nothing.
	issue, tree := createRoomIssue(t, handler, database, "PLAN", "A converted issue",
		":::criterion{#k1}\nThe spec paragraph.\n:::\n\n:::criterion{#k2}\nA second spec paragraph.\n:::\n", "")
	if tree == "" {
		t.Fatalf("issue %s holds no tree", issue.Key)
	}
	upload := func(content string) model.Artifact {
		t.Helper()
		response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/artifacts", map[string]any{"name": "plan.md", "content": content}, "alice")
		if response.Code != http.StatusCreated {
			t.Fatalf("upload plan.md: status=%d body=%s", response.Code, response.Body.String())
		}
		return decodeBody[artifactUploadResponse](t, response).Artifact
	}
	plan := upload("The first plan.\n")
	if plan.Room != tree || plan.Fragment != plan.ID || plan.Role != model.ArtifactRolePlan {
		t.Errorf("the upload answers room %q fragment %q role %q, want room %s, fragment %s, role plan", plan.Room, plan.Fragment, plan.Role, tree, plan.ID)
	}
	var room *string
	var role *string
	var ownUpdates int
	if err := database.Pool.QueryRow(ctx, `
		select (select room::text from document_rooms where artifact_id = a.id), a.role,
		       (select count(*) from doc_updates where artifact_id = a.id)
		from artifacts a where a.id = $1::uuid`, plan.ID).Scan(&room, &role, &ownUpdates); err != nil {
		t.Fatalf("read the plan's room: %v", err)
	}
	if room == nil || *room != tree || role != nil || ownUpdates != 0 {
		t.Errorf("the plan's row holds room %v, role %v and %d updates of its own, want room %s, no role and none", room, role, ownUpdates, tree)
	}
	if text := artifactTextOf(t, handler, plan.ID); !strings.Contains(text, "The first plan.") {
		t.Errorf("the plan reads %q, want its first text", text)
	}

	// The upload's pass over the room states nothing of the spec's unchanged blocks.
	var event int64
	if err := database.Pool.QueryRow(ctx, `
		select id from events where type = 'artifact.created' and payload->'artifact'->>'id' = $1`, plan.ID).Scan(&event); err != nil {
		t.Fatalf("read the upload's event: %v", err)
	}
	var specFacts, specBlocks int
	var specKinds string
	if err := database.Pool.QueryRow(ctx, `
		select (select count(*) from graph_tuples t
		        join issue_room_blocks b on t.from_id = $3 || '#' || b.block_id
		        where b.tree_artifact_id = $1::uuid and b.document_id = $2::uuid and t.source_event = $4),
		       (select coalesce(string_agg(distinct t.kind, ','), '') from graph_tuples t
		        join issue_room_blocks b on t.from_id = $3 || '#' || b.block_id
		        where b.tree_artifact_id = $1::uuid and b.document_id = $2::uuid and t.source_event = $4),
		       (select count(*) from issue_room_blocks b where b.tree_artifact_id = $1::uuid and b.document_id = $2::uuid)`,
		tree, issue.PrimaryArtifactID, issue.Key+"/tree", event).Scan(&specFacts, &specKinds, &specBlocks); err != nil {
		t.Fatalf("read what the upload's pass stated of the spec: %v", err)
	}
	if specBlocks == 0 || specFacts != 0 {
		t.Errorf("the room holds %d blocks of the spec and the upload's event caused %d facts of them (%s), want the spec's blocks and none", specBlocks, specFacts, specKinds)
	}

	again := upload("The plan, written again.\n")
	if again.ID != plan.ID {
		t.Fatalf("the second upload of plan.md made %s, want the plan %s's next version", again.ID, plan.ID)
	}
	if text := artifactTextOf(t, handler, plan.ID); !strings.Contains(text, "written again") || strings.Contains(text, "first plan") {
		t.Errorf("the plan reads %q after its second upload, want the second text alone", text)
	}
	edited := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+plan.ID+"/edits", map[string]any{
		"ops": []map[string]any{{"op": "replace", "find": "written again", "with": "edited in place"}},
	}, "alice")
	if edited.Code != http.StatusOK {
		t.Fatalf("edit the plan: status=%d body=%s", edited.Code, edited.Body.String())
	}
	if text := artifactTextOf(t, handler, plan.ID); !strings.Contains(text, "edited in place") {
		t.Errorf("the plan reads %q after its edit, want the edit", text)
	}
	if text := artifactTextOf(t, handler, issue.PrimaryArtifactID); !strings.Contains(text, "The spec paragraph.") || strings.Contains(text, "plan") {
		t.Errorf("the spec reads %q, want its own text untouched by the plan's writes", text)
	}
}

// A document write addressed through a parent issue that names, by its store address, a node of
// its child issue - a criterion written into the child's own room - is refused 409
// NODE_IN_OTHER_ISSUE naming the child, and writes nothing (D3 of dispatch://INFRA-1253's plan).
func TestAWriteThroughTheParentNamingAChildsNodeIsRefused(t *testing.T) {
	ctx := context.Background()
	handler, database, _ := newTestServer(t, testServerOptions{})
	createRoomProject(t, handler, "FAM")
	parent, parentTree := createRoomIssue(t, handler, database, "FAM", "The parent", "The parent's spec.\n", "")
	child, childTree := createRoomIssue(t, handler, database, "FAM", "The child", "The child's spec.\n", parent.Key)
	if parentTree == "" || childTree == "" {
		t.Fatalf("the parent's tree is %q and the child's %q, want both", parentTree, childTree)
	}
	written := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+childTree+"/edits", map[string]any{
		"ops": []map[string]any{{"op": "insert", "after": "end", "markdown": ":::criterion{#k1}\nThe child's criterion.\n:::\n"}},
	}, "alice")
	if written.Code != http.StatusOK {
		t.Fatalf("write the child's criterion: status=%d body=%s", written.Code, written.Body.String())
	}
	var address string
	if err := database.Pool.QueryRow(ctx, `
		select n.id from graph_nodes n
		where n.kind = 'block' and n.id = $1 || '#k1'`, child.Key+"/tree").Scan(&address); err != nil {
		t.Fatalf("read the child's criterion node: %v", err)
	}
	before := artifactTextOf(t, handler, parentTree)
	refused := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+parentTree+"/edits", map[string]any{
		"ops": []map[string]any{{"op": "delete", "block": address}},
	}, "alice")
	refusal := decodeBody[struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}](t, refused)
	if refused.Code != http.StatusConflict || refusal.Code != codeNodeInOtherIssue || !strings.Contains(refusal.Error, child.Key) {
		t.Errorf("a delete through %s naming %s answered %d %s %q, want 409 %s naming %s", parent.Key, address, refused.Code, refusal.Code, refusal.Error, codeNodeInOtherIssue, child.Key)
	}
	if after := artifactTextOf(t, handler, parentTree); after != before {
		t.Errorf("the parent's tree reads %q after the refused write, want %q", after, before)
	}
	if text := artifactTextOf(t, handler, childTree); !strings.Contains(text, "The child's criterion.") {
		t.Errorf("the child's tree reads %q, want its criterion still there", text)
	}
}

// storedFragmentLength is how many elements the fragment named fragment holds in the stored history
// of room, every update of it applied in order: what the room's next load holds of the fragment.
func storedFragmentLength(t *testing.T, database *store.Store, room, fragment string) int {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `select update from doc_updates where artifact_id = $1::uuid order by version`, room)
	if err != nil {
		t.Fatalf("read the stored history of %s: %v", room, err)
	}
	defer rows.Close()
	doc := crdt.New()
	for rows.Next() {
		var update []byte
		if err := rows.Scan(&update); err != nil {
			t.Fatalf("read an update of %s: %v", room, err)
		}
		if err := crdt.ApplyUpdateV1(doc, update, nil); err != nil {
			t.Fatalf("apply an update of %s: %v", room, err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the stored history of %s: %v", room, err)
	}
	return doc.GetXmlFragment(fragment).Len()
}

// A document of a converted issue is judged for removal, and removed, by its own fragment of the
// issue's room: one a person typed in, whose edit no version holds yet, is refused as not
// untouched and keeps what was typed, while an untouched one goes - its document_rooms row, and its
// fragment's content out of the room's stored state - and the sibling documents' fragments keep
// their text (T12b of dispatch://INFRA-1253's plan).
func TestARemovalOfAConvertedIssuesDocumentWeighsAndTakesOutItsOwnFragment(t *testing.T) {
	ctx := context.Background()
	// Settlement is held back, so the typed edit is still unsettled when the removal reads it.
	handler, database, deps := newTestServer(t, testServerOptions{settle: time.Hour})
	createRoomProject(t, handler, "GONE")
	issue, tree := createRoomIssue(t, handler, database, "GONE", "A converted issue", "The spec's words.\n", "")
	if tree == "" {
		t.Fatalf("issue %s holds no tree", issue.Key)
	}
	sibling := uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "Sibling notes", "The sibling's words.\n").Artifact
	typed := emptyDocument(t, handler, issue.Key, "Typed notes")
	untouched := emptyDocument(t, handler, issue.Key, "Untouched notes")
	for _, document := range []model.Artifact{sibling, typed, untouched} {
		if document.Room != tree {
			t.Fatalf("%s reads room %q, want the issue's room %s", document.Name, document.Room, tree)
		}
	}
	if _, err := replaceDocumentText(database, deps.Docs, typed.ID, "The typed words.\n", model.Actor{Kind: "user", ID: "alice"}); err != nil {
		t.Fatalf("type into the document's fragment: %v", err)
	}
	assertRemovalRefused(t, handler, "a document of the room whose edit has not settled", typed,
		http.StatusConflict, "ARTIFACT_NOT_UNTOUCHED", notUntouchedSentence(typed, "an edit of it has not settled"))
	if text := artifactTextOf(t, handler, typed.ID); !strings.Contains(text, "The typed words.") {
		t.Errorf("the typed document reads %q after its refused removal, want what was typed", text)
	}

	if held := storedFragmentLength(t, database, tree, untouched.ID); held == 0 {
		t.Fatalf("the room's stored state holds nothing of %s's fragment before its removal, so its removal takes nothing out", untouched.ID)
	}
	// The untouched document's fragment renders as no text, so the room's index holds no block of
	// it, as a document of its own room that renders as no text holds none.
	var blocks int
	if err := database.Pool.QueryRow(ctx, `select count(*) from issue_room_blocks where document_id = $1::uuid`, untouched.ID).Scan(&blocks); err != nil {
		t.Fatalf("read the room's blocks of %s: %v", untouched.ID, err)
	}
	if blocks != 0 {
		t.Errorf("the room's index holds %d blocks of the untouched document %s, want none", blocks, untouched.ID)
	}
	removedDocument(t, handler, untouched.ID)
	if held := storedFragmentLength(t, database, tree, untouched.ID); held != 0 {
		t.Errorf("the room's stored state holds %d elements of the removed document's fragment, want none", held)
	}
	var joined []string
	rows, err := database.Pool.Query(ctx, `select artifact_id::text from document_rooms where room = $1::uuid order by artifact_id`, tree)
	if err != nil {
		t.Fatalf("read the room's documents: %v", err)
	}
	for rows.Next() {
		var document string
		if err := rows.Scan(&document); err != nil {
			t.Fatalf("read a document of the room: %v", err)
		}
		joined = append(joined, document)
	}
	rows.Close()
	if slices.Contains(joined, untouched.ID) || !slices.Contains(joined, sibling.ID) || !slices.Contains(joined, typed.ID) ||
		!slices.Contains(joined, issue.PrimaryArtifactID) {
		t.Errorf("the room's documents are %v after the removal, want the spec, %s and %s and not %s", joined, sibling.ID, typed.ID, untouched.ID)
	}
	for document, want := range map[string]string{
		issue.PrimaryArtifactID: "The spec's words.", sibling.ID: "The sibling's words.", typed.ID: "The typed words.",
	} {
		if text := artifactTextOf(t, handler, document); !strings.Contains(text, want) {
			t.Errorf("%s reads %q after its sibling's removal, want %q", document, text, want)
		}
	}
}

// A removal of a document of a converted issue reads what cites the document only once the room's
// index holds every write of the room: an upload citing the document, made while another fragment
// of the room holds an edit no version holds yet, is not indexed by its own request, which leaves
// the room's index owed to the room's settlement, and until that pass runs the removal of the cited
// document is refused as one to retry, and the document stays (T12b of dispatch://INFRA-1253's
// plan). The pass cannot run in the removal's transaction while the spec's edit is unsettled, for
// the reason the request's could not.
func TestARemovalOfADocumentAnOwedRoomIndexMayCiteIsRefusedUntilTheRoomSettles(t *testing.T) {
	handler, database, deps := newTestServer(t, testServerOptions{settle: time.Hour})
	createRoomProject(t, handler, "CITE")
	issue, tree := createRoomIssue(t, handler, database, "CITE", "A converted issue", "The spec's words.\n", "")
	if tree == "" {
		t.Fatalf("issue %s holds no tree", issue.Key)
	}
	cited := emptyDocument(t, handler, issue.Key, "Cited notes")
	if _, err := replaceDocumentText(database, deps.Docs, issue.PrimaryArtifactID, "The spec's words, edited.\n", model.Actor{Kind: "user", ID: "alice"}); err != nil {
		t.Fatalf("edit the spec's fragment: %v", err)
	}
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "Citing notes", "See dispatch://"+issue.Key+"/artifact/"+cited.Slug+" for the rest.\n")
	assertRemovalRefused(t, handler, "a document an upload cites while the room's index is owed", cited,
		http.StatusConflict, "ARTIFACT_NOT_UNTOUCHED", notUntouchedSentence(cited, roomIndexOwedReason))
}

// An ask block of a converted document is that document's own ask (the T12b fixes of
// dispatch://INFRA-1253's plan): settlement indexes it keyed by the issue's tree, whose room holds
// the block, and every read of it names the document whose fragment holds the block. The ask read
// names the spec as its block's document; the spec's text read shows the block as the spec's own
// ask, as no copy of another document's; and an answer to it leaves no settlement owed under any
// document of the room, whose settlement is keyed by, and clears, the room's own row.
func TestAConvertedDocumentsAskIsItsOwnAndItsAnswerOwesNoCopy(t *testing.T) {
	ctx := context.Background()
	handler, database := blockAskHandler(t)
	issue := createInteractionIssue(t, handler, "OWN", "A converted ask",
		"Context\n\n:::ask{#ship-it urgency=\"med\" multiple=\"false\"}\nShip it?\n\n- Yes: Now.\n- No: Later.\n:::\n")
	tree, err := issueTreeOf(ctx, database.Pool, issue.Key)
	if err != nil || tree == "" {
		t.Fatalf("the tree of %s is %q (%v), want the issue born converted", issue.Key, tree, err)
	}
	awaitIndexedAskBlock(t, handler, issue.PrimaryArtifactID, "ship-it", "Ship it?")
	askID := blockAskID(t, handler, issue.Key, "ship-it")
	var key string
	if err := database.Pool.QueryRow(ctx, `select block_artifact_id::text from asks where id = $1`, askID).Scan(&key); err != nil || key != tree {
		t.Fatalf("the ask's row is keyed by %q (%v), want the issue's tree %s", key, err, tree)
	}
	if ask := readBlockAsk(t, handler, askID); ask.BlockArtifact == nil || ask.BlockArtifact.ID != issue.PrimaryArtifactID {
		t.Fatalf("the ask read names %#v as its block's document, want the spec %s", ask.BlockArtifact, issue.PrimaryArtifactID)
	}
	if text := artifactTextOf(t, handler, issue.PrimaryArtifactID); !strings.Contains(text, ":::ask{#ship-it") || strings.Contains(text, "copied_from") {
		t.Fatalf("the spec's text read shows its own ask as a copy, or not at all:\n%s", text)
	}
	answered := dispatchRequest(t, handler, http.MethodPost, "/api/v1/asks/"+askID+"/answer", map[string]any{"selected": []string{"Yes"}}, "alice")
	if answered.Code != http.StatusOK {
		t.Fatalf("answer the ask: status=%d body=%s", answered.Code, answered.Body.String())
	}
	var owed []string
	if err := database.Pool.QueryRow(ctx, `
		select coalesce(array_agg(p.artifact_id::text order by p.artifact_id), '{}')
		from doc_settlements_pending p join artifacts a on a.id = p.artifact_id
		where a.issue_key = $1 and a.kind <> 'tree'`, issue.Key).Scan(&owed); err != nil {
		t.Fatalf("read the issue's pending settlements: %v", err)
	}
	if len(owed) != 0 {
		t.Fatalf("the answer left settlements owed under the documents %v, which the room's settlement never clears", owed)
	}
	if text := artifactTextOf(t, handler, issue.PrimaryArtifactID); !strings.Contains(text, `state="answered"`) || strings.Contains(text, "copied_from") {
		t.Fatalf("the spec's text read after the answer shows no answered ask of its own:\n%s", text)
	}
}

// Block ids are unique within an issue room, so a copy of the spec uploaded into its own converted
// issue is no copy of the spec's asks: its ask blocks take ids of their own when its fragment
// settles, and each opens an ask of its own on the copy, beside the spec's, which keeps its block
// (the T12b fixes of dispatch://INFRA-1253's plan). The copy rule between an owner's documents
// (TestAnUploadedCopyOfASpecOpensNoAskForItsCopiedBlocks) is that of documents that are each their
// own room.
func TestACopyOfASpecUploadedIntoItsConvertedIssueOpensItsOwnAsks(t *testing.T) {
	ctx := context.Background()
	handler, database := blockAskHandler(t)
	spec := "Context\n\n:::ask{#rec-open urgency=\"med\" multiple=\"false\"}\nWhich region?\n:::\n"
	issue := createInteractionIssue(t, handler, "COPY", "A copy in its own room", spec)
	if tree, err := issueTreeOf(ctx, database.Pool, issue.Key); err != nil || tree == "" {
		t.Fatalf("the tree of %s is %q (%v), want the issue born converted", issue.Key, tree, err)
	}
	awaitIndexedAskBlock(t, handler, issue.PrimaryArtifactID, "rec-open", "Which region?")
	source := blockAskID(t, handler, issue.Key, "rec-open")
	uploaded := sessionRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/artifacts", map[string]any{
		"actor": sessionActor(), "name": "spec-record.md", "content": artifactTextOf(t, handler, issue.PrimaryArtifactID),
	})
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("upload the copy: status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	copied := decodeBody[struct {
		Artifact model.Artifact `json:"artifact"`
	}](t, uploaded).Artifact.ID
	deadline := time.Now().Add(10 * time.Second)
	for {
		var own *model.Ask
		listed := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue.Key+"/asks?state=all", nil, "alice")
		asks := decodeBody[[]model.Ask](t, listed)
		for index, ask := range asks {
			if ask.BlockArtifact != nil && ask.BlockArtifact.ID == copied {
				own = &asks[index]
			}
		}
		if own != nil {
			if own.ID == source || own.BlockID == nil || *own.BlockID == "rec-open" || own.State != "open" || own.Question != "Which region?" {
				t.Fatalf("the copy's ask = %#v, want an open ask of its own under an id of its own", own)
			}
			if len(asks) != 2 {
				t.Fatalf("the issue holds %d asks, want the spec's and the copy's: %#v", len(asks), asks)
			}
			if again := blockAskID(t, handler, issue.Key, "rec-open"); again != source {
				t.Fatalf("the spec's block rec-open indexes %s, was %s", again, source)
			}
			if text := artifactTextOf(t, handler, copied); strings.Contains(text, "copied_from") || strings.Contains(text, "#rec-open") {
				t.Fatalf("the copy's text still reads as a copy of the spec's ask:\n%s", text)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the copy never opened an ask of its own: %#v", asks)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
