package requests

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/broker/record"
)

// judgeRows is a Dispatch holding TEAM public and PRIV private.
func judgeRows(team projectaccesstest.Row) map[string]projectaccesstest.Row {
	return map[string]projectaccesstest.Row{
		teamProject: team,
		privProject: {Visibility: "private", Owner: otherPerson},
	}
}

// TestTheJudgesDecisionChecksTheProjectRuleAgain is round 3's first hardening item on
// cybermonklabs/legion#4135. The judge's run checks the project rule before it asks the model
// (JudgeProject) and then spends up to twenty seconds on the model call, in which the project an
// issue of the reason names can turn private, or the request can be adopted by an enrollment that
// names a private project. The decision reads Dispatch again before its transaction, so the
// judge's approval of a request that is a private project's by then is refused, neither granted
// nor denied, and the request waits for its person. A request still of no private project is
// granted, and so is any request on a broker with no project-access client, as at the merge base.
func TestTheJudgesDecisionChecksTheProjectRuleAgain(t *testing.T) {
	const reason = "TEAM-7: rotate the deploy key"
	for _, c := range []struct {
		name string
		turn func(t *testing.T, m *Machine, enrollmentID string)
	}{
		{"the reason's project turned private", func(t *testing.T, m *Machine, _ string) {
			answerFrom(t, m, judgeRows(privateTeam()))
		}},
		{"the enrollment now names a private project", func(t *testing.T, m *Machine, enrollmentID string) {
			nameProjects(t, m, enrollmentID, privProject)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, enr, key := newJudgeFixture(t)
			answerFrom(t, m, judgeRows(publicTeam))
			req := pendingJudgeable(t, m, enr, key, reason, 600, "DEEL_API_KEY")
			if private, err := m.JudgeProject(t.Context(), enr, "DEEL_API_KEY", reason); err != nil || private {
				t.Fatalf("JudgeProject before the change = %v, %v; want false: the run's own check passed", private, err)
			}
			c.turn(t, m, enr)

			dec, err := m.ApplyJudgeDecision(t.Context(), *req.RecordID, judgeModel, map[string]any{"verdict": "approve"})
			if !errors.Is(err, record.ErrNotApprover) {
				t.Fatalf("the judge's approval once %s = %+v, %v; want record.ErrNotApprover", c.name, dec, err)
			}
			got := readDecided(t, m, *req.RecordID)
			if got.events != 0 || got.state != "pending" || got.decidedBy != "" || len(got.approvers) != 0 {
				t.Fatalf("after the refusal = %+v; want the request untouched and pending", got)
			}
			if judged := readJudged(t, m, *req.RecordID); len(judged) != 0 {
				t.Errorf("request.judged rows = %+v; want none: the transaction wrote nothing", judged)
			}
		})
	}

	for _, c := range []struct {
		name   string
		client bool
	}{
		{"nothing turned private", true},
		{"a broker with no project-access client", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, enr, key := newJudgeFixture(t)
			answerFrom(t, m, judgeRows(publicTeam))
			if !c.client {
				m.Projects = nil
			}
			req := pendingJudgeable(t, m, enr, key, reason, 600, "DEEL_API_KEY")
			dec, err := m.ApplyJudgeDecision(t.Context(), *req.RecordID, judgeModel, map[string]any{"verdict": "approve"})
			if err != nil || dec.State != "granted" || dec.GrantID == "" {
				t.Fatalf("the judge's approval, %s = %+v, %v; want it granted, as at the merge base", c.name, dec, err)
			}
		})
	}
}

// TestAReasonNamingManyProjectsCostsABoundedRead is round 3's second hardening item on
// cybermonklabs/legion#4135. The person rule and the judge read the project of every issue key a
// reason names, one cached Dispatch read per distinct project, so a reason naming a thousand
// projects would cost a thousand reads per row per reader. Past maxReasonProjects distinct
// projects the request is hidden from every reader and left to nobody's judge, with no read of
// Dispatch at all: a request no rule could check cheaply is one nobody decides, and it expires.
// A reason naming maxReasonProjects projects, none of them private, is read and shown as before.
func TestAReasonNamingManyProjectsCostsABoundedRead(t *testing.T) {
	m, enr, _ := newJudgeFixture(t)
	dispatch := answerFrom(t, m, judgeRows(publicTeam))
	keys := make([]string, 1000)
	for i := range keys {
		keys[i] = fmt.Sprintf("P%03d-1", i)
	}
	many := strings.Join(keys, " ")

	before := dispatch.Calls()
	if may, err := m.MaySee(t.Context(), outsider, RecordDetail{Reason: many}); err != nil || may {
		t.Errorf("MaySee(a reason naming %d projects) = %v, %v; want false: past the cap nobody sees it", len(keys), may, err)
	}
	if private, err := m.JudgeProject(t.Context(), enr, "DEEL_API_KEY", many); err != nil || !private {
		t.Errorf("JudgeProject(a reason naming %d projects) = %v, %v; want true: the judge leaves it to a person", len(keys), private, err)
	}
	if calls := dispatch.Calls() - before; calls > 2*maxReasonProjects {
		t.Errorf("reading a reason naming %d projects twice cost %d Dispatch reads; want at most %d", len(keys), calls, 2*maxReasonProjects)
	}

	atTheCap := strings.Join(keys[:maxReasonProjects-1], " ") + " TEAM-7"
	if may, err := m.MaySee(t.Context(), outsider, RecordDetail{Reason: atTheCap}); err != nil || !may {
		t.Errorf("MaySee(a reason naming %d projects, none private) = %v, %v; want true, as before the cap", maxReasonProjects, may, err)
	}
	if private, err := m.JudgeProject(t.Context(), enr, "DEEL_API_KEY", atTheCap); err != nil || private {
		t.Errorf("JudgeProject(a reason naming %d projects, none private) = %v, %v; want false, as before the cap", maxReasonProjects, private, err)
	}
}
