package graphcensus

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The backfill of the project of every node: the nodes of the issues, documents, asks and messages
// written before their kind's writer wrote one (backfillObjectNodes), and the project of the
// comment and block nodes written before a node named one (backfillScopes). projects.go holds what
// a node must carry and the census that reads it.

// objectKind is a kind the backfill writes a node for from its rows.
type objectKind struct {
	// kind is the node's kind, and start the id the walk over its rows starts after.
	kind, start string
	// read is one batch of its rows its marker does not mark (unmarked), after the id $1 and at
	// most $2 of them, in the order of the index that keys them, as (id, created_at, author,
	// project, session, root author, root target, own event). session is a conversation's, for a
	// document or an ask on one; the root columns are a message's on no issue alone: the author and
	// the target of its thread's root, its parties (graphstore.ThreadParties). own event is a
	// document's own create event (ownCreateEvent), null for the other kinds and for a row that has
	// none; the row's node is stated under it.
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
// at the address id that names a project or parties. The node is the marker, since no row of these
// kinds names its node in a column of its own; a live one, since a row whose node is gone is one a
// build before this one created at an address this build's removal stated gone, and is owed a
// live node as much as a row with none (classGone); and one naming a project or parties, since a
// node naming neither is the census's unscoped class, which this pass cures by stating them.
func unmarked(kind, id string) string {
	return `not exists (select 1 from graph_nodes n where n.kind = '` + kind + `' and n.id = ` + id +
		` and n.state = '` + graphstore.StateLive + `' and ` + scopedNode + `)`
}

// scopedNode is, for the node n, whether it names a project or parties.
const scopedNode = `(n.project_key is not null or n.parties is not null)`

// unmarkedDocument is unmarked for a document, whose address a later row takes again where a
// removal can take the row away: the store holds no live node naming a project or parties at the
// row a's address that is that row's own - any such node, for a row no removal takes, and for one
// a removal can take, one whose live statement was made for it (ownLiveStatement). A live node
// whose live statement is not the row's is a document a build before this one removed and then
// made again at the address before the node was erased, and that row is owed its own live
// statement as much as a row with none (classGone).
func unmarkedDocument(a string) string {
	return `not exists (select 1 from graph_nodes n where n.kind = '` + graphstore.KindArtifact + `' and n.id = ` + a + `.ref_key` +
		` and n.state = '` + graphstore.StateLive + `' and ` + scopedNode + ` and (not ` + removableDocument(a) + ` or ` +
		ownLiveStatement("n", a+".id::text", a+".issue_key") + `))`
}

// ownCreateEvent is the id of the artifacts row a's own create event, the artifact.created whose
// payload names the row, or null for a row that has none: a document no create recorded an event
// of (an issue's spec, which issue.created records, a conversation's file, whose owner records no
// event) and one made before Dispatch recorded artifact.created. An issue's document's event is its
// issue's, and a project's document's is its own (events.artifact_id), each read by the index that
// keys that owner's events (the events table's unique (issue_key, seq) and (artifact_id, seq)),
// whose rows hold every event, published or not.
func ownCreateEvent(a string) string {
	return `(case when ` + a + `.issue_key is not null then (
			select e.id from events e
			where e.issue_key = ` + a + `.issue_key and e.type = 'artifact.created' and e.payload->'artifact'->>'id' = ` + a + `.id::text
			order by e.id limit 1)
		else (
			select e.id from events e
			where e.artifact_id = ` + a + `.id and e.type = 'artifact.created'
			order by e.id limit 1)
		end)`
}

// objectKinds are the kinds the backfill writes a node for from their rows.
var objectKinds = []objectKind{
	{kind: graphstore.KindIssue, read: `
		select i.key, i.created_at, i.created_by, i.project_key, null::text, null::jsonb, null::text, null::bigint
		from issues i
		where i.key > $1 and ` + unmarked(graphstore.KindIssue, "i.key") + `
		order by i.key limit $2`,
		rows: `select key, created_at from issues where key = any($1::text[])`},
	{kind: graphstore.KindArtifact, read: `
		select a.ref_key, a.created_at, a.created_by, a.project_key, a.session_id, null::jsonb, null::text, ` + ownCreateEvent("a") + `
		from artifacts a
		where a.ref_key > $1 and octet_length(a.ref_key) <= ` + strconv.Itoa(graphstore.MaxAddressBytes) + `
		  and ` + unmarkedDocument("a") + `
		order by a.ref_key limit $2`,
		rows:   `select ref_key, created_at from artifacts where ref_key = any($1::text[])`,
		owners: `select coalesce(issue_key, ''), id::text, ref_key from artifacts where ref_key = any($1::text[])`},
	{kind: graphstore.KindAsk, start: zeroUUID, read: `
		select k.id::text, k.created_at, k.author, coalesce(i.project_key, a.project_key),
		       case when coalesce(i.project_key, a.project_key) is null then a.session_id end, null::jsonb, null::text, null::bigint
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
		select batch.id::text, batch.created_at, batch.author, i.project_key, null::text, root.author, root.target, null::bigint
		from batch
		left join issues i on i.key = batch.issue_key
		left join walk on walk.message = batch.id and walk.in_reply_to is null
		left join messages root on root.id = walk.at
		order by batch.id`,
		rows: `select id::text, created_at from messages where id = any($1::text[]::uuid[])`},
}

// ObjectRowStands answers whether a row of kind stands at id, for a kind the backfill writes a
// node for from its rows (objectKinds), and false for any other kind. The erase reads it to refuse
// a node of these kinds while its row holds its text (grapherase's Erasure.read).
func ObjectRowStands(ctx context.Context, q graphstore.Queryer, kind, id string) (bool, error) {
	for _, known := range objectKinds {
		if known.kind != kind {
			continue
		}
		standing, err := readStanding(ctx, q, known, []string{id})
		if err != nil {
			return false, fmt.Errorf("read the %s row of %s: %w", kind, id, err)
		}
		return len(standing) > 0, nil
	}
	return false, nil
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
// whose node is gone or holds another row's live statement, under the backfill's contract: it
// walks only the rows its marker does not mark - the marker is a live node (unmarked), and for a
// document a removal can take a live node whose live statement is the row's own (unmarkedDocument)
// - and writes the marker in the transaction that writes the node, so a second run reads no row the
// first one wrote. Its input is the row's own content: a node it makes is made by the row's author
// at the row's created_at, and a node the store holds keeps the made_at and made_by it has. The
// row's author states the node live, and the backfill itself states gone a document's node that
// stood live for an earlier row; each is dated at the row's created_at or one microsecond past the
// newest statement about the node where that is later, so a read as of a time reads the order the
// store appended in. A document's statements are caused by its own create event where the read
// finds one, which is what names the row (ownLiveStatement); an issue's, an ask's and a message's,
// and a document's with no such event, are at the sentinel. It appends no event.
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
			report.Skipped += written.Skipped
			if len(nodes) < backfillBatch {
				break
			}
		}
		var remaining int64
		if err := database.Pool.QueryRow(ctx, `select count(*) from (`+kind.read+`) as remaining`, kind.start, nil).Scan(&remaining); err != nil {
			return done, fmt.Errorf("count the %s rows no live node marks: %w", kind.kind, err)
		}
		report.Remaining = remaining
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
// kind's writer writes for each (graphstore.IssueNode and its siblings), caused by the row's own
// create event where the read found one, and at the sentinel otherwise.
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
		var ownEvent *int64
		if err := rows.Scan(&id, &createdAt, &authorJSON, &project, &session, &rootAuthorJSON, &rootTarget, &ownEvent); err != nil {
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
				if parties, err = threadParties(id, rootAuthorJSON, rootTarget); err != nil {
					return nil, err
				}
			}
			nodes = append(nodes, graphstore.MessageNode(id, derefText(project), parties, author, createdAt))
		}
		if ownEvent != nil {
			nodes[len(nodes)-1].SourceEvent = *ownEvent
		}
	}
	return nodes, rows.Err()
}

// threadParties is the parties of message id on no issue: those of its thread's root, whose author
// and target the read answers (graphstore.ThreadParties). A root author the read did not find is
// the zero actor.
func threadParties(id string, rootAuthorJSON []byte, rootTarget *string) ([]string, error) {
	var root model.Actor
	if len(rootAuthorJSON) > 0 {
		if err := json.Unmarshal(rootAuthorJSON, &root); err != nil {
			return nil, fmt.Errorf("read the thread root's author of message %s: %w", id, err)
		}
	}
	return graphstore.ThreadParties(root, rootTarget), nil
}

func derefText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// writeObjectBatch writes one batch's nodes in one transaction, which takes their rows in ascending
// (kind, id) and states each one's base attributes, and answers what it wrote. A node a live
// writer wrote meanwhile is written again with nothing moved, and states nothing; a gone node is
// moved live, which states its state alone; one in another project fails the batch
// (graphstore.ErrProjectMoved), which is a difference for the census to report rather than for
// this run to paper over. Each node and what it states carry its row's own create event where the
// read found one (readObjectNodes), and the sentinel otherwise: the batch writes its nodes in
// ascending id, in runs of consecutive nodes of one cause, each run through graphstore.WriteUncaused
// and then graphstore.Caused under that cause, and the report counts the tuples those runs stated.
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
//
// Under those locks it reads the nodes the store holds of the batch (heldNodes). One that is live
// and its row's own (ownLiveStatement, for a document a removal can take; any live one for the
// rest, whose address no later row takes) was written meanwhile, by another run of the backfill
// that took the owner rows first, and is left as it is. A document whose node is live but not its
// row's own is one a build before this one removed and made again at the address: its node is
// first stated gone, by the backfill, and then live, under the row's own event, as this build's
// create states it (api's storeArtifact). A node the store holds is dated one microsecond past
// the newest statement it holds about it where the row's created_at is no later - a row a build
// before this one dated at its transaction's start can predate a removal it waited behind - so a
// read of the node as of a time reads the statements in the order the store appended them.
//
// A node between a party graphstore.PartiesValue refuses is skipped, counted in Skipped, and
// left for the census to name: one such row of an earlier build would otherwise fail every batch
// it falls in, and every run of the backfill with it.
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
		held, err := heldNodes(ctx, tx, kind.kind, ids)
		if err != nil {
			return err
		}
		var write []graphstore.Node
		// taken holds, by id, the gone statement of each node a live writer left live for an
		// earlier row, which goes in before that node's live one, under the same cause.
		taken := map[string]graphstore.Node{}
		for _, node := range nodes {
			if created, stands := standing[node.ID]; !stands || !created.Equal(node.MadeAt) {
				continue
			}
			if partiesRefused(node.Parties) {
				written.Skipped++
				continue
			}
			stored, found := held[node.ID]
			if found && stored.live && stored.own && stored.scoped {
				continue
			}
			if found && !stored.newest.Before(node.MadeAt) {
				node.MadeAt = stored.newest.Add(time.Microsecond)
			}
			if found && stored.live && !stored.own {
				gone := node
				gone.State, gone.MadeBy = graphstore.StateGone, backfillActor
				taken[node.ID] = gone
			}
			write = append(write, node)
		}
		written.Rows, written.Nodes = int64(len(write)), int64(len(write))
		for start := 0; start < len(write); {
			end := start + 1
			for end < len(write) && write[end].SourceEvent == write[start].SourceEvent {
				end++
			}
			run := write[start:end]
			var gone []graphstore.Node
			for _, node := range run {
				if statement, ok := taken[node.ID]; ok {
					gone = append(gone, statement)
				}
			}
			var stated graphstore.Uncaused
			if len(gone) > 0 {
				if stated, err = graphstore.WriteUncaused(ctx, tx, gone, nil); err != nil {
					return err
				}
			}
			made, err := graphstore.WriteUncaused(ctx, tx, run, nil)
			if err != nil {
				return err
			}
			made.Tuples = append(stated.Tuples, made.Tuples...)
			written.Tuples += int64(len(made.Tuples))
			if err := graphstore.Caused(ctx, tx, made, run[0].SourceEvent); err != nil {
				return err
			}
			start = end
		}
		return nil
	}); err != nil {
		return KindBackfill{}, fmt.Errorf("write a batch of %s nodes: %w", kind.kind, err)
	}
	return written, nil
}

// storedNode is what the store holds of one node of a batch: whether it is live, whether its live
// statement is its row's own, whether it names a project or parties, and when its newest statement
// is dated.
type storedNode struct {
	live, own, scoped bool
	newest            time.Time
}

// heldNodes answers, of the batch's ids, each node the store holds (storedNode). A node is its
// row's own when, for a document a removal can take, its live statement was made for that row
// (ownLiveStatement); any node of the other kinds, and of a document no removal takes, is its
// row's own, since no later row takes its address.
func heldNodes(ctx context.Context, tx pgx.Tx, kind string, ids []string) (map[string]storedNode, error) {
	rows, err := tx.Query(ctx, `
		select n.id, n.state = '`+graphstore.StateLive+`',
		       coalesce((select not `+removableDocument("a")+` or `+ownLiveStatement("n", "a.id::text", "a.issue_key")+`
		                 from artifacts a where n.kind = '`+graphstore.KindArtifact+`' and a.ref_key = n.id), true),
		       `+scopedNode+`,
		       (select max(t.at) from graph_tuples t where t.from_kind = n.kind and t.from_id = n.id)
		from graph_nodes n where n.kind = $1 and n.id = any($2::text[])
	`, kind, ids)
	if err != nil {
		return nil, fmt.Errorf("read the nodes the store holds of the batch's %s rows: %w", kind, err)
	}
	held := make(map[string]storedNode, len(ids))
	var id string
	var stored storedNode
	var newest *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &stored.live, &stored.own, &stored.scoped, &newest}, func() error {
		stored.newest = time.Time{}
		if newest != nil {
			stored.newest = *newest
		}
		held[id] = stored
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read the nodes the store holds of the batch's %s rows: %w", kind, err)
	}
	return held, nil
}

// partiesRefused reports whether graphstore.PartiesValue refuses parties, a party that is empty
// or holds white space: a node between them cannot be written, so every pass of the backfill
// skips its row, counted in Skipped, rather than fail the batch it falls in and every run after it.
func partiesRefused(parties []string) bool {
	_, err := graphstore.PartiesValue(parties)
	return err != nil
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
// the census holds it, and no read answers it. A scope between a party graphstore.PartiesValue
// refuses is skipped and counted in Skipped, as the object batch skips one (writeObjectBatch).
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
			fillable := make([]graphstore.Scope, 0, len(scopes))
			for _, scope := range scopes {
				if partiesRefused(scope.Parties) {
					report.Skipped++
					continue
				}
				fillable = append(fillable, scope)
			}
			if len(fillable) > 0 {
				filled, err := writeScopeBatch(ctx, database, kind.kind, fillable)
				if err != nil {
					return done, err
				}
				report.Rows += int64(len(fillable))
				report.Nodes += filled
				report.Tuples += filled
			}
			if len(scopes) < backfillBatch {
				break
			}
		}
		var remaining int64
		if err := database.Pool.QueryRow(ctx, `select count(*) from (`+kind.read+`) as remaining`, "", nil).Scan(&remaining); err != nil {
			return done, fmt.Errorf("count the %s nodes naming no project: %w", kind.kind, err)
		}
		report.Remaining = remaining
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
