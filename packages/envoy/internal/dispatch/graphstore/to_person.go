package graphstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The three relations that tie a thing to a person by what reached them (dispatch://INFRA-1152 R2
// and R4). TupleDeliveredTo (people.go) points from a write at each person it reached;
// TupleMentions points from a thing at the person a mention of it names, as it points at a
// document or an issue the thing cites; TupleParticipatesIn points from a person, or a session,
// at the root of a thread they took part in. Each of the three that names a person is appended by
// ToPerson and by nothing else.
//
// TupleDismissed is the read state a person's dismissal of a thread states, from the person to the
// thread's root, whose retraction a later write by somebody else appends (inboxwrite.Record).
const (
	TupleMentions       = "mentions"
	TupleParticipatesIn = "participates_in"
	TupleDismissed      = "dismissed"
)

// ErrReservedForAuthor refuses a delivered_to, a mentions or a participates_in tuple between a
// node reserved for its author (VisibilityAuthor) and a person who is not that author: the read
// of a person's own node (R1) answers only what that person may see, so such a tuple would be
// mail nobody can read (R2).
var ErrReservedForAuthor = errors.New("a node reserved for its author reaches no other person")

// ErrPersonTupleOutsideToPerson refuses one of the three kinds ToPerson appends, naming a person,
// handed to any other writer of this store: ToPerson is the one place that holds such a tuple to
// the rule of ErrReservedForAuthor, so a writer that went around it would be a second rule.
var ErrPersonTupleOutsideToPerson = errors.New("a delivered_to, mentions or participates_in tuple naming a person is appended by ToPerson alone")

// ToPerson appends tuples that tie a thing to a person: a delivered_to or a mentions from the
// thing to the person, or a participates_in from the person to the root of the thread. It is
// the only way those three kinds reach a person's node (ErrPersonTupleOutsideToPerson).
//
// The person of each tuple is graphstore.PersonNode's canonical login (model.CanonicalLogin), so
// the three spellings a record can hold of one person meet at one node. Each person's node is
// written beside the tuples, `do nothing` where the store holds it already, so a person reached
// before they wrote anything has a node their page is read from (api's listPersonNodes): the
// node is made by the tuple's writer at its moment and under its event, and its base attributes
// with it, as a write that first records a person makes one (AuthorFacts).
//
// A thing reserved for its author reaches no person but that author: a tuple whose other end is
// a node the store holds at VisibilityAuthor is refused with ErrReservedForAuthor, naming the
// node and its visibility, unless the person is who made that node (AuthorOf of its made_by), and
// nothing of the call is appended. A node the store holds no row for is visible, as the read's own
// predicate has it (refs.VisibleKind). An end past the store's address bound is no tuple, and the
// rest of the call is appended.
//
// It runs in the caller's transaction, in two statements: the read of the nodes reserved for their
// author among the tuples' other ends, which locks nothing, and one write of the person nodes and
// the tuples, the nodes first in ascending (kind, id), as every write of this store takes them
// (WriteArms). The tuples take the relation repeat key, so one event states each relation once.
func ToPerson(ctx context.Context, tx pgx.Tx, tuples []Tuple) error {
	if len(tuples) == 0 {
		return nil
	}
	stated := make([]Tuple, 0, len(tuples))
	// reaches is, beside each stated tuple, the person it names and the node at its other end.
	reaches := make([]personReach, 0, len(tuples))
	persons := make([]Node, 0, len(tuples))
	made := make(map[string]struct{}, len(tuples))
	ends := make(map[[2]string]struct{}, len(tuples))
	for _, tuple := range tuples {
		person, end, err := personEnds(&tuple)
		if err != nil {
			return err
		}
		if !AddressFits(person) || !AddressFits(end[1]) {
			continue
		}
		stated = append(stated, tuple)
		reaches = append(reaches, personReach{person: person, end: end})
		ends[end] = struct{}{}
		if _, held := made[person]; !held {
			made[person] = struct{}{}
			persons = append(persons, PersonNode(person, tuple.Who, tuple.At, tuple.SourceEvent))
		}
	}
	if len(stated) == 0 {
		return nil
	}
	reserved, err := reservedForAuthor(ctx, tx, ends)
	if err != nil {
		return err
	}
	for index, reach := range reaches {
		author, held := reserved[reach.end]
		if !held {
			continue
		}
		if kind, id, named := AuthorOf(author); named && kind == KindPerson && id == reach.person {
			continue
		}
		tuple := stated[index]
		return fmt.Errorf("%s %s/%s -> %s/%s names person %s, and %s/%s is visible to its author alone (visibility %s): %w",
			tuple.Kind, tuple.FromKind, tuple.FromID, tuple.ToKind, tuple.ToID, reach.person, reach.end[0], reach.end[1],
			VisibilityAuthor, ErrReservedForAuthor)
	}
	arms, params, err := writeArms(1, persons, stated, nil, true)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "with "+arms+"\nselect 1", params...); err != nil {
		return fmt.Errorf("append %d tuple naming a person: %w", len(stated), err)
	}
	return nil
}

// personReach is what one tuple ToPerson appends ties: the person, by their canonical login, and
// the node at the tuple's other end, whose visibility decides whether the person may be reached.
type personReach struct {
	person string
	end    [2]string
}

// personEnds reads one tuple ToPerson is handed: the person it names, canonicalised on the tuple
// itself, and the node at its other end, which is the thing a delivered_to or a mentions points
// from and the thread root a participates_in points at. A tuple of another kind, or of one of the
// three with no person at the end its kind puts one, is no tuple of ToPerson's.
func personEnds(tuple *Tuple) (string, [2]string, error) {
	switch tuple.Kind {
	case TupleDeliveredTo, TupleMentions:
		if tuple.ToKind == KindPerson {
			tuple.ToID = model.CanonicalLogin(tuple.ToID)
			return tuple.ToID, [2]string{tuple.FromKind, tuple.FromID}, nil
		}
	case TupleParticipatesIn:
		if tuple.FromKind == KindPerson {
			tuple.FromID = model.CanonicalLogin(tuple.FromID)
			return tuple.FromID, [2]string{tuple.ToKind, tuple.ToID}, nil
		}
	}
	return "", [2]string{}, fmt.Errorf("ToPerson appends %s to a person and %s from one, not %s %s/%s -> %s/%s",
		TupleDeliveredTo+" and "+TupleMentions, TupleParticipatesIn, tuple.Kind, tuple.FromKind, tuple.FromID, tuple.ToKind, tuple.ToID)
}

// reservedForAuthor answers, of the nodes ends names, each the store holds at VisibilityAuthor,
// with who made it, in one read of graph_nodes by its key.
func reservedForAuthor(ctx context.Context, tx pgx.Tx, ends map[[2]string]struct{}) (map[[2]string]model.Actor, error) {
	kinds := make([]string, 0, len(ends))
	ids := make([]string, 0, len(ends))
	for end := range ends {
		kinds, ids = append(kinds, end[0]), append(ids, end[1])
	}
	rows, err := tx.Query(ctx, `
		select n.kind, n.id, n.made_by
		from unnest($1::text[], $2::text[]) as named(kind, id)
		join graph_nodes n on n.kind = named.kind and n.id = named.id
		where n.visibility = $3
	`, kinds, ids, VisibilityAuthor)
	if err != nil {
		return nil, fmt.Errorf("read which of %d node is reserved for its author: %w", len(ends), err)
	}
	defer rows.Close()
	reserved := map[[2]string]model.Actor{}
	for rows.Next() {
		var kind, id string
		var madeBy []byte
		if err := rows.Scan(&kind, &id, &madeBy); err != nil {
			return nil, fmt.Errorf("read a node reserved for its author: %w", err)
		}
		var author model.Actor
		if err := json.Unmarshal(madeBy, &author); err != nil {
			return nil, fmt.Errorf("decode the author of %s/%s: %w", kind, id, err)
		}
		reserved[[2]string{kind, id}] = author
	}
	return reserved, rows.Err()
}

// namesPerson is whether tuple is one of the three kinds ToPerson alone appends, naming a person
// at the end its kind puts one.
func namesPerson(tuple Tuple) bool {
	switch tuple.Kind {
	case TupleDeliveredTo, TupleMentions:
		return tuple.ToKind == KindPerson
	case TupleParticipatesIn:
		return tuple.FromKind == KindPerson
	}
	return false
}

// refusePersonTuples is the check every writer of this store but ToPerson makes of the tuples and
// the claims it is handed (ErrPersonTupleOutsideToPerson).
func refusePersonTuples(tuples []Tuple, claims []Claim) error {
	for _, tuple := range tuples {
		if namesPerson(tuple) {
			return fmt.Errorf("%s %s/%s -> %s/%s: %w", tuple.Kind, tuple.FromKind, tuple.FromID, tuple.ToKind, tuple.ToID, ErrPersonTupleOutsideToPerson)
		}
	}
	for _, claim := range claims {
		if namesPerson(Tuple{Kind: claim.Kind, FromKind: claim.FromKind, ToKind: claim.ToKind}) {
			return fmt.Errorf("claim %s %s/%s -> %s/%s: %w", claim.Kind, claim.FromKind, claim.FromID, claim.ToKind, claim.ToID, ErrPersonTupleOutsideToPerson)
		}
	}
	return nil
}
