package store

import (
	"context"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The teardown of a release that suspends no role reads again the one suspension the rule that
// marked it holds a pod for: an operator's own (pods.IdleClaim.OperatorSuspended). An operator
// can make it after the mark, by suspending a claim that is already suspended, and the teardown
// then cancels the release under its own cause rather than delete the pod. Any other source
// holds nothing, and an idle release, whose rule reads no operator's suspension when it marks,
// reads none here either.
func TestTheTeardownOfAReleaseThatSuspendsNoRoleReadsTheOperatorsSuspension(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      string
		by        supervise.SuspendSource
		want      ReleaseAct
		wantCause ReleaseCause
	}{
		{name: "an operator's suspension holds a claim of the pod", kind: releaseKindUnowned,
			by: supervise.SuspendedByOperator, want: ReleaseCancel, wantCause: ReleaseCauseOperatorSuspended},
		{name: "the claim is still released", kind: releaseKindUnowned, by: supervise.SuspendedByReleased, want: ReleaseActs},
		{name: "a daemon's suspension holds the claim", kind: releaseKindUnowned, by: supervise.SuspendedByDaemon, want: ReleaseActs},
		// The idle release's own architect is released by it; the planner is the operator's,
		// which that rule's mark never read, so its teardown does not read it either.
		{name: "an idle release beside an operator's suspension it never read", kind: releaseKindIdle,
			by: supervise.SuspendedByOperator, want: ReleaseActs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := migratedStore(t)
			ctx := context.Background()
			openRunningTree(t, st)
			role, suspended := "", []string{}
			if tc.kind == releaseKindIdle {
				role, suspended = string(claim.RoleArchitect), []string{string(claim.RoleArchitect)}
			}
			if _, err := st.Pool().Exec(ctx, `insert into issue_releases
				(project, issue, generation, first_row, boundary_row, last_row, started_row, role, kind, suspended, marked_at)
				values ('legion', $1, 1, 10, 10, 10, 0, $2, $3, $4, now())`, rootIssue, role, tc.kind, suspended); err != nil {
				t.Fatalf("mark the %s release: %v", tc.kind, err)
			}
			tokenOf := func(role claim.Role) claim.Token {
				t.Helper()
				token, err := claim.NewToken("legion", rootIssue, role)
				if err != nil {
					t.Fatal(err)
				}
				return token
			}
			claims := []supervise.Claim{{Token: tokenOf(claim.RoleArchitect), Role: claim.RoleArchitect,
				SuspendedBy: supervise.SuspendedByReleased}}
			if tc.kind == releaseKindIdle {
				claims = append(claims, supervise.Claim{Token: tokenOf(claim.RolePlanner), Role: claim.RolePlanner,
					SuspendedBy: tc.by})
			} else {
				claims[0].SuspendedBy = tc.by
			}
			for _, c := range claims {
				c.Project, c.Tree, c.Issue, c.TreeEpoch, c.Generation, c.State = "legion", rootIssue, rootIssue, 1, 1, supervise.StateSuspended
				if err := st.PutClaim(ctx, c); err != nil {
					t.Fatalf("store the %s claim: %v", c.Role, err)
				}
			}

			release := IssueReleased{Issue: rootIssue, Tree: rootIssue, IssueGeneration: 1, TreeGeneration: 1, Row: 10}
			decision, err := st.IssueRelease(ctx, "legion", release, record.OutOfWorkflow)
			if err != nil || decision.Act != tc.want || decision.Cause != tc.wantCause {
				t.Fatalf("the teardown = %s (cause %s), %v; want %s (cause %s)", decision.Act, decision.Cause, err, tc.want, tc.wantCause)
			}
		})
	}
}
