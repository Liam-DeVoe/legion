package legionagents

import (
	"regexp"
	"strings"
)

// RolePattern is the role grammar as a pattern of a whole name, `^[a-z][a-z0-9-]{0,31}$`: the
// spelling a JSON schema's `pattern` and a check of the database give it. Of its names, every one
// but the controller's is a role of an issue (IsRole).
const RolePattern = `^` + roleGrammar + `$`

// IsRole reports whether role is a role of an issue: one of Roles, or a role the deployment
// defines in its settings, a name of RolePattern. The controller's name is of the grammar and is
// the project's, no issue's, so it is none. It is the one rule each reader of an issue's role
// outside this package takes, as an entry of the registry takes it (Validate).
func IsRole(role string) bool { return isRole(role) }

// claimTokenPattern is a Legion claim token of an issue, legion-<project>-<issue project,
// lowercased>-<issue number>-<agent> (the daemon's claim.AgentToken), split by its fields as the
// daemon's claim.Token.Cut splits it: a project token holds no "-", and neither part of an issue
// key does, so the agent's name is everything after the fourth "-" and may hold a "-" of its own
// (code-checker). The controller's token, legion-<project>-controller, names no issue number and
// does not match.
var claimTokenPattern = regexp.MustCompile(`^legion-([a-z0-9]+)-([a-z][a-z0-9]*)-([0-9]+)-(.+)$`)

// CutClaimToken splits a Legion claim token of an issue into the Legion project token it carries,
// the Dispatch issue key it names (upper case, ACME-7) and the name of its agent. The agent's name
// is the role of the agent's first step, one of Roles or a role the deployment defines, and stays
// the agent's whatever role its later steps are on, so a token whose last part is no role of an
// issue (IsRole), the controller's name among them, is no claim of an issue: ok is false for it
// and for every token of another shape. It is the one split of a claim token in Envoy, which the
// registry's entries (Validate) and the broker's reading of a pod's issue (cipher/requests) take.
func CutClaimToken(token string) (project, issue, agent string, ok bool) {
	match := claimTokenPattern.FindStringSubmatch(token)
	if match == nil || !isRole(match[4]) {
		return "", "", "", false
	}
	return match[1], strings.ToUpper(match[2]) + "-" + match[3], match[4], true
}
