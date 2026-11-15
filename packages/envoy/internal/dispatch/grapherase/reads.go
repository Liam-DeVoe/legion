package grapherase

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/events"
)

// erasedRead is one read of what the erase removes and rewrites: the text the node's rows hold
// (erasedTexts), the versions holding it, as the (artifact, number) pairs the deletes take
// (erasedVersions), the documents whose live state the erase replaces (heldDocuments), the
// documents whose block texts or excerpts it rewrites (quoted, quotedDocuments), the issues and
// documents whose embedding rows it deletes, by the issue key or artifact id each row's id starts
// with (embedded, embeddedOwners), and the owner rows all of those and the node itself stand
// under.
type erasedRead struct {
	texts     []string
	artifacts []string
	numbers   []int32
	documents []heldDocument
	quoted    []string
	embedded  []string
	owners    events.OwnerRows
}

// read reads the node as the erase would remove it, and the owner row of every row it would write
// that another writer writes under an owner, which is the owner row that writer takes before it.
// It locks nothing and writes no row, so lockOwnersThenNode runs it once to learn which owner rows
// to take and once more under them. What the erase writes, statement by statement, and the owner
// row each is owed:
//
//   - the node's own rows: its projection rows and every row that cannot exist without one (the
//     deletes in apply), the whole text of its blocks (eraseBlockTexts), its whole-text embedding
//     row, its item's windows and their window state (forgetEmbeddings), and the tuples out of
//     it. The comment path writes those under the comment's issue or document, and the version
//     write and the backfill under the block's document: nodeOwners reads that owner.
//   - the versions holding the text and the approvals over them (apply), the search rows that
//     stood on those versions and the search vector of an issue whose spec lost one
//     (restateSearch). A version, a review and the search row a version's trigger writes are each
//     written under the document's owner: erasedVersions reads those documents.
//   - each document's live state (replaceLiveTexts), whose writers take the document's owner row
//     and then its room's lock: heldDocuments reads those documents.
//   - the block texts and excerpts holding the text (redactBlocks), which the version write, the
//     block index tick and the backfill write under the document's owner and then its revision
//     fence: quotedDocuments reads those documents.
//   - the issue rows, whole-text document rows and document windows of the embedding store that
//     hold the text, and the windows covering a block the run erases or rewrites
//     (forgetEmbeddings), the issue rows written again and the documents marked owed their windows
//     (restateEmbeddings). The window arm writes a document's windows and clears its mark under the
//     document's owner (refs.WriteDocumentWindows), and an issue's row is written by the trigger
//     of a write of its spec or of its own title, under the issue: embeddedOwners reads those
//     owners.
//
// The rest of what the erase writes is owed no owner row, since none of its other writers takes
// one, or nothing else writes it:
//
//   - the event payloads (redactEvents). An event is appended under its owner row, but an append
//     writes a new row and never one already there. What writes a row already there is the
//     outbox's record of what it published (outbox's recordBatch), which takes no owner row, and
//     the ask.opened payload events.Broker.AppendAll writes into the row its own transaction
//     appended, which no other transaction sees before that one commits.
//   - the tuples into the node (apply). A tuple is appended once, by the writer of its cause, and
//     nothing writes it again, so a delete of one meets no other writer's write of that row.
//   - the term statistics (api.RecountSearchTermStatsTx), whose writers take a project's totals
//     row and no owner row; the queue rows it deletes are this transaction's own.
//   - the display trees (forgetTrees, sweepTrees), which a read stores and the display tree fill
//     rewrites, neither under an owner row.
//   - the node's own graph_nodes row, which the run has held since it took it after its owner
//     rows, and graph_erasures, which only the erase writes.
//
// The block texts, excerpts and embedding rows of other issues and documents are written by
// statements that test each row afresh, so redactBlocks and forgetEmbeddings write only the rows
// of the documents and issues this read found, by the same tests (blockTextHolds, excerptHolds,
// quotingEmbeddings). A row that a writer under an owner the run did not take brings to hold the
// text after this read is that writer's, as a row it brings after the erase commits would be,
// and the run writes under no owner it has not taken.
func (e *Erasure) read(ctx context.Context, tx pgx.Tx) (erasedRead, error) {
	texts, err := erasedTexts(ctx, tx, e.Kind, e.ID)
	if err != nil {
		return erasedRead{}, err
	}
	artifacts, numbers, err := erasedVersions(ctx, tx, texts, e)
	if err != nil {
		return erasedRead{}, err
	}
	documents, err := heldDocuments(ctx, tx, texts, artifacts)
	if err != nil {
		return erasedRead{}, err
	}
	quoted, err := quotedDocuments(ctx, tx, e.marker(), texts)
	if err != nil {
		return erasedRead{}, err
	}
	owners, err := nodeOwners(ctx, tx, e.Kind, e.ID)
	if err != nil {
		return erasedRead{}, err
	}
	embedded, err := e.embeddedOwners(ctx, tx, texts, owners)
	if err != nil {
		return erasedRead{}, err
	}
	for index, version := range e.Versions {
		owners.Add(version.Issue, artifacts[index], "document "+version.Document)
	}
	for _, document := range documents {
		owners.Add(document.issue, document.artifact, "document "+document.document)
	}
	read := erasedRead{texts: texts, artifacts: artifacts, numbers: numbers, documents: documents, embedded: embedded, owners: owners}
	for _, document := range quoted {
		owners.Add(document.issue, document.artifact, "the blocks of document "+document.document)
		read.quoted = append(read.quoted, document.artifact)
	}
	return read, nil
}

// quotedDocuments reads every document a block text or an excerpt of which redactBlocks
// rewrites, by the tests its statements write with (blockTextHolds, excerptHolds), in artifact id
// order.
func quotedDocuments(ctx context.Context, tx pgx.Tx, marker string, texts []string) ([]heldDocument, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	bound := placeholders(texts)
	rows, err := tx.Query(ctx, `
		select a.id::text, a.ref_key, coalesce(a.issue_key, ''), coalesce(a.project_key, '')
		from artifacts a
		where a.id in (
			select held.artifact_id from document_block_texts held where `+blockTextHolds(bound)+`
			union
			select held.artifact_id from document_blocks held where `+excerptHolds(bound)+`
		)
		order by a.id`, redactionArgs(marker, texts)...)
	if err != nil {
		return nil, fmt.Errorf("read the documents whose blocks hold the erased text: %w", err)
	}
	documents, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (heldDocument, error) {
		var document heldDocument
		err := row.Scan(&document.artifact, &document.document, &document.issue, &document.project)
		return document, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the documents whose blocks hold the erased text: %w", err)
	}
	return documents, nil
}

// embeddedOwners reads every row of the embedding store forgetEmbeddings deletes, by the tests its
// statement deletes with (nodeEmbeddings, quotingEmbeddings), adds to owners the owner each of an
// issue or a document stands under - the issue an issue row is of, and the owner of the document a
// whole-text document row or a document window is of - and answers the issue key or artifact id
// each row's id starts with, which forgetEmbeddings holds its delete of other issues' and
// documents' rows to. The node's own rows stand under the node's owner, which nodeOwners reads.
func (e *Erasure) embeddedOwners(ctx context.Context, tx pgx.Tx, texts []string, owners events.OwnerRows) ([]string, error) {
	rows, err := tx.Query(ctx, erasedBlocks+`
		select distinct held.kind, split_part(held.id, '#', 1), coalesce(a.id::text, ''), coalesce(a.ref_key, ''),
		       coalesce(a.issue_key, '')
		from embeddings held
		left join artifacts a on held.kind in ('document', $6) and a.id::text = split_part(held.id, '#', 1)
		where `+nodeEmbeddings+`
		   or `+quotingEmbeddings, e.embeddingArgs(texts)...)
	if err != nil {
		return nil, fmt.Errorf("read the embedding rows holding the text of %s:%s: %w", e.Kind, e.ID, err)
	}
	held, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([5]string, error) {
		var embedded [5]string
		err := row.Scan(&embedded[0], &embedded[1], &embedded[2], &embedded[3], &embedded[4])
		return embedded, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the embedding rows holding the text of %s:%s: %w", e.Kind, e.ID, err)
	}
	keys := map[string]struct{}{}
	for _, embedded := range held {
		kind, key, artifact, document, issue := embedded[0], embedded[1], embedded[2], embedded[3], embedded[4]
		keys[key] = struct{}{}
		switch {
		case kind == "issue":
			owners.Add(key, "", "the embedding row of issue "+key)
		case artifact != "":
			owners.Add(issue, artifact, "the embedding rows of document "+document)
		}
	}
	return slices.Sorted(maps.Keys(keys)), nil
}

// nodeOwners reads the owner row each projection row of the node stands under: an issue's own
// comment stands under the issue, and every other row under the owner of the document it belongs
// to, which is that document's issue or, for a document no issue owns, the document's own
// artifacts row - the owner docs' lockArtifactOwner finds and events.LockOwnerRows takes. A node
// no row projects stands under none.
func nodeOwners(ctx context.Context, tx pgx.Tx, kind, id string) (events.OwnerRows, error) {
	arms := make([]string, len(projectedTables))
	for index, held := range projectedTables {
		issue := "null::text"
		if held.Issue != "" {
			issue = held.Issue
		}
		arms[index] = `select ` + issue + `, artifact_id from ` + held.Table + ` where node_kind = $1 and node_address = $2`
	}
	rows, err := tx.Query(ctx, `
		select coalesce(held.issue_key, a.issue_key, ''), coalesce(a.id::text, '')
		from (`+strings.Join(arms, " union all ")+`) as held (issue_key, artifact_id)
		left join artifacts a on a.id = held.artifact_id`, kind, id)
	if err != nil {
		return nil, fmt.Errorf("read the owners of %s:%s: %w", kind, id, err)
	}
	held, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
		var owner [2]string
		err := row.Scan(&owner[0], &owner[1])
		return owner, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the owners of %s:%s: %w", kind, id, err)
	}
	owners := events.OwnerRows{}
	for _, owner := range held {
		owners.Add(owner[0], owner[1], kind+":"+id)
	}
	return owners, nil
}

// erasedTexts is the text the node's own rows hold, which is what the erase looks for in the
// document versions and in every store it rewrites. A node whose projection holds no text - a
// block reference, or a node with no projection row at all - erases no version and redacts
// nothing, and the run then removes the node alone.
func erasedTexts(ctx context.Context, tx pgx.Tx, kind, id string) ([]string, error) {
	var texts []string
	for _, held := range projectedTables {
		if held.Text == "" {
			continue
		}
		rows, err := tx.Query(ctx, `select coalesce(`+held.Text+`, '') from `+held.Table+
			` where node_kind = $1 and node_address = $2`, kind, id)
		if err != nil {
			return nil, fmt.Errorf("read %s rows of %s:%s: %w", held.Table, kind, id, err)
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("read %s rows of %s:%s: %w", held.Table, kind, id, err)
		}
		for _, text := range found {
			if strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
		}
	}
	return texts, nil
}

// erasedVersions reads every document version whose markdown holds one of the erased texts, and
// answers the (artifact, number) pairs the review read takes.
func erasedVersions(ctx context.Context, tx pgx.Tx, texts []string, erasure *Erasure) ([]string, []int32, error) {
	erasure.Versions = []ErasedVersion{}
	if len(texts) == 0 {
		return nil, nil, nil
	}
	rows, err := tx.Query(ctx, `
		select v.artifact_id::text, v.number, a.ref_key, coalesce(a.issue_key, ''), coalesce(a.project_key, '')
		from artifact_versions v join artifacts a on a.id = v.artifact_id
		where exists (
			select 1 from unnest($1::text[]) as erased(text)
			where position(erased.text in v.markdown) > 0
		)
		order by a.ref_key, v.number`, texts)
	if err != nil {
		return nil, nil, fmt.Errorf("read the versions holding the erased text: %w", err)
	}
	defer rows.Close()
	var artifacts []string
	var numbers []int32
	for rows.Next() {
		var artifact, refKey, issue, project string
		var number int32
		if err := rows.Scan(&artifact, &number, &refKey, &issue, &project); err != nil {
			return nil, nil, fmt.Errorf("scan a version holding the erased text: %w", err)
		}
		artifacts = append(artifacts, artifact)
		numbers = append(numbers, number)
		erasure.Versions = append(erasure.Versions, ErasedVersion{
			Document: refKey, Issue: issue, Project: project, Version: int(number),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read the versions holding the erased text: %w", err)
	}
	return artifacts, numbers, nil
}

// erasedReviews reads every approval standing on one of those versions. Its content_sha256 is
// the hash of exactly the bytes about to go, so the approval cannot survive them: the trigger
// refuses to move a stored digest, and an approval over bytes nobody can reproduce is a claim
// about a text that no longer exists.
func erasedReviews(ctx context.Context, tx pgx.Tx, artifacts []string, numbers []int32, erasure *Erasure) error {
	erasure.Reviews = []ErasedReview{}
	if len(artifacts) == 0 {
		return nil
	}
	// Ordered as docs.ReviewOrder orders every read of artifact_reviews in this tree: a review
	// that committed later outranks one that committed first, since created_at is its
	// transaction's start and its id is random (api's review_order_guard_test.go). The order is
	// spelled out with its table named on every column rather than taken from docs.ReviewOrder,
	// because the artifacts row joined here has a created_at of its own, which a bare created_at
	// would leave ambiguous.
	rows, err := tx.Query(ctx, `
		select artifact_reviews.id::text, a.ref_key, coalesce(a.issue_key, ''),
		       artifact_reviews.version, artifact_reviews.state
		from artifact_reviews join artifacts a on a.id = artifact_reviews.artifact_id
		where exists (
			select 1 from unnest($1::uuid[], $2::int[]) as doomed(artifact, number)
			where doomed.artifact = artifact_reviews.artifact_id and doomed.number = artifact_reviews.version
		)
		order by artifact_reviews.event_seq desc nulls last, artifact_reviews.created_at desc,
		         artifact_reviews.id desc`,
		artifacts, numbers)
	if err != nil {
		return fmt.Errorf("read the reviews over the erased bytes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var review ErasedReview
		if err := rows.Scan(&review.ID, &review.Document, &review.Issue, &review.Version, &review.State); err != nil {
			return fmt.Errorf("scan a review over the erased bytes: %w", err)
		}
		erasure.Reviews = append(erasure.Reviews, review)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the reviews over the erased bytes: %w", err)
	}
	return nil
}

// countTuples reads how many tuples each side of the node holds, before any of them goes.
func (e *Erasure) countTuples(ctx context.Context, tx pgx.Tx) error {
	if err := tx.QueryRow(ctx,
		`select count(*) from graph_tuples where from_kind = $1 and from_id = $2`,
		e.Kind, e.ID).Scan(&e.Tuples); err != nil {
		return fmt.Errorf("count the tuples out of %s:%s: %w", e.Kind, e.ID, err)
	}
	if err := tx.QueryRow(ctx,
		`select count(*) from graph_tuples where to_kind = $1 and to_id = $2`,
		e.Kind, e.ID).Scan(&e.Inbound); err != nil {
		return fmt.Errorf("count the tuples into %s:%s: %w", e.Kind, e.ID, err)
	}
	return nil
}

// referencingColumn is one foreign key pointing at a projected table: the table and column that
// hold the reference, the column they hold, and whether that reference can be null.
type referencingColumn struct {
	Table, Column, Referenced string
	Required                  bool
}

// referencesTo reads every single-column foreign key pointing at one table. The composite key a
// projection carries to graph_nodes is not one of them, which is why the read binds a key of one
// column: that pair is the node's own address and goes with the node.
func referencesTo(ctx context.Context, tx pgx.Tx, table string) ([]referencingColumn, error) {
	rows, err := tx.Query(ctx, `
		select c.conrelid::regclass::text, child.attname, parent.attname, child.attnotnull
		from pg_constraint c
		join pg_attribute child on child.attrelid = c.conrelid and child.attnum = c.conkey[1]
		join pg_attribute parent on parent.attrelid = c.confrelid and parent.attnum = c.confkey[1]
		where c.contype = 'f' and c.confrelid = $1::regclass and array_length(c.conkey, 1) = 1
		order by 1, 2`, table)
	if err != nil {
		return nil, fmt.Errorf("read the foreign keys into %s: %w", table, err)
	}
	defer rows.Close()
	var references []referencingColumn
	for rows.Next() {
		var reference referencingColumn
		if err := rows.Scan(&reference.Table, &reference.Column, &reference.Referenced, &reference.Required); err != nil {
			return nil, fmt.Errorf("scan a foreign key into %s: %w", table, err)
		}
		references = append(references, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the foreign keys into %s: %w", table, err)
	}
	return references, nil
}

// projectionRows is the select of one projection's key column, which every delete against a row
// that points at it is written over.
func projectionRows(reference referencingColumn, table string) string {
	return `select held.` + reference.Referenced + ` from ` + table +
		` held where held.node_kind = $1 and held.node_address = $2`
}

// readBlockers names every row that points at this node's projection and is not part of it. A
// nullable reference is such a row: a reply whose reply_to names an erased comment is somebody
// else's words, and an ask handed back through it is the ask's own history, so the erase refuses
// rather than deleting one or rewriting the other.
func (e *Erasure) readBlockers(ctx context.Context, tx pgx.Tx, kind, id string) error {
	for _, held := range projectedTables {
		references, err := referencesTo(ctx, tx, held.Table)
		if err != nil {
			return err
		}
		for _, reference := range references {
			if reference.Required {
				continue
			}
			var rows int64
			if err := tx.QueryRow(ctx, `select count(*) from `+reference.Table+` child where child.`+
				reference.Column+` in (`+projectionRows(reference, held.Table)+`)`, kind, id).Scan(&rows); err != nil {
				return fmt.Errorf("count the %s.%s rows at %s:%s: %w", reference.Table, reference.Column, kind, id, err)
			}
			if rows > 0 {
				e.Blockers = append(e.Blockers, fmt.Sprintf("%d %s row(s) point at this node through %s.%s",
					rows, reference.Table, reference.Table, reference.Column))
			}
		}
	}
	return nil
}

// heldDocument is one document the erased text stands in, named as a reader addresses it, with
// the issue that owns it, empty for a document no issue owns.
type heldDocument struct{ artifact, document, issue, project string }

// heldDocuments reads every document the erased text stands in, in artifact id order. Those
// documents are the ones a version the run deletes belongs to, the ones whose block text holds
// the erased text, and the ones whose stored history, a pending prune's checkpoint or a snapshot
// holds its bytes whole: an edit nothing has versioned yet stands there alone. A text a person
// typed piece by piece can be stored split across updates, so no search of those bytes can be the
// whole list, which is why the versions and the blocks are on it as well.
func heldDocuments(ctx context.Context, tx pgx.Tx, texts, artifacts []string) ([]heldDocument, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		with held (artifact_id) as (
			select unnest($1::uuid[])
			union
			select b.artifact_id from document_block_texts b
			 where exists (select 1 from unnest($2::text[]) erased(text) where position(erased.text in b.text) > 0)
			union
			select u.artifact_id from doc_updates u
			 where exists (select 1 from unnest($2::text[]) erased(text)
			               where position(convert_to(erased.text, 'UTF8') in u.update) > 0)
			union
			select c.artifact_id from doc_checkpoints c
			 where exists (select 1 from unnest($2::text[]) erased(text)
			               where position(convert_to(erased.text, 'UTF8') in c.state) > 0)
			union
			select s.artifact_id from doc_snapshots s
			 where exists (select 1 from unnest($2::text[]) erased(text)
			               where position(convert_to(erased.text, 'UTF8') in s.state) > 0)
		)
		select a.id::text, a.ref_key, coalesce(a.issue_key, ''), coalesce(a.project_key, '')
		from artifacts a join held on held.artifact_id = a.id
		where a.kind = 'doc'
		order by a.id`, artifacts, texts)
	if err != nil {
		return nil, fmt.Errorf("read the documents holding the erased text: %w", err)
	}
	documents, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (heldDocument, error) {
		var document heldDocument
		err := row.Scan(&document.artifact, &document.document, &document.issue, &document.project)
		return document, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the documents holding the erased text: %w", err)
	}
	return documents, nil
}

// readDocuments reads the live state of every document heldDocuments read, as the erase would
// leave it (docs.ReadErasedLiveText), in artifact id order, which is the order two erases take
// the documents' room locks in. Each document's owner row the erase took before its node's row
// (lockInStoreOrder), so the owner row docs.ReadErasedLiveText takes first is one this
// transaction holds already, and no owner row is taken after the node's.
//
// Every one of them has its history replaced, a document whose live text no longer holds the
// erased text included, since an edit that took the text out leaves its bytes in the items it
// deleted until a fold collects them. It reads, and writes nothing: each live state is replaced
// in apply (replaceLiveTexts).
func (e *Erasure) readDocuments(ctx context.Context, tx pgx.Tx, texts []string, documents []heldDocument) error {
	e.Documents = []ErasedDocument{}
	for _, document := range documents {
		live, err := docs.ReadErasedLiveText(ctx, tx, document.artifact, texts, e.marker())
		if err != nil {
			return fmt.Errorf("read the live state of %s: %w", document.document, err)
		}
		if live == nil {
			continue
		}
		e.Documents = append(e.Documents, ErasedDocument{
			Document: document.document, Issue: document.issue, Project: document.project,
			Held: live.Held, Left: live.Left, Updates: live.Updates, AnchorMarks: live.AnchorMarks, MarginRecords: live.MarginRecords,
			live: live,
		})
	}
	return nil
}

// treeSums is the key of the stored display tree of every whole text the run removes or rewrites:
// the node's own texts, the markdown of each version it deletes, each string of an event payload
// that redactEvents rewrites, and the live text of each document whose history it replaces.
// text_trees keys a tree by the SHA-256 of its text's UTF-8 bytes (pmdoc.TextSum, migration
// 20261105121325) and holds the text's words in the tree, so each of those texts leaves a row
// holding the erased bytes once its own row is gone. It is read before anything goes, while the
// versions and the payloads still hold those texts.
func (e *Erasure) treeSums(ctx context.Context, tx pgx.Tx, texts, artifacts []string, numbers []int32) ([][]byte, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	whole := append([]string(nil), texts...)
	for _, document := range e.Documents {
		whole = append(whole, document.live.Text)
	}
	bound := placeholders(texts)
	args := append(redactionArgs(e.marker(), texts), whole, artifacts, numbers)
	wholeAt, artifactsAt, numbersAt := len(bound)+1, len(bound)+2, len(bound)+3
	payload := redaction("events.payload::text", jsonStrings(bound))
	member := redaction("(member #>> '{}')", bound)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		select sum from (
			select sha256(convert_to(whole.text, 'UTF8')) as sum from unnest($%d::text[]) as whole(text)
			union
			select sha256(convert_to(v.markdown, 'UTF8')) from artifact_versions v
			  join unnest($%d::uuid[], $%d::int[]) as doomed(artifact, number)
			    on doomed.artifact = v.artifact_id and doomed.number = v.number
			 where v.markdown is not null
			union
			select sha256(convert_to(member #>> '{}', 'UTF8'))
			  from events cross join lateral jsonb_path_query(events.payload, 'strict $.**') as member
			 where events.payload::text <> %s
			   and jsonb_typeof(member) = 'string' and %s <> member #>> '{}'
		) trees`, wholeAt, artifactsAt, numbersAt, payload, member), args...)
	if err != nil {
		return nil, fmt.Errorf("read the display trees of the texts holding the text of %s:%s: %w", e.Kind, e.ID, err)
	}
	sums, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, fmt.Errorf("read the display trees of the texts holding the text of %s:%s: %w", e.Kind, e.ID, err)
	}
	return sums, nil
}
