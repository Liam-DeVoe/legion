// Unit tests of the Stage 4a agent-secrets checks' pure logic that live_secrets_pure_test.go
// defines: deploymentBrokerAudience and shellJoin's shell quoting. The checks it backs
// (checkSecretsTwoRolesEnrolled and the rest) need a cluster and a broker, which the kind job's
// rig gives them.
package sandbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/config"
)

// configMapList is `kubectl get configmaps -o json` for ConfigMaps of these names and data.
func configMapList(t *testing.T, data map[string]map[string]string) []byte {
	t.Helper()
	type item struct {
		Metadata map[string]string `json:"metadata"`
		Data     map[string]string `json:"data"`
	}
	list := struct {
		Items []item `json:"items"`
	}{Items: []item{}}
	for name, values := range data {
		list.Items = append(list.Items, item{Metadata: map[string]string{"name": name}, Data: values})
	}
	out, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Stage 4a enrolls its pods for the broker audience the namespace's daemons use: the one their
// legion.yaml names, the daemon's default where it names none, and no audience at all where two
// daemons use different ones. Each row that reads one audience has a neighbour that reads another
// from the same ConfigMaps changed in one line.
func TestStage4aEnrollsForTheDeploymentsBrokerAudience(t *testing.T) {
	const enrolled = "runtime:\n  kubernetes:\n    agent_secrets:\n      url: https://secrets.internal.example\n"
	named := enrolled + "      audience: agent-secrets\n"
	unenrolled := "runtime:\n  kubernetes:\n    namespace: legion\n"
	ca := map[string]string{"ca.crt": "not a daemon's configuration"}
	for name, tc := range map[string]struct {
		configMaps map[string]map[string]string
		want       string
		refused    []string
	}{
		"a daemon that names agent-secrets": {
			configMaps: map[string]map[string]string{"legion-config": {"legion.yaml": named}, "kube-root-ca.crt": ca},
			want:       "agent-secrets",
		},
		"the same daemon naming no audience takes the default": {
			configMaps: map[string]map[string]string{"legion-config": {"legion.yaml": enrolled}, "kube-root-ca.crt": ca},
			want:       config.DefaultAgentSecretsAudience,
		},
		"no daemon enrolls its pods": {
			configMaps: map[string]map[string]string{"legion-config": {"legion.yaml": unenrolled}, "kube-root-ca.crt": ca},
			want:       config.DefaultAgentSecretsAudience,
		},
		"two daemons that agree": {
			configMaps: map[string]map[string]string{"legion-config": {"legion.yaml": named}, "legion-acme-config": {"legion.yaml": named}},
			want:       "agent-secrets",
		},
		"two daemons that disagree": {
			configMaps: map[string]map[string]string{"legion-config": {"legion.yaml": named}, "legion-acme-config": {"legion.yaml": enrolled}},
			refused: []string{"different broker audiences", "agent-secrets (legion-config)",
				config.DefaultAgentSecretsAudience + " (legion-acme-config)"},
		},
		"a legion.yaml that does not parse": {
			configMaps: map[string]map[string]string{"legion-config": {"legion.yaml": "runtime: [\n"}},
			refused:    []string{"ConfigMap legion-config: legion.yaml"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := deploymentBrokerAudience(configMapList(t, tc.configMaps))
			if tc.refused == nil {
				if err != nil || got != tc.want {
					t.Fatalf("deploymentBrokerAudience = %q, %v; want %q", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("deploymentBrokerAudience = %q; want a refusal naming %q", got, tc.refused)
			}
			for _, part := range tc.refused {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("refusal %q does not name %q", err, part)
				}
			}
		})
	}
}

func TestShellJoin(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no args":              {nil, ""},
		"plain args":           {[]string{"self", "--json"}, "'self' '--json'"},
		"single quote in arg":  {[]string{"it's"}, `'it'\''s'`},
		"arg with a space":     {[]string{"s4a cross-issue probe"}, "'s4a cross-issue probe'"},
		"empty string arg":     {[]string{""}, "''"},
		"shell metacharacters": {[]string{"$(rm -rf /)", "a;b"}, `'$(rm -rf /)' 'a;b'`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := shellJoin(tc.args); got != tc.want {
				t.Fatalf("shellJoin(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
