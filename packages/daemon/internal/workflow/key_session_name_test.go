package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A key keeps the name whose claim holds its session after the versions that left it out have
// replaced every row that named it: the claim's record of its session's agent
// (supervise.Claim.SessionAgent, `key:k`) keeps the name for the key. Version 1, `implementer:k
// reviewer`, starts key k on the claim implementer. Version 2, `implementer reviewer`, names its
// step 1 from version 1's rows, which name key k, so it takes implementer-2. Version 3, `planner
// implementer reviewer`, is named from version 2's rows alone, which name no key, and its
// implementer still takes a name of its own, implementer-3, and runs step 2 there, since the claim
// implementer holds key k's session. Version 4, `implementer:k reviewer`, gives key k implementer
// again, from that claim, and its start names key k, the agent whose session the claim holds, so
// the claim resumes it. After each start the claim it reached records the start's agent, as the
// runner does; no start may reach a claim whose session is another agent's. The fault: version 3's
// implementer took implementer and ran on key k's session, which the runner leaves behind as
// another agent's, so key k's return found that claim holding the other agent's session.
func TestAKeyKeepsTheNameWhoseClaimHoldsItsSessionAfterItsRowsAreGone(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	tickingClock(engine)
	apply := applyFacts(t, pool, engine)
	root := stepRoot(t, pool, phase.Admitted)
	key, gen := root.Key, root.Generation
	for i, v := range []struct {
		steps []string
		// names is the name of each step's agent; role, place and agent are the role, the place and
		// the agent of the step the version's start runs.
		names []claim.Agent
		role  claim.Role
		place int
		agent string
	}{
		{steps: []string{"implementer:k", "reviewer"}, names: []claim.Agent{"implementer", "reviewer"},
			role: claim.RoleImplementer, place: 1, agent: "key:k"},
		{steps: []string{"implementer", "reviewer"}, names: []claim.Agent{"implementer-2", "reviewer"},
			role: claim.RoleImplementer, place: 1, agent: "step:1:a"},
		{steps: []string{"planner", "implementer", "reviewer"}, names: []claim.Agent{"planner", "implementer-3", "reviewer"},
			role: claim.RoleImplementer, place: 2, agent: "step:2:a"},
		{steps: []string{"implementer:k", "reviewer"}, names: []claim.Agent{"implementer", "reviewer"},
			role: claim.RoleImplementer, place: 1, agent: "key:k"},
	} {
		version := i + 1
		mustTake(t, fmt.Sprintf("version %d", version), apply(planID(key, gen, version), ownersPlan(t, key, gen, version, givenTask(t, "Build the widget.", v.steps...))))
		if version == 1 {
			move(t, pool, engine, key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
		}
		if got := agentsOfTask(planStandingOf(t, pool, engine, key, gen).plan, "a"); !slices.Equal(got, v.names) {
			t.Fatalf("the agents of version %d = %q, want %q", version, got, v.names)
		}
		if v.place == 2 {
			// The planner's step runs first and moves the task on to the implementer's.
			completesAs(t, pool, engine, claim.RolePlanner, agentToken(t, "planner"), "plan", version)
			assertWalksAt(t, pool, engine, key, phase.Implementing, stepPlace(2))
		}
		start := lastStartOf(t, pool, key, v.role)
		if reason := fmt.Sprintf("the owner gave version %d of the plan", version); version > 1 && v.place == 1 && !strings.Contains(start.Task, reason) {
			t.Fatalf("version %d: the %s's last start = %+v, want the start of this version, which says %q", version, v.role, start, reason)
		}
		if start.Agent != v.agent {
			t.Fatalf("version %d: the start of step %d names the agent %q, want %q", version, v.place, start.Agent, v.agent)
		}
		reached := agentToken(t, v.names[v.place-1])
		if row := roleRowNow(t, pool, engine, key, v.role); row.Claim != reached {
			t.Fatalf("version %d: step %d runs on the claim %q, want %q", version, v.place, row.Claim, reached)
		}
		if held := sessionAgentOf(t, pool, reached); held != "" && held != start.Agent {
			t.Fatalf("version %d: the start for %s reaches %s, whose session is %s's", version, start.Agent, reached, held)
		} else if version == 4 && held != "key:k" {
			t.Fatalf("version 4: the claim %s records the agent %q of its session, want key:k's, which the start resumes", reached, held)
		}
		recordSessionAgent(t, pool, reached, v.role, start.Agent)
		if version < 4 {
			if result := apply(fmt.Sprintf("stop %d", version), ownersStop(t, root, "a")); result.Refusal != nil {
				t.Fatalf("version %d: the owner's stop was refused: %+v", version, result.Refusal)
			}
		}
	}
}

// sessionAgentOf is the agent whose session the stored claim token holds (claims.session_agent),
// "" for no such claim or a claim that records none.
func sessionAgentOf(t *testing.T, pool *pgxpool.Pool, token claim.Token) string {
	t.Helper()
	var agent string
	err := pool.QueryRow(context.Background(), `select session_agent from claims where token = $1`, string(token)).Scan(&agent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("read the agent of %s's session: %v", token, err)
	}
	return agent
}

// recordSessionAgent stores the claim token of LEGION-208's role as holding the session of agent, as
// the runner records the agent of a start that brings the claim back (supervise.Claim.SessionAgent).
func recordSessionAgent(t *testing.T, pool *pgxpool.Pool, token claim.Token, role claim.Role, agent string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `insert into claims (token, project, tree, issue, role, generation, session, session_file,
		state, launch_failures, prompt_failures, prompt_retires, uncertain_streak, session_agent)
		values ($1, 'legion', 'LEGION-208', 'LEGION-208', $2, 1, '', '', 'suspended', 0, 0, 0, 0, $3)
		on conflict (token) do update set session_agent = excluded.session_agent`, string(token), string(role), agent); err != nil {
		t.Fatalf("record %s as the agent of %s's session: %v", agent, token, err)
	}
}
