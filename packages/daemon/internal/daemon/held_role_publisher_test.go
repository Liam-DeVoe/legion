package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// A claim the boot could not suspend keeps its process, and that process keeps the Envoy role its
// session claimed, so the outbox publishes nothing to the role of a claim that records a session
// it does not hold (outbox.publish, reviews rounds 13 and 14 of #4220): a role message to it
// finishes undelivered with the publisher never called. Nor does it publish a controller notice to
// the controller topic while the controller's claim records a session it does not hold; that row
// is retried on its backoff. A message to a claim holding its own session, and to a topic of no
// claim this daemon supervises, is published as before (R9).
//
// The controls (the pull request records the runs): publish handing every topic to the publisher
// as it came publishes to the claim and the controller that do not hold their sessions; publish
// holding role topics alone publishes the controller notice.
func TestTheOutboxPublishesNothingToTheRoleOfAClaimThatDoesNotHoldItsSession(t *testing.T) {
	ctx := context.Background()
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	claims := sup.deps.Store.(*outboxClaimStore)
	holder := mustClaimToken(t, "LEGION-943", claim.RoleImplementer)
	held, unheld := mustClaimToken(t, "LEGION-941", claim.RoleImplementer), mustClaimToken(t, "LEGION-942", claim.RoleImplementer)
	controller := claim.ControllerToken("legion")
	const taken, controllerSession = "ses_942", "ses_controller"
	for _, s := range []string{taken, controllerSession} {
		if _, err := claims.HoldSessions(ctx, "legion", holder, supervise.RecordedHolds(s, "")); err != nil {
			t.Fatalf("hold %s for %s, the claim that recorded it first: %v", s, holder, err)
		}
	}
	for _, c := range []supervise.Claim{
		{Token: held, Project: "legion", Tree: "LEGION-941", Issue: "LEGION-941", Role: claim.RoleImplementer, Session: "ses_941"},
		{Token: unheld, Project: "legion", Tree: "LEGION-942", Issue: "LEGION-942", Role: claim.RoleImplementer, Session: taken},
		{Token: controller, Project: "legion", Role: claim.RoleController, Session: controllerSession},
	} {
		c.State, c.Generation, c.SessionFile = supervise.StateReady, 1, "/sessions/"+c.Session+".jsonl"
		if _, _, err := sup.Create(ctx, c, ""); err != nil {
			t.Fatalf("create %s: %v", c.Token, err)
		}
	}
	publisher := &outboxPublisher{}
	runner := newOutbox(nil, nil, &outboxDispatch{}, publisher, sup, nil, nil, nil, nil, "legion", "LEGION", t.TempDir(), "", nil, nil, quietLogger())

	const noClaim = "a-topic-of-no-claim"
	for i, role := range []string{string(unheld), string(held), noClaim} {
		row := mustOutboxRow(t, "LEGION-941", record.RoleMessage{Role: role, Body: "CI settled at head"}, time.Now())
		row.ID = int64(90 + i)
		if err := runner.execute(ctx, row); err != nil {
			t.Fatalf("execute the role message to %s: %v", role, err)
		}
	}
	want := []string{notify.RoleTopicPrefix + string(held), notify.RoleTopicPrefix + noClaim}
	if got := publisher.topics(); !slices.Equal(got, want) {
		t.Fatalf("published to %v, want %v: %s records %s, which %s holds, so nothing goes to its role", got, want, unheld, taken, holder)
	}

	row := mustOutboxRow(t, "LEGION-941", record.ControllerNotice{Kind: "held", Role: claim.RolePlanner, Phase: phase.Planning}, time.Now())
	row.ID = 99
	if err := runner.execute(ctx, row); err == nil {
		t.Fatalf("a controller notice while the controller's claim records %s, which %s holds, was executed with no error, want it held for a retry", controllerSession, holder)
	}
	if got := publisher.topics(); !slices.Equal(got, want) {
		t.Fatalf("published to %v, want still %v: nothing goes to the controller topic while its claim does not hold its session", got, want)
	}
}
