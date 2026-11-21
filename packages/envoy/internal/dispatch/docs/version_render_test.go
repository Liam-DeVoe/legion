package docs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// renderWho writes the render rig's tuples and reads its renders.
var renderWho = model.Actor{Kind: "user", ID: "alice"}

// The blocks of the render rig's spec, each as its text value holds it: the whole slice of the
// block, its children included and the blank line after it left out (D9 of this branch's plan).
// The memo is the issue's first child; the two criteria are authored nodes at the top; the two
// requirements are authored nodes under the first criterion, ranked 1a and 1B, which byte order
// takes B first; the box is a callout whose text holds the inner callout, a node placed by the
// text under it; the closing paragraph is the last top-level block. The check is a block of the
// Verification document under the first requirement, which no render of the spec takes. The
// intruder is a paragraph a test dates before the version written before the cutover.
const (
	renderMemo     = ":::callout{#memo kind=\"note\" title=\"\"}\nThe memo says why.\n:::"
	renderK1       = ":::criterion{#k1}\nI want the urgent list.\n:::"
	renderR1       = ":::requirement{#r1}\nAn urgent ask comes first.\n:::"
	renderR1Edited = ":::requirement{#r1}\nAn urgent ask comes first, always.\n:::"
	renderR2       = ":::requirement{#r2}\nAn ask keeps its urgency.\n:::"
	renderK2       = ":::criterion{#k2}\nI want it quick.\n:::"
	renderInner    = ":::callout{#inner kind=\"note\" title=\"\"}\nThe inner words.\n:::"
	renderBox      = "::::callout{#box kind=\"note\" title=\"\"}\nThe box holds:\n\n" + renderInner + "\n::::"
	renderP1       = "A closing paragraph."
	renderCheck    = ":::check{#c1}\nThe list reads the urgent asks first.\n:::"
	renderIntruder = "A paragraph dated before the cutover."
	// renderBefore is the version of the spec written before the cutover, which holds no row.
	renderBefore = "The words of the spec before the cutover.\n"
)

// joinRenderings is the rendering of the top-level renderings given, each joined to the next by
// one blank line, and the document's last line ended.
func joinRenderings(renderings ...string) string { return strings.Join(renderings, "\n\n") + "\n" }

// renderRig is the store the render steps share: DOC-1, converted into its room, whose spec a
// real settlement writes; DOC-2, converted too, whose spec's blocks are written as tuples straight
// into the store, version 2 of it written before the cutover and version 3 after, from the tree;
// and DOC-9, an issue that has not converted, whose blocks stand as the release before this one
// stores them.
type renderRig struct {
	database *store.Store
	settled  issueRoom
	room     issueRoom
	// tree is the ref_key of DOC-2's tree artifact, which the blocks of its room are addressed
	// under, and spec the ref_key of its spec, which each block's in_document names.
	tree, spec string
	// before is when version 2, the version before the cutover, was written; first the bytes the
	// first render of version 3 answered, and digest the digest of its record.
	before        time.Time
	first, digest string
}

// address is the store address of block id of DOC-2's room.
func (r *renderRig) address(id string) string { return r.tree + "#" + id }

// now is the database's clock, clock_timestamp(), which dates every tuple the rig writes, so the
// order of the rig's writes and of the versions' times is the database's own.
func (r *renderRig) now(t *testing.T) time.Time {
	t.Helper()
	var at time.Time
	if err := r.database.Pool.QueryRow(context.Background(), `select clock_timestamp()`).Scan(&at); err != nil {
		t.Fatalf("read the database's clock: %v", err)
	}
	return at
}

// write appends tuples straight into the store, in one transaction.
func (r *renderRig) write(t *testing.T, tuples ...graphstore.Tuple) {
	t.Helper()
	if err := pgx.BeginFunc(context.Background(), r.database.Pool, func(tx pgx.Tx) error {
		return graphstore.Write(context.Background(), tx, nil, tuples)
	}); err != nil {
		t.Fatalf("write %+v: %v", tuples, err)
	}
}

// relation is a tuple out of block id of DOC-2's room, at.
func (r *renderRig) relation(id, kind, toKind, toID string, asserted bool, at time.Time) graphstore.Tuple {
	return graphstore.Tuple{
		FromKind: refs.BlockNodeKind, FromID: r.address(id), Kind: kind, ToKind: toKind, ToID: toID,
		At: at, Who: renderWho, SourceEvent: graphstore.NoEvent, Asserted: asserted,
	}
}

// value is the value tuple of attribute on block id of DOC-2's room, at.
func (r *renderRig) value(id, attribute, value string, at time.Time) graphstore.Tuple {
	return r.relation(id, attribute, graphstore.KindValue, value, true, at)
}

// block is every tuple a pass states of block id of document, at: its in_document, its part_of
// to parent, its rank, its text and its placement.
func (r *renderRig) block(id, document string, parent refs.Target, rank, text, placement string, at time.Time) []graphstore.Tuple {
	return []graphstore.Tuple{
		r.relation(id, refs.TupleInDocument, graphstore.KindArtifact, document, true, at),
		r.relation(id, graphstore.TuplePartOf, parent.Kind, parent.ID, true, at),
		r.value(id, refs.AttributeRank, rank, at),
		r.value(id, graphstore.AttributeText, text, at),
		r.value(id, refs.AttributePlacement, placement, at),
	}
}

// issue is DOC-2's node, and under the block id of its room.
func (r *renderRig) issue() refs.Target {
	return refs.Target{Kind: graphstore.KindIssue, ID: r.room.issue}
}
func (r *renderRig) under(id string) refs.Target {
	return refs.Target{Kind: refs.BlockNodeKind, ID: r.address(id)}
}

// renderVersion is RenderVersion of version number of artifactID.
func (r *renderRig) renderVersion(t *testing.T, artifactID string, number int) Rendering {
	t.Helper()
	rendering, err := RenderVersion(context.Background(), r.database.Pool, renderWho, artifactID, number)
	if err != nil {
		t.Fatalf("render version %d of %s: %v", number, artifactID, err)
	}
	return rendering
}

// renderAt is RenderAt of artifactID as of at.
func (r *renderRig) renderAt(t *testing.T, artifactID string, at time.Time) Rendering {
	t.Helper()
	rendering, err := RenderAt(context.Background(), r.database.Pool, renderWho, artifactID, at)
	if err != nil {
		t.Fatalf("render %s as of %s: %v", artifactID, at, err)
	}
	return rendering
}

// requireVersionFromTree fails unless version 3 of the spec renders from the tree as want.
func (r *renderRig) requireVersionFromTree(t *testing.T, what, want string) {
	t.Helper()
	if got := r.renderVersion(t, r.room.spec, 3); got.Source != RenderedFromTree || got.Markdown != want {
		t.Errorf("%s: version 3 renders %q from %q, want %q from the tree", what, got.Markdown, got.Source, want)
	}
}

// requireHead fails unless the spec, rendered as of the database's clock now, reads want.
func (r *renderRig) requireHead(t *testing.T, what, want string) {
	t.Helper()
	if got := r.renderAt(t, r.room.spec, r.now(t)); got.Source != RenderedFromTree || got.Markdown != want {
		t.Errorf("%s: the head renders %q from %q, want %q from the tree", what, got.Markdown, got.Source, want)
	}
}

// versionDigest is artifact_version_text_sha256 of version number of artifactID.
func (r *renderRig) versionDigest(t *testing.T, artifactID string, number int) string {
	t.Helper()
	var digest *string
	if err := r.database.Pool.QueryRow(context.Background(), `select artifact_version_text_sha256($1::uuid, $2)`, artifactID, number).Scan(&digest); err != nil {
		t.Fatalf("read the digest of version %d of %s: %v", number, artifactID, err)
	}
	if digest == nil {
		return ""
	}
	return *digest
}

// renderSum is the lower-case hex sha256 of text.
func renderSum(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// writeVersionFromTree writes version number of the spec in tx as this child's writer writes one:
// the spec rendered as of the head's cut, stored as the version's markdown, and its version_renders
// row holding the digest of the spec's record read at that cut. It answers the rendering and the
// record.
func (r *renderRig) writeVersionFromTree(t *testing.T, tx pgx.Tx, number int) (Rendering, QueryRecord) {
	t.Helper()
	ctx := context.Background()
	cut, err := refs.HeadCut(ctx, tx)
	if err != nil {
		t.Fatalf("read the head's cut: %v", err)
	}
	rendering, err := RenderAt(ctx, tx, renderWho, r.room.spec, cut.At)
	if err != nil {
		t.Fatalf("render the spec at the head: %v", err)
	}
	record, err := ReadQueryRecord(ctx, tx, r.room.issue, graphstore.QuerySpec, cut)
	if err != nil {
		t.Fatalf("read the spec's record at the head: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		values ($1, $2, $3, '[{"kind":"user","id":"alice"}]')`, r.room.spec, number, rendering.Markdown); err != nil {
		t.Fatalf("write version %d of the spec: %v", number, err)
	}
	if err := store.RunWrite(ctx, tx, func(b *store.WriteBatch) error {
		return QueueVersionRender(b, r.room.spec, number, record)
	}); err != nil {
		t.Fatalf("write the version_renders row of version %d: %v", number, err)
	}
	return rendering, record
}

// versionRows is how many artifact_versions and version_renders rows version number of the spec
// holds.
func (r *renderRig) versionRows(t *testing.T, q Queryer, number int) (versions, renders int) {
	t.Helper()
	if err := q.QueryRow(context.Background(), `
		select (select count(*) from artifact_versions where artifact_id = $1 and number = $2),
		       (select count(*) from version_renders where artifact_id = $1 and number = $2)`,
		r.room.spec, number).Scan(&versions, &renders); err != nil {
		t.Fatalf("count version %d's rows: %v", number, err)
	}
	return versions, renders
}

// writeUnconvertedIssue writes by SQL, as the release before this one stores them, the spec of
// DOC-9, an issue with no issue_trees row and no document_rooms row: one version, the rows of its
// two blocks in document_blocks and document_block_texts, and their nodes, which hold no text
// value; the document indexed at that version by a pass of rule 1, so it owes no index, and stated
// by no pass of rule 2. It answers how many block rows it wrote.
func writeUnconvertedIssue(t *testing.T, database *store.Store) int {
	t.Helper()
	ctx := context.Background()
	words := []string{"The first words of an issue that has not converted.", "Its second words."}
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			insert into issues (key, project_key, number, title, created_by, rank)
			values ('DOC-9', 'DOC', 9, 'An issue that has not converted', '{"kind":"user","id":"alice"}', 'W')`); err != nil {
			return err
		}
		var id, refKey string
		if err := tx.QueryRow(ctx, `
			insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
			values ('DOC-9', 'DOC', 'spec', 'spec.md', 'doc', true, '{"kind":"user","id":"alice"}')
			returning id::text, ref_key`).Scan(&id, &refKey); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			insert into artifact_versions (artifact_id, number, markdown, authors)
			values ($1, 1, $2, '[{"kind":"user","id":"alice"}]')`, id, joinRenderings(words...)); err != nil {
			return err
		}
		for index, text := range words {
			block := "u" + string(rune('1'+index))
			address := refKey + "#" + block
			if _, err := tx.Exec(ctx, `
				insert into graph_nodes (kind, id, supertag, visibility, state, made_at, made_by, source_event)
				values ('block', $1, 'block', 'project', 'live', now(), '{"kind":"user","id":"alice"}', 0)`, address); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				insert into document_blocks (artifact_id, block_id, node_id, type, text_sha256, excerpt, changed_version, changed_at, node_kind, node_address)
				values ($1, $2, $3, 'paragraph', $4, $5, 1, now(), 'block', $3)`, id, block, address, renderSum(text), text); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				insert into document_block_texts (artifact_id, block_id, version, text) values ($1, $2, 1, $3)`, id, block, text); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			insert into document_block_versions (artifact_id, version, store_version) values ($1, 1, 1)
			on conflict (artifact_id) do update
			set version = 1, store_version = 1, store_owed_at = null, stated_version = null, stated_rule = null`, id)
		return err
	}); err != nil {
		t.Fatalf("write the issue that has not converted: %v", err)
	}
	return len(words)
}

// A version this child's writer wrote renders again from the tree (R15 of this branch's spec;
// D2, D5, D6, D9 and D10 of its plan). The test writes a subtree of DOC-2's spec straight into
// the store, writes version 3 from it - its rendering and its version_renders row, holding the
// digest of the spec's record, in one transaction - and then, one change at a time, edits a
// requirement's text, re-parents another, re-orders two siblings and retracts a paragraph's
// part_of, each dated after the version: the version renders its first bytes after each, the head
// renders each change, and two renders of the version at two times read one byte string. The
// render prints each node it takes once, a node whose text its parent's already holds left out,
// and orders two siblings ranked a and B with B first, byte for byte; it refuses to run where
// select 'a' < 'B' is false. A transaction rolled back leaves neither the version nor its row. The
// digest of a version with a row is its record's, and of one without its stored markdown's. A
// tuple dated before the version written before the cutover moves nothing of it, though the tree
// at its time holds that tuple; it moves the version that holds a row, which the census of the
// versions then reads as a difference; and with that row deleted the version answers its stored
// bytes and says so. A version an erase deletes takes its version_renders row with it, and a row
// a delete leaves behind is a difference of the census of the rendered versions. A version whose
// markdown is null says it holds no bytes. The head of a spec a real settlement wrote renders as
// that version's stored markdown. The census of document_block_texts reads no difference in
// either direction over the converted issues, and prints the rows of DOC-9 as the class of unstated
// rows of an issue that has not converted, with their count; and document_block_texts keeps the
// columns 0121_block_graph.up.sql creates. Last, a version the real path settles whose requirement
// stands under the criterion it serves and not after the paragraph before it takes a row, and the
// step logs what the census of the rendered versions reads of it, with both byte strings.
func TestTheTreeRendersAPostCutoverVersionFromItsTuples(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	var collated bool
	if err := database.Pool.QueryRow(ctx, `select 'a' < 'B'`).Scan(&collated); err != nil {
		t.Fatalf("read the database's collation of a and B: %v", err)
	}
	if !collated {
		t.Fatal("this database orders 'B' before 'a' by its own collation, so it cannot tell a render that orders ranks byte for byte from one that forgot to: run this test where select 'a' < 'B' is true")
	}
	service, server := newTestServiceInstance(t, database)
	rig := &renderRig{database: database, settled: createIssueRoom(t, database, 1), room: createIssueRoom(t, database, 2)}
	if err := database.Pool.QueryRow(ctx, `
		select (select ref_key from artifacts where id = $1), (select ref_key from artifacts where id = $2)`,
		rig.room.tree, rig.room.spec).Scan(&rig.tree, &rig.spec); err != nil {
		t.Fatalf("read DOC-2's ref_keys: %v", err)
	}

	// DOC-1's spec, written by a real settlement of its room once the peer's update has landed.
	peer := connectPeer(t, server.URL, rig.settled.spec, "alice")
	stored := roomUpdateCount(t, database, rig.settled.tree)
	if _, err := peer.Send(writeParagraph(peer.Doc, rig.settled.spec, "Spec words.")); err != nil {
		t.Fatalf("write into DOC-1's spec: %v", err)
	}
	if err := peer.WaitForText(fragmentMarkdown(rig.settled.spec), "Spec words.\n", roomWait(t)); err != nil {
		t.Fatalf("a peer's copy of DOC-1's room: %v", err)
	}
	waitForStoredUpdates(t, database, rig.settled.tree, stored)
	if err := service.waitForDurableAppends(ctx, rig.settled.tree); err != nil {
		t.Fatalf("wait for DOC-1's appends: %v", err)
	}
	if err := service.settleOwedNow(ctx, rig.settled.tree, time.Now().Add(roomWait(t))); err != nil {
		t.Fatalf("settle DOC-1's room: %v", err)
	}
	unconverted := writeUnconvertedIssue(t, database)

	// DOC-2: version 2 of the spec, written before the cutover, and then the spec's subtree.
	if err := database.Pool.QueryRow(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors, created_at)
		values ($1, 2, $2, '[{"kind":"user","id":"alice"}]', clock_timestamp()) returning created_at`,
		rig.room.spec, renderBefore).Scan(&rig.before); err != nil {
		t.Fatalf("write the version before the cutover: %v", err)
	}
	at := rig.now(t)
	var subtree []graphstore.Tuple
	for _, block := range []struct {
		id, document    string
		parent          refs.Target
		rank, text, how string
	}{
		{"memo", rig.spec, rig.issue(), "1c", renderMemo, refs.PlacementText},
		{"k1", rig.spec, rig.issue(), "1g", renderK1, refs.PlacementAuthored},
		{"r1", rig.spec, rig.under("k1"), "1a", renderR1, refs.PlacementAuthored},
		{"r2", rig.spec, rig.under("k1"), "1B", renderR2, refs.PlacementAuthored},
		{"k2", rig.spec, rig.issue(), "1m", renderK2, refs.PlacementAuthored},
		{"box", rig.spec, rig.issue(), "1p", renderBox, refs.PlacementText},
		{"inner", rig.spec, rig.under("box"), "1q", renderInner, refs.PlacementText},
		{"p1", rig.spec, rig.issue(), "1t", renderP1, refs.PlacementText},
		{"c1", rig.room.issue + "/verification-md", rig.under("r1"), "3a", renderCheck, refs.PlacementAuthored},
	} {
		subtree = append(subtree, rig.block(block.id, block.document, block.parent, block.rank, block.text, block.how, at)...)
	}
	rig.write(t, subtree...)

	for _, step := range []struct {
		name string
		run  func(t *testing.T, rig *renderRig)
	}{
		{"a version written from the tree holds its row, written in the version's own transaction", renderStepVersionRow},
		{"a text edit after the version leaves the version's text as it stood", renderStepText},
		{"a re-parent after the version leaves the version's parent as it stood", renderStepParent},
		{"a re-order after the version leaves the version's ranks as they stood", renderStepRanks},
		{"a part_of retracted after the version leaves the node in it", renderStepRetraction},
		{"two renders of the version at two times read one byte string, and the head is the renderer at now", renderStepTwoTimes},
		{"the digest of a version with a row is its record's, and of one without its stored markdown's", renderStepDigests},
		{"the census reads the converted issues' texts both ways and the unconverted issue's rows as a class", func(t *testing.T, rig *renderRig) {
			renderStepCensus(t, rig, unconverted)
		}},
		{"a tuple dated before the version written before the cutover moves nothing of it", renderStepBoundary},
		{"with its row deleted a version answers its stored bytes and says so", renderStepRowDeleted},
		{"a version an erase deletes takes its row, and a row left behind is a difference", renderStepErasedVersion},
		{"a version whose markdown is null says it holds no bytes", renderStepNoBytes},
		{"document_block_texts keeps the columns 0121_block_graph creates", renderStepTextColumns},
		{"a settled version whose authored block stands under a parent other than the block before it", func(t *testing.T, rig *renderRig) {
			renderStepAuthoredPlacement(t, rig, service)
		}},
	} {
		t.Run(step.name, func(t *testing.T) { step.run(t, rig) })
	}
}

// renderFirst is the rendering of version 3, the spec's subtree as written: the requirement
// ranked 1B before the one ranked 1a, and the inner callout once, inside the box.
var renderFirst = joinRenderings(renderMemo, renderK1, renderR2, renderR1, renderK2, renderBox, renderP1)

func renderStepVersionRow(t *testing.T, rig *renderRig) {
	ctx := context.Background()
	tx, err := rig.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the version's transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	rendering, record := rig.writeVersionFromTree(t, tx, 3)
	if rendering.Source != RenderedFromTree || rendering.Markdown != renderFirst {
		t.Fatalf("the head renders %q from %q, want %q from the tree", rendering.Markdown, rendering.Source, renderFirst)
	}
	if strings.Index(rendering.Markdown, renderR2) > strings.Index(rendering.Markdown, renderR1) {
		t.Errorf("the render takes r1 (rank 1a) before r2 (rank 1B), want B first, byte for byte")
	}
	if strings.Count(rendering.Markdown, renderInner) != 1 {
		t.Errorf("the render holds the inner callout %d times, want once, inside its box", strings.Count(rendering.Markdown, renderInner))
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit version 3: %v", err)
	}
	rig.digest = record.Digest
	var digest string
	var at time.Time
	var readSeq int64
	if err := rig.database.Pool.QueryRow(ctx, `
		select record_sha256, at, read_seq from version_renders where artifact_id = $1 and number = 3`, rig.room.spec).Scan(&digest, &at, &readSeq); err != nil {
		t.Fatalf("read version 3's row: %v", err)
	}
	if digest != record.Digest || !at.Equal(record.Cut.At) || readSeq != record.Cut.ReadSeq {
		t.Errorf("version 3's row holds %s at %s and seq %d, want the record's %s at %s and seq %d",
			digest, at, readSeq, record.Digest, record.Cut.At, record.Cut.ReadSeq)
	}
	if renderSum(renderFirst) == record.Digest {
		t.Errorf("the record's digest is the sha256 of the version's markdown, %s", record.Digest)
	}

	// The same write rolled back leaves neither the version nor its row.
	rolled, err := rig.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the rolled back version's transaction: %v", err)
	}
	rig.writeVersionFromTree(t, rolled, 4)
	if versions, renders := rig.versionRows(t, rolled, 4); versions != 1 || renders != 1 {
		t.Errorf("inside its transaction version 4 holds %d versions and %d rows, want one of each", versions, renders)
	}
	if err := rolled.Rollback(ctx); err != nil {
		t.Fatalf("roll version 4 back: %v", err)
	}
	if versions, renders := rig.versionRows(t, rig.database.Pool, 4); versions != 0 || renders != 0 {
		t.Errorf("after its rollback version 4 holds %d versions and %d rows, want neither", versions, renders)
	}
	rig.first = rig.renderVersion(t, rig.room.spec, 3).Markdown
	rig.requireVersionFromTree(t, "as written", renderFirst)
}

func renderStepText(t *testing.T, rig *renderRig) {
	rig.write(t, rig.value("r1", graphstore.AttributeText, renderR1Edited, rig.now(t)))
	rig.requireVersionFromTree(t, "after r1's text edit", renderFirst)
	rig.requireHead(t, "after r1's text edit", joinRenderings(renderMemo, renderK1, renderR2, renderR1Edited, renderK2, renderBox, renderP1))
}

func renderStepParent(t *testing.T, rig *renderRig) {
	at := rig.now(t)
	rig.write(t,
		rig.relation("r2", graphstore.TuplePartOf, refs.BlockNodeKind, rig.address("k1"), false, at),
		rig.relation("r2", graphstore.TuplePartOf, refs.BlockNodeKind, rig.address("k2"), true, at))
	rig.requireVersionFromTree(t, "after r2 moved under k2", renderFirst)
	rig.requireHead(t, "after r2 moved under k2", joinRenderings(renderMemo, renderK1, renderR1Edited, renderK2, renderR2, renderBox, renderP1))
}

func renderStepRanks(t *testing.T, rig *renderRig) {
	at := rig.now(t)
	rig.write(t, rig.value("box", refs.AttributeRank, "1t", at), rig.value("p1", refs.AttributeRank, "1p", at))
	rig.requireVersionFromTree(t, "after the box and the closing paragraph swapped ranks", renderFirst)
	rig.requireHead(t, "after the box and the closing paragraph swapped ranks",
		joinRenderings(renderMemo, renderK1, renderR1Edited, renderK2, renderR2, renderP1, renderBox))
}

func renderStepRetraction(t *testing.T, rig *renderRig) {
	rig.write(t, rig.relation("p1", graphstore.TuplePartOf, graphstore.KindIssue, rig.room.issue, false, rig.now(t)))
	rig.requireVersionFromTree(t, "after the closing paragraph's part_of was retracted", renderFirst)
	rig.requireHead(t, "after the closing paragraph's part_of was retracted",
		joinRenderings(renderMemo, renderK1, renderR1Edited, renderK2, renderR2, renderBox))
}

func renderStepTwoTimes(t *testing.T, rig *renderRig) {
	if again := rig.renderVersion(t, rig.room.spec, 3).Markdown; again != rig.first || rig.first != renderFirst {
		t.Errorf("version 3 rendered after every change reads %q, and first read %q, want one byte string, %q", again, rig.first, renderFirst)
	}
	var stored string
	if err := rig.database.Pool.QueryRow(context.Background(),
		`select markdown from artifact_versions where artifact_id = $1 and number = 3`, rig.room.spec).Scan(&stored); err != nil {
		t.Fatalf("read version 3's markdown: %v", err)
	}
	if stored != rig.first {
		t.Errorf("version 3 stores %q, want its own render, %q", stored, rig.first)
	}
	// The head of DOC-1's spec, which a real settlement indexed, is that version's markdown.
	var settled string
	if err := rig.database.Pool.QueryRow(context.Background(), `
		select markdown from artifact_versions where artifact_id = $1 order by number desc limit 1`, rig.settled.spec).Scan(&settled); err != nil {
		t.Fatalf("read DOC-1's newest spec version: %v", err)
	}
	if settled != "Spec words.\n" {
		t.Fatalf("DOC-1's newest spec version holds %q, want the settlement's \"Spec words.\\n\"", settled)
	}
	if head := rig.renderAt(t, rig.settled.spec, rig.now(t)); head.Source != RenderedFromTree || head.Markdown != settled {
		t.Errorf("the head of DOC-1's spec renders %q from %q, want its settled version's %q from the tree", head.Markdown, head.Source, settled)
	}
}

func renderStepDigests(t *testing.T, rig *renderRig) {
	ctx := context.Background()
	if got := rig.versionDigest(t, rig.room.spec, 2); got != renderSum(renderBefore) {
		t.Errorf("the version before the cutover digests to %s, want the sha256 of its stored markdown, %s", got, renderSum(renderBefore))
	}
	if got := rig.versionDigest(t, rig.room.spec, 3); got != rig.digest {
		t.Errorf("version 3 digests to %s, want its record's digest, %s", got, rig.digest)
	}
	for number, want := range map[int]string{2: renderSum(renderBefore), 3: rig.digest} {
		var pinned string
		if err := rig.database.Pool.QueryRow(ctx, `
			insert into artifact_reviews (artifact_id, version, state, actor)
			values ($1, $2, 'approved', '{"kind":"user","id":"alice"}') returning content_sha256`, rig.room.spec, number).Scan(&pinned); err != nil {
			t.Fatalf("approve version %d: %v", number, err)
		}
		if pinned != want {
			t.Errorf("an approval of version %d pins %s, want %s", number, pinned, want)
		}
	}
}

func renderStepCensus(t *testing.T, rig *renderRig, unconverted int) {
	census := treeCensus(t, rig.database)
	texts := treeCensusKind(t, census, "block_text")
	for _, difference := range slices.Concat(texts.ReportedRows, texts.ReportedNodes) {
		t.Errorf("the census reads a block_text difference at %s: %s", difference.At, difference.Cause)
	}
	if texts.RowDifferences != 0 || texts.NodeDifferences != 0 || texts.Rows == 0 || texts.Nodes == 0 {
		t.Errorf("the census reads %d rows and %d nodes of block_text with %d and %d differences, want the converted issues' blocks and none",
			texts.Rows, texts.Nodes, texts.RowDifferences, texts.NodeDifferences)
	}
	if !texts.CountsUnstated || texts.Unstated != int64(unconverted) {
		t.Errorf("the census counts %d unstated block_text rows of an issue that has not converted (held %t), want DOC-9's %d, and no other",
			texts.Unstated, texts.CountsUnstated, unconverted)
	}
	var printed bytes.Buffer
	census.Write(&printed)
	if line := "census: store kind=block_text unstated_rows=" + strconv.Itoa(unconverted) + " "; !strings.Contains(printed.String(), line) {
		t.Errorf("the census prints no line %q:\n%s", line, printed.String())
	}
	renders, err := ReadVersionRenderCensus(context.Background(), rig.database.Pool)
	if err != nil {
		t.Fatalf("read the census of the versions version_renders holds: %v", err)
	}
	if renders.Versions != 1 || renders.Differences != 0 {
		t.Errorf("the census of the rendered versions compares %d and reads %d differences (%+v), want version 3 alone and none",
			renders.Versions, renders.Differences, renders.Reported)
	}
}

func renderStepBoundary(t *testing.T, rig *renderRig) {
	ctx := context.Background()
	rig.write(t, rig.block("intruder", rig.spec, rig.issue(), "1d", renderIntruder, refs.PlacementText, rig.before.Add(-time.Minute))...)
	if tree := rig.renderAt(t, rig.room.spec, rig.before); !strings.Contains(tree.Markdown, renderIntruder) {
		t.Fatalf("the tree as of the version before the cutover renders %q, without the paragraph dated before it, so this case proves nothing", tree.Markdown)
	}
	if got := rig.renderVersion(t, rig.room.spec, 2); got.Source != StoredBytes || got.Markdown != renderBefore {
		t.Errorf("the version before the cutover renders %q from %q, want its stored %q, the stored bytes", got.Markdown, got.Source, renderBefore)
	}
	// The version that holds a row renders from the tree, which now holds the paragraph dated
	// before it: the census of the rendered versions reads it.
	if got := rig.renderVersion(t, rig.room.spec, 3); got.Source != RenderedFromTree || !strings.Contains(got.Markdown, renderIntruder) {
		t.Errorf("version 3 renders %q from %q, want the tree's, with the paragraph dated before it", got.Markdown, got.Source)
	}
	renders, err := ReadVersionRenderCensus(ctx, rig.database.Pool)
	if err != nil {
		t.Fatalf("read the census of the versions version_renders holds: %v", err)
	}
	if renders.Versions != 1 || renders.Differences != 1 || len(renders.Reported) != 1 || !strings.Contains(renders.Reported[0].At, rig.spec+"@v3") {
		t.Errorf("the census of the rendered versions compares %d and reads %d differences (%+v), want version 3's", renders.Versions, renders.Differences, renders.Reported)
	}
}

func renderStepRowDeleted(t *testing.T, rig *renderRig) {
	ctx := context.Background()
	if _, err := rig.database.Pool.Exec(ctx, `delete from version_renders where artifact_id = $1 and number = 3`, rig.room.spec); err != nil {
		t.Fatalf("delete version 3's row: %v", err)
	}
	if got := rig.renderVersion(t, rig.room.spec, 3); got.Source != StoredBytes || got.Markdown != renderFirst {
		t.Errorf("version 3 with no row renders %q from %q, want its stored %q, the stored bytes", got.Markdown, got.Source, renderFirst)
	}
	if got := rig.versionDigest(t, rig.room.spec, 3); got != renderSum(renderFirst) {
		t.Errorf("version 3 with no row digests to %s, want the sha256 of its stored markdown, %s", got, renderSum(renderFirst))
	}
	renders, err := ReadVersionRenderCensus(ctx, rig.database.Pool)
	if err != nil {
		t.Fatalf("read the census of the versions version_renders holds: %v", err)
	}
	if renders.Versions != 0 || renders.Differences != 0 {
		t.Errorf("the census of the rendered versions compares %d and reads %d differences, want none: no version holds a row", renders.Versions, renders.Differences)
	}
}

// readRenderCensus is the census of the versions version_renders holds.
func (r *renderRig) readRenderCensus(t *testing.T) graphcensus.VersionRenderCensus {
	t.Helper()
	renders, err := ReadVersionRenderCensus(context.Background(), r.database.Pool)
	if err != nil {
		t.Fatalf("read the census of the versions version_renders holds: %v", err)
	}
	return renders
}

// eraseVersions deletes version numbers[i] of the spec as the erase deletes a version holding
// erased text: its version_renders row, by docs' one statement of that delete, and then the
// version, in one transaction; with keepRows the row is left behind, as a deleter that forgot it
// would leave it.
func (r *renderRig) eraseVersions(t *testing.T, keepRows bool, numbers ...int32) {
	t.Helper()
	ctx := context.Background()
	artifacts := make([]string, len(numbers))
	for index := range numbers {
		artifacts[index] = r.room.spec
	}
	if err := pgx.BeginFunc(ctx, r.database.Pool, func(tx pgx.Tx) error {
		if !keepRows {
			if _, err := ForgetVersionRenders(ctx, tx, artifacts, numbers); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			delete from artifact_versions where exists (
				select 1 from unnest($1::uuid[], $2::int[]) as doomed (artifact, number)
				where doomed.artifact = artifact_versions.artifact_id and doomed.number = artifact_versions.number)`, artifacts, numbers)
		return err
	}); err != nil {
		t.Fatalf("delete versions %v of the spec: %v", numbers, err)
	}
}

func renderStepErasedVersion(t *testing.T, rig *renderRig) {
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, rig.database.Pool, func(tx pgx.Tx) error {
		rig.writeVersionFromTree(t, tx, 4)
		rig.writeVersionFromTree(t, tx, 5)
		return nil
	}); err != nil {
		t.Fatalf("write versions 4 and 5 from the tree: %v", err)
	}
	if renders := rig.readRenderCensus(t); renders.Versions != 2 || renders.Differences != 0 {
		t.Fatalf("with versions 4 and 5 written the census of the rendered versions compares %d and reads %d differences (%+v), want both and none",
			renders.Versions, renders.Differences, renders.Reported)
	}
	rig.eraseVersions(t, false, 4)
	if versions, renders := rig.versionRows(t, rig.database.Pool, 4); versions != 0 || renders != 0 {
		t.Errorf("after its erase version 4 holds %d versions and %d rows, want neither", versions, renders)
	}
	if renders := rig.readRenderCensus(t); renders.Versions != 1 || renders.Differences != 0 {
		t.Errorf("after version 4's erase the census of the rendered versions compares %d and reads %d differences (%+v), want version 5 alone and none",
			renders.Versions, renders.Differences, renders.Reported)
	}
	// The control: a delete of version 5 that leaves its row behind is read as a difference.
	rig.eraseVersions(t, true, 5)
	renders := rig.readRenderCensus(t)
	if renders.Versions != 1 || renders.Differences != 1 || len(renders.Reported) != 1 || !strings.Contains(renders.Reported[0].At, rig.spec+"@v5") {
		t.Errorf("with version 5 deleted and its row left behind the census of the rendered versions compares %d and reads %d differences (%+v), want the row of version 5",
			renders.Versions, renders.Differences, renders.Reported)
	} else {
		t.Logf("the census reads the row left behind at %s: %s", renders.Reported[0].At, renders.Reported[0].Cause)
	}
	if err := pgx.BeginFunc(ctx, rig.database.Pool, func(tx pgx.Tx) error {
		_, err := ForgetVersionRenders(ctx, tx, []string{rig.room.spec}, []int32{5})
		return err
	}); err != nil {
		t.Fatalf("forget version 5's row: %v", err)
	}
	if renders := rig.readRenderCensus(t); renders.Versions != 0 || renders.Differences != 0 {
		t.Errorf("with version 5's row forgotten the census of the rendered versions compares %d and reads %d differences, want none", renders.Versions, renders.Differences)
	}
}

// renderAuthoredSpec is the spec of CORE-1, an issue's spec the real version path writes: a
// criterion, a paragraph, and a requirement that serves the criterion, which the block index
// places under the criterion rather than after the paragraph it follows.
const renderAuthoredSpec = ":::criterion{#k1}\nI want the urgent list.\n:::\n\n" +
	"A paragraph between the two.\n\n" +
	":::requirement{#r1 serves=\"dispatch://CORE-1/spec#b-k1\"}\nAn urgent ask comes first.\n:::\n"

// A version the real path settles, whose requirement the block index places under the criterion
// it serves and not after the paragraph before it, takes a version_renders row as the pass that
// writes a version's tree will write it; the census of the rendered versions then reads that
// version. The step records what it reads, the stored bytes and the render's, for the rule of the
// render's order to be decided on; it holds the census to reading the version.
func renderStepAuthoredPlacement(t *testing.T, rig *renderRig, service *Service) {
	ctx := context.Background()
	if _, err := rig.database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee) values ('CORE', 'Core', 'alice') on conflict (key) do nothing;
		insert into issues (key, project_key, number, title, created_by, rank)
		values ('CORE-1', 'CORE', 1, 'One', '{"kind":"user","id":"alice"}', 'U')`); err != nil {
		t.Fatalf("seed project CORE: %v", err)
	}
	spec, written := treeDocument(t, rig.database, service, "CORE-1", "spec", true, "", renderAuthoredSpec)
	if err := pgx.BeginFunc(ctx, rig.database.Pool, func(tx pgx.Tx) error {
		cut, err := refs.HeadCut(ctx, tx)
		if err != nil {
			return err
		}
		record, err := ReadQueryRecord(ctx, tx, "CORE-1", graphstore.QuerySpec, cut)
		if err != nil {
			return err
		}
		return store.RunWrite(ctx, tx, func(b *store.WriteBatch) error {
			return QueueVersionRender(b, spec, written.Version.Number, record)
		})
	}); err != nil {
		t.Fatalf("write the version_renders row of CORE-1's spec: %v", err)
	}
	var stored string
	if err := rig.database.Pool.QueryRow(ctx, `select markdown from artifact_versions where artifact_id = $1 and number = $2`,
		spec, written.Version.Number).Scan(&stored); err != nil {
		t.Fatalf("read CORE-1's spec version: %v", err)
	}
	rendered := rig.renderVersion(t, spec, written.Version.Number)
	renders := rig.readRenderCensus(t)
	if renders.Versions != 1 {
		t.Errorf("the census of the rendered versions compares %d versions, want CORE-1's spec's version alone", renders.Versions)
	}
	t.Logf("CORE-1's spec version %d stores %q", written.Version.Number, stored)
	t.Logf("CORE-1's spec version %d renders %q from %q", written.Version.Number, rendered.Markdown, rendered.Source)
	t.Logf("the census of the rendered versions compares %d and reads %d differences: %+v", renders.Versions, renders.Differences, renders.Reported)
}

func renderStepNoBytes(t *testing.T, rig *renderRig) {
	var file string
	if err := rig.database.Pool.QueryRow(context.Background(), `
		with file as (
			insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
			values ($1, 'DOC', 'diagram-bin', 'diagram.bin', 'file', false, '{"kind":"user","id":"alice"}')
			returning id
		), version as (
			insert into artifact_versions (artifact_id, number, content, mime, size, sha256, authors)
			select id, 1, '\x0001'::bytea, 'application/octet-stream', 2, $2, '[{"kind":"user","id":"alice"}]' from file
		)
		select id::text from file`, rig.room.issue, renderSum("\x00\x01")).Scan(&file); err != nil {
		t.Fatalf("write a file: %v", err)
	}
	if got := rig.renderVersion(t, file, 1); got.Source != NoBytes || got.Markdown != "" {
		t.Errorf("a version whose markdown is null renders %q from %q, want no bytes", got.Markdown, got.Source)
	}
	// An empty markdown is a document: the spec's first version holds one.
	if got := rig.renderVersion(t, rig.room.spec, 1); got.Source != StoredBytes || got.Markdown != "" {
		t.Errorf("the spec's empty first version renders %q from %q, want its stored empty markdown", got.Markdown, got.Source)
	}
}

func renderStepTextColumns(t *testing.T, rig *renderRig) {
	rows, err := rig.database.Pool.Query(context.Background(), `
		select column_name || ' ' || data_type || ' ' || is_nullable
		from information_schema.columns
		where table_schema = 'public' and table_name = 'document_block_texts'
		order by ordinal_position`)
	if err != nil {
		t.Fatalf("read document_block_texts' columns: %v", err)
	}
	columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read document_block_texts' columns: %v", err)
	}
	want := []string{"artifact_id uuid NO", "block_id text NO", "version integer NO", "text text YES"}
	if !slices.Equal(columns, want) {
		t.Errorf("document_block_texts holds the columns %v, want %v as 0121_block_graph.up.sql creates them", columns, want)
	}
}
