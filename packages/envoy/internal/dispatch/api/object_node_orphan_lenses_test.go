package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// lensedDocument is a document of a settleRig's issue that alice made by typing a title, at
// address, with her trade-off analysis LO on it holding one finding FO and the second agent's lens
// LP on FO, reserved for itself. LO records the document's row (graphstore.AttributeArtifactID).
// It holds no lens on a block of the document: a removable document holds no block.
type lensedDocument struct {
	settle   settleRig
	document model.Artifact
	address  string
	lo, lp   lensInvoked
	fo       lensFinding
}

func newLensedDocument(t *testing.T, name string) lensedDocument {
	t.Helper()
	settle := newSettleRig(t)
	document := emptyDocument(t, settle.handler, settle.issue.Key, name)
	made := lensedDocument{settle: settle, document: document, address: "dispatch://" + settle.issue.Key + "/artifact/" + document.Slug}
	invoke := func(name string, as sender, body map[string]any) lensInvoked {
		t.Helper()
		response := as(http.MethodPost, "/api/v1/issues/"+settle.issue.Key+"/lenses", body)
		if response.Code != http.StatusCreated {
			t.Fatalf("%s: status=%d body=%s", name, response.Code, response.Body.String())
		}
		return decodeBody[lensInvoked](t, response)
	}
	made.lo = invoke("alice's trade-off analysis LO on the document", settle.asAlice, map[string]any{
		"on": made.address, "kind": "trade-off", "findings": []string{"FO, the one finding of LO."},
	})
	made.fo = made.lo.Findings[0]
	made.lp = invoke("the second agent's lens LP on FO", settle.asSecond, map[string]any{
		"on": made.fo.Address, "kind": "trade-off", "visibility": graphstore.VisibilityAuthor,
	})
	return made
}

// stood is the address of LO, FO and LP, each the removed document's.
func (d lensedDocument) stood() []string {
	return []string{d.lo.Address, d.fo.Address, d.lp.Address}
}

// requireReadsAsNobodys holds alice's GET /api/v1/lenses?on= LO and GET /api/v1/references?to= LO,
// and the second agent's same two reads of LP, to what the same read of a lens nobody wrote answers,
// 404 NOT_FOUND, byte for byte once the id is substituted. failure is the words of a read that
// answers otherwise, given the read, its status and body, and the reader.
func (d lensedDocument) requireReadsAsNobodys(t *testing.T, failure func(read, status, login string) string) {
	t.Helper()
	nobody := uuid.NewString()
	for _, read := range []struct {
		login string
		as    sender
		lens  lensInvoked
	}{{"alice", d.settle.asAlice, d.lo}, {"the second agent", d.settle.asSecond, d.lp}} {
		for _, route := range []string{"/api/v1/lenses?on=", "/api/v1/references?to="} {
			own := read.as(http.MethodGet, route+url.QueryEscape(read.lens.Address), nil)
			nobodys := read.as(http.MethodGet, route+url.QueryEscape(refs.StoreRef(graphstore.KindLens, nobody)), nil)
			want := bytes.ReplaceAll(nobodys.Body.Bytes(), []byte(nobody), []byte(read.lens.ID))
			if own.Code != http.StatusNotFound || nobodys.Code != http.StatusNotFound || !bytes.Equal(own.Body.Bytes(), want) {
				t.Error(failure("GET "+route+read.lens.Address, fmt.Sprintf("%d %s", own.Code, own.Body.String()), read.login))
				t.Logf("a lens nobody wrote answers %s %d %s, %s once the id is substituted", read.login, nobodys.Code, nobodys.Body.String(), want)
			}
		}
	}
}

// requireStaleBeforeTheSweep holds the four reads of requireReadsAsNobodys to 404 NOT_FOUND before
// any sweep has run, while LO, FO and LP still read live in the store: the row LO recorded no
// longer stands at the document's address, so LO is stale, and FO and LP with it (spec v70 R5).
func (d lensedDocument) requireStaleBeforeTheSweep(t *testing.T) {
	t.Helper()
	d.requireReadsAsNobodys(t, func(read, status, login string) string {
		return fmt.Sprintf("before the orphan sweep of %s, %s answers %s to %s, want 404: the row %s LO recorded no longer stands at %s, so LO, FO and LP are stale",
			d.address, read, status, login, d.document.ID, d.address)
	})
}

// removeAsABuildStatingTheNodeGone removes the untouched document as a build whose removal states
// the document's node gone and takes nothing beneath it gone removes it, in one transaction: the
// document's owner lock first, then an artifact.removed event naming it, its node stated gone under
// that event, and the rows removeAsTheReleaseBefore deletes.
func removeAsABuildStatingTheNodeGone(t *testing.T, database *store.Store, artifact model.Artifact) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		owners := events.OwnerRows{}
		owners.Add(*artifact.IssueKey, artifact.ID, "document "+artifact.ID)
		if err := events.LockOwnerRows(ctx, tx, owners); err != nil {
			return err
		}
		at, err := graphstore.StatementTime(ctx, tx, graphstore.KindArtifact, artifact.RefKey)
		if err != nil {
			return err
		}
		removal, err := appendIssueEventDirectly(ctx, tx, *artifact.IssueKey, "artifact.removed", artifact)
		if err != nil {
			return err
		}
		gone := graphstore.ArtifactNode(artifact.RefKey, artifact.Project, "", model.Actor{Kind: "user", ID: "alice"}, at)
		gone.State, gone.SourceEvent = graphstore.StateGone, removal
		if err := graphstore.Write(ctx, tx, []graphstore.Node{gone}, nil); err != nil {
			return err
		}
		return deleteArtifactRows(ctx, tx, artifact)
	}); err != nil {
		t.Fatalf("remove %s as a build stating its node gone does: %v", artifact.ID, err)
	}
}

// appendIssueEventDirectly appends an event of type naming artifact to issue's events as a build of
// another release appends one, written straight to the table: at the sequence after the issue's
// last_seq, which it moves, as every append of the issue's events does. It answers the event's id.
func appendIssueEventDirectly(ctx context.Context, tx pgx.Tx, issue, eventType string, artifact model.Artifact) (int64, error) {
	payload, err := json.Marshal(map[string]any{"artifact": artifact})
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `
		with next as (update issues set last_seq = last_seq + 1 where key = $1 returning last_seq)
		insert into events (issue_key, seq, type, actor, payload, notify)
		select $1, next.last_seq, $2, '{"kind":"user","id":"alice"}', $3::jsonb, false from next
		returning id
	`, issue, eventType, payload).Scan(&id)
	return id, err
}

// sweepAsTheTick runs the orphan sweep as the block-index tick runs it while it owes the object
// pass (docs' writeObjectNodesAnotherBuildWrote): it reads the document nodes the census reads as
// orphaned, at most the tick's bound of 20 and one more (graphcensus.OrphanedDocumentNodes), sweeps
// each of the first 20 (graphcensus.StateOrphanedGone), leaving a node it may not sweep, and then
// states every stale lens gone (stateStaleLensesGoneAsTheTick).
func sweepAsTheTick(t *testing.T, database *store.Store) {
	t.Helper()
	ctx := context.Background()
	const swept = 20
	owed, err := graphcensus.OrphanedDocumentNodes(ctx, database.Pool, swept+1)
	if err != nil {
		t.Fatalf("read the orphaned document nodes: %v", err)
	}
	for _, ref := range owed[:min(len(owed), swept)] {
		if err := graphcensus.StateOrphanedGone(ctx, database, ref); err != nil && !errors.Is(err, graphcensus.ErrNotOrphaned) {
			t.Fatalf("sweep the orphaned node of %s: %v", ref, err)
		}
	}
	stateStaleLensesGoneAsTheTick(t, database)
}

// stateStaleLensesGoneAsTheTick runs graphcensus.StateStaleLensesGone as the block-index tick runs
// it: over every address, in a transaction of its own, by the tick (graphcensus.SweepActor) at the
// database's clock.
func stateStaleLensesGoneAsTheTick(t *testing.T, database *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		_, err := graphcensus.StateStaleLensesGone(ctx, tx, "", time.Time{}, graphcensus.SweepActor)
		return err
	}); err != nil {
		t.Fatalf("state the stale lenses gone: %v", err)
	}
}

// requireRemovedBehindNoRow holds the test's premise before the sweep: the document's node reads
// state behind no row, and LO, FO and LP still read live.
func (d lensedDocument) requireRemovedBehindNoRow(t *testing.T, state string) {
	t.Helper()
	stands, err := graphcensus.ObjectRowStands(context.Background(), d.settle.database.Pool, graphstore.KindArtifact, d.document.RefKey)
	if err != nil {
		t.Fatalf("read whether a row stands at %s: %v", d.address, err)
	}
	if held := storedNodeOf(t, d.settle.database, d.address).State; held != state || stands {
		t.Fatalf("the node of %s reads %s and a row stands there: %t; the test's premise is its node %s behind no row", d.address, held, stands, state)
	}
	for _, node := range d.stood() {
		if held := storedNodeOf(t, d.settle.database, node).State; held != graphstore.StateLive {
			t.Fatalf("%s reads %s before the sweep, want live: the test's premise is that the removal took nothing beneath the document gone", node, held)
		}
	}
}

// The orphan sweep takes every lens on a document the release before this one removed to gone,
// and all that stands under each (spec v70 R5, c-settle): the lenses are stale from the removal on,
// since the row LO recorded no longer stands at the document's address, and the sweep is what
// takes them gone in the store. On c-settle's rig (settleRig), alice makes a document of the spec's
// issue by typing a title, invokes a trade-off analysis LO on it with one finding FO, and the
// second agent invokes a lens LP on FO, reserved for itself (lensedDocument). The test removes the
// document as the release before this one removes it (removeAsTheReleaseBefore), which states no
// node gone, so the document's node stands live behind no row. Before the sweep, alice's GET
// /api/v1/lenses?on= LO and GET /api/v1/references?to= LO, and the second agent's same two reads of
// LP, already answer what the read of a lens nobody wrote answers, 404 NOT_FOUND, byte for byte once
// the id is substituted, because LO, FO and LP are stale. The test then runs the sweep as the
// block-index tick runs it: graphcensus.StateOrphanedGone on the document's address, then
// graphcensus.StateStaleLensesGone. The rows of the document's node, LO, FO and LP then each read
// gone, and the four reads still answer 404 NOT_FOUND.
func TestTheOrphanSweepTakesEveryLensOnItsDocumentGone(t *testing.T) {
	removed := newLensedDocument(t, "Notes a release before removes.md")
	removeAsTheReleaseBefore(t, removed.settle.database, removed.document)
	removed.requireRemovedBehindNoRow(t, graphstore.StateLive)
	removed.requireStaleBeforeTheSweep(t)
	ctx := context.Background()
	if err := graphcensus.StateOrphanedGone(ctx, removed.settle.database, removed.document.RefKey); err != nil {
		t.Fatalf("sweep the orphaned node of %s: %v", removed.address, err)
	}
	stateStaleLensesGoneAsTheTick(t, removed.settle.database)
	for _, node := range append([]string{removed.address}, removed.stood()...) {
		if state := storedNodeOf(t, removed.settle.database, node).State; state != graphstore.StateGone {
			t.Errorf("after the orphan sweep of %s, %s reads %s, want gone with the document it stood on", removed.address, node, state)
		}
	}
	removed.requireReadsAsNobodys(t, func(read, status, login string) string {
		return fmt.Sprintf("after the orphan sweep of %s, %s answers %s to %s, want 404", removed.address, read, status, login)
	})
}

// The sweep takes gone the lenses on a document a build removed that stated the document's node
// gone and took nothing beneath it gone (spec v70 R5): the sweep takes every stale lens gone,
// whatever the state of the document's node, so LO, FO and LP do not stay live in the store on a
// gone node behind no row. On lensedDocument's document, in one transaction the test states the
// document's node gone, deletes its row as removeAsTheReleaseBefore deletes it, appends an
// artifact.removed event and takes no lens gone (removeAsABuildStatingTheNodeGone). Before the
// sweep the document's node reads gone behind no row and LO, FO and LP live in the store, while
// alice's two reads of LO and the second agent's two reads of LP already answer what a lens nobody
// wrote answers, because each is stale. The test then runs the orphan sweep as the block-index tick
// runs it (sweepAsTheTick: graphcensus.OrphanedDocumentNodes, graphcensus.StateOrphanedGone on each
// address that read answers, then graphcensus.StateStaleLensesGone), after which LO, FO and LP each
// read gone in the store, and the four reads still answer 404 NOT_FOUND.
func TestTheSweepTakesTheLensesOnADocumentAnotherBuildStatedGone(t *testing.T) {
	removed := newLensedDocument(t, "Notes another build states gone.md")
	removeAsABuildStatingTheNodeGone(t, removed.settle.database, removed.document)
	removed.requireRemovedBehindNoRow(t, graphstore.StateGone)
	removed.requireStaleBeforeTheSweep(t)
	sweepAsTheTick(t, removed.settle.database)
	for _, node := range removed.stood() {
		if state := storedNodeOf(t, removed.settle.database, node).State; state != graphstore.StateGone {
			t.Errorf("after the orphan sweep, %s reads %s, want gone: its document %s was stated gone with no row and no cascade", node, state, removed.address)
		}
	}
	removed.requireReadsAsNobodys(t, func(read, status, login string) string {
		return fmt.Sprintf("after the orphan sweep, %s answers %s to %s, want 404", read, status, login)
	})
}
