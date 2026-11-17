package claim

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	// issueKey is a Dispatch issue key, the one shape an issue has (legion-roles.ts:30).
	issueKey = regexp.MustCompile(`^([A-Z][A-Z0-9]*)-([0-9]+)$`)
	// projectToken is a project as a token spells it (legion-roles.ts:28).
	projectToken = regexp.MustCompile(`^[a-z0-9]+$`)
	// notAlphanumeric is everything ProjectToken drops.
	notAlphanumeric = regexp.MustCompile(`[^a-z0-9]`)
	// roleName is a role a deployment defines: the role grammar of Dispatch's step record
	// (packages/envoy/internal/dispatch/workflowstore/write.go), which each of Legion's six matches.
	roleName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// Roles are Legion's six roles, in the order the workflow reaches them. A claim can also be on a
// role the deployment defines (IsRole).
var Roles = []Role{RoleArchitect, RolePlanner, RoleImplementer, RoleTester, RoleReviewer, RoleMerger}

// ProjectToken is the project as every token, and the private tmux server, spell it: the
// operator's `project` lowercased with every character outside [a-z0-9] dropped — the shipped
// daemon's rule (`legionProjectToken`, packages/contracts/src/legion-roles.ts), so `cybermonklabs/legion`
// names the same role topics under either daemon.
func ProjectToken(project string) (string, error) {
	token := notAlphanumeric.ReplaceAllString(strings.ToLower(project), "")
	if token == "" {
		return "", fmt.Errorf("project %q must include at least one alphanumeric character", project)
	}
	return token, nil
}

// IsIssueKey reports whether key is a Dispatch issue key (`LEGION-208`).
func IsIssueKey(key string) bool { return issueKey.MatchString(key) }

// IsRole reports whether a claim can be on role: one of Legion's six, or a role the deployment
// defines, a name of `^[a-z][a-z0-9-]{0,31}$`. The controller is none: its claim holds no issue.
func IsRole(role Role) bool {
	return role != RoleController && roleName.MatchString(string(role))
}

// IsBuiltin reports whether role is one of Legion's six (Roles), and no role of the deployment.
func IsBuiltin(role Role) bool { return slices.Contains(Roles, role) }

// NewToken is the token of the claim on issue's role: `legion-<project>-<issue project,
// lowercased>-<number>-<role>`, byte for byte the shipped roleToken
// (packages/contracts/src/legion-roles.ts:48-55). The token is the Envoy role the agent claims, so
// it has Envoy's role-token shape, and a peer that derives it from the issue and role in either
// language reaches the same agent. project is already a ProjectToken.
func NewToken(project, issue string, role Role) (Token, error) {
	if !projectToken.MatchString(project) {
		return "", fmt.Errorf("claim token: project %q is not a project token", project)
	}
	match := issueKey.FindStringSubmatch(issue)
	if match == nil {
		return "", fmt.Errorf("claim token: %q is not an issue key", issue)
	}
	if !IsRole(role) {
		return "", fmt.Errorf("claim token: %q is not a role", role)
	}
	return Token(fmt.Sprintf("legion-%s-%s-%s-%s", project, strings.ToLower(match[1]), match[2], role)), nil
}

// Cut splits a role claim's token, `legion-<project>-<issue project>-<number>-<role>`, at its first
// four "-": into the issue's part, `legion-<project>-<issue>`, which every role claim of the issue
// shares, and the role after it, which may hold a "-" of its own (`code-checker`). ok is false for
// a token that ends in no role (IsRole), such as the controller's.
func (t Token) Cut() (issue string, role Role, ok bool) {
	parts := strings.SplitN(string(t), "-", 5)
	if len(parts) != 5 || parts[0] != "legion" || !IsRole(Role(parts[4])) {
		return string(t), "", false
	}
	return strings.Join(parts[:4], "-"), Role(parts[4]), true
}

// ControllerToken is the project controller's role token, `legion-<project>-controller`
// (legion-roles.ts:66-69). project is already a ProjectToken.
func ControllerToken(project string) Token { return Token("legion-" + project + "-controller") }

// Role is the role t names: the role it ends in (Cut), or RoleController for the project
// controller's token (ControllerToken); ok is false for a token that is neither.
func (t Token) Role() (role Role, ok bool) {
	if _, role, ok := t.Cut(); ok {
		return role, true
	}
	project, prefixed := strings.CutPrefix(string(t), "legion-")
	project, suffixed := strings.CutSuffix(project, "-"+string(RoleController))
	if prefixed && suffixed && projectToken.MatchString(project) {
		return RoleController, true
	}
	return "", false
}
