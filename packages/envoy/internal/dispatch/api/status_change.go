package api

// status_change.go is POST /api/v1/issues/{key}/status-change: a status change of one issue that
// names, with an option, what happens to the open issues under it or to the issues above it, so
// that every parent's status stays in line with its children's (R24, parent_status_rule.go).
//
// The call is a sequence of single-issue writes, each one the PATCH's own write (writeIssue) in a
// transaction of its own, in an order where every write keeps the rule: the ancestors that
// include_parent moves, highest first; the unlinks of unlink_children; for a status that is done
// or not started, the descendants that include_children moves, deepest first; the issue the call
// names; and for a status in work, the descendants that include_children moves, highest first.
// Before the first write, the call checks its whole plan under the owner locks of every issue the
// plan writes (planStatusChange). Each write then decides what it writes from what it reads under
// its own lock, so a write that another writer made unneeded writes nothing.
//
// A keyed call keeps its progress in its request key's recovery point (statusChangeRecovery, D7):
// each step that writes adds its entry there in its own transaction, and the call marks the point
// finished after its last step. A repeat of a call that finished is answered from the point
// (replayStatusChange). A repeat that finds the holder gone before the mark takes the key over
// (claimRequestKey), and that attempt runs only the steps the point does not hold.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// statusChangeRoute is the route's own name, which the advice of each write names.
const statusChangeRoute = "POST /api/v1/issues/{key}/status-change"

// The three options of a status change, as its body names them.
const (
	optionIncludeChildren = "include_children"
	optionUnlinkChildren  = "unlink_children"
	optionIncludeParent   = "include_parent"
)

// statusChangeRuleSentence is the rule as each comment of an option states it.
const statusChangeRuleSentence = "Dispatch keeps a parent's status in line with its children's: a parent is done only when " +
	"each child is done, and while a child is in work its parent is in work too."

// statusChangeInput is the body of POST /api/v1/issues/{key}/status-change.
type statusChangeInput struct {
	// Status is the status the call writes on the issue it names.
	Status string `json:"status" required:"true"`
	// IncludeChildren writes the status on each open issue under the issue, at any depth, that is
	// not in the status's group.
	IncludeChildren bool `json:"include_children,omitempty"`
	// UnlinkChildren unlinks each open child of the issue, so each one becomes a root.
	UnlinkChildren bool `json:"unlink_children,omitempty"`
	// IncludeParent moves each ancestor of the issue that is not in work to in_progress.
	IncludeParent bool `json:"include_parent,omitempty"`
	// DryRun answers what the call would change, and writes nothing.
	DryRun bool `json:"dry_run,omitempty"`
	// ExpectedSeq makes the call conditional, as on the PATCH: 409 ISSUE_CHANGED when the
	// issue's last event is no longer that sequence.
	ExpectedSeq *int64 `json:"expected_sequence"`
	// Actor names the writer, as on the PATCH.
	Actor *model.Actor `json:"actor"`
}

// statusChangeAnswer is the 200 answer of the route: the issue the call names, as the PATCH
// answers it, and each issue the call changed.
type statusChangeAnswer struct {
	Issue   model.Issue         `json:"issue"`
	Changed []statusChangeEntry `json:"changed"`
}

// statusChangeEntry is one issue whose status or parent the call changed, in the order of the
// writes: the status and the parent the call left it with, its last_seq as its own write read it
// under its owner lock, and the issue as that write answered it. A dry run's entry holds the
// status and the parent the call would leave, the issue's last_seq now, and no issue.
type statusChangeEntry struct {
	Key             string       `json:"key"`
	Status          string       `json:"status"`
	Parent          *string      `json:"parent"`
	PreviousLastSeq int          `json:"previous_last_seq"`
	Issue           *model.Issue `json:"issue,omitempty"`
}

// statusChangeRefusal is every refusal of the route but 409 PARENT_STATUS_CONFLICT: its code and
// its message, and the entries of the writes that committed before it ([] when none did).
type statusChangeRefusal struct {
	Code    string              `json:"code"`
	Error   string              `json:"error"`
	Changed []statusChangeEntry `json:"changed"`
}

// statusChangeConflict is the route's 409 PARENT_STATUS_CONFLICT: the rule's refusal
// (parentStatusConflictBody), and the entries of the writes that committed before it.
type statusChangeConflict struct {
	Code    string                      `json:"code"`
	Error   string                      `json:"error"`
	Issues  []parentStatusConflictIssue `json:"issues"`
	Changed []statusChangeEntry         `json:"changed"`
}

// statusChange is a status change's body as the route checked it.
type statusChange struct {
	status                                         string
	includeChildren, unlinkChildren, includeParent bool
	dryRun                                         bool
	expectedSeq                                    *int64
}

// parseStatusChange checks the body: a status of the lifecycle, and at most one of the two
// options that act on the issues under the issue.
func parseStatusChange(input statusChangeInput) (statusChange, error) {
	status := strings.TrimSpace(input.Status)
	if status == "" {
		return statusChange{}, errorf(http.StatusBadRequest, "STATUS_CHANGE_INPUT", "status is required")
	}
	if !model.IsIssueStatus(status) {
		return statusChange{}, errorf(http.StatusBadRequest, "INVALID_STATUS", "status is not in the Legion lifecycle")
	}
	if input.IncludeChildren && input.UnlinkChildren {
		return statusChange{}, errorf(http.StatusBadRequest, "STATUS_CHANGE_INPUT",
			"include_children and unlink_children cannot both be set: pick what happens to the open issues under the issue")
	}
	return statusChange{
		status:          status,
		includeChildren: input.IncludeChildren,
		unlinkChildren:  input.UnlinkChildren,
		includeParent:   input.IncludeParent,
		dryRun:          input.DryRun,
		expectedSeq:     input.ExpectedSeq,
	}, nil
}

// changeIssueStatus is POST /api/v1/issues/{key}/status-change. It plans the call and checks the
// plan under its owner locks (planStatusChange), answers a dry run from that plan, and otherwise
// runs each step through the PATCH's write core. A refused step stops the call: no later step
// runs, and the refusal carries the entries of the steps that committed before it. An attempt
// that took over a key an earlier attempt left unanswered resumes the call from that attempt's
// recovery point (resumedStatusChange).
func (s *server) changeIssueStatus(w http.ResponseWriter, r *http.Request) {
	none := []statusChangeEntry{}
	var input statusChangeInput
	if err := decodeJSON(r, &input); err != nil {
		s.writeStatusChangeRefusal(w, err, none)
		return
	}
	writer, err := s.requestWriter(r, input.Actor)
	if err != nil {
		s.writeStatusChangeRefusal(w, err, none)
		return
	}
	change, err := parseStatusChange(input)
	if err != nil {
		s.writeStatusChangeRefusal(w, err, none)
		return
	}
	ctx, key := r.Context(), r.PathValue("key")
	resumed, err := s.resumedStatusChange(ctx)
	if err != nil {
		s.writeStatusChangeRefusal(w, err, none)
		return
	}
	callerProject, _ := s.callerProject(r)
	run := &statusChangeRun{
		server: s, key: key, change: change, writer: writer,
		callerProject: callerProject, outliving: outlivingTheClient(r), resumed: resumed, pointed: resumed != nil,
	}
	plan, err := s.planStatusChange(ctx, key, change, writer, resumed)
	// A dry run answers what the call answers with the rule on, whatever the setting.
	if err == nil && plan.conflict != nil && (change.dryRun || s.deps.ParentStatusRule.On()) {
		err = plan.conflict
	}
	if err == nil && change.dryRun {
		WriteJSON(w, http.StatusOK, plan.dryRunAnswer())
		return
	}
	var ran []statusChangeEntry
	if err == nil {
		ran, err = run.steps(ctx, plan.steps)
	}
	run.answer(ctx, w, ran, err)
}

// statusChangeStep is one step of a status change's plan: the write of one issue, the option that
// makes it (empty for the named issue's own step), and the issue's last_seq as the plan read it,
// which a dry run answers.
type statusChangeStep struct {
	issueWriteStep
	option  string
	lastSeq int
}

// statusChangePlan is a status change as its planning transaction read it under its owner locks:
// the issue the call names, every step in the order the steps run, and the rule's conflict with
// the whole plan, nil when the plan keeps the rule.
type statusChangePlan struct {
	named    model.Issue
	steps    []statusChangeStep
	conflict *parentStatusConflict
}

// dryRunAnswer is what a dry run answers: each issue the call would change with the status and
// the parent the call would leave and its last_seq now.
func (plan statusChangePlan) dryRunAnswer() statusChangeAnswer {
	changed := make([]statusChangeEntry, 0, len(plan.steps))
	for _, step := range plan.steps {
		entry := statusChangeEntry{Key: step.key, Status: step.statusBefore, Parent: step.parentBefore, PreviousLastSeq: step.lastSeq}
		if step.statusAfter != nil {
			entry.Status = *step.statusAfter
		}
		if step.reparent {
			entry.Parent = step.parentAfter
		}
		changed = append(changed, entry)
	}
	return statusChangeAnswer{Issue: plan.named, Changed: changed}
}

// statusChangeIssue is an issue that a status change reads to plan its steps.
type statusChangeIssue struct {
	key, status string
	parent      *string
}

// statusChangeRead is what a status change's plan is made from: the issue the call names, its
// ancestors, parent first, when the call passes include_parent, and every issue under it when
// the call passes include_children or unlink_children.
type statusChangeRead struct {
	named       statusChangeIssue
	ancestors   []statusChangeIssue
	descendants []refs.IssueDescendant
}

// readStatusChange reads what the plan of change on the issue key is made from, with plain reads
// through q. An issue no row has is pgx.ErrNoRows, which answers 404 as the PATCH answers it.
func readStatusChange(ctx context.Context, q queryer, key string, change statusChange) (statusChangeRead, error) {
	read := statusChangeRead{named: statusChangeIssue{key: key}}
	if err := q.QueryRow(ctx, `select status, parent_key from issues where key = $1`, key).Scan(&read.named.status, &read.named.parent); err != nil {
		return statusChangeRead{}, err
	}
	if change.includeParent {
		above, err := refs.IssueAncestors(ctx, q, key)
		if err != nil {
			return statusChangeRead{}, err
		}
		rows, err := readStatusChangeRows(ctx, q, above)
		if err != nil {
			return statusChangeRead{}, err
		}
		for _, ancestor := range above {
			if row, ok := rows[ancestor]; ok {
				read.ancestors = append(read.ancestors, statusChangeIssue{key: ancestor, status: row.status, parent: row.parent})
			}
		}
	}
	if change.includeChildren || change.unlinkChildren {
		descendants, err := refs.IssueDescendantRows(ctx, q, key)
		if err != nil {
			return statusChangeRead{}, err
		}
		read.descendants = descendants
	}
	return read, nil
}

// statusChangeRow is an issue's row as a status change's plan reads it.
type statusChangeRow struct {
	key, title, status string
	parent             *string
	lastSeq            int
}

// readStatusChangeRows reads the rows of the issues keys, by key.
func readStatusChangeRows(ctx context.Context, q queryer, keys []string) (map[string]statusChangeRow, error) {
	rows, err := q.Query(ctx, `select key, title, status, parent_key, last_seq from issues where key = any($1)`, keys)
	if err != nil {
		return nil, fmt.Errorf("read the issues of a status change: %w", err)
	}
	collected, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (statusChangeRow, error) {
		var read statusChangeRow
		err := row.Scan(&read.key, &read.title, &read.status, &read.parent, &read.lastSeq)
		return read, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the issues of a status change: %w", err)
	}
	read := make(map[string]statusChangeRow, len(collected))
	for _, row := range collected {
		read[row.key] = row
	}
	return read, nil
}

// steps is the plan's steps over read, in the order they run. Each option acts whenever the call
// passes it, whether or not the rule needs it.
func (read statusChangeRead) steps(change statusChange) []statusChangeStep {
	var steps []statusChangeStep
	if change.includeParent {
		inProgress := "in_progress"
		for index := len(read.ancestors) - 1; index >= 0; index-- {
			ancestor := read.ancestors[index]
			if statusGroup(ancestor.status) == groupInWork {
				continue
			}
			steps = append(steps, statusChangeStep{option: optionIncludeParent, issueWriteStep: issueWriteStep{
				key: ancestor.key, statusBefore: ancestor.status, statusAfter: &inProgress, parentBefore: ancestor.parent,
			}})
		}
	}
	named := read.named.key
	if change.unlinkChildren {
		for _, child := range read.descendants {
			if child.Depth != 1 || statusGroup(child.Status) == groupDone {
				continue
			}
			steps = append(steps, statusChangeStep{option: optionUnlinkChildren, issueWriteStep: issueWriteStep{
				key: child.Key, statusBefore: child.Status, parentBefore: &named, reparent: true,
			}})
		}
	}
	var moved []statusChangeStep
	if change.includeChildren {
		for _, descendant := range read.descendants {
			if statusGroup(descendant.Status) == groupDone || statusGroup(descendant.Status) == statusGroup(change.status) {
				continue
			}
			parent := descendant.Parent
			moved = append(moved, statusChangeStep{option: optionIncludeChildren, issueWriteStep: issueWriteStep{
				key: descendant.Key, statusBefore: descendant.Status, statusAfter: &change.status, parentBefore: &parent,
			}})
		}
	}
	intoWork := statusGroup(change.status) == groupInWork
	if !intoWork {
		// Deepest first, so each issue's children are moved before it is.
		depths := make(map[string]int, len(read.descendants))
		for _, descendant := range read.descendants {
			depths[descendant.Key] = descendant.Depth
		}
		slices.SortStableFunc(moved, func(a, b statusChangeStep) int { return cmp.Compare(depths[b.key], depths[a.key]) })
		steps = append(steps, moved...)
	}
	steps = append(steps, statusChangeStep{issueWriteStep: issueWriteStep{
		key: named, statusBefore: read.named.status, statusAfter: &change.status, parentBefore: read.named.parent,
	}})
	if intoWork {
		steps = append(steps, moved...)
	}
	return steps
}

// statusChangeLocks is the owner locks a plan's steps need: each step's issue, and its current and
// new parent, in key order.
func statusChangeLocks(steps []statusChangeStep) []string {
	var keys []string
	for _, step := range steps {
		keys = append(keys, step.key)
		if step.parentBefore != nil {
			keys = append(keys, *step.parentBefore)
		}
		if step.parentAfter != nil {
			keys = append(keys, *step.parentAfter)
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// planStatusChange is the plan of change on the issue key, checked under its owner locks before
// any step writes (D16). It reads the plan with plain reads, then, in a transaction of its own
// that writes nothing, takes the owner locks of every step's issue and of each step's current and
// new parent in one store.LockOwners call. Under them it loads the issue the call names and
// decides expected_sequence first (409 ISSUE_CHANGED), then the closed-issue gate as the PATCH has
// it (409 ISSUE_CLOSED), reads the plan again, and checks the whole plan (refuseIssueWritePlan). A
// plan whose issues moved between the two reads, so that it needs a lock the transaction does not
// hold, starts over with what the second read found, as lockPatchOwnersAndParent does, and a plan
// that moves at every attempt is 409 PARENT_CONTENDED with nothing applied. The transaction rolls
// back before the first step, and each step takes its own locks in its own transaction. resumed is
// the recovery point of an earlier attempt that this attempt resumes (D7), nil for a first
// attempt: it changes the fence, the gate and the plan as planStatusChangeUnderLocks says.
func (s *server) planStatusChange(
	ctx context.Context, key string, change statusChange, writer issueWriter, resumed *statusChangeRecovery,
) (statusChangePlan, error) {
	pool, err := s.readyPool()
	if err != nil {
		return statusChangePlan{}, err
	}
	read, err := readStatusChange(ctx, pool, key, change)
	if err != nil {
		return statusChangePlan{}, err
	}
	for range parentLockAttempts {
		plan, moved, err := s.planStatusChangeUnderLocks(ctx, key, change, writer, read, resumed)
		if err != nil || moved == nil {
			return plan, err
		}
		read = *moved
	}
	return statusChangePlan{}, errorf(http.StatusConflict, parentContended,
		"the issues that the status change of %s writes changed at each of %d attempts while this request waited to lock them, so nothing was applied: read the issue and retry",
		key, parentLockAttempts)
}

// planStatusChangeUnderLocks is one attempt of planStatusChange, which locks the owners the plan
// over read needs. It answers the plan, or, when the second read needs a lock the attempt does not
// hold, that read, for the next attempt to lock.
//
// An attempt that resumes a call from resumed, its recovery point, decides the fence by event ids
// rather than by the PATCH's equality, which the earlier attempt's own events moved: an event on
// the named issue after expected_sequence that the point does not hold is 409 ISSUE_CHANGED, as
// the uninterrupted call would have answered at its next step. It plans every step of the call
// whose entry the point does not hold, since a step counts as made only when the point holds its
// entry, and runs no closed-issue gate on the named issue when the point holds its entry: the
// call's own close closed it.
func (s *server) planStatusChangeUnderLocks(
	ctx context.Context, key string, change statusChange, writer issueWriter, read statusChangeRead, resumed *statusChangeRecovery,
) (statusChangePlan, *statusChangeRead, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return statusChangePlan{}, nil, err
	}
	defer tx.Rollback(ctx)
	held := statusChangeLocks(read.steps(change))
	owners := make([]store.Owner, 0, len(held))
	for _, owner := range held {
		owners = append(owners, store.IssueOwner(owner))
	}
	if err := store.LockOwners(ctx, tx, owners...); err != nil {
		return statusChangePlan{}, nil, err
	}
	named, err := s.loadIssue(ctx, tx, key)
	if err != nil {
		return statusChangePlan{}, nil, err
	}
	if change.expectedSeq != nil {
		if resumed != nil {
			if err := resumed.fence(*change.expectedSeq).refuseForeignEvents(ctx, tx, key); err != nil {
				return statusChangePlan{}, nil, err
			}
		} else if *change.expectedSeq != int64(named.LastSeq) {
			return statusChangePlan{}, nil, errIssueChanged()
		}
	}
	if named.ClosedAt != nil && change.status == "done" && !resumed.holds(key) {
		return statusChangePlan{}, nil, errorf(http.StatusConflict, "ISSUE_CLOSED", "issue is closed")
	}
	again, err := readStatusChange(ctx, tx, key, change)
	if err != nil {
		return statusChangePlan{}, nil, err
	}
	steps := slices.DeleteFunc(again.steps(change), func(step statusChangeStep) bool { return resumed.holds(step.key) })
	for _, lock := range statusChangeLocks(steps) {
		if _, locked := slices.BinarySearch(held, lock); !locked {
			return statusChangePlan{}, &again, nil
		}
	}
	keys := make([]string, 0, len(steps))
	for _, step := range steps {
		keys = append(keys, step.key)
	}
	rows, err := readStatusChangeRows(ctx, tx, keys)
	if err != nil {
		return statusChangePlan{}, nil, err
	}
	writes := issueWritePlan{steps: make([]issueWriteStep, 0, len(steps)), includeChildren: change.includeChildren}
	for index := range steps {
		row := rows[steps[index].key]
		steps[index].title, steps[index].lastSeq = row.title, row.lastSeq
		writes.steps = append(writes.steps, steps[index].issueWriteStep)
	}
	conflict, err := s.refuseIssueWritePlan(ctx, tx, writer, writes)
	if err != nil {
		return statusChangePlan{}, nil, err
	}
	return statusChangePlan{named: named, steps: steps, conflict: conflict}, nil, nil
}

// statusChangeRun is one attempt of a status change that runs its steps. resumed is the recovery
// point of the earlier attempt this one resumes (resumedStatusChange), nil for a first attempt.
// pointed is that the request key holds a recovery point, which the call marks finished after its
// last step: the point this attempt resumed, or one a step of this keyed attempt wrote. named is
// the result of the named issue's own step, when this attempt ran it (namedRan).
type statusChangeRun struct {
	server        *server
	key           string
	change        statusChange
	writer        issueWriter
	callerProject string
	outliving     context.Context
	resumed       *statusChangeRecovery
	pointed       bool
	named         issueWriteResult
	namedRan      bool
}

// steps runs each step of the plan through the PATCH's write core, in a transaction of its own,
// and answers the entry of each issue it changed. A step refused stops the call: it answers that
// refusal, with the entries of the steps that committed before it. A call that sent
// expected_sequence is fenced at each step (issueWriteFence), with every event an earlier step
// appended on the issue the call names counted as the call's own, those of the point it resumes
// included. Each step of a keyed call that writes adds its entry to the call's recovery point in
// its own transaction (writeIssue, D7). The close of the issue the call names leaves its ask sweep
// to the call's answer (statusChangeRun.answer).
func (run *statusChangeRun) steps(ctx context.Context, steps []statusChangeStep) ([]statusChangeEntry, error) {
	s := run.server
	ran := []statusChangeEntry{}
	var fence *issueWriteFence
	if run.change.expectedSeq != nil {
		fence = run.resumed.fence(*run.change.expectedSeq)
	}
	_, keyed := ctx.Value(requestKeyClaimKey{}).(requestKeyClaim)
	for index, step := range steps {
		written, err := s.writeIssue(ctx, run.call(step, fence))
		if err != nil {
			return ran, err
		}
		if step.key == run.key {
			run.named, run.namedRan = written, true
		}
		if !written.wrote {
			continue
		}
		if fence != nil {
			for _, id := range written.namedEvents {
				fence.own[id] = true
			}
		}
		issue := written.issue
		ran = append(ran, statusChangeEntry{
			Key: issue.Key, Status: issue.Status, Parent: issue.Parent, PreviousLastSeq: written.previousLastSeq, Issue: &issue,
		})
		run.pointed = run.pointed || keyed
		if s.statusChangeAfterStep != nil {
			s.statusChangeAfterStep(ctx, index)
		}
	}
	return ran, nil
}

// answer ends the attempt, refused by err or not, with ran the entries of its own steps. After the
// last step it marks the call's recovery point finished, when the request key holds one
// (markStatusChangeFinished). Then it runs the ask sweep of the named issue's close (sweep), since
// a close that committed is owed its sweep whatever the answer, and answers changed: the entries
// of the point it resumed, each with its issue as it stands now, followed by ran. An attempt whose
// key another attempt took over sweeps nothing: the attempt that holds the key sweeps instead.
func (run *statusChangeRun) answer(ctx context.Context, w http.ResponseWriter, ran []statusChangeEntry, err error) {
	s := run.server
	if err == nil && run.pointed {
		err = s.markStatusChangeFinished(ctx)
	}
	if !errors.Is(err, errRequestKeyTakenOver) {
		run.sweep()
	}
	changed := []statusChangeEntry{}
	if run.resumed != nil {
		stored, readErr := s.statusChangeEntriesNow(ctx, *run.resumed)
		if readErr != nil {
			s.writeStatusChangeRefusal(w, readErr, ran)
			return
		}
		changed = append(changed, stored...)
	}
	changed = append(changed, ran...)
	if err != nil {
		s.writeStatusChangeRefusal(w, err, changed)
		return
	}
	issue := run.named.issue
	if !run.namedRan {
		// The point this attempt resumed holds the named issue's own step.
		if issue, err = s.loadIssue(ctx, s.deps.Store.Pool, run.key); err != nil {
			s.writeStatusChangeRefusal(w, err, changed)
			return
		}
	}
	WriteJSON(w, http.StatusOK, statusChangeAnswer{Issue: issue, Changed: changed})
}

// sweep runs the ask sweep of the named issue's close (sweepClosedIssueAsks), with no transaction
// open: of the close this attempt's own step made, with the asks it withdrew, or of the close the
// point it resumed holds, with the ids of the asks that close withdrew, which are all the sweep
// reads of them (sweepClosedIssueAsksTx).
func (run *statusChangeRun) sweep() {
	s := run.server
	switch {
	case run.named.closed:
		s.sweepClosedIssueAsks(run.outliving, run.key, run.named.withdrawn, run.writer.actor)
	case run.resumed.closes(run.key):
		withdrawn := make([]model.Ask, 0, len(run.resumed.Withdrawn))
		for _, id := range run.resumed.Withdrawn {
			withdrawn = append(withdrawn, model.Ask{ID: id})
		}
		s.sweepClosedIssueAsks(run.outliving, run.key, withdrawn, run.writer.actor)
	}
}

// call is the write of one step: the status or the unlink the step writes, as the caller writes
// it, and, for a step an option makes, the comment of Dispatch's system actor that names the call.
func (run *statusChangeRun) call(step statusChangeStep, fence *issueWriteFence) issueWriteCall {
	call := issueWriteCall{
		writer:        run.writer,
		key:           step.key,
		callerProject: run.callerProject,
		outliving:     run.outliving,
		adviceRoute:   statusChangeRoute,
		step:          &issueWriteCallStep{named: run.key, fence: fence, sweepsLater: step.key == run.key},
	}
	if step.reparent {
		call.fields.Parent = json.RawMessage("null")
	} else {
		status := *step.statusAfter
		call.fields.Status = &status
	}
	if step.option == "" {
		return call
	}
	call.comment = &issueComment{author: parentStatusRuleActor}
	caller, named, status := statusChangeCallerName(run.writer.actor), run.key, run.change.status
	because := fmt.Sprintf("because %s's status change of %s passed `%s`. %s", caller, named, step.option, statusChangeRuleSentence)
	switch step.option {
	case optionIncludeChildren:
		call.step.leaves = func(before model.Issue) bool {
			return statusGroup(before.Status) == groupDone || statusGroup(before.Status) == statusGroup(status)
		}
		call.step.words = func(before model.Issue) string {
			return fmt.Sprintf("Dispatch moved this issue from `%s` to `%s` with %s, %s", before.Status, status, named, because)
		}
	case optionIncludeParent:
		call.step.leaves = func(before model.Issue) bool { return statusGroup(before.Status) == groupInWork }
		call.step.words = func(before model.Issue) string {
			return fmt.Sprintf("Dispatch moved this issue from `%s` to `%s`, %s", before.Status, *step.statusAfter, because)
		}
	case optionUnlinkChildren:
		call.step.leaves = func(before model.Issue) bool {
			return statusGroup(before.Status) == groupDone || before.Parent == nil || *before.Parent != named
		}
		call.step.words = func(model.Issue) string {
			return fmt.Sprintf("Dispatch removed this issue from %s, %s", named, because)
		}
	}
	return call
}

// statusChangeCallerName is the caller a status change's comments name: a person's login, the
// person a token acts for, or else the session the write records.
func statusChangeCallerName(actor model.Actor) string {
	if actor.Kind != "user" && actor.Owner != nil {
		return *actor.Owner
	}
	return actor.ID
}

// writeStatusChangeRefusal answers err as every other route answers it (writeAuthenticationError,
// which answers every error that is not an identity's as writeHandlerError does), with changed
// beside the refusal: the entries of the steps that committed before it.
func (s *server) writeStatusChangeRefusal(w http.ResponseWriter, err error, changed []statusChangeEntry) {
	var conflict *parentStatusConflict
	if errors.As(err, &conflict) {
		body := conflict.body()
		WriteJSON(w, http.StatusConflict, statusChangeConflict{Code: body.Code, Error: body.Error, Issues: body.Issues, Changed: changed})
		return
	}
	refused := &refusalRecorder{header: http.Header{}, status: http.StatusOK}
	s.writeAuthenticationError(refused, err)
	var body errorBody
	if decodeErr := json.Unmarshal(refused.body.Bytes(), &body); decodeErr != nil {
		// Every refusal writeAuthenticationError writes is {code, error}, so this is a fault of
		// the server's own.
		s.writeHandlerError(w, fmt.Errorf("read the refusal of a status change: %w", decodeErr))
		return
	}
	maps.Copy(w.Header(), refused.header)
	WriteJSON(w, refused.status, statusChangeRefusal{Code: body.Code, Error: body.Error, Changed: changed})
}

// refusalRecorder holds the refusal a writer of refusals answers: its status, its headers and its
// body, for writeStatusChangeRefusal to answer again with changed beside it.
type refusalRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *refusalRecorder) Header() http.Header         { return r.header }
func (r *refusalRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *refusalRecorder) WriteHeader(status int)      { r.status = status }

// statusChangeRecovery is the recovery point of a keyed status change (D7, request_keys.go): what
// the steps that committed made, from which a repeat finishes the call. Each step that writes adds
// to it in its own transaction, with a statement that builds these members in its SQL
// (writeIssue), and the call marks it finished once its last step has run
// (markStatusChangeFinished). Go only reads it.
type statusChangeRecovery struct {
	// Changed is the entry of each step that wrote, in the order the steps ran, with no issue: an
	// answer from the point reads each issue as it stands then (statusChangeEntriesNow).
	Changed []statusChangeEntry `json:"changed"`
	// Events is the ids of every event the steps appended on the issue the call names, which the
	// fence of a resumed attempt counts as the call's own.
	Events []int64 `json:"events"`
	// Withdrawn is the ids of the asks that the close of the issue the call names withdrew, which
	// that close's ask sweep takes.
	Withdrawn []string `json:"withdrawn"`
	// Finished is the mark that every step of the call ran.
	Finished bool `json:"finished"`
}

// holds reports whether the point holds the entry of key's step, which counts that step as made.
// A nil point holds none.
func (point *statusChangeRecovery) holds(key string) bool {
	return point != nil && slices.ContainsFunc(point.Changed, func(entry statusChangeEntry) bool { return entry.Key == key })
}

// closes reports whether the point holds the close of named, the issue the call names, by its own
// step: an entry of named at done, which a step writes only when it closes the issue, since the
// PATCH's gate refuses done on an issue already closed.
func (point *statusChangeRecovery) closes(named string) bool {
	return point != nil && slices.ContainsFunc(point.Changed, func(entry statusChangeEntry) bool {
		return entry.Key == named && entry.Status == "done"
	})
}

// fence is the fence of a call that sent expected_sequence sequence, with every event the point
// holds counted as the call's own. A nil point holds none.
func (point *statusChangeRecovery) fence(sequence int64) *issueWriteFence {
	own := map[int64]bool{}
	if point != nil {
		for _, id := range point.Events {
			own[id] = true
		}
	}
	return &issueWriteFence{sequence: sequence, own: own}
}

// resumedStatusChange is the recovery point that an earlier attempt of the keyed request ctx
// carries left, when this attempt took that request's key over (claimRequestKey): the call this
// attempt finishes. It is nil for every other request: one with no key, a first attempt, and an
// attempt that took over a key with no point, which runs the call as a first attempt does.
func (s *server) resumedStatusChange(ctx context.Context) (*statusChangeRecovery, error) {
	claim, keyed := ctx.Value(requestKeyClaimKey{}).(requestKeyClaim)
	if !keyed || claim.attempt == 1 {
		return nil, nil
	}
	pool, err := s.readyPool()
	if err != nil {
		return nil, err
	}
	var recovery json.RawMessage
	err = pool.QueryRow(ctx, `
		select recovery from request_keys where login = $1 and idempotency_key = $2 and attempt = $3
	`, claim.login, claim.key, claim.attempt).Scan(&recovery)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errRequestKeyTakenOver
	}
	if err != nil {
		return nil, fmt.Errorf("read the recovery point of a status change: %w", err)
	}
	if recovery == nil {
		return nil, nil
	}
	var point statusChangeRecovery
	if err := json.Unmarshal(recovery, &point); err != nil {
		return nil, fmt.Errorf("read the recovery point of a status change: %w", err)
	}
	return &point, nil
}

// markStatusChangeFinished writes into the recovery point of the keyed status change ctx carries
// the mark that every step of the call ran, once the last step has run and before the call
// answers. A repeat that finds the mark is answered from the point and never takes the key over.
// It runs in a transaction of its own that s.begin opens, whose only write is to request_keys,
// which the commit step does not count (markRequestKeyCommitted). The statement is scoped to the
// request's own claim, so an attempt whose key another attempt took over is refused
// (errRequestKeyTakenOver). A request with no key marks nothing.
func (s *server) markStatusChangeFinished(ctx context.Context) error {
	claim, keyed := ctx.Value(requestKeyClaimKey{}).(requestKeyClaim)
	if !keyed {
		return nil
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		update request_keys set recovery = recovery || '{"finished": true}'::jsonb
		where login = $1 and idempotency_key = $2 and attempt = $3 and status is null
	`, claim.login, claim.key, claim.attempt)
	if err != nil {
		return fmt.Errorf("mark a status change finished: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errRequestKeyTakenOver
	}
	return tx.Commit(ctx)
}

// replayStatusChange answers a repeat of a keyed status change from its recovery point (D7): a
// call whose point carries the mark, or one that answered a success beside its point. It answers
// the 200 of the call with the entries the point holds, each entry's issue and the answer's issue
// read as they stand now. No step and no fence runs again, so a later write of the issue the call
// names, its close's ask sweep among them, changes no answer of a call that finished.
func (s *server) replayStatusChange(w http.ResponseWriter, r *http.Request, point statusChangeRecovery, _ []byte) {
	ctx := r.Context()
	changed, err := s.statusChangeEntriesNow(ctx, point)
	if err != nil {
		s.writeStatusChangeRefusal(w, err, []statusChangeEntry{})
		return
	}
	issue, err := s.loadIssue(ctx, s.deps.Store.Pool, r.PathValue("key"))
	if err != nil {
		s.writeStatusChangeRefusal(w, err, changed)
		return
	}
	WriteJSON(w, http.StatusOK, statusChangeAnswer{Issue: issue, Changed: changed})
}

// statusChangeEntriesNow is each entry point holds, in its order, with its issue as it stands now,
// as a replay and a resumed attempt answer them.
func (s *server) statusChangeEntriesNow(ctx context.Context, point statusChangeRecovery) ([]statusChangeEntry, error) {
	pool, err := s.readyPool()
	if err != nil {
		return nil, err
	}
	entries := make([]statusChangeEntry, 0, len(point.Changed))
	for _, entry := range point.Changed {
		issue, err := s.loadIssue(ctx, pool, entry.Key)
		if err != nil {
			return nil, err
		}
		entry.Issue = &issue
		entries = append(entries, entry)
	}
	return entries, nil
}
