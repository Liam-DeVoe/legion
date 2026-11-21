package replay

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// inFlightStep is how many more issues in flight multiply a work segment's length by the slowdown
// factor once (spec R3): a segment that starts with n issues in flight in a replay and m in the
// record takes its recorded length times f^((n − m)/inFlightStep).
const inFlightStep = 42

// slowed is a work segment's length in a replay (spec R3, assumption 1): its recorded length times
// f to the power (n − m)/42, n the issues in flight in the replay at the segment's start and m in
// the record at the record's start of it. At n = m, or at f = 1, it keeps its recorded length.
func slowed(recorded time.Duration, f float64, n, m int) time.Duration {
	if n == m || f == 1 {
		return recorded
	}
	return time.Duration(math.Round(float64(recorded) * math.Pow(f, float64(n-m)/inFlightStep)))
}

// pieceOpen is what opens a piece of an issue's work in a replay.
type pieceOpen int

const (
	// opensOnStart is a piece the start row of its phase's role opens: the stub takes a start
	// whose entry is above the one its claim was given.
	opensOnStart pieceOpen = iota
	// opensOnRetry is the rest of a visit after a hold: the start the hold's release writes
	// (workflow's restart, a resend of the entry) opens it.
	opensOnRetry
	// opensOnPhase is a piece no start row opens, its phase's role being the architect that owns
	// the issue (accepting): the move into its phase opens it (design 10, "the accepting segment").
	opensOnPhase
	// opensAtSeed is a piece under way at the window's start, which the start state's claims
	// stand in: it runs at its recorded times.
	opensAtSeed
)

// pieceEnd is how a piece of work ends.
type pieceEnd int

const (
	// endMove is a piece the recorded move of its visit ends, which the replay enters at its end
	// (MoveSources).
	endMove pieceEnd = iota
	// endHeldSignOff is an accepting piece whose sign-off the engine held behind a member beneath
	// (SegmentEndHeldSignOff): the sign-off is entered at its end, and again at each leave of a
	// member beneath while it is refused.
	endHeldSignOff
	// endFailure is a piece its role's failed claim ends: the replay enters the claim's failure
	// (intake.ClaimFailed) at its end, which holds the issue.
	endFailure
	// endPass is a piece whose visit the admission pass ended (gate_opened): its worker's work
	// ended with its registration or its last turn, and the replay enters no move.
	endPass
	// endLeft is a piece the issue's leaving the workflow cut, and endOpen one the record shows
	// unended at the window's end; neither has an end the replay enters.
	endLeft
	endOpen
)

// pieceState is where a piece stands in a replay.
type pieceState int

const (
	pieceWaiting pieceState = iota
	pieceRunning
	piecePaused
	pieceEnded
	pieceCancelled
	pieceSkipped
)

// piece is one stretch of an issue's work as the record shows it and as a replay runs it: a visit's
// work segment (Visit.Segment), or the part of one before or after a hold, with every act of the
// issue's workers that falls in it at its offset from its start.
type piece struct {
	issue string
	index int
	visit *Visit
	phase phase.Phase
	role  claim.Role
	opens pieceOpen
	// start and end are the record's, end zero for a piece the record shows unended; ends is how
	// it ends, and hold the hold whose claim failure ends it (endFailure) or whose release opens
	// it (opensOnRetry).
	start, end time.Time
	ends       pieceEnd
	hold       *Wait
	// m is the issues in flight in the record at its start.
	m int
	// acts is every act of the piece, by its offset from the start.
	acts []act

	// The replay's own: where the piece stands, when it started, its length, when it was paused,
	// and the items it scheduled that have not run.
	state    pieceState
	at       time.Time
	n        int
	length   time.Duration
	pausedAt time.Time
	pending  []*item
	// turns is each role's turn under way in the piece, which a pause ends and a resume begins
	// again.
	turns map[claim.Role]bool
}

// recorded is the piece's recorded length, zero for one the record shows unended.
func (p *piece) recorded() time.Duration {
	if p.end.IsZero() {
		return 0
	}
	return p.end.Sub(p.start)
}

// actKind is one kind of act inside a piece.
type actKind int

const (
	actTurnStart actKind = iota
	actTurnEnd
	// actEvent is a Dispatch event a worker of the issue made (its session the actor), which
	// moves with the work it was made in: a child filed, a document's version written, an ask
	// opened, a link set.
	actEvent
	// actRegistration is the architect's registration of a design document
	// (intake.GateRegistered, from its `workflow: design gate registered` line).
	actRegistration
	// actClaimFailed is the failure of a claim of the issue whose role does not work the phase
	// it stands in, which holds nothing and tells its architect.
	actClaimFailed
	// actAdoption is the architect's release of queued roots into its tree (intake.ChildAdoption).
	actAdoption
	// actCloseRoot is the architect's close of its root before the first phase
	// (intake.CloseRoot).
	actCloseRoot
	// actReady is the merger's ready mark of the release at 9542c6fb inside merging
	// (ReadyMark, assumption 20).
	actReady
)

// act is one act inside a piece: its kind, its offset from the piece's start, the record's moment
// and order of it, and what it is of.
type act struct {
	kind   actKind
	offset time.Duration
	rec    time.Time
	order  int
	role   claim.Role
	turn   *Turn
	event  int
	gate   GateFact
	failed ClaimFailed
	joined []RootJoined
}

// script is a replay's reading of the record's work: each issue's pieces in order, every
// recorded Dispatch event with the anchor that places it, and the record's issues in flight at any
// moment.
type script struct {
	pieces map[string][]*piece
	events []scriptEvent
	// flight is each issue's stretches in flight in the record (IssueTrace.inFlight), and inFlight
	// the count of them at any moment, each issue's own joined first (joinedStretches), so an
	// issue counts once.
	flight   map[string][][2]time.Time
	inFlight stretchCount
}

// anchorKind is what places an event of the record in a replay.
type anchorKind int

const (
	// anchorTime places the event at its recorded time.
	anchorTime anchorKind = iota
	// anchorPiece places it at its offset in a piece of its actor's work (actEvent).
	anchorPiece
	// anchorEvent places it at its offset from another event the replay places first: a
	// person's answer from the question it answers (Branch.Answer).
	anchorEvent
	// anchorWait places it at its offset from the replay's start of the wait it ended in the
	// record (Branch.Timing).
	anchorWait
)

// scriptEvent is one recorded Dispatch event, read once (ParseDispatchEvent, and its envelope as
// the replay rewrites it), and how the replay places it.
type scriptEvent struct {
	parsed DispatchEvent
	env    envelope
	at     time.Time
	order  int
	anchor anchorKind
	// on is the event an anchorEvent is placed from, and offset the event's own distance from
	// it; wait is the wait an anchorWait ended in the record.
	on     int
	offset time.Duration
	wait   waitAnchor
	// dependents is every event placed from this one.
	dependents []int
	// placed is whether the replay has placed the event.
	placed bool
}

// waitAnchor is the wait an event ended in the record: its waiting issue, the kind, and the
// record's start of it; the replay's start of the same wait places the event (Timing offset).
type waitAnchor struct {
	issue string
	kind  WaitKind
	start time.Time
	// added is the event that added the link a link wait stands on, and blocker the issue it
	// names.
	added   int
	blocker string
}

// inFlightAt is how many issues the record holds in flight at at.
func (s *script) inFlightAt(at time.Time) int {
	return s.inFlight.at(at)
}

// joinedStretches is the moments stretches hold, as withinStretches reads them, as stretches that
// neither meet nor overlap, in order: a stretch that holds no moment is left out, and stretches
// that meet or overlap are joined into one.
func joinedStretches(stretches [][2]time.Time) [][2]time.Time {
	var held [][2]time.Time
	for _, stretch := range stretches {
		if stretch[1].IsZero() || stretch[0].Before(stretch[1]) {
			held = append(held, stretch)
		}
	}
	slices.SortFunc(held, func(x, y [2]time.Time) int { return x[0].Compare(y[0]) })
	var joined [][2]time.Time
	for _, stretch := range held {
		n := len(joined)
		if n == 0 || !joined[n-1][1].IsZero() && stretch[0].After(joined[n-1][1]) {
			joined = append(joined, stretch)
			continue
		}
		if end := joined[n-1][1]; !end.IsZero() && (stretch[1].IsZero() || stretch[1].After(end)) {
			joined[n-1][1] = stretch[1]
		}
	}
	return joined
}

// buildScript reads t into the pieces of its issues' work and the placement of its events, from
// the window's start on. order is the record's order of each line and event the acts and events
// come from, so acts of one instant run in the record's order.
func buildScript(t Trace, w Window, lines recordLines) (script, error) {
	s := script{pieces: map[string][]*piece{}, flight: map[string][][2]time.Time{}}
	var flying [][2]time.Time
	for _, key := range slices.Sorted(maps.Keys(t.Issues)) {
		iss := t.Issues[key]
		s.flight[key] = iss.inFlight()
		flying = append(flying, joinedStretches(s.flight[key])...)
		if iss.Project != t.Project {
			continue
		}
		s.pieces[key] = issuePieces(t, iss)
	}
	s.inFlight = countStretches(flying)
	for _, pieces := range s.pieces {
		for _, p := range pieces {
			p.m = s.inFlightAt(p.start)
		}
	}
	s.placeTurns(t)
	s.placeLines(t, lines)
	if err := s.placeEvents(t, w); err != nil {
		return script{}, err
	}
	for _, pieces := range s.pieces {
		for _, p := range pieces {
			slices.SortStableFunc(p.acts, func(x, y act) int {
				return cmp.Or(cmp.Compare(x.offset, y.offset), x.rec.Compare(y.rec), cmp.Compare(x.order, y.order))
			})
		}
	}
	return s, nil
}

// issuePieces is iss's pieces of work that end at or after the window's start, in order: each
// visit's segment, cut at each hold of its role's claim inside it.
func issuePieces(t Trace, iss *IssueTrace) []*piece {
	var pieces []*piece
	for i := range iss.Visits {
		visit := &iss.Visits[i]
		segment := visit.Segment
		if segment == nil {
			continue
		}
		role := phaseRole(visit.Phase)
		opens := opensOnStart
		if visit.Phase == phase.Accepting {
			opens = opensOnPhase
		}
		until := segment.End
		if until.IsZero() {
			until = visit.Exit
		}
		start := segment.Start
		var holds []*Wait
		for j := range iss.Waits {
			hold := &iss.Waits[j]
			if hold.Kind == WaitHeld && !hold.Start.Before(start) && (until.IsZero() || hold.Start.Before(until)) {
				holds = append(holds, hold)
			}
		}
		released := true
		for _, hold := range holds {
			pieces = append(pieces, &piece{issue: iss.Key, visit: visit, phase: visit.Phase, role: role, opens: opens, start: start,
				end: hold.Start, ends: endFailure, hold: hold})
			if hold.EndedBy != WaitReleased {
				released = false
				break
			}
			start, opens = hold.End, opensOnRetry
		}
		if !released {
			continue
		}
		last := &piece{issue: iss.Key, visit: visit, phase: visit.Phase, role: role, opens: opens, start: start, end: segment.End}
		if len(holds) > 0 {
			last.hold = holds[len(holds)-1]
		}
		switch segment.EndedBy {
		case SegmentEndMove:
			last.ends = endMove
		case SegmentEndHeldSignOff:
			last.ends = endHeldSignOff
		case SegmentEndRegistration, SegmentEndTurn:
			last.ends = endPass
		case SegmentEndLeft:
			last.ends, last.end = endLeft, time.Time{}
		default:
			last.ends, last.end = endOpen, time.Time{}
		}
		pieces = append(pieces, last)
	}
	var kept []*piece
	for _, p := range pieces {
		if !p.end.IsZero() && p.end.Before(t.From) {
			continue
		}
		if p.start.Before(t.From) {
			p.opens = opensAtSeed
		}
		p.index = len(kept)
		kept = append(kept, p)
	}
	return kept
}

// pieceAt is the piece of key's work an act at at belongs to: the latest whose start is before
// it, or, where none is, the first that starts at it; with starting set, the latest that starts
// at or before it, as a turn that begins as a piece begins is that piece's. Nil for none.
func (s *script) pieceAt(key string, at time.Time, starting bool) *piece {
	var found *piece
	for _, p := range s.pieces[key] {
		if p.start.Before(at) || starting && p.start.Equal(at) {
			found = p
		}
	}
	if found == nil {
		for _, p := range s.pieces[key] {
			if p.start.Equal(at) {
				return p
			}
		}
	}
	return found
}

// placeTurns puts each turn of each issue's workers into the piece it falls in, its start and its
// end at their offsets from the piece's start.
func (s *script) placeTurns(t Trace) {
	for _, key := range slices.Sorted(maps.Keys(t.Issues)) {
		iss := t.Issues[key]
		for i := range iss.Turns {
			turn := &iss.Turns[i]
			if !turn.End.IsZero() && turn.End.Before(t.From) {
				continue
			}
			p := s.pieceAt(key, turn.Start, true)
			if p == nil {
				continue
			}
			p.acts = append(p.acts, act{kind: actTurnStart, offset: turn.Start.Sub(p.start), rec: turn.Start, role: turn.Claim.Role, turn: turn})
			if !turn.End.IsZero() {
				p.acts = append(p.acts, act{kind: actTurnEnd, offset: turn.End.Sub(p.start), rec: turn.End, role: turn.Claim.Role, turn: turn})
			}
		}
	}
}

// placeLines puts each recorded act of an architect or a worker that a line of the record holds
// into the piece of its issue it falls in: each registration of a design document, each failure of
// a claim that holds nothing, each release of queued roots into a tree, and each close of a root by
// its architect.
func (s *script) placeLines(t Trace, lines recordLines) {
	for _, key := range slices.Sorted(maps.Keys(t.Issues)) {
		for _, gate := range t.Issues[key].Gates {
			if gate.Kind != GateRegisteredFact || gate.At.Before(t.From) {
				continue
			}
			if p := s.pieceAt(key, gate.At, false); p != nil {
				p.acts = append(p.acts, act{kind: actRegistration, offset: gate.At.Sub(p.start), rec: gate.At, order: 1, gate: gate})
			}
		}
	}
	for _, failed := range lines.failed {
		key := failed.Claim.Issue
		if failed.Time.Before(t.From) || key == "" || s.endsAPiece(key, failed) {
			continue
		}
		if p := s.pieceAt(key, failed.Time, false); p != nil {
			p.acts = append(p.acts, act{kind: actClaimFailed, offset: failed.Time.Sub(p.start), rec: failed.Time, order: 1, failed: failed})
		}
	}
	joined := map[string]map[time.Time][]RootJoined{}
	for _, line := range lines.joined {
		if line.Time.Before(t.From) {
			continue
		}
		if joined[line.Tree] == nil {
			joined[line.Tree] = map[time.Time][]RootJoined{}
		}
		joined[line.Tree][line.Time] = append(joined[line.Tree][line.Time], line)
	}
	for _, tree := range slices.Sorted(maps.Keys(joined)) {
		for _, at := range slices.SortedFunc(maps.Keys(joined[tree]), time.Time.Compare) {
			if p := s.pieceAt(tree, at, false); p != nil {
				p.acts = append(p.acts, act{kind: actAdoption, offset: at.Sub(p.start), rec: at, order: 1, joined: joined[tree][at]})
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(t.Issues)) {
		for _, at := range t.Issues[key].Ready {
			if at.Before(t.From) {
				continue
			}
			if p := s.pieceAt(key, at, false); p != nil {
				p.acts = append(p.acts, act{kind: actReady, offset: at.Sub(p.start), rec: at, order: 1})
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(t.Issues)) {
		iss := t.Issues[key]
		for _, leave := range iss.Leaves {
			if leave.At.Before(t.From) || leave.Status != record.DoneStatus || !closedByArchitect(iss, leave) {
				continue
			}
			if p := s.pieceAt(key, leave.At, false); p != nil {
				p.acts = append(p.acts, act{kind: actCloseRoot, offset: leave.At.Sub(p.start), rec: leave.At, order: 1})
			}
		}
	}
}

// endsAPiece says whether failed, a claim failure of key, is the one that ends a piece of key's
// work (endFailure), which the piece's end enters rather than an act of it.
func (s *script) endsAPiece(key string, failed ClaimFailed) bool {
	return slices.ContainsFunc(s.pieces[key], func(p *piece) bool {
		return p.ends == endFailure && p.end.Equal(failed.Time) && p.role == failed.Claim.Role
	})
}

// closedByArchitect says whether leave, iss's leaving the workflow at done, is its architect's
// close of the root before its first phase (close_root): a root that leaves from admitted or from
// a wait on a person with no move to done, and with no event of a person or a session of its
// own making it done at that moment, which the daemon's own close writes instead.
func closedByArchitect(iss *IssueTrace, leave Leave) bool {
	entry := iss.entryAt(leave.At)
	if entry == nil || entry.Tree != iss.Key {
		return false
	}
	for _, visit := range iss.Visits {
		if visit.Exit.Equal(leave.At) && (!visit.Left || visit.Phase != phase.Admitted) {
			return false
		}
	}
	for _, event := range iss.Dispatch {
		if event.At.Equal(leave.At) && event.Issue != nil && event.Issue.Status == record.DoneStatus && !daemonWrote(event.Actor, iss.Key) {
			return false
		}
	}
	return true
}

// placeEvents reads every recorded event of the window at or after its start, and says how a
// replay places it: a worker's event at its offset in the piece of its issue's work it falls in;
// a person's answer from its question (Branch.Answer); an event that ended a wait in the record
// from the replay's start of that wait (Branch.Timing); every other at its recorded time.
func (s *script) placeEvents(t Trace, w Window) error {
	for _, key := range slices.Sorted(maps.Keys(w.Events)) {
		for _, raw := range w.Events[key] {
			parsed, err := ParseDispatchEvent(raw)
			if err != nil {
				return err
			}
			env, err := readEnvelope(raw, parsed)
			if err != nil {
				return err
			}
			s.events = append(s.events, scriptEvent{parsed: parsed, env: env, at: parsed.At, order: int(parsed.ID), on: -1})
		}
	}
	slices.SortStableFunc(s.events, func(x, y scriptEvent) int { return cmp.Or(x.at.Compare(y.at), cmp.Compare(x.order, y.order)) })
	ended := s.waitEnders(t)
	questions := s.questions()
	for i := range s.events {
		event := &s.events[i]
		if event.at.Before(t.From) {
			continue
		}
		if ref, err := claimRefOf(claimTokenOf(event.env.actor)); err == nil && ref.Issue != "" {
			if p := s.pieceAt(ref.Issue, event.at, false); p != nil {
				event.anchor = anchorPiece
				p.acts = append(p.acts, act{kind: actEvent, offset: event.at.Sub(p.start), rec: event.at, event: i})
				continue
			}
		}
		if question, ok := questions[i]; ok && !s.events[question].at.Before(t.From) {
			event.anchor, event.on, event.offset = anchorEvent, question, event.at.Sub(s.events[question].at)
			s.events[question].dependents = append(s.events[question].dependents, i)
			continue
		}
		if wait, ok := ended[i]; ok && !wait.start.Before(t.From) {
			event.anchor, event.wait, event.offset = anchorWait, wait, event.at.Sub(wait.start)
		}
	}
	return nil
}

// claimTokenOf is the claim token a session actor names, "" for an actor of another kind.
func claimTokenOf(actor dispatch.Actor) claim.Token {
	if actor.Kind != "session" {
		return ""
	}
	return claim.Token(actor.ID)
}

// questions is, by the index of each person's answer among the events, the event it answers: the
// opening of the ask an ask's close, hand-back or comment is of, or the newest version event of
// the document a person's verdict is on, at or before the verdict — the event that stands for the
// registration of the version the person reviews, since the registration itself is a line of the
// record. It reads the events once, in order, keeping for each issue the newest opening of each
// ask and the newest version event of each version of each document.
func (s *script) questions() map[int]int {
	type askOf struct{ issue, ask string }
	type versionOf struct {
		issue, artifact string
		number          int
	}
	opened := map[askOf]int{}
	written := map[versionOf]int{}
	found := map[int]int{}
	for i := range s.events {
		event := &s.events[i]
		key := event.parsed.Key
		if ask, artifact, version := answerOf(event); ask != "" {
			if j, ok := opened[askOf{key, ask}]; ok {
				found[i] = j
			}
		} else if artifact != "" {
			if j, ok := written[versionOf{key, artifact, version}]; ok {
				found[i] = j
			}
		}
		switch event.parsed.Type {
		case "ask.opened":
			opened[askOf{key, event.parsed.Ask.ID}] = i
		case "artifact.version":
			if artifact, number := versionWritten(event); artifact != "" {
				written[versionOf{key, artifact, number}] = i
			}
		}
	}
	return found
}

// answerOf is what event answers where it is a person's: the ask an ask's close or hand-back or a
// comment is of, or the document and version a verdict is on; all empty for any other event.
func answerOf(event *scriptEvent) (ask, artifact string, version int) {
	parsed := event.parsed
	if parsed.Actor.Kind != "user" {
		return "", "", 0
	}
	switch {
	case parsed.Ask != nil && parsed.Type != "ask.opened":
		return parsed.Ask.ID, "", 0
	case parsed.Verdict != nil:
		return "", parsed.Verdict.Artifact, parsed.Verdict.Version
	case parsed.Type == "comment.created" || parsed.Type == "comment.answered":
		var payload struct {
			AskID string `json:"ask_id"`
		}
		if decodeField(event.env.payload, &payload) {
			return payload.AskID, "", 0
		}
	}
	return "", "", 0
}

// versionWritten is the document event, an artifact.version event, writes a version of and the
// version's number; "" for a payload that names no document.
func versionWritten(event *scriptEvent) (string, int) {
	var payload struct {
		ArtifactID string `json:"artifact_id"`
		Version    struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	if !decodeField(event.env.payload, &payload) {
		return "", 0
	}
	return payload.ArtifactID, payload.Version.Number
}

// waitEnders is, by the index of the event, each event that ended a wait in the record: the last
// unfinished link taken off (link_removed, the waiting issue's own event), an outside blocker's
// close (blocker_done of an issue Legion does not work), and the last open child moved out from
// under its parent (child_moved_out, the child's event), each with the wait it ended.
func (s *script) waitEnders(t Trace) map[int]waitAnchor {
	ended := map[int]waitAnchor{}
	// own is each issue's issue events, by their index, in order.
	own := map[string][]int{}
	for i := range s.events {
		if event := &s.events[i]; issueEvent(event.env.kind) {
			own[event.env.key] = append(own[event.env.key], i)
		}
	}
	find := func(key string, at time.Time) int {
		events := own[key]
		j, found := slices.BinarySearchFunc(events, at, func(i int, at time.Time) int { return s.events[i].at.Compare(at) })
		if !found {
			return -1
		}
		return events[j]
	}
	for _, key := range slices.Sorted(maps.Keys(t.Issues)) {
		iss := t.Issues[key]
		for _, wait := range iss.Waits {
			if wait.End.IsZero() {
				continue
			}
			switch {
			case wait.Kind == WaitLink && wait.EndedBy == WaitLinkRemoved:
				if i := find(key, wait.End); i >= 0 {
					ended[i] = s.linkAnchor(own[key], iss, wait)
				}
			case wait.Kind == WaitLink && wait.EndedBy == WaitBlockerDone:
				for _, blocker := range wait.On {
					outside, known := t.Issues[blocker]
					if known && len(outside.Visits) > 0 {
						continue
					}
					if i := find(blocker, wait.End); i >= 0 {
						anchor := s.linkAnchor(own[key], iss, wait)
						anchor.blocker = blocker
						ended[i] = anchor
					}
				}
			case wait.Kind == WaitChildren && wait.EndedBy == WaitChildMovedOut:
				for _, child := range wait.On {
					if i := find(child, wait.End); i >= 0 {
						ended[i] = waitAnchor{issue: key, kind: WaitChildren, start: wait.Start, added: -1}
					}
				}
			}
		}
	}
	return ended
}

// linkAnchor is the anchor of an event that ended wait, a link wait of iss: the wait's start, and
// the event of iss that added the link it began on: the first of the issue events of iss, up to
// the wait's start, that each name a blocker the wait is on, with no event between them that
// names none. own is the issue events of iss, by their index, in order.
func (s *script) linkAnchor(own []int, iss *IssueTrace, wait Wait) waitAnchor {
	anchor := waitAnchor{issue: iss.Key, kind: WaitLink, start: wait.Start, added: -1}
	for _, i := range own {
		event := &s.events[i]
		if event.at.After(wait.Start) {
			break
		}
		blockedBy := event.parsed.Issue.BlockedBy
		names := slices.ContainsFunc(wait.On, func(key string) bool { return slices.Contains(blockedBy, key) })
		switch {
		case !names:
			anchor.added = -1
		case anchor.added < 0:
			anchor.added = i
		}
	}
	return anchor
}

// decodeField decodes payload, a payload read by its fields, into into, and says whether it read.
func decodeField(payload map[string]json.RawMessage, into any) bool {
	if payload == nil {
		return false
	}
	raw, err := json.Marshal(payload)
	return err == nil && json.Unmarshal(raw, into) == nil
}

// recordLines is what a replay reads of the window's log beside its trace: the lines of acts it
// applies again that the trace keeps no fact of — each claim's failure, each release of a hold
// with its reason, each escalation of a hold, and each root an architect's release took into its
// tree — each of a kind the window holds (Window.Lines), in order.
type recordLines struct {
	failed    []ClaimFailed
	released  []HoldReleased
	escalated []PhaseHeld
	joined    []RootJoined
}

// readRecordLines reads w's log into its recordLines.
func readRecordLines(w Window) (recordLines, error) {
	held := make(map[string]bool, len(w.Lines))
	for _, msg := range w.Lines {
		held[msg] = true
	}
	var lines recordLines
	for i, raw := range w.Log {
		line, err := ParseLogLine(raw)
		if err != nil {
			return recordLines{}, fmt.Errorf("replay: line %d of the window's log: %w", i+1, err)
		}
		if line == nil || !held[line.Head().Msg] {
			continue
		}
		switch l := line.(type) {
		case ClaimFailed:
			lines.failed = append(lines.failed, l)
		case HoldReleased:
			lines.released = append(lines.released, l)
		case PhaseHeld:
			if l.Reason == string(record.HoldEscalated) {
				lines.escalated = append(lines.escalated, l)
			}
		case RootJoined:
			lines.joined = append(lines.joined, l)
		}
	}
	order := func(x, y LineHead) int { return x.Time.Compare(y.Time) }
	slices.SortStableFunc(lines.failed, func(x, y ClaimFailed) int { return order(x.LineHead, y.LineHead) })
	slices.SortStableFunc(lines.released, func(x, y HoldReleased) int { return order(x.LineHead, y.LineHead) })
	slices.SortStableFunc(lines.escalated, func(x, y PhaseHeld) int { return order(x.LineHead, y.LineHead) })
	slices.SortStableFunc(lines.joined, func(x, y RootJoined) int { return order(x.LineHead, y.LineHead) })
	return lines, nil
}
