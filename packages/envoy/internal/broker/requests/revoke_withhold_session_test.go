package requests

import (
	"errors"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/exchange"
	"github.com/sjawhar/envoy/internal/broker/record"
)

// withheldCount is how many names are withheld from enrollment id.
func withheldCount(t *testing.T, m *Machine, id string) int {
	t.Helper()
	var n int
	if err := m.Store.Pool.QueryRow(t.Context(), `select count(*) from withheld_secrets where enrollment_id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAnOperatorsRevokeWithholdsFromEveryLiveBoxOfTheSession: the session ends its own AUTO_TOKEN
// grant and moves to a second box of the same session name under the same principal. The first
// box held nothing live when the session moved, so no adoption takes its grants: the ended grant
// stays on it. The operator then revokes that grant from a Live grants page loaded while it was
// live, which withholds AUTO_TOKEN (RevokeByApprover's already-ended case). The name must be
// withheld where the session runs, so the session's next AUTO_TOKEN request from the second box
// waits for a person. Rows: the first box ended for good, and the first box still live. The
// control is a second box of another session of the same person: a withhold binds a session, so
// that box gets nothing, and its request is granted at once.
func TestAnOperatorsRevokeWithholdsFromEveryLiveBoxOfTheSession(t *testing.T) {
	for _, c := range []struct {
		name         string
		secondName   string
		endFirst     bool
		wantWithheld int
	}{
		{name: "the box the session left ended for good", secondName: "sess-moved", endFirst: true, wantWithheld: 1},
		{name: "the box the session left still live", secondName: "sess-moved", wantWithheld: 1},
		{name: "a box of another session of the same person (the control)", secondName: "sess-other", wantWithheld: 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
			principal := exchange.PersonPrincipal(fixtureOperator)
			nameTheSession(t, m, enr, "sess-moved", principal)
			_, grantID := grantOf(t, m, enr, key, "deploy the worker", "AUTO_TOKEN")
			if err := m.RevokeGrant(t.Context(), grantID, enr); err != nil {
				t.Fatalf("the session's own revoke: %v", err)
			}
			if c.endFirst {
				if _, err := m.Store.Pool.Exec(t.Context(), `update enrollments set revoked_at=now() where id=$1`, enr); err != nil {
					t.Fatal(err)
				}
			}
			second, secondKey := newEnrollment(t, m.Store, "session", "box-b-"+t.Name(), new(fixtureOperator), nil)
			nameTheSession(t, m, second, c.secondName, principal)

			if err := m.RevokeByApprover(t.Context(), grantID, fixtureOperator, record.HumanActor(fixtureOperator)); err != nil {
				t.Fatalf("the operator's revoke of the ended grant = %v; want it to withhold", err)
			}
			if got := withheldCount(t, m, enr); got != 1 {
				t.Errorf("names withheld on the box that holds the grant = %d; want 1", got)
			}
			if got := withheldCount(t, m, second); got != c.wantWithheld {
				t.Errorf("names withheld on the second box = %d; want %d", got, c.wantWithheld)
			}
			again, err := m.Create(t.Context(), second, signRequest(t, m, secondKey, "deploy again", "AUTO_TOKEN"), "", nil)
			if err != nil {
				t.Fatalf("the second box's AUTO_TOKEN request: %v", err)
			}
			if granted := again.GrantID != nil; granted != (c.wantWithheld == 0) {
				t.Errorf("the second box's AUTO_TOKEN request granted at once = %v; want %v", granted, c.wantWithheld == 0)
			}
		})
	}
}

// TestARevokeStartsAgainWhenTheSessionGainsABoxWhileItRuns: a box of the grant's session that an
// adoption commits after the revoke's transaction locked the session's boxes is not among the
// locked ones (testRevokeLockedHook commits one there, once). The transaction's last check finds
// it and gives the transaction up, and the next attempt locks the new box with the rest, so the
// withhold reaches it too and its AUTO_TOKEN request waits for a person.
func TestARevokeStartsAgainWhenTheSessionGainsABoxWhileItRuns(t *testing.T) {
	m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
	principal := exchange.PersonPrincipal(fixtureOperator)
	nameTheSession(t, m, enr, "sess-racing", principal)
	_, grantID := grantOf(t, m, enr, key, "deploy the worker", "AUTO_TOKEN")
	var late string
	var lateKey = key
	m.testRevokeLockedHook = func() {
		if late != "" {
			return
		}
		late, lateKey = newEnrollment(t, m.Store, "session", "box-late-"+t.Name(), new(fixtureOperator), nil)
		nameTheSession(t, m, late, "sess-racing", principal)
	}
	if err := m.RevokeByApprover(t.Context(), grantID, fixtureOperator, record.HumanActor(fixtureOperator)); err != nil {
		t.Fatalf("the operator's revoke = %v; want it to withhold", err)
	}
	if late == "" {
		t.Fatal("the hook never ran, so no box appeared while the revoke ran and the row proves nothing")
	}
	if got := withheldCount(t, m, late); got != 1 {
		t.Errorf("names withheld on the box that appeared while the revoke ran = %d; want 1", got)
	}
	again, err := m.Create(t.Context(), late, signRequest(t, m, lateKey, "deploy again", "AUTO_TOKEN"), "", nil)
	if err != nil || again.GrantID != nil {
		t.Errorf("the late box's AUTO_TOKEN request = %+v, %v; want it waiting for a person", again, err)
	}
}

// TestAnUnmappedPersonsPodsAutomaticGrantNamesNoOperatorOnLiveGrants: dave's pod gets SLACK_TOKEN
// without asking while BROKER_POD_OPERATORS maps it to dave, then the setting drops the account.
// The pod acts for no person, so the revoke counts nobody as the grant's operator (operatorOf),
// and the list says the same: dave's Live grants still holds the grant, which it selects by the
// enrollment's record, but names no operator and no approver on it, so the page reads "Ends with
// its machine login" (GrantsSection.tsx's unrevocable) rather than offering a Revoke the broker
// refuses. dave's revoke is refused NOT_APPROVER. The control, in the same row: once the setting
// maps the account to dave again, the row names him as the operator and his revoke ends it.
func TestAnUnmappedPersonsPodsAutomaticGrantNamesNoOperatorOnLiveGrants(t *testing.T) {
	m, _, _, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
	dave := personPod{person: "dave@example.com", service: "person-dave", account: "system:serviceaccount:people:dave"}
	set, err := exchange.NewSet([]exchange.AccountEntry{{Account: dave.account, Project: teamProject, Person: insider}}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("exchange.NewSet: %v", err)
	}
	m.Exchange = set
	m.PodOperators = map[string]string{dave.account: dave.person}
	pod, key := dave.enroll(t, m.Store, "pod-dave-automatic")
	_, grantID := grantOf(t, m, pod, key, "post to the team channel", "SLACK_TOKEN")
	delete(m.PodOperators, dave.account)

	row := func() (ApproverGrant, bool) {
		t.Helper()
		grants, err := m.GrantsForApprover(t.Context(), dave.person)
		if err != nil {
			t.Fatalf("GrantsForApprover(%s): %v", dave.person, err)
		}
		i := slices.IndexFunc(grants, func(g ApproverGrant) bool { return g.GrantID == grantID })
		if i < 0 {
			return ApproverGrant{}, false
		}
		return grants[i], true
	}
	g, ok := row()
	if !ok {
		t.Fatalf("dave's Live grants does not hold grant %s of the pod made for him", grantID)
	}
	if g.Approver != "" || g.Enrollment.Operator != "" {
		t.Errorf("dave's row of grant %s names approver %q and operator %q; want neither, so the page offers no Revoke the broker refuses", grantID, g.Approver, g.Enrollment.Operator)
	}
	if err := m.RevokeByApprover(t.Context(), grantID, dave.person, record.HumanActor(dave.person)); !errors.Is(err, ErrNotApprover) {
		t.Errorf("dave's revoke of grant %s = %v; want ErrNotApprover", grantID, err)
	}
	requireGrantLive(t, m, grantID)

	m.PodOperators[dave.account] = dave.person
	if g, ok := row(); !ok || g.Enrollment.Operator != dave.person {
		t.Errorf("once the setting maps the pod to dave again, his row of grant %s = %+v, %v; want it naming him as the operator", grantID, g, ok)
	}
	if err := m.RevokeByApprover(t.Context(), grantID, dave.person, record.HumanActor(dave.person)); err != nil {
		t.Errorf("dave's revoke once the setting maps the pod to him again = %v; want it ended", err)
	}
	requireRevokedBy(t, m, grantID, dave.person)
}
