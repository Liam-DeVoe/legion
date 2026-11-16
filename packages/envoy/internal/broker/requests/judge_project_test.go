package requests

import (
	"testing"

	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/exchange"
)

// TestTheJudgeLeavesAPrivateProjectsPodRequestToAPerson: the model judge decides no private
// project's request, and PRIV's pod asking for an untagged secret is PRIV's request, its reason
// PRIV's text, though the secret carries no project of its own. So JudgeProject answers true for
// it, read from the project the pod's operator entry maps it to, the same source the release and
// the approval page read (namedProjects); a session naming no project asking for the same untagged
// secret answers false and asks Dispatch nothing.
//
// The same class as TestAPrivateProjectsPodRequestIsDecidedListedAndShownOnlyToItsPeople, the
// tester's finding on cybermonklabs/legion#4135: the judge's project check read only the secret's
// tag, so a private project's pod request of an untagged secret reached the model.
func TestTheJudgeLeavesAPrivateProjectsPodRequestToAPerson(t *testing.T) {
	m, enr, _, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
	set, err := exchange.NewSet([]exchange.AccountEntry{{Account: fixtureSubject, Project: privProject, Person: insider}}, nil, nil, "")
	if err != nil {
		t.Fatalf("exchange.NewSet: %v", err)
	}
	m.Exchange = set
	pod, _ := newServiceEnrollment(t, m.Store, fixtureService, fixtureSubject, "pod-priv-"+t.Name())

	if private, err := m.JudgeProject(t.Context(), pod, "SHARED_KEY"); err != nil || !private {
		t.Fatalf("JudgeProject(PRIV's pod, SHARED_KEY) = %v, %v; want true: the pod's operator entry maps it to PRIV, a private project", private, err)
	}
	dispatch := answerFrom(t, m, dispatchRows(publicTeam))
	if private, err := m.JudgeProject(t.Context(), enr, "SHARED_KEY"); err != nil || private {
		t.Fatalf("JudgeProject(a session naming no project, SHARED_KEY) = %v, %v; want false", private, err)
	}
	if calls := dispatch.Calls(); calls != 0 {
		t.Fatalf("judging an untagged secret for a session naming no project made %d calls to Dispatch; want none", calls)
	}
}
