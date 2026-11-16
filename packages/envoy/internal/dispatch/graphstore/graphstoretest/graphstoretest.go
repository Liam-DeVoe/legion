// Package graphstoretest holds what the tests of other packages share to seed the node and tuple
// store with rows its writers refuse to write. Nothing a deployment builds imports it.
package graphstoretest

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// SeedAuthorOnly reserves each of nodes for its author by writing its row straight into the
// store, in the caller's transaction: the node row at visibility author, with the base
// attributes the store's writer states of a node its insert makes (graphstore.BaseAttributeFacts)
// for a node the store does not hold, and the one visibility tuple that move states for a node it
// holds at another visibility. A node already reserved for its author moves nothing and states
// nothing.
//
// The store's writers take a node reserved for its author only of a kind the read registry
// flags (graphstore.AdmitAuthorOnly), and no kind dispatch://INFRA-1149 registers is flagged,
// since not every read of those kinds binds the visibility predicate. The reads that do bind it
// are still held to answering such a node to its author alone, and a test of them seeds the node
// here, as the rows a writer would have stored, rather than through a writer that refuses it.
func SeedAuthorOnly(ctx context.Context, tx pgx.Tx, nodes ...graphstore.Node) error {
	for _, node := range nodes {
		node.Visibility = graphstore.VisibilityAuthor
		madeBy, err := json.Marshal(node.MadeBy.Recorded())
		if err != nil {
			return fmt.Errorf("encode the writer of %s %s: %w", node.Kind, node.ID, err)
		}
		var held bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from graph_nodes where kind = $1 and id = $2)`,
			node.Kind, node.ID).Scan(&held); err != nil {
			return fmt.Errorf("read whether the store holds %s %s: %w", node.Kind, node.ID, err)
		}
		var facts []graphstore.UncausedTuple
		if held {
			tag, err := tx.Exec(ctx, `update graph_nodes set visibility = $3 where kind = $1 and id = $2 and visibility <> $3`,
				node.Kind, node.ID, node.Visibility)
			if err != nil {
				return fmt.Errorf("reserve %s %s for its author: %w", node.Kind, node.ID, err)
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			facts = []graphstore.UncausedTuple{{Tuple: graphstore.Tuple{
				FromKind: node.Kind, FromID: node.ID, Kind: graphstore.AttributeVisibility,
				ToKind: graphstore.KindValue, ToID: node.Visibility,
				At: node.MadeAt, Who: node.MadeBy, SourceEvent: node.SourceEvent, Asserted: true,
			}}}
		} else {
			attributes := []byte("{}")
			if node.Attributes != nil {
				if attributes, err = json.Marshal(node.Attributes); err != nil {
					return fmt.Errorf("encode the attributes of %s %s: %w", node.Kind, node.ID, err)
				}
			}
			var project *string
			if node.Project != "" {
				project = &node.Project
			}
			var parties []string
			if len(node.Parties) > 0 {
				parties = node.Parties
			}
			if _, err := tx.Exec(ctx, `
				insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event, project_key, parties)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			`, node.Kind, node.ID, node.Supertag, node.Visibility, node.State, attributes, node.MadeAt, madeBy,
				node.SourceEvent, project, parties); err != nil {
				return fmt.Errorf("write the row of %s %s: %w", node.Kind, node.ID, err)
			}
			facts = graphstore.BaseAttributeFacts([]graphstore.Node{node})
		}
		for _, fact := range facts {
			who, err := json.Marshal(fact.Who.Recorded())
			if err != nil {
				return fmt.Errorf("encode the writer of %s %s's %s: %w", node.Kind, node.ID, fact.Kind, err)
			}
			if _, err := tx.Exec(ctx, `
				insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
				values ($1, $2, $3, $4, $5, $6, $7, $8, null, $9)
			`, fact.FromKind, fact.FromID, fact.Kind, fact.ToKind, fact.ToID, fact.At, who, fact.SourceEvent, fact.Asserted); err != nil {
				return fmt.Errorf("state %s %s's %s: %w", node.Kind, node.ID, fact.Kind, err)
			}
		}
	}
	return nil
}
