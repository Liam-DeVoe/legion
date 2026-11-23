package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A key's agent that ran on a claim before the upgrade keeps that claim's name, and so the session
// the claim holds, however many later versions of its plan leave the key out. A claim the release
// before made records no agent of its session (20261122120917 adds claims.session_agent at the
// empty string), and a claim that records none gives its session to the next start that reaches it
// (supervise.Claim.SessionOfAnotherAgent), so where nothing keeps the name for the key, a new agent
// given that name resumes the key's conversation. Version 1 of LEGION-208's plan,
// `implementer:a reviewer`, written by the release before, ran key a's agent on the claim
// implementer. Version 2, `implementer reviewer`, is named from version 1's rows, which name key a,
// so its implementer takes implementer-2, and its write replaces those rows. Version 3, `planner
// implementer reviewer`, finds no row of key a, and its implementer is a new agent: it takes
// implementer-2 again, never implementer. Version 4, `implementer:a reviewer`, gives key a
// implementer again. The fault: version 3's implementer took implementer.
//
// A start the release before queued for a key's first step makes that step's claim when it runs,
// after the upgrade, so it names the key (record.SuperviseRequest.Agent), and the claim it makes
// records the key, as the claim of a start of this release does.
//
// The controls: the claim of a name only agents with no key held, reviewer, still records no agent
// after the upgrade, and a start for such a claim names none, as the release before's rule for them
// says (20261122120917's header).
func TestAKeysClaimFromBeforeTheUpgradeKeepsItsNameAfterVersionsLeaveTheKeyOut(t *testing.T) {
	ctx := context.Background()
	st := emptyStore(t)
	migrateThrough(t, st, claimSessionAgentVersion-1)
	records := record.NewStore()
	owner, err := claim.AgentToken("legion", "LEGION-208", claim.Agent(claim.RoleArchitect))
	if err != nil {
		t.Fatal(err)
	}
	// put writes version of issue's plan at generation 1, task t with words as its steps, each
	// `role` or `role:key`, named as every writer names them (record.NameAgents): from the issue's
	// rows and, where withSessions, the names whose claims hold a key's session, which the release
	// before had no record of. It answers the names.
	put := func(issue string, version int, withSessions bool, words ...string) []claim.Agent {
		t.Helper()
		var names []claim.Agent
		if err := st.Tx(ctx, func(tx pgx.Tx) error {
			steps := make([]record.PlanStep, len(words))
			for i, word := range words {
				role, key, _ := strings.Cut(word, ":")
				steps[i] = record.PlanStep{Issue: issue, Generation: 1, Task: "t", Position: i + 1, Role: role, Type: "implementation", AgentKey: key}
			}
			earlier, err := records.PlanStepsOf(ctx, tx, issue)
			if err != nil {
				return err
			}
			var sessions record.KeySessions
			if withSessions {
				claims, err := records.IssueClaims(ctx, tx, "legion", issue)
				if err != nil {
					return err
				}
				sessions = record.KeySessionsOf(claims)
			}
			steps = record.NameAgents(earlier, sessions, steps)
			v := record.PlanVersion{Issue: issue, Generation: 1, Version: version, ArtifactID: "plan-artifact",
				Source: record.PlanSourceOwner, GivenBy: owner, GivenAt: time.Date(2026, 11, 22, 0, version, 0, 0, time.UTC)}
			task := record.PlanTask{Issue: issue, Generation: 1, Task: "t", PlanVersion: version, BlockText: "Build the widget.", State: record.TaskWaiting}
			if err := records.PutPlan(ctx, tx, v, []record.PlanTask{task}, steps); err != nil {
				return err
			}
			for _, step := range steps {
				names = append(names, step.Agent)
			}
			return nil
		}); err != nil {
			t.Fatalf("write version %d of %s's plan %v: %v", version, issue, words, err)
		}
		return names
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := st.Pool().Exec(ctx, statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	// The release before: its writes name no column it does not know.
	if got := put("LEGION-208", 1, false, "implementer:a", "reviewer"); !slices.Equal(got, []claim.Agent{"implementer", "reviewer"}) {
		t.Fatalf("the names of version 1 = %q, want implementer and reviewer", got)
	}
	if got := put("LEGION-209", 1, false, "implementer:b", "reviewer"); !slices.Equal(got, []claim.Agent{"implementer", "reviewer"}) {
		t.Fatalf("the names of LEGION-209's version 1 = %q, want implementer and reviewer", got)
	}
	const releaseBeforesClaim = `insert into claims (token, project, tree, issue, role, generation, session, session_file, state,
		launch_failures, prompt_failures, prompt_retires, uncertain_streak)
		values ($1, 'legion', $2, $2, $3, 1, $4, '', 'idle', 0, 0, 0, 0)`
	exec(releaseBeforesClaim, "legion-legion-legion-208-implementer", "LEGION-208", "implementer", "ses_key_a")
	exec(releaseBeforesClaim, "legion-legion-legion-208-reviewer", "LEGION-208", "reviewer", "ses_reviewer")
	startOf := func(token string) string {
		return `{"op":"start","tree":"LEGION-209","role":"` + strings.TrimPrefix(token, "legion-legion-legion-209-") +
			`","claim":"` + token + `","generation":1,"phase":"implementing","entry":1,"task":"Build it."}`
	}
	for _, token := range []string{"legion-legion-legion-209-implementer", "legion-legion-legion-209-reviewer"} {
		exec(`insert into outbox (kind, issue, payload, attempts, next_at, last_error, lease_token)
			values ('supervise', 'LEGION-209', $1, 0, now(), '', '')`, startOf(token))
	}

	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate to this release: %v", err)
	}

	t.Run("key a keeps implementer", func(t *testing.T) {
		if got := put("LEGION-208", 2, true, "implementer", "reviewer"); !slices.Equal(got, []claim.Agent{"implementer-2", "reviewer"}) {
			t.Fatalf("the names of version 2 = %q, want implementer-2 and reviewer: version 1's rows name key a's implementer", got)
		}
		if got := put("LEGION-208", 3, true, "planner", "implementer", "reviewer"); !slices.Equal(got, []claim.Agent{"planner", "implementer-2", "reviewer"}) {
			t.Fatalf("the names of version 3 = %q, want planner, implementer-2 and reviewer: its implementer is a new agent, and "+
				"the claim implementer holds key a's session, which that claim gives to the next start that reaches it", got)
		}
		if got := rowsAsText(t, st, `select concat(token, '=', session_agent) from claims where issue = 'LEGION-208' order by token`); !slices.Equal(got,
			[]string{"legion-legion-legion-208-implementer=key:a", "legion-legion-legion-208-reviewer="}) {
			t.Fatalf("the claims of LEGION-208 = %q, want the implementer's recording key a and the reviewer's none", got)
		}
		if got := put("LEGION-208", 4, true, "implementer:a", "reviewer"); !slices.Equal(got, []claim.Agent{"implementer", "reviewer"}) {
			t.Fatalf("the names of version 4 = %q, want implementer and reviewer: key a comes back to the claim of its session", got)
		}
	})

	t.Run("a queued start of key b names it", func(t *testing.T) {
		for token, want := range map[string]string{"legion-legion-legion-209-implementer": "key:b", "legion-legion-legion-209-reviewer": ""} {
			payloads := rowsAsText(t, st, `select payload::text from outbox where kind = 'supervise' and payload->>'claim' = '`+token+`'`)
			if len(payloads) != 1 {
				t.Fatalf("the starts of %s = %q, want one", token, payloads)
			}
			decoded, err := record.DecodeOutboxPayload(record.OutboxRow{Kind: record.OutboxKind("supervise"), Payload: []byte(payloads[0])})
			if err != nil {
				t.Fatalf("decode the start of %s: %v", token, err)
			}
			if got := string(decoded.(record.SuperviseRequest).Agent); got != want {
				t.Errorf("the start of %s names the agent %q, want %q", token, got, want)
			}
		}
	})
}
