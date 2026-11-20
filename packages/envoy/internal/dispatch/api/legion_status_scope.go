package api

// legion_status_scope.go is R26 of dispatch://INFRA-3320: the refusal of three writes of a Legion
// issue. A Legion issue carries the legion label, in any letter case, or is under an issue that
// carries it (legionManagedStatusOfRow). Where Deps.LegionStatusScope is on
// (DISPATCH_LEGION_STATUS_SCOPE), a credential that holds neither model.ScopePerson nor
// model.ScopeLegionStatus cannot make these writes:
//
//   - (a) A write of the issue's status, when the issue is a Legion issue before the call or
//     after it. A write of the status that the issue holds already is a status write too.
//   - (b) The removal of the legion label: the labels before the call hold it, in any letter
//     case, and the labels after the call do not.
//   - (c) A change of the parent that makes a Legion issue stop being a Legion issue.
//
// The Legion daemon owns a Legion issue's status. Its own broker token holds legion-status, and a
// person's own credential holds person. Every other credential holds neither: the shared token, a
// personal token without person, a project's broker token and a pod's Kubernetes token. R26
// refuses each of them.
//
// The refusal reads the request's credential, as optionalActor resolved it
// (requestWriter), and never the actor that a body names. A bearer that proves no
// session names its session in the body, and that body can name any session
// (bearerSessionActor). The shared token whose body names the daemon's session is still the
// shared token, so R26 refuses it.
//
// The refusal is the first statement of refuseIssueWritePlan (parent_status_rule.go). Every write
// that R26 judges runs that check before its first write.
// TestOnlyTheJudgedWritersWriteAnIssuesStatusLabelsOrParent names each writer of an issue's
// status, labels or parent, and why a writer that runs no such check needs none. So R26 judges
// every step of every plan, and it judges a call before R24 does.
//
// The refusal takes no lock on an ancestor, and needs none. The walk is one statement, so it
// reads one snapshot of the chain. It runs after the write's own locks, which the caller of
// refuseIssueWritePlan holds: the owner lock of each step's issue and of its current and new
// parent (the PATCH's lockPatchOwners, lockPatchOwnersAndParent), and, for every parent write,
// the deployment-wide dependency lock (lockIssueDependencyWrite). So, under READ COMMITTED, it
// reads every write that those locks waited for. An ancestor that the write does not lock cannot
// break the rule, for this reason. With the refusal on, a caller with neither scope can make no
// issue stop being a Legion issue, because (b) and (c) refuse it. So a judgement read from a
// chain that moves under it can go stale in two ways only:
//
//   - A concurrent write made the issue a Legion issue (a label added, a move under a labelled
//     issue). Then the write that the refusal let through comes first in a serial order, and
//     nothing broke.
//   - A concurrent write of a person, the daemon or the system writer made the issue stop being a
//     Legion issue. Then the refusal comes first in a serial order, and the refusal was right.
//
// A loop made of moves (X under C while C goes under X, or a longer ring) cannot commit. Every
// parent write takes the deployment-wide dependency lock, and validateIssueParent refuses the
// cycle under that lock (TestConcurrentThreeWayDependenciesDoNotCommitCycle,
// issue_dependencies_test.go). The PATCH answers PARENT_INPUT before this refusal, so this
// refusal never judges a chain that the call would close into a loop.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// codeLegionStatusScopeRequired refuses one of R26's three writes of a Legion issue to a
// credential that holds neither the person scope nor the legion-status scope. It is not
// codeScopeRequired, which refuses a credential that lacks the one scope a route or a field
// needs, whatever the write changes. This refusal depends on the issue: the same write of an
// issue that is not a Legion issue is taken, and either of two scopes lets the write through.
// Its challenge names legion-status, the scope that a writer which is not a person needs. No
// client acts on this code, so @legion/contracts holds no copy of it.
const codeLegionStatusScopeRequired = "LEGION_STATUS_SCOPE_REQUIRED"

// refuseLegionStatusWrite is R26's refusal (this file's comment). refuseIssueWritePlan runs it as
// its first statement, before the plan's first write, so a refused call changes nothing. It
// judges every step of the plan. on is Deps.LegionStatusScope: off refuses nothing.
//
// It lets two writers through. One is Dispatch's system writer (writer.system): only
// parentStatusFixWriter sets it, for R25's boot fix, and no request can produce it. The other is
// a writer whose credential holds the person scope or the legion-status scope. That credential is
// the one that optionalActor resolved for the request (requestWriter), and a nil credential holds
// no scope.
//
// A refused step answers 403 codeLegionStatusScopeRequired. A walk that fails is an error, which
// the caller answers 500: the walk gates a write, so a failed walk never lets the write through.
func refuseLegionStatusWrite(ctx context.Context, q pgx.Tx, on bool, writer issueWriter, plan issueWritePlan) error {
	if !on || writer.system || writer.credential.Holds(model.ScopePerson) || writer.credential.Holds(model.ScopeLegionStatus) {
		return nil
	}
	for _, step := range plan.steps {
		if err := refuseLegionStatusStep(ctx, q, step); err != nil {
			return err
		}
	}
	return nil
}

// refuseLegionStatusStep judges one step by R26's three rules. A step writes the status when it
// names a status (statusAfter), with any value, the value the issue holds already included. The
// label rule reads the labels alone, so it runs first and needs no query. The other two rules
// read whether the issue is a Legion issue: before the call at most once, and after the call
// only when a rule needs it.
func refuseLegionStatusStep(ctx context.Context, q pgx.Tx, step issueWriteStep) error {
	// (b) The removal of the legion label.
	if holdsLegionLabel(step.labelsBefore) && !holdsLegionLabel(step.labelsLeft()) {
		return legionStatusScopeRefusal(step.key, "the removal of the legion label")
	}
	writesStatus := step.statusAfter != nil
	if !writesStatus && !step.changesParent() {
		return nil
	}
	before, err := legionManagedStatusOfRow(ctx, q, step.key, step.parentBefore, step.labelsBefore)
	if err != nil {
		return err
	}
	if writesStatus {
		// (a) A status write of an issue that is a Legion issue before the call or after it.
		if before {
			return legionStatusScopeRefusal(step.key, "a status write")
		}
		after, err := step.legionIssueAfter(ctx, q, before)
		if err != nil {
			return err
		}
		if after {
			return legionStatusScopeRefusal(step.key, "a status write")
		}
		return nil
	}
	// (c) A parent write that makes a Legion issue stop being a Legion issue.
	if !before {
		return nil
	}
	after, err := step.legionIssueAfter(ctx, q, before)
	if err != nil {
		return err
	}
	if !after {
		return legionStatusScopeRefusal(step.key, "a move out from under every labelled issue")
	}
	return nil
}

// labelsLeft is the labels that the step leaves on its issue: the labels it writes, or the labels
// the issue has when the step keeps them.
func (step issueWriteStep) labelsLeft() []string {
	if step.labelsAfter != nil {
		return *step.labelsAfter
	}
	return step.labelsBefore
}

// parentLeft is the parent that the step leaves on its issue: the parent it writes, or the
// parent the issue has when the step writes none. Nil is no parent.
func (step issueWriteStep) parentLeft() *string {
	if step.reparent {
		return step.parentAfter
	}
	return step.parentBefore
}

// legionIssueAfter says whether the step's issue is a Legion issue after the call. before is the
// answer before the call. A step that changes neither the parent nor the labels leaves that
// answer, so it is answered without a second walk.
func (step issueWriteStep) legionIssueAfter(ctx context.Context, q pgx.Tx, before bool) (bool, error) {
	labels := step.labelsLeft()
	if !step.changesParent() && slices.Equal(step.labelsBefore, labels) {
		return before, nil
	}
	return legionManagedStatusOfRow(ctx, q, step.key, step.parentLeft(), labels)
}

// holdsLegionLabel says whether labels hold the legion label in any letter case. Labels keep the
// case they were typed in, and are de-duplicated without case (normalizeIssueLabels).
func holdsLegionLabel(labels []string) bool {
	return slices.ContainsFunc(labels, func(label string) bool { return strings.EqualFold(label, legionLabel) })
}

// legionStatusScopeRefusal is R26's refusal of write on key, a Legion issue. It is
// missingScopeError's refusal with R26's own code, so writeHandlerError writes the same challenge
// that RFC 6750 gives a credential without the legion-status scope (insufficientScopeChallenge).
func legionStatusScopeRefusal(key, write string) *apiError {
	refusal := missingScopeError(model.ScopeLegionStatus, fmt.Sprintf(
		"%s is a Legion issue (it, or an issue above it, carries the legion label): "+
			"%s needs a credential that holds the %s scope or the %s scope, and this request's holds neither",
		key, write, model.ScopePerson, model.ScopeLegionStatus))
	refusal.code = codeLegionStatusScopeRequired
	return refusal
}
