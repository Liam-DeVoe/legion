package graphstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// ProvesAuthor reports whether a credential proves an author: the person a cookie, a personal
// token or a session the broker verified for its owner proves, the workload a service-account
// token proved, or a session that names no owner (provedAuthor). A credential that names none of
// them - the deployment's shared agent bearer, which resolves as {} - proves nobody: every holder
// of it presents the same value, so model.Actor.SameCredential reports two such credentials as
// one, and a node reserved for the author of such a write would be readable by every holder of
// the bearer rather than by its writer.
//
// This is the one rule, and every reader and every writer asks it rather than keeping a copy:
// the going routes (api.createGoingRequest, api.streamGoingAnswer), the visibility a write may
// ask for (VisibilityFor) and the SQL every author-private read binds (AuthorIsSQL, which states
// the same test over a stored author).
func ProvesAuthor(actor model.Actor) bool {
	_, proved := provedAuthor(actor)
	return proved
}

// author is who a credential proved, the one thing an author-private node is read by: the root's
// R19 makes an author the person a credential proved, so two credentials of one person - her
// cookie, her personal token, a session the broker verified as hers - are one author, and a node
// any of them wrote is hers under each of them. Kind is "person", with the canonical login as the
// id; the actor's own kind, for a credential that proves its own id and no person, which is a
// session nobody owns; or "service", with the workload a service-account token proved.
//
// Only a person joins credentials. A session that names a workload and no person - a session the
// broker verified for a service, the shared public-session account's among them - is its own
// author, by its own id, because every session of that workload carries the same workload: an
// author by the workload would let each of them read what any other reserved, which is the shared
// bearer's fault again.
type author struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Author kinds that are no actor kind.
const (
	authorPerson  = "person"
	authorService = "service"
)

// provedAuthor is the author a credential proved, in the order authorSQL reads a stored one: the
// owner a session or a personal token carries, then a person at a browser, then the credential's
// own id, then a workload, which only a credential with no id of its own is authored by. proved is
// false for a credential that names none of them.
func provedAuthor(actor model.Actor) (author, bool) {
	switch {
	case actor.Owner != nil && *actor.Owner != "":
		return author{Kind: authorPerson, ID: model.CanonicalLogin(*actor.Owner)}, true
	case actor.Kind == "user" && actor.ID != "":
		return author{Kind: authorPerson, ID: model.CanonicalLogin(actor.ID)}, true
	case actor.ID != "":
		return author{Kind: actor.Kind, ID: actor.ID}, true
	case actor.Service != nil && *actor.Service != "":
		return author{Kind: authorService, ID: *actor.Service}, true
	}
	return author{}, false
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
// Person and Author ride beside those four and are never compared with them. Person is the
// person the credential proves (provedPerson), which the kinds that compare the person read
// (AuthorPersonIsSQL); a credential that proves no person leaves the key out. Author is the
// author the credential proved (provedAuthor), which the one rule compares (AuthorIsSQL).
// AuthorCredentialIsSQL takes both off the argument before it compares the four.
type authorCredential struct {
	Kind    string  `json:"kind"`
	ID      string  `json:"id"`
	Owner   *string `json:"owner"`
	Service *string `json:"service"`
	Person  string  `json:"person,omitempty"`
	Author  author  `json:"author"`
}

// ActingCredential is the one value an author-private read binds beside its own arguments: the
// acting credential as AuthorCredentialIsSQL compares it, and beside it the person that
// credential proves, as AuthorPersonIsSQL compares that, and the author it proved
// (provedAuthor), as AuthorIsSQL compares that. A credential that proves no author binds nil,
// which is SQL null, which every comparison answers false for - so a reader the store cannot
// name reads no author-private node at all, rather than every node written on the shared bearer.
func ActingCredential(actor model.Actor) any {
	proved, ok := provedAuthor(actor)
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(authorCredential{
		Kind: actor.Kind, ID: actor.ID, Owner: actor.Owner, Service: actor.Service,
		Person: provedPerson(actor), Author: proved,
	})
	if err != nil {
		// Strings and string pointers: json.Marshal has nothing here to fail on.
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
// records proved somebody, and that somebody is the author bound at param (ActingCredential). It
// states in SQL exactly what provedAuthor states of a credential, so a read and a route cannot
// answer differently. A login is compared in the form user_threads_login gives it on both sides,
// the stored one and the bound one, so the two are canonical by one function. It is the
// comparison of every kind dispatch://INFRA-1149 registers.
//
// The whole test is wrapped in coalesce so it is false rather than unknown for an acting
// credential that binds null, and for a stored author that names nobody: a reader that proves no
// author reads no author-private node, and a node no author wrote is nobody's.
func AuthorIsSQL(column, param string) string {
	bound := `(` + param + `::jsonb -> 'author')`
	return `coalesce(` + authorSQL(column) + ` = ` + authorKeySQL(bound+`->>'kind'`, bound+`->>'id'`) + `, false)`
}

// AuthorCredentialIsSQL is the comparison of a kind whose predicate compares the whole acting
// credential rather than the author it proved (refs' registry states which a kind compares): true
// when the author a node's `made_by` column records proved somebody (authorIsSomebodySQL) and is
// the credential bound at param. It states in SQL what model.Actor.SameCredential states of two
// credentials, so a run is read by the credential that asked it and by no other, as the going
// stream reads it. It compares the credential's four fields and nothing else: the argument also
// carries the person the credential proves and the author it proved, which are taken off it
// first, since the stored author this compares it with is those four fields alone.
//
// It is wrapped in coalesce as AuthorIsSQL is, and for the same reason.
func AuthorCredentialIsSQL(column, param string) string {
	return `coalesce((` + authorIsSomebodySQL(column) + `) and ` + authorNameSQL(column) + ` = ` + actingCredentialSQL(param) + `, false)`
}

// AuthorPersonIsSQL is the comparison of a kind whose predicate compares the person a credential
// proves rather than the credential (refs' registry states which a kind compares): true when the
// author a node's `made_by` column records is somebody and, where that author names a person -
// a user's login, or the owner a token named (authorPersonSQL) - that person is the person the
// credential bound at param proves; where it names none, the whole credential, as
// AuthorCredentialIsSQL compares it. So a person's personal token reads what that person's cookie
// wrote, and a node a workload or a session no person owns wrote is read by that credential alone.
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
// one: its four fields, with the person and the author ActingCredential sets beside them taken
// off.
func actingCredentialSQL(param string) string {
	return `(` + param + `::jsonb - 'person'::text - 'author'::text)`
}

// authorIsSomebodySQL is ProvesAuthor over a stored author: the one rule finds an author in it
// (authorSQL is not null), so a stored author proves somebody exactly where a credential does.
func authorIsSomebodySQL(column string) string {
	return `(` + authorSQL(column) + `) is not null`
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

// authorSQL is provedAuthor over a stored author, case for case and in the same order, and null
// for a stored author that names none. An actor with no owner or no workload leaves the key out
// (model.Actor's omitempty), which reads as null here as an empty name does.
func authorSQL(column string) string {
	return `case
		when coalesce(` + column + `->>'owner', '') <> ''
			then ` + authorKeySQL(`'`+authorPerson+`'`, column+`->>'owner'`) + `
		when ` + column + `->>'kind' = 'user' and coalesce(` + column + `->>'id', '') <> ''
			then ` + authorKeySQL(`'`+authorPerson+`'`, column+`->>'id'`) + `
		when coalesce(` + column + `->>'id', '') <> ''
			then ` + authorKeySQL(`coalesce(`+column+`->>'kind', '')`, column+`->>'id'`) + `
		when coalesce(` + column + `->>'service', '') <> ''
			then ` + authorKeySQL(`'`+authorService+`'`, column+`->>'service'`) + `
	end`
}

// authorKeySQL is one author as the rule compares two: its kind, and its id, a person's login put
// through user_threads_login, the form every SQL read of this tree compares a login in.
func authorKeySQL(kind, id string) string {
	return `jsonb_build_object('kind', ` + kind + `, 'id', case when ` + kind + ` = '` + authorPerson +
		`' then user_threads_login(` + id + `) else ` + id + ` end)`
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
