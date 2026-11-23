package config

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// githubProxyBlock is an agent_secrets block whose github member is lines, each indented under it.
func githubProxyBlock(lines string) string {
	return "    agent_secrets:\n      url: https://secrets.internal.example\n" + agentSecretsTokenFile + "      github:\n" + lines
}

// runtime.kubernetes.agent_secrets.github maps a role to the secret names its GitHub access goes
// through the broker's proxy under, the controller among the roles; a block that names none leaves
// the map nil, which is every role's App token minted by the daemon as before.
func TestTheGitHubProxyMapSettlesByRole(t *testing.T) {
	cfg, err := LoadForValidation(writeConfigFile(t, kubernetesFile+githubProxyBlock(
		"        implementer: [LEGION_IMPLEMENT_APP]\n        reviewer: [LEGION_REVIEW_APP, LEGION_REVIEW_READ]\n        controller: [LEGION_REVIEW_APP]\n")), noEnv)
	if err != nil {
		t.Fatalf("load = %v", err)
	}
	got := cfg.Runtime.Kubernetes.AgentSecrets.GitHub
	want := map[claim.Role][]string{
		claim.RoleImplementer: {"LEGION_IMPLEMENT_APP"},
		claim.RoleReviewer:    {"LEGION_REVIEW_APP", "LEGION_REVIEW_READ"},
		claim.RoleController:  {"LEGION_REVIEW_APP"},
	}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("agent_secrets.github settled as %v, want %v", got, want)
	}

	cfg, err = LoadForValidation(writeConfigFile(t, kubernetesFile+"    agent_secrets:\n      url: https://secrets.internal.example\n"+agentSecretsTokenFile), noEnv)
	if err != nil || cfg.Runtime.Kubernetes.AgentSecrets.GitHub != nil {
		t.Fatalf("a block with no github member settled as %v, %v; want nil", cfg.Runtime.Kubernetes.AgentSecrets.GitHub, err)
	}
}

// Each refusal of the github member names the key and the rule it breaks: a value that is no
// mapping, a key that is no Legion role or names one twice, a list that is empty or holds no
// strings, a name the broker could hold no secret under, and a name given twice.
func TestTheGitHubProxyMapRefusesWhatNoRoleCouldAskFor(t *testing.T) {
	const key = "runtime.kubernetes.agent_secrets.github"
	for _, tc := range []struct{ name, lines, want string }{
		{"not a mapping", "        - LEGION_IMPLEMENT_APP\n", key + " must be a mapping of a role to the secret names its GitHub access goes through the broker's proxy under"},
		{"an unknown role", "        coder: [LEGION_IMPLEMENT_APP]\n", key + ` names "coder", which is not a Legion role`},
		{"a role twice", "        tester: [A_APP]\n        tester: [B_APP]\n", key + " names tester twice"},
		{"an empty list", "        tester: []\n", key + ".tester names no secret: give the role at least one, or leave it out"},
		{"no strings", "        tester: [1]\n", key + ".tester must be an array of non-empty strings"},
		{"a lowercase name", "        tester: [legion_app]\n", key + `.tester entry "legion_app" is no secret name: uppercase letters, digits and single underscores, starting with a letter`},
		{"a dotted name", "        tester: [LEGION.APP]\n", key + `.tester entry "LEGION.APP" is no secret name`},
		{"a name twice", "        tester: [A_APP, A_APP]\n", key + ".tester names A_APP twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadForValidation(writeConfigFile(t, kubernetesFile+githubProxyBlock(tc.lines)), noEnv)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load = %v, want a refusal holding %q", err, tc.want)
			}
		})
	}
}
