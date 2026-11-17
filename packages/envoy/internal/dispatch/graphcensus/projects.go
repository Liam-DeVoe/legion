package graphcensus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The project of every node, which a read limited to a project depends on. Each object a project
// owns - an issue, a document, an ask, a message, a comment and a document's block - is a node of
// the store whose project is a base attribute its own write states (graphstore.Node), and an
// object no project owns - a conversation's file, an ask or a comment on one, a message on no
// issue - names the parties it is between instead. This file reads the two against the rows and
// writes them from the rows:
// scopedRows, the one statement of what each row's node must carry; the census's classes over it
// (ProjectCensus); the read that answers every row not yet complete (ProjectComplete); and the
// backfill of the rows written before their kind's writer did this (backfillObjectNodes,
// backfillScopes).

// projectKinds are the kinds whose nodes carry a project or parties, in the order the census
// reports them.
var projectKinds = []string{
	graphstore.KindIssue, graphstore.KindArtifact, graphstore.KindAsk, graphstore.KindMessage,
	graphstore.KindComment, refs.BlockNodeKind,
}

// authorParty is, for an actor stored as jsonb at column, the party graphstore.AuthorOf names it
// as: a person by their canonical login, a session by its id, a personal token's owner by theirs,
// or null for an actor that names nobody. The login is canonical by model.CanonicalLogin's rule,
// spelled in SQL (canonicalLoginSQL).
func authorParty(column string) string {
	return `case
		when ` + column + `->>'kind' = 'user' and coalesce(` + column + `->>'id', '') <> ''
			then '` + graphstore.KindPerson + `:' || ` + canonicalLoginSQL(column+`->>'id'`) + `
		when ` + column + `->>'kind' = 'session' and coalesce(` + column + `->>'id', '') <> ''
			then '` + graphstore.KindSession + `:' || (` + column + `->>'id')
		when coalesce(` + column + `->>'owner', '') <> ''
			then '` + graphstore.KindPerson + `:' || ` + canonicalLoginSQL(column+`->>'owner'`) + `
	end`
}

// canonicalLoginSQL is model.CanonicalLogin spelled in SQL over the text expression login: every
// Unicode white space trimmed from both ends, as strings.TrimSpace trims it, and the ASCII letters
// lowered and nothing else, as fold.ASCII lowers them. Postgres's lower() would fold a non-ASCII
// letter too (U+212A KELVIN SIGN to "k"), and a btrim() with no set trims the space alone, so
// either would name a person other than the one the writer names.
func canonicalLoginSQL(login string) string {
	return `translate(btrim(` + login + `, '` + whiteSpaceSQL + `'), 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz')`
}

// whiteSpaceSQL is every character unicode.IsSpace reports, the set strings.TrimSpace trims, as
// the body of a SQL string literal. None of them is a quote.
var whiteSpaceSQL = func() string {
	var set strings.Builder
	for _, span := range unicode.White_Space.R16 {
		for r := rune(span.Lo); r <= rune(span.Hi); r += rune(span.Stride) {
			set.WriteRune(r)
		}
	}
	for _, span := range unicode.White_Space.R32 {
		for r := rune(span.Lo); r <= rune(span.Hi); r += rune(span.Stride) {
			set.WriteRune(r)
		}
	}
	return set.String()
}()

// partiesOf is the parties array the given party expressions make, as graphstore.PartiesValue
// orders them - each once, in byte order (collate "C") - or null for none.
func partiesOf(parties ...string) string {
	return `(select array_agg(party order by party collate "C")
		from (select distinct party from unnest(array[` + strings.Join(parties, ", ") + `]::text[]) as listed(party)
		      where party is not null) as held)`
}

// sessionParties is the parties of a thing a conversation's file owns: that conversation's
// session (graphstore.ArtifactNode), or null where the file is not a conversation's.
func sessionParties(session string) string {
	return `case when ` + session + ` is not null then array['` + graphstore.KindSession + `:' || ` + session + `] end`
}

// scopedRows is every row of a project-owned kind whose node the store must hold, as (kind, id,
// project, parties): the address its node is written under, and the project or the parties that
// node must carry, by the rule its writer follows - graphstore.ScopeOf for an ask and a comment,
// graphstore.ThreadParties for a message on no issue, the document's own for a block. A row whose
// address is past the store's bound is left out, as every writer leaves it out of the store: a
// document whose ref_key is, and a comment one of whose relations names such a document.
const scopedRows = `
	with recursive threads (id, root) as (
		select m.id, m.id from messages m where m.issue_key is null and m.in_reply_to is null
		union all
		select m.id, thread.root from messages m join threads thread on m.in_reply_to = thread.id
		where m.issue_key is null
	),
	scoped (kind, id, project, parties) as (
		select 'issue', i.key, i.project_key, null::text[] from issues i
		union all
		select 'artifact', a.ref_key, a.project_key, ` + `ARTIFACT_PARTIES` + `
		from artifacts a where octet_length(a.ref_key) <= ADDRESS_BOUND
		union all
		select 'ask', k.id::text, coalesce(i.project_key, a.project_key),
		       case when coalesce(i.project_key, a.project_key) is null then ` + `ASK_PARTIES` + ` end
		from asks k left join issues i on i.key = k.issue_key left join artifacts a on a.id = k.artifact_id
		union all
		select 'message', m.id::text, i.project_key,
		       case when m.issue_key is null then ` + `MESSAGE_PARTIES` + ` end
		from messages m
		left join issues i on i.key = m.issue_key
		left join threads thread on thread.id = m.id
		left join messages root on root.id = thread.root
		union all
		select 'comment', c.id::text, coalesce(i.project_key, owner.project_key),
		       case when coalesce(i.project_key, owner.project_key) is null then ` + `COMMENT_PARTIES` + ` end
		from comments c
		left join issues i on i.key = c.issue_key
		left join artifacts anchor on anchor.id = (c.anchor->>'artifact_id')::uuid
		left join artifacts owner on owner.id = c.artifact_id
		where c.node_kind is not null
		   or ((anchor.ref_key is null or octet_length(anchor.ref_key) <= ADDRESS_BOUND)
		       and (owner.ref_key is null or octet_length(owner.ref_key) <= ADDRESS_BOUND))
		union all
		select 'block', d.node_id, a.project_key, ` + `BLOCK_PARTIES` + `
		from document_blocks d join artifacts a on a.id = d.artifact_id
		where octet_length(d.node_id) <= ADDRESS_BOUND
	)`

// scopedRowsSQL is scopedRows with its parts filled in.
var scopedRowsSQL = strings.NewReplacer(
	"ADDRESS_BOUND", strconv.Itoa(graphstore.MaxAddressBytes),
	"ARTIFACT_PARTIES", sessionParties("a.session_id"),
	"ASK_PARTIES", sessionParties("a.session_id"),
	"MESSAGE_PARTIES", partiesOf(authorParty("root.author"), "root.target"),
	"COMMENT_PARTIES", sessionParties("owner.session_id"),
	"BLOCK_PARTIES", sessionParties("a.session_id"),
).Replace(scopedRows)

// The classes the census and ProjectComplete name a row or a node by. missing is a row whose node
// the store does not hold; gone a row of a kind whose rows only its own creates write
// (objectKinds) whose node is not live; unscoped a node holding neither a project nor parties;
// differs a node holding another project, or other parties, than its row's; orphaned a live node
// of one of those kinds that no row stands behind.
const (
	classMissing  = "missing"
	classGone     = "gone"
	classUnscoped = "unscoped"
	classDiffers  = "differs"
	classOrphaned = "orphaned"
)

// orphanKindList is the kinds the census reads the state and the node side of too, as a SQL list:
// the kinds the backfill writes a node for from their rows (objectKinds) - an issue, a document,
// an ask and a message - whose rows only their own creates write and only a document's removal
// deletes. A node of one of them stands live while its row does, and the removal of this build
// states it gone as it deletes the row; a build before this one writes no node, so during a roll
// or after a rollback it can create a row at an address whose node this build stated gone (gone),
// or remove a row whose node this build wrote (orphaned). The store census reads both directions
// for a comment and a block already (projections), so the project census reads them for these
// alone. A kind added to objectKinds is read every way from that one list.
var orphanKindList = func() string {
	quoted := make([]string, len(objectKinds))
	for index, kind := range objectKinds {
		quoted[index] = "'" + kind.kind + "'"
	}
	return strings.Join(quoted, ", ")
}()

// rowClass is the class of one row of scopedRows against its node n, null for a complete one.
var rowClass = `case
		when n.id is null then '` + classMissing + `'
		when scoped.kind in (` + orphanKindList + `) and n.state <> '` + graphstore.StateLive + `' then '` + classGone + `'
		when n.project_key is null and n.parties is null then '` + classUnscoped + `'
		when n.project_key is distinct from scoped.project or n.parties is distinct from scoped.parties then '` + classDiffers + `'
	end`

// classedRows follows scopedRowsSQL: every row of scoped with its class against its node, null for
// a complete one, and every live node of an object kind that no row of scoped stands behind, as
// orphaned, each with whether it is a row.
var classedRows = `,
	classed (kind, id, class, is_row) as (
		select scoped.kind, scoped.id, ` + rowClass + `, true
		from scoped
		left join graph_nodes n on n.kind = scoped.kind and n.id = scoped.id
		union all
		select n.kind, n.id, '` + classOrphaned + `', false
		from graph_nodes n
		where n.kind in (` + orphanKindList + `) and n.state = '` + graphstore.StateLive + `'
		  and not exists (select 1 from scoped where scoped.kind = n.kind and scoped.id = n.id)
	)`

// ProjectKindReport is what the project census read of one kind: how many rows it holds, and how
// many of them are in each class, and, for a kind whose rows only its own creates write, how many
// of its rows name a node that is not live and how many of its live nodes no row stands behind.
type ProjectKindReport struct {
	Kind                                       string
	Rows                                       int64
	Missing, Gone, Differs, Unscoped, Orphaned int64
}

// Findings is how many rows and nodes of the kind are incomplete, over the five classes.
func (r ProjectKindReport) Findings() int64 {
	return r.Missing + r.Gone + r.Differs + r.Unscoped + r.Orphaned
}

// ProjectCensus reads, for every project-owned kind, how many rows it holds and how many of them
// have no node, a node that is not live, a node with no project or parties, or a node in another
// project or between other parties than the row's, and how many live nodes of an orphan kind no
// row stands behind, in one statement over q, which the store census runs in its snapshot.
func ProjectCensus(ctx context.Context, q graphstore.Queryer) ([]ProjectKindReport, error) {
	rows, err := q.Query(ctx, scopedRowsSQL+classedRows+`
		select kind, count(*) filter (where is_row),
		       count(*) filter (where class = '`+classMissing+`'),
		       count(*) filter (where class = '`+classGone+`'),
		       count(*) filter (where class = '`+classDiffers+`'),
		       count(*) filter (where class = '`+classUnscoped+`'),
		       count(*) filter (where class = '`+classOrphaned+`')
		from classed group by kind
	`)
	if err != nil {
		return nil, fmt.Errorf("read the project census: %w", err)
	}
	defer rows.Close()
	read := map[string]ProjectKindReport{}
	for rows.Next() {
		var kind ProjectKindReport
		if err := rows.Scan(&kind.Kind, &kind.Rows, &kind.Missing, &kind.Gone, &kind.Differs, &kind.Unscoped, &kind.Orphaned); err != nil {
			return nil, fmt.Errorf("read the project census: %w", err)
		}
		read[kind.Kind] = kind
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the project census: %w", err)
	}
	reports := make([]ProjectKindReport, 0, len(projectKinds))
	for _, kind := range projectKinds {
		report := read[kind]
		report.Kind = kind
		reports = append(reports, report)
	}
	return reports, nil
}

// writeProjectCensus prints one line per kind, in the form the operator's runbook reads
// (cmd/dispatch/README.md, "The node and tuple store census").
func writeProjectCensus(w io.Writer, kinds []ProjectKindReport) {
	for _, kind := range kinds {
		fmt.Fprintf(w, "census: project kind=%s rows=%d missing=%d gone=%d differs=%d unscoped=%d orphaned=%d\n",
			kind.Kind, kind.Rows, kind.Missing, kind.Gone, kind.Differs, kind.Unscoped, kind.Orphaned)
	}
}

// ProjectComplete answers every project-owned row whose node is not complete, and every live node
// of an orphan kind no row stands behind, as `<kind> <id>: <class>` (missing, gone, unscoped,
// differs or orphaned), in one read: the rows and the nodes a read limited by a node's project
// would answer wrongly. An empty answer is every row and every node of every kind complete.
func ProjectComplete(ctx context.Context, q graphstore.Queryer) ([]string, error) {
	rows, err := q.Query(ctx, scopedRowsSQL+classedRows+`
		select kind || ' ' || id || ': ' || class
		from classed
		where class is not null
		order by kind, id
	`)
	if err != nil {
		return nil, fmt.Errorf("read the rows whose node names no project: %w", err)
	}
	defer rows.Close()
	var incomplete []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			return nil, fmt.Errorf("read the rows whose node names no project: %w", err)
		}
		incomplete = append(incomplete, row)
	}
	return incomplete, rows.Err()
}

// objectKind is a kind the backfill writes a node for from its rows.
type objectKind struct {
	// kind is the node's kind, and start the id the walk over its rows starts after.
	kind, start string
	// read is one batch of its rows its marker does not mark (unmarked), after the id $1 and at
	// most $2 of them, in the order of the index that keys them, as (id, created_at, author,
	// project, session, root author, root target). session is a conversation's, for a document
	// or an ask on one; the root columns are a message's on no issue alone: the author and the
	// target of its thread's root, its parties (graphstore.ThreadParties).
	read string
	// rows answers, of the ids $1, each a row of the kind holds now, with its created_at: what
	// tells the row the batch read from another a build before this one wrote at the same address
	// since, which a node built from the first would misdate.
	rows string
	// owners answers, for a kind some writer deletes rows of, the owner row each row of the ids $1
	// stands under, as (issue, document id, id): the row that writer takes before anything else
	// (api's removeArtifact takes its document's issue row). It is empty for a kind no writer
	// deletes a row of.
	owners string
}

// unmarked is the backfill's marker for these kinds, unset: the store holds no live node of kind
// at the address id. The node is the marker, since no row of these kinds names its node in a
// column of its own, and a live one, since a row whose node is gone is one a build before this
// one created at an address this build's removal stated gone, and is owed a live node as much as a
// row with none (classGone).
func unmarked(kind, id string) string {
	return `not exists (select 1 from graph_nodes n where n.kind = '` + kind + `' and n.id = ` + id +
		` and n.state = '` + graphstore.StateLive + `')`
}

// objectKinds are the kinds the backfill writes a node for from their rows.
var objectKinds = []objectKind{
	{kind: graphstore.KindIssue, read: `
		select i.key, i.created_at, i.created_by, i.project_key, null::text, null::jsonb, null::text
		from issues i
		where i.key > $1 and ` + unmarked(graphstore.KindIssue, "i.key") + `
		order by i.key limit $2`,
		rows: `select key, created_at from issues where key = any($1::text[])`},
	{kind: graphstore.KindArtifact, read: `
		select a.ref_key, a.created_at, a.created_by, a.project_key, a.session_id, null::jsonb, null::text
		from artifacts a
		where a.ref_key > $1 and octet_length(a.ref_key) <= ` + strconv.Itoa(graphstore.MaxAddressBytes) + `
		  and ` + unmarked(graphstore.KindArtifact, "a.ref_key") + `
		order by a.ref_key limit $2`,
		rows:   `select ref_key, created_at from artifacts where ref_key = any($1::text[])`,
		owners: `select coalesce(issue_key, ''), id::text, ref_key from artifacts where ref_key = any($1::text[])`},
	{kind: graphstore.KindAsk, start: zeroUUID, read: `
		select k.id::text, k.created_at, k.author, coalesce(i.project_key, a.project_key),
		       case when coalesce(i.project_key, a.project_key) is null then a.session_id end, null::jsonb, null::text
		from asks k left join issues i on i.key = k.issue_key left join artifacts a on a.id = k.artifact_id
		where k.id > $1::uuid and ` + unmarked(graphstore.KindAsk, "k.id::text") + `
		order by k.id limit $2`,
		rows: `select id::text, created_at from asks where id = any($1::text[]::uuid[])`},
	{kind: graphstore.KindMessage, start: zeroUUID, read: `
		with recursive batch as (
			select m.id, m.issue_key, m.in_reply_to, m.created_at, m.author
			from messages m
			where m.id > $1::uuid and ` + unmarked(graphstore.KindMessage, "m.id::text") + `
			order by m.id limit $2
		),
		walk (message, at, in_reply_to) as (
			select batch.id, batch.id, batch.in_reply_to from batch where batch.issue_key is null
			union all
			select walk.message, parent.id, parent.in_reply_to from walk join messages parent on parent.id = walk.in_reply_to
		)
		select batch.id::text, batch.created_at, batch.author, i.project_key, null::text, root.author, root.target
		from batch
		left join issues i on i.key = batch.issue_key
		left join walk on walk.message = batch.id and walk.in_reply_to is null
		left join messages root on root.id = walk.at
		order by batch.id`,
		rows: `select id::text, created_at from messages where id = any($1::text[]::uuid[])`},
}

// ObjectRowStands answers whether a row of kind stands at id, for a kind the backfill writes a
// node for from its rows (objectKinds), and object false for any other kind. The erase reads it to
// refuse a node of these kinds while its row holds its text (grapherase's Erasure.read).
func ObjectRowStands(ctx context.Context, q graphstore.Queryer, kind, id string) (stands, object bool, err error) {
	for _, known := range objectKinds {
		if known.kind != kind {
			continue
		}
		standing, err := readStanding(ctx, q, known, []string{id})
		if err != nil {
			return false, true, fmt.Errorf("read the %s row of %s: %w", kind, id, err)
		}
		return len(standing) > 0, true, nil
	}
	return false, false, nil
}

// readStanding reads, of ids, each row of kind that stands, with its created_at (objectKind.rows).
func readStanding(ctx context.Context, q graphstore.Queryer, kind objectKind, ids []string) (map[string]time.Time, error) {
	rows, err := q.Query(ctx, kind.rows, ids)
	if err != nil {
		return nil, err
	}
	standing := make(map[string]time.Time, len(ids))
	var id string
	var created time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &created}, func() error {
		standing[id] = created
		return nil
	}); err != nil {
		return nil, err
	}
	return standing, nil
}

// zeroUUID is where a walk over a uuid key starts: every id sorts after it.
const zeroUUID = "00000000-0000-0000-0000-000000000000"

// backfillObjectNodes writes a live node for every issue, document, ask and message written before
// its kind's writer wrote one, and for every one a build before this one created at an address
// whose node this build stated gone, under the backfill's contract: it walks only the rows its
// marker does not mark - the marker is a live node (unmarked) - dates each node at its row's own
// created_at, made by the row's own author, and writes the marker in the transaction that writes
// the node, so a second run reads no row the first one wrote. It appends no event, and every fact
// it states carries the sentinel.
func backfillObjectNodes(ctx context.Context, database *store.Store) ([]KindBackfill, error) {
	var done []KindBackfill
	for _, kind := range objectKinds {
		report := KindBackfill{Kind: kind.kind}
		after := kind.start
		for {
			nodes, err := readObjectNodes(ctx, database, kind.kind, kind.read, after, backfillBatch)
			if err != nil {
				return done, err
			}
			if len(nodes) == 0 {
				break
			}
			after = nodes[len(nodes)-1].ID
			written, err := writeObjectBatch(ctx, database, kind, nodes)
			if err != nil {
				return done, err
			}
			report.Rows += written.Rows
			report.Nodes += written.Nodes
			report.Tuples += written.Tuples
			if len(nodes) < backfillBatch {
				break
			}
		}
		left, err := readObjectNodes(ctx, database, kind.kind, kind.read, kind.start, math.MaxInt32)
		if err != nil {
			return done, err
		}
		report.Remaining = int64(len(left))
		report.Complete = report.Remaining == 0
		done = append(done, report)
	}
	return done, nil
}

// statedOf is how many base attributes the node write states for nodes it makes: visibility and
// state for each, and its project or its parties.
func statedOf(nodes []graphstore.Node) int64 {
	var stated int64
	for _, node := range nodes {
		stated += 2
		if node.Project != "" || len(node.Parties) > 0 {
			stated++
		}
	}
	return stated
}

// readObjectNodes reads one batch of a kind's rows no live node marks, and answers the node its
// kind's writer writes for each (graphstore.IssueNode and its siblings).
func readObjectNodes(ctx context.Context, database *store.Store, kind, read, after string, limit int) ([]graphstore.Node, error) {
	rows, err := database.Pool.Query(ctx, read, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read the %s rows no live node marks: %w", kind, err)
	}
	defer rows.Close()
	var nodes []graphstore.Node
	for rows.Next() {
		var id string
		var createdAt time.Time
		var authorJSON, rootAuthorJSON []byte
		var project, session, rootTarget *string
		if err := rows.Scan(&id, &createdAt, &authorJSON, &project, &session, &rootAuthorJSON, &rootTarget); err != nil {
			return nil, fmt.Errorf("read a %s row no live node marks: %w", kind, err)
		}
		var author model.Actor
		if err := json.Unmarshal(authorJSON, &author); err != nil {
			return nil, fmt.Errorf("read the author of %s %s: %w", kind, id, err)
		}
		var parties []string
		if session != nil {
			parties = []string{graphstore.PartySession(*session)}
		}
		switch kind {
		case graphstore.KindIssue:
			nodes = append(nodes, graphstore.IssueNode(id, derefText(project), author, createdAt))
		case graphstore.KindArtifact:
			nodes = append(nodes, graphstore.ArtifactNode(id, derefText(project), derefText(session), author, createdAt))
		case graphstore.KindAsk:
			nodes = append(nodes, graphstore.AskNode(id, derefText(project), parties, author, createdAt))
		case graphstore.KindMessage:
			if project == nil {
				var root model.Actor
				if len(rootAuthorJSON) > 0 {
					if err := json.Unmarshal(rootAuthorJSON, &root); err != nil {
						return nil, fmt.Errorf("read the thread root's author of message %s: %w", id, err)
					}
				}
				parties = graphstore.ThreadParties(root, rootTarget)
			}
			nodes = append(nodes, graphstore.MessageNode(id, derefText(project), parties, author, createdAt))
		}
	}
	return nodes, rows.Err()
}

func derefText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// writeObjectBatch writes one batch's nodes in one transaction (graphstore.Write), which takes
// their rows in ascending (kind, id) and states each one's base attributes, and answers what it
// wrote. A node a live writer wrote meanwhile is written again with nothing moved, and states
// nothing; a gone node is moved live, which states its state alone; one in another project fails
// the batch (graphstore.ErrProjectMoved), which is a difference for the census to report rather
// than for this run to paper over.
//
// The batch was read before this transaction opened, and a removal may commit in between: a node
// written live then would stand behind no row. So for a kind a writer deletes rows of
// (objectKind.owners) the transaction first takes the owner row each of its rows stands under
// (lockObjectOwners): api's removeArtifact takes the document's issue row before it states the
// node gone and deletes the row, and a removal of a build before this one takes it too and states
// nothing. Under those rows it reads which of the batch's rows stand and when each was created
// (objectKind.rows), and writes the node of each row that is still the row the batch read, and of
// no other: a row a removal deleted before the lock has its node left as the removal left it, and
// a row a build before this one wrote at the same address since has another created_at, so the
// node the batch built from the first would misdate it; it is left for the next run, which the
// report's remaining count says. A removal that waits for this batch removes a row whose node is
// live, and this build's removal then states that node gone. The owner rows come first and the
// nodes after them, the order every writer of these nodes takes (a document's create, its removal,
// the erase). A kind no writer deletes a row of takes no lock and is read again the same way.
func writeObjectBatch(ctx context.Context, database *store.Store, kind objectKind, nodes []graphstore.Node) (KindBackfill, error) {
	ids := make([]string, len(nodes))
	for index, node := range nodes {
		ids[index] = node.ID
	}
	var written KindBackfill
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		if kind.owners != "" {
			if err := lockObjectOwners(ctx, tx, kind, ids); err != nil {
				return err
			}
		}
		standing, err := readStanding(ctx, tx, kind, ids)
		if err != nil {
			return fmt.Errorf("read which rows of the batch stand: %w", err)
		}
		// The nodes the store holds of the batch already: each is gone, since the batch read the
		// rows no live node marks, and the write states its state alone.
		held, err := heldNodes(ctx, tx, kind.kind, ids)
		if err != nil {
			return fmt.Errorf("read the nodes the store holds of the batch: %w", err)
		}
		write := make([]graphstore.Node, 0, len(standing))
		for _, node := range nodes {
			if created, stands := standing[node.ID]; !stands || !created.Equal(node.MadeAt) {
				continue
			}
			write = append(write, node)
			if _, gone := held[node.ID]; gone {
				written.Tuples++
			} else {
				written.Tuples += statedOf([]graphstore.Node{node})
			}
		}
		written.Rows, written.Nodes = int64(len(write)), int64(len(write))
		if len(write) == 0 {
			return nil
		}
		return graphstore.Write(ctx, tx, write, nil)
	}); err != nil {
		return KindBackfill{}, fmt.Errorf("write a batch of %s nodes: %w", kind.kind, err)
	}
	return written, nil
}

// heldNodes is the ids of the nodes of kind the store holds of ids.
func heldNodes(ctx context.Context, tx pgx.Tx, kind string, ids []string) (map[string]struct{}, error) {
	rows, err := tx.Query(ctx, `select id from graph_nodes where kind = $1 and id = any($2::text[])`, kind, ids)
	if err != nil {
		return nil, err
	}
	held := make(map[string]struct{}, len(ids))
	var id string
	if _, err := pgx.ForEachRow(rows, []any{&id}, func() error {
		held[id] = struct{}{}
		return nil
	}); err != nil {
		return nil, err
	}
	return held, nil
}

// lockObjectOwners takes the owner row each row of ids stands under (objectKind.owners), each once
// and in owner key order (events.LockOwnerRows).
func lockObjectOwners(ctx context.Context, tx pgx.Tx, kind objectKind, ids []string) error {
	rows, err := tx.Query(ctx, kind.owners, ids)
	if err != nil {
		return fmt.Errorf("read the owners of the batch's %s rows: %w", kind.kind, err)
	}
	owners := events.OwnerRows{}
	var issue, document, id string
	if _, err := pgx.ForEachRow(rows, []any{&issue, &document, &id}, func() error {
		owners.Add(issue, document, kind.kind+" "+id)
		return nil
	}); err != nil {
		return fmt.Errorf("read the owners of the batch's %s rows: %w", kind.kind, err)
	}
	return events.LockOwnerRows(ctx, tx, owners)
}

// scopeKinds are the kinds whose writers stored nodes before a node named its project, each with
// the read of one batch of those nodes, after the id $1 and at most $2 of them, as (id, project,
// session): the comment nodes and the block nodes, whose project is their row's
// (scopedRows' rule).
var scopeKinds = []struct{ kind, read string }{
	{graphstore.KindComment, `
		select n.id, coalesce(i.project_key, owner.project_key),
		       case when coalesce(i.project_key, owner.project_key) is null then owner.session_id end
		from graph_nodes n
		join comments c on c.id = n.id::uuid
		left join issues i on i.key = c.issue_key
		left join artifacts owner on owner.id = c.artifact_id
		where n.kind = 'comment' and n.id > $1 and n.project_key is null and n.parties is null
		order by n.id limit $2`},
	{refs.BlockNodeKind, `
		select n.id, a.project_key, case when a.project_key is null then a.session_id end
		from graph_nodes n
		join artifacts a on a.ref_key = split_part(n.id, '#', 1)
		where n.kind = 'block' and n.id > $1 and n.project_key is null and n.parties is null
		order by n.id limit $2`},
}

// backfillScopes gives every comment and block node the comment and block writers stored before a
// node named its project its project, or its conversation's party (graphstore.FillScopes), under
// the same contract: its marker is the node's own project_key and parties, null on exactly the
// nodes it has to fill, and the fill writes them in the transaction that states the fact. A block
// node whose document is gone has no row to read a project from, and is left as it is: no row of
// the census holds it, and no read answers it.
func backfillScopes(ctx context.Context, database *store.Store) ([]KindBackfill, error) {
	var done []KindBackfill
	for _, kind := range scopeKinds {
		report := KindBackfill{Kind: kind.kind + "_project"}
		after := ""
		for {
			scopes, err := readScopes(ctx, database, kind.kind, kind.read, after, backfillBatch)
			if err != nil {
				return done, err
			}
			if len(scopes) == 0 {
				break
			}
			after = scopes[len(scopes)-1].ID
			filled, err := writeScopeBatch(ctx, database, kind.kind, scopes)
			if err != nil {
				return done, err
			}
			report.Rows += int64(len(scopes))
			report.Nodes += filled
			report.Tuples += filled
			if len(scopes) < backfillBatch {
				break
			}
		}
		left, err := readScopes(ctx, database, kind.kind, kind.read, "", math.MaxInt32)
		if err != nil {
			return done, err
		}
		report.Remaining = int64(len(left))
		report.Complete = report.Remaining == 0
		done = append(done, report)
	}
	return done, nil
}

// writeScopeBatch fills one batch's scopes in one transaction. A block node is written under the
// revision fence (refs.IndexBlocks), so the fill of a block's node takes the fence of each
// document it fills first, as every other writer of a block's node does (lockBlockDocuments); a
// comment's node is written under no fence, and its fill takes the node rows alone.
func writeScopeBatch(ctx context.Context, database *store.Store, kind string, scopes []graphstore.Scope) (int64, error) {
	var filled int64
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		if kind == refs.BlockNodeKind {
			documents, err := blockDocuments(ctx, tx, scopes)
			if err != nil {
				return err
			}
			if err := lockBlockDocuments(ctx, tx, documents); err != nil {
				return err
			}
		}
		var err error
		filled, err = graphstore.FillScopes(ctx, tx, scopes)
		return err
	}); err != nil {
		return 0, fmt.Errorf("fill the project of a batch of %s nodes: %w", kind, err)
	}
	return filled, nil
}

func readScopes(ctx context.Context, database *store.Store, kind, read, after string, limit int) ([]graphstore.Scope, error) {
	rows, err := database.Pool.Query(ctx, read, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read the %s nodes naming no project: %w", kind, err)
	}
	defer rows.Close()
	var scopes []graphstore.Scope
	for rows.Next() {
		var scope graphstore.Scope
		var project, session *string
		if err := rows.Scan(&scope.ID, &project, &session); err != nil {
			return nil, fmt.Errorf("read a %s node naming no project: %w", kind, err)
		}
		scope.Kind, scope.Project = kind, derefText(project)
		if session != nil {
			scope.Parties = []string{graphstore.PartySession(*session)}
		}
		scopes = append(scopes, scope)
	}
	return scopes, rows.Err()
}

// blockDocuments is the ids of the documents the block scopes belong to, read in tx off each
// block's address, which is its document's ref_key, '#' and its own id.
func blockDocuments(ctx context.Context, tx pgx.Tx, scopes []graphstore.Scope) ([]string, error) {
	refKeys := make([]string, len(scopes))
	for index, scope := range scopes {
		refKeys[index], _, _ = strings.Cut(scope.ID, "#")
	}
	rows, err := tx.Query(ctx, `select id::text from artifacts where ref_key = any($1)`, refKeys)
	if err != nil {
		return nil, fmt.Errorf("read the documents of a batch of block nodes: %w", err)
	}
	defer rows.Close()
	var documents []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read the documents of a batch of block nodes: %w", err)
		}
		documents = append(documents, id)
	}
	return documents, rows.Err()
}
