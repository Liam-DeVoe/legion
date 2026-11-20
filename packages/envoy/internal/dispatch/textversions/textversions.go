// Package textversions fills search_text_versions, the keyword table of each past version of the
// searchable text of Dispatch's issues, comments, issue messages and asks (migration
// 20261120021308), from the event log, so a keyword search can be answered as that text stood at
// a past time (store.TextVersions).
//
// A version starts at an event that changes an item's text. The rules say which events those are
// and which version each one starts: versionRules holds the rule of each event type that can
// start one, and unchangingTypes names every other type with the reason it changes no searchable
// text, so that between them they sort every type the log can hold
// (TestEveryEventTypeIsSortedForTheKeywordFill, over contracts.DispatchEventTypes and the types
// of production's log). The first event of each payload shape production's log holds is a
// fixture under testdata/payloads, which TestEveryProductionPayloadShapeGivesItsVersion decodes
// through the rules.
package textversions

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// event is one event of the log as the rules read it: the columns of its row they need, its
// payload as the event wrote it, of an ask's event the ask's body, which the fill's read of the
// log makes in SQL, and the bytes that read counted of it against the batch's bound.
type event struct {
	ID        int64
	Type      string
	IssueKey  *string // the issue the event belongs to; nil for an event of no issue
	Project   string  // the key of its project; "" for an event of no project
	Seq       int32   // its seq in its owner's order
	CreatedAt time.Time
	Payload   json.RawMessage
	// AskBody is the body of an ask's event as readEventsQuery reads it (askBodySQL): the
	// question, a space and ask_search_suffix of the options and the answer, made by the SQL
	// function the ask's own search column reads. It is nil for an event of any other type, and
	// for an ask's event whose payload holds no question.
	AskBody *string
	// PayloadBytes and RowBodyBytes are what readEventsQuery counted of the event against the
	// batch's bound: the bytes of its payload, for a type a rule reads, and the bytes of its
	// present row's body, for a post whose event cut its body; 0 otherwise. decideVersions adds
	// what the version the event starts reads besides.
	PayloadBytes, RowBodyBytes int64
}

// version is one row of search_text_versions as the rules decide it, before the batch's insert
// makes its vector (insertVersions).
type version struct {
	Kind       string  // kindIssue, kindComment, kindMessage or kindAsk
	ID         string  // the issue's key, or the comment's, message's or ask's uuid
	IssueKey   *string // nil for a comment or an ask on a project document
	ProjectKey string
	EventID    int64
	EventSeq   int32
	StartedAt  time.Time
	// Text is the item's own text the version holds: an issue's title, a comment's or a
	// message's body as its event carries it, or an ask's question. It is nil where neither the
	// event nor an earlier version of the item gives it, as for an artifact.version of an issue
	// with no version yet. A version read from the table holds its row's body as its Text
	// (latestVersions), which for an ask is its whole body, not its question alone.
	Text *string
	// Body is the text the row's body column holds, which the rules compare with the item's
	// latest version's to tell whether an event changed it, and which the insert writes: an
	// issue's title, a comment's or a message's whole body (decideVersions reads it from another
	// source where the event cut it), and an ask's body as readEventsQuery made it in SQL
	// (event.AskBody), its question, a space and ask_search_suffix of its options and answer.
	Body string
	// BodyFrom is where Body came from: bodyFromEvent, bodyFromRow, bodyFromBlocks or
	// bodyFromCut, the values of the row's body_from.
	BodyFrom string
	// BodyTruncated is whether the event cut the post's body, which is then read whole from
	// another source.
	BodyTruncated bool
	// SpecArtifactID and SpecVersion are an issue's primary document and the version of it that
	// the issue's text held: nil on the rows of the other kinds, and where no event has named
	// them yet.
	SpecArtifactID *string
	SpecVersion    *int32
}

// latestVersion answers an item's latest version, in the table or earlier in the batch, or nil
// for an item that has none yet.
type latestVersion func(kind, id string) *version

// noLatest is the latestVersion of a batch that knows no version of any item: it answers nil.
func noLatest(string, string) *version { return nil }

// rule decides the version an event of its type starts, from its row, its payload and the item's
// latest version. started is false for an event that changes none of the item's text, such as an
// issue.updated that keeps the title.
type rule func(e event, latest latestVersion) (v version, started bool, err error)

// errNoRule is versionOf's answer for an event of a type versionRules holds no rule for.
var errNoRule = errors.New("no keyword version rule reads this event type")

// versionOf decodes e through the rule of its type as the first event of its item, which has no
// version before it (noLatest): the version e starts, or started false where it starts none.
func versionOf(e event) (v version, started bool, err error) {
	decide, ok := versionRules[e.Type]
	if !ok {
		return version{}, false, fmt.Errorf("%w: %s", errNoRule, e.Type)
	}
	return decide(e, noLatest)
}
