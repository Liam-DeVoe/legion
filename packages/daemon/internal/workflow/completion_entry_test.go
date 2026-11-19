package workflow

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The completion route judges the entry a completion is of on its own read, before the fact's
// lock (api's entryFence); the workflow judges it again on the role's phases row its own
// transaction reads, with the route's one rule (intake.BehindEntryClause), so a completion of an entry
// raised in between moves nothing. Here the route read the role's row on entry `first` and passed
// the completion; then the daemon's conflict move sent the work back to implementing while that
// turn still ran, and the implementer's completion entered the role's step again on entry
// `second`. The completion carries entry `first`, as the route builds it.
//
// The checker, a role of the deployment, runs as step, which ends with no handoff record: its
// completion of entry `first` is refused HANDOFF_STALE_ENTRY whatever commit it names, records
// nothing on the row, and leaves the fact unspent; the issue stays at step 2. The tester's testing
// ends with a record: its behind completion is refused at a commit the pull request has left, and
// taken at the head the pull request is at, as the route takes it. Each control is the completion
// of the entry that stands, which moves the issue on.
func TestACompletionOfAnEntryRaisedAfterTheRoutesReadMovesNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		role   claim.Role
		record string
		stale  bool
		commit string
		// refused is the refusal's code, "" for a completion that is taken.
		refused string
		// then is the phase the issue stands in after the completion.
		then phase.Phase
	}{
		{"the checker's pass of the entry the conflict left", "checker", "", true, stepHead, "HANDOFF_STALE_ENTRY", phase.Step},
		{"the control: the checker's pass of the entry that stands", "checker", "", false, stepHead, "", phase.Retro},
		{"the tester's pass of the entry the conflict left, at a commit the pull request left", claim.RoleTester, "test-2", true, stepHead, "HANDOFF_STALE_ENTRY", phase.Testing},
		{"the tester's pass of the entry the conflict left, at the pull request's head", claim.RoleTester, "test-2", true, "head", "", phase.Retro},
		{"the control: the tester's pass of the entry that stands", claim.RoleTester, "test-2", false, stepHead, "", phase.Retro},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := deploymentEngine()
			root := stepRoot(t, pool, phase.Admitted)
			seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", string(tc.role))
			at, version := phase.Testing, 1
			if tc.role != claim.RoleTester {
				at, version = phase.Step, 0
				if _, err := pool.Exec(t.Context(), "update plan_steps set type = 'testing' where issue = $1 and task = 'a' and position = 2", root.Key); err != nil {
					t.Fatalf("store the type of %s: %v", tc.role, err)
				}
			}
			move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
			stepReports(t, pool, engine, root.Key, claim.RoleImplementer, "", "built", "impl-1", 0)
			assertWalksAt(t, pool, engine, root.Key, at, stepPlace(2))
			first := roleRowOf(t, pool, engine, root.Key, tc.role).Entry

			// The daemon's own move on a head that conflicts with its base, while the role's turn
			// of entry `first` still runs; then the implementer's fix enters step 2 again.
			move(t, pool, engine, root.Key, TriggerConflict, record.TaskPlace{}, record.PhaseRow{})
			assertWalksAt(t, pool, engine, root.Key, phase.Implementing, stepPlace(1))
			stepReports(t, pool, engine, root.Key, claim.RoleImplementer, "", "rebased", "impl-2", 0)
			assertWalksAt(t, pool, engine, root.Key, at, stepPlace(2))
			second := roleRowOf(t, pool, engine, root.Key, tc.role)
			if second.Entry <= first {
				t.Fatalf("the %s stands on entry %d after the re-entry, want one above %d", tc.role, second.Entry, first)
			}

			entry := second.Entry
			if tc.stale {
				entry = first
			}
			result := applyFacts(t, pool, engine)(string(tc.role)+"-pass", intake.HandoffComplete{Generation: root.Generation, Issue: root.Key, Role: tc.role,
				Claim: claim.Token(string(tc.role) + "-claim"), Summary: "checked", Commit: tc.commit, Verdict: "pass", Entry: entry,
				HandoffRecord: tc.record, HandoffVersion: version})
			got := issueOf(t, pool, engine, root.Key)
			if refusalCode(result) != tc.refused || got.Phase != tc.then {
				t.Fatalf("the %s's pass of entry %d at %s, with entry %d standing at step 2: refusal %+v, %s now stands in %s; want refusal %q and %s",
					tc.role, entry, tc.commit, second.Entry, result.Refusal, root.Key, got.Phase, tc.refused, tc.then)
			}
			if tc.refused == "" {
				return
			}
			if !result.Unspent {
				t.Fatalf("the refused completion of entry %d left its fact spent, want it unspent so the same report can apply once it is the work of the entry that stands", entry)
			}
			if after := roleRowOf(t, pool, engine, root.Key, tc.role); after.Verdict != second.Verdict || !after.CompletedAt.Equal(second.CompletedAt) ||
				after.HandoffRecord != second.HandoffRecord || after.Entry != second.Entry {
				t.Fatalf("the refused completion wrote the %s's row: %+v, want it as it stood: %+v", tc.role, after, second)
			}
		})
	}
}
