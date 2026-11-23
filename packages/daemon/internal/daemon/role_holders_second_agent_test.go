package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// roleHoldersRead is GET /api/v1/issues/{key}/role-holders as Dispatch answers it: the session
// that holds each Legion role of the issue, as the daemon last published it.
type roleHoldersRead struct {
	Issue   string `json:"issue"`
	Holders []struct {
		Role    string `json:"role"`
		Session string `json:"session"`
	} `json:"holders"`
}

// A second agent of one role is a holder of its role on Dispatch while it works its step. The owner
// gives `implementer reviewer implementer`; once `…-implementer-2` is ready on step 3 and takes its
// task, Dispatch's role holders of the issue name its session for the role implementer.
//
// Fault (A3 at fc1981aff4e): the daemon publishes one holder for each live claim, by the claim's
// role (record.Postgres.LiveRoleHolders), so with `…-implementer` idle after step 1 the set names
// the role implementer twice. Dispatch refuses it 400 INVALID_ROLE_HOLDER ("implementer is named
// twice. Name each role once."), the outbox drops the row with an ERROR line, and Dispatch keeps
// the set it took before step 3, which has no session of the second agent.
//
// Control: `implementer:a reviewer implementer:a`, whose step 3 is the one agent of the role, walked
// the same way; Dispatch names its session for the role implementer, so the read and the match can
// pass.
func TestASecondAgentOfOneRoleIsAHolderOfItsRoleOnDispatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []string
		agent claim.Agent
	}{
		{"second agent", secondAgentPlan, secondAgent},
		{"control: one key at steps 1 and 3", roleTwicePlan, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAgentsRig(t)
			r.givePlan(tc.steps...)
			r.openGate(phase.Implementing)
			r.github.open("legion/"+r.key, r.number)
			third := r.worker(claim.RoleImplementer)
			r.work(claim.RoleImplementer, "implement", implementCompletion, phase.Reviewing)
			r.reviewReported("APPROVED", phase.Implementing)
			if tc.agent != "" {
				third = r.agentWorker(tc.agent, claim.RoleImplementer)
			}
			third.takeTask()
			waitRead(r.stepRecordsRig, "the session of step 3's agent as a holder of the role implementer", "Dispatch", time.Second,
				func() (roleHoldersRead, error) {
					var read roleHoldersRead
					err := r.personRead("/api/v1/issues/"+r.key+"/role-holders", &read)
					return read, err
				},
				func(read roleHoldersRead) error {
					for _, h := range read.Holders {
						if h.Role == string(claim.RoleImplementer) && h.Session == third.session {
							return nil
						}
					}
					return fmt.Errorf("Dispatch holds %+v, want the role implementer held by %s, the session of %s", read.Holders, third.session, third.token)
				})
		})
	}
}
