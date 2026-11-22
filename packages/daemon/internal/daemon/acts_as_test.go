package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// The daemon's ActsAs is the owner's step: LEGION-1 stands at step 1 of `implementer:owner
// reviewer`, which its owner, LEGION-1's own architect, does. Its architect's slot acts as the
// implementer; LEGION-2's architect, the owner of an issue that is not LEGION-1, and LEGION-1's
// implementer act as nothing, and neither does a token of an issue the daemon does not record.
// runtime.SlotRole, given that ActsAs, answers the implementer for LEGION-1's architect and the
// role it is given for the others, each claim's stored role.
func TestTheDaemonsActsAsIsTheOwnersStep(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	issue := func(key string, at phase.Phase) record.Issue {
		return record.Issue{Key: key, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: key, Project: "LEGION",
			Title: key, Phase: at, Generation: 1, Status: "in_progress", DispatchStatus: "in_progress", Rank: "A", LastDispatchSeq: 1}
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		version := record.PlanVersion{Issue: "LEGION-1", Generation: 1, Version: 1, ArtifactID: "plan-artifact", Source: record.PlanSourceOwner,
			GivenBy: mustClaimToken(t, "LEGION-1", claim.RoleArchitect), GivenAt: now}
		task := record.PlanTask{Issue: "LEGION-1", Generation: 1, Task: "a", PlanVersion: 1, BlockText: "Build the widget.",
			State: record.TaskRunning, Place: record.TaskPlace{Kind: record.PlaceStep, Position: 1}}
		steps := []record.PlanStep{
			{Issue: "LEGION-1", Generation: 1, Task: "a", Position: 1, Role: "implementer", Type: "implementation", AgentKey: "owner"},
			{Issue: "LEGION-1", Generation: 1, Task: "a", Position: 2, Role: "reviewer", Type: "review"},
		}
		for _, put := range []error{
			records.PutIssue(ctx, tx, issue("LEGION-1", phase.Implementing)),
			records.PutIssue(ctx, tx, issue("LEGION-2", phase.Planning)),
			records.PutPlan(ctx, tx, version, []record.PlanTask{task}, steps),
		} {
			if put != nil {
				return put
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed LEGION-1 at its owner's implementer step and LEGION-2: %v", err)
	}

	actsAs := ownerActsAs(pool, records)
	for _, tc := range []struct {
		name   string
		token  claim.Token
		stored claim.Role
		want   claim.Role
		acting bool
	}{
		{"LEGION-1's architect, its owner", mustClaimToken(t, "LEGION-1", claim.RoleArchitect), claim.RoleArchitect, claim.RoleImplementer, true},
		{"LEGION-2's architect, the owner of another issue", mustClaimToken(t, "LEGION-2", claim.RoleArchitect), claim.RoleArchitect, "", false},
		{"LEGION-1's implementer", mustClaimToken(t, "LEGION-1", claim.RoleImplementer), claim.RoleImplementer, "", false},
		{"the architect of an issue the daemon does not record", mustClaimToken(t, "LEGION-3", claim.RoleArchitect), claim.RoleArchitect, "", false},
	} {
		role, acting, err := actsAs(ctx, tc.token)
		if err != nil || role != tc.want || acting != tc.acting {
			t.Errorf("ActsAs(%s) = (%q, %v, %v), want (%q, %v, nil)", tc.name, role, acting, err, tc.want, tc.acting)
		}
		slot, err := runtime.SlotRole(ctx, actsAs, tc.token, tc.stored)
		want := tc.stored
		if tc.acting {
			want = tc.want
		}
		if err != nil || slot != want {
			t.Errorf("SlotRole(%s) = (%q, %v), want (%q, nil)", tc.name, slot, err, want)
		}
	}
}
