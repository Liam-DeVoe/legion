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
// (requireActorAndCredential), and never the actor that a body names. A bearer that proves no
// session names its session in the body, and that body can name any session
// (bearerSessionActor). The shared token whose body names the daemon's session is still the
// shared token, so R26 refuses it.
//
// The refusal takes no lock on an ancestor, and needs none. The walk is one statement, so it
// reads one snapshot of the chain. It runs after the write's own locks: the issue, its current
// and new parent (lockPatchOwners, lockPatchOwnersAndParent), and, for every parent write, the
// deployment-wide dependency lock (lockIssueDependencyWrite). So, under READ COMMITTED, it reads
// every write that those locks waited for. An ancestor that the write does not lock cannot break
// the rule, for this reason. With the refusal on, a caller with neither scope can make no issue
// stop being a Legion issue, because (b) and (c) refuse it. So a judgement read from a chain that
// moves under it can go stale in two ways only:
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

// legionStatusStep is one issue that a call changes, as R26 judges it: the issue's parent and
// labels before the call and after it, and whether the call writes the issue's status. For a
// call that changes neither the parent nor the labels, the after values are the before values.
type legionStatusStep struct {
	key string
	// writesStatus is true for a call that writes the issue's status, with any value, the value
	// the issue holds already included.
	writesStatus bool
	// parentBefore and parentAfter are the issue's parent before and after the call. Nil is no
	// parent.
	parentBefore, parentAfter *string
	// parentChanged is true for a call that writes the issue's parent, the current parent
	// included. A call that writes the current parent leaves the same chain, so rule (c) cannot
	// refuse it.
	parentChanged bool
	// labelsBefore and labelsAfter are the issue's labels before and after the call.
	labelsBefore, labelsAfter []string
}

// legionStatusWriter is the writer of a call, as R26 reads it.
type legionStatusWriter struct {
	// credential is the request's credential, as optionalActor resolved it
	// (requireActorAndCredential). A nil credential holds no scope.
	credential *model.Credential
	// system is true for Dispatch's own writer, which acts for no request and which R26 never
	// refuses. No request sets it. Today nothing sets it: the system writer of
	// dispatch://INFRA-4731 (parentStatusFixWriter) will set it.
	system bool
}

// refuseLegionStatusWrite is R26's refusal (this file's comment). It judges every step of one
// call, and the caller runs it once, before the call's first write, so a refused call changes
// nothing. on is Deps.LegionStatusScope: off refuses nothing. A refused step answers 403
// codeLegionStatusScopeRequired. A walk that fails is an error, which the caller answers 500: the
// walk gates a write, so a failed walk never lets the write through.
func refuseLegionStatusWrite(ctx context.Context, tx pgx.Tx, on bool, writer legionStatusWriter, steps ...legionStatusStep) error {
	if !on || writer.system || writer.credential.Holds(model.ScopePerson) || writer.credential.Holds(model.ScopeLegionStatus) {
		return nil
	}
	for _, step := range steps {
		if err := refuseLegionStatusStep(ctx, tx, step); err != nil {
			return err
		}
	}
	return nil
}

// refuseLegionStatusStep judges one step by R26's three rules. The label rule reads the labels
// alone, so it runs first and needs no query. The other two rules read whether the issue is a
// Legion issue: before the call at most once, and after the call only when a rule needs it.
func refuseLegionStatusStep(ctx context.Context, tx pgx.Tx, step legionStatusStep) error {
	// (b) The removal of the legion label.
	if holdsLegionLabel(step.labelsBefore) && !holdsLegionLabel(step.labelsAfter) {
		return legionStatusScopeRefusal(step.key, "the removal of the legion label")
	}
	if !step.writesStatus && !step.parentChanged {
		return nil
	}
	before, err := legionManagedStatusOfRow(ctx, tx, step.key, step.parentBefore, step.labelsBefore)
	if err != nil {
		return err
	}
	if step.writesStatus {
		// (a) A status write of an issue that is a Legion issue before the call or after it.
		if before {
			return legionStatusScopeRefusal(step.key, "a status write")
		}
		after, err := step.legionIssueAfter(ctx, tx, before)
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
	after, err := step.legionIssueAfter(ctx, tx, before)
	if err != nil {
		return err
	}
	if !after {
		return legionStatusScopeRefusal(step.key, "a move out from under every labelled issue")
	}
	return nil
}

// legionIssueAfter says whether the step's issue is a Legion issue after the call. before is the
// answer before the call. A call that changes neither the parent nor the labels leaves that
// answer, so it is answered without a second walk.
func (step legionStatusStep) legionIssueAfter(ctx context.Context, tx pgx.Tx, before bool) (bool, error) {
	if !step.parentChanged && slices.Equal(step.labelsBefore, step.labelsAfter) {
		return before, nil
	}
	return legionManagedStatusOfRow(ctx, tx, step.key, step.parentAfter, step.labelsAfter)
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
