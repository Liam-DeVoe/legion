package graphstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// ProvesAuthor reports whether a credential proves an author: a session or person id, the person
// a personal token belongs to, or the workload a service-account token proved. A credential that
// names none of them - the deployment's shared agent bearer, which resolves as {} - proves
// nobody: every holder of it presents the same value, so model.Actor.SameCredential reports two
// such credentials as one, and a node reserved for the author of such a write would be readable
// by every holder of the bearer rather than by its writer.
//
// This is the one rule, and every reader and every writer asks it rather than keeping a copy:
// the going routes (api.createGoingRequest, api.streamGoingAnswer), the visibility a write may
// ask for (VisibilityFor) and the SQL every author-private read binds (AuthorIsSQL, which states
// the same test over a stored author).
func ProvesAuthor(actor model.Actor) bool {
	return actor.ID != "" || actor.Owner != nil || actor.Service != nil
}

// ErrAuthorNamesNobody refuses a node reserved for its author written on a credential that
// proves none. It is a sentinel so a route can answer it as a refusal of the caller's rather
// than as a failure of the server's, and its words are the reason itself: they say what is
// wrong with the credential and what would be wrong with the node.
var ErrAuthorNamesNobody = errors.New(
	"this credential names nobody, so a node reserved for its author would be nobody's to read: " +
		"every holder of the deployment's shared bearer presents the same value, and two such " +
		"credentials compare as one credential, so write it project-visible or write it under a " +
		"credential that names a session, a person or a workload")

// VisibilityFor is the visibility a write gets for what it asked for. A write that asks for
// nothing is project-visible, which is what every node Dispatch writes today is; a write that
// asks to reserve a node for its author gets that only on a credential that proves one, and is
// refused with ErrAuthorNamesNobody otherwise. A word that is neither is refused by name with the
// InvalidAttributeValueError every write of this store refuses it with, so a misspelling is a
// failed write rather than a node nobody can read.
func VisibilityFor(requested string, actor model.Actor) (string, error) {
	switch requested {
	case "", VisibilityProject:
		return VisibilityProject, nil
	case VisibilityAuthor:
		if !ProvesAuthor(actor) {
			return "", ErrAuthorNamesNobody
		}
		return VisibilityAuthor, nil
	}
	return "", &InvalidAttributeValueError{Attribute: AttributeVisibility, Value: requested}
}

// authorCredential is the part of an actor two credentials are compared by, in the shape a
// stored `made_by` holds it: exactly the fields model.Actor.SameCredential compares, and no
// field two requests of one credential differ in. Owner and Service are pointers so a
// credential that names none is a JSON null here and a missing key there, which compare equal
// as jsonb, where an empty string would compare equal to neither.
//
// Person rides beside those four and is never compared with them: it is the person the
// credential proves (provedPerson), which the kinds that compare the person read
// (AuthorPersonIsSQL), and AuthorIsSQL takes it off the argument before it compares the four.
// A credential that proves no person leaves the key out.
type authorCredential struct {
	Kind    string  `json:"kind"`
	ID      string  `json:"id"`
	Owner   *string `json:"owner"`
	Service *string `json:"service"`
	Person  string  `json:"person,omitempty"`
}

// ActingCredential is the one value an author-private read binds beside its own arguments: the
// acting credential as AuthorIsSQL compares it, and beside it the person that credential proves,
// as AuthorPersonIsSQL compares that. A credential that proves no author binds nil, which is SQL
// null, which both comparisons answer false for - so a reader the store cannot name reads no
// author-private node at all, rather than every node written on the shared bearer.
func ActingCredential(actor model.Actor) any {
	if !ProvesAuthor(actor) {
		return nil
	}
	encoded, err := json.Marshal(authorCredential{
		Kind: actor.Kind, ID: actor.ID, Owner: actor.Owner, Service: actor.Service,
		Person: provedPerson(actor),
	})
	if err != nil {
		// Four strings and two string pointers: json.Marshal has nothing here to fail on.
		return nil
	}
	return encoded
}

// provedPerson is the person a credential proves, by canonical login (model.CanonicalLogin): a
// cookie's user, whose actor is the user's own login, or the owner a token names - a personal
// token's, or the person a broker-issued token bound its session to. Any other credential proves
// none, and answers the empty string. It is authorPersonSQL's reading of a stored author, stated
// of the acting one.
func provedPerson(actor model.Actor) string {
	switch {
	case actor.Kind == "user" && actor.ID != "":
		return model.CanonicalLogin(actor.ID)
	case actor.Owner != nil && *actor.Owner != "":
		return model.CanonicalLogin(*actor.Owner)
	}
	return ""
}

// AuthorIsSQL is the SQL half of the one rule: true when the author a node's `made_by` column
// records is somebody and is the credential bound at param (ActingCredential). It states in SQL
// exactly what ProvesAuthor states of a credential and model.Actor.SameCredential states of two,
// so a read and a route cannot answer differently. It compares the credential's four fields and
// nothing else: the argument also carries the person the credential proves, which is taken off
// it first, since the stored author this compares it with is those four fields alone.
//
// The whole test is wrapped in coalesce so it is false rather than unknown for an acting
// credential that binds null: a reader that proves no author reads no author-private node.
func AuthorIsSQL(column, param string) string {
	return `coalesce((` + authorIsSomebodySQL(column) + `) and ` + authorNameSQL(column) + ` = ` + actingCredentialSQL(param) + `, false)`
}

// AuthorPersonIsSQL is the comparison of a kind whose predicate compares the person a credential
// proves rather than the credential (refs' registry states which a kind compares): true when the
// author a node's `made_by` column records is somebody and, where that author names a person -
// a user's login, or the owner a token named (authorPersonSQL) - that person is the person the
// credential bound at param proves; where it names none, the whole credential, as AuthorIsSQL
// compares it. So a person's personal token reads what that person's cookie wrote, and a node a
// workload or a session no person owns wrote is read by that credential alone.
//
// It is false for a reader that proves no author, as AuthorIsSQL is, and false for a reader that
// proves no person against a node whose author names one: such a reader's credential names no
// login and no owner, so it is never the credential that wrote the node either.
func AuthorPersonIsSQL(column, param string) string {
	person := authorPersonSQL(column)
	return `coalesce((` + authorIsSomebodySQL(column) + `) and case when ` + person + ` is null then ` +
		authorNameSQL(column) + ` = ` + actingCredentialSQL(param) + ` else ` + person + ` = (` + param + `::jsonb ->> 'person') end, false)`
}

// actingCredentialSQL is the acting credential bound at param as authorNameSQL builds a stored
// one: its four fields, with the person ActingCredential sets beside them taken off.
func actingCredentialSQL(param string) string {
	return `(` + param + `::jsonb - 'person'::text)`
}

// authorIsSomebodySQL is ProvesAuthor over a stored author: the id a record holds, the person a
// personal token belongs to, or the workload a service-account token proved. An actor with no
// owner leaves the key out (model.Actor's omitempty), so the key's presence is the pointer's.
func authorIsSomebodySQL(column string) string {
	return `coalesce(` + column + `->>'id', '') <> '' or jsonb_exists(` + column + `, 'owner') or jsonb_exists(` + column + `, 'service')`
}

// authorNameSQL is a stored author as ActingCredential encodes the acting one: the kind and the
// id as text, empty where the record left them out, and the owner and the workload as the JSON
// they are, so a credential that names none compares equal to a null and not to an empty name.
func authorNameSQL(column string) string {
	return `jsonb_build_object(` +
		`'kind', coalesce(` + column + `->>'kind', ''), ` +
		`'id', coalesce(` + column + `->>'id', ''), ` +
		`'owner', ` + column + `->'owner', ` +
		`'service', ` + column + `->'service')`
}

// authorPersonSQL is provedPerson over a stored author: the canonical login of a user, or of the
// owner the record names, and null for an author that names neither. lower(btrim()) is
// model.CanonicalLogin, as graphcensus's authorParty reads it.
func authorPersonSQL(column string) string {
	return `(case when ` + column + `->>'kind' = 'user' and coalesce(` + column + `->>'id', '') <> '' ` +
		`then lower(btrim(` + column + `->>'id')) ` +
		`else nullif(lower(btrim(` + column + `->>'owner')), '') end)`
}

// ErrVisibilityNotAdmitted refuses a node reserved for its author whose kind takes no such node.
// A kind takes one only where every read of it binds the visibility predicate, which the read
// registry states of each kind it flags and hands the store at start (AdmitAuthorOnly): a node of
// any other kind reserved for its author would be answered to everybody by whichever read of it
// binds none. It is a sentinel so a route can answer it as a refusal of the caller's, 400
// VISIBILITY_NOT_ADMITTED; what a write is refused with is a VisibilityNotAdmittedError, which
// wraps it and names the kind.
var ErrVisibilityNotAdmitted = errors.New("a node of this kind is never reserved for its author")

// VisibilityNotAdmittedError is ErrVisibilityNotAdmitted for one kind: the kind of the node a
// write reserved for its author, as the graph addresses it (AddressKind).
type VisibilityNotAdmittedError struct{ Kind string }

func (e *VisibilityNotAdmittedError) Error() string {
	return fmt.Sprintf("the node kind %s takes no node reserved for its author, because not every read of %s binds "+
		"the visibility predicate: write it project-visible", e.Kind, WithArticle(e.Kind))
}

func (e *VisibilityNotAdmittedError) Unwrap() error { return ErrVisibilityNotAdmitted }

// authorOnlyKinds is every node kind the store takes a node reserved for its author of, by the
// kind the graph addresses it by (AddressKind). It is filled once at start, by the read
// registry's init (AdmitAuthorOnly), before any write runs, and only read after that, so it takes
// no lock. A program that never loads the registry holds none, and its store takes no
// author-only node at all.
var authorOnlyKinds = map[string]struct{}{}

// AdmitAuthorOnly is the hand-off of the kinds that may hold a node reserved for its author: the
// read registry's init (refs) hands it, once, every kind its registry flags as one whose every
// read binds the predicate. It runs at init and at no other time. A kind handed twice stops the
// program, naming the kind: each is handed by the one registry entry that flags it, so a second
// hand-off is a second place that thinks it decides the kind, and the two can drift.
func AdmitAuthorOnly(kinds ...string) {
	for _, kind := range kinds {
		if _, admitted := authorOnlyKinds[kind]; admitted {
			panic("graphstore: the node kind " + kind + " is admitted as one that takes a node reserved for its author twice: " +
				"the read registry's flag is the one place that admits it")
		}
		authorOnlyKinds[kind] = struct{}{}
	}
}

// AuthorOnlyKinds is every kind the store takes a node reserved for its author of, sorted.
func AuthorOnlyKinds() []string {
	return slices.Sorted(maps.Keys(authorOnlyKinds))
}

// CheckVisibilityAdmitted is the rule every node write of this store holds a node to before its
// first statement (checkBaseValues): a node of kind and id at visibility author is refused with a
// VisibilityNotAdmittedError naming the kind the graph addresses it by (AddressKind), unless the
// read registry admitted that kind (AdmitAuthorOnly). Any other visibility passes, the values the
// attribute takes being CheckAttributeValue's to hold. A writer that keeps a thing out of the
// store and so reaches no node write - a comment past the address bound (api's commentStored) -
// asks it itself, so the thing is refused as its node would have been.
func CheckVisibilityAdmitted(kind, id, visibility string) error {
	if visibility != VisibilityAuthor {
		return nil
	}
	addressed := AddressKind(kind, id)
	if _, admitted := authorOnlyKinds[addressed]; admitted {
		return nil
	}
	return &VisibilityNotAdmittedError{Kind: addressed}
}
