package appauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/plan"
)

// DeploymentApps is each role the deployment defines in its settings (`roles`) with the App its
// github_app names.
type DeploymentApps map[claim.Role]AppRole

// Planned is what the plan in force of a claim's issue says of a role: whether it defines the
// role, and the role's type there ("" where its block names none).
type Planned struct {
	Defines bool
	Type    plan.Type
}

// ErrRoleNotDefined is AppRoleFor's answer for a role that is none of Legion's and that neither
// the deployment nor the issue's plan defines: such a role acts as no App.
var ErrRoleNotDefined = errors.New("neither the deployment nor the issue's plan defines it")

// roleAcceptance is the role of the step `acceptance`, a role of Legion's that is no claim role.
const roleAcceptance claim.Role = "acceptance"

// AppRoleFor is the one role-to-App decision, the one place a change of how a role's App is
// chosen goes. Keep this switch exhaustive over Legion's claim roles, plus the two non-claim
// roles it also decides: the project controller, and the role acceptance, the fresh agent that
// reads the whole of a task's work, which both act as the review App (neither writes an issue
// branch, and a review never comes from the App that wrote the change). Roles that can write an
// issue branch use implement; every verdict-only role uses review. Neither the deployment's map
// nor a plan overrides one of these. Every other role acts as the App deployed names for it; a
// role deployed does not name acts as the App of its type in the issue's plan (planned): the
// implement App for the type implementation, the review App for every other type, no type among
// them. A role none of the three defines is ErrRoleNotDefined.
func AppRoleFor(role claim.Role, deployed DeploymentApps, planned Planned) (AppRole, error) {
	switch role {
	case claim.RoleArchitect, claim.RolePlanner, claim.RoleTester, claim.RoleReviewer, claim.RoleController, roleAcceptance:
		return Review, nil
	case claim.RoleImplementer, claim.RoleMerger:
		return Implement, nil
	}
	if app, ok := deployed[role]; ok {
		return app, nil
	}
	if planned.Defines {
		if planned.Type == plan.TypeImplementation {
			return Implement, nil
		}
		return Review, nil
	}
	return "", fmt.Errorf("the role %s is none of Legion's, and %w", role, ErrRoleNotDefined)
}

// RoleApps is what a reader of a claim's App holds: the deployment's roles, and the one read of
// a role of an issue's plan in force. The daemon builds the one value (internal/daemon's
// roleApps), whose Plan is the daemon's one reader of a role of a plan.
type RoleApps struct {
	Deployed DeploymentApps
	// Plan is what the plan in force of issue says of role; nil reads no plan, so a role of a plan
	// acts as no App.
	Plan func(ctx context.Context, issue string, role claim.Role) (Planned, error)
}

// Of is the App of role for a claim of issue: AppRoleFor, with the plan read only for a role that
// is none of Legion's and none of the deployment's. A role of a plan is its issue's alone, since
// two issues can define one name with two types, so a reader names the claim's issue.
func (a RoleApps) Of(ctx context.Context, issue string, role claim.Role) (AppRole, error) {
	app, err := AppRoleFor(role, a.Deployed, Planned{})
	if err == nil || !errors.Is(err, ErrRoleNotDefined) || a.Plan == nil {
		return app, err
	}
	planned, err := a.Plan(ctx, issue, role)
	if err != nil {
		return "", fmt.Errorf("the plan of %s for the role %s: %w", issue, role, err)
	}
	return AppRoleFor(role, a.Deployed, planned)
}
