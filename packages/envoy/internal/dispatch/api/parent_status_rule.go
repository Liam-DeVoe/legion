package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// group is one of the three groups that R24 puts each issue status in. The rule holds two
// things between a parent and each of its children: a parent is done only when each child is
// done, and while a child is in work, its parent is in work. The zero group is no group:
// statusGroup gives it for a string that is not a status.
type group string

const (
	// groupNotStarted holds the statuses of work that nobody started: triage, icebox and
	// backlog.
	groupNotStarted group = "not started"
	// groupInWork holds the statuses of work that is planned or that runs: todo, in_progress,
	// testing, needs_review and retro.
	groupInWork group = "in work"
	// groupDone holds done alone.
	groupDone group = "done"
)

// statusGroup is the group of an issue status of model.IssueStatuses.
func statusGroup(status string) group {
	switch status {
	case model.IssueStatusTriage, "icebox", "backlog":
		return groupNotStarted
	case "todo", "in_progress", "testing", "needs_review", "retro":
		return groupInWork
	case "done":
		return groupDone
	}
	return ""
}

// pairBreaksTheRule reports whether a child in childStatus under a parent in parentStatus breaks
// R24: the parent is done and the child is not, or the child is in work and the parent is not.
func pairBreaksTheRule(childStatus, parentStatus string) bool {
	child, parent := statusGroup(childStatus), statusGroup(parentStatus)
	return (parent == groupDone && child != groupDone) || (child == groupInWork && parent != groupInWork)
}

// ParentStatusRule is the setting DISPATCH_PARENT_STATUS_RULE as the server holds it. While it
// is on, a write that breaks R24 is refused 409 PARENT_STATUS_CONFLICT. Its one field is
// unexported, so outside this package only the zero value, which is off, can be made.
type ParentStatusRule struct{ on bool }

// On reports whether the rule refuses a write that breaks it.
func (rule ParentStatusRule) On() bool { return rule.on }

// parentStatusRuleActor is Dispatch's own system actor for R24 and R25. R25's fix writes as it,
// and it is the author of the comment that each option of a status change and each change of
// the fix leaves. A status that an option writes stays the caller's.
var parentStatusRuleActor = model.Actor{Kind: "system", ID: "parent-status-rule"}

// issueWriter is who makes a write of an issue, as refuseIssueWritePlan judges it.
type issueWriter struct {
	// actor is the actor the write records (actorFrom).
	actor model.Actor
	// credential is the credential of the caller that optionalActor resolved, which actor no
	// longer carries (model.Actor.Recorded). It is nil for the fix, which has no request.
	credential *model.Credential
	// human is true only for a request that presents no token and that the browser identity
	// names: a person at a browser (optionalActor's second answer).
	human bool
	// system is true only for R25's fix (parentStatusFixWriter). No request sets it.
	system bool
}

// requestWriter is the writer of a request: the actor that actorFrom records, the credential of
// the caller that optionalActor resolved, and whether that caller is a person at a browser. It
// resolves the caller once and refuses what actorFrom refuses.
func (s *server) requestWriter(r *http.Request, supplied *model.Actor) (issueWriter, error) {
	caller, human, err := s.optionalActor(r)
	if err != nil {
		return issueWriter{}, err
	}
	actor, err := recordedActor(r, caller, human, supplied)
	if err != nil {
		return issueWriter{}, err
	}
	return issueWriter{actor: actor, credential: caller.Credential, human: human}, nil
}

// parentStatusFixWriter is the writer of R25's fix, which only the boot fix uses: Dispatch's
// system actor, with no request and no credential.
func parentStatusFixWriter() issueWriter {
	return issueWriter{actor: parentStatusRuleActor, system: true}
}

// issueWritePlan is a write of the status or the parent of one or more issues, step by step, as
// refuseIssueWritePlan judges it before the first step writes.
type issueWritePlan struct {
	steps []issueWriteStep
	// includeChildren is that the call moves the open issues under the issue it names with that
	// issue (the status-change route's include_children). The PATCH never sets it.
	includeChildren bool
}

// issueWriteStep is the write of one issue in a plan: the issue as the plan read it under its
// owner lock, and what the step writes.
type issueWriteStep struct {
	key          string
	title        string
	statusBefore string
	// statusAfter is the status the step writes, or nil when the step writes no status.
	statusAfter *string
	// parentBefore is the parent the issue has before the step.
	parentBefore *string
	// reparent is that the step writes the parent: parentAfter, or no parent (an unlink) when
	// parentAfter is nil.
	reparent    bool
	parentAfter *string
	// labelsBefore is the labels the issue has before the step.
	labelsBefore []string
	// labelsAfter is the labels the step writes, or nil when the step keeps them. Only a PATCH
	// writes labels.
	labelsAfter *[]string
}

// changesStatus reports whether the step writes a status other than the one the issue has.
func (step issueWriteStep) changesStatus() bool {
	return step.statusAfter != nil && *step.statusAfter != step.statusBefore
}

// changesParent reports whether the step writes a parent other than the one the issue has.
func (step issueWriteStep) changesParent() bool {
	return step.reparent && !stringPointersEqual(step.parentBefore, step.parentAfter)
}

// issueWritePlanOfPatch is the plan of a PATCH: one step, from the status, the parent and the
// labels of its body, over the issue as the PATCH read it under its owner locks. status is nil
// when the body names no status, and labels is nil when the body names no labels.
func issueWritePlanOfPatch(before model.Issue, status *string, parentProvided bool, parent *string, labels *[]string) issueWritePlan {
	return issueWritePlan{steps: []issueWriteStep{{
		key:          before.Key,
		title:        before.Title,
		statusBefore: before.Status,
		statusAfter:  status,
		parentBefore: before.Parent,
		reparent:     parentProvided,
		parentAfter:  parent,
		labelsBefore: before.Labels,
		labelsAfter:  labels,
	}}}
}

// refuseIssueWritePlan is the one check of a plan, which its caller runs before the plan's first
// write, with the owner lock of each step's issue and of each step's current and new parent held.
// It refuses with an error what no setting lets through, and it always answers the conflict with
// R24 (parentStatusConflictOf), or nil when the plan keeps the rule. The caller refuses that
// conflict only while the rule is on.
func (s *server) refuseIssueWritePlan(ctx context.Context, q pgx.Tx, writer issueWriter, plan issueWritePlan) (*parentStatusConflict, error) {
	// The guard of who may write the status of a Legion issue comes first here.
	if plan.includeChildren && !writer.human && plan.writesStatus("done") {
		return nil, errorf(http.StatusForbidden, "BROWSER_SIGN_IN_REQUIRED",
			"only a person signed in at a browser can close the open issues under an issue with it (include_children with done); this request presents a token")
	}
	return parentStatusConflictOf(ctx, q, plan)
}

// writesStatus reports whether a step of the plan writes status.
func (plan issueWritePlan) writesStatus(status string) bool {
	for _, step := range plan.steps {
		if step.statusAfter != nil && *step.statusAfter == status {
			return true
		}
	}
	return false
}

// plannedIssue is an issue that a plan involves, as q read it and with each step of the plan
// written over it.
type plannedIssue struct {
	key, title, status string
	parent             *string
}

// parentStatusConflictOf judges the pairs that the plan changes (D2) on the state after every
// step: it reads the issues involved (each judged step's issue, its parent, and the direct
// children of each issue whose status a step changes) and writes each step's status and parent
// over them. A status change judges the issue with its parent and with each direct child. A
// change of parent judges the issue with its new parent. A pair that no step changes is not
// judged, so a plan that changes no pair reads nothing. The issues are listed in the order of the
// steps, a parent before the children, and the children in the order of their numbers.
func parentStatusConflictOf(ctx context.Context, q pgx.Tx, plan issueWritePlan) (*parentStatusConflict, error) {
	var judged []issueWriteStep
	var keys, parentsOfChildren []string
	for _, step := range plan.steps {
		if !step.changesStatus() && !step.changesParent() {
			continue
		}
		judged = append(judged, step)
		keys = append(keys, step.key)
		if step.parentBefore != nil {
			keys = append(keys, *step.parentBefore)
		}
		if step.parentAfter != nil {
			keys = append(keys, *step.parentAfter)
		}
		if step.changesStatus() {
			parentsOfChildren = append(parentsOfChildren, step.key)
		}
	}
	if len(judged) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		select key, title, status, parent_key from issues
		where key = any($1) or parent_key = any($2)
		order by project_key, number
	`, keys, parentsOfChildren)
	if err != nil {
		return nil, fmt.Errorf("read the issues of a write's parent-status pairs: %w", err)
	}
	read, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*plannedIssue, error) {
		var issue plannedIssue
		err := row.Scan(&issue.key, &issue.title, &issue.status, &issue.parent)
		return &issue, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the issues of a write's parent-status pairs: %w", err)
	}
	issues := make(map[string]*plannedIssue, len(read))
	for _, issue := range read {
		issues[issue.key] = issue
	}
	for _, step := range plan.steps {
		issue, ok := issues[step.key]
		if !ok {
			issue = &plannedIssue{key: step.key, title: step.title, status: step.statusBefore, parent: step.parentBefore}
			issues[step.key] = issue
		}
		if step.statusAfter != nil {
			issue.status = *step.statusAfter
		}
		if step.reparent {
			issue.parent = step.parentAfter
		}
	}
	conflict := &parentStatusConflict{}
	listed := map[[2]string]bool{}
	list := func(holder *plannedIssue, relation string, of *plannedIssue, reason string) {
		if listed[[2]string{of.key, holder.key}] {
			return
		}
		listed[[2]string{of.key, holder.key}] = true
		conflict.issues = append(conflict.issues, parentStatusConflictIssue{
			Key: holder.key, Title: holder.title, Status: holder.status, Relation: relation, of: of.key, reason: reason,
		})
	}
	for _, step := range judged {
		issue := issues[step.key]
		if issue.parent != nil {
			if parent, ok := issues[*issue.parent]; ok && pairBreaksTheRule(issue.status, parent.status) {
				list(parent, relationParent, issue, parentReason(parent.status))
			}
		}
		if !step.changesStatus() {
			continue
		}
		for _, child := range read {
			if child.parent != nil && *child.parent == issue.key && pairBreaksTheRule(child.status, issue.status) {
				list(child, relationChild, issue, childReason(issue.status))
			}
		}
	}
	if len(conflict.issues) == 0 {
		return nil, nil
	}
	return conflict, nil
}

// openChildrenOfAClose is the second read of a close with the rule on (D4): the children of key
// that are not done, read in the close's write phase after its own update of key's row. A create
// under key holds no owner lock of key, so the close's first check cannot see a child whose create
// has not committed. The create's share lock of key's row makes the close's update wait for that
// commit, so this read, the statement after the update, sees the child.
func openChildrenOfAClose(ctx context.Context, tx pgx.Tx, key string) (*parentStatusConflict, error) {
	rows, err := tx.Query(ctx, `
		select key, title, status from issues where parent_key = $1 and status <> 'done' order by project_key, number
	`, key)
	if err != nil {
		return nil, fmt.Errorf("read the open children of a close: %w", err)
	}
	children, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (parentStatusConflictIssue, error) {
		child := parentStatusConflictIssue{Relation: relationChild, of: key, reason: childReason("done")}
		err := row.Scan(&child.Key, &child.Title, &child.Status)
		return child, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the open children of a close: %w", err)
	}
	if len(children) == 0 {
		return nil, nil
	}
	return &parentStatusConflict{issues: children}, nil
}

// createParentConflict is the conflict of a new issue, which is in triage, with a parent in
// parentStatus: only a done parent holds the rule against it.
func createParentConflict(parentKey, parentTitle, parentStatus string) *parentStatusConflict {
	if !pairBreaksTheRule(model.IssueStatusTriage, parentStatus) {
		return nil
	}
	return &parentStatusConflict{issues: []parentStatusConflictIssue{{
		Key: parentKey, Title: parentTitle, Status: parentStatus, Relation: relationParent, reason: parentReason(parentStatus),
	}}}
}

// The relations of an issue that holds R24 against a write to the issue that the write judged.
const (
	relationParent = "parent"
	relationChild  = "child"
)

// parentReason is why a parent in parentStatus holds the rule against its child: it is done, or,
// for a child in work, it is not in work.
func parentReason(parentStatus string) string {
	if statusGroup(parentStatus) == groupDone {
		return "is done"
	}
	return "is not in work"
}

// childReason is why a child holds the rule against its parent in parentStatus: the parent is
// done and the child is not, or the child is in work and the parent is not.
func childReason(parentStatus string) string {
	if statusGroup(parentStatus) == groupDone {
		return "is not done"
	}
	return "is in work"
}

// parentStatusConflict is a write that R24 refuses, with each issue that holds the rule against
// it. writeHandlerError answers it 409 PARENT_STATUS_CONFLICT.
type parentStatusConflict struct {
	issues []parentStatusConflictIssue
	// changed is what a call of several steps answers for the steps that it committed before
	// the step that is refused. It is nil for the PATCH and the create, which write one issue,
	// and their body leaves it out.
	changed any
}

// parentStatusConflictIssue is an issue that holds R24 against a write: the issue at the other
// end of a pair that the write would break, with its status as the write would leave it.
type parentStatusConflictIssue struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// Relation is what this issue is to the issue that the write judged: its parent or its child.
	Relation string `json:"relation"`
	// of is the key of the issue that the write judged. It is empty for the issue that a create
	// files, which has no key yet.
	of string
	// reason is why this issue holds the rule against that issue, as the error text says it.
	reason string
}

// parentStatusConflictBody is the 409 PARENT_STATUS_CONFLICT body.
type parentStatusConflictBody struct {
	Code    string                      `json:"code"`
	Error   string                      `json:"error"`
	Issues  []parentStatusConflictIssue `json:"issues"`
	Changed any                         `json:"changed,omitempty"`
}

// Error names each issue that holds the rule with its status, so a client that reads no issues
// still shows them. Each issue is a sentence of its own.
func (conflict *parentStatusConflict) Error() string {
	sentences := make([]string, 0, len(conflict.issues)+2)
	sentences = append(sentences, "this change would leave a parent's status out of line with its children's.")
	for _, issue := range conflict.issues {
		of := issue.of
		if of == "" {
			of = "the new issue"
		}
		kin := "a child of"
		if issue.Relation == relationParent {
			kin = "the parent of"
		}
		sentences = append(sentences, fmt.Sprintf("%s (%s) is %s %s and %s.", issue.Key, issue.Status, kin, of, issue.reason))
	}
	sentences = append(sentences, "A parent is done only when each child is done, and while a child is in work, its parent is in work.")
	return strings.Join(sentences, " ")
}

// body is the conflict as writeHandlerError answers it.
func (conflict *parentStatusConflict) body() parentStatusConflictBody {
	return parentStatusConflictBody{
		Code:    "PARENT_STATUS_CONFLICT",
		Error:   conflict.Error(),
		Issues:  conflict.issues,
		Changed: conflict.changed,
	}
}
