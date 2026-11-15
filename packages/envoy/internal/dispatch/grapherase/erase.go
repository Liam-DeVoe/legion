// Package grapherase is `envoy-dispatch erase --node <kind>:<id>`: the one path in the tree that
// deletes a node of the store, its tuples and its projection rows, and that takes the erased bytes
// out of every other store that holds them. Only cmd/dispatch imports it.
//
// It is a package of its own, and not a pass of graphcensus, because it writes through docs and
// api inside its own transaction - a document's live state (docs.ReadErasedLiveText) and the term
// statistics (api.RecountSearchTermStatsTx) - and docs' own tests import graphcensus, which can
// therefore import neither.
package grapherase

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc/schemanodes"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// eraseApplicationName is how the erase appears in pg_stat_activity, as the census names itself:
// an operator reading the activity of a database sees which pass holds its rows.
const eraseApplicationName = "envoy-dispatch erase"

// eraseMarker stands where the erased bytes were in a row the erase rewrites rather than
// deletes, with the node the run addressed written into it. A reader of a redacted payload
// learns three things from it: those bytes went by an erase and not by a lost write, which node
// that erase addressed, and that `graph_erasures` holds the record of the run - who ran it,
// when, and everything else it removed. It names the node and never the text, so a row it marks
// holds none of what was erased.
const eraseMarker = "[erased by envoy-dispatch erase %s:%s, recorded in graph_erasures]"

// blockExcerpt is refs.BlockExcerptRunes as a statement spells it: an excerpt this package
// rewrites is cut from its block's text exactly as the indexer cuts it (refs.IndexBlocks).
var blockExcerpt = strconv.Itoa(refs.BlockExcerptRunes)

// projectedTables are the tables that carry a node's address, and so are the rows a node is
// projected onto (migration 20261109022248 gave all three `node_kind` and `node_address`). Text
// is the column of each holding what a reader of that row sees, empty for a table that holds no
// text of its own: a block reference is one link out of a block and carries none. Issue is the
// column naming the issue that owns a row itself, empty for a table whose rows are owned through
// their document alone, by the artifact_id all three carry: a comment is an issue's or a
// document's (comments_one_owner, migration 0009), and a block and a block reference are their
// document's. Key is the table's primary key, the order the erase takes a node's rows of it in
// (lockProjectionRows).
//
// The erase walks all three by (node_kind, node_address) rather than by the node's kind, so a
// kind that gains a projection carries no second list here to keep in step.
var projectedTables = []struct{ Table, Text, Issue, Key string }{
	{Table: "comments", Text: "body", Issue: "issue_key", Key: "id"},
	{Table: "document_blocks", Text: "excerpt", Key: "artifact_id, block_id"},
	{Table: "block_refs", Text: "", Key: "artifact_id, block_id, to_kind, to_id, kind"},
}

// connect opens the erase's one connection, named in pg_stat_activity (eraseApplicationName). The
// erase uses no pool and migrates nothing: the schema it works on is the deployment's own.
func connect(ctx context.Context, databaseURL string) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse Postgres URL: %w", err)
	}
	config.RuntimeParams["application_name"] = eraseApplicationName
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return conn, nil
}

// ParseNode reads the `<kind>:<id>` the --node flag takes. The kind is the node's kind as
// graph_nodes keys it and holds no colon; everything after the first one is the id, so a page's
// `page:role/<topic>` and a comment's uuid are both read whole.
func ParseNode(value string) (kind, id string, err error) {
	kind, id, found := strings.Cut(strings.TrimSpace(value), ":")
	if !found || strings.TrimSpace(kind) == "" || strings.TrimSpace(id) == "" {
		return "", "", fmt.Errorf("--node takes <kind>:<id>, not %q", value)
	}
	return strings.TrimSpace(kind), strings.TrimSpace(id), nil
}

// Erase removes one node and the bytes that stand on it, in one transaction: the node's tuples,
// the tuples whose far end it is, every projection row of it and the rows that cannot exist
// without one, the `artifact_versions` rows whose markdown holds its text, and each
// `artifact_reviews` row whose `content_sha256` covered those bytes. It is the one path in this
// tree that deletes a tuple row or a node row (graphstore.EraseNode); every other writer appends.
// It takes the bytes out of every other store that holds them in the same transaction: the blocks
// and the events that quoted them (redact), the live state of each document that held them
// (readDocuments, replaceLiveTexts), a document's search row and its spec's issue vector
// (restateSearch), the embedding rows and windows cut from them (forgetEmbeddings,
// restateEmbeddings), the term statistics (api.RecountSearchTermStatsTx) and the stored display
// trees (forgetTrees).
//
// It reads what it would remove first and refuses to remove anything until confirmed is true,
// so an operator reads every version and every review the erase would end - each by its document
// and its version - and every document whose live state it would replace, with what that costs,
// before a byte goes. A run that is not confirmed rolls its transaction back and has written
// nothing. It refuses, confirmed or not, while a document's live text holds the erased bytes
// where its markdown spells them otherwise (Unredacted), since its seed would keep them.
//
// It refuses a type node, a supertag or an attribute, before it opens a connection: a type is
// data the block schema seeds (migration 20261109051232, pmdoc/schemanodes), and no route and no
// command creates, retitles or deletes one (R6). Such a node holds no text anybody wrote, so no
// erasure is owed for it, and deleting one would take a type out of the schema every server
// installs.
//
// It takes its locks in the store's lock order, written down on api/inbox_read.go's
// markInboxReadThreads: every owner row it writes under first, then the node's row, then
// everything else (lockInStoreOrder).
//
// What it did goes into `graph_erasures`: what was removed, who ran it and when, and no erased
// text. Nobody addresses that row and no read of the graph answers it.
//
// Once it has committed it waits treeStoreBound and deletes the display trees it deleted once
// more (sweepTrees). treeStoreBound is how long after the commit a read that began before it can
// still store the display tree of a text the erase removed: a read stores the trees it read on a
// goroutine of its own, detached from the request and bounded by api.StoreTreesTimeout, which is
// what cmd/dispatch passes; a test passes a shorter one. An error there comes back beside the
// Erasure, which is then Applied: the erase stands, and what the sweep could not do is the
// error's to say.
func Erase(ctx context.Context, databaseURL, kind, id string, confirmed bool, treeStoreBound time.Duration) (*Erasure, error) {
	if kind == schemanodes.KindSupertag || kind == schemanodes.KindAttribute {
		return nil, fmt.Errorf("the erase refuses %s:%s: a type node is data the block schema seeds, and no command deletes it", kind, id)
	}
	conn, err := connect(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	tx, erasure, read, err := lockInStoreOrder(ctx, conn, kind, id)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())

	if err := tx.QueryRow(ctx, `select current_user`).Scan(&erasure.RanBy.DatabaseUser); err != nil {
		return nil, fmt.Errorf("read the database user: %w", err)
	}
	if account, err := user.Current(); err == nil {
		erasure.RanBy.SystemUser = account.Username
	}
	if host, err := os.Hostname(); err == nil {
		erasure.RanBy.Host = host
	}

	texts, artifacts, numbers := read.texts, read.artifacts, read.numbers
	if err := erasedReviews(ctx, tx, artifacts, numbers, erasure); err != nil {
		return nil, err
	}
	if err := erasure.countTuples(ctx, tx); err != nil {
		return nil, err
	}
	if err := erasure.readBlockers(ctx, tx, kind, id); err != nil {
		return nil, err
	}
	if err := erasure.readDocuments(ctx, tx, texts, read.documents); err != nil {
		return nil, err
	}
	if !confirmed || len(erasure.Blockers) > 0 || len(erasure.Unredacted()) > 0 {
		return erasure, nil
	}
	if err := erasure.apply(ctx, tx, read); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	erasure.Applied = true
	if err := erasure.sweepTrees(ctx, conn, treeStoreBound); err != nil {
		return erasure, err
	}
	return erasure, nil
}

// apply removes everything the run read, in the one order the foreign keys allow. Before anything
// goes it deletes the rows of the embedding store that hold the text or stand on a block it
// erases or rewrites (forgetEmbeddings). Then go the whole text of every block of the node, the
// rows that cannot exist without a projection row, the projection rows, and the approvals and
// the versions they stood on. Then it rewrites what it may not remove - the blocks of the
// documents that quoted the node, the event log, the search vectors that stood on a version that
// went, the embedding rows of what it leaves (restateEmbeddings), and the live state of each
// document that held the text - counts the term statistics of every project whose items moved
// again, and deletes the display trees of the texts that went. Last go the tuples of both sides
// and the node (graphstore.EraseNode), and the record of what the run did goes in. Every row it
// writes is one read found, under the owner rows the run took before the node's (erasedRead).
func (e *Erasure) apply(ctx context.Context, tx pgx.Tx, read erasedRead) error {
	texts, artifacts, numbers := read.texts, read.artifacts, read.numbers
	sums, err := e.treeSums(ctx, tx, texts, artifacts, numbers)
	if err != nil {
		return err
	}
	e.sums = sums
	forgotten, err := e.forgetEmbeddings(ctx, tx, texts, read.embedded)
	if err != nil {
		return err
	}
	if err := e.eraseBlockTexts(ctx, tx); err != nil {
		return err
	}
	for _, held := range projectedTables {
		references, err := referencesTo(ctx, tx, held.Table)
		if err != nil {
			return err
		}
		for _, reference := range references {
			if !reference.Required {
				continue
			}
			tag, err := tx.Exec(ctx, `delete from `+reference.Table+` child where child.`+reference.Column+
				` in (`+projectionRows(reference, held.Table)+`)`, e.Kind, e.ID)
			if err != nil {
				return fmt.Errorf("delete the %s rows of %s:%s: %w", reference.Table, e.Kind, e.ID, err)
			}
			e.removed(reference.Table, tag.RowsAffected())
		}
		tag, err := tx.Exec(ctx, `delete from `+held.Table+` where node_kind = $1 and node_address = $2`, e.Kind, e.ID)
		if err != nil {
			return fmt.Errorf("delete the %s rows of %s:%s: %w", held.Table, e.Kind, e.ID, err)
		}
		e.removed(held.Table, tag.RowsAffected())
	}
	if len(artifacts) > 0 {
		tag, err := tx.Exec(ctx, `delete from artifact_reviews where exists (
			select 1 from unnest($1::uuid[], $2::int[]) as doomed(artifact, number)
			where doomed.artifact = artifact_reviews.artifact_id and doomed.number = artifact_reviews.version)`,
			artifacts, numbers)
		if err != nil {
			return fmt.Errorf("delete the reviews over the erased bytes: %w", err)
		}
		e.removed("artifact_reviews", tag.RowsAffected())
		tag, err = tx.Exec(ctx, `delete from artifact_versions where exists (
			select 1 from unnest($1::uuid[], $2::int[]) as doomed(artifact, number)
			where doomed.artifact = artifact_versions.artifact_id and doomed.number = artifact_versions.number)`,
			artifacts, numbers)
		if err != nil {
			return fmt.Errorf("delete the versions holding the erased text: %w", err)
		}
		e.removed("artifact_versions", tag.RowsAffected())
	}
	if err := e.redact(ctx, tx, texts, read.quoted); err != nil {
		return err
	}
	projects, err := e.restateSearch(ctx, tx, artifacts)
	if err != nil {
		return err
	}
	if err := e.restateEmbeddings(ctx, tx, forgotten); err != nil {
		return err
	}
	if err := e.replaceLiveTexts(ctx, tx); err != nil {
		return err
	}
	recounted, queued, err := api.RecountSearchTermStatsTx(ctx, tx, projects)
	if err != nil {
		return fmt.Errorf("count the term statistics again: %w", err)
	}
	e.removed("search_term_queue", queued)
	e.Recounted = recounted
	if err := e.forgetTrees(ctx, tx, sums); err != nil {
		return err
	}
	if err := graphstore.EraseNode(ctx, tx, e.Kind, e.ID); err != nil {
		return err
	}
	removed, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("render the erasure record: %w", err)
	}
	ranBy, err := json.Marshal(e.RanBy)
	if err != nil {
		return fmt.Errorf("render who ran the erase: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into graph_erasures (node_kind, node_id, removed, ran_by, ran_at)
		values ($1, $2, $3, $4, $5)`, e.Kind, e.ID, removed, ranBy, e.RanAt); err != nil {
		return fmt.Errorf("record the erase: %w", err)
	}
	return nil
}

// eraseBlockTexts deletes the whole text of every block the node projects onto, which is the
// most of the bytes an erased block holds. document_block_texts is keyed by
// (artifact_id, block_id, version) and carries a foreign key to artifacts alone (migration
// 0121), so the walk of single-column references into a projected table never names it and
// the text would outlive the node that held it. It is deleted here by the pair the block's
// own document_blocks row holds, while that row is still there, for every version of the
// block, and counted like any other table the run emptied. A node with no document_blocks
// row - a comment, a person - names no pair and deletes nothing.
func (e *Erasure) eraseBlockTexts(ctx context.Context, tx pgx.Tx) error {
	tag, err := tx.Exec(ctx, `
		delete from document_block_texts erased
		where (erased.artifact_id, erased.block_id) in (
			select held.artifact_id, held.block_id from document_blocks held
			where held.node_kind = $1 and held.node_address = $2
		)`, e.Kind, e.ID)
	if err != nil {
		return fmt.Errorf("delete the texts of the blocks of %s:%s: %w", e.Kind, e.ID, err)
	}
	e.removed("document_block_texts", tag.RowsAffected())
	return nil
}

// marker is eraseMarker naming the node this run addressed, which is what stands in every row
// the run rewrites.
func (e *Erasure) marker() string { return fmt.Sprintf(eraseMarker, e.Kind, e.ID) }
