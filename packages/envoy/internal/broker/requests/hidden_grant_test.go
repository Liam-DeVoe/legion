// hidden_grant_test.go holds RevokeByApprover's person rule: a grant of a private project, by a
// project tag of a name it releases, by a project a credential its request has had names, or by
// an issue key its reason names, is ended only by a person Dispatch says may see that project,
// the people GrantsForApprover lists it to. Anyone else is answered pgx.ErrNoRows, the answer a
// grant id no grant has, and nothing is revoked; while Dispatch cannot answer, nothing is revoked
// either.
package requests

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/broker/record"
)

// requireGrantLive fails t unless grant grantID has not been revoked.
func requireGrantLive(t *testing.T, m *Machine, grantID string) {
	t.Helper()
	var revoked *time.Time
	if err := m.Store.Pool.QueryRow(t.Context(), `select revoked_at from grants where id=$1`, grantID).Scan(&revoked); err != nil {
		t.Fatalf("read grant %s: %v", grantID, err)
	}
	if revoked != nil {
		t.Fatalf("grant %s was revoked at %s; want it live", grantID, revoked)
	}
}

// requireRevokedAsUnknown fails t unless login's revoke of grantID answers pgx.ErrNoRows alone,
// the answer of a grant id no grant has, which a fresh id answers in the same call, and leaves
// the grant live.
func requireRevokedAsUnknown(t *testing.T, m *Machine, grantID, login, what string) {
	t.Helper()
	err := m.RevokeByApprover(t.Context(), grantID, login, record.HumanActor(login))
	if !errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotApprover) {
		t.Fatalf("RevokeByApprover(%s, %s) = %v; want pgx.ErrNoRows alone, as for a grant id no grant has", what, login, err)
	}
	if err := m.RevokeByApprover(t.Context(), uuid.NewString(), login, record.HumanActor(login)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RevokeByApprover(a grant id no grant has, %s) = %v; want pgx.ErrNoRows (control)", login, err)
	}
	requireGrantLive(t, m, grantID)
}

// TestAnOutsiderCannotRevokeAJudgedGrantOfAProjectTurnedPrivate: the model judge approves
// SHARED_KEY on a service's pod in anyone's stead, so the grant's approver is
// record.AnyoneApprover, which mayRevoke lets every signed-in person end. Its reason names
// INFRA-1353, and INFRA then turns private with carol alone on its list. mallory, outside INFRA,
// is answered pgx.ErrNoRows, as for a grant id no grant has, the grant stays live, and her list
// does not hold it; carol, on INFRA's list, ends it.
func TestAnOutsiderCannotRevokeAJudgedGrantOfAProjectTurnedPrivate(t *testing.T) {
	m, _, _ := newJudgeFixture(t)
	pod, podKey := newServiceEnrollment(t, m.Store, fixtureService, fixtureSubject, "pod-"+t.Name())
	grantID := judgedGrant(t, m, pod, podKey, 600, "SHARED_KEY")
	answerFrom(t, m, map[string]projectaccesstest.Row{"INFRA": {Visibility: "private", Owner: otherPerson, Listed: []string{insider}}})

	requireRevokedAsUnknown(t, m, grantID, outsider, "the judged grant of INFRA")
	listed, err := m.GrantsForApprover(t.Context(), outsider)
	if err != nil || slices.ContainsFunc(listed, func(g ApproverGrant) bool { return g.GrantID == grantID }) {
		t.Fatalf("GrantsForApprover(%s) = %+v, %v; want it answered, without the grant", outsider, listed, err)
	}
	if err := m.RevokeByApprover(t.Context(), grantID, insider, record.HumanActor(insider)); err != nil {
		t.Fatalf("RevokeByApprover(the judged grant, %s, on INFRA's list) = %v; want it ended", insider, err)
	}
	var revoked *time.Time
	if err := m.Store.Pool.QueryRow(t.Context(), `select revoked_at from grants where id=$1`, grantID).Scan(&revoked); err != nil || revoked == nil {
		t.Fatalf("the judged grant after %s's revoke: revoked_at %v, %v; want it revoked", insider, revoked, err)
	}
}

// TestAnOutsiderCannotRevokeAPersonsGrantOfAPrivateProject: a grant carol, on PRIV's list,
// approved is refused to mallory, outside PRIV, as a grant id no grant has, not with
// ErrNotApprover, whichever part of the grant makes it PRIV's: its secret's tag, the projects of
// the credential that asked, or those of the credential it hangs off after an adoption moved it.
func TestAnOutsiderCannotRevokeAPersonsGrantOfAPrivateProject(t *testing.T) {
	m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
	nameProjects(t, m, enr, privProject)
	_, tagged := approvedGrant(t, m, testAudience, enr, key, insider, "PRIV_HUMAN")
	requireRevokedAsUnknown(t, m, tagged, outsider, "PRIV_HUMAN's grant")

	for _, c := range []struct {
		name          string
		asked, adopts []string
	}{
		{"the credential that asked names PRIV", []string{privProject}, []string{}},
		{"the credential it hangs off now names PRIV", []string{}, []string{privProject}},
	} {
		t.Run(c.name, func(t *testing.T) {
			asking, askingKey := newEnrollment(t, m.Store, "session", "box-a-"+t.Name(), new(fixtureOperator), nil)
			nameProjects(t, m, asking, c.asked...)
			_, grantID := approvedGrant(t, m, testAudience, asking, askingKey, insider, "SHARED_KEY")
			adopter, _ := newEnrollment(t, m.Store, "session", "box-b-"+t.Name(), new(fixtureOperator), nil)
			nameProjects(t, m, adopter, c.adopts...)
			// An adoption moves the session's grant and its request to the enrollment of the box the
			// session runs on now (enroll.adopt); the record keeps the enrollment that asked.
			if _, err := m.Store.Pool.Exec(t.Context(), `with moved as (update grants set enrollment_id=$2 where id=$1 returning request_id)
				update requests set enrollment_id=$2 where id=(select request_id from moved)`, grantID, adopter); err != nil {
				t.Fatalf("move the grant to the adopting enrollment: %v", err)
			}
			requireRevokedAsUnknown(t, m, grantID, outsider, "SHARED_KEY's grant")
			if err := m.RevokeByApprover(t.Context(), grantID, insider, record.HumanActor(insider)); err != nil {
				t.Fatalf("RevokeByApprover(SHARED_KEY's grant, %s, on PRIV's list) = %v; want it ended", insider, err)
			}
		})
	}
}

// TestARevokeOfAPrivateProjectsGrantFailsClosedAndStaysWithItsPeople: two grants of PRIV_HUMAN,
// each on a session of the fixture's operator naming PRIV, approved by carol. While Dispatch
// cannot answer, neither carol, their approver, nor the operator ends one: each is answered
// ErrProjectAccessUnavailable and both stay live. Once Dispatch answers, carol ends the first as
// its approver and the operator the second as its operator. A third grant's operator, once PRIV's
// list no longer names them, is answered as for a grant id no grant has, as their own list leaves
// it out.
func TestARevokeOfAPrivateProjectsGrantFailsClosedAndStaysWithItsPeople(t *testing.T) {
	m, _, _, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
	grantOn := func(box string) string {
		t.Helper()
		session, sessionKey := newEnrollment(t, m.Store, "session", box+"-"+t.Name(), new(fixtureOperator), nil)
		nameProjects(t, m, session, privProject)
		_, grantID := approvedGrant(t, m, testAudience, session, sessionKey, insider, "PRIV_HUMAN")
		return grantID
	}
	byApprover, byOperator, unseen := grantOn("box-approver"), grantOn("box-operator"), grantOn("box-unseen")

	down := answerFrom(t, m, dispatchRows(publicTeam))
	down.SetDown(true)
	for _, c := range []struct{ grant, login string }{{byApprover, insider}, {byOperator, fixtureOperator}} {
		if err := m.RevokeByApprover(t.Context(), c.grant, c.login, record.HumanActor(c.login)); !errors.Is(err, ErrProjectAccessUnavailable) {
			t.Fatalf("RevokeByApprover(%s, %s) while Dispatch is down = %v; want ErrProjectAccessUnavailable", c.grant, c.login, err)
		}
		requireGrantLive(t, m, c.grant)
	}
	if down.Calls() == 0 {
		t.Fatal("the down Dispatch was never asked: the rows above are blind")
	}

	answerFrom(t, m, dispatchRows(publicTeam))
	if err := m.RevokeByApprover(t.Context(), byApprover, insider, record.HumanActor(insider)); err != nil {
		t.Fatalf("RevokeByApprover(the first grant, %s, its approver) = %v; want it ended", insider, err)
	}
	if err := m.RevokeByApprover(t.Context(), byOperator, fixtureOperator, record.HumanActor(fixtureOperator)); err != nil {
		t.Fatalf("RevokeByApprover(the second grant, %s, its operator) = %v; want it ended", fixtureOperator, err)
	}
	for grantID, by := range map[string]string{byApprover: insider, byOperator: fixtureOperator} {
		var revokedBy *string
		if err := m.Store.Pool.QueryRow(t.Context(), `select revoked_by from grants where id=$1`, grantID).Scan(&revokedBy); err != nil {
			t.Fatalf("read grant %s: %v", grantID, err)
		}
		if revokedBy == nil || *revokedBy != record.HumanActor(by) {
			t.Fatalf("grant %s revoked_by = %v; want %s", grantID, revokedBy, record.HumanActor(by))
		}
	}

	answerFrom(t, m, map[string]projectaccesstest.Row{
		privProject: {Visibility: "private", Owner: otherPerson, Listed: []string{insider}},
		teamProject: publicTeam,
	})
	requireRevokedAsUnknown(t, m, unseen, fixtureOperator, "the third grant, its operator off PRIV's list")
}
