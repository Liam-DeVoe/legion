package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// TestAnArtifactNodeHangsUnderItsIssueWithItsBytesAsValues holds the writers of an issue's
// document's node to R16 of dispatch://INFRA-1253 (T2a of its plan): the spec at the issue's
// create, a markdown upload and an image upload each write the document's node with its part_of to
// issue:<KEY>, caused by the event of the write, and stamp the node on the artifacts row
// (node_kind, node_address); the issue's row names its node too; and the image's node holds the
// mime, the size and the sha256 of its bytes as values, which its next version moves. A document
// no issue owns - a project's, a file of an agent's conversation - hangs under none, and its row
// names its node all the same.
//
// Each document's row and its tuples are read in one statement after its request, so the two are
// read in one snapshot. A request refused at its commit - by a deferred trigger the test installs
// on artifacts, which fires after the handler has appended its event and every tuple through
// graphstore.Caused - leaves neither the row, nor the node, nor a tuple out of the node or into
// the issue: they commit in one transaction or not at all, and a writer that appended the part_of
// in a transaction of its own would leave that tuple standing.
func TestAnArtifactNodeHangsUnderItsIssueWithItsBytesAsValues(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	ctx := context.Background()
	if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects", map[string]string{
		"key": "TREE", "name": "Tree",
	}, "alice"); response.Code != http.StatusCreated {
		t.Fatalf("create the project: status=%d body=%s", response.Code, response.Body.String())
	}
	const key = "TREE-1"

	// A create refused at its commit rolls back the issue, its spec and their nodes and tuples.
	refuseCommits(t, database, func() {
		response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]string{
			"project": "TREE", "title": "A tree of documents", "spec": "The spec.\n",
		}, "alice")
		if response.Code < http.StatusInternalServerError {
			t.Fatalf("the create refused at its commit answered %d %s, want a 5xx", response.Code, response.Body.String())
		}
	})
	requireNothingOf(t, database, key+"/spec")
	var issueLeft int
	if err := database.Pool.QueryRow(ctx, `
		select (select count(*) from issues where key = $1)
		     + (select count(*) from graph_nodes where kind = $2 and id = $1)
		     + (select count(*) from graph_tuples where to_kind = $2 and to_id = $1)
	`, key, graphstore.KindIssue).Scan(&issueLeft); err != nil {
		t.Fatalf("read what the refused create left of %s: %v", key, err)
	}
	if issueLeft != 0 {
		t.Errorf("the refused create left %d rows, nodes or tuples of issue %s, want none", issueLeft, key)
	}

	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]string{
		"project": "TREE", "title": "A tree of documents", "spec": "The spec.\n",
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("create the issue: status=%d body=%s", created.Code, created.Body.String())
	}
	issue := decodeBody[struct {
		Key               string `json:"key"`
		PrimaryArtifactID string `json:"primary_artifact_id"`
	}](t, created)
	if issue.Key != key {
		t.Fatalf("the create made %s, want %s: the refused create took a number", issue.Key, key)
	}
	var issueKind, issueAddress *string
	if err := database.Pool.QueryRow(ctx, `select node_kind, node_address from issues where key = $1`, key).
		Scan(&issueKind, &issueAddress); err != nil {
		t.Fatalf("read the marker of %s: %v", key, err)
	}
	if deref(issueKind) != graphstore.KindIssue || deref(issueAddress) != key {
		t.Errorf("issue %s names node %q/%q, want %s/%s", key, deref(issueKind), deref(issueAddress), graphstore.KindIssue, key)
	}
	spec := readPlacement(t, database, issue.PrimaryArtifactID)
	spec.require(t, "the spec", []string{"issue:" + key}, eventOf(t, database, "issue.created", "issue_key", key))
	requireNoBytes(t, database, spec.refKey)

	// An upload refused at its commit leaves neither its document's row, nor its node, nor a tuple
	// out of it or into the issue: the issue holds the spec's part_of alone, before and after.
	underTheIssue := func() int {
		t.Helper()
		var count int
		if err := database.Pool.QueryRow(ctx, `
			select count(*) from graph_tuples where to_kind = $1 and to_id = $2
		`, graphstore.KindIssue, key).Scan(&count); err != nil {
			t.Fatalf("count the tuples into issue %s: %v", key, err)
		}
		return count
	}
	before := underTheIssue()
	refuseCommits(t, database, func() {
		for _, refused := range []*httptest.ResponseRecorder{
			dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+key+"/artifacts", map[string]string{
				"name": "notes.md", "content": "# Notes\n",
			}, "alice"),
			multipartRequest(t, handler, "/api/v1/issues/"+key+"/artifacts", map[string]string{"name": "shot.png"},
				"shot.png", "image/png", pngBytes("refused"), "alice"),
		} {
			if refused.Code < http.StatusInternalServerError {
				t.Fatalf("the upload refused at its commit answered %d %s, want a 5xx", refused.Code, refused.Body.String())
			}
		}
	})
	requireNothingOf(t, database, key+"/notes-md")
	requireNothingOf(t, database, key+"/shot-png")
	if after := underTheIssue(); after != before || before != 1 {
		t.Errorf("issue %s holds %d tuples into it after the refused uploads and %d before, want the spec's part_of alone both times",
			key, after, before)
	}

	markdown := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+key+"/artifacts", map[string]string{
		"name": "notes.md", "content": "# Notes\n\nThe plan stands.\n",
	}, "alice")
	if markdown.Code != http.StatusCreated {
		t.Fatalf("upload the markdown document: status=%d body=%s", markdown.Code, markdown.Body.String())
	}
	notes := decodeBody[artifactUploadResponse](t, markdown).Artifact
	placed := readPlacement(t, database, notes.ID)
	placed.require(t, "the markdown upload", []string{"issue:" + key}, artifactCreated(t, database, notes.ID))
	requireNoBytes(t, database, placed.refKey)

	first := pngBytes("the first picture")
	image := multipartRequest(t, handler, "/api/v1/issues/"+key+"/artifacts", map[string]string{"name": "shot.png"},
		"shot.png", "image/png", first, "alice")
	if image.Code != http.StatusCreated {
		t.Fatalf("upload the image: status=%d body=%s", image.Code, image.Body.String())
	}
	uploaded := decodeBody[artifactUploadResponse](t, image)
	shot := readPlacement(t, database, uploaded.Artifact.ID)
	shotCreated := artifactCreated(t, database, uploaded.Artifact.ID)
	shot.require(t, "the image upload", []string{"issue:" + key}, shotCreated)
	requireVersionBytes(t, uploaded.Version.MIME, uploaded.Version.Size, uploaded.Version.SHA256, "image/png", first)

	// The image's next version moves its values to the new bytes, and hangs it under nothing more.
	second := pngBytes("the second picture, which is longer")
	again := multipartRequest(t, handler, "/api/v1/issues/"+key+"/artifacts", map[string]string{"name": "shot.png"},
		"shot.png", "image/png", second, "alice")
	if again.Code != http.StatusCreated {
		t.Fatalf("upload the image's second version: status=%d body=%s", again.Code, again.Body.String())
	}
	if number := decodeBody[artifactUploadResponse](t, again).Version.Number; number != 2 {
		t.Fatalf("the second upload of shot.png wrote version %d, want 2", number)
	}
	readPlacement(t, database, uploaded.Artifact.ID).require(t, "the image after its second version", []string{"issue:" + key}, shotCreated)
	var shotVersion int64
	if err := database.Pool.QueryRow(ctx, `
		select id from events where type = 'artifact.version' and payload->>'artifact_id' = $1
	`, uploaded.Artifact.ID).Scan(&shotVersion); err != nil {
		t.Fatalf("read the artifact.version event of the image: %v", err)
	}
	requireBytesHistory(t, database, shot.refKey, []statedBytes{
		{mime: "image/png", content: first, event: shotCreated},
		{mime: "image/png", content: second, event: shotVersion},
	})

	// A project's document and a conversation's file belong to no issue: neither hangs under one,
	// and each row names its node.
	runbook := createProjectDocument(t, handler, "TREE", "Runbook.md", "# Runbook\n")
	readPlacement(t, database, runbook.ID).require(t, "the project document", nil, artifactCreated(t, database, runbook.ID))
	const session = "01a1e0fe-90a3-76fa-b47a-7d62f39b4577"
	trace := []byte("\x00\x01 a trace sent in the conversation")
	sent := multipartRequest(t, handler, "/api/v1/agents/"+session+"/artifacts", map[string]string{"name": "trace.bin"},
		"trace.bin", "application/octet-stream", trace, "alice")
	if sent.Code != http.StatusCreated {
		t.Fatalf("upload a file to a conversation: status=%d body=%s", sent.Code, sent.Body.String())
	}
	conversationFile := decodeBody[artifactUploadResponse](t, sent).Artifact
	file := readPlacement(t, database, conversationFile.ID)
	file.require(t, "the conversation's file", nil, graphstore.NoEvent)
	requireBytesHistory(t, database, file.refKey, []statedBytes{
		{mime: "application/octet-stream", content: trace, event: graphstore.NoEvent},
	})
}

// TestARemovedDocumentIsNoLongerANodeOfItsIssue holds the removal of an issue's document (api's
// removeArtifact) to R16 of dispatch://INFRA-1253: the document is a node under its issue, and one
// removed is present no longer. The removal states the node gone in its own transaction, caused by
// the artifact.removed event, as the block index states a deleted block gone, and leaves its
// history as it stood, its part_of among it. A removed document holds no block node, since the
// removal takes only a document whose one version is empty (artifactIsUntouched), so there is no
// block node to read beside it, before or after. The store census and the project census read no
// difference after it.
func TestARemovedDocumentIsNoLongerANodeOfItsIssue(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	ctx := context.Background()
	issue := createInteractionIssue(t, handler, "GONE", "A document made by mistake", "The spec.\n")
	document := emptyDocument(t, handler, issue.Key, "Lauch notes")
	readPlacement(t, database, document.ID).require(t, "the document before its removal",
		[]string{"issue:" + issue.Key}, artifactCreated(t, database, document.ID))
	presentBlocks := func(when string) int {
		t.Helper()
		var present int
		if err := database.Pool.QueryRow(ctx, `
			select count(*) from graph_nodes where kind = 'block' and id like $1 || '#%' and state <> $2
		`, document.RefKey, graphstore.StateGone).Scan(&present); err != nil {
			t.Fatalf("count the present block nodes of %s %s: %v", document.RefKey, when, err)
		}
		return present
	}
	if present := presentBlocks("before its removal"); present != 0 {
		t.Fatalf("the untouched document %s holds %d present block nodes before its removal, want none", document.RefKey, present)
	}

	removedDocument(t, handler, document.ID)

	node, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindArtifact, document.RefKey)
	if err != nil || !held || node.State != graphstore.StateGone {
		t.Errorf("after its removal %s's node is %+v (held %t, %v), want it held and gone", document.RefKey, node, held, err)
	}
	removal := eventOf(t, database, "artifact.removed", "issue_key", issue.Key)
	states, err := graphstore.History(ctx, database.Pool, graphstore.KindArtifact, document.RefKey, graphstore.AttributeState)
	if err != nil {
		t.Fatalf("read the states of %s: %v", document.RefKey, err)
	}
	if len(states) != 2 || states[1].ToID != graphstore.StateGone || states[1].SourceEvent != removal {
		t.Errorf("%s states %+v, want live at its create and then gone, caused by the artifact.removed event %d",
			document.RefKey, states, removal)
	}
	history, err := graphstore.History(ctx, database.Pool, graphstore.KindArtifact, document.RefKey, graphstore.TuplePartOf)
	if err != nil || len(history) != 1 || history[0].ToID != issue.Key || !history[0].Asserted {
		t.Errorf("%s's part_of is %+v (%v) after its removal, want the one its create stated, standing in its history", document.RefKey, history, err)
	}
	if present := presentBlocks("after its removal"); present != 0 {
		t.Errorf("the removed document %s holds %d present block nodes, want none", document.RefKey, present)
	}
	requireStoreCensusZero(t, database, "after the removal")
	incomplete, err := graphcensus.ProjectComplete(ctx, database.Pool)
	if err != nil || len(incomplete) != 0 {
		t.Errorf("after the removal the project census names %v (%v), want nothing", incomplete, err)
	}
}

// placement is what one statement reads of a document after its request: its row's address and
// marker, whether the store holds its node, and the part_of tuples out of that node, each as
// `<to_kind>:<to_id>` and its cause, in the order they were appended.
type placement struct {
	refKey                string
	nodeKind, nodeAddress *string
	nodeHeld              bool
	partOf                []string
	causes                []int64
}

// readPlacement reads the row of the artifact and the tuples of its node in one statement, so
// the two are one snapshot of the store.
func readPlacement(t *testing.T, database *store.Store, artifactID string) placement {
	t.Helper()
	var read placement
	if err := database.Pool.QueryRow(context.Background(), `
		select a.ref_key, a.node_kind, a.node_address,
		       exists (select 1 from graph_nodes n where n.kind = $2 and n.id = a.ref_key),
		       coalesce((select array_agg(t.to_kind || ':' || t.to_id order by t.seq) from graph_tuples t
		                 where t.from_kind = $2 and t.from_id = a.ref_key and t.kind = $3), '{}'),
		       coalesce((select array_agg(t.source_event order by t.seq) from graph_tuples t
		                 where t.from_kind = $2 and t.from_id = a.ref_key and t.kind = $3), '{}')
		from artifacts a where a.id = $1::uuid
	`, artifactID, graphstore.KindArtifact, graphstore.TuplePartOf).Scan(
		&read.refKey, &read.nodeKind, &read.nodeAddress, &read.nodeHeld, &read.partOf, &read.causes,
	); err != nil {
		t.Fatalf("read the row and the part_of of artifact %s: %v", artifactID, err)
	}
	return read
}

// require fails unless the document's row names its node, the store holds that node, and the node
// hangs under exactly the issues parents names, each part_of caused by event.
func (p placement) require(t *testing.T, what string, parents []string, event int64) {
	t.Helper()
	if deref(p.nodeKind) != graphstore.KindArtifact || deref(p.nodeAddress) != p.refKey {
		t.Errorf("%s's row %s names node %q/%q, want %s/%s", what, p.refKey, deref(p.nodeKind), deref(p.nodeAddress),
			graphstore.KindArtifact, p.refKey)
	}
	if !p.nodeHeld {
		t.Errorf("%s's row %s names a node the store does not hold", what, p.refKey)
	}
	if !slices.Equal(p.partOf, parents) {
		t.Errorf("%s's node %s is part_of %v, want %v", what, p.refKey, p.partOf, parents)
	}
	for _, cause := range p.causes {
		if cause != event {
			t.Errorf("%s's part_of carries cause %d, want the event %d of the write that stored its row", what, cause, event)
		}
	}
}

// requireNothingOf fails if the store or the artifacts table holds anything of refKey: its row, its
// node, or a tuple out of it.
func requireNothingOf(t *testing.T, database *store.Store, refKey string) {
	t.Helper()
	var left int
	if err := database.Pool.QueryRow(context.Background(), `
		select (select count(*) from artifacts where ref_key = $1)
		     + (select count(*) from graph_nodes where kind = $2 and id = $1)
		     + (select count(*) from graph_tuples where from_kind = $2 and from_id = $1)
	`, refKey, graphstore.KindArtifact).Scan(&left); err != nil {
		t.Fatalf("read what a refused write left of %s: %v", refKey, err)
	}
	if left != 0 {
		t.Errorf("a refused write left %d rows, nodes or tuples of %s, want none", left, refKey)
	}
}

// refuseCommits runs during with a deferred constraint trigger in place on artifacts, which
// refuses at commit every transaction that inserted an artifacts row: after the handler has
// written its rows, appended its event and every tuple through graphstore.Caused, and called
// commit. It takes the trigger away after.
func refuseCommits(t *testing.T, database *store.Store, during func()) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.Pool.Exec(ctx, `
		create or replace function test_refuse_commit() returns trigger language plpgsql as $$
		begin
		  raise exception 'the test refuses this commit';
		end $$;
		create constraint trigger zz_refuse_commit after insert on artifacts
		deferrable initially deferred
		for each row execute function test_refuse_commit();
	`); err != nil {
		t.Fatalf("install the trigger that refuses a commit: %v", err)
	}
	during()
	if _, err := database.Pool.Exec(ctx, `drop trigger zz_refuse_commit on artifacts`); err != nil {
		t.Fatalf("take the trigger that refuses a commit away: %v", err)
	}
}

// artifactCreated answers the id of the artifact.created event of the artifact.
func artifactCreated(t *testing.T, database *store.Store, artifactID string) int64 {
	t.Helper()
	var id int64
	if err := database.Pool.QueryRow(context.Background(), `
		select id from events where type = 'artifact.created' and payload->'artifact'->>'id' = $1
	`, artifactID).Scan(&id); err != nil {
		t.Fatalf("read the artifact.created event of %s: %v", artifactID, err)
	}
	return id
}

// requireNoBytes fails if the node of refKey states any value of the bytes an image or a file
// carries: a markdown document's body is its blocks.
func requireNoBytes(t *testing.T, database *store.Store, refKey string) {
	t.Helper()
	for _, attribute := range []string{graphstore.AttributeMIME, graphstore.AttributeSize, graphstore.AttributeSHA256} {
		history, err := graphstore.History(context.Background(), database.Pool, graphstore.KindArtifact, refKey, attribute)
		if err != nil {
			t.Fatalf("read the %s of %s: %v", attribute, refKey, err)
		}
		if len(history) != 0 {
			t.Errorf("the markdown document %s states %s %+v, want none", refKey, attribute, history)
		}
	}
}

// requireVersionBytes fails unless the version row the upload answered holds the mime, the size
// and the sha256 of content, which is what its node is held to below.
func requireVersionBytes(t *testing.T, mime *string, size *int, sum *string, wantMIME string, content []byte) {
	t.Helper()
	digest := sha256.Sum256(content)
	if deref(mime) != wantMIME || size == nil || *size != len(content) || deref(sum) != hex.EncodeToString(digest[:]) {
		t.Errorf("the image's version holds mime %q, size %v, sha256 %q; want %q, %d, %x",
			deref(mime), size, deref(sum), wantMIME, len(content), digest)
	}
}

// statedBytes is one version's bytes as its node states them, and the event that caused the write.
type statedBytes struct {
	mime    string
	content []byte
	event   int64
}

// requireBytesHistory fails unless the node of refKey states the mime, the size and the sha256 of
// each version in turn, each caused by that version's event, so its current values are the newest
// version's.
func requireBytesHistory(t *testing.T, database *store.Store, refKey string, versions []statedBytes) {
	t.Helper()
	ctx := context.Background()
	for _, attribute := range []string{graphstore.AttributeMIME, graphstore.AttributeSize, graphstore.AttributeSHA256} {
		history, err := graphstore.History(ctx, database.Pool, graphstore.KindArtifact, refKey, attribute)
		if err != nil {
			t.Fatalf("read the %s of %s: %v", attribute, refKey, err)
		}
		if len(history) != len(versions) {
			t.Errorf("node %s states %s %d times (%+v), want once for each of its %d versions", refKey, attribute, len(history), history, len(versions))
			continue
		}
		for index, version := range versions {
			digest := sha256.Sum256(version.content)
			want := map[string]string{
				graphstore.AttributeMIME:   version.mime,
				graphstore.AttributeSize:   strconv.Itoa(len(version.content)),
				graphstore.AttributeSHA256: hex.EncodeToString(digest[:]),
			}[attribute]
			if stated := history[index]; stated.ToID != want || stated.ToKind != graphstore.KindValue ||
				!stated.Asserted || stated.SourceEvent != version.event {
				t.Errorf("node %s's %s at version %d is %+v, want the value %q caused by event %d",
					refKey, attribute, index+1, stated, want, version.event)
			}
		}
	}
}
