package textversions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// rulesVersion is the version of the event rules versionRules holds, which the fill records in
// its cursor row (search_text_versions_fill.rules) beside the cursor the rules wrote up to, so a
// later build that reads more event types knows which events to read again.
const rulesVersion = 1

// Where a version's body came from, the values of search_text_versions.body_from this build
// writes: the event's payload, whole; the item's present row, which shows no later change; and
// revision 1 of a comment's blocks; or the event's own text, cut short, where no source holds the
// whole body. 'history', a post's body history, is a later build's.
const (
	bodyFromEvent  = "event"
	bodyFromRow    = "row"
	bodyFromBlocks = "blocks"
	bodyFromCut    = "cut"
)

// versionRules holds the rule of each event type that can start a version (rules version 1).
// Every other type is in unchangingTypes: TestEveryEventTypeIsSortedForTheKeywordFill holds each
// member of contracts.DispatchEventTypes in exactly one of the two.
//
// An issue's version holds its title and the version of its primary document its vector reads:
// issue.created starts the first, with version 1 of the primary document, which the create
// writes in its own transaction and which never gets an event of its own; issue.updated and
// issue.closed (a close can retitle in the same write) start one when the payload's title
// differs from the title of the issue's latest version, since older events carry no `changed`;
// and artifact.version starts one for the issue's primary document alone, which no other
// document's version changes, carrying the title forward. A primary document never gets
// artifact.created.
//
// A comment's version starts at comment.created and comment.answered (a reply is a new comment)
// and at a comment.edited whose body differs; an issue message's at message.created and
// message.answered (a reply is a new message), and a message of no issue starts none, since the
// keyword table leaves direct messages out. An ask's starts at ask.opened (a reopen is one too),
// ask.edited and ask.answered (a restored answer included) when its question, options or answer,
// as ask_search_suffix reads them, differ from its latest version's.
var versionRules = map[string]rule{
	"issue.created":    issueCreated,
	"issue.updated":    issueRetitled,
	"issue.closed":     issueRetitled,
	"artifact.version": primaryDocumentVersion,
	"comment.created":  postVersion("comment", false),
	"comment.answered": postVersion("comment", false),
	"comment.edited":   postVersion("comment", true),
	"message.created":  postVersion("message", false),
	"message.answered": postVersion("message", false),
	"ask.opened":       askVersion,
	"ask.edited":       askVersion,
	"ask.answered":     askVersion,
}

// unchangingTypes names each event type that changes no searchable text, with the reason.
var unchangingTypes = map[string]string{
	"issue.claimed":                             "an issue's claim is its state, not its text",
	"issue.released":                            "an issue's claim is its state, not its text",
	"issue.deployed":                            "an issue's deploy is its state, not its text",
	"issue.pull_request_attribution_changed":    "an issue's pull request is its state, not its text",
	"child.added":                               "a child issue is its parent's state, not its text",
	"child.removed":                             "a child issue is its parent's state, not its text",
	"child.status":                              "a child's status is its parent's state, not its text",
	"artifact.created":                          "no primary document is created by it: an issue's spec rides issue.created",
	"artifact.renamed":                          "an issue's primary document is refused a rename (ARTIFACT_PRIMARY), and a name is no text the search reads",
	"artifact.removed":                          "it changes no issue's title or primary document version",
	"artifact.human_edited":                     "it changes no issue's title or primary document version",
	"artifact.approved":                         "it changes no issue's title or primary document version",
	"artifact.changes_requested":                "it changes no issue's title or primary document version",
	"ask.resolved":                              "it leaves the ask's question, options and answer alone",
	"ask.handed_back":                           "it leaves the ask's question, options and answer alone: a reword before a hand-back is its own ask.edited",
	"ask.asked_of":                              "it leaves the ask's question, options and answer alone",
	"ask.anchor_refreshed":                      "it leaves the ask's question, options and answer alone",
	"ask.follower_added":                        "it leaves the ask's question, options and answer alone",
	"ask.follower_removed":                      "it leaves the ask's question, options and answer alone",
	"comment.resolved":                          "the comment's body does not change",
	"comment.reopened":                          "the comment's body does not change",
	"comment.anchor_refreshed":                  "the comment's body does not change",
	"comment.delivery":                          "the comment's body does not change",
	"suggestion.accepted":                       "the comment's body does not change: the suggestion changes the document through its own artifact.version",
	"suggestion.rejected":                       "the comment's body does not change",
	"message.delivery":                          "a message's delivery carries no text",
	"message.accepted":                          "a message's acceptance carries no text",
	"block.repaired":                            "the document's own version carries the change",
	"block.invalid":                             "the document's own version carries the change",
	"result.recorded":                           "a result is not an issue, comment, message or ask",
	"result.retracted":                          "a result is not an issue, comment, message or ask",
	"project.created":                           "a project is not an issue, comment, message or ask",
	"project.updated":                           "a project is not an issue, comment, message or ask",
	"settings.architecture_source.updated":      "a setting is not an issue, comment, message or ask",
	"settings.project_default_assignee.updated": "a setting is not an issue, comment, message or ask",
	"settings.repo_project.updated":             "a setting is not an issue, comment, message or ask",
	"architecture.synced":                       "an architecture sync is not an issue, comment, message or ask",
	"architecture.sync_failed":                  "an architecture sync is not an issue, comment, message or ask",
	"user_state.updated":                        "a person's state is not an issue, comment, message or ask",
	"user_agent_state.updated":                  "a person's state is not an issue, comment, message or ask",
	"user_thread_state.updated":                 "a person's state is not an issue, comment, message or ask",
	"subscription.remove_requested":             "a subscription is not an issue, comment, message or ask",
	"subscription.removed":                      "a subscription is not an issue, comment, message or ask",
	"handoff.recorded":                          "a handoff is not an issue, comment, message or ask",
	"decision.kept":                             "a kept decision is not an issue, comment, message or ask",
	"priority.updated":                          "a priority is not an issue, comment, message or ask",
}

// errUnreadablePayload is a rule's answer for a payload that lacks a field the rule reads, which
// stops the fill at that event rather than letting it miss a version.
var errUnreadablePayload = errors.New("the keyword rules cannot read this event's payload")

// issuePayload is what the rules read of an issue.* event's payload, the issue as the write left
// it.
type issuePayload struct {
	Key               *string `json:"key"`
	Title             *string `json:"title"`
	PrimaryArtifactID *string `json:"primary_artifact_id"`
}

// artifactVersionPayload is what the rules read of an artifact.version event's payload: the
// document and the version the write made of it, with its size in bytes.
type artifactVersionPayload struct {
	ArtifactID *string `json:"artifact_id"`
	Version    *struct {
		Number *int32 `json:"number"`
		Size   int64  `json:"size"`
	} `json:"version"`
}

// postPayload is what the rules read of a comment.* or message.* event's payload, and whether
// the event cut the body (model.EventBody).
type postPayload struct {
	ID            *string `json:"id"`
	IssueKey      *string `json:"issue_key"`
	Body          *string `json:"body"`
	BodyTruncated bool    `json:"body_truncated"`
}

// askPayload is what the rules read of an ask.* event's payload: its question, and its options
// and answer as ask_search_suffix reads them.
type askPayload struct {
	ID       *string         `json:"id"`
	IssueKey *string         `json:"issue_key"`
	Question *string         `json:"question"`
	Options  json.RawMessage `json:"options"`
	Answer   json.RawMessage `json:"answer"`
}

// decodePayload decodes e's payload into payload.
func decodePayload(e event, payload any) error {
	if err := json.Unmarshal(e.Payload, payload); err != nil {
		return fmt.Errorf("%w: %v", errUnreadablePayload, err)
	}
	return nil
}

// missing is the error of a payload that lacks field.
func missing(field string) error {
	return fmt.Errorf("%w: it has no %s", errUnreadablePayload, field)
}

// newVersion is the version e starts of item (kind, id) of issueKey, with the columns every row
// takes from its event. Its project is the event's, which a version of any kind needs.
func newVersion(e event, kind, id string, issueKey *string) (version, error) {
	if e.Project == "" {
		return version{}, fmt.Errorf("%w: the event names no project", errUnreadablePayload)
	}
	return version{
		Kind:       kind,
		ID:         id,
		IssueKey:   issueKey,
		ProjectKey: e.Project,
		EventID:    e.ID,
		EventSeq:   e.Seq,
		StartedAt:  e.CreatedAt,
		BodyFrom:   bodyFromEvent,
	}, nil
}

// issueKeyOf is the key of the issue an issue.* event is of: the payload's, else the event's.
func issueKeyOf(e event, p issuePayload) (string, error) {
	switch {
	case p.Key != nil && *p.Key != "":
		return *p.Key, nil
	case e.IssueKey != nil && *e.IssueKey != "":
		return *e.IssueKey, nil
	}
	return "", missing("key")
}

// issueCreated starts an issue's first version: its title, and version 1 of its primary
// document, which the create wrote in its own transaction.
func issueCreated(e event, _ latestVersion) (version, bool, error) {
	var p issuePayload
	if err := decodePayload(e, &p); err != nil {
		return version{}, false, err
	}
	key, err := issueKeyOf(e, p)
	if err != nil {
		return version{}, false, err
	}
	if p.Title == nil {
		return version{}, false, missing("title")
	}
	v, err := newVersion(e, "issue", key, &key)
	if err != nil {
		return version{}, false, err
	}
	v.Text, v.Body = p.Title, *p.Title
	if p.PrimaryArtifactID != nil {
		first := int32(1)
		v.SpecArtifactID, v.SpecVersion = p.PrimaryArtifactID, &first
	}
	return v, true, nil
}

// issueRetitled starts a version at an issue.updated or an issue.closed whose title differs from
// the title of the issue's latest version, carrying that version's primary document version
// forward; an issue with no version yet takes the payload's primary document, and no version of
// it.
func issueRetitled(e event, latest latestVersion) (version, bool, error) {
	var p issuePayload
	if err := decodePayload(e, &p); err != nil {
		return version{}, false, err
	}
	key, err := issueKeyOf(e, p)
	if err != nil {
		return version{}, false, err
	}
	if p.Title == nil {
		return version{}, false, missing("title")
	}
	prior := latest("issue", key)
	if prior != nil && prior.Body == *p.Title {
		return version{}, false, nil
	}
	v, err := newVersion(e, "issue", key, &key)
	if err != nil {
		return version{}, false, err
	}
	v.Text, v.Body = p.Title, *p.Title
	v.SpecArtifactID = p.PrimaryArtifactID
	if prior != nil && prior.SpecArtifactID != nil {
		v.SpecArtifactID, v.SpecVersion = prior.SpecArtifactID, prior.SpecVersion
	}
	return v, true, nil
}

// primaryDocumentVersion starts a version at an artifact.version of an issue's primary document,
// the artifact id the issue's latest version carries, holding the version the event names and
// the title carried forward. A version of another document of the issue, and one of a project's
// document (no issue), starts none. An issue whose latest version names no primary document, or
// that has no version yet, cannot tell its primary document from its versions: the version
// starts with the artifact the event names, and no title where no version gives one, and decide
// reads which document is the issue's primary one from its rows.
func primaryDocumentVersion(e event, latest latestVersion) (version, bool, error) {
	if e.IssueKey == nil {
		return version{}, false, nil
	}
	var p artifactVersionPayload
	if err := decodePayload(e, &p); err != nil {
		return version{}, false, err
	}
	if p.ArtifactID == nil {
		return version{}, false, missing("artifact_id")
	}
	if p.Version == nil || p.Version.Number == nil {
		return version{}, false, missing("version.number")
	}
	key := *e.IssueKey
	prior := latest("issue", key)
	if prior != nil && prior.SpecArtifactID != nil && *prior.SpecArtifactID != *p.ArtifactID {
		return version{}, false, nil
	}
	v, err := newVersion(e, "issue", key, &key)
	if err != nil {
		return version{}, false, err
	}
	v.SpecArtifactID, v.SpecVersion, v.SpecSize = p.ArtifactID, p.Version.Number, p.Version.Size
	if prior != nil {
		v.Text, v.Body, v.BodyFrom = prior.Text, prior.Body, prior.BodyFrom
	}
	return v, true, nil
}

// postVersion is the rule of an event of a comment or a message of kind: the version holds the
// body the event carries, which decide reads whole from another source where the event cut it.
// onlyWhenChanged is an edit's rule, which starts a version only when the body differs from the
// item's latest version's; where the event cut the body, decide compares the whole one. A
// message of no issue starts none.
func postVersion(kind string, onlyWhenChanged bool) rule {
	return func(e event, latest latestVersion) (version, bool, error) {
		var p postPayload
		if err := decodePayload(e, &p); err != nil {
			return version{}, false, err
		}
		if p.ID == nil || *p.ID == "" {
			return version{}, false, missing("id")
		}
		issueKey := p.IssueKey
		if issueKey == nil {
			issueKey = e.IssueKey
		}
		if kind == "message" && issueKey == nil {
			return version{}, false, nil
		}
		if p.Body == nil {
			return version{}, false, missing("body")
		}
		if onlyWhenChanged && !p.BodyTruncated {
			if prior := latest(kind, *p.ID); prior != nil && prior.Body == *p.Body {
				return version{}, false, nil
			}
		}
		v, err := newVersion(e, kind, *p.ID, issueKey)
		if err != nil {
			return version{}, false, err
		}
		v.Text, v.Body, v.BodyTruncated = p.Body, *p.Body, p.BodyTruncated
		return v, true, nil
	}
}

// askVersion is the rule of ask.opened, ask.edited and ask.answered: the version holds the ask's
// question, options and answer as the event carries them, and starts only when the text they
// make, the question, a space and ask_search_suffix of the options and the answer, differs from
// the ask's latest version's.
func askVersion(e event, latest latestVersion) (version, bool, error) {
	var p askPayload
	if err := decodePayload(e, &p); err != nil {
		return version{}, false, err
	}
	if p.ID == nil || *p.ID == "" {
		return version{}, false, missing("id")
	}
	if p.Question == nil {
		return version{}, false, missing("question")
	}
	body := *p.Question + " " + askSearchSuffix(p.Options, p.Answer)
	if prior := latest("ask", *p.ID); prior != nil && prior.Body == body {
		return version{}, false, nil
	}
	issueKey := p.IssueKey
	if issueKey == nil {
		issueKey = e.IssueKey
	}
	v, err := newVersion(e, "ask", *p.ID, issueKey)
	if err != nil {
		return version{}, false, err
	}
	v.Text, v.Body, v.Options, v.Answer = p.Question, body, p.Options, p.Answer
	return v, true, nil
}

// askSearchSuffix is ask_search_suffix(options, answer) of migration 0019, the text an ask's
// options and answer add to its question in its vector, made in Go so the rules can tell whether
// an event changed it: the label and the description of each option, the answer's text and its
// selected options, joined by single spaces, an absent or null value adding nothing but its
// separator. A string is its own text; a value of another JSON type, which no writer puts where
// the function reads, is its JSON as the event wrote it, which can differ from the text jsonb
// gives it, and so can only make a version that repeats its text, never miss one.
func askSearchSuffix(options, answer json.RawMessage) string {
	var described []string
	var elements []json.RawMessage
	if jsonKind(options) == '[' && json.Unmarshal(options, &elements) == nil {
		described = make([]string, 0, len(elements))
		for _, element := range elements {
			fields := jsonObject(element)
			var parts []string
			for _, key := range []string{"label", "description"} {
				if text, ok := jsonText(fields[key]); ok {
					parts = append(parts, text)
				}
			}
			described = append(described, strings.Join(parts, " "))
		}
	}
	answered := jsonObject(answer)
	text, _ := jsonText(answered["text"])
	var selected []string
	if raw := answered["selected"]; jsonKind(raw) == '[' {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			for _, item := range items {
				if value, ok := jsonText(item); ok {
					selected = append(selected, value)
				}
			}
		}
	}
	return strings.Join(described, " ") + " " + text + " " + strings.Join(selected, " ")
}

// jsonKind is the first byte of the JSON value raw, which names its type ('{', '[', '"', 'n'
// and so on), or 0 for no value.
func jsonKind(raw json.RawMessage) byte {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return b
	}
	return 0
}

// jsonObject is the fields of the JSON object raw, and none for a value of another type, which
// ->> reads no field of.
func jsonObject(raw json.RawMessage) map[string]json.RawMessage {
	if jsonKind(raw) != '{' {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	return fields
}

// jsonText is the text ->> gives of a JSON value: a string's own text, and false for an absent
// value or null, which give SQL's null.
func jsonText(raw json.RawMessage) (string, bool) {
	switch jsonKind(raw) {
	case 0, 'n':
		return "", false
	case '"':
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text, true
		}
	}
	return strings.TrimSpace(string(raw)), true
}

// item names one item of the keyword table, the key its versions share: its kind and its id.
type item struct{ kind, id string }

// decided is what decideVersions made of a batch of events: the versions they start, in event id
// order, each with its body whole where a source holds it, and Cut, how many of them hold a
// post's body cut short because no source held it whole (bodyFromCut), the count the fill's
// progress line reads.
type decided struct {
	Versions []version
	Cut      int
}

// decideVersions decides, in the caller's transaction tx, the versions events start: events in
// ascending id order, as the fill reads the log. Each event goes through the rule of its type
// (versionRules) with the item's latest version, which is the latest of the item decided earlier
// in the same batch, else the item's latest row in the table (latestVersions), so two events of
// one item in one batch each see the other's change: an issue's title and its primary document
// version are carried forward from row to row, and an edit is compared with the text before it.
// An event of a type no rule reads starts no version.
//
// What an event does not hold is read from the item's rows, in the same transaction:
//
//   - A post whose event cut its body (body_truncated) takes it whole, in this order: from its
//     present row when that shows no later change, a comment whose edited_at is null or at or
//     before the event's created_at (an edit stores its transaction's now(), its event's
//     created_at) and an issue message, which no build edits (bodyFromRow); for a
//     comment.created, from revision 1 of its blocks when every block of that revision was
//     inserted by the comment's own transaction (firstRevisionBody, bodyFromBlocks); else it
//     keeps the event's text (bodyFromCut), with a warning naming the item and the event, and
//     counts in Cut. An edit whose whole body equals its latest version's starts no version.
//   - An issue.created whose primary document's version 1 was not written at its created_at, by
//     the create's own transaction, still holds that version, with a warning naming the issue.
//   - An issue event before any version of its issue, which only a log that lacks the issue's
//     issue.created holds, as after a reset of the log that kept the issue's rows, reads the
//     issue from its rows (readIssueRows), with a warning naming the issue and the event: its
//     primary document (artifacts.is_primary), whose version at the event's time an
//     issue.updated or issue.closed holds, and for an artifact.version, which carries no
//     title, the issue's present title (bodyFromRow). An artifact.version of an issue whose
//     latest version names no primary document is held to artifacts.is_primary the same way.
//
// logger takes the warnings. An error names the event it met: a payload a rule cannot read
// (errUnreadablePayload) or a statement that failed.
func decideVersions(ctx context.Context, tx pgx.Tx, logger *slog.Logger, events []event) (decided, error) {
	// Each rule asks for the latest version of the item it decides: a first pass through the
	// rules names those items, so the table's rows of all of them are read in one statement.
	var items []item
	asked := map[item]bool{}
	record := func(kind, id string) *version {
		if key := (item{kind, id}); !asked[key] {
			asked[key] = true
			items = append(items, key)
		}
		return nil
	}
	for index, e := range events {
		if index > 0 && e.ID <= events[index-1].ID {
			return decided{}, fmt.Errorf("decide the keyword versions: event %d comes after event %d, out of id order", e.ID, events[index-1].ID)
		}
		if apply, ok := versionRules[e.Type]; ok {
			if _, _, err := apply(e, record); err != nil {
				return decided{}, eventError(e, err)
			}
		}
	}
	stored, err := latestVersions(ctx, tx, items)
	if err != nil {
		return decided{}, err
	}
	d := &decider{tx: tx, logger: logger, stored: stored, batch: map[item]*version{}}
	for _, e := range events {
		apply, ok := versionRules[e.Type]
		if !ok {
			continue
		}
		v, started, err := apply(e, d.latest)
		if err != nil {
			return decided{}, eventError(e, err)
		}
		if !started {
			continue
		}
		keep, err := d.complete(ctx, e, &v)
		if err != nil {
			return decided{}, eventError(e, err)
		}
		if !keep {
			continue
		}
		d.out.Versions = append(d.out.Versions, v)
		d.batch[item{v.Kind, v.ID}] = &v
	}
	if err := d.warnFirstSpecVersions(ctx); err != nil {
		return decided{}, err
	}
	return d.out, nil
}

// eventError names the event err was met at.
func eventError(e event, err error) error {
	return fmt.Errorf("decide the keyword version of event %d (%s): %w", e.ID, e.Type, err)
}

// decider is one batch's decision as decideVersions makes it: the latest versions the table held
// of the batch's items, the latest each item has taken in this batch, and the versions decided so
// far.
type decider struct {
	tx     pgx.Tx
	logger *slog.Logger
	stored map[item]*version
	batch  map[item]*version
	out    decided
	// firstSpecs indexes the versions of out an issue.created started holding version 1 of the
	// issue's primary document, which warnFirstSpecVersions reads the making time of.
	firstSpecs []int
}

// latest is the latestVersion the batch's rules read: the item's latest version decided in this
// batch, else its latest row in the table.
func (d *decider) latest(kind, id string) *version {
	if v, ok := d.batch[item{kind, id}]; ok {
		return v
	}
	return d.stored[item{kind, id}]
}

// complete gives the version v that e starts what the event does not hold, from the item's rows
// (decideVersions), and answers whether v is still a version: false for an artifact.version the
// rows show is not of the issue's primary document, and for an edit whose whole body is its
// latest version's.
func (d *decider) complete(ctx context.Context, e event, v *version) (bool, error) {
	prior := d.latest(v.Kind, v.ID)
	switch v.Kind {
	case "issue":
		switch {
		case e.Type == "issue.created":
			if v.SpecVersion != nil {
				d.firstSpecs = append(d.firstSpecs, len(d.out.Versions))
			}
		case prior == nil:
			return d.seedIssue(ctx, e, v)
		case e.Type == "artifact.version" && prior.SpecArtifactID == nil:
			rows, found, err := readIssueRows(ctx, d.tx, v.ID, e.CreatedAt)
			if err != nil {
				return false, err
			}
			return found && rows.primary != nil && *rows.primary == *v.SpecArtifactID, nil
		}
	case "comment", "message":
		if !v.BodyTruncated {
			return true, nil
		}
		if err := d.wholeBody(ctx, e, v); err != nil {
			return false, err
		}
		return prior == nil || prior.Body != v.Body, nil
	}
	return true, nil
}

// seedIssue completes v, which e starts of an issue that has no version yet, from the issue's
// rows (decideVersions), and answers whether it is still a version.
func (d *decider) seedIssue(ctx context.Context, e event, v *version) (bool, error) {
	rows, found, err := readIssueRows(ctx, d.tx, v.ID, e.CreatedAt)
	if err != nil {
		return false, err
	}
	if !found {
		d.logger.Warn("search text versions fill: an issue event names an issue the log never created and the database does not hold; it starts no version",
			"issue", v.ID, "event_id", e.ID, "event_type", e.Type)
		return false, nil
	}
	if e.Type == "artifact.version" {
		if rows.primary == nil || *rows.primary != *v.SpecArtifactID {
			return false, nil
		}
		v.Text, v.Body, v.BodyFrom = &rows.title, rows.title, bodyFromRow
	} else {
		if v.SpecArtifactID == nil {
			v.SpecArtifactID = rows.primary
		}
		if v.SpecArtifactID != nil && rows.primary != nil && *v.SpecArtifactID == *rows.primary {
			v.SpecVersion = rows.specVersion
		}
	}
	d.logger.Warn("search text versions fill: the log holds no earlier version of the issue, so its version reads the issue's rows",
		"issue", v.ID, "event_id", e.ID, "event_type", e.Type, "body_from", v.BodyFrom)
	return true, nil
}

// issueRows is what an issue's rows say of it at a time: its present title, its primary
// document, and the latest version of that document made at or before the time.
type issueRows struct {
	title       string
	primary     *string
	specVersion *int32
}

// issueRowsQuery reads issueRows of issue $1 at time $2.
const issueRowsQuery = `
	select i.title, a.id::text,
	       (select max(av.number) from public.artifact_versions av
	         where av.artifact_id = a.id and av.created_at <= $2)
	  from public.issues i
	  left join public.artifacts a on a.issue_key = i.key and a.is_primary
	 where i.key = $1`

// readIssueRows reads issueRows of issue key at time at; found is false when no issue has the
// key.
func readIssueRows(ctx context.Context, tx pgx.Tx, key string, at time.Time) (issueRows, bool, error) {
	var rows issueRows
	err := tx.QueryRow(ctx, issueRowsQuery, key, at).Scan(&rows.title, &rows.primary, &rows.specVersion)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return issueRows{}, false, nil
	case err != nil:
		return issueRows{}, false, fmt.Errorf("read issue %s: %w", key, err)
	}
	return rows, true, nil
}

// wholeBody gives v, a version of a post whose event e cut its body, the whole body from the
// first source that holds it (decideVersions), or keeps the event's text, warns and counts it.
func (d *decider) wholeBody(ctx context.Context, e event, v *version) error {
	switch v.Kind {
	case "comment":
		var body string
		var editedAt *time.Time
		err := d.tx.QueryRow(ctx, `select body, edited_at from public.comments where id = $1::uuid`, v.ID).Scan(&body, &editedAt)
		switch {
		case err == nil && (editedAt == nil || !editedAt.After(e.CreatedAt)):
			v.Body, v.BodyFrom = body, bodyFromRow
			return nil
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read comment %s: %w", v.ID, err)
		}
		if e.Type == "comment.created" {
			body, ok, err := firstRevisionBody(ctx, d.tx, v.ID, e.CreatedAt)
			if err != nil {
				return err
			}
			if ok {
				v.Body, v.BodyFrom = body, bodyFromBlocks
				return nil
			}
		}
	case "message":
		var body string
		err := d.tx.QueryRow(ctx, `select body from public.messages where id = $1::uuid`, v.ID).Scan(&body)
		switch {
		case err == nil:
			v.Body, v.BodyFrom = body, bodyFromRow
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read message %s: %w", v.ID, err)
		}
	}
	v.BodyFrom = bodyFromCut
	d.out.Cut++
	d.logger.Warn("search text versions fill: no source holds the whole body its event cut, so the version keeps the event's text",
		"kind", v.Kind, "id", v.ID, "event_id", e.ID, "event_type", e.Type)
	return nil
}

// firstRevisionQuery reads the blocks of revision 1 of comment $1, the comment's first indexing,
// in the order of their ids' numbers (b2 before b10), which the first indexing mints in body
// order: each block's text and whether its block was inserted by the transaction whose start is
// $2.
const firstRevisionQuery = `
	select t.text, b.created_at = $2
	  from public.comment_block_texts t
	  join public.comment_blocks b on b.comment_id = t.comment_id and b.block_id = t.block_id
	 where t.comment_id = $1::uuid and t.revision = 1
	 order by length(t.block_id), t.block_id`

// firstRevisionBody is comment id's first body as revision 1 of its blocks holds it: their
// texts, in their order, joined by one blank line. ok is false unless every block of revision 1
// was inserted by the transaction that started at createdAt, the comment's own (a comment is
// indexed in the transaction that writes it, so revision 1 is then its first body), and holds a
// text. A block's text keeps the body's words and drops only the white space that ends it
// (itemblocks.Split), so the body differs from the one the comment was written with only in the
// white space between blocks, which changes no lexeme of its vector.
func firstRevisionBody(ctx context.Context, tx pgx.Tx, id string, createdAt time.Time) (string, bool, error) {
	rows, err := tx.Query(ctx, firstRevisionQuery, id, createdAt)
	if err != nil {
		return "", false, fmt.Errorf("read the first revision of comment %s: %w", id, err)
	}
	var texts []string
	whole := true
	for rows.Next() {
		var text *string
		var own bool
		if err := rows.Scan(&text, &own); err != nil {
			rows.Close()
			return "", false, fmt.Errorf("read the first revision of comment %s: %w", id, err)
		}
		if text == nil || !own {
			whole = false
			continue
		}
		texts = append(texts, *text)
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("read the first revision of comment %s: %w", id, err)
	}
	if !whole || len(texts) == 0 {
		return "", false, nil
	}
	return strings.Join(texts, "\n\n"), true, nil
}

// warnFirstSpecVersions warns, naming the issue, of each issue.created version whose primary
// document's version 1 was not made at the event's created_at, by the create's own transaction,
// or is not there at all: the version holds it all the same.
func (d *decider) warnFirstSpecVersions(ctx context.Context) error {
	if len(d.firstSpecs) == 0 {
		return nil
	}
	artifacts := make([]string, 0, len(d.firstSpecs))
	for _, index := range d.firstSpecs {
		artifacts = append(artifacts, *d.out.Versions[index].SpecArtifactID)
	}
	rows, err := d.tx.Query(ctx, `
		select a.id, v.created_at
		  from unnest($1::text[]) as a (id)
		  left join public.artifact_versions v on v.artifact_id = a.id::uuid and v.number = 1`, artifacts)
	if err != nil {
		return fmt.Errorf("read version 1 of the issues' primary documents: %w", err)
	}
	made := make(map[string]*time.Time, len(artifacts))
	for rows.Next() {
		var id string
		var at *time.Time
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return fmt.Errorf("read version 1 of the issues' primary documents: %w", err)
		}
		made[id] = at
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read version 1 of the issues' primary documents: %w", err)
	}
	for _, index := range d.firstSpecs {
		v := d.out.Versions[index]
		if at := made[*v.SpecArtifactID]; at == nil || !at.Equal(v.StartedAt) {
			d.logger.Warn("search text versions fill: the issue's create did not write version 1 of its primary document, which the issue's first version holds all the same",
				"issue", v.ID, "event_id", v.EventID, "artifact_id", *v.SpecArtifactID, "version_created_at", at, "event_created_at", v.StartedAt)
		}
	}
	return nil
}

// latestVersionsQuery reads, for each item ($1 its kinds and $2 its ids), its latest row in the
// keyword table: the one with the greatest event_seq, read backwards through the primary key
// (kind, id, event_seq), one probe for each item.
const latestVersionsQuery = `
	select l.kind, l.id, l.issue_key, l.project_key, l.event_id, l.event_seq, l.started_at, l.body,
	       l.body_from, l.spec_artifact_id::text, l.spec_version
	  from unnest($1::text[], $2::text[]) as k (kind, id)
	  cross join lateral (
	        select v.kind, v.id, v.issue_key, v.project_key, v.event_id, v.event_seq, v.started_at,
	               v.body, v.body_from, v.spec_artifact_id, v.spec_version
	          from public.search_text_versions v
	         where v.kind = k.kind and v.id = k.id
	         order by v.event_seq desc
	         limit 1) l`

// latestVersions reads the latest row in the keyword table of each of items, in tx: an item with
// no row is not in the map. A version read from the table holds its body as its Body, and as its
// Text where the body is the item's own text, which an ask's is not: an ask's body holds its
// question, a space and ask_search_suffix of its options and answer.
func latestVersions(ctx context.Context, tx pgx.Tx, items []item) (map[item]*version, error) {
	latest := make(map[item]*version, len(items))
	if len(items) == 0 {
		return latest, nil
	}
	kinds := make([]string, len(items))
	ids := make([]string, len(items))
	for index, key := range items {
		kinds[index], ids[index] = key.kind, key.id
	}
	rows, err := tx.Query(ctx, latestVersionsQuery, kinds, ids)
	if err != nil {
		return nil, fmt.Errorf("read the latest keyword versions of %d items: %w", len(items), err)
	}
	defer rows.Close()
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.Kind, &v.ID, &v.IssueKey, &v.ProjectKey, &v.EventID, &v.EventSeq, &v.StartedAt, &v.Body,
			&v.BodyFrom, &v.SpecArtifactID, &v.SpecVersion); err != nil {
			return nil, fmt.Errorf("read the latest keyword versions of %d items: %w", len(items), err)
		}
		if v.Kind != "ask" {
			v.Text = &v.Body
		}
		latest[item{v.Kind, v.ID}] = &v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the latest keyword versions of %d items: %w", len(items), err)
	}
	return latest, nil
}

// insertVersionsQuery writes the rows of decided versions, given as parallel arrays, in one
// statement, and makes each row's body and vector with the functions the items' own search
// triggers call, so the vector of a version of an unchanged item equals the item's search
// column: an issue's from its key, its title and the markdown of the version of its primary
// document the row names (issue_search_vector, 20261105145242), a comment's or a message's from
// its body, and an ask's from its question, a space and ask_search_suffix of its options and
// answer (search_vector over search_text, 0087 and 0019), which is also the body the ask's row
// holds.
const insertVersionsQuery = `
	insert into public.search_text_versions
	       (kind, id, issue_key, project_key, event_id, event_seq, started_at, body, body_from,
	        spec_artifact_id, spec_version, search)
	select r.kind, r.id, r.issue_key, r.project_key, r.event_id, r.event_seq, r.started_at, b.body,
	       r.body_from, r.spec_artifact_id::uuid, r.spec_version,
	       case r.kind
	         when 'issue' then issue_search_vector(r.id, b.body,
	           (select a.markdown from public.artifact_versions a
	             where a.artifact_id = r.spec_artifact_id::uuid and a.number = r.spec_version))
	         else search_vector('', search_text(b.body))
	       end
	  from unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::bigint[], $6::integer[],
	              $7::timestamptz[], $8::text[], $9::text[], $10::text[], $11::integer[], $12::text[],
	              $13::text[])
	       as r (kind, id, issue_key, project_key, event_id, event_seq, started_at, text, body_from,
	             spec_artifact_id, spec_version, options, answer)
	 cross join lateral (
	       select case r.kind
	                when 'ask' then r.text || ' ' || ask_search_suffix(r.options::jsonb, r.answer::jsonb)
	                else r.text
	              end as body) b`

// insertVersions writes versions as rows of the keyword table in tx, in one statement
// (insertVersionsQuery), and writes nothing for none. A version's text is its Body, and an ask's
// its question (Text), which the statement makes the body of with its options and answer. A row
// is never written twice: a version whose (kind, id, event_seq) the table holds fails the
// statement.
func insertVersions(ctx context.Context, tx pgx.Tx, versions []version) error {
	if len(versions) == 0 {
		return nil
	}
	n := len(versions)
	kinds, ids, projects, texts, bodyFroms := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	issueKeys, specArtifacts, options, answers := make([]*string, n), make([]*string, n), make([]*string, n), make([]*string, n)
	eventIDs, seqs, specVersions := make([]int64, n), make([]int32, n), make([]*int32, n)
	startedAts := make([]time.Time, n)
	for index, v := range versions {
		text := v.Body
		if v.Kind == "ask" {
			if v.Text == nil {
				return fmt.Errorf("write the keyword version of ask %s at event %d: it holds no question", v.ID, v.EventID)
			}
			text = *v.Text
			options[index], answers[index] = rawJSON(v.Options), rawJSON(v.Answer)
		}
		kinds[index], ids[index], issueKeys[index], projects[index] = v.Kind, v.ID, v.IssueKey, v.ProjectKey
		eventIDs[index], seqs[index], startedAts[index] = v.EventID, v.EventSeq, v.StartedAt
		texts[index], bodyFroms[index] = text, v.BodyFrom
		specArtifacts[index], specVersions[index] = v.SpecArtifactID, v.SpecVersion
	}
	tag, err := tx.Exec(ctx, insertVersionsQuery, kinds, ids, issueKeys, projects, eventIDs, seqs, startedAts, texts,
		bodyFroms, specArtifacts, specVersions, options, answers)
	if err != nil {
		return fmt.Errorf("write %d keyword versions: %w", n, err)
	}
	if tag.RowsAffected() != int64(n) {
		return fmt.Errorf("write %d keyword versions: the insert wrote %d", n, tag.RowsAffected())
	}
	return nil
}

// rawJSON is raw as the text of a jsonb parameter, nil (SQL's null) for no value.
func rawJSON(raw json.RawMessage) *string {
	if len(raw) == 0 {
		return nil
	}
	text := string(raw)
	return &text
}
