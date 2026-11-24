// The Stage 4a agent-secrets checks' pure logic: the audience the deployment's daemons enroll
// their pods for, and the shell-quoting `agent-secrets <args>` runs through kubectl exec with. It
// touches no cluster, so it sits apart from live_secrets_test.go, which the kind job runs whole,
// and daemon-go exercises it directly (live_secrets_unit_test.go).
package sandbox

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sjawhar/legion/daemon/internal/config"
)

// deploymentBrokerAudience is the audience Stage 4a's broker verifies the cluster's tokens for and
// the run enrolls its pods for: the audience the deployment's own daemons use, so the run proves
// enrollment against the broker they enroll with. configMaps is `kubectl get configmaps -o json` of
// the run's namespace (startLiveBroker). Each ConfigMap with a legion.yaml key is a daemon's
// configuration, and one whose runtime.kubernetes.agent_secrets block names no audience uses the
// daemon's default (config.DefaultAgentSecretsAudience), as the loader does; with no such block at
// all the run takes that default too. Daemons that use different audiences are refused, each
// named: no one audience is the deployment's.
func deploymentBrokerAudience(configMaps []byte) (string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Data map[string]string `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(configMaps, &list); err != nil {
		return "", fmt.Errorf("reading the namespace's ConfigMaps: %v", err)
	}
	daemons := map[string][]string{}
	for _, item := range list.Items {
		text, ok := item.Data["legion.yaml"]
		if !ok {
			continue
		}
		var file struct {
			Runtime struct {
				Kubernetes struct {
					AgentSecrets *struct {
						Audience string `yaml:"audience"`
					} `yaml:"agent_secrets"`
				} `yaml:"kubernetes"`
			} `yaml:"runtime"`
		}
		if err := yaml.Unmarshal([]byte(text), &file); err != nil {
			return "", fmt.Errorf("ConfigMap %s: legion.yaml: %v", item.Metadata.Name, err)
		}
		if block := file.Runtime.Kubernetes.AgentSecrets; block != nil {
			audience := cmp.Or(block.Audience, config.DefaultAgentSecretsAudience)
			daemons[audience] = append(daemons[audience], item.Metadata.Name)
		}
	}
	audiences := slices.Sorted(maps.Keys(daemons))
	switch len(audiences) {
	case 0:
		return config.DefaultAgentSecretsAudience, nil
	case 1:
		return audiences[0], nil
	}
	each := make([]string, len(audiences))
	for i, audience := range audiences {
		each[i] = audience + " (" + strings.Join(daemons[audience], ", ") + ")"
	}
	return "", fmt.Errorf("the namespace's daemons enroll their pods for different broker audiences: %s", strings.Join(each, "; "))
}

// shellJoin quotes args for a POSIX shell -c string, the way (*liveRig).agentSecrets builds the
// command kubectl exec runs in a pod's container.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
