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

// event is one event of the log as the rules read it: the columns of its row they need, and its
// payload as the event wrote it.
type event struct {
	ID        int64
	Type      string
	IssueKey  *string // the issue the event belongs to; nil for an event of no issue
	Project   string  // the key of its project; "" for an event of no project
	Seq       int32   // its seq in its owner's order
	CreatedAt time.Time
	Payload   json.RawMessage
}

// version is one row of search_text_versions as the rules decide it, before the batch's insert
// makes its vector (insertVersions).
type version struct {
	Kind       string  // "issue", "comment", "message" or "ask"
	ID         string  // the issue's key, or the comment's, message's or ask's uuid
	IssueKey   *string // nil for a comment or an ask on a project document
	ProjectKey string
	EventID    int64
	EventSeq   int32
	StartedAt  time.Time
	// Text is the item's own text the version holds: an issue's title, a comment's or a
	// message's body as its event carries it, or an ask's question. It is nil where neither the
	// event nor an earlier version of the item gives it, as for an artifact.version of an issue
	// with no version yet, and on an ask's version read from the table, whose row keeps its
	// question only inside its body.
	Text *string
	// Body is the text the row's body column holds, which the rules compare with the item's
	// latest version's to tell whether an event changed it: an issue's title, a comment's or a
	// message's whole body (decideVersions reads it from another source where the event cut
	// it), and an ask's question, a space and askSearchSuffix of its options and answer. The
	// insert makes an ask's stored body in SQL, through ask_search_suffix, from Text, Options
	// and Answer.
	Body string
	// BodyFrom is where Body came from: bodyFromEvent, bodyFromRow, bodyFromBlocks or
	// bodyFromCut, the values of the row's body_from.
	BodyFrom string
	// BodyTruncated is whether the event cut the post's body, which is then read whole from
	// another source.
	BodyTruncated bool
	// Options and Answer are an ask's, as its event carries them, which the ask's body adds to
	// its question through ask_search_suffix.
	Options, Answer json.RawMessage
	// SpecArtifactID and SpecVersion are an issue's primary document and the version of it that
	// the issue's text held: nil on the rows of the other kinds, and where no event has named
	// them yet.
	SpecArtifactID *string
	SpecVersion    *int32
	// SpecSize is the size in bytes of the primary document version an artifact.version names
	// (its payload's version.size), the markdown the row's vector is made from, which a batch
	// counts; 0 on every other row.
	SpecSize int64
}

// latestVersion answers an item's latest version, in the table or earlier in the batch, or nil
// for an item that has none yet.
type latestVersion func(kind, id string) *version

// rule decides the version an event of its type starts, from its row, its payload and the item's
// latest version. started is false for an event that changes none of the item's text, such as an
// issue.updated that keeps the title.
type rule func(e event, latest latestVersion) (v version, started bool, err error)

// errNoRule is versionOf's answer for an event of a type versionRules holds no rule for.
var errNoRule = errors.New("no keyword version rule reads this event type")

// versionOf decodes e through the rule of its type as the first event of its item, which has no
// version before it: the version e starts, or started false where it starts none.
func versionOf(e event) (v version, started bool, err error) {
	decide, ok := versionRules[e.Type]
	if !ok {
		return version{}, false, fmt.Errorf("%w: %s", errNoRule, e.Type)
	}
	return decide(e, func(string, string) *version { return nil })
}
