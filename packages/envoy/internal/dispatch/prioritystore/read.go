package prioritystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// tupleColumns is what every tuple read answers, in the order scanTuples takes them.
const tupleColumns = `seq, from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted`

// nodeColumns is what every node read answers, in the order scanNode takes them.
const nodeColumns = `kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event`

// AdvancesTupleRead is the one reading of the tuple log the `advances` relation is answered by:
// the newest tuple of each (from_id, to_id) pair of the relation, which a `where asserted` over
// its answer then cuts to the relations standing now. Its seq is a `graph_tuples` seq, which is
// what the revision of an issue's own set is read off (api/issue_queries.go). It is SQL text
// rather than a read, because every caller splices it into a statement over a page of issues or
// over a whole corpus: the priorities read and the issue reads (api/priority_queries.go,
// api/issue_queries.go) and the seed's report of its corpus (cmd/dispatch-seed). Those readings
// differ only in the columns each carries out and the one predicate that narrows it, and one
// reading of the log written in each of them would be several to keep in step, which is how the
// seed's report once counted a retracted relation the route did not.
//
// columns is what each row carries beside the two ids, written to follow them, and narrow is the
// predicate that bounds the read, written inside the `where` so it cuts the rows the `distinct
// on` sorts rather than its answer; an empty narrow reads the whole relation. The caller writes
// both from its own text and never from a value a request carried.
//
// from_kind, kind and to_kind are each bound by equality: every `advances` tuple is written out
// of an issue with ToKind KindPriority (relate), and `graph_tuples` holds every fact of the store,
// so the three are what keep the read to this one relation. The equalities prove the predicate of
// graph_tuples_current (to_kind <> 'value' and kind <> 'delivered_to'), whose column order this
// `distinct on` walks (from_kind, from_id, kind, to_kind, to_id, seq desc), so a read narrowed to
// a page of issues probes that index issue by issue. A read of the whole relation can take
// graph_tuples_as_of_in instead, which leads with to_kind and holds kind third, and reads off it
// the tuples into priorities alone.
func AdvancesTupleRead(columns, narrow string) string {
	return `select distinct on (t.from_id, t.to_id) t.from_id, t.to_id` + columns + `
		from graph_tuples t
		where t.from_kind = '` + KindIssue + `' and t.kind = '` + RelationAdvances + `'
			and t.to_kind = '` + KindPriority + `'
			` + narrow + `
		order by t.from_id, t.to_id, t.seq desc`
}

// History answers every tuple of one node under one kind, in the order they were written, each
// with its value or its far end, its time and its writer. It is one read for an attribute and a
// relation alike: an attribute's history is three values under one name, a relation's is the
// asserting and retracting of far ends, and the rows have one shape, so a caller cannot tell from
// the answer which of the two it asked for. It is the store's own reading of one node's history
// (graphstore.History), since a priority's facts, and an issue's `advances`, are the store's
// tuples.
func History(ctx context.Context, q Queryer, fromKind, fromID, kind string) ([]Tuple, error) {
	return graphstore.History(ctx, q, fromKind, fromID, kind)
}

// Read answers one priority's node row, the projection of its tuples, and whether the store holds
// it. It answers a node whose state is StateGone too: a caller that serves the page of a priority
// somebody deleted reads the row and says so.
func Read(ctx context.Context, q Queryer, id string) (Node, bool, error) {
	node, err := scanNode(q.QueryRow(ctx, `
		select `+nodeColumns+`
		from graph_nodes
		where kind = '`+KindPriority+`' and id = $1
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, fmt.Errorf("read priority %s: %w", id, err)
	}
	return node, true, nil
}

// List answers every live priority with its attributes, in the order they are ranked, which is
// the order the priorities page draws. It reads through the primary key of `graph_nodes`, whose
// leading column, the kind, it binds, so it costs the priorities the store holds and none of the
// other nodes beside them. The id breaks a tie, because nothing makes a rank unique. The order is
// the key's own byte order, which is what rankKey (rank.go) names and why.
func List(ctx context.Context, q Queryer) ([]Node, error) {
	rows, err := q.Query(ctx, `
		select `+nodeColumns+`
		from graph_nodes
		where kind = '`+KindPriority+`' and state = $1
		order by `+rankKey+`, id
	`, StateLive)
	if err != nil {
		return nil, fmt.Errorf("read the live priorities: %w", err)
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the live priorities: %w", err)
	}
	return nodes, nil
}

// Timeline answers one priority's whole history, newest first: every attribute it was written
// with, and every `advances` relation asserted or retracted on it, each with its writer and its
// time. It is what the priority page's margin lists, and it is two index scans — the tuples
// written on the node, which graph_tuples_as_of_out keys by the node with no predicate of its
// own, and the `advances` tuples into it, which graph_tuples_as_of_in keys by the far end and
// whose predicate to_kind <> 'value' the equality to_kind = 'priority' proves — merged on the log
// position they were appended at, which is the order they happened in.
func Timeline(ctx context.Context, q Queryer, id string) ([]Tuple, error) {
	rows, err := q.Query(ctx, `
		select `+tupleColumns+` from (
			select `+tupleColumns+`
			from graph_tuples
			where from_kind = '`+KindPriority+`' and from_id = $1
			union all
			select `+tupleColumns+`
			from graph_tuples
			where to_kind = '`+KindPriority+`' and to_id = $1 and kind = '`+RelationAdvances+`'
		) stated
		order by seq desc
	`, id)
	if err != nil {
		return nil, fmt.Errorf("read the history of priority %s: %w", id, err)
	}
	return scanTuples(rows)
}

// scanNode reads one node row, as Read, List and a write's lockedNode take it.
func scanNode(row pgx.Row) (Node, error) {
	var node Node
	var attributes, madeBy []byte
	if err := row.Scan(&node.Kind, &node.ID, &node.Supertag, &node.Visibility, &node.State,
		&attributes, &node.MadeAt, &madeBy, &node.SourceEvent); err != nil {
		return Node{}, err
	}
	if err := json.Unmarshal(attributes, &node.Attributes); err != nil {
		return Node{}, fmt.Errorf("decode attributes of priority %s: %w", node.ID, err)
	}
	if err := json.Unmarshal(madeBy, &node.MadeBy); err != nil {
		return Node{}, fmt.Errorf("decode writer of priority %s: %w", node.ID, err)
	}
	return node, nil
}

func scanTuples(rows pgx.Rows) ([]Tuple, error) {
	defer rows.Close()
	var tuples []Tuple
	for rows.Next() {
		var tuple Tuple
		var who []byte
		if err := rows.Scan(&tuple.Seq, &tuple.FromKind, &tuple.FromID, &tuple.Kind, &tuple.ToKind,
			&tuple.ToID, &tuple.At, &who, &tuple.SourceEvent, &tuple.LegacySourceSeq, &tuple.Asserted); err != nil {
			return nil, fmt.Errorf("read priority tuple: %w", err)
		}
		if err := json.Unmarshal(who, &tuple.Who); err != nil {
			return nil, fmt.Errorf("decode priority tuple writer: %w", err)
		}
		tuples = append(tuples, tuple)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read priority tuples: %w", err)
	}
	return tuples, nil
}
