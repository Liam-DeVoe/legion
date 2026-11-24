package workflow

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The owner's catch-up at a launch names the open merge forward asked of it only where the owner's
// claim as it is ready now was not told it, by the one rule of told an owner-step is judged by
// (toldNow): a mark that names a session is decided by that session alone, and a mark with no
// session, a claim that recorded none, by its launch. On `implementer:owner reviewer` at the
// reviewer's step, with the owner's claim in the session ses at its launch 3: an ask told nobody
// is named; an ask told in ses is not; an ask told in an older session is; an ask told at launch 3
// with no session is not named at launch 3, and is at launch 4. The value is the pull request the
// catch-up names (untoldMergeForward), not its words. The control: the ask of a fresh implementer,
// whose message no catch-up of the owner names.
func TestTheOwnersCatchUpNamesAnOpenAskOnlyWhereItsClaimWasNotToldIt(t *testing.T) {
	owner := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleArchitect))
	implementer := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleImplementer))
	reviewer := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleReviewer))
	for _, tc := range []struct {
		name   string
		agent  claim.Token
		told   *record.OwnerTold
		launch uint64
		names  bool
	}{
		{name: "an ask told nobody", agent: owner, launch: 3, names: true},
		{name: "an ask told in the session the claim runs in", agent: owner, told: &record.OwnerTold{Launch: 3, Session: "ses"}, launch: 3},
		{name: "an ask told in an older session", agent: owner, told: &record.OwnerTold{Launch: 2, Session: "ses_old"}, launch: 3, names: true},
		{name: "an ask told at this launch, no session recorded", agent: owner, told: &record.OwnerTold{Launch: 3}, launch: 3},
		{name: "an ask told at an earlier launch, no session recorded", agent: owner, told: &record.OwnerTold{Launch: 3}, launch: 4, names: true},
		{name: "control: the ask of a fresh implementer", agent: implementer, launch: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			words := []string{"implementer:owner", "reviewer"}
			if tc.agent == implementer {
				words[0] = "implementer"
			}
			pool := standsAtStep(t, phase.Reviewing, 2, words, []claim.Token{tc.agent, reviewer}, tc.agent)
			seedIdleOwner(t, pool, "LEGION-208", "LEGION-208", 3)
			pr := recordedPR(t, pool, "LEGION-208")
			pr.MergeForward.Told = tc.told
			seedPR(t, pool, pr)
			issue := issueOf(t, pool, returnEngine(), "LEGION-208")
			var named *record.PullRequest
			seedRecord(t, pool, func(tx pgx.Tx) (err error) {
				named, err = untoldMergeForward(t.Context(), tx, record.NewStore(), issue, tc.launch)
				return err
			})
			if (named != nil) != tc.names || (named != nil && (named.Number != 42 || named.MergeForward.Head != "head-42")) {
				t.Fatalf("the catch-up at launch %d names %+v, want the open ask of #42 at head-42 named: %t", tc.launch, named, tc.names)
			}
		})
	}
}
