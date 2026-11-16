package requests

import (
	"testing"

	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
)

// TestTheJudgeLeavesARequestWhoseReasonNamesAPrivateIssueToAPerson is round 2's B6 on
// cybermonklabs/legion#4135: a session naming no project asks for SHARED_KEY, an untagged secret
// the rules let the judge decide, with the reason "INFRA-12: rotate the key PRIV-3 uses". INFRA is
// public and PRIV private, so the person rule makes this PRIV's request (MaySee is false for an
// outsider, the control showing that rule sees the reason), and the judge must leave it to a
// person too: deciding it would grant in the name of an approver who can see neither the request
// nor the grant. A reason naming only a public project's issue is judged as at the merge base
// (R9), and so is any reason on a broker with no project-access client.
func TestTheJudgeLeavesARequestWhoseReasonNamesAPrivateIssueToAPerson(t *testing.T) {
	m, enr, key := newJudgeFixture(t)
	answerFrom(t, m, map[string]projectaccesstest.Row{
		"INFRA": {Visibility: "public", Owner: otherPerson},
		"PRIV":  {Visibility: "private", Owner: otherPerson},
	})
	const private = "INFRA-12: rotate the key PRIV-3 uses"
	pending := pendingJudgeable(t, m, enr, key, private, 600, "SHARED_KEY")

	detail, err := m.ReadRecord(t.Context(), *pending.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if may, err := m.MaySee(t.Context(), outsider, detail); err != nil || may {
		t.Fatalf("MaySee(%s, a request whose reason names PRIV-3) = %v, %v; want false: the person rule reads every key", outsider, may, err)
	}
	if got, err := m.JudgeProject(t.Context(), enr, "SHARED_KEY", private); err != nil || !got {
		t.Fatalf("JudgeProject(SHARED_KEY, %q) = %v, %v; want true: the reason names PRIV-3, a private project's issue, after its first key", private, got, err)
	}

	for _, c := range []struct{ name, reason string }{
		{"a public project's issue", "INFRA-12: rotate the deploy key"},
		{"a word of a key's form that is no project", "SHA-256 of the INFRA-12 bundle"},
	} {
		if got, err := m.JudgeProject(t.Context(), enr, "SHARED_KEY", c.reason); err != nil || got {
			t.Errorf("JudgeProject(SHARED_KEY, %s: %q) = %v, %v; want false, as the merge base judged it", c.name, c.reason, got, err)
		}
	}
	m.Projects = nil
	if got, err := m.JudgeProject(t.Context(), enr, "SHARED_KEY", private); err != nil || got {
		t.Errorf("JudgeProject with no project-access client, reason %q = %v, %v; want false, as the merge base judged it", private, got, err)
	}
}
