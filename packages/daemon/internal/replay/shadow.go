package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// shadow is a replay's copy of Dispatch (design 10.4): each issue's current payload as Dispatch
// would serve it, and a sequence of its own. It starts where the record stands at the window's
// start: every recorded event before the start is applied at its recorded time and its recorded
// sequence (seedEvent), so each issue's payload, asks and documents are what the record shows then,
// and its sequence is the one the start state's record of it stands at. Every event a replay
// applies from the start on — a recorded event at its replayed time, or the echo of a status write
// the replay's own outbox made — takes its issue's next sequence past that and carries the shadow's
// whole payload, so no sequence fence of the daemon drops it for arriving out of the record's
// order, and an echo that changes the status leaves the issue's links, parent, labels and rank as
// they stand. A recorded issue event changes the payload by what it changed in the record, read as
// the fields its payload differs in from the recorded event before it, so a person's edit lands on
// the replay's status rather than writing back the status the record's daemon had written. The
// daemon's reads of Dispatch — a boot's listing, a tick's re-read, a status write's read, a gate
// seed's read of a document — are answered from it.
type shadow struct {
	project string
	issues  map[string]*shadowIssue
	// ranks is the rank each issue is shown at in place of Dispatch's, under the branch's
	// FirstStarts (firstStartRanks), as the replay's first slots have moved them (rankWrite);
	// empty where the branch keeps Dispatch's ranks.
	ranks map[string]string
	// nextID is the id of the next event the replay makes itself (an echo), past every recorded
	// one, so each fact's id (dispatch-<id>) is the replay's own.
	nextID int64
}

// shadowIssue is one issue of the shadow.
type shadowIssue struct {
	key, project string
	// seq is the sequence of the issue's last event in the replay: the recorded sequence of its
	// newest event before the window's start (seedEvent), then one more for each event the replay
	// applies.
	seq int64
	// payload is the issue as Dispatch shows it now, nil before its first issue event.
	payload map[string]json.RawMessage
	// recorded is the payload of the issue's last recorded issue event, whoever wrote it, which
	// the next recorded issue event's change is read against.
	recorded map[string]json.RawMessage
	// log is every event of the issue the shadow holds, as applied: first the recorded events
	// before the window's start (seedEvent), the first seeded of them, then every event the replay
	// applied. A read of the issue's event log is answered from it (intake.LabelSetter), as
	// Dispatch's log holds the issue's events from before the start too.
	log    []json.RawMessage
	seeded int
	// asks is each ask of the issue as its events leave it, and artifacts each document.
	asks      map[string]*shadowAsk
	artifacts map[string]*shadowArtifact
}

// shadowAsk is one ask as its events leave it: whether it is open, whose turn it stands on, and
// the document an approval request asks a person to approve.
type shadowAsk struct {
	id, kind, artifact, waitingOn string
	open                          bool
}

// shadowArtifact is one document as its events leave it: its latest version, and each verdict on
// a version with the place of its event in the issue's log and its actor.
type shadowArtifact struct {
	latest   int
	verdicts []shadowVerdict
}

// shadowVerdict is one person's verdict on a version of a document.
type shadowVerdict struct {
	version  int
	approved bool
	reason   string
	mark     record.EventMark
	actor    dispatch.Actor
}

// echoIDBase is where the ids of the events a replay makes begin: above every id Dispatch gives
// a project's events in a window.
const echoIDBase = int64(1) << 40

// bookkeeping is the fields of an issue event's payload that every event changes and none of
// them changes by itself: they are written by the shadow for each event it applies.
var bookkeeping = map[string]bool{"last_seq": true, "updated_at": true}

// newShadow is the empty copy of project's Dispatch.
func newShadow(project string, ranks map[string]string) *shadow {
	return &shadow{project: project, issues: map[string]*shadowIssue{}, ranks: ranks, nextID: echoIDBase}
}

// issue is key's shadow, made where none is yet.
func (s *shadow) issue(key, project string) *shadowIssue {
	iss, ok := s.issues[key]
	if !ok {
		if project == "" {
			project, _, _ = strings.Cut(key, "-")
		}
		iss = &shadowIssue{key: key, project: project, asks: map[string]*shadowAsk{}, artifacts: map[string]*shadowArtifact{}}
		s.issues[key] = iss
	}
	return iss
}

// envelope is a Dispatch event read as the replay rewrites it: each top-level field, and the
// fields the shadow reads of it.
type envelope struct {
	fields  map[string]json.RawMessage
	key     string
	project string
	kind    string
	actor   dispatch.Actor
	payload map[string]json.RawMessage
}

// readEnvelope reads one recorded event, raw, whose envelope parsed already holds
// (ParseDispatchEvent): its top-level fields and its payload, each by name.
func readEnvelope(raw json.RawMessage, parsed DispatchEvent) (envelope, error) {
	e := envelope{key: parsed.Key, project: parsed.Project, kind: parsed.Type, actor: parsed.Actor}
	if err := json.Unmarshal(raw, &e.fields); err != nil {
		return envelope{}, fmt.Errorf("replay: read a recorded Dispatch event: %w", err)
	}
	if payload, ok := e.fields["payload"]; ok && !bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		if err := json.Unmarshal(payload, &e.payload); err != nil {
			return envelope{}, fmt.Errorf("replay: read the payload of a recorded %s event of %s: %w", e.kind, e.key, err)
		}
	}
	return e, nil
}

// issueEvent says whether kind is an event of an issue's own fields, which carries the whole
// issue as its payload.
func issueEvent(kind string) bool {
	return kind == "issue.created" || kind == "issue.updated" || kind == "issue.closed"
}

// daemonWrote says whether actor is the project's daemon, whose own writes a replay makes again
// from its own outbox rather than taking them from the record.
func daemonWrote(actor dispatch.Actor, key string) bool {
	return actor.Kind == "session" && actor.ID == dispatch.DaemonSession(key)
}

// note reads a recorded issue event of the daemon's own into the shadow without applying it: the
// next recorded event's change is read against it, and the shadow's payload is the replay's own.
func (s *shadow) note(e envelope) {
	if issueEvent(e.kind) && e.payload != nil {
		s.issue(e.key, e.project).recorded = e.payload
	}
}

// apply applies a recorded event at at: it takes its issue's next sequence, and an issue event
// changes the payload by the fields it changed in the record. It answers the event as the replay
// applies it, which the daemon's decoder reads.
func (s *shadow) apply(e envelope, at time.Time) (json.RawMessage, error) {
	iss := s.issue(e.key, e.project)
	iss.seq++
	return s.take(iss, e, at)
}

// seedEvent applies a recorded event from before the window's start at its recorded time at and
// its recorded sequence seq, not its issue's next, which brings the shadow to where the record
// stood at the start: each issue at the sequence of its newest event before it, where the start
// state writes its record's LastDispatchSeq, with the payload, asks and documents the record shows
// then. A write of the daemon's own is applied as any other, since the record's state at the start
// holds it. The event joins the issue's log ahead of every event the replay applies
// (shadowIssue.seeded): a replay seeds every event before its start before it applies its first.
func (s *shadow) seedEvent(e envelope, seq int64, at time.Time) error {
	iss := s.issue(e.key, e.project)
	iss.seq = seq
	if _, err := s.take(iss, e, at); err != nil {
		return err
	}
	iss.seeded = len(iss.log)
	return nil
}

// take applies e to iss at at, at the sequence iss stands at: an issue event changes the payload
// by the fields it changed in the record, and every event's asks and documents are read
// (notePayload). It answers the event as applied, which joins the issue's log.
func (s *shadow) take(iss *shadowIssue, e envelope, at time.Time) (json.RawMessage, error) {
	fields := maps.Clone(e.fields)
	if issueEvent(e.kind) && e.payload != nil {
		if iss.payload == nil {
			iss.payload = maps.Clone(e.payload)
		} else {
			for name, value := range e.payload {
				if bookkeeping[name] {
					continue
				}
				if before, had := iss.recorded[name]; !had || !sameJSON(before, value) {
					iss.payload[name] = value
				}
			}
		}
		iss.recorded = e.payload
		iss.stamp(at)
		payload, err := iss.render(s.ranks)
		if err != nil {
			return nil, err
		}
		fields["payload"] = payload
	}
	if err := s.notePayload(iss, e, at); err != nil {
		return nil, err
	}
	return iss.event(fields, at)
}

// stamp writes the shadow's bookkeeping fields for an event at at.
func (iss *shadowIssue) stamp(at time.Time) {
	iss.payload["last_seq"] = json.RawMessage(fmt.Sprint(iss.seq))
	if _, has := iss.payload["updated_at"]; has {
		iss.payload["updated_at"] = mustJSON(at)
	}
}

// render is the payload as the replay shows it: with the rank the branch shows the issue at.
func (iss *shadowIssue) render(ranks map[string]string) (json.RawMessage, error) {
	shown := iss.payload
	if rank, ok := ranks[iss.key]; ok {
		shown = maps.Clone(iss.payload)
		shown["rank"] = mustJSON(rank)
	}
	return json.Marshal(shown)
}

// event is fields, an event of iss, at its sequence and at at, and kept in its log.
func (iss *shadowIssue) event(fields map[string]json.RawMessage, at time.Time) (json.RawMessage, error) {
	fields["seq"] = json.RawMessage(fmt.Sprint(iss.seq))
	fields["created_at"] = mustJSON(at)
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("replay: write the event of %s at seq %d: %w", iss.key, iss.seq, err)
	}
	iss.log = append(iss.log, raw)
	return raw, nil
}

// notePayload reads what a non-issue event says of the issue's asks and documents, at the
// sequence the shadow gave it.
func (s *shadow) notePayload(iss *shadowIssue, e envelope, at time.Time) error {
	read := func(into any) error {
		raw, err := json.Marshal(e.payload)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, into)
	}
	switch e.kind {
	case "artifact.version":
		var payload struct {
			ArtifactID string `json:"artifact_id"`
			Version    struct {
				Number int `json:"number"`
			} `json:"version"`
		}
		if err := read(&payload); err != nil {
			return err
		}
		document := iss.document(payload.ArtifactID)
		document.latest = max(document.latest, payload.Version.Number)
		// A newer version leaves the approval request of the document with its agent.
		for _, ask := range iss.asks {
			if ask.artifact == payload.ArtifactID && ask.open {
				ask.waitingOn = "agent"
			}
		}
	case "artifact.approved", "artifact.changes_requested":
		var payload struct {
			ArtifactID string  `json:"artifact_id"`
			Version    int     `json:"version"`
			Reason     *string `json:"reason"`
		}
		if err := read(&payload); err != nil {
			return err
		}
		document := iss.document(payload.ArtifactID)
		document.latest = max(document.latest, payload.Version)
		verdict := shadowVerdict{version: payload.Version, approved: e.kind == "artifact.approved", mark: record.EventMark{Seq: iss.seq, At: at},
			actor: e.actor}
		if payload.Reason != nil {
			verdict.reason = *payload.Reason
		}
		document.verdicts = append(document.verdicts, verdict)
	case "ask.opened", "ask.answered", "ask.resolved", "ask.handed_back", "ask.edited":
		var payload struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			State    string `json:"state"`
			Approval *struct {
				ArtifactID       string `json:"artifact_id"`
				Version          int    `json:"version"`
				RequestedVersion int    `json:"requested_version"`
			} `json:"approval"`
		}
		if err := read(&payload); err != nil {
			return err
		}
		ask, known := iss.asks[payload.ID]
		if !known {
			ask = &shadowAsk{id: payload.ID}
			iss.asks[payload.ID] = ask
		}
		if payload.Kind != "" {
			ask.kind = payload.Kind
		}
		if payload.Approval != nil && payload.Approval.ArtifactID != "" {
			ask.artifact = payload.Approval.ArtifactID
		}
		switch e.kind {
		case "ask.opened", "ask.handed_back":
			ask.open, ask.waitingOn = true, "human"
		case "ask.answered", "ask.resolved":
			ask.open = false
		case "ask.edited":
			if payload.Approval != nil && payload.Approval.RequestedVersion < payload.Approval.Version {
				ask.waitingOn = "agent"
			}
		}
	case "comment.created", "comment.answered":
		var payload struct {
			AskID        string `json:"ask_id"`
			AskWaitingOn string `json:"ask_waiting_on"`
		}
		if err := read(&payload); err != nil {
			return err
		}
		if ask, known := iss.asks[payload.AskID]; known && payload.AskWaitingOn != "" {
			ask.waitingOn = payload.AskWaitingOn
		}
	}
	return nil
}

// document is iss's document artifact, made where none is yet.
func (iss *shadowIssue) document(artifact string) *shadowArtifact {
	document, ok := iss.artifacts[artifact]
	if !ok {
		document = &shadowArtifact{}
		iss.artifacts[artifact] = document
	}
	return document
}

// statusWrite is the event the daemon's write of key's status makes in Dispatch, at at: the
// shadow's payload at status, with the legion label added for a hand-over (handOver), by the
// daemon's own session, at the issue's next sequence. It answers false where the shadow holds no
// payload of the issue to write over, which Dispatch would refuse.
func (s *shadow) statusWrite(key, status string, handOver bool, at time.Time) (json.RawMessage, bool, error) {
	iss, ok := s.issues[key]
	if !ok || iss.payload == nil {
		return nil, false, nil
	}
	iss.seq++
	iss.payload["status"] = mustJSON(status)
	if _, has := iss.payload["closed_at"]; has || status == record.DoneStatus {
		if status == record.DoneStatus {
			iss.payload["closed_at"] = mustJSON(at)
		} else {
			iss.payload["closed_at"] = json.RawMessage("null")
		}
	}
	if handOver {
		labels := iss.labels()
		if !dispatch.CarriesLegionLabel(labels) {
			iss.payload["labels"] = mustJSON(append(labels, dispatch.LegionLabel))
		}
	}
	iss.stamp(at)
	payload, err := iss.render(s.ranks)
	if err != nil {
		return nil, false, err
	}
	kind := "issue.updated"
	if status == record.DoneStatus {
		kind = "issue.closed"
	}
	raw, err := s.made(iss, kind, jsonActor{Kind: "session", ID: dispatch.DaemonSession(key)}, payload, at)
	return raw, err == nil, err
}

// rankActor is the actor of the replay's own moves of a rank (rankWrite): the replay itself, as
// Dispatch names itself (system), since no person, session or daemon of the record made them.
var rankActor = jsonActor{Kind: "system", ID: "replay-first-starts"}

// rankWrite moves the rank the replay shows key at to rank, or to Dispatch's own where shown is
// false (FirstStarts recorded, firstStarts.rootRank), and answers the event the move makes in
// Dispatch at at: an issue.updated of the shadow's payload at the new rank, whose changed fields
// are the rank, by rankActor, at the issue's next sequence. It answers false where the rank shown
// does not move, and where the shadow holds no payload of the issue to write over, whose first
// event then carries the rank. No recorded event is this one: the record's own payload, which the
// next recorded event's change is read against, stays as it was.
func (s *shadow) rankWrite(key, rank string, shown bool, at time.Time) (json.RawMessage, bool, error) {
	if before, had := s.ranks[key]; had == shown && before == rank {
		return nil, false, nil
	}
	if shown {
		s.ranks[key] = rank
	} else {
		delete(s.ranks, key)
	}
	iss, ok := s.issues[key]
	if !ok || iss.payload == nil {
		return nil, false, nil
	}
	iss.seq++
	iss.stamp(at)
	moved := maps.Clone(iss.payload)
	if shown {
		moved["rank"] = mustJSON(rank)
	}
	moved["changed"] = mustJSON([]string{"rank"})
	payload, err := json.Marshal(moved)
	if err != nil {
		return nil, false, err
	}
	raw, err := s.made(iss, "issue.updated", rankActor, payload, at)
	return raw, err == nil, err
}

// made is an event the replay makes itself of iss at at, past every recorded one: of kind, by
// actor, carrying payload, at the sequence iss stands at, and kept in its log.
func (s *shadow) made(iss *shadowIssue, kind string, actor jsonActor, payload json.RawMessage, at time.Time) (json.RawMessage, error) {
	s.nextID++
	return iss.event(map[string]json.RawMessage{
		"id": json.RawMessage(fmt.Sprint(s.nextID)), "issue_key": mustJSON(iss.key), "artifact_id": json.RawMessage("null"),
		"project": mustJSON(iss.project), "type": mustJSON(kind), "actor": mustJSON(actor), "notify": json.RawMessage("true"),
		"payload": payload,
	}, at)
}

// jsonActor is a Dispatch event's actor as Dispatch writes it.
type jsonActor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// status is the issue's status as the shadow holds it, "" for an issue it holds no payload of.
func (iss *shadowIssue) status() string { return iss.str("status") }

// str is the string field name of the payload, "" where it is absent or null.
func (iss *shadowIssue) str(name string) string {
	var value *string
	if raw, ok := iss.payload[name]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	if value == nil {
		return ""
	}
	return *value
}

// strs is the list field name of the payload, nil where it is absent or null.
func (iss *shadowIssue) strs(name string) []string {
	var value []string
	if raw, ok := iss.payload[name]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	return value
}

// labels, parent and blockers are the payload's.
func (iss *shadowIssue) labels() []string   { return iss.strs("labels") }
func (iss *shadowIssue) parent() string     { return iss.str("parent") }
func (iss *shadowIssue) blockers() []string { return iss.strs("blocked_by") }

// rank is the rank the replay shows the issue at.
func (s *shadow) rank(iss *shadowIssue) string {
	if rank, ok := s.ranks[iss.key]; ok {
		return rank
	}
	return iss.str("rank")
}

// claimedBy is who the payload shows holding the issue's Dispatch claim (`<kind>:<id>`, "" for
// nobody), and whether it shows the claim at all.
func (iss *shadowIssue) claimedBy() (string, bool) {
	raw, ok := iss.payload["claim"]
	if !ok {
		return "", false
	}
	var held *struct {
		Actor *dispatch.Actor `json:"actor"`
	}
	if err := json.Unmarshal(raw, &held); err != nil {
		return "", false
	}
	if held == nil {
		return "", true
	}
	if held.Actor == nil {
		return "", false
	}
	return held.Actor.Kind + ":" + held.Actor.ID, true
}

// summary is the issue as a boot's listing shows it (dispatch.IssueSummary), at the rank the
// replay shows it at.
func (s *shadow) summary(iss *shadowIssue) dispatch.IssueSummary {
	var parent *string
	if p := iss.parent(); p != "" {
		parent = &p
	}
	claimed, known := iss.claimedBy()
	labels := iss.labels()
	return dispatch.IssueSummary{Key: iss.key, Title: iss.str("title"), Status: iss.status(), Parent: parent, Rank: s.rank(iss),
		HandedOver: dispatch.CarriesLegionLabel(labels), Labels: labels, LastSeq: iss.seq, ResolvedRepo: iss.str("resolved_repo"),
		BlockedBy: iss.blockers(), ClaimedBy: claimed, ClaimUnread: !known}
}

// observed is the issue as a tick's re-read of it shows it (intake.IssueStatus).
func (s *shadow) observed(iss *shadowIssue) intake.IssueStatus {
	claimed, known := iss.claimedBy()
	return intake.IssueStatus{Key: iss.key, Project: iss.project, Status: iss.status(), Blockers: iss.blockers(), Seq: iss.seq,
		ClaimedBy: claimed, ClaimKnown: known, Parent: iss.parent(), Labels: iss.labels(), Rank: s.rank(iss)}
}

// listing is every issue of the project the shadow holds a payload of, as a boot's listing shows
// them, by key.
func (s *shadow) listing() []dispatch.IssueSummary {
	var listed []dispatch.IssueSummary
	for _, key := range slices.Sorted(maps.Keys(s.issues)) {
		if iss := s.issues[key]; iss.payload != nil && iss.project == s.project {
			listed = append(listed, s.summary(iss))
		}
	}
	return listed
}

// chain is key's Dispatch parent chain as the shadow shows it, from parent up to the top, stopping
// at a repeated key or back at key: the chain the daemon's preparation reads (Ancestry).
func (s *shadow) chain(key, parent string) []string {
	var chain []string
	for ancestor := parent; ancestor != "" && ancestor != key && !slices.Contains(chain, ancestor); {
		chain = append(chain, ancestor)
		next := ""
		if iss, ok := s.issues[ancestor]; ok {
			next = iss.parent()
		}
		ancestor = next
	}
	return chain
}

// approval is what Dispatch's read of a document's approval answers from the shadow
// (dispatch.Approval): its latest version, the verdict on that version, and the approval request
// open on it and whose turn it stands on.
type approval struct {
	latest          int
	approved        *shadowVerdict
	changes         *shadowVerdict
	ask, waitingOn  string
	latestReviewer  dispatch.Actor
	latestReviewSeq record.EventMark
}

// approvalOf is the approval of artifact on key as the shadow holds it.
func (s *shadow) approvalOf(key, artifact string) approval {
	var read approval
	iss, ok := s.issues[key]
	if !ok {
		return read
	}
	if document, ok := iss.artifacts[artifact]; ok {
		read.latest = document.latest
		for i := range document.verdicts {
			verdict := &document.verdicts[i]
			if verdict.version != document.latest {
				continue
			}
			read.approved, read.changes = nil, nil
			if verdict.approved {
				read.approved = verdict
			} else {
				read.changes = verdict
			}
			read.latestReviewer, read.latestReviewSeq = verdict.actor, verdict.mark
		}
	}
	for _, id := range slices.Sorted(maps.Keys(iss.asks)) {
		if ask := iss.asks[id]; ask.open && ask.artifact == artifact {
			read.ask, read.waitingOn = ask.id, ask.waitingOn
		}
	}
	return read
}

// approvedBefore is the highest version below version of the document artifact that a person's
// verdict the shadow holds approved, 0 where none did: Dispatch's answer to the daemon's read of
// the document's review history (dispatch.Client.ApprovedVersionBefore), which takes the highest
// such version wherever it stands, since a changes request approves none and a review of a version
// at or above the one asked about is not before it. The shadow holds the verdicts of every event
// of the window and of its reach before the start (seedEvent), on whichever issue the document is
// recorded, so a document none of those events names answers 0, as a history with no approval in
// it does.
func (s *shadow) approvedBefore(artifact string, version int) int {
	approved := 0
	for _, iss := range s.issues {
		document, ok := iss.artifacts[artifact]
		if !ok {
			continue
		}
		for _, verdict := range document.verdicts {
			if verdict.approved && verdict.version < version && verdict.version > approved {
				approved = verdict.version
			}
		}
	}
	return approved
}

// sameJSON says whether a and b are one JSON value written alike, spaces aside.
func sameJSON(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	if json.Compact(&x, a) != nil || json.Compact(&y, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(x.Bytes(), y.Bytes())
}

// mustJSON is v as JSON, for a value that always marshals.
func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
