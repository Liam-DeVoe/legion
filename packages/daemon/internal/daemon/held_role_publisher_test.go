package daemon

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// A claim the boot could not suspend keeps its process, and that process keeps the Envoy role its
// session claimed, so the outbox publishes nothing to the role of a claim that records a session
// it does not hold (outbox.publish, reviews rounds 13 to 15 of #4220), whatever the claim's state:
// a role message to a ready claim and to a retired one finishes undelivered with the publisher
// never called. A message to a claim holding its own session, and to a topic of no claim this
// daemon supervises, is published as before (R9).
//
// The controls (the pull request records the runs): publish handing every topic to the publisher
// as it came publishes to both claims that do not hold their sessions.
func TestTheOutboxPublishesNothingToTheRoleOfAClaimThatDoesNotHoldItsSession(t *testing.T) {
	ctx := context.Background()
	_, publisher, runner := heldTopicsRig(t, true, []supervise.Claim{
		{Token: mustClaimToken(t, "LEGION-941", claim.RoleImplementer), Tree: "LEGION-941", Issue: "LEGION-941", Role: claim.RoleImplementer, Session: "ses_941", State: supervise.StateReady},
		{Token: mustClaimToken(t, "LEGION-942", claim.RoleImplementer), Tree: "LEGION-942", Issue: "LEGION-942", Role: claim.RoleImplementer, Session: "ses_942", State: supervise.StateReady},
		{Token: mustClaimToken(t, "LEGION-944", claim.RoleTester), Tree: "LEGION-944", Issue: "LEGION-944", Role: claim.RoleTester, Session: "ses_944", State: supervise.StateRetired},
	}, "ses_942", "ses_944")
	held, unheld, retired := mustClaimToken(t, "LEGION-941", claim.RoleImplementer), mustClaimToken(t, "LEGION-942", claim.RoleImplementer), mustClaimToken(t, "LEGION-944", claim.RoleTester)

	const noClaim = "a-topic-of-no-claim"
	for i, role := range []string{string(unheld), string(held), string(retired), noClaim} {
		row := mustOutboxRow(t, "LEGION-941", record.RoleMessage{Role: role, Body: "CI settled at head"}, time.Now())
		row.ID = int64(90 + i)
		if err := runner.execute(ctx, row); err != nil {
			t.Fatalf("execute the role message to %s: %v", role, err)
		}
	}
	want := []string{notify.RoleTopicPrefix + string(held), notify.RoleTopicPrefix + noClaim}
	if got := publisher.topics(); !slices.Equal(got, want) {
		t.Fatalf("published to %v, want %v: %s and the retired %s record sessions another claim holds, so nothing goes to their roles", got, want, unheld, retired)
	}
}

// The controller topic follows the deployment's controller mode (heldTopic, review round 15 of
// #4220). Under `controller: daemon` the launched controller claim is the project's controller, so
// a controller notice waits while that claim records a session it does not hold: the row is
// retried as a wait, logged as one rather than as a failure. Under `controller: operator` the topic
// is the operator's own controller's, so a launched controller claim an earlier boot retired, whose
// session another claim holds, holds nothing back, and the notice is published.
//
// The controls (the pull request records the runs): heldTopic mapping the controller topic under
// either mode, as round 15 first did, holds the notice under `controller: operator` too; and
// controllerNotice answering the gate's refusal as a plain failure fails the wait assertion.
func TestTheControllerTopicIsHeldOnlyToTheDaemonsOwnController(t *testing.T) {
	ctx := context.Background()
	controller := claim.ControllerToken("legion")
	notice := func(t *testing.T) record.OutboxRow {
		row := mustOutboxRow(t, "LEGION-941", record.ControllerNotice{Kind: "held", Role: claim.RolePlanner, Phase: phase.Planning}, time.Now())
		row.ID = 99
		return row
	}
	t.Run("controller: daemon, its claim not holding its session", func(t *testing.T) {
		_, publisher, runner := heldTopicsRig(t, true, []supervise.Claim{
			{Token: controller, Role: claim.RoleController, Session: "ses_controller", State: supervise.StateReady},
		}, "ses_controller")
		err := runner.execute(ctx, notice(t))
		if !errors.Is(err, wait.ErrWaiting) || !errors.Is(err, notify.ErrNoHolder) {
			t.Fatalf("the controller notice = %v, want a wait on the gate's refusal", err)
		}
		if got := publisher.topics(); len(got) != 0 {
			t.Fatalf("published to %v while the daemon's controller claim records a session it does not hold", got)
		}
	})
	t.Run("controller: operator, a retired launched claim not holding its session", func(t *testing.T) {
		_, publisher, runner := heldTopicsRig(t, false, []supervise.Claim{
			{Token: controller, Role: claim.RoleController, Session: "ses_controller", State: supervise.StateRetired},
		}, "ses_controller")
		if err := runner.execute(ctx, notice(t)); err != nil {
			t.Fatalf("the controller notice under controller: operator = %v, want it published", err)
		}
		if got, want := publisher.topics(), []string{notify.ControllerTopic("legion")}; !slices.Equal(got, want) {
			t.Fatalf("published to %v, want %v: the operator's controller holds the topic", got, want)
		}
	})
}

// heldTopicsRig is a supervisor with a machine for each of claims, in project legion at generation
// 1, and an outbox built as newOutbox builds it under the controller mode launched names. Each of
// taken is a session another claim, legion-943's implementer, recorded first.
func heldTopicsRig(t *testing.T, launched bool, claims []supervise.Claim, taken ...string) (*supervisor, *outboxPublisher, *outbox) {
	t.Helper()
	ctx := context.Background()
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	store := sup.deps.Store.(*outboxClaimStore)
	holder := mustClaimToken(t, "LEGION-943", claim.RoleImplementer)
	for _, s := range taken {
		if _, err := store.HoldSessions(ctx, "legion", holder, supervise.RecordedHolds(s, "")); err != nil {
			t.Fatalf("hold %s for %s, the claim that recorded it first: %v", s, holder, err)
		}
	}
	for _, c := range claims {
		c.Project, c.Generation, c.SessionFile = "legion", 1, "/sessions/"+c.Session+".jsonl"
		if _, _, err := sup.Create(ctx, c, ""); err != nil {
			t.Fatalf("create %s: %v", c.Token, err)
		}
	}
	publisher := &outboxPublisher{}
	runner := newOutbox(nil, nil, &outboxDispatch{}, publisher, sup, nil, nil, nil, nil, "legion", "LEGION", t.TempDir(), "", nil, nil, launched, quietLogger())
	return sup, publisher, runner
}
