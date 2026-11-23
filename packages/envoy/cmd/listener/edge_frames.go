package main

// edge_frames.go is the edge listener's rule for every frame it hands an outside session: the
// relay's, before it publishes a frame on the session's relay subject (sessionRelays.edgeWithholds),
// and the relay socket's, before it writes one to the client (relaySocket.forward). A frame passes
// only when it is of a project the session's token lists, when it is the session's own message, or
// when it is a GitHub event that the repository rule passes (edgeRepositories.relayVerdict).
//
// The projects are a token's own, and never a stored record's: the socket holds the token it was
// opened with, which it asks the broker about again and closes on when the answer names other
// projects (relaySocket.admitted, relaySocket.verdict), and the relay holds the token this replica
// admitted the session's registration on, in memory (edgeAdmissions). So nothing the bus stores -
// a session's entry, an interest row, a role claim, the filter of the session's consumer - can
// put another project's frame in front of the session. Every frame the rule withholds is
// acknowledged undelivered and counted under project_not_held.

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
)

// withheldProjectNotHeld is the reason envoy_edge_github_withheld_total counts a frame under that
// is of no project the session's token lists.
const withheldProjectNotHeld = "project_not_held"

// withheldAmbiguousKey is the reason envoy_edge_github_withheld_total counts a frame under that
// names a field the edge judges by under more than one top-level key, or under a key that differs
// from the field's name only in case (errAmbiguousFrameKey).
const withheldAmbiguousKey = "ambiguous_key"

// withheldPrototypeKey is the reason envoy_edge_github_withheld_total counts a frame under that
// holds a top-level "__proto__" key (errPrototypeFrameKey).
const withheldPrototypeKey = "prototype_key"

// frameKeyWithheld is the verdict on a frame whose read of a field the edge judges by answered err
// (exactFrameField): withheld, counted under withheldPrototypeKey for a "__proto__" key and under
// withheldAmbiguousKey for a key readers disagree on, so the log and the count say why.
func frameKeyWithheld(err error) frameVerdict {
	if errors.Is(err, errPrototypeFrameKey) {
		return frameDrops(withheldPrototypeKey)
	}
	return frameDrops(withheldAmbiguousKey)
}

// withheldNotAdmitted is why the relay withholds, unacknowledged and uncounted, a frame of a
// session this listener admitted no registration of (sessionRelays.edgeWithholds).
const withheldNotAdmitted = "not_admitted"

// frameAction is what the edge does with a frame (edgeFrameVerdict): the control flow its callers
// switch on, so a new mode is a value they must handle rather than a reason string that falls
// through to acknowledge-and-count. The metric label a counted drop or a withhold carries is
// frameVerdict.reason, a field apart from the action.
type frameAction uint8

const (
	// framePass delivers the frame.
	framePass frameAction = iota
	// frameDrop acknowledges the frame undelivered and counts it under its reason.
	frameDrop
	// frameHold withholds the frame unacknowledged, for the consumer to deliver again
	// (withheldNotAdmitted); the relay alone reaches it, and it is never counted.
	frameHold
	// frameAnswerUnknown withholds a GitHub frame for want of a fresh repository answer: the relay
	// marks the session's gap and acknowledges it, the socket answers it nak, and it is sent again.
	frameAnswerUnknown
)

// frameVerdict is the edge's decision on one frame: its action, the metric label (reason) a
// counted drop or a withhold logs under, and whether a fresh repository answer made it (answered):
// a GitHub event relayed because a fresh answer names its repository, or dropped because every
// answer of the session's projects is fresh and none names it (relayVerdict). Only such a verdict
// ends a run of GitHub frames given back for want of an answer (sessionRelays.endGiveBacks,
// relaySocket.endUnknownNaks); a drop of an event that names no repository, or of an envelope that
// is no GitHub event, says nothing of the answer.
type frameVerdict struct {
	action   frameAction
	reason   string
	answered bool
}

func framePasses() frameVerdict             { return frameVerdict{action: framePass} }
func frameDrops(reason string) frameVerdict { return frameVerdict{action: frameDrop, reason: reason} }
func frameHolds() frameVerdict              { return frameVerdict{action: frameHold, reason: withheldNotAdmitted} }
func frameAnswerUnknownVerdict() frameVerdict {
	return frameVerdict{action: frameAnswerUnknown, reason: withheldAnswerUnknown}
}

// edgeFrameVerdict is whether the edge withholds from sessionID, whose token lists projects, a
// frame of the message stored on subject with data, and why. Every field of data it judges by is
// read by its exact top-level key, as the session's plugin reads the frame (exactFrameField), and
// a frame that holds such a field under more than one key, or under a key that differs from the
// field's name only in case, is withheld: readers disagree on which of those keys a field is. A
// frame with a top-level "__proto__" key is withheld too, on every branch: the plugin reads that
// key's fields as the envelope's own (errPrototypeFrameKey).
//
//   - a GitHub event is the repository rule's (relayVerdict), with no reader withheld
//     answer_unknown, and one whose envelope names a topic that is no GitHub event's is withheld;
//   - a message on an agent subject passes only on the session's own (ownFrameAgentSubject): a
//     role message - a role-lane forward or a role-marked send, told by the read the session
//     relay tells one by (readRoleFrame) - passes only for the project its role's claim works for
//     (roleOfProjects), judged by the role and not by the subject it is stored on, since a forged
//     role claim or a kept or taken copy can store another project's role's message on the
//     session's own subject; any other frame, a message whose role that read cannot tell among
//     them, as the relay sends one, passes by the topic its envelope names: the session's own
//     agent subject is a message to the session, a send or a dead letter, which passes, and any
//     other topic is judged as a frame of that topic. A forward of a GitHub event or of another
//     agent subject, an envelope that names no topic, and a frame encoding/json does not read
//     (frameIsJSON) that is no role message, are withheld;
//   - every other frame passes when contracts.SubjectProject gives it a project the token lists
//     and, where its envelope names a topic, that topic is of the same project: the subject a
//     relay header names and the envelope it carries are both what a publisher on the relay
//     subject writes, so a frame whose envelope names another project's event under a header of
//     the token's project is withheld.
func edgeFrameVerdict(repositories *edgeRepositories, sessionID string, projects []string, subject string, data []byte) frameVerdict {
	topic, named, err := exactFrameTopic(data)
	if err != nil {
		return frameKeyWithheld(err)
	}
	if strings.HasPrefix(subject, contracts.GithubTopicPrefix) {
		if named && !strings.HasPrefix(topic, contracts.GithubTopicPrefix) {
			return frameDrops(withheldProjectNotHeld)
		}
		if repositories == nil {
			return frameAnswerUnknownVerdict()
		}
		return repositories.relayVerdict(projects, subject, data)
	}
	if strings.HasPrefix(subject, contracts.AgentTopicPrefix) {
		if !ownFrameAgentSubject(sessionID, projects, subject) {
			return frameDrops(withheldProjectNotHeld)
		}
		// A role message - a role-lane forward, whose topic names the lane, or a role-marked
		// send, whose role mark names the role - is judged by the project its role's claim works
		// for (roleOfProjects), not by the subject the frame is stored on. A forged role claim can
		// name the session for another project's role and have Dispatch send that role's message
		// on the session's own subject, and a kept or a taken copy of the session's own project's
		// role keeps the previous holder's subject as its topic; both reach the session on its own
		// agent subject. The role is read as the session relay, a takeover pass and the kept
		// lane's walk read it (readRoleFrame): by the exact keys the plugins' transport reads,
		// past a byte order mark and at any depth, so a frame the session's plugin reads as a
		// role's message is judged by that role here, whatever encoding/json makes of its other
		// fields. It holds a role lane to the project edgeTopicRule does (contracts.RoleClaimOf).
		// A frame whose role mark readers disagree on - one under more than one "role" key, or
		// under a key that differs from "role" only in case, which readRoleFrame passes over and
		// encoding/json takes - is withheld first, as one whose topic they disagree on is above
		// (exactFrameField). A message whose role that read cannot tell is no role message, as
		// the relay sends it, and is judged by its topic below; one encoding/json does not read
		// (frameIsJSON) is withheld there, as it was when the edge read the topic with
		// encoding/json.
		if _, _, err := exactFrameField(data, "role"); err != nil {
			return frameKeyWithheld(err)
		}
		if frame, err := readRoleFrame(data); err == nil && frame.role != "" {
			if roleOfProjects(frame.role, projects) {
				return framePasses()
			}
			return frameDrops(withheldProjectNotHeld)
		}
		if !named || !frameIsJSON(data) {
			return frameDrops(withheldProjectNotHeld)
		}
		if ownFrameAgentSubject(sessionID, projects, topic) {
			return framePasses()
		}
		if strings.HasPrefix(topic, contracts.GithubTopicPrefix) || strings.HasPrefix(topic, contracts.AgentTopicPrefix) {
			return frameDrops(withheldProjectNotHeld)
		}
		subject = topic
	}
	project, _, ok := contracts.SubjectProject(subject)
	if !ok || !slices.Contains(projects, project) {
		return frameDrops(withheldProjectNotHeld)
	}
	if named {
		if topicProject, _, ok := contracts.SubjectProject(topic); !ok || topicProject != project {
			return frameDrops(withheldProjectNotHeld)
		}
	}
	return framePasses()
}

// exactFrameTopic is the topic of the envelope that data holds, read by its exact top-level key
// (exactFrameField), and false when data holds no JSON object or its object names no topic. It
// answers errAmbiguousFrameKey for a frame whose topic readers disagree on: the session's plugin
// reads the last "topic" key (JSON.parse), and encoding/json, which the edge read the topic with
// before, the last key equal to "topic" ignoring case, so a later "Topic" key passed a frame under
// one topic that the session then read under another. It answers errPrototypeFrameKey for a frame
// with a top-level "__proto__" key, whose "topic" the plugin reads when the frame holds none.
func exactFrameTopic(data []byte) (string, bool, error) {
	return exactFrameField(data, "topic")
}

// frameByteOrderMark is the UTF-8 byte order mark a frame's JSON may start with. The plugins'
// transport decodes a frame's bytes with TextDecoder, which drops one, before JSON.parse reads
// them, so the edge reads a frame past it too.
var frameByteOrderMark = []byte("\xef\xbb\xbf")

// errAmbiguousFrameKey is exactFrameField's answer for a field that a frame names under more than
// one top-level key, or under a key that differs from the field's name only in case.
var errAmbiguousFrameKey = errors.New("a frame names the field under more than one key, or under a key that differs from its name in case")

// errPrototypeFrameKey is exactFrameField's answer for a frame with a top-level key that reads as
// "__proto__", however its escapes spell it. The plugin's envelope read
// (packages/envoy-client/src/delivery.ts, InboundEnvelopeSchema, a zod object with .passthrough())
// copies each key the schema does not name onto the envelope by assignment, so that key's value
// becomes the envelope's prototype, and every field the frame does not hold itself - the topic, the
// payload - is read through it: a field no reader of the frame's own keys can see.
var errPrototypeFrameKey = errors.New(`a frame holds a "__proto__" key, whose fields the plugin reads as the envelope's own`)

// frameIsJSON reports whether data, past one leading byte order mark, is JSON encoding/json reads
// (json.Valid). A frame it is not is either text JSON.parse refuses too, such as an object with
// text after it or a misspelt literal, or a document nested deeper than 10,000. The edge withholds
// such a frame on an agent subject unless the relay's read tells it a role message, which is
// judged by its role (readRoleFrame), and names no repository for such a GitHub event, as it did
// when it read both with encoding/json; on any other subject it judges such a frame by the topic
// exactFrameTopic reads, or by its subject when it names none, as it judged it then.
func frameIsJSON(data []byte) bool {
	return json.Valid(bytes.TrimPrefix(data, frameByteOrderMark))
}

// exactFrameField is the string value of the top-level field key of the JSON object that data
// holds past one leading byte order mark, as the plugins read a frame, and whether data names it
// (exactJSONString).
func exactFrameField(data []byte, key string) (string, bool, error) {
	return exactJSONString(bytes.TrimPrefix(data, frameByteOrderMark), key)
}

// exactJSONString is the string value of the top-level field key of the JSON object doc, read by
// its exact key as JSON.parse reads one, and whether doc names it: false for a doc that is no JSON
// object (topLevelJSONFields), that has no key, or whose value under it is null, empty or no
// string. A doc whose top-level keys hold key more than once, or hold a key that equals key
// ignoring case as encoding/json matches a key into a struct field (strings.EqualFold) and is not
// key, is errAmbiguousFrameKey whatever its values: JSON.parse takes the last key, encoding/json
// the last of any case, and another reader the first, so no read of such a field is the one
// every reader of the frame makes. A doc with a top-level "__proto__" key is errPrototypeFrameKey,
// whichever field is asked for.
func exactJSONString(doc []byte, key string) (string, bool, error) {
	var value []byte
	found, ambiguous, prototype := false, false, false
	object := topLevelJSONFields(doc, func(name string, raw []byte) {
		if name == "__proto__" {
			prototype = true
			return
		}
		if !strings.EqualFold(name, key) {
			return
		}
		if name != key || found {
			ambiguous = true
			return
		}
		found, value = true, raw
	})
	switch {
	case !object:
		return "", false, nil
	case prototype:
		return "", false, errPrototypeFrameKey
	case ambiguous:
		return "", false, errAmbiguousFrameKey
	case !found:
		return "", false, nil
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil || text == "" {
		return "", false, nil
	}
	return text, true, nil
}

// topLevelJSONFields calls visit with each top-level key of the JSON object doc, its escapes read,
// and the raw text of the key's value, in the order doc holds them, a repeated key each time, and
// reports whether doc reads as a JSON object: past JSON whitespace, an opening brace, then string
// keys, each followed by a colon and a value, separated by commas, up to the closing brace. It
// finds each value's end by its brackets and strings alone, with no limit on depth: encoding/json
// refuses a document nested deeper than 10,000 and JSON.parse does not, so a reader with that limit
// names no topic for a frame whose topic the plugin reads. It checks no more of a value than where
// it ends, and nothing after the closing brace, so it reads some documents JSON.parse refuses; the
// plugin reads no field of such a document, and the edge withholds it where frameIsJSON says.
func topLevelJSONFields(doc []byte, visit func(key string, value []byte)) bool {
	i := skipTopLevelJSONSpace(doc, 0)
	if i == len(doc) || doc[i] != '{' {
		return false
	}
	i = skipTopLevelJSONSpace(doc, i+1)
	if i < len(doc) && doc[i] == '}' {
		return true
	}
	for {
		if i == len(doc) || doc[i] != '"' {
			return false
		}
		keyEnd, ok := topLevelJSONStringEnd(doc, i)
		if !ok {
			return false
		}
		key := doc[i+1 : keyEnd-1]
		if bytes.IndexByte(key, '\\') >= 0 {
			var unescaped string
			if json.Unmarshal(doc[i:keyEnd], &unescaped) != nil {
				return false
			}
			key = []byte(unescaped)
		}
		i = skipTopLevelJSONSpace(doc, keyEnd)
		if i == len(doc) || doc[i] != ':' {
			return false
		}
		start := skipTopLevelJSONSpace(doc, i+1)
		end, ok := topLevelJSONValueEnd(doc, start)
		if !ok {
			return false
		}
		visit(string(key), doc[start:end])
		i = skipTopLevelJSONSpace(doc, end)
		if i == len(doc) {
			return false
		}
		switch doc[i] {
		case ',':
			i = skipTopLevelJSONSpace(doc, i+1)
		case '}':
			return true
		default:
			return false
		}
	}
}

// topLevelJSONValueEnd is the index just past the JSON value that starts at doc[i], and false when
// there is none: a string, an object or array found by counting its brackets outside strings, or a
// scalar that runs to the next comma, closing bracket or whitespace.
func topLevelJSONValueEnd(doc []byte, i int) (int, bool) {
	if i == len(doc) {
		return 0, false
	}
	switch doc[i] {
	case '"':
		return topLevelJSONStringEnd(doc, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(doc); j++ {
			switch doc[j] {
			case '"':
				end, ok := topLevelJSONStringEnd(doc, j)
				if !ok {
					return 0, false
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, true
				}
			}
		}
		return 0, false
	}
	j := i
	for j < len(doc) && strings.IndexByte(",}] \t\n\r", doc[j]) < 0 {
		j++
	}
	return j, j > i
}

// topLevelJSONStringEnd is the index just past the JSON string whose opening quote is doc[i], an
// escaped character skipped, and false when the string has no end.
func topLevelJSONStringEnd(doc []byte, i int) (int, bool) {
	for j := i + 1; j < len(doc); j++ {
		switch doc[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return 0, false
}

// skipTopLevelJSONSpace is the index of the first byte at or after i that is no JSON whitespace.
func skipTopLevelJSONSpace(doc []byte, i int) int {
	for i < len(doc) && strings.IndexByte(" \t\n\r", doc[i]) >= 0 {
		i++
	}
	return i
}

// ownFrameAgentSubject reports whether subject is sessionID's own agent subject in a form a
// message is stored on: ownAgentSubject's forms without the wildcard project, which no publisher
// stores a message under, so it is no one's here.
func ownFrameAgentSubject(sessionID string, projects []string, subject string) bool {
	return ownAgentSubject(sessionID, projects, subject) && subject != contracts.AgentTopicPrefix+"*."+sessionID
}

// edgeAdmissions is the projects of the token each session's latest registration on this replica
// was admitted on (subscribeHandler), the list the relay holds that session's frames to
// (sessionRelays.edgeWithholds). It is memory only and is never read back from a record: a session
// this replica admitted no registration of since it started has none here, and the relay holds its
// messages in its consumer until it registers through this replica (sessionRelays.relayNATS),
// which the plugin does at every heartbeat. An entry is forgotten once its session's record has
// left the session cache (prune): the map holds the sessions whose records the bucket holds and
// those admitted within the last edgeAdmissionGrace, each one session id and its token's list, at
// most oauthwire.MaxProjects keys.
type edgeAdmissions struct {
	mu       sync.Mutex
	admitted map[string]edgeAdmission
}

// edgeAdmission is the list of the token one session's latest registration on this replica was
// admitted on, and when this replica admitted it.
type edgeAdmission struct {
	projects []string
	at       time.Time
}

// edgeAdmissionGrace is how long an admission is kept whatever the session cache holds
// (edgeAdmissions.prune). A registration admits its session before it writes the session's entry
// (subscribeHandler), within sendRequestBudget, and the entry reaches the cache by the watch after
// that write; an admission younger than this may be one whose entry is still on its way, and is
// kept.
const edgeAdmissionGrace = relayPruneInterval

// admit records projects as the list of the token sessionID's registration was admitted on.
func (a *edgeAdmissions) admit(sessionID string, projects []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admitted == nil {
		a.admitted = map[string]edgeAdmission{}
	}
	a.admitted[sessionID] = edgeAdmission{projects: slices.Clone(projects), at: time.Now()}
}

// of is the list sessionID's latest admitted registration's token carried, and whether this
// replica admitted one.
func (a *edgeAdmissions) of(sessionID string) ([]string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	admission, admitted := a.admitted[sessionID]
	return admission.projects, admitted
}

// prune forgets each admission made before cutoff whose session gone reports has no record, so a
// replica keeps no list for a session that deregistered or lapsed. gone runs under a.mu, so it may
// not reach the admissions; the session cache's Get does not. A session that registers here again
// is admitted again.
func (a *edgeAdmissions) prune(cutoff time.Time, gone func(sessionID string) bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for sessionID, admission := range a.admitted {
		if admission.at.Before(cutoff) && gone(sessionID) {
			delete(a.admitted, sessionID)
		}
	}
}
