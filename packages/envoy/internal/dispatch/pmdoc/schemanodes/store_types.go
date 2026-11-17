package schemanodes

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// StoreType is one type of the store that declares no block type - an issue, a comment, a lens, a
// lens kind, a constraint - with the attributes its `attribute_of` tuples name, in the order its
// attribute nodes record. The block schema Read builds reads past such a type, since no document
// holds a block of it, so this is how a reader learns what the store's own types hold.
type StoreType struct {
	Name       string
	Attributes []StoreAttribute
}

// StoreAttribute is one attribute of a StoreType: the name its node carries and the kind of value
// it holds.
type StoreAttribute struct {
	Name string
	Kind pmdoc.BlockAttributeKind
}

// StoreTypes reads every supertag node the store holds at any state but gone that declares no
// block type, by name, each with its attributes, in one read-only transaction at repeatable read
// as Read reads the block types, so a migration that commits between its reads is read whole or
// not at all. It is read from the store on each call, so a type written later is answered by the
// next call of every process.
func StoreTypes(ctx context.Context, db Beginner) ([]StoreType, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin the read of the store's types: %w", err)
	}
	// The transaction reads only, so its end is a rollback whatever happened.
	defer func() { _ = tx.Rollback(context.Background()) }()
	attributes, err := readAttributeNodes(ctx, tx)
	if err != nil {
		return nil, err
	}
	supertags, err := graphstore.NodesOfKind(ctx, tx, KindSupertag)
	if err != nil {
		return nil, err
	}
	ties, err := attributeTies(ctx, tx)
	if err != nil {
		return nil, err
	}
	var types []StoreType
	for _, node := range supertags {
		if node.State == graphstore.StateGone {
			continue
		}
		var carried supertagAttributes
		if err := decodeNodeAttributes(node, &carried); err != nil {
			return nil, err
		}
		if carried.BlockType {
			continue
		}
		held := make([]attributeAttributes, 0, len(ties[node.ID]))
		for _, id := range ties[node.ID] {
			if attribute, present := attributes[id]; present {
				held = append(held, attribute)
			}
		}
		sort.SliceStable(held, func(left, right int) bool {
			if held[left].Position != held[right].Position {
				return held[left].Position < held[right].Position
			}
			return held[left].Name < held[right].Name
		})
		typ := StoreType{Name: node.ID, Attributes: make([]StoreAttribute, len(held))}
		for index, attribute := range held {
			typ.Attributes[index] = StoreAttribute{Name: attribute.Name, Kind: attribute.Kind}
		}
		types = append(types, typ)
	}
	sort.Slice(types, func(left, right int) bool { return types[left].Name < types[right].Name })
	return types, nil
}

// attributeTies is, for each supertag, the attribute nodes its current `attribute_of` tuples
// come from, in one read over graph_tuples_inbound for every supertag at once.
func attributeTies(ctx context.Context, q graphstore.Queryer) (map[string][]string, error) {
	rows, err := q.Query(ctx, `
		select newest.to_id, newest.from_id from (
			`+graphstore.CurrentTupleRead(`t.to_id, t.from_kind, t.from_id`, `t.to_id, t.from_id, t.asserted`,
		`t.to_kind = $1 and t.kind = $2 and t.from_kind = $3 and t.to_kind <> '`+graphstore.KindValue+`'`)+`
		) newest
		where newest.asserted`, KindSupertag, TupleAttributeOf, KindAttribute)
	if err != nil {
		return nil, fmt.Errorf("read the attributes tied to each type: %w", err)
	}
	defer rows.Close()
	ties := map[string][]string{}
	for rows.Next() {
		var supertag, attribute string
		if err := rows.Scan(&supertag, &attribute); err != nil {
			return nil, fmt.Errorf("read the attributes tied to each type: %w", err)
		}
		ties[supertag] = append(ties[supertag], attribute)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the attributes tied to each type: %w", err)
	}
	return ties, nil
}
