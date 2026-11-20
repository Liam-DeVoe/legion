package requests

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/broker/record"
)

// TestARevokeDecidesOnlyOnTheGrantTheRuleJudged: RevokeByApprover's person rule reads the grant
// on the pool before the transaction (hideUnseenGrant), and the transaction reads it again. The
// model judge granted SHARED_KEY to the fixture's session in anyone's stead, so its approver is
// record.AnyoneApprover; its reason names INFRA-1353, and INFRA is private with carol alone on its
// list. The session's operator, not on that list, is exempt from the rule as the operator. An
// adoption then moves the grant to a service's pod, which names no operator, after the rule read
// it and before the transaction did (testRevokeHook). The transaction does not decide on that
// row: the operator is not named on it, and its approver's arm (anyone) would let them end a
// grant of INFRA the rule never judged for them. The revoke starts again from the rule, which now
// hides the grant from them, so they are answered as for a grant id no grant has and the grant
// stays live.
func TestARevokeDecidesOnlyOnTheGrantTheRuleJudged(t *testing.T) {
	m, session, key := newJudgeFixture(t)
	grantID := judgedGrant(t, m, session, key, 600, "SHARED_KEY")
	answerFrom(t, m, map[string]projectaccesstest.Row{"INFRA": {Visibility: "private", Owner: otherPerson, Listed: []string{insider}}})
	var approver, operator *string
	if err := m.Store.Pool.QueryRow(t.Context(), `select g.approver, e.operator from grants g join enrollments e on e.id=g.enrollment_id where g.id=$1`, grantID).Scan(&approver, &operator); err != nil || deref(approver) != record.AnyoneApprover || deref(operator) == "" {
		t.Fatalf("the judged grant: approver %q, operator %q, %v; want the judge's anyone, held by a session with an operator", deref(approver), deref(operator), err)
	}
	login := *operator
	pod, _ := newServiceEnrollment(t, m.Store, fixtureService, fixtureSubject, "pod-"+t.Name())
	moves := 0
	m.testRevokeHook = func() {
		if moves++; moves > 1 {
			return
		}
		if _, err := m.Store.Pool.Exec(t.Context(), `with moved as (update grants set enrollment_id=$2 where id=$1 returning request_id)
			update requests set enrollment_id=$2 where id=(select request_id from moved)`, grantID, pod); err != nil {
			t.Errorf("move the grant to the service's pod: %v", err)
		}
	}
	err := m.RevokeByApprover(t.Context(), grantID, login, record.HumanActor(login))
	if moves == 0 {
		t.Fatal("the hook never ran, so the grant never moved between the two reads and the row proves nothing")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("RevokeByApprover(the judged grant, its former operator %s, moved since the rule read it) = %v; want the answer an unknown grant gets (pgx.ErrNoRows)", login, err)
	}
	requireGrantLive(t, m, grantID)
}
