package workflow

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The work hold (`gates.hold: work`, dispatch://INFRA-4528's spec, steps 1 to 7 under `work`, and
// its plan's decisions 13 to 19) as the workflow writes it. Under that hold a tree's closed
// standing design gate holds its running work as well as its merge: at the close every phase
// worker of the tree is told to end its turn and is stopped with its session kept, and at the
// opening each claim the hold stopped is started again with one task. The workflow writes the
// rows; the outbox runs them (internal/daemon's outbox, its suspend and start ops and its
// role message), and supervise holds the claim (supervise.SuspendedByHold, Machine.EndHold).
//
// The hold is the deployment's word (Config.Hold) over the one predicate the merge hold reads too
// (record.TreeGate.HoldsWork, over the standing gate). The record keeps the hold of each tree
// (record.WorkHold): the close writes it, the opening deletes it, and the boot reads it to tell a
// tree whose close was never applied from one whose drain is under way (workHoldBooted).

// The sentences of the hold's texts beside today's gate messages (gateClosedSentence,
// gateOpenedMessage). Every one is a reader's words and part of the contract (H12).
const (
	// workHoldSentence follows today's first sentence of the close under `work` (spec step 2),
	// and every opening task repeats it (hold-decisions point 2).
	workHoldSentence = "The deployment holds the tree's work until every gated step stands open again: no phase " +
		"starts, and the daemon stops each phase worker, with its session kept, when its turn ends or 2 minutes after this message."
	// workerCloseEnd ends the close message to a phase worker, and architectCloseEnd the one to an
	// architect, whom the hold does not stop.
	workerCloseEnd    = "End your turn now."
	architectCloseEnd = "Your own work goes on."
	// holdStoppedSentence and checkTurnSentence frame today's open message in the opening task.
	holdStoppedSentence = "The work hold stopped your turn."
	checkTurnSentence   = "Check what your turn already did: a tool call may not have finished."
	// noPhaseWork is the work of an opening task to a claim that holds no phase of its issue.
	noPhaseWork = "The issue's other roles may ask you questions."
)

// gateClosedSentence is today's first sentence of the close message: the tree whose gate closed,
// and the version, the step and the document that closed it. The step and its own document, never
// "spec version": under more than one gated step the version is that step's document's, and a
// message naming the spec would send a reader to the wrong one (INFRA-1272).
func gateClosedSentence(tree string, step record.DesignStep, document string, version int) string {
	return fmt.Sprintf("The design gate of %s closed: version %d of its %s document (%s) is with its reviewer, "+
		"so the design you are working to may change.", tree, version, step, document)
}

// mergeCloseMessage is today's close message, which `merge` sends word for word.
func mergeCloseMessage(tree string, step record.DesignStep, document string, version int) string {
	return gateClosedSentence(tree, step, document, version) +
		" Your phase goes on; the merge waits until every gated step stands open again."
}

// gateOpenedMessage is today's open message, which both holds send at the opening.
func gateOpenedMessage(tree string, step record.DesignStep, document string, version int) string {
	return fmt.Sprintf("The design gate of %s opened: a person approved version %d of its %s document (%s), "+
		"and every step the deployment gates now stands open, so the design you are working to is the approved one. "+
		"A merge the closed gate held is released.", tree, version, step, document)
}

// workCloseMessage is the close message under `work` (spec step 2): today's first sentence, the
// hold's, and the ending of the recipient, an architect or a phase worker.
func workCloseMessage(hold record.WorkHold, architect bool) string {
	end := workerCloseEnd
	if architect {
		end = architectCloseEnd
	}
	return gateClosedSentence(hold.Tree, hold.Step, hold.ArtifactID, hold.Version) + " " + workHoldSentence + " " + end
}

// bootOpenedSentence is what the boot says where a tree's hold ends with its gate still where it
// stood: the deployment moved to `merge`, or turned the gate off (row H26).
func bootOpenedSentence(tree string) string {
	return "The deployment no longer holds the work of " + tree + "'s tree."
}

// openingSentences is what the one task of the opening starts with (hold-decisions point 2): the
// close message's two sentences, which name the version that closed the gate, that the hold
// stopped the turn, what ended the hold (today's open message, or the boot's sentence), and the
// check of what the turn already did, since a cut turn can leave a tool call unfinished. A hold no
// record names the closing version of says the hold's own sentence alone in front.
func openingSentences(closing *record.WorkHold, ended string) string {
	opening := workHoldSentence
	if closing != nil {
		opening = gateClosedSentence(closing.Tree, closing.Step, closing.ArtifactID, closing.Version) + " " + opening
	}
	return opening + " " + holdStoppedSentence + " " + ended + " " + checkTurnSentence
}

// holdReason is the reason a stop of the hold logs on the claim it stops.
func holdReason(hold record.WorkHold) string {
	return fmt.Sprintf("the work hold: version %d of the %s document (%s) of %s closed the design gate",
		hold.Version, hold.Step, hold.ArtifactID, hold.Tree)
}

// holdsWork is whether the deployment's hold is `work` (Config.Hold).
func (e *Engine) holdsWork() bool { return e.cfg.Hold == config.HoldWork }

// treeClaim is one role of an in-workflow member of a tree as the hold reads it: the member, the
// role, the claim token and the role's queued rows and claim at the member's generation, whose
// Claim is nil for a role the daemon holds no claim of.
type treeClaim struct {
	issue record.Issue
	role  claim.Role
	token claim.Token
	run   record.RoleRun
}

// state is the claim as the rules of supervise read it, the zero claim for none.
func (c treeClaim) state() supervise.Claim {
	if c.run.Claim == nil {
		return supervise.Claim{}
	}
	return supervise.Claim{State: c.run.Claim.State, SuspendedBy: c.run.Claim.SuspendedBy}
}

// live is whether the claim holds its role now: it is not suspended, failed or retired, which a
// claim launching or queued for its launch is not either.
func (c treeClaim) live() bool {
	if c.run.Claim == nil {
		return false
	}
	switch c.run.Claim.State {
	case supervise.StateSuspended, supervise.StateFailed, supervise.StateRetired:
		return false
	}
	return true
}

// treeClaims reads every role of every member of root's tree that is in the workflow
// (record.InWorkflow), members by key and roles in claim.Roles's order, the architect first, with
// the role's queued rows and claim at the member's generation, in batches (RoleRuns). A member
// out of the workflow is left out, a closed one whose production check a role looks at among
// them (row H29): the hold holds no work of an issue that has left the workflow. A role whose
// token cannot be built is logged and left out, as tellGateMoved leaves such a holder untold.
func (e *Engine) treeClaims(ctx context.Context, tx pgx.Tx, root record.Issue) ([]treeClaim, error) {
	members, err := e.store.TreeIssues(ctx, tx, root.Tree)
	if err != nil {
		return nil, err
	}
	project, err := claim.ProjectToken(root.Project)
	if err != nil {
		return nil, err
	}
	var claims []treeClaim
	for _, member := range members {
		if !record.InWorkflow(member) {
			continue
		}
		for _, role := range claim.Roles {
			token, err := claim.NewToken(project, member.Key, role)
			if err != nil {
				e.logOnCommit(ctx, "workflow: the work hold cannot name this role, so it neither tells nor stops it",
					"tree", root.Tree, "issue", member.Key, "role", role, "error", err)
				continue
			}
			claims = append(claims, treeClaim{issue: member, role: role, token: token})
		}
	}
	for start := 0; start < len(claims); start += strandedRoleRunBatchSize {
		batch := claims[start:min(start+strandedRoleRunBatchSize, len(claims))]
		keys := make([]record.RoleRunKey, len(batch))
		for i, c := range batch {
			keys[i] = record.RoleRunKey{Token: c.token, Issue: c.issue.Key, Role: c.role, Generation: c.issue.Generation}
		}
		runs, err := e.store.RoleRuns(ctx, tx, keys)
		if err != nil {
			return nil, err
		}
		for i := range batch {
			batch[i].run = runs[batch[i].token]
		}
	}
	return claims, nil
}

// workHoldClose is the work hold's close of root's tree at version of step's document (spec steps
// 1 to 3, decisions 14 and 15): the gate's move to closed under `work` (tellGateMoved), and the
// boot's close of a tree it finds held with no record (workHoldBooted, row H25). It writes the
// record and tells each role of the tree what the hold does to it:
//
//   - every live architect, the root's and each member's own, the close and "Your own work goes
//     on.": the hold stops no architect (row H9);
//   - every live phase worker (planner, implementer, tester, reviewer, merger), idle claims of a
//     finished phase among them, the close and "End your turn now.", carrying its stop
//     (record.RoleMessage.Stop), which the outbox posts once the message reached the claim, with
//     the deadline 2 minutes on (rows H8, H13, H15);
//   - a released merger the same message, marked to wake it (record.RoleMessage.Wake), so it can
//     take its pull request out of the merge queue (decision 13, row H16);
//   - a released worker that is not a merger nothing: a wake would bring its pod back only to stop
//     it, so its stop is written at once, and the hold takes its `released` source over with its
//     bundle and its pending task kept (row H14);
//   - and a claim an operator or the daemon suspended, or a failed or retired one, nothing: the
//     operator's suspension and the daemon's stand above the hold's (rows H17, H27).
//
// Each stop is one log line naming the issue, the role and the version (row H33).
func (e *Engine) workHoldClose(ctx context.Context, tx pgx.Tx, root record.Issue, step record.DesignStep, document string, version int) error {
	hold := record.WorkHold{Project: root.Project, Tree: root.Tree, RootGeneration: root.Generation, Step: step,
		ArtifactID: document, Version: version, ClosedAt: e.now()}
	if err := e.store.PutWorkHold(ctx, tx, hold); err != nil {
		return err
	}
	claims, err := e.treeClaims(ctx, tx, root)
	if err != nil {
		return err
	}
	told, stopped := 0, 0
	for _, c := range claims {
		stop := record.SuperviseRequest{Op: "suspend", Tree: c.issue.Tree, Role: c.role, Generation: c.issue.Generation,
			SuspendedBy: supervise.SuspendedByHold, Reason: holdReason(hold)}
		var how string
		switch {
		case c.role == claim.RoleArchitect && c.live():
			if err := e.enqueue(ctx, tx, c.issue.Key, record.RoleMessage{Role: string(c.token), Body: workCloseMessage(hold, true)}); err != nil {
				return err
			}
			told++
			continue
		case c.role == claim.RoleArchitect:
			continue
		case c.live():
			how = "told to end its turn"
			if err := e.enqueue(ctx, tx, c.issue.Key, record.RoleMessage{Role: string(c.token), Body: workCloseMessage(hold, false), Stop: &stop}); err != nil {
				return err
			}
			told++
		case c.role == claim.RoleMerger && c.state().Released():
			how = "woken from its release to end its turn"
			if err := e.enqueue(ctx, tx, c.issue.Key, record.RoleMessage{Role: string(c.token), Body: workCloseMessage(hold, false),
				Wake: true, Stop: &stop}); err != nil {
				return err
			}
			told++
		case c.state().Released():
			how = "held over its release at once"
			if err := e.enqueue(ctx, tx, c.issue.Key, stop); err != nil {
				return err
			}
		default:
			continue
		}
		stopped++
		e.logOnCommit(ctx, "workflow: the work hold stops a phase worker of the tree", "issue", c.issue.Key, "role", c.role,
			"version", version, "tree", root.Tree, "step", step, "how", how)
	}
	e.logOnCommit(ctx, "workflow: the work hold holds the tree's work", "tree", root.Tree, "step", step, "document", document,
		"version", version, "told", told, "stopped", stopped)
	return nil
}

// workHoldOpen is the work hold's end at the gate's move to open under `work` (spec step 7,
// decision 18): a person's approval of version of step's document, or Dispatch's carried approval
// after an exact undo (F3). Every live architect and phase worker of the tree is told today's
// open message, a worker whose stop the outbox has not made among them, since that stop is not
// made now (row H32); then the hold ends (endWorkHold).
func (e *Engine) workHoldOpen(ctx context.Context, tx pgx.Tx, root record.Issue, step record.DesignStep, document string, version int) error {
	claims, err := e.treeClaims(ctx, tx, root)
	if err != nil {
		return err
	}
	opened := gateOpenedMessage(root.Tree, step, document, version)
	told, err := e.tellLiveHolders(ctx, tx, claims, opened)
	if err != nil {
		return err
	}
	e.logOnCommit(ctx, "workflow: the design gate opened and the work hold ends", "tree", root.Tree, "step", step,
		"document", document, "version", version, "told", told)
	fallback := &record.WorkHold{Project: root.Project, Tree: root.Tree, Step: step, ArtifactID: document, Version: version}
	return e.endWorkHold(ctx, tx, root, claims, opened, fallback)
}

// tellLiveHolders tells every live architect and phase worker of claims body, and answers how many.
func (e *Engine) tellLiveHolders(ctx context.Context, tx pgx.Tx, claims []treeClaim, body string) (int, error) {
	told := 0
	for _, c := range claims {
		if !c.live() {
			continue
		}
		if err := e.enqueue(ctx, tx, c.issue.Key, record.RoleMessage{Role: string(c.token), Body: body}); err != nil {
			return told, err
		}
		told++
	}
	return told, nil
}

// endWorkHold ends root's work hold: the record goes, and each claim the hold stopped is started
// (openHeldClaims) with the opening's sentences around ended, what ended the hold. The record names
// the version that closed the gate; a tree whose record is gone already is named by fallback.
func (e *Engine) endWorkHold(ctx context.Context, tx pgx.Tx, root record.Issue, claims []treeClaim, ended string, fallback *record.WorkHold) error {
	closing, err := e.store.WorkHold(ctx, tx, root.Tree)
	if err != nil {
		return err
	}
	if closing == nil {
		closing = fallback
	}
	if err := e.store.DeleteWorkHold(ctx, tx, root.Tree); err != nil {
		return err
	}
	return e.openHeldClaims(ctx, tx, root, claims, openingSentences(closing, ended), closing)
}

// openHeldClaims starts each claim of claims the hold stopped (stored suspended by hold), and no
// other, with one task that starts with opening (decision 18, hold-decisions points 2 and 4; rows
// H10, H11, H14). A claim that holds its issue's unfinished phase is given its phase's work built
// as the boot builds it (startSuspendedOwner): the work of the entry its phases row names where
// the claim stands behind that entry — a start written for the role while the gate was closed,
// a backward move's or an owed entry's, whose row this start is written in place of, so the role
// gets one task, the newest entry's (Store.EnqueueStart, row H30) — and the phase's resume task
// where it does not. A claim that holds no phase is told that the issue's other roles may ask it
// questions. The start carries opening apart (record.SuperviseRequest.Opening): where the claim
// kept a task of its own, supervise puts those sentences in front of it instead
// (Machine.EndHold). A claim that already has such a start queued is given no second. Each start
// is one log line naming the issue, the role and the version that closed the gate (row H33).
func (e *Engine) openHeldClaims(ctx context.Context, tx pgx.Tx, root record.Issue, claims []treeClaim, opening string, closing *record.WorkHold) error {
	closedAt := 0
	if closing != nil {
		closedAt = closing.Version
	}
	for _, c := range claims {
		if c.role == claim.RoleArchitect || !c.state().WorkHeld() || openingQueued(c.run, root) {
			continue
		}
		holds := phase.RoleFor(c.issue.Phase) == c.role
		if holds {
			finished, err := e.phaseFinished(ctx, tx, c.issue)
			if err != nil {
				return err
			}
			holds = !finished
		}
		var err error
		switch {
		case holds:
			err = e.startHeldPhase(ctx, tx, c, opening)
		default:
			err = e.enqueue(ctx, tx, c.issue.Key, record.SuperviseRequest{Op: "start", Tree: c.issue.Tree, Role: c.role,
				Generation: c.issue.Generation, Task: opening + "\n\n" + noPhaseWork, Opening: opening})
		}
		if err != nil {
			return err
		}
		e.logOnCommit(ctx, "workflow: the work hold's opening starts a claim it stopped", "issue", c.issue.Key, "role", c.role,
			"version", closedAt, "tree", root.Tree, "phase", holds)
	}
	return nil
}

// startHeldPhase is the opening's start of c, a claim the hold stopped that holds its issue's
// phase: the work of the entry its phases row names where the claim stands behind it, and the
// phase's resume task where it does not, each behind opening.
func (e *Engine) startHeldPhase(ctx context.Context, tx pgx.Tx, c treeClaim, opening string) error {
	row, err := e.phaseRow(ctx, tx, c.issue.Key, c.role)
	if err != nil {
		return err
	}
	if GivenEntry(c.run) < row.Entry {
		return e.sendOwedEntry(ctx, tx, c.issue, c.role, row, startMarks{opening: opening})
	}
	return e.started(ctx, tx, c.issue, c.role, ResumePhaseTask(c.issue), 0, startMarks{resumeTask: true, reasserts: true, opening: opening})
}

// openingQueued is whether run already has a start of the opening queued that still acts: one
// carrying the opening's sentences, newer than any stop that will still act (actingStop).
func openingQueued(run record.RoleRun, root record.Issue) bool {
	stop := actingStop(run, root)
	for _, queued := range run.Queued {
		if queued.Request.Op == "start" && queued.Request.Opening != "" && queued.ID > stop {
			return true
		}
	}
	return false
}

// workHoldBooted is the boot's reconcile of the work hold over the project's running trees
// (booted; issues and running are what the boot read, record.RunningRoots). A lingering tree is
// left alone, since its linger suspended every claim of it, and so is a root still waiting for
// its tree to be opened. For each running tree:
//
//   - a record of an older generation of the root is dropped: the hold of a run that ended holds
//     no newer one (row H28);
//   - a tree that holds work (record.TreeGate.HoldsWork) with no record is closed now: the
//     deployment moved from `merge` to `work` while its gate stood closed (row H25);
//   - a tree that holds work with its record starts nothing: its drain is under way, and every
//     stop stands as its row stored it (row H24);
//   - a tree that holds no work with its record has its hold ended: the deployment moved to
//     `merge` or turned the gate off. Every live holder is told so, and each claim the hold
//     stopped starts with its task (row H26);
//   - and a tree that holds neither starts each claim the hold stopped that no start covers yet,
//     with no message.
func (e *Engine) workHoldBooted(ctx context.Context, tx pgx.Tx, issues []record.Issue, running map[string]struct{}) error {
	recorded, err := e.store.WorkHolds(ctx, tx, e.cfg.Project)
	if err != nil {
		return err
	}
	holds := make(map[string]record.WorkHold, len(recorded))
	for _, hold := range recorded {
		holds[hold.Tree] = hold
	}
	for _, root := range issues {
		if _, runs := running[root.Key]; !runs || !claim.IsTreeRoot(root.Key, root.Tree) {
			continue
		}
		hold, held := holds[root.Key]
		if held && hold.RootGeneration != root.Generation {
			if err := e.store.DeleteWorkHold(ctx, tx, root.Key); err != nil {
				return err
			}
			e.logOnCommit(ctx, "workflow: the boot drops the work hold of a generation of the tree that has ended", "tree", root.Key,
				"held", hold.RootGeneration, "generation", root.Generation)
			held = false
		}
		gates, err := e.store.GatesOf(ctx, tx, root.Key)
		if err != nil {
			return err
		}
		gate := record.TreeGate{Steps: e.cfg.Steps, Rows: gates}
		closed, holdsWork := gate.HoldsWork(e.holdsWork())
		switch {
		case holdsWork && !held:
			e.logOnCommit(ctx, "workflow: the boot applies the work hold to a tree whose standing gate stands closed", "tree", root.Key,
				"step", closed.Step.OrSpec(), "version", closed.LatestVersion)
			if err := e.workHoldClose(ctx, tx, root, closed.Step.OrSpec(), closed.ArtifactID, closed.LatestVersion); err != nil {
				return err
			}
		case holdsWork:
		case held:
			claims, err := e.treeClaims(ctx, tx, root)
			if err != nil {
				return err
			}
			ended := bootOpenedSentence(root.Key)
			told, err := e.tellLiveHolders(ctx, tx, claims, ended)
			if err != nil {
				return err
			}
			e.logOnCommit(ctx, "workflow: the boot ends the work hold of a tree the deployment no longer holds", "tree", root.Key,
				"hold", e.cfg.Hold, "told", told)
			if err := e.endWorkHold(ctx, tx, root, claims, ended, nil); err != nil {
				return err
			}
		default:
			claims, err := e.treeClaims(ctx, tx, root)
			if err != nil {
				return err
			}
			if err := e.openHeldClaims(ctx, tx, root, claims, openingSentences(nil, bootOpenedSentence(root.Key)), nil); err != nil {
				return err
			}
		}
	}
	return nil
}
