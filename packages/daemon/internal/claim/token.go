package claim

import (
	"fmt"
	"regexp"
	"strings"
)

// agentGrammar is the name of an agent, the last part of its token: the role grammar of a
// deployment, at most 32 bytes.
const agentGrammar = `[a-z][a-z0-9-]{0,31}`

var (
	// issueKey is a Dispatch issue key, the one shape an issue has (legion-roles.ts:30).
	issueKey = regexp.MustCompile(`^([A-Z][A-Z0-9]*)-([0-9]+)$`)
	// projectToken is a project as a token spells it (legion-roles.ts:28).
	projectToken = regexp.MustCompile(`^[a-z0-9]+$`)
	// agentName is an agent's name alone.
	agentName = regexp.MustCompile(`^` + agentGrammar + `$`)
	// agentToken is a claim token of an issue cut after its fourth "-": the issue's part,
	// `legion-<project>-<issue project>-<number>`, three parts with no dash in them, then the agent's
	// name, which can hold dashes of its own.
	agentToken = regexp.MustCompile(`^(legion-[^-]+-[^-]+-[^-]+)-(` + agentGrammar + `)$`)
	// notAlphanumeric is everything ProjectToken drops.
	notAlphanumeric = regexp.MustCompile(`[^a-z0-9]`)
)

// Roles are the six roles a claim can be on, in the order the workflow reaches them.
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

// IsRole reports whether role is one of the six a claim can be on.
func IsRole(role Role) bool {
	for _, known := range Roles {
		if role == known {
			return true
		}
	}
	return false
}

// NewToken is the token of the agent named for role on issue, AgentToken(project, issue,
// Agent(role)): `legion-<project>-<issue project, lowercased>-<number>-<role>`, byte for byte the
// shipped roleToken (packages/contracts/src/legion-roles.ts:48-55). It refuses a role that is none
// of Roles, which its callers read as "no claim of this role". project is already a ProjectToken.
func NewToken(project, issue string, role Role) (Token, error) {
	if !IsRole(role) {
		return "", fmt.Errorf("claim token: %q is not a role", role)
	}
	return AgentToken(project, issue, Agent(role))
}

// AgentToken is the claim token of agent on issue: `legion-<project>-<issue project,
// lowercased>-<number>-<agent>`. The token is the Envoy role the agent claims, so it has Envoy's
// role-token shape, and a peer that derives it from the issue and the name in either language
// reaches the same agent. It refuses a name outside the agent grammar (agentName). project is
// already a ProjectToken.
func AgentToken(project, issue string, agent Agent) (Token, error) {
	if !projectToken.MatchString(project) {
		return "", fmt.Errorf("claim token: project %q is not a project token", project)
	}
	match := issueKey.FindStringSubmatch(issue)
	if match == nil {
		return "", fmt.Errorf("claim token: %q is not an issue key", issue)
	}
	if !agentName.MatchString(string(agent)) {
		return "", fmt.Errorf("claim token: %q is not an agent name", agent)
	}
	return Token(fmt.Sprintf("legion-%s-%s-%s-%s", project, strings.ToLower(match[1]), match[2], agent)), nil
}

// Cut splits a claim's token into the issue's part, `legion-<project>-<issue>`, which every claim
// of the issue shares, and the name of the agent it ends in: everything after the fourth "-",
// which can hold dashes of its own (`implementer-2`). ok is false for a token with no issue's part
// or no agent's name, such as the controller's.
func (t Token) Cut() (issue string, agent Agent, ok bool) {
	match := agentToken.FindStringSubmatch(string(t))
	if match == nil {
		return string(t), "", false
	}
	return match[1], Agent(match[2]), true
}

// ControllerToken is the project controller's role token, `legion-<project>-controller`
// (legion-roles.ts:66-69). project is already a ProjectToken.
func ControllerToken(project string) Token { return Token("legion-" + project + "-controller") }

// IsController says whether t is the project controller's token, ControllerToken(p) for a project
// token p.
func (t Token) IsController() bool {
	project, prefixed := strings.CutPrefix(string(t), "legion-")
	project, suffixed := strings.CutSuffix(project, "-"+string(RoleController))
	return prefixed && suffixed && projectToken.MatchString(project)
}
