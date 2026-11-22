package appauth

import (
	"context"
	"errors"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/plan"
)

// One function answers the App of every role from the role's definition: Legion's roles and the
// role acceptance by the switch, which nothing overrides; a role of the deployment by its
// github_app; a role of the issue's plan by its type, the implement App for the type
// implementation and the review App for every other type, no type among them. A role none of the
// three defines acts as no App. RoleApps.Of reads the plan of the claim's own issue, and only for
// a role that is neither Legion's nor the deployment's.
func TestTheAppOfARoleIsItsDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		role     claim.Role
		deployed DeploymentApps
		planned  Planned
		want     AppRole
	}{
		{"a role of a plan of the type implementation", "coder", nil, Planned{Defines: true, Type: plan.TypeImplementation}, Implement},
		{"a role of a plan of the type testing", "prober", nil, Planned{Defines: true, Type: plan.TypeTesting}, Review},
		{"a role of a plan of a type no list holds", "odd", nil, Planned{Defines: true, Type: "fuzzing"}, Review},
		{"a role of a plan of no type", "blank", nil, Planned{Defines: true}, Review},
		{"a role of the deployment over the plan", "checker", DeploymentApps{"checker": Implement}, Planned{Defines: true, Type: plan.TypeTesting}, Implement},
		{"Legion's implementer over the plan", claim.RoleImplementer, nil, Planned{Defines: true, Type: plan.TypeTesting}, Implement},
		{"the role acceptance", "acceptance", nil, Planned{}, Review},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := AppRoleFor(tc.role, tc.deployed, tc.planned); err != nil || got != tc.want {
				t.Fatalf("AppRoleFor(%s) = %q, %v; want %q", tc.role, got, err, tc.want)
			}
		})
	}
	_, err := AppRoleFor("ghost", nil, Planned{})
	const want = "the role ghost is none of Legion's, and neither the deployment nor the issue's plan defines it"
	if !errors.Is(err, ErrRoleNotDefined) || err.Error() != want {
		t.Fatalf("AppRoleFor(ghost) = %v; want an error that is ErrRoleNotDefined and reads %q", err, want)
	}

	type asked struct {
		issue string
		role  claim.Role
	}
	var calls []asked
	apps := RoleApps{Deployed: DeploymentApps{"checker": Review}, Plan: func(_ context.Context, issue string, role claim.Role) (Planned, error) {
		calls = append(calls, asked{issue, role})
		if issue == "LEGION-43" && role == "coder" {
			return Planned{Defines: true, Type: plan.TypeImplementation}, nil
		}
		return Planned{}, nil
	}}
	ctx := context.Background()
	if got, err := apps.Of(ctx, "LEGION-43", "coder"); err != nil || got != Implement {
		t.Errorf("Of(LEGION-43, coder) = %q, %v; want implement", got, err)
	}
	if _, err := apps.Of(ctx, "LEGION-44", "coder"); !errors.Is(err, ErrRoleNotDefined) {
		t.Errorf("Of(LEGION-44, coder) = %v; want ErrRoleNotDefined: the role is LEGION-43's alone", err)
	}
	if got, err := apps.Of(ctx, "LEGION-43", "checker"); err != nil || got != Review {
		t.Errorf("Of(LEGION-43, checker) = %q, %v; want review", got, err)
	}
	if got, err := apps.Of(ctx, "LEGION-43", "acceptance"); err != nil || got != Review {
		t.Errorf("Of(LEGION-43, acceptance) = %q, %v; want review", got, err)
	}
	for _, call := range calls {
		if call.role == "checker" || call.role == "acceptance" {
			t.Errorf("Of read the plan for %s, which the deployment or Legion defines", call.role)
		}
	}
}
