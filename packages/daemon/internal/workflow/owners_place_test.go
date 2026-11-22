package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// ownersAcceptanceWords is what the catch-up says after the keys of the issues waiting in accepting
// for the architect's acceptance.
const ownersAcceptanceWords = "(where its task holds acceptance:owner, the acceptance is yours and no worker gives it: read the evidence, " +
	"then sign_off or request_backward_move; a step acceptance with no key is its fresh agent's)"

// The start a step's record names at accepting (startAgent: admission's promotion of a member and
// the boot's open of a run no start counted read it) is the owner's architect only where the
// acceptance is the owner's own step: for `acceptance:owner` the tree's architect, which owns the
// root, and for `acceptance` with no key the fresh agent of acceptance, the claim that does the
// step. The control is the accepting case that names the architect whoever does the step: the
// fresh agent's step is recorded as the architect's.
func TestAnAcceptanceStartNamesTheAgentThatDoesIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		words []string
		want  func(t *testing.T) string
	}{
		{"the owner's acceptance", []string{"implementer", "acceptance:owner"}, func(t *testing.T) string {
			return treeRoleToken(t, "LEGION-208", claim.RoleArchitect)
		}},
		{"the fresh agent's acceptance", []string{"implementer", "acceptance"}, func(t *testing.T) string {
			token, err := claim.AgentToken("legion", "LEGION-208", "acceptance")
			if err != nil {
				t.Fatalf("the fresh agent's token: %v", err)
			}
			return string(token)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			root := stepRoot(t, pool, phase.Accepting)
			seedStepsAt(t, pool, root.Key, root.Generation, 2, nil, tc.words...)
			issue := issueOf(t, pool, engine, root.Key)
			var got claim.Token
			applyInTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				got, err = engine.startAgent(ctx, tx, issue, phase.Accepting)
				return err
			})
			if want := tc.want(t); string(got) != want {
				t.Fatalf("the agent of %s's acceptance = %q, want %q", root.Key, got, want)
			}
		})
	}
}

// A sign-off ends the owner's own acceptance and no other agent's: the acceptance of a step
// `acceptance` with no key is the fresh agent of acceptance's, which ends it with its own verdict,
// so the owner's sign_off there is refused SIGNOFF_NOT_THE_OWNERS, naming the step and its agent,
// and the issue stays at the step. The twin is the owner's own acceptance, `acceptance:owner`,
// whose sign-off moves the task on to its retro, as an issue with no plan rows does (the tests of
// today's sign-off). The control is signOff reading the phase alone: the owner's sign-off of the
// fresh agent's step is taken and starts the retro before that agent's reading arrives.
func TestASignOffEndsOnlyTheOwnersOwnAcceptance(t *testing.T) {
	t.Run("the fresh agent's acceptance", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := freshAgentAccepting(t, pool, engine)
		result := signsOff(t, pool, engine, root.Key)
		const want = "step 3 (acceptance) of task a of LEGION-208 is not a step of yours: its agent is acceptance, which ends it with its own verdict, and a sign-off takes only an acceptance whose agent is the owner. This sign-off changed nothing."
		if result.Refusal == nil || result.Refusal.Status != 409 || result.Refusal.Code != "SIGNOFF_NOT_THE_OWNERS" || result.Refusal.Message != want {
			t.Fatalf("the owner's sign-off of the fresh agent's acceptance = %+v, want 409 SIGNOFF_NOT_THE_OWNERS saying %q", result.Refusal, want)
		}
		assertWalksAt(t, pool, engine, root.Key, phase.Accepting, stepPlace(3))
	})

	t.Run("the owner's own acceptance", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Accepting)
		seedStepsAt(t, pool, root.Key, root.Generation, 3, nil, "implementer", "reviewer", "acceptance:owner")
		if result := signsOff(t, pool, engine, root.Key); result.Refusal != nil {
			t.Fatalf("the owner's sign-off of its own acceptance was refused: %+v", result.Refusal)
		}
		assertWalksAt(t, pool, engine, root.Key, phase.Retro, record.TaskPlace{Kind: record.PlaceRetro})
	})
}

// A root architect's catch-up names, of the children waiting in accepting, only those whose
// acceptance is the owner's: of two children of LEGION-1 in accepting, LEGION-2 on `implementer
// acceptance`, which a fresh agent of acceptance accepts, and LEGION-3 on `implementer
// acceptance:owner`, it names LEGION-3 alone. The control is the list that takes every member in
// accepting: it names both.
func TestACatchUpNamesOnlyTheChildrenWhoseAcceptanceIsTheOwners(t *testing.T) {
	pool := migratedPool(t)
	root := seedAdmittedTree(t, pool, 1)
	root.Phase = phase.Implementing
	seedIssue(t, pool, root)
	parent := root.Key
	for key, words := range map[string][]string{"LEGION-2": {"implementer", "acceptance"}, "LEGION-3": {"implementer", "acceptance:owner"}} {
		seedIssue(t, pool, record.Issue{Key: key, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: root.Key, Project: "LEGION",
			Title: "Child", Parent: &parent, Phase: phase.Accepting, Generation: 1, Status: "needs_review", Rank: "V"})
		seedStepsAt(t, pool, key, 1, 2, nil, words...)
	}
	claimReady(t, pool, testEngine(config.DesignGateRootIssues, nil), root.Key, claim.RoleArchitect, 1)
	const want = "the tree of LEGION-1 at generation 1 as the daemon records it now; start or resume it as your role says" +
		"; in accepting: LEGION-3 " + ownersAcceptanceWords
	if got := catchUps(t, pool); len(got) != 1 || !strings.HasSuffix(got[0], " reason="+want) {
		t.Fatalf("catch-ups %v; want one whose reason is %q", got, want)
	}
}

// The architect's catch-up names what is its own to do by the rule of the owner's place: an
// acceptance whose agent is the owner waits for its acceptance, as an issue with no plan rows in
// accepting does, and any other step of the key owner of its own issue's task, at any phase, is
// named with its step and the report that ends it (complete_step). An acceptance a fresh agent of
// acceptance does is named for neither: the owner is not told to sign off another agent's step.
// The control is the catch-up that reads the phase alone: it tells the owner to sign off the fresh
// agent's step, and names the owner's implementer step nowhere.
func TestACatchUpNamesTheOwnersOwnStepWhereverItStands(t *testing.T) {
	const base = "the tree of LEGION-208 at generation 1 as the daemon records it now; start or resume it as your role says"
	for _, tc := range []struct {
		name  string
		at    phase.Phase
		place int
		words []string
		want  string
	}{
		{"the fresh agent's acceptance", phase.Accepting, 3, []string{"implementer", "reviewer", "acceptance"}, base},
		{"the owner's acceptance", phase.Accepting, 3, []string{"implementer", "reviewer", "acceptance:owner"},
			base + "; in accepting: LEGION-208 " + ownersAcceptanceWords},
		{"the owner's implementer step", phase.Implementing, 1, []string{"implementer:owner", "reviewer"},
			base + "; at a step of yours, which no worker does: LEGION-208 at step 1 (implementer) of task a (do it as its owner-step notice says, then report it with complete_step)"},
		{"another agent's implementer step", phase.Implementing, 1, []string{"implementer", "reviewer:owner"}, base},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			root := stepRoot(t, pool, tc.at)
			seedStepsAt(t, pool, root.Key, root.Generation, tc.place, nil, tc.words...)
			claimReady(t, pool, testEngine(config.DesignGateRootIssues, nil), root.Key, claim.RoleArchitect, 1)
			got := catchUps(t, pool)
			if len(got) != 1 || !strings.HasSuffix(got[0], " reason="+tc.want) {
				t.Fatalf("catch-ups %v; want one whose reason is %q", got, tc.want)
			}
		})
	}
}
