package replay

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// heldRootReleaseReason is the reason of the release of a held root a person set to todo
// (workflow's releaseReason): the person's own event releases it in a replay, so no retry is
// entered for it. operatorRetryReason is the reason of the operator's retry of a phase
// (workflow's restartPhase), which a replay enters as intake.RestartPhase, and by which the trace
// tells that retry of a worker whose issue stands in no hold from a release of a hold it missed.
const (
	heldRootReleaseReason = "the issue was set to todo while its phase was held"
	operatorRetryReason   = "the operator retried this phase's worker"
)

// openSeeded runs each piece under way at the window's start at its recorded times: the start
// state's claims stand in its worker, so only its acts from the start on are entered.
func (d *driver) openSeeded() {
	for _, key := range sortedKeys(d.script.pieces) {
		for _, p := range d.script.pieces[key] {
			if p.opens == opensAtSeed && p.state == pieceWaiting {
				d.openPiece(p, p.start, true)
			}
		}
	}
}

// openOnPhase opens the next piece of key's work that the move into to opens rather than a start
// row (opensOnPhase): accepting's, whose role is the architect that owns the issue.
func (d *driver) openOnPhase(key string, to phase.Phase) {
	for _, p := range d.script.pieces[key] {
		if p.state != pieceWaiting {
			continue
		}
		if p.opens == opensOnPhase && p.phase == to {
			d.openPiece(p, d.clock.Instant(), false)
		}
		return
	}
}

// startPiece opens, for a start of role on key that the stub took, serving the phase served, the
// next piece of key's work that role's start opens: the first piece not yet run that is role's in
// served; a piece before it the replay passes over, counted. It answers the piece it opened, nil
// for none.
func (d *driver) startPiece(key string, role claim.Role, served phase.Phase, retry bool) *piece {
	for _, p := range d.script.pieces[key] {
		if p.state != pieceWaiting {
			continue
		}
		if p.role != role || p.phase != served || p.opens == opensOnPhase || p.opens == opensAtSeed || (p.opens == opensOnRetry) != retry {
			continue
		}
		for _, before := range d.script.pieces[key][:p.index] {
			if before.state == pieceWaiting {
				before.state = pieceSkipped
				d.counters.SkippedSegments++
			}
		}
		d.openPiece(p, d.clock.Instant(), false)
		return p
	}
	return nil
}

// runningPiece is the piece of key's work role's claim stands in now, running or paused; nil for
// none.
func (d *driver) runningPiece(key string, role claim.Role) *piece {
	for _, p := range d.script.pieces[key] {
		if (p.state == pieceRunning || p.state == piecePaused) && p.role == role {
			return p
		}
	}
	return nil
}

// openPiece runs p from at: each of its acts is queued at its offset from at, and its end at at
// plus its length, its recorded length slowed by the issues in flight (slowed). A piece under way
// at the window's start (seeded) runs at its recorded times, its acts before the start already in
// the start state. Any other piece is sized at the end of the instant it opens in (sizePiece), so
// the issues in flight it is slowed by are the instant's, once every pass of the instant has run,
// as the record's count it is set against is (script.inFlightAt): the starts of one instant, each
// in a pass of its own, are each read at the instant's count, not at the count of the passes
// before it. Its acts at its very start are queued at once.
func (d *driver) openPiece(p *piece, at time.Time, seeded bool) {
	p.state, p.at, p.turns = pieceRunning, at, map[claim.Role]bool{}
	d.used[1] = true
	if seeded {
		d.sizePiece(p, at, p.m, true)
		return
	}
	for _, a := range p.acts {
		if a.offset <= 0 {
			d.queueAct(p, a, at)
		}
	}
	d.push(&item{at: at, class: classSize, rec: at, piece: p, run: func() error {
		d.sizePiece(p, d.clock.Instant(), len(d.inFlight), false)
		return nil
	}})
}

// sizePiece sets p's length for n issues in flight at its start and queues its acts after its
// start, at their offsets from at, and its end at at plus its length. An act the record shows at
// the segment's end, the last turn's end among them, comes at the replay's end of it, and an act
// the record shows after the end of a piece the replay shortened is queued at its end, so a turn
// that ran to the end of its segment runs to the end of the replay's, however long the replay made
// it. A seeded piece's acts before the window's start are left out but for the turns under way at
// it.
func (d *driver) sizePiece(p *piece, at time.Time, n int, seeded bool) {
	p.n = n
	p.length = slowed(p.recorded(), d.cfg.Slowdown, p.n, p.m)
	if d.cfg.TraceSegments {
		d.segments = append(d.segments, Segment{Issue: p.issue, Phase: p.phase, Start: p.at, N: p.n, M: p.m, Recorded: Duration(p.recorded()),
			Length: Duration(p.length)})
	}
	for _, a := range p.acts {
		if seeded && a.rec.Before(d.window.From) {
			if a.kind == actTurnStart {
				p.turns[a.role] = true
			}
			continue
		}
		if !seeded && a.offset <= 0 {
			continue
		}
		when := at.Add(a.offset)
		switch {
		case p.end.IsZero():
		case a.offset >= p.recorded():
			when = at.Add(p.length)
		case a.offset > p.length && a.kind != actTurnEnd:
			when = at.Add(p.length)
		}
		d.queueAct(p, a, when)
	}
	if !p.end.IsZero() {
		d.push(&item{at: at.Add(p.length), class: classAct, rec: p.end, order: lineOrder + 1, piece: p, run: func() error { return d.endPiece(p) }})
	}
}

// queueAct queues one act of p at when.
func (d *driver) queueAct(p *piece, a act, when time.Time) {
	switch a.kind {
	case actTurnStart:
		d.push(&item{at: when, class: classTurnStart, rec: a.rec, piece: p, run: func() error { return d.turnStarts(p, a.role, a.turn) }})
	case actTurnEnd:
		d.push(&item{at: when, class: classTurnEnd, rec: a.rec, piece: p, run: func() error { return d.turnEnds(p, a.role, a.turn) }})
	case actEvent:
		d.placePieceEvent(a.event, when, p)
	case actRegistration:
		d.push(&item{at: when, class: classAct, rec: a.rec, order: lineOrder + int64(a.order), piece: p,
			run: func() error { return d.register(p.issue, a.gate) }})
	case actClaimFailed:
		d.push(&item{at: when, class: classAct, rec: a.rec, order: lineOrder, piece: p, run: func() error { return d.claimFailed(a.failed) }})
	case actAdoption:
		d.push(&item{at: when, class: classAct, rec: a.rec, order: lineOrder, piece: p, run: func() error { return d.adopt(a.joined) }})
	case actCloseRoot:
		d.push(&item{at: when, class: classAct, rec: a.rec, order: lineOrder, piece: p, run: func() error { return d.closeRoot(p.issue) }})
	case actReady:
		d.push(&item{at: when, class: classAct, rec: a.rec, order: lineOrder, piece: p, run: func() error { return d.readyMark(p.issue) }})
	}
}

// placePieceEvent queues the recorded event events[i], an act of p's work, at when.
func (d *driver) placePieceEvent(i int, when time.Time, p *piece) {
	event := &d.script.events[i]
	if event.placed {
		return
	}
	event.placed = true
	d.placed[i] = when
	var applied []byte
	d.push(&item{at: when, class: classAct, rec: event.at, order: int64(event.order), piece: p, eventKey: event.env.key,
		eventKind: event.env.kind,
		dispatch: func() error {
			var err error
			applied, err = d.dispatchEvent(i)
			return err
		},
		run: func() error { return d.applyEvent(i, applied) }})
	for _, dependent := range event.dependents {
		d.placeEvent(dependent, when.Add(d.script.events[dependent].offset))
	}
}

// pausePiece stops p where its worker's claim was suspended for a wait the rule stops the phase
// for (record.SlotRule.WaitStopsPhase): its items wait, keeping the rest of its length, and the
// turns under way end.
func (d *driver) pausePiece(p *piece) error {
	if p.state != pieceRunning {
		return nil
	}
	p.state, p.pausedAt = piecePaused, d.clock.Instant()
	for _, role := range slices.Sorted(maps.Keys(p.turns)) {
		if p.turns[role] {
			if err := d.writeTurnEnd(p.issue, role); err != nil {
				return err
			}
		}
	}
	return nil
}

// resumePiece runs p again from the clock's instant, with the rest of its length: every item it
// held is queued again later by the time it stood paused, and the turns the pause ended begin
// again.
func (d *driver) resumePiece(p *piece) error {
	if p.state != piecePaused {
		return nil
	}
	now := d.clock.Instant()
	shift := now.Sub(p.pausedAt)
	p.state = pieceRunning
	held := p.pending
	p.pending = nil
	for _, it := range held {
		if it.done {
			continue
		}
		it.done = true
		again := *it
		again.done, again.at = false, it.at.Add(shift)
		if again.at.Before(now) {
			again.at = now
		}
		d.push(&again)
	}
	for _, role := range slices.Sorted(maps.Keys(p.turns)) {
		if p.turns[role] {
			if err := d.writeTurnStart(p.issue, role, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// cancelWork cancels every piece of key's work under way: its issue left the workflow, or its
// claims were closed, so nothing of that work goes on.
func (d *driver) cancelWork(key string) {
	for _, p := range d.script.pieces[key] {
		if p.state == pieceRunning || p.state == piecePaused {
			p.state = pieceCancelled
		}
	}
}

// endPiece ends p at the replay's end of its segment: the turns under way in it end, the task its
// role's claim held for it is retired as its worker's last turn retires it (retireTask), and its end
// is entered by how it ends — its recorded move, a sign-off the engine may hold, the failure of its
// role's claim, which keeps its task as a failed claim does — or by nothing, where the pass made its
// move or nothing ended it.
func (d *driver) endPiece(p *piece) error {
	for _, role := range slices.Sorted(maps.Keys(p.turns)) {
		if p.turns[role] {
			if err := d.turnEnds(p, role, nil); err != nil {
				return err
			}
		}
	}
	p.state = pieceEnded
	if p.ends == endFailure {
		return d.failPiece(p)
	}
	if err := d.retireTask(p.issue, p.role); err != nil {
		return err
	}
	switch p.ends {
	case endMove:
		return d.enterMove(p)
	case endHeldSignOff:
		return d.signOff(p.issue)
	}
	return nil
}

// generationAt is the generation key's record stood at in the record at at.
func (d *driver) generationAt(key string, at time.Time) uint64 {
	if iss, ok := d.trace.Issues[key]; ok {
		if entry := iss.entryAt(at); entry != nil {
			return entry.Generation
		}
	}
	return 1
}

// enterMove enters the recorded move that ends p's visit, by its source (MoveSources): through the
// engine's ReplayTransition, or through the daemon's own handler of the fact the API or GitHub
// applies. A move whose source is the conflict draw is no move this replay enters: the stretch
// that ends in it is a stretch of a head that can conflict (stretchEnds). Every move the daemon's
// code refuses is counted with its reason.
func (d *driver) enterMove(p *piece) error {
	move := p.visit.Out
	if move == nil {
		return nil
	}
	if move.Source.Way == MoveDraw {
		return d.stretchEnds(p)
	}
	if move.Trigger == workflow.TriggerImplementationReady || move.Trigger == workflow.TriggerPullRequestMerged || pastImplementing(move.From) {
		if err := d.ensurePullRequest(p.issue); err != nil {
			return err
		}
	}
	to := cmp.Or(move.Source.To, move.To)
	if phase.MovesBack(move.From, to) {
		d.used[3], d.used[13] = true, true
	}
	refuse := func(reason string) {
		d.counters.Refused = append(d.counters.Refused, RefusedMove{Issue: p.issue, At: d.clock.Instant(), Trigger: move.Trigger,
			From: move.From, To: to, Reason: reason})
	}
	switch move.Source.Way {
	case MoveTransition:
		d.used[assumeCompletionPassed] = true
		result, err := d.apply("replay", d.factID("move", p.issue, move.Trigger), RecordedMove{Issue: p.issue,
			Generation: d.generationAt(p.issue, move.At), From: move.From, To: to, Trigger: move.Trigger})
		if err != nil {
			return err
		}
		if result.Refusal != nil {
			refuse(result.Refusal.Message)
		}
		return nil
	case MoveFact:
		switch move.Trigger {
		case workflow.TriggerSignOff:
			return d.signOff(p.issue)
		case workflow.TriggerPullRequestMerged:
			pr, err := d.pullRequestOf(p.issue)
			if err != nil || pr == nil {
				if err == nil {
					refuse(fmt.Sprintf("%s holds no pull request record to merge", p.issue))
				}
				return err
			}
			if _, err := d.apply("github", d.factID("merge", p.issue, pr.Number), intake.PullRequestMerged{Repo: pr.Repo, Number: pr.Number,
				MergeSHA: fmt.Sprintf("replay-merge-%s-%d", p.issue, pr.Number), HeadSHA: pr.HeadSHA}); err != nil {
				return err
			}
			return d.refusedUnlessMoved(p.issue, move, to, refuse, phase.Merging)
		case workflow.TriggerBackward:
			if move.Drop != "" {
				d.drops[p.issue] = move.Drop
			}
			result, err := d.apply("api", d.factID("backward", p.issue, to), intake.BackwardMove{Issue: p.issue,
				Requester: phaseRole(move.From), To: to, Reason: fmt.Sprintf("the record's move back from %s to %s", move.From, to)})
			if err != nil {
				return err
			}
			if result.Refusal != nil {
				refuse(result.Refusal.Message)
			}
			return nil
		}
	}
	return nil
}

// refusedUnlessMoved counts move refused where key does not stand in its target to after it, nor
// waits in waiting for the issues beneath it (a merge's close, which the engine makes at the last
// one's leave).
func (d *driver) refusedUnlessMoved(key string, move *Move, to phase.Phase, refuse func(string), waiting phase.Phase) error {
	issue, err := d.recorded(key)
	if err != nil {
		return err
	}
	switch {
	case issue == nil:
		refuse(fmt.Sprintf("%s is not recorded", key))
	case issue.Phase != to && issue.Phase != waiting:
		refuse(fmt.Sprintf("the %s of %s left it in %s, not %s", move.Trigger, key, issue.Phase, to))
	}
	return nil
}

// stretchEnds is the end of a stretch in which a head can conflict whose recorded end is a
// conflict (MoveDraw, assumption 3): the hook where a replay draws whether the stretch's head
// conflicts. This replay draws none, as a window whose fitted rate gives no stretch a conflict
// does: the stretch goes on as one that did not conflict, past the loop the record ran for its
// conflict, with the work the record shows after the loop returned to the phase it left, which
// opens at once.
func (d *driver) stretchEnds(p *piece) error {
	pieces := d.script.pieces[p.issue]
	for _, next := range pieces[p.index+1:] {
		if next.state != pieceWaiting {
			continue
		}
		if next.phase == p.phase && next.visit.In != nil && next.visit.In.To == p.phase {
			d.openPiece(next, d.clock.Instant(), false)
			return nil
		}
		next.state = pieceSkipped
	}
	return nil
}

// signOff applies key's architect's sign-off (intake.SignOff) through the engine's own handler.
// A sign-off the engine holds while a member beneath is in the workflow is applied again at each
// leave of a member of its tree (observe), and is no refusal: the record shows the architect
// signing off again at once (review Q2, item 3).
func (d *driver) signOff(key string) error {
	result, err := d.apply("api", d.factID("sign-off", key), intake.SignOff{Issue: key})
	if err != nil {
		return err
	}
	switch {
	case result.Refusal == nil:
		delete(d.signOffs, key)
	case result.Refusal.Code == "SIGNOFF_CHILD_IN_WORKFLOW":
		if !d.signOffs[key] {
			d.counters.HeldSignOffs++
		}
		d.signOffs[key] = true
	default:
		delete(d.signOffs, key)
		d.counters.Refused = append(d.counters.Refused, RefusedMove{Issue: key, At: d.clock.Instant(), Trigger: workflow.TriggerSignOff,
			From: phase.Accepting, To: phase.Retro, Reason: result.Refusal.Message})
	}
	return nil
}

// failPiece enters the failure of the claim of p's role that ends p (intake.ClaimFailed), which
// holds the issue, and queues the hold's recorded end at its recorded offset from the failure: the
// architect's retry (intake.RetryOrEscalate), the operator's (intake.RestartPhase), or nothing for a
// person's todo on the held root, whose own event releases it; and each escalation of the hold at
// its own offset.
func (d *driver) failPiece(p *piece) error {
	failed := ClaimFailed{LineHead: LineHead{Time: p.end}, Claim: ClaimRef{Issue: p.issue, Role: p.role}}
	for _, line := range d.lines.failed {
		if line.Claim.Issue == p.issue && line.Claim.Role == p.role && line.Time.Equal(p.end) {
			failed = line
		}
	}
	if err := d.claimFailed(failed); err != nil {
		return err
	}
	hold := p.hold
	if hold == nil {
		return nil
	}
	failedAt := d.clock.Instant()
	for _, escalation := range d.lines.escalated {
		if escalation.Issue == p.issue && escalation.Time.After(hold.Start) && (hold.End.IsZero() || escalation.Time.Before(hold.End)) {
			d.push(&item{at: failedAt.Add(escalation.Time.Sub(hold.Start)), class: classAct, rec: escalation.Time, order: lineOrder,
				run: func() error {
					_, err := d.apply("api", d.factID("escalate", p.issue), intake.RetryOrEscalate{Issue: p.issue, Decision: intake.EscalateDecision,
						Reason: escalation.Reason})
					return err
				}})
		}
	}
	if hold.EndedBy != WaitReleased {
		return nil
	}
	reason := ""
	for _, line := range d.lines.released {
		if line.Issue == p.issue && line.Time.Equal(hold.End) {
			reason = line.Reason
		}
	}
	if reason == heldRootReleaseReason {
		return nil
	}
	at := failedAt.Add(hold.End.Sub(hold.Start))
	d.push(&item{at: at, class: classAct, rec: hold.End, order: lineOrder, run: func() error {
		if reason == operatorRetryReason {
			_, err := d.apply("api", d.factID("restart", p.issue), intake.RestartPhase{Issue: p.issue, Role: p.role})
			return err
		}
		_, err := d.apply("api", d.factID("retry", p.issue), intake.RetryOrEscalate{Issue: p.issue, Decision: intake.RetryDecision, Reason: reason})
		return err
	}})
	return nil
}

// claimFailed enters a recorded claim failure: the claim the stub holds is failed, its turn
// ended, the line written, and intake.ClaimFailed applied, which holds the issue where the
// claim's role works the phase it stands in, and otherwise only tells its architect.
func (d *driver) claimFailed(failed ClaimFailed) error {
	key, role := failed.Claim.Issue, failed.Claim.Role
	token, err := claim.NewToken(d.token, key, role)
	if err != nil {
		return err
	}
	if c, ok := d.claims[token]; ok {
		if d.turnOpen(c) {
			if err := d.writeTurnEnd(key, role); err != nil {
				return err
			}
		}
		c = d.claims[token]
		c.State, c.FailedAt, c.Failure, c.FailureCause = supervise.StateFailed, d.now(), failed.Why, supervise.FailureCause(failed.Cause)
		if err := d.putClaim(c); err != nil {
			return err
		}
	}
	d.log.Error("supervise: claim failed", "claim", string(token), "cause", failed.Cause, "why", failed.Why)
	_, err = d.apply("supervise", d.factID("claim-failed", key, role), intake.ClaimFailed{Issue: key, Role: role, Reason: failed.Why,
		Cause: supervise.FailureCause(failed.Cause)})
	return err
}

// register enters the architect's registration of a design document (intake.GateRegistered),
// rebuilt from its recorded line and the shadow's reading of the document, as register_gate reads
// Dispatch's approval of it: its latest version, the approval or changes request on that version,
// the approval request open on it, and the issue's sequence before the read (Since).
func (d *driver) register(key string, gate GateFact) error {
	read := d.shadow.approvalOf(key, gate.Artifact)
	fact := intake.GateRegistered{Issue: key, Step: gate.Step, ArtifactID: gate.Artifact, Version: gate.Version, DispatchVersion: read.latest,
		RequestAsk: read.ask, RequestWaitsOnAgent: read.ask != "" && read.waitingOn == "agent", ReviewedBy: read.latestReviewer}
	if iss, ok := d.shadow.issues[key]; ok {
		fact.Since = record.EventMark{Seq: iss.seq}
	}
	if read.approved != nil && read.approved.version >= max(gate.Version, read.latest) {
		fact.ApprovedVersion, fact.ApprovedMark = read.approved.version, read.approved.mark
	}
	if read.changes != nil && read.changes.version >= max(gate.Version, read.latest) {
		fact.ChangesRequestedVersion, fact.ChangesReason, fact.ChangesRequestedMark = read.changes.version, read.changes.reason, read.changes.mark
	}
	_, err := d.apply("api", d.factID("gate", key, gate.Artifact, gate.Version), fact)
	return err
}

// adopt enters an architect's release of the queued roots joined into its tree
// (intake.ChildAdoption), each as the shadow shows it.
func (d *driver) adopt(joined []RootJoined) error {
	if len(joined) == 0 {
		return nil
	}
	fact := intake.ChildAdoption{Tree: joined[0].Tree}
	for _, line := range joined {
		child := intake.AdoptedChild{Issue: line.Issue}
		if iss, ok := d.shadow.issues[line.Issue]; ok {
			child.Parent, child.Status, child.BlockedBy, child.Seq = iss.parent(), iss.status(), iss.blockers(), iss.seq
		}
		fact.Children = append(fact.Children, child)
	}
	_, err := d.apply("api", d.factID("adopt", fact.Tree), fact)
	return err
}

// closeRoot enters the architect's close of its root before the first phase (intake.CloseRoot).
func (d *driver) closeRoot(key string) error {
	_, err := d.apply("api", d.factID("close-root", key), intake.CloseRoot{Issue: key, Reason: "the record's close of the root by its architect"})
	return err
}

// readyMark enters the merger's ready mark of key inside merging (ReadyMark, assumption 20).
func (d *driver) readyMark(key string) error {
	d.used[19], d.used[20] = true, true
	issue, err := d.recorded(key)
	if err != nil || issue == nil {
		return err
	}
	_, err = d.apply("replay", d.factID("ready", key), ReadyMark{Issue: key, Generation: issue.Generation})
	return err
}

// ensurePullRequest writes the replay's own open pull request record of key where it holds none,
// on the issue's branch against its default branch, numbered by the number a recorded line names
// or by the issue's own, its head arriving now as a live webhook's would: the record the daemon's
// guards read (review Q2, item 1).
func (d *driver) ensurePullRequest(key string) error {
	pr, err := d.pullRequestOf(key)
	if err != nil || pr != nil {
		return err
	}
	issue, err := d.recorded(key)
	if err != nil || issue == nil {
		return err
	}
	number := 0
	if iss, ok := d.trace.Issues[key]; ok {
		number = iss.PullRequestNumber
	}
	if number == 0 {
		_, suffix, _ := cutKey(key)
		number, _ = strconv.Atoi(suffix)
	}
	d.used[assumeNoGitHub] = true
	arrived := d.now()
	return d.st.Tx(d.ctx, func(tx pgx.Tx) error {
		return d.records.PutPullRequest(d.ctx, tx, record.PullRequest{Issue: key, Repo: issue.Repo, Number: number, Branch: claim.IssueBranch(key),
			Base: issue.DefaultBranch, HeadUpdatedAt: arrived, State: record.PullRequestOpen})
	})
}

// pullRequestOf is the pull request record key holds in the replay's database, nil for none.
func (d *driver) pullRequestOf(key string) (*record.PullRequest, error) {
	var pr *record.PullRequest
	err := d.st.Tx(d.ctx, func(tx pgx.Tx) error {
		var err error
		pr, err = d.records.PullRequest(d.ctx, tx, key)
		return err
	})
	return pr, err
}

// turnStarts is a recorded turn of role's worker beginning in p: its claim is in a turn.
func (d *driver) turnStarts(p *piece, role claim.Role, _ *Turn) error {
	p.turns[role] = true
	return d.writeTurnStart(p.issue, role, p)
}

// turnEnds is a turn of role's worker in p ending.
func (d *driver) turnEnds(p *piece, role claim.Role, _ *Turn) error {
	if !p.turns[role] {
		return nil
	}
	p.turns[role] = false
	return d.writeTurnEnd(p.issue, role)
}

// sortedKeys is the keys of m, in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
