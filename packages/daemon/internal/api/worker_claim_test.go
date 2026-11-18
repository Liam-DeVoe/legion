package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// keyedTester records LEGION-209, a child of LEGION-208 testing, and its tester row as the
// tester's step of `implementer:a tester:a reviewer` leaves it: it names key a's claim,
// `…-implementer`, and runs step 2 of task a.
func keyedTester(t *testing.T, h *harness, keyed claim.Token) {
	t.Helper()
	ctx := context.Background()
	parent := "LEGION-208"
	h.recordIssue(record.Issue{Key: "LEGION-209", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: parent,
		Project: testProject, Title: "LEGION-209", Parent: &parent, Phase: phase.Testing, Generation: 1, Status: "in_progress"})
	if err := h.store.Tx(ctx, func(tx pgx.Tx) error {
		return record.NewStore().PutPhase(ctx, tx, record.PhaseRow{Issue: "LEGION-209", Role: claim.RoleTester, Claim: keyed,
			StepTask: "a", StepPlace: 2, Entry: 2})
	}); err != nil {
		t.Fatalf("seed the tester's row of LEGION-209: %v", err)
	}
}

// While the tester's step of `implementer:a tester:a reviewer` runs, an operator's spawn of
// (LEGION-209, tester) acts on the claim that works that step, key a's `…-implementer`, which is
// launching, so the spawn is refused as one of a launched claim and launches nothing. A route that
// derived the role's own token would create and launch `…-tester`, a claim that runs no step.
func TestAnOperatorsSpawnOfARoleReachesTheClaimOfItsStepsAgent(t *testing.T) {
	h := newHarness(t)
	if recorder := h.operator(http.MethodPost, "/legion/v1/operator/claims", spawnBody()); recorder.Code != http.StatusCreated {
		t.Fatalf("spawn the tree's architect = %d; body %s", recorder.Code, recorder.Body)
	}
	worker := SpawnRequest{Tree: "LEGION-208", Issue: "LEGION-209", Role: claim.RoleImplementer, Prompt: "Reply ready and wait."}
	if recorder := h.operator(http.MethodPost, "/legion/v1/operator/claims", worker); recorder.Code != http.StatusCreated {
		t.Fatalf("spawn key a's agent = %d; body %s", recorder.Code, recorder.Body)
	}
	keyed, err := claim.NewToken(testProject, "LEGION-209", claim.RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	keyedTester(t, h, keyed)

	tester := worker
	tester.Role = claim.RoleTester
	wantRefusal(t, h.operator(http.MethodPost, "/legion/v1/operator/claims", tester), http.StatusConflict,
		"spawn refused: the claim is launching (the claim is already launched)")
	if spawns := h.runtime.CallsOf("Spawn"); len(spawns) != 2 {
		t.Fatalf("spawns = %d, want the architect's and key a's agent's alone", len(spawns))
	}
	stale, err := claim.NewToken(testProject, "LEGION-209", claim.RoleTester)
	if err != nil {
		t.Fatal(err)
	}
	if _, supervised := h.supervisor.Machine(stale); supervised {
		t.Fatalf("the spawn of the tester made the claim %s, which works no step", stale)
	}
}

// While the tester's step of `implementer:a tester:a reviewer` runs, the controller's fresh
// session of (LEGION-209, tester) names the claim that works that step, key a's `…-implementer`:
// it is launching, so the fresh session is refused naming it. A route that derived the role's own
// token would answer that it supervises no claim `…-tester`.
func TestTheControllersFreshSessionOfARoleNamesTheClaimOfItsStepsAgent(t *testing.T) {
	h := freshSessionHarness(t, &phaseRestarter{})
	keyed, _ := h.launchIn("LEGION-208", "LEGION-209", claim.RoleImplementer)
	keyedTester(t, h, keyed)

	recorder := h.request(http.MethodPost, "/legion/v1/claims/fresh-session",
		ClaimRequest{GrantID: controllerGrant(t, h).GrantID, Issue: "LEGION-209", Role: string(claim.RoleTester)}, nil)

	wantFreshSessionRefusal(t, recorder, http.StatusConflict, claimUnsettled,
		unsettledRefusal(keyed, supervise.StateLaunching, wantFreshSessionReaches))
}
