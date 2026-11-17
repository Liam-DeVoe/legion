package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// A claim the boot could not suspend keeps its process, and that process keeps the Envoy role its
// session claimed, so the outbox publishes nothing to the role of a claim that records a session
// it does not hold (heldRolePublisher, review round 13 of #4220): a role message to it finishes
// undelivered with the publisher never called. A message to a claim holding its own session, and
// to a topic of no claim this daemon supervises, is published as before (R9).
//
// The control (the pull request records the run): newOutbox handing its executors the publisher as
// it came; the message to the claim that does not hold its session is published too.
func TestTheOutboxPublishesNothingToTheRoleOfAClaimThatDoesNotHoldItsSession(t *testing.T) {
	ctx := context.Background()
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	claims := sup.deps.Store.(*outboxClaimStore)
	holder := mustClaimToken(t, "LEGION-943", claim.RoleImplementer)
	held, unheld := mustClaimToken(t, "LEGION-941", claim.RoleImplementer), mustClaimToken(t, "LEGION-942", claim.RoleImplementer)
	const taken = "ses_942"
	if _, err := claims.HoldSessions(ctx, "legion", holder, supervise.RecordedHolds(taken, "/sessions/943.jsonl")); err != nil {
		t.Fatalf("hold %s for %s, the claim that recorded it first: %v", taken, holder, err)
	}
	for _, c := range []struct {
		token          claim.Token
		issue, session string
	}{{held, "LEGION-941", "ses_941"}, {unheld, "LEGION-942", taken}} {
		if _, _, err := sup.Create(ctx, supervise.Claim{Token: c.token, Project: "legion", Tree: c.issue, Issue: c.issue,
			Role: claim.RoleImplementer, State: supervise.StateReady, Generation: 1,
			Session: c.session, SessionFile: "/sessions/" + c.session + ".jsonl"}, ""); err != nil {
			t.Fatalf("create %s: %v", c.token, err)
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
}
