package grapherase

import "slices"

// payloadClass is what a string value of an event payload is to the erase's redaction
// (redactPayload), which decides what the redaction may rewrite in it.
type payloadClass int

const (
	// ownWords is the erased node's own words, or a value that can carry them: every erased text
	// in it is replaced with the marker wherever it stands.
	ownWords payloadClass = iota + 1
	// quotedWords is another node's words, which the payload quotes: an erased text in it is
	// replaced only where it stands as a block of its own (redactBlockQuote), since inside a
	// longer line the same words are that node's, which the erase of this one leaves (R5).
	quotedWords
	// identity is who wrote or acted, and the payload's structure: ids, keys, addresses, states,
	// kinds, digests and times. It is never rewritten, as a key is never rewritten: it says who did
	// what the event records and what it is about, and none of it is the node's text.
	identity
)

// payloadPaths is the classification of one payload shape: each string value it can hold, by
// its path, in the list of its class. A path names the JSON keys from the payload's top level
// down to the value, joined by `.`, with `[]` after the key of an array for an element of it
// (`author.origin.host`, `ask_sessions[]`, `version.authors[].act.token_name`). A string at a path
// no list names is read by eventClasses.classOf.
type payloadPaths struct {
	own, quoted, identity []string
	// legacy are identity paths production's log holds that no Go field of this build writes, so
	// no walk of the shape's Go type reaches them.
	legacy []legacyPath
}

// legacyPath is an identity path of payloads an older build wrote, which the erase still reads:
// where it was seen and why no field of this build writes it.
type legacyPath struct {
	path, seen, why string
}

// payloadClassification is the erase's classification of the payload of each event type it
// reads (readEvents): every string value each type's Go payload can hold, by its path, in one of
// three classes - the node's own words, or a value that can carry them (ownWords); another node's
// words, which the payload quotes (quotedWords); and identity, which the redaction never rewrites.
// Every identity path is listed as every other is - one an older build wrote that no field of
// this build writes in its shape's legacy list - so a path nobody classified is one no list names:
// TestEveryStringOfAnEventPayloadTheEraseReadsHasAClass fails on any string of a payload's Go type
// that no list names, and on any listed path no field writes.
//
// Who wrote or acted is not the node's text. An actor (actorPaths) - its kind and id, the
// client environment its session reported (`origin`), the agent that acted for a person (`act`) -
// and a list of the sessions or the people a write reached are identity wherever they stand, and
// so are the payload's ids, keys, addresses, states, kinds, digests and times. An event whose
// identity value holds an erased text keeps it, and the listing names that event as a row the run
// leaves holding the text (readLeft, cleanedEvents): an author whose host is named after the
// word a comment held is that author's fact, which the erase of the comment leaves.
//
// A path it does not name - a key a later or an older build writes, or a payload of a type it
// does not hold - is read by the rule it replaced (eventClasses.classOf): identity where
// structuralKey names the key the value stands under, else the content class of the predicate
// that read the event (classesOf). That fallback stays because a field another build adds that
// holds what a person wrote must lose the erased bytes as every other does.
var payloadClassification = map[string]payloadPaths{
	"comment.anchor_refreshed":   commentEventPaths,
	"comment.answered":           commentEventPaths,
	"comment.created":            commentEventPaths,
	"comment.edited":             commentEventPaths,
	"comment.reopened":           commentEventPaths,
	"comment.resolved":           commentEventPaths,
	"suggestion.accepted":        commentEventPaths,
	"suggestion.rejected":        commentEventPaths,
	"comment.delivery":           deliveryReceiptPaths,
	"artifact.version":           artifactVersionPaths,
	"artifact.human_edited":      humanEditedPaths,
	"artifact.approved":          reviewPaths,
	"artifact.changes_requested": reviewPaths,
}

// actorPaths are the string paths of a model.Actor, from the actor down: its kind and id, the
// client environment its session reported (model.ActorOrigin), the person and the workload a
// credential named (model.Actor's owner and service; it writes no project), and the agent that
// acted for a person (model.ActorAct). An actor is who wrote or acted, so each is identity at every
// path an actor stands at (under).
var actorPaths = []string{
	"kind", "id",
	"origin.host", "origin.machine", "origin.cwd", "origin.tmux", "origin.pane", "origin.session_title",
	"owner", "service",
	"act.session", "act.token_id", "act.token_name", "act.scopes[]",
}

// referenceChangePaths are the string paths model.ReferenceChangesPayload and
// model.NameReferenceChanges write: each node whose inbound reference edges a write moved, by its
// kind, its id, its issue, its document and its reference, and the kinds of which the write moved
// more than the event names. Each is identity.
var referenceChangePaths = slices.Concat(
	under("references_changed[]", []string{"kind", "id", "issue_key", "artifact_id", "ref"}),
	[]string{"references_changed_truncated_kinds[]"},
)

// commentEventPaths classify model.CommentEventPayload, the payload of a comment's own events:
// the comment (model.Comment), with the name of the document it is anchored to, the question of
// the ask it replies to, and the sessions and routes its write reached.
var commentEventPaths = payloadPaths{
	// What the comment's author wrote - its body and a suggestion's replacement - and what each
	// send of it answered, which can echo the body the send carried.
	own: []string{"body", "suggestion.replace_with", "deliveries[].error", "deliveries[].resolve_error"},
	// What the comment's write copied from the node it quotes: the quote of the document the
	// comment is anchored to (model.Anchor) and the text of the table its anchor's block stands
	// in (model.TablePosition), that document's name, the question of the ask the comment replies
	// to, and the text of the block the reply answers (model.ReplyBlock).
	quoted: []string{
		"anchor.quote", "anchor_block.table.header", "anchor_block.table.cells[]",
		"artifact_name", "ask_question", "reply_block.text",
	},
	identity: slices.Concat([]string{
		"id", "issue_key", "artifact_id", "reply_to", "answers", "ask_id", "turn",
		"resolved_at", "edited_at", "created_at",
		"anchor.artifact_id", "anchor.block_id", "anchor.mark_id",
		"anchor_block.id", "anchor_block.type", "anchor_block.path[].type", "anchor_block.path[].id",
		"anchor_block.table.id", "anchor_block_error",
		"mentions[].target", "mentions[].delivery", "mentions[].session_id",
		"deliveries[].comment_id", "deliveries[].target", "deliveries[].delivery", "deliveries[].session_id",
		"deliveries[].envelope_id", "deliveries[].state", "deliveries[].reply_id", "deliveries[].created_at",
		"reply_block.node", "reply_block.ref", "reply_block.block_id",
		"project_key", "artifact_slug", "ask_state", "ask_waiting_on", "ask_sessions[]", "thread_root_id",
		"suppressed_route", "suppressed_route_session_id", "thread_logins[]",
	}, under("author", actorPaths), under("resolved_by", actorPaths), referenceChangePaths),
	legacy: []legacyPath{{
		path: "suppressed_authors[]",
		seen: "comment.created and comment.edited payloads of production's log " +
			"(textversions' testdata/payloads/comment.created-*.json and comment.edited-1.json)",
		why: "model.CommentEventPayload carried it, the authors' sessions a route left to the comment's mentions; " +
			"the session routes read the comment's mentions and nothing read it, so the field went (CHANGELOG)",
	}},
}

// deliveryReceiptPaths classify model.CommentDeliveryEventPayload, the receipt of one attempt to
// deliver a comment's mention. Its error is what the send answered, which can echo what the send
// carried; everything else names the attempt.
var deliveryReceiptPaths = payloadPaths{
	own:      []string{"error"},
	identity: []string{"comment_id", "ask_id", "target", "delivery", "session_id", "state", "reply_id"},
}

// artifactVersionPaths classify the payload docs.ArtifactVersionEventPayload builds: the
// document, the version (model.Version) and the diff it was taken with, and what the write moved
// in the reference graph. The payload quotes the whole document, so every value of it a person
// wrote is quoted words: the document's name, the version's summary and the diff.
var artifactVersionPaths = payloadPaths{
	quoted: []string{"name", "diff", "version.summary"},
	identity: slices.Concat(
		[]string{"artifact_id", "version.created_at", "version.mime", "version.sha256"},
		under("version.authors[]", actorPaths),
		referenceChangePaths,
	),
}

// humanEditedPaths classify model.ArtifactHumanEditedEventPayload: the document, the people who
// edited it and the diff of their edits, which quotes the document.
var humanEditedPaths = payloadPaths{
	quoted:   []string{"name", "diff"},
	identity: []string{"artifact_id", "editors[]"},
}

// reviewPaths classify model.ArtifactReviewEventPayload, the payload of artifact.approved and
// artifact.changes_requested, with the digest of the approved bytes and where it was read from,
// which a statement adds after the append (docs' approval review). The review's reason quotes the
// document as its name does.
var reviewPaths = payloadPaths{
	quoted: []string{"name", "reason"},
	identity: slices.Concat(
		[]string{"artifact_id", "kind", "ask_id", "content_sha256", "content_sha256_source"},
		under("actor", actorPaths),
	),
}

// under is each of paths as it stands below prefix.
func under(prefix string, paths []string) []string {
	composed := make([]string, len(paths))
	for index, path := range paths {
		composed[index] = keyPath(prefix, path)
	}
	return composed
}

// keyPath is the path of the value under key inside the object at path, the payload's top level
// where path is empty.
func keyPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// eventClasses is the classification of one event type as redactPayload reads it: the class of
// each path it names, and the class of a string at a path it does not name (classOf).
type eventClasses struct {
	paths   map[string]payloadClass
	content payloadClass
}

// payloadClasses is payloadClassification by event type, each path a shape names with its class:
// its own, quoted and identity paths, and its legacy paths as identity.
var payloadClasses = indexPayloadClasses()

// indexPayloadClasses answers payloadClassification by event type, each path with its class.
func indexPayloadClasses() map[string]map[string]payloadClass {
	indexed := make(map[string]map[string]payloadClass, len(payloadClassification))
	for kind, shape := range payloadClassification {
		paths := make(map[string]payloadClass, len(shape.own)+len(shape.quoted)+len(shape.identity)+len(shape.legacy))
		for _, listed := range []struct {
			class payloadClass
			paths []string
		}{{ownWords, shape.own}, {quotedWords, shape.quoted}, {identity, shape.identity}} {
			for _, path := range listed.paths {
				paths[path] = listed.class
			}
		}
		for _, legacy := range shape.legacy {
			paths[legacy.path] = identity
		}
		indexed[kind] = paths
	}
	return indexed
}

// classesOf is the classification of a payload of type kind that the predicate comment names read
// (readEvents): the class of each path payloadClassification names for kind, none for a type it
// does not hold, and the content class of every other path, which the predicate alone decides.
// comment is the erased comment's id, empty for a block: an event about a comment, which the
// comment's predicate read, is the comment's own words, and an event about a block, a document
// event of the block's document, quotes that document. Each type the classification holds is read
// by one of the two predicates alone - a comment's own events and its receipts by the comment's,
// the document events by the block's - so no type carries a content class of its own.
func classesOf(kind, comment string) eventClasses {
	content := quotedWords
	if comment != "" {
		content = ownWords
	}
	return eventClasses{paths: payloadClasses[kind], content: content}
}

// classOf is the class of the string at path, which stands under key - the key of the array
// holding it, for an element of one: the class the classification names for path, else identity
// where key is structural (structuralKey), else the content class of the predicate that read the
// event (classesOf).
func (c eventClasses) classOf(path, key string) payloadClass {
	if class, held := c.paths[path]; held {
		return class
	}
	if structuralKey(key) {
		return identity
	}
	return c.content
}
