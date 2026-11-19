package workflow

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// roleRowClaims is, for every row of kind issue's outbox holds, oldest first, the role it is
// addressed to, the claim it names ("" for none) and whether its task follows, read from the row's
// JSON as the outbox decodes it.
func roleRowClaims(t *testing.T, pool *pgxpool.Pool, issue, kind string) []struct{ role, claim, follows string } {
	t.Helper()
	rows, err := pool.Query(t.Context(), `select payload->>'role', coalesce(payload->>'claim', ''), coalesce(payload->>'taskFollows', 'false')
		from outbox where issue = $1 and kind = $2 order by id`, issue, kind)
	if err != nil {
		t.Fatalf("read the %s rows of %s: %v", kind, issue, err)
	}
	defer rows.Close()
	var got []struct{ role, claim, follows string }
	for rows.Next() {
		var row struct{ role, claim, follows string }
		if err := rows.Scan(&row.role, &row.claim, &row.follows); err != nil {
			t.Fatalf("scan a %s row of %s: %v", kind, issue, err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the %s rows of %s: %v", kind, issue, err)
	}
	return got
}

// A head that starts conflicting with its base ends the phase of the role working it, and the
// row that tells it so names the claim of the agent that worked that phase, read in the
// transition's own transaction before its start writes the next step's agent on a role's row: the
// outbox publishes on that claim's topic, whatever a later entry of the role's step names when the
// row runs. In the plan `implementer:a tester:a reviewer` key a's agent, `…-implementer`, works the
// tester's step, and the row names it, not the role's own claim `…-tester`. In `implementer tester
// reviewer` the retro's agent is the implementer, which the conflict starts, so its implementing
// task follows.
func TestAPhaseEndedRowNamesTheAgentThatWorkedThePhase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		words []string
		// to is the walk from admitted to the phase the conflict ends.
		to      []TriggerKind
		ended   claim.Role
		follows bool
	}{
		{name: "a keyed agent on the tester's step", words: []string{"implementer:a", "tester:a", "reviewer"},
			to: []TriggerKind{TriggerGateOpened, TriggerImplementationReady}, ended: claim.RoleTester},
		{name: "the implementer in retro, whose task follows", words: []string{"implementer", "tester", "reviewer"},
			to:    []TriggerKind{TriggerGateOpened, TriggerImplementationReady, TriggerTesterPassed, TriggerReviewApproved},
			ended: claim.RoleImplementer, follows: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := stepEngine(nil)
			root := stepRoot(t, pool, phase.Admitted)
			seedStepsPlan(t, pool, root.Key, root.Generation, tc.words...)
			for _, trigger := range tc.to {
				move(t, pool, engine, root.Key, trigger, "", record.PhaseRow{})
			}
			from := issueOf(t, pool, engine, root.Key).Phase
			if phase.RoleFor(from) != tc.ended {
				t.Fatalf("%s stands in %s before the conflict, want a phase of the %s: the case's premise", root.Key, from, tc.ended)
			}
			worked := roleRowOf(t, pool, engine, root.Key, tc.ended).Claim
			if worked == "" || (tc.ended == claim.RoleTester && worked == claim.Token(token(t, root.Key, claim.RoleTester))) {
				t.Fatalf("the %s's phases row names %q before the conflict, want the claim of its step's agent: the case's premise", tc.ended, worked)
			}

			move(t, pool, engine, root.Key, TriggerConflict, "", record.PhaseRow{})

			ended := roleRowClaims(t, pool, root.Key, string(record.OutboxKindPhaseEnded))
			if len(ended) != 1 || ended[0].role != string(tc.ended) {
				t.Fatalf("the conflict wrote the phase-ended rows %+v, want one to the %s", ended, tc.ended)
			}
			if ended[0].claim != string(worked) {
				t.Fatalf("the phase-ended row names the claim %q, want %s, the agent that worked the %s phase", ended[0].claim, worked, from)
			}
			if follows := ended[0].follows == "true"; follows != tc.follows {
				t.Fatalf("the phase-ended row to %s says its task follows = %t, want %t", worked, follows, tc.follows)
			}
		})
	}
}

// A hook that fires on a move back writes a prompt for each role it names, and each names the
// claim of the agent that works its role's step in the round the move opened, read once the move's
// start has written that agent: the outbox publishes on that claim's topic, whatever a later entry
// of the role's step names when the row runs. In the plan `implementer:a tester:a reviewer` key
// a's agent, `…-implementer`, works both steps, so both prompts name it, and not the role's own
// claim `…-tester`.
func TestAHookPromptNamesTheAgentOfItsRolesStep(t *testing.T) {
	pool := migratedPool(t)
	engine := stepEngine(nil)
	engine.cfg.Hooks = []Hook{{Name: "work-back", On: HookEventWorkBack, Every: 1,
		Roles: []claim.Role{claim.RoleImplementer, claim.RoleTester}, Prompt: "Say what you have done so far."}}
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:a", "tester:a", "reviewer")
	move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
	move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})

	move(t, pool, engine, root.Key, TriggerTesterFailed, "", record.PhaseRow{})

	keyed := roleRowOf(t, pool, engine, root.Key, claim.RoleImplementer).Claim
	if keyed == claim.Token(token(t, root.Key, claim.RoleTester)) || keyed == "" {
		t.Fatalf("the implementer's phases row names %q after the move back, want key a's agent: the case's premise", keyed)
	}
	prompts := roleRowClaims(t, pool, root.Key, string(record.OutboxKindHookPrompt))
	if len(prompts) != 2 {
		t.Fatalf("the firing wrote the prompts %+v, want one for each role the hook names", prompts)
	}
	for _, prompt := range prompts {
		if want := roleRowOf(t, pool, engine, root.Key, claim.Role(prompt.role)).Claim; prompt.claim != string(want) || want != keyed {
			t.Errorf("the %s's prompt names the claim %q, want %s, key a's agent on its step", prompt.role, prompt.claim, keyed)
		}
	}
}
