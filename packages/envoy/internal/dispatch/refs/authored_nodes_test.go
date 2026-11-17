package refs

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// writeAuthoredVersion writes version number of the document as a settlement stores it, with
// every writer it read in the order it stores them (docs' actorSlice), under the database's clock
// at the statement, and answers that time.
func writeAuthoredVersion(t *testing.T, tx pgx.Tx, artifact string, number int, markdown string, authors ...model.Actor) time.Time {
	t.Helper()
	encoded, err := json.Marshal(authors)
	if err != nil {
		t.Fatalf("encode the authors of version %d: %v", number, err)
	}
	var at time.Time
	if err := tx.QueryRow(context.Background(), `
		insert into artifact_versions (artifact_id, number, markdown, authors, created_at)
		values ($1::uuid, $2, $3, $4, clock_timestamp())
		returning created_at
	`, artifact, number, markdown, encoded).Scan(&at); err != nil {
		t.Fatalf("write version %d: %v", number, err)
	}
	return at
}

// authoredBy is every authored_by tuple the log holds out of the block node address, in its
// sequence order: its far end and the whole actor it records as who.
func authoredBy(t *testing.T, tx pgx.Tx, address string) []graphstore.Tuple {
	t.Helper()
	rows, err := tx.Query(context.Background(), `
		select to_kind, to_id, who, asserted from graph_tuples
		where from_kind = $1 and from_id = $2 and kind = $3
		order by seq
	`, BlockNodeKind, address, graphstore.TupleAuthoredBy)
	if err != nil {
		t.Fatalf("read the authored_by of %s: %v", address, err)
	}
	defer rows.Close()
	var held []graphstore.Tuple
	for rows.Next() {
		var tuple graphstore.Tuple
		var who []byte
		if err := rows.Scan(&tuple.ToKind, &tuple.ToID, &who, &tuple.Asserted); err != nil {
			t.Fatalf("scan an authored_by of %s: %v", address, err)
		}
		if err := json.Unmarshal(who, &tuple.Who); err != nil {
			t.Fatalf("decode the who of an authored_by of %s: %v", address, err)
		}
		held = append(held, tuple)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the authored_by of %s: %v", address, err)
	}
	return held
}

// requireAuthors fails unless the block node address records exactly authors, by one asserted
// authored_by tuple each in that order, each tuple's far end the author's own node and its who
// the author's whole actor, and unless the node itself is made by the first of them at at, and a
// read of the node (Node) answers all of them, in that order, as its makers at at.
func requireAuthors(t *testing.T, tx pgx.Tx, what, address string, at time.Time, authors ...model.Actor) {
	t.Helper()
	ctx := context.Background()
	held := authoredBy(t, tx, address)
	if len(held) != len(authors) {
		t.Fatalf("%s records %d authored_by tuple(s) %+v, want one for each of the %d authors of the version that first held it",
			what, len(held), held, len(authors))
	}
	for index, author := range authors {
		kind, id, _ := graphstore.AuthorOf(author)
		if tuple := held[index]; tuple.ToKind != kind || tuple.ToID != id || !tuple.Asserted || !reflect.DeepEqual(tuple.Who, author) {
			t.Errorf("%s's authored_by %d is %s/%s (asserted %t) by %+v, want %s/%s by the whole actor %+v",
				what, index+1, tuple.ToKind, tuple.ToID, tuple.Asserted, tuple.Who, kind, id, author)
		}
		var nodes int
		if err := tx.QueryRow(ctx, `select count(*) from graph_nodes where kind = $1 and id = $2`, kind, id).Scan(&nodes); err != nil {
			t.Fatalf("read the node of %s/%s: %v", kind, id, err)
		}
		if nodes != 1 {
			t.Errorf("%s's author %s/%s has %d node(s), want the one its authored_by points at", what, kind, id, nodes)
		}
	}
	var madeBy []byte
	var madeAt time.Time
	if err := tx.QueryRow(ctx, `select made_by, made_at from graph_nodes where kind = $1 and id = $2`, BlockNodeKind, address).Scan(&madeBy, &madeAt); err != nil {
		t.Fatalf("read the node of %s: %v", what, err)
	}
	var maker model.Actor
	if err := json.Unmarshal(madeBy, &maker); err != nil {
		t.Fatalf("decode the made_by of %s: %v", what, err)
	}
	if !reflect.DeepEqual(maker, authors[0]) || !madeAt.Equal(at) {
		t.Errorf("%s's node is made by %+v at %s, want the first author %+v at the version's own time %s", what, maker, madeAt, authors[0], at)
	}
	node, found, err := Node(ctx, tx, model.Actor{}, BlockNodeKind, address, false)
	if err != nil || !found {
		t.Fatalf("read the node of %s: found %t, %v", what, found, err)
	}
	if !reflect.DeepEqual(node.MadeBy, authors) || node.MadeAt == nil || !node.MadeAt.Equal(at) {
		t.Errorf("a read of %s answers it made by %+v at %v, want %+v at %s", what, node.MadeBy, node.MadeAt, authors, at)
	}
}

// A node records its authors by authored_by tuples, one per author of the version that first
// holds it, in that version's own order, each through graphstore.AuthorFacts: its far end the
// author's person or session node and its who the author's whole stored actor (T7 of
// dispatch://INFRA-1253's plan). That holds for an authored node of any document, here a
// requirement of an issue's spec, and for every block of an issue room, where the version is that
// of the block's own document and not the newest the room's pass covers. A paragraph of a
// document that is no room records none. A later version by a third writer adds no author.
func TestABlockRecordsEveryAuthorOfItsFirstVersion(t *testing.T) {
	tx := windowFixture(t)
	owner, service := "bob", "system:serviceaccount:legion:worker"
	agent := model.Actor{
		Kind: "session", ID: "worker-9", Owner: &owner, Service: &service,
		Origin: &model.ActorOrigin{Host: "worker-host", Cwd: "/workspace"},
		Act:    &model.ActorAct{Session: "worker-9", Scopes: []string{model.ScopePerson}},
	}
	person := model.Actor{Kind: "user", ID: "alice"}
	third := model.Actor{Kind: "user", ID: "carol"}

	// A document block: the requirement of an issue's spec, first written in a version the agent
	// and the person both wrote, stored in the settlement's order (the session before the user).
	createReferenceProject(t, tx, "AUTH")
	createReferenceIssue(t, tx, "AUTH-1", "AUTH")
	document, documentRef := createReferenceArtifact(t, tx, "AUTH-1", "AUTH", "spec-md")
	first, firstBlocks := documentOf([]part{{"r1", "requirement", "It records who wrote it."}, {"p1", "paragraph", "Prose."}})
	firstAt := writeAuthoredVersion(t, tx, document, 1, first, agent, person)
	indexAndCause(t, tx, document, OneVersion(document, 1, firstAt), first, firstBlocks)
	requireAuthors(t, tx, "the requirement", documentRef+"#r1", firstAt, agent, person)
	if held := authoredBy(t, tx, documentRef+"#p1"); len(held) != 0 {
		t.Errorf("a paragraph of a document no room holds records the authored_by %+v, want none", held)
	}

	// A room block: x of the spec's fragment, first written in the spec's version 1, which the
	// agent and the person both wrote, while the newest version the room's pass covers is the
	// Verification document's, which erin wrote, and the tree's own is dave's.
	spec, _, verification, tree, treeRef := roomFixture(t, tx)
	dave, erin := model.Actor{Kind: "user", ID: "dave"}, model.Actor{Kind: "user", ID: "erin"}
	treeAt := writeAuthoredVersion(t, tx, tree, 1, "", dave)
	specAt := writeAuthoredVersion(t, tx, spec, 1, "Room words.", agent, person)
	verificationAt := writeAuthoredVersion(t, tx, verification, 1, "Check.", erin)
	room, roomBlocks := documentOf([]part{{"x", "paragraph", "Room words."}, {"y", "paragraph", "Check."}})
	roomBlocks[0].Document, roomBlocks[1].Document = spec, verification
	indexAndCause(t, tx, tree, Versions{
		tree: {Number: 1, At: treeAt}, spec: {Number: 1, At: specAt}, verification: {Number: 1, At: verificationAt},
	}, room, roomBlocks)
	requireAuthors(t, tx, "the room's block x", treeRef+"#x", specAt, agent, person)
	requireAuthors(t, tx, "the room's block y", treeRef+"#y", verificationAt, erin)

	// A later version by a third writer rewrites both blocks and adds no author to either.
	second, secondBlocks := documentOf([]part{{"r1", "requirement", "It records who wrote it first."}, {"p1", "paragraph", "Prose."}})
	secondAt := writeAuthoredVersion(t, tx, document, 2, second, third)
	indexAndCause(t, tx, document, OneVersion(document, 2, secondAt), second, secondBlocks)
	requireAuthors(t, tx, "the requirement after carol's version", documentRef+"#r1", firstAt, agent, person)
	specNext := writeAuthoredVersion(t, tx, spec, 2, "Room words, rewritten.", third)
	rewritten, rewrittenBlocks := documentOf([]part{{"x", "paragraph", "Room words, rewritten."}, {"y", "paragraph", "Check."}})
	rewrittenBlocks[0].Document, rewrittenBlocks[1].Document = spec, verification
	indexAndCause(t, tx, tree, Versions{
		tree: {Number: 1, At: treeAt}, spec: {Number: 2, At: specNext}, verification: {Number: 1, At: verificationAt},
	}, rewritten, rewrittenBlocks)
	requireAuthors(t, tx, "the room's block x after carol's version", treeRef+"#x", specAt, agent, person)
	if changes := blockChanges(t, tx, tree); changes["x"].version != 2 {
		t.Errorf("x changed at version %d, want the spec's version 2, which carol's rewrite is", changes["x"].version)
	}
}

// unrecordAuthors deletes every authored_by tuple out of the block node address, which leaves the
// node, its row and its texts as a build before this one wrote them, or as the drain of
// dispatch://INFRA-1253's T6 left a block it indexed: with no author recorded.
func unrecordAuthors(t *testing.T, tx pgx.Tx, address string) {
	t.Helper()
	if tag, err := tx.Exec(context.Background(), `
		delete from graph_tuples where from_kind = $1 and from_id = $2 and kind = $3
	`, BlockNodeKind, address, graphstore.TupleAuthoredBy); err != nil || tag.RowsAffected() == 0 {
		t.Fatalf("delete the authored_by of %s: %d rows, %v", address, tag.RowsAffected(), err)
	}
}

// A node written before its authors were recorded - by a build before this one, or by the drain
// of dispatch://INFRA-1253's T6, which recorded none - takes them at the next pass that indexes
// its document, which the drain marks every stored document for: one authored_by per author of
// its own first version, in that version's order, and not the authors of the version that pass
// indexes, which a third writer wrote. A requirement of a document and a block of an issue room
// alike, each left untouched by the version the pass indexes.
func TestANodeWrittenBeforeItsAuthorsTakesThemAtTheNextPass(t *testing.T) {
	tx := windowFixture(t)
	agent := model.Actor{Kind: "session", ID: "worker-9"}
	person, third := model.Actor{Kind: "user", ID: "alice"}, model.Actor{Kind: "user", ID: "carol"}

	createReferenceProject(t, tx, "AUTH")
	createReferenceIssue(t, tx, "AUTH-1", "AUTH")
	document, documentRef := createReferenceArtifact(t, tx, "AUTH-1", "AUTH", "spec-md")
	first, firstBlocks := documentOf([]part{{"r1", "requirement", "It records who wrote it."}, {"p1", "paragraph", "Prose."}})
	firstAt := writeAuthoredVersion(t, tx, document, 1, first, agent, person)
	indexAndCause(t, tx, document, OneVersion(document, 1, firstAt), first, firstBlocks)
	unrecordAuthors(t, tx, documentRef+"#r1")
	second, secondBlocks := documentOf([]part{{"r1", "requirement", "It records who wrote it."}, {"p1", "paragraph", "Prose, rewritten."}})
	secondAt := writeAuthoredVersion(t, tx, document, 2, second, third)
	indexAndCause(t, tx, document, OneVersion(document, 2, secondAt), second, secondBlocks)
	requireAuthors(t, tx, "the requirement written before its authors were recorded", documentRef+"#r1", firstAt, agent, person)

	spec, _, verification, tree, treeRef := roomFixture(t, tx)
	treeAt := writeAuthoredVersion(t, tx, tree, 1, "", third)
	specAt := writeAuthoredVersion(t, tx, spec, 1, "Room words.", agent, person)
	verificationAt := writeAuthoredVersion(t, tx, verification, 1, "Check.", third)
	room, roomBlocks := documentOf([]part{{"x", "paragraph", "Room words."}, {"y", "paragraph", "Check."}})
	roomBlocks[0].Document, roomBlocks[1].Document = spec, verification
	indexAndCause(t, tx, tree, Versions{
		tree: {Number: 1, At: treeAt}, spec: {Number: 1, At: specAt}, verification: {Number: 1, At: verificationAt},
	}, room, roomBlocks)
	unrecordAuthors(t, tx, treeRef+"#x")
	verificationNext := writeAuthoredVersion(t, tx, verification, 2, "Check, rewritten.", third)
	next, nextBlocks := documentOf([]part{{"x", "paragraph", "Room words."}, {"y", "paragraph", "Check, rewritten."}})
	nextBlocks[0].Document, nextBlocks[1].Document = spec, verification
	indexAndCause(t, tx, tree, Versions{
		tree: {Number: 1, At: treeAt}, spec: {Number: 1, At: specAt}, verification: {Number: 2, At: verificationNext},
	}, next, nextBlocks)
	requireAuthors(t, tx, "the room block written before its authors were recorded", treeRef+"#x", specAt, agent, person)
}

// A plan node's needs naming an issue (T5a's widened needs, T7 of dispatch://INFRA-1253's plan) is
// a pointer as any other is, which replaceBlockRefs appends: a needs tuple from the plan node to
// the issue's own node, issue:<KEY>, beside the block_refs row that projects it.
func TestAPlanNodesNeedsNamingAnIssueIsATupleToTheIssue(t *testing.T) {
	ctx := context.Background()
	tx := windowFixture(t)
	if _, err := tx.Exec(ctx, `
		insert into issues (key, project_key, number, title, created_by, rank)
		values ('WIND-2', 'WIND', 2, 'The child', '{"kind":"user","id":"alice"}', 'V')
	`); err != nil {
		t.Fatalf("create the issue the plan node needs: %v", err)
	}
	document, documentRef := createReferenceArtifact(t, tx, "WIND-1", "WIND", "spec-md")
	markdown, blocks := documentOf([]part{{"t1", "plan_task", "After the child."}})
	at := writeAuthoredVersion(t, tx, document, 1, markdown, model.Actor{Kind: "user", ID: "alice"})
	pointers := []Pointer{{Block: "t1", BlockType: "plan_task", Kind: "needs",
		Target: text.Ref{Kind: "issue", IssueKey: "WIND-2", ID: "WIND-2"}, Targets: []string{"plan_task"}}}
	_, won, write, err := IndexBlocks(ctx, tx, document, OneVersion(document, 1, at), markdown, blocks, nil, pointers, TypedFacts{})
	if err != nil || !won {
		t.Fatalf("index the plan: won %t, %v", won, err)
	}
	if err := graphstore.Caused(ctx, tx, write, graphstore.NoEvent); err != nil {
		t.Fatalf("append what the plan's index stated: %v", err)
	}
	held, err := graphstore.CurrentOut(ctx, tx, BlockNodeKind, documentRef+"#t1", []string{"needs"})
	if err != nil {
		t.Fatalf("read the plan node's needs: %v", err)
	}
	if len(held) != 1 || held[0].ToKind != graphstore.KindIssue || held[0].ToID != "WIND-2" {
		t.Fatalf("the plan node states the needs %+v, want one tuple to issue:WIND-2", held)
	}
	var toKind, toID string
	if err := tx.QueryRow(ctx, `
		select to_kind, to_id from block_refs where from_node = $1 and kind = 'needs'
	`, documentRef+"#t1").Scan(&toKind, &toID); err != nil || toKind != graphstore.KindIssue || toID != "WIND-2" {
		t.Fatalf("the plan node's needs row names %s:%s (%v), want issue:WIND-2", toKind, toID, err)
	}
}

// indexAndCause runs one block index pass in the fixture's transaction and appends what it
// stated, at the sentinel, as a caller with no event of its own does.
func indexAndCause(t *testing.T, tx pgx.Tx, artifact string, versions Versions, markdown string, blocks []model.ArtifactBlock) {
	t.Helper()
	ctx := context.Background()
	_, won, write, err := IndexBlocks(ctx, tx, artifact, versions, markdown, blocks, nil, nil, TypedFacts{})
	if err != nil || !won {
		t.Fatalf("index %s: won %t, %v", artifact, won, err)
	}
	if err := graphstore.Caused(ctx, tx, write, graphstore.NoEvent); err != nil {
		t.Fatalf("append what the index of %s stated: %v", artifact, err)
	}
}

// requireChainReachesIssue fails unless the part_of chain the block node address climbs through,
// as the store holds it now, reaches issue's node within ten steps and passes no node twice.
func requireChainReachesIssue(t *testing.T, tx pgx.Tx, address, issue string) {
	t.Helper()
	kind, id := BlockNodeKind, address
	seen := map[string]bool{}
	for range 10 {
		if kind == graphstore.KindIssue && id == issue {
			return
		}
		if seen[kind+":"+id] {
			t.Fatalf("the chain up from %s passes %s:%s twice: a part_of cycle", address, kind, id)
		}
		seen[kind+":"+id] = true
		held, err := graphstore.CurrentOut(context.Background(), tx, kind, id, []string{graphstore.TuplePartOf})
		if err != nil || len(held) != 1 {
			t.Fatalf("%s:%s, on the chain up from %s, stands under %+v (%v), want exactly one part_of", kind, id, address, held, err)
		}
		kind, id = held[0].ToKind, held[0].ToID
	}
	t.Fatalf("the chain up from %s does not reach issue:%s within ten steps", address, issue)
}

// Two requirements of two documents of one issue, each serving the other: the spec's r1 serves the
// plan's r2 and r2 serves r1, indexed in either order. The first placed has no target standing
// yet and hangs under the issue, and the second hangs under it. A pass the store holds stale,
// which places every authored node again, then reads the first's serves, and a version giving the
// first a parent attribute naming the second places it again by that: each would hang it under a
// block of another document standing under it, which the pass reads off the store's chain of that
// block and passes over. So each node's chain reaches the issue, and no walk meets a cycle.
func TestTwoDocumentsThatServeEachOtherLeaveNoCycle(t *testing.T) {
	for _, first := range []string{"spec", "plan"} {
		t.Run(first+" first", func(t *testing.T) {
			ctx := context.Background()
			tx := windowFixture(t)
			type served struct{ artifact, refKey, block string }
			documents := map[string]*served{"spec": {block: "r1"}, "plan": {block: "r2"}}
			documents["spec"].artifact, documents["spec"].refKey = createReferenceArtifact(t, tx, "WIND-1", "WIND", "spec-md")
			if err := tx.QueryRow(ctx, `
				insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
				values ('WIND-1', 'WIND', 'plan-md', 'plan.md', 'doc', false, '{"kind":"user","id":"alice"}')
				returning id::text, ref_key
			`).Scan(&documents["plan"].artifact, &documents["plan"].refKey); err != nil {
				t.Fatalf("create the plan: %v", err)
			}
			other := map[string]string{"spec": "plan", "plan": "spec"}
			second := other[first]
			ref := func(name string) text.Ref {
				document := documents[name]
				return text.Ref{Kind: BlockNodeKind, IssueKey: "WIND-1", ID: strings.TrimPrefix(document.refKey, "WIND-1/"), Block: document.block}
			}
			// Each version is written once; a later pass over the same version indexes it again.
			type written struct {
				name   string
				number int
			}
			versions := map[written]time.Time{}
			index := func(name string, number int, parent bool) {
				t.Helper()
				document := documents[name]
				markdown, blocks := documentOf([]part{{document.block, "requirement", "It serves the other one."}})
				at, held := versions[written{name, number}]
				if !held {
					at = writeAuthoredVersion(t, tx, document.artifact, number, markdown, model.Actor{Kind: "user", ID: "alice"})
					versions[written{name, number}] = at
				}
				pointers := []Pointer{{Block: document.block, BlockType: "requirement", Kind: "serves", Target: ref(other[name]), Targets: []string{"criterion", "requirement"}}}
				if parent {
					pointers = append(pointers, Pointer{Block: document.block, BlockType: "requirement", Kind: pmdoc.ParentAttribute, Target: ref(other[name])})
				}
				_, won, write, err := IndexBlocks(ctx, tx, document.artifact, OneVersion(document.artifact, number, at), markdown, blocks, nil, pointers, TypedFacts{})
				if err != nil || !won {
					t.Fatalf("index version %d of the %s: won %t, %v", number, name, won, err)
				}
				if err := graphstore.Caused(ctx, tx, write, graphstore.NoEvent); err != nil {
					t.Fatalf("append what version %d of the %s stated: %v", number, name, err)
				}
			}
			address := func(name string) string { return documents[name].refKey + "#" + documents[name].block }
			index(first, 1, false)
			index(second, 1, false)
			if held, err := graphstore.CurrentOut(ctx, tx, BlockNodeKind, address(second), []string{graphstore.TuplePartOf}); err != nil ||
				len(held) != 1 || held[0].ToID != address(first) {
				t.Fatalf("the %s's requirement stands under %+v (%v), want the %s's, which it serves and which stood first", second, held, err, first)
			}
			// The store's statement of the first document goes stale, as another release's index
			// leaves it, and the drain marks it owed: the pass places its authored node again.
			if _, err := tx.Exec(ctx, `
				update document_block_versions set store_version = 0, store_owed_at = now() where artifact_id = $1::uuid
			`, documents[first].artifact); err != nil {
				t.Fatalf("leave the %s's statement stale: %v", first, err)
			}
			index(first, 1, false)
			index(first, 2, true)
			for _, name := range []string{first, second} {
				requireChainReachesIssue(t, tx, address(name), "WIND-1")
			}
		})
	}
}
