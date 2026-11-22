package appauth

import (
	"errors"
	"fmt"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/plan"
)

// TokenSet names the permissions a write token is minted with (writePermissions): one set per
// Legion role, and the daemon's own verdict token. One App serves every set, so a set says what a
// token may do and never which App mints it. Each value is the role's own name, the word a token
// command's profile is chosen by.
type TokenSet string

const (
	SetImplementer TokenSet = "implementer"
	SetMerger      TokenSet = "merger"
	SetArchitect   TokenSet = "architect"
	SetPlanner     TokenSet = "planner"
	SetTester      TokenSet = "tester"
	SetReviewer    TokenSet = "reviewer"
	SetController  TokenSet = "controller"
	// SetVerdict is the daemon's own token, the one set that writes check runs and commit
	// statuses. No role is handed it: Tokens.Token refuses it, and only Tokens.VerdictToken
	// mints it.
	SetVerdict TokenSet = "verdict"
)

// ErrNoTokenSet is SetFor's refusal of a role it keeps no set for: a role that is none of
// Legion's own and no role a step can name, or a role whose type no set is kept for. Every mint
// for such a role fails with it (GitHubCredential, Bundles.Bundle, and internal/daemon's git
// identity of a launch), so a claim on that role fails its start, and the daemon goes on.
var ErrNoTokenSet = errors.New("no token set")

// stepRoles are the roles a step can name, by name (plan.BuiltinRoles), each with its type. It is
// read once and never changed.
var stepRoles = plan.BuiltinRoles()

// SetFor is the one role-to-set decision: the set every token of role's claim is minted with. The
// three roles no step names hold their own sets: the owner of an issue (the architect) and the
// project controller the review set, and the merger the implementation set. A role a step can
// name takes the default set of its type (setForStep), so a role a plan defines needs no set of
// its own. Any other role has no set: SetFor answers ErrNoTokenSet naming it, and never panics,
// so the mint asked for it fails and the daemon does not. No role maps to SetVerdict.
func SetFor(role claim.Role) (TokenSet, error) {
	switch role {
	case claim.RoleArchitect:
		return SetArchitect, nil
	case claim.RoleMerger:
		return SetMerger, nil
	case claim.RoleController:
		return SetController, nil
	}
	if def, ok := stepRoles[string(role)]; ok {
		return setForStep(def)
	}
	return "", fmt.Errorf("%w for the role %q: it is not the architect, the merger, the controller or a role a step can name", ErrNoTokenSet, role)
}

// setForStep is the default set of the type of def, a role a step can name (plan.RoleDef.Type):
// the implementer's for the type implementation, the tester's for the type testing, the
// planner's for the type planning, and the reviewer's, the review set, for the types design and
// review. A set's name is also the word a token command's profile is chosen by (TokenSet), so a
// design role's token is the reviewer's with the key and with a token command alike. A type
// outside these has no set: ErrNoTokenSet, naming the role and its type.
func setForStep(def plan.RoleDef) (TokenSet, error) {
	switch def.Type {
	case plan.TypeImplementation:
		return SetImplementer, nil
	case plan.TypeTesting:
		return SetTester, nil
	case plan.TypePlanning:
		return SetPlanner, nil
	case plan.TypeDesign, plan.TypeReview:
		return SetReviewer, nil
	}
	return "", fmt.Errorf("%w for the role %q: its type %q is none of implementation, testing, planning, design and review", ErrNoTokenSet, def.Name, def.Type)
}
