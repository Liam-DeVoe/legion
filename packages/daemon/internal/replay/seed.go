package replay

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/blockers"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// defaultBranch is the base branch a start state records an issue on where the window's merges name
// none for its repository.
const defaultBranch = "main"

// seedSession is the session a claim the start state writes in a turn runs, which no worker of the
// replay's own takes: the record's session is not the replay's.
const seedSession = "replay-seed"

// Seed writes into a replay's database, in tx, the state t's record shows at at (assumption 6): what
// every line and event before at made, and nothing at or after it, which the replay applies itself.
// It writes the Dispatch mirror row of every issue an event before at shows; the record of every
// issue that entered the workflow before at, with its phase (held, or awaiting a person, where the
// trace holds that wait at at), Dispatch status, rank, parent, tree, generation, hold, awaiting and
// whether it is handed to Legion, and a lingering root's linger; the design gate rows of each such
// issue's current run, and the mirror row of each ask on such a root; the slots the issues hold,
// the trace's inferred slots among them (assumption 34), and the mark of an issue whose slot went
// back for a wait on another issue; the tree lifecycle of each tree that runs or lingers; the
// claims of the turns under way of the issues in the workflow, as the outbox's start writes a claim
// (its given entry and a confirmed delivery); the owed start of each issue whose work came back
// while it held no slot, marked to wait for one and created at the moment its work came back; and
// a synthetic open pull request for each issue the trace has seen one of. records writes the
// record's tables and claims the claims.
//
// It names each issue it cannot place, with the reason: one Dispatch shows no event or no
// repository of, one in the workflow whose turn stands in a tree the record shows running no
// lifecycle, one a fact the trace could not place leaves in doubt, and one the daemon's own
// judgement of the line, read back from what Seed wrote, finds held by no word it has
// (record.HeldUnclassified). A fact leaves an issue in doubt where it bears on what Seed writes for
// it (bearsOnStart): its phase and entry, its slot, its hold, its linger. A turn fact bears on none,
// and a fact of a linger only where the linger it closes stands at at.
//
// A turn that stands at at of an issue outside the workflow then — one that left it, or a member
// of a tree that closed — is a production check's, which changes nothing of a replay (assumption
// 30): Seed writes no claim of it and names no issue for it, and SeedAndReport counts it. Seed
// answers the issues it names; SeedAndReport answers its whole report (SeedReport).
func Seed(ctx context.Context, tx pgx.Tx, records record.Store, claims *store.Store, t Trace, at time.Time) ([]Unplaced, error) {
	report, err := SeedAndReport(ctx, tx, records, claims, t, at)
	if err != nil {
		return nil, err
	}
	return report.Unplaced, nil
}

// SeedReport is what a start state reports beside what it writes (SeedAndReport): the issues it
// names, the turns it wrote no claim of, the slots it wrote that the trace inferred, and the
// trace's check of its slots against the record's own count, for which it names no issue.
type SeedReport struct {
	// Unplaced is each issue the start state could not place, once for each reason (Seed).
	Unplaced []Unplaced
	// LeftOut is how many turns standing at the start the start state wrote no claim of, by the
	// number of the assumption each rests on: the turns of the issues outside the workflow, a
	// production check's (assumption 30).
	LeftOut map[int]int
	// InferredSlots is how many of the slots the start state wrote the trace inferred an issue held
	// from before the reach (SlotSpan.Inferred, assumption 34).
	InferredSlots int
	// SlotCount is the trace's check of the slots it holds against the slots in use its first slot
	// line says (Trace.SlotCount), whose difference is no issue's.
	SlotCount SlotCount
}

// SeedAndReport writes the start state Seed writes and answers its whole report (SeedReport).
func SeedAndReport(ctx context.Context, tx pgx.Tx, records record.Store, claims *store.Store, t Trace, at time.Time) (SeedReport, error) {
	token, err := claim.ProjectToken(t.Project)
	if err != nil {
		return SeedReport{}, fmt.Errorf("replay: seed the start state of %s: %w", t.Project, err)
	}
	s := seeder{ctx: ctx, tx: tx, records: records, claims: claims, t: t, at: at, rule: t.ruleAt(at), token: token,
		placed: map[string]record.Issue{}, epochs: map[string]uint64{}, leftOut: map[int]int{}}
	keys := slices.Sorted(maps.Keys(t.Issues))
	for _, key := range keys {
		if err := s.mirror(t.Issues[key]); err != nil {
			return SeedReport{}, err
		}
	}
	for _, key := range keys {
		if err := s.issue(t.Issues[key]); err != nil {
			return SeedReport{}, err
		}
	}
	for _, fn := range []func() error{s.gates, s.asks, s.slots, s.lifecycles, s.turns, s.owedStarts, s.pullRequests, s.unclassified} {
		if err := fn(); err != nil {
			return SeedReport{}, err
		}
	}
	for _, fact := range t.Unplaced {
		if _, considered := t.Issues[fact.Issue]; !considered || !bearsOnStart(fact, at) {
			continue
		}
		if fact.At.Before(at) {
			s.unplaced(fact.Issue, "the trace could not place a fact of it before the window's start: %s", fact.Reason)
		} else {
			s.unplaced(fact.Issue, "the trace could not place a fact of it that stands at the window's start: %s", fact.Reason)
		}
	}
	return SeedReport{Unplaced: s.report, LeftOut: s.leftOut, InferredSlots: s.inferred, SlotCount: t.SlotCount}, nil
}

// bearsOnStart says whether fact, which the trace could not place, leaves in doubt what a start
// state at at writes. A fact of a turn bears on none: it is of a turn that ended at the fact's own
// moment (UnplacedTurn), and the claims written are of the turns standing at at. A fact of a linger,
// the close of one the trace does not hold, bears on the start where that linger stands at at, its
// close at or after it: a linger closed before at leaves its tree closed at at whether the trace
// held its start or not. Every other fact bears on the start where it came before at.
func bearsOnStart(fact UnplacedFact, at time.Time) bool {
	switch fact.Kind {
	case UnplacedTurn:
		return false
	case UnplacedLinger:
		return !fact.At.Before(at)
	}
	return fact.At.Before(at)
}

// seeder is SeedAndReport's state while it writes one start state.
type seeder struct {
	ctx     context.Context
	tx      pgx.Tx
	records record.Store
	claims  *store.Store
	t       Trace
	at      time.Time
	rule    record.SlotRule
	// token is the project's claim token, which claims and tree lifecycles are keyed by.
	token string
	// placed is the record written for each issue, and epochs the epoch of each tree lifecycle
	// opened, by its root.
	placed map[string]record.Issue
	epochs map[string]uint64
	// report is each issue named, leftOut the turns written no claim of by assumption, and
	// inferred the slots written that the trace inferred (SeedReport).
	report   []Unplaced
	leftOut  map[int]int
	inferred int
}

// unplaced names an issue the start state cannot place, once for each reason.
func (s *seeder) unplaced(issue, format string, args ...any) {
	reason := fmt.Sprintf(format, args...)
	if !slices.Contains(s.report, Unplaced{Issue: issue, Reason: reason}) {
		s.report = append(s.report, Unplaced{Issue: issue, Reason: reason})
	}
}

// standsAt says whether a stretch from start to end (zero while it stands) stands at at: it began
// before at and had not ended, an end at at itself being a fact the replay applies.
func standsAt(start, end, at time.Time) bool {
	return start.Before(at) && (end.IsZero() || !end.Before(at))
}

// dispatchBefore is the newest issue event of iss before at, and the highest sequence of any of its
// events before it; nil where none is.
func dispatchBefore(iss *IssueTrace, at time.Time) (*DispatchEvent, int64) {
	var newest *DispatchEvent
	var seq int64
	for i := range iss.Dispatch {
		event := &iss.Dispatch[i]
		if !event.At.Before(at) {
			continue
		}
		seq = max(seq, event.Seq)
		if event.Issue != nil && (newest == nil || event.Seq > newest.Seq) {
			newest = event
		}
	}
	return newest, seq
}

// mirror writes iss's Dispatch mirror row as its newest issue event before the start shows it,
// through the daemon's own writer of the copy (blockers.Mirror), as the daemon writes a reading of
// an event: the copy of Dispatch's issues is written by a reading alone, and the record's newest
// event before the start is the reading the start state stands on.
func (s *seeder) mirror(iss *IssueTrace) error {
	event, _ := dispatchBefore(iss, s.at)
	if event == nil {
		return nil
	}
	shown := event.Issue
	fact := intake.DispatchIssue{Key: iss.Key, Seq: event.Seq, Status: shown.Status, Parent: shown.Parent,
		Blockers: append([]string{}, shown.BlockedBy...), Labels: append([]string{}, shown.Labels...), Rank: shown.Rank}
	if _, err := blockers.NewMirror(s.records, cmp.Or(event.Project, iss.Project)).Apply(s.ctx, s.tx, fact); err != nil {
		return fmt.Errorf("replay: seed the mirror row of %s: %w", iss.Key, err)
	}
	return nil
}

// run is iss's latest entry before the start and the visits of that run, nil where it had entered
// none.
func (s *seeder) run(iss *IssueTrace) (*Entry, []Visit) {
	var entry *Entry
	for i := range iss.Entries {
		if iss.Entries[i].At.Before(s.at) {
			entry = &iss.Entries[i]
		}
	}
	if entry == nil {
		return nil, nil
	}
	var visits []Visit
	for _, visit := range iss.Visits {
		if !visit.Enter.Before(entry.At) && visit.Enter.Before(s.at) {
			visits = append(visits, visit)
		}
	}
	return entry, visits
}

// waitAt is iss's wait of kind that stands at the start, nil for none.
func (s *seeder) waitAt(iss *IssueTrace, kind WaitKind) *Wait {
	for i := range iss.Waits {
		if wait := &iss.Waits[i]; wait.Kind == kind && standsAt(wait.Start, wait.End, s.at) {
			return wait
		}
	}
	return nil
}

// visitAt is the visit of visits that stands at the start, nil for none.
func (s *seeder) visitAt(visits []Visit) *Visit {
	for i := range visits {
		if standsAt(visits[i].Enter, visits[i].Exit, s.at) {
			return &visits[i]
		}
	}
	return nil
}

// issue writes iss's record as the start finds it, where it had entered the workflow.
func (s *seeder) issue(iss *IssueTrace) error {
	entry, visits := s.run(iss)
	if entry == nil {
		return nil
	}
	event, seq := dispatchBefore(iss, s.at)
	switch {
	case event == nil:
		s.unplaced(iss.Key, "it runs in the workflow and the window holds no Dispatch event of it before %s", stamp(s.at))
		return nil
	case iss.Repo == "":
		s.unplaced(iss.Key, "Dispatch names no repository of it, which the daemon records every issue on")
		return nil
	}
	shown := event.Issue
	issue := record.Issue{Key: iss.Key, Tree: entry.Tree, Project: s.t.Project, Title: cmp.Or(iss.Title, iss.Key), Phase: phase.Done,
		Generation: entry.Generation, Status: shown.Status, DispatchStatus: shown.Status, Rank: shown.Rank, LastDispatchSeq: seq,
		HandedOver: dispatch.CarriesLegionLabel(shown.Labels), Repo: iss.Repo, GitHubRepo: iss.Repo,
		DefaultBranch: cmp.Or(s.t.Branches[iss.Repo], defaultBranch)}
	if shown.Parent != "" {
		parent := shown.Parent
		issue.Parent = &parent
	}
	for _, visit := range visits {
		if visit.Phase != phase.Admitted {
			issue.TakenBack = true
		}
	}
	if visit := s.visitAt(visits); visit != nil {
		issue.Phase = visit.Phase
		if held := s.waitAt(iss, WaitHeld); held != nil {
			since := held.Start
			issue.Phase, issue.Hold = phase.Held, &record.Hold{From: visit.Phase, Since: &since}
			if held.Reason == string(record.HoldEscalated) {
				issue.Hold.Reason = record.HoldEscalated
			}
		} else if person := s.waitAt(iss, WaitPerson); person != nil && visit.Phase == phase.Admitted {
			issue.Phase = phase.AwaitingPerson
			issue.Awaiting = &record.Awaiting{Ask: person.Ask, GateVersion: person.Version, Artifact: person.Artifact,
				Since: record.EventMark{Seq: person.SinceSeq, At: eventAtSeq(iss, person.SinceSeq, person.Start)}}
		}
	}
	if entry.Tree == iss.Key {
		for _, linger := range iss.Lingers {
			if !linger.At.Before(entry.At) && standsAt(linger.At, linger.Closed, s.at) {
				until := linger.Until
				issue.LingerUntil = &until
			}
		}
	}
	if err := s.records.PutIssue(s.ctx, s.tx, issue); err != nil {
		return fmt.Errorf("replay: seed the record of %s: %w", iss.Key, err)
	}
	// The hold's beginning is the one column of the hold PutIssue keeps as stored: the move into
	// held writes it beside the record (workflow's claimFailed).
	if issue.Hold != nil && issue.Hold.Since != nil {
		if err := s.records.MarkHoldBegan(s.ctx, s.tx, iss.Key, *issue.Hold.Since); err != nil {
			return fmt.Errorf("replay: seed when the hold of %s began: %w", iss.Key, err)
		}
	}
	s.placed[iss.Key] = issue
	return nil
}

// eventAtSeq is the time of iss's event of sequence seq, or otherwise when where the window holds
// none of that sequence.
func eventAtSeq(iss *IssueTrace, seq int64, otherwise time.Time) time.Time {
	for _, event := range iss.Dispatch {
		if event.Seq == seq {
			return event.At
		}
	}
	return otherwise
}

// placedKeys is the keys of the issues the start state recorded, in order.
func (s *seeder) placedKeys() []string { return slices.Sorted(maps.Keys(s.placed)) }

// inWorkflow says whether a placed record stands in the workflow: a phase short of done.
func inWorkflow(issue record.Issue) bool { return issue.Phase != phase.Done }

// gates writes the design gate rows of each placed issue's current run: each step's document and
// latest version, the version approved with the Dispatch event of its approval, the changes a
// person requested, and whether the gate ever opened, as the gate lines and the verdicts before the
// start leave them.
func (s *seeder) gates() error {
	for _, key := range s.placedKeys() {
		iss := s.t.Issues[key]
		entry, _ := s.run(iss)
		steps := map[record.DesignStep]*record.DesignGate{}
		stepOf := func(artifact string) record.DesignStep {
			for step, gate := range steps {
				if gate.ArtifactID == artifact {
					return step
				}
			}
			return record.DesignStepSpec
		}
		for _, fact := range iss.Gates {
			if fact.At.Before(entry.At) || !fact.At.Before(s.at) {
				continue
			}
			step := fact.Step.OrSpec()
			if fact.Kind != GateRegisteredFact {
				step = stepOf(fact.Artifact)
			}
			gate, known := steps[step]
			if !known {
				if fact.Kind != GateRegisteredFact {
					continue
				}
				gate = &record.DesignGate{Issue: key, Step: step}
				steps[step] = gate
			}
			version := fact.Version
			switch fact.Kind {
			case GateRegisteredFact:
				gate.ArtifactID, gate.LatestVersion = fact.Artifact, max(gate.LatestVersion, version)
				if fact.Open {
					gate.ApprovedVersion, gate.ApprovedMark = &version, s.approvalMark(iss, fact)
					gate.EverOpened = true
				}
				if fact.ChangesRequested {
					gate.ToldChangesVersion = &version
				}
			case GateOpenedFact:
				gate.LatestVersion = max(gate.LatestVersion, version)
				gate.ApprovedVersion, gate.ApprovedMark = &version, s.approvalMark(iss, fact)
				gate.ToldChangesVersion, gate.ToldChangesReason, gate.ToldChangesMark = nil, "", record.EventMark{}
				gate.EverOpened = true
			case GateClosedFact:
				gate.LatestVersion = max(gate.LatestVersion, version)
				if fact.Event == string(GateChangesRequested) {
					gate.ApprovedVersion, gate.ApprovedMark = nil, record.EventMark{}
					gate.ToldChangesVersion = &version
				}
			case GateChangesRequested:
				if version == gate.LatestVersion {
					gate.ToldChangesVersion, gate.ToldChangesMark = &version, record.EventMark{Seq: fact.Seq, At: fact.At}
				}
			}
		}
		for _, step := range slices.Sorted(maps.Keys(steps)) {
			if err := s.records.PutGate(s.ctx, s.tx, *steps[step]); err != nil {
				return fmt.Errorf("replay: seed the %s gate of %s: %w", step, key, err)
			}
		}
	}
	return nil
}

// approvalMark is the Dispatch event a gate's opening at fact stands on: the newest approval of its
// document at or before the fact, or, where a registration opened an ungated step, the issue's
// newest event before it at the registration's own time.
func (s *seeder) approvalMark(iss *IssueTrace, fact GateFact) record.EventMark {
	for _, verdict := range slices.Backward(iss.Gates) {
		if verdict.Kind == GateApproved && verdict.Artifact == fact.Artifact && !verdict.At.After(fact.At) {
			return record.EventMark{Seq: verdict.Seq, At: verdict.At}
		}
	}
	_, seq := dispatchBefore(iss, fact.At.Add(time.Nanosecond))
	return record.EventMark{Seq: seq, At: fact.At}
}

// heldSlot is the slot iss holds at the start, and whether it holds one.
func (s *seeder) heldSlot(iss *IssueTrace) (SlotSpan, bool) {
	for _, span := range iss.Slots {
		if standsAt(span.Start, span.End, s.at) {
			return span, true
		}
	}
	return SlotSpan{}, false
}

// asks writes the mirror row of each ask on a placed root of its own tree as its events before the
// start leave it (workflow's mirrorAsk): its kind and document, open on the person's turn from its
// opening, closed by a person or by someone else with the person's answer or other act on it, and
// whether a session of the root's tree opened it. A root that waits on a person's answer waits on
// such a row.
func (s *seeder) asks() error {
	for _, key := range s.placedKeys() {
		issue, iss := s.placed[key], s.t.Issues[key]
		if issue.Tree != key {
			continue
		}
		rows := map[string]*record.RootAsk{}
		for _, event := range iss.Dispatch {
			if event.Ask == nil || !event.At.Before(s.at) {
				continue
			}
			row, known := rows[event.Ask.ID]
			if !known {
				row = &record.RootAsk{Issue: key, Ask: event.Ask.ID}
				rows[event.Ask.ID] = row
			}
			if event.Ask.Kind == record.AskQuestion || event.Ask.Kind == record.AskApproval {
				row.Kind = event.Ask.Kind
			}
			if event.Ask.Artifact != "" {
				row.Artifact = event.Ask.Artifact
			}
			person := event.Actor.Kind == "user"
			switch event.Type {
			case "ask.opened":
				row.Open, row.Seq, row.WaitingOn, row.WaitingOnSeq = true, event.Seq, "human", event.Seq
				if ref, err := claimRefOf(claim.Token(event.Actor.ID)); event.Actor.Kind == "session" && err == nil {
					if member, ok := s.placed[ref.Issue]; ok && member.Tree == key {
						row.ByTree = true
					}
				}
			default:
				row.Open, row.Seq, row.ClosedSeq = false, event.Seq, event.Seq
				row.ClosedBy = record.ClosedByOther
				if person {
					row.ClosedBy = record.ClosedByPerson
					if event.Ask.State == "answered" {
						row.PersonAnswerSeq = event.Seq
					} else {
						row.PersonSeq = event.Seq
					}
				}
			}
		}
		for _, ask := range slices.Sorted(maps.Keys(rows)) {
			if err := s.records.PutRootAsk(s.ctx, s.tx, *rows[ask]); err != nil {
				return fmt.Errorf("replay: seed the mirror row of the ask %s of %s: %w", ask, key, err)
			}
		}
	}
	return nil
}

// slots writes the slot of each placed issue that holds one at the start, in the order the slots
// were given out, each with the moment a link or children wait of its holder began where one stands
// beside it, counting each the trace inferred (SeedReport.InferredSlots); and the mark of each issue
// in the workflow whose last slot went back for a wait on another issue and that has taken none
// since, which stands it in the line for a slot of its own again (record.Issue.ReleasedForWaitAt).
func (s *seeder) slots() error {
	type holder struct {
		key  string
		span SlotSpan
	}
	var holders []holder
	for _, key := range s.placedKeys() {
		if span, held := s.heldSlot(s.t.Issues[key]); held && inWorkflow(s.placed[key]) {
			holders = append(holders, holder{key: key, span: span})
		}
	}
	slices.SortStableFunc(holders, func(x, y holder) int { return x.span.Start.Compare(y.span.Start) })
	for index, h := range holders {
		slot := record.Slot{Issue: h.key, Index: index, AdmittedAt: h.span.Start}
		for _, kind := range []WaitKind{WaitLink, WaitChildren} {
			if wait := s.waitAt(s.t.Issues[h.key], kind); wait != nil && (slot.WaitsSince == nil || wait.Start.Before(*slot.WaitsSince)) {
				since := wait.Start
				slot.WaitsSince = &since
			}
		}
		if err := s.records.PutSlot(s.ctx, s.tx, slot); err != nil {
			return fmt.Errorf("replay: seed the slot of %s: %w", h.key, err)
		}
		if h.span.Inferred {
			s.inferred++
		}
	}
	next := len(holders)
	for _, key := range s.placedKeys() {
		issue, iss := s.placed[key], s.t.Issues[key]
		if _, held := s.heldSlot(iss); held || !inWorkflow(issue) || !s.rule.PhaseKeepsSlot(record.StandsAt(issue)) {
			continue
		}
		var last *SlotSpan
		for i := range iss.Slots {
			if span := &iss.Slots[i]; span.Start.Before(s.at) {
				last = span
			}
		}
		entry, _ := s.run(iss)
		if last == nil || !last.ForWait || last.Start.Before(entry.At) {
			continue
		}
		// The mark is written with the release of a slot row, in one statement (ReleaseSlotForWait),
		// so the row is put first and released at the moment the record shows it went back.
		if err := s.records.PutSlot(s.ctx, s.tx, record.Slot{Issue: key, Index: next, AdmittedAt: last.Start}); err != nil {
			return fmt.Errorf("replay: seed the slot %s gave back for its wait: %w", key, err)
		}
		if err := s.records.ReleaseSlotForWait(s.ctx, s.tx, key, last.End); err != nil {
			return fmt.Errorf("replay: seed the release of %s's slot for its wait: %w", key, err)
		}
	}
	return nil
}

// lifecycles opens, under the workflow's authority, the lifecycle of each tree that runs or
// lingers at the start: each tree whose root the start state recorded in its current run, once
// that run took its first slot or a member of the tree had started, and whose linger has not
// closed it.
func (s *seeder) lifecycles() error {
	for _, root := range s.placedKeys() {
		issue := s.placed[root]
		if issue.Tree != root || !inWorkflow(issue) && issue.LingerUntil == nil {
			continue
		}
		iss := s.t.Issues[root]
		entry, _ := s.run(iss)
		opened := slices.ContainsFunc(iss.Slots, func(span SlotSpan) bool {
			return span.Start.Before(s.at) && (!span.Start.Before(entry.At) || span.Before)
		})
		for _, key := range s.placedKeys() {
			member := s.placed[key]
			_, visits := s.run(s.t.Issues[key])
			if member.Tree == root && slices.ContainsFunc(visits, func(visit Visit) bool { return visit.Phase != phase.Admitted }) {
				opened = true
			}
		}
		if !opened {
			continue
		}
		lifecycle, err := s.records.OpenTreeLifecycle(s.ctx, s.tx, s.token, root, treelifecycle.AuthorityWorkflow)
		if err != nil {
			return fmt.Errorf("replay: seed the tree lifecycle of %s: %w", root, err)
		}
		s.epochs[root] = lifecycle.Epoch
	}
	return nil
}

// turns writes the claim of each turn under way at the start of a placed issue in the workflow, as
// the outbox's start and the turn's confirmation write it: working in its turn since the turn
// began, bound to its tree's lifecycle, the entry it was given and a delivery confirmed at that
// moment. A turn whose tree runs no lifecycle the record shows open is one the start state cannot
// place. A turn of an issue outside the workflow at the start is a production check's: it is
// written no claim and counted under assumption 30 (SeedReport.LeftOut), not named.
func (s *seeder) turns() error {
	for _, key := range s.placedKeys() {
		issue, iss := s.placed[key], s.t.Issues[key]
		for _, turn := range iss.Turns {
			if !standsAt(turn.Start, turn.End, s.at) {
				continue
			}
			if !inWorkflow(issue) {
				s.leftOut[assumeNoProductionCheck]++
				continue
			}
			epoch, open := s.epochs[issue.Tree]
			if !open {
				s.unplaced(key, "the turn of its %s from %s stands at %s, and its tree %s runs no lifecycle the record shows open",
					turn.Claim.Role, stamp(turn.Start), stamp(s.at), issue.Tree)
				continue
			}
			var worked phase.Phase
			if record.StandsAt(issue) != phase.Admitted && phaseRole(record.StandsAt(issue)) == turn.Claim.Role {
				worked = record.StandsAt(issue)
			}
			c := supervise.Claim{Token: turn.Claim.Token, Project: s.token, Tree: issue.Tree, Issue: key, Role: turn.Claim.Role,
				Generation: max(turn.Generation, 1), TreeEpoch: epoch, Session: seedSession, State: supervise.StateWorking,
				Handshake: supervise.EntryHandshake{Generation: issue.Generation, QueuedAt: turn.Start, ConfirmedAt: turn.Start},
				LastReady: turn.Start, LastTurnStart: turn.Start}
			d := supervise.Delivery{ID: "seed:" + string(turn.Claim.Token), Task: "the work the record shows under way at the window's start",
				Phase: worked, Generation: issue.Generation, QueuedAt: turn.Start, DeliveredAt: turn.Start, ConfirmedAt: turn.Start,
				Prompted: true}
			if _, err := s.claims.PutClaimAndDeliveryTx(s.ctx, s.tx, c, d); err != nil {
				return fmt.Errorf("replay: seed the claim of %s: %w", turn.Claim.Token, err)
			}
		}
	}
	return nil
}

// owedStarts writes the start each placed issue owes the worker of the phase it stands in where
// its work came back to it by a move while it held no slot and no worker of that phase has started:
// a start of that phase's role that waits for the issue's slot (needsSlot), created at the moment
// its work first came back, the move into the first of the visits since then in which no worker
// started, since a later move writes over the owed start and keeps its place (EnqueueStart).
func (s *seeder) owedStarts() error {
	for _, key := range s.placedKeys() {
		issue, iss := s.placed[key], s.t.Issues[key]
		if issue.Phase == phase.Held || issue.Phase == phase.AwaitingPerson || !inWorkflow(issue) {
			continue
		}
		if _, held := s.heldSlot(iss); held {
			continue
		}
		_, visits := s.run(iss)
		at := slices.IndexFunc(visits, func(visit Visit) bool { return standsAt(visit.Enter, visit.Exit, s.at) })
		if at < 0 {
			continue
		}
		// A visit whose worker started did so before it ended, so only the visit standing at the
		// start can have a worker start at or after it.
		unstarted := func(visit Visit) bool {
			return visit.In != nil && s.rule.PhaseKeepsSlot(visit.Phase) && (visit.WorkerStart.IsZero() || !visit.WorkerStart.Before(s.at))
		}
		if !unstarted(visits[at]) {
			continue
		}
		first := at
		for first > 0 && unstarted(visits[first-1]) && visits[first-1].Exit.Equal(visits[first].Enter) {
			first--
		}
		created := visits[first].Enter
		standing := visits[at].Phase
		payload := record.SuperviseRequest{Op: "start", Tree: issue.Tree, Role: phaseRole(standing), Task: workflow.ResumePhaseTask(issue),
			Generation: issue.Generation, Phase: standing, NeedsSlot: true}
		row, err := record.NewOutboxRow(key, payload, created)
		if err != nil {
			return fmt.Errorf("replay: seed the owed start of %s: %w", key, err)
		}
		row.CreatedAt = created
		if err := s.records.Enqueue(s.ctx, s.tx, row); err != nil {
			return fmt.Errorf("replay: seed the owed start of %s: %w", key, err)
		}
	}
	return nil
}

// pullRequests writes a synthetic open pull request for each placed issue in the workflow the trace
// has seen one of before the start, on the issue's own branch against its default branch, numbered
// as a line named it or by the issue's own number, its head arrived when the trace first saw the
// pull request: the record the daemon's guards read (review Q2, item 1). An issue that stands at
// the start in a visit to merging whose merge came before it, its close waiting for the issues
// beneath it (Visit.Merge), holds its pull request merged, as the replay's own merge leaves it, so
// the engine closes it at the last one's leave as it closed it in the record.
func (s *seeder) pullRequests() error {
	for _, key := range s.placedKeys() {
		issue, iss := s.placed[key], s.t.Issues[key]
		if !inWorkflow(issue) || iss.PullRequest.IsZero() || !iss.PullRequest.Before(s.at) {
			continue
		}
		number := iss.PullRequestNumber
		if number == 0 {
			_, suffix, _ := strings.Cut(key, "-")
			number, _ = strconv.Atoi(suffix)
		}
		pr := record.PullRequest{Issue: key, Repo: issue.Repo, Number: number, Branch: claim.IssueBranch(key), Base: issue.DefaultBranch,
			HeadUpdatedAt: iss.PullRequest, State: record.PullRequestOpen}
		_, visits := s.run(iss)
		if visit := s.visitAt(visits); visit != nil && visit.Merge != nil && visit.Merge.At.Before(s.at) {
			pr.State, pr.MergeSHA = record.PullRequestMerged, fmt.Sprintf("replay-merge-%s-%d", key, number)
		}
		if err := s.records.PutPullRequest(s.ctx, s.tx, pr); err != nil {
			return fmt.Errorf("replay: seed the pull request of %s: %w", key, err)
		}
	}
	return nil
}

// unclassified reads the line back from what the start state wrote, as the tick of an admission
// pass at the start reads it (blockers.JudgeLine over the project's live issues and slots, under the
// rule and the gate steps the window ran, and record.HeldList over its judgement), and names each
// issue that reading holds by no word it has: a state the record could not have left the daemon
// in, such as a phase held from past the review with no slot.
func (s *seeder) unclassified() error {
	issues, err := s.records.LiveIssues(s.ctx, s.tx, s.t.Project)
	if err != nil {
		return fmt.Errorf("replay: read back the seeded issues of %s: %w", s.t.Project, err)
	}
	slots, err := s.records.Slots(s.ctx, s.tx)
	if err != nil {
		return fmt.Errorf("replay: read back the seeded slots of %s: %w", s.t.Project, err)
	}
	live := make(map[string]bool, len(issues))
	for _, issue := range issues {
		live[issue.Key] = true
	}
	own := slices.DeleteFunc(slices.Clone(slots), func(slot record.Slot) bool { return !live[slot.Issue] })
	facts, err := record.ReadLineFactsUnder(s.ctx, s.tx, s.records, s.t.Project, issues, s.t.GateSteps, s.rule)
	if err != nil {
		return fmt.Errorf("replay: read back the seeded line facts of %s: %w", s.t.Project, err)
	}
	_, facts, line, dropped, err := blockers.JudgeLine(s.ctx, s.tx, s.records, s.t.Project, issues, own, facts, nil, s.at)
	if err != nil {
		return fmt.Errorf("replay: judge the seeded line of %s: %w", s.t.Project, err)
	}
	for _, row := range record.HeldList(issues, own, line, dropped, facts) {
		if row.Reason == record.HeldUnclassified {
			issue := s.placed[row.Issue]
			s.unplaced(row.Issue, "at %s it stands in %s with the status %s, and the daemon's judgement of the line holds it by no word (%s)",
				stamp(s.at), issue.Phase, issue.Status, record.HeldUnclassified)
		}
	}
	return nil
}
