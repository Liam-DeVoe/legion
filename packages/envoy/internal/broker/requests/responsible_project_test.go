package requests

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/record"
)

// wantListed fails t unless exactly the logins in want, of every login in all, list recordID.
func wantListed(t *testing.T, m *Machine, recordID string, all, want []string) {
	t.Helper()
	for _, login := range all {
		if _, got := listedRow(t, m, login, PendingStates{}, recordID); got != slices.Contains(want, login) {
			t.Errorf("%s's list holds record %s: %v; want %v", login, recordID, got, slices.Contains(want, login))
		}
	}
}

// TestARequestWhoseResponsiblePersonMayNotSeeItWaitsForThoseWhoMay: a request for a shared secret
// is listed for the person responsible for the agent that made it (responsiblePerson), and a
// private project's request for nobody outside the project (MaySee). Where the responsible person
// is outside the project, the request is listed for everyone who may see it instead
// (listedFor), so it waits where somebody sees it:
//   - an outsider's own session asks with a reason naming a PRIV issue: the two people on PRIV's
//     list, the insider and the fixture's operator, list it, and the outsider does not;
//   - the operator's request names a TEAM issue while TEAM is public, and is the operator's alone;
//     TEAM then turns private with the insider alone on its list, and the insider lists it from
//     then on, the operator no more;
//   - control: the operator's request names a PRIV issue, and the operator is on PRIV's list, so it
//     stays the operator's alone.
func TestARequestWhoseResponsiblePersonMayNotSeeItWaitsForThoseWhoMay(t *testing.T) {
	people := []string{outsider, insider, fixtureOperator}
	t.Run("an outsider's request naming a private project's issue", func(t *testing.T) {
		m, _, _, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
		out, outKey := newEnrollment(t, m.Store, "session", "box-r-outsider", new(outsider), nil)
		req, err := m.Create(t.Context(), out, signRequest(t, m, outKey, "PRIV-12: rotate the deploy key", "SHARED_KEY"), "", nil)
		if err != nil || req.RecordID == nil || req.State != "pending" {
			t.Fatalf("Create(SHARED_KEY) = %+v, %v; want it pending", req, err)
		}
		wantListed(t, m, *req.RecordID, people, []string{insider, fixtureOperator})
	})
	t.Run("a project that turns private under its responsible person", func(t *testing.T) {
		m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
		req, err := m.Create(t.Context(), enr, signRequest(t, m, key, "TEAM-7: rotate the deploy key", "SHARED_KEY"), "", nil)
		if err != nil || req.RecordID == nil || req.State != "pending" {
			t.Fatalf("Create(SHARED_KEY) = %+v, %v; want it pending", req, err)
		}
		wantListed(t, m, *req.RecordID, people, []string{fixtureOperator})
		answerFrom(t, m, dispatchRows(privateTeam(insider)))
		wantListed(t, m, *req.RecordID, people, []string{insider})
	})
	t.Run("a responsible person on the private project (control)", func(t *testing.T) {
		m, enr, key, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
		req, err := m.Create(t.Context(), enr, signRequest(t, m, key, "PRIV-12: rotate the deploy key", "SHARED_KEY"), "", nil)
		if err != nil || req.RecordID == nil || req.State != "pending" {
			t.Fatalf("Create(SHARED_KEY) = %+v, %v; want it pending", req, err)
		}
		wantListed(t, m, *req.RecordID, people, []string{fixtureOperator})
	})
}

// TestTheJudgesRefusalReadsNothingOfDispatch: the model judge's refusal of a request it holds is
// final whatever project the request belongs to, and asks Dispatch nothing, since a refusal grants
// nothing (applyDecisionAttempt). A request whose reason names a private project's issue is
// refused (the run's policy and withheld checks refuse before its project check), and so is one
// whose reason names a public project's issue while Dispatch does not answer. Control: the judge's
// grant of the private project's request is refused, record.ErrNotApprover, since a grant asks
// Dispatch again.
func TestTheJudgesRefusalReadsNothingOfDispatch(t *testing.T) {
	rules := airlock.Rules{Secrets: []airlock.SecretRule{{Name: "SHARED_KEY", Hosts: []string{"shared.example"}, Judge: new(uint32(900))}}}
	const why = "the model judge refused this request: SHARED_KEY is withheld from this session. This request will not be granted. Tell whoever gave you this work."
	held := func(t *testing.T, reason string) (*Machine, Request) {
		t.Helper()
		m, enr, key, _ := newProjectFixture(t, rules, publicTeam)
		m.Chain = NewChainVerifier(m.Store, testAudience, time.Minute, m.Airlock)
		m.Judge = func(PendingRequest) {}
		req := pendingJudgeable(t, m, enr, key, reason, 600, "SHARED_KEY")
		if _, ok := judgeHold(t, m, req.ID); !ok {
			t.Fatalf("request %s is not held for the judge", req.ID)
		}
		return m, req
	}
	for _, c := range []struct {
		name, reason string
		down         bool
	}{
		{"a private project's issue", "PRIV-12: rotate the deploy key", false},
		{"a public project's issue while Dispatch is down", "TEAM-7: rotate the deploy key", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, req := held(t, c.reason)
			dispatch := answerFrom(t, m, dispatchRows(publicTeam))
			dispatch.SetDown(c.down)
			dec, err := m.DenyJudged(t.Context(), *req.RecordID, judgeModel, map[string]any{"verdict": "deny"}, why)
			if err != nil || dec.State != "denied" {
				t.Fatalf("DenyJudged = %+v, %v; want the request denied", dec, err)
			}
			if calls := dispatch.Calls(); calls != 0 {
				t.Errorf("the judge's refusal asked Dispatch %d times; want none", calls)
			}
		})
	}
	t.Run("the judge's grant of a private project's request (control)", func(t *testing.T) {
		m, req := held(t, "PRIV-12: rotate the deploy key")
		answerFrom(t, m, dispatchRows(publicTeam))
		dec, err := m.ApplyJudgeDecision(t.Context(), *req.RecordID, judgeModel, map[string]any{"verdict": "grant"})
		if !errors.Is(err, record.ErrNotApprover) {
			t.Fatalf("ApplyJudgeDecision = %+v, %v; want record.ErrNotApprover", dec, err)
		}
	})
}
