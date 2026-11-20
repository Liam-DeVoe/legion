package appauth

import (
	"fmt"

	"github.com/sjawhar/legion/daemon/internal/claim"
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

// SetFor is the one role-to-set decision: each role's token holds its own role's set. Keep this
// switch exhaustive over claim.Role, plus the one non-claim role it also decides: the project
// controller. No role maps to SetVerdict.
func SetFor(role claim.Role) TokenSet {
	switch role {
	case claim.RoleImplementer:
		return SetImplementer
	case claim.RoleMerger:
		return SetMerger
	case claim.RoleArchitect:
		return SetArchitect
	case claim.RolePlanner:
		return SetPlanner
	case claim.RoleTester:
		return SetTester
	case claim.RoleReviewer:
		return SetReviewer
	case claim.RoleController:
		return SetController
	}
	panic(fmt.Sprintf("SetFor: unsupported claim role %q", role))
}
