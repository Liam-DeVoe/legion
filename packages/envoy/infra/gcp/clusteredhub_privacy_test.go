package gcp_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/core"
	"github.com/sjawhar/envoy/infra/gcp"
)

// The inputs a private hub's session listener reads each decision's privacy with: the secrets
// broker's base URL and its privacy route there, the broker's audience (BROKER_K8S_OIDC_AUDIENCE),
// which the projected token of the listener's own account carries, the listener's own origin,
// and Dispatch's account, the one caller served every project's sessions.
const (
	privacyBrokerURL       = "https://example-broker.example-dispatch.svc.cluster.local:8443"
	privacySourceURL       = privacyBrokerURL + "/v1/project-privacy"
	privacyBrokerAudience  = "example-broker-audience"
	privacyPublicURL       = "http://envoy-nats-listener.envoy.svc.cluster.local:9020"
	privacyDispatchAccount = "system:serviceaccount:example-dispatch:example-dispatch"
	// clusteredListenerInstances is the headless Service over the session listener's replicas.
	clusteredListenerInstances = clusteredListenerName + "-instances"
)

// withPrivacySource gives l the inputs a private hub's listener reads privacy with, and lists
// Dispatch's account among the subjects its token check admits, since an all-projects caller is
// first a caller the listener verifies. It returns l.
func withPrivacySource(l *gcp.ClusteredHubListenerArgs) *gcp.ClusteredHubListenerArgs {
	l.AgentSecretsUrl, l.PublicUrl, l.BrokerAudience = privacyBrokerURL, privacyPublicURL, privacyBrokerAudience
	l.PrivacySourceUrl, l.AllProjectsAccounts = privacySourceURL, []string{privacyDispatchAccount}
	l.OidcAccounts = append(l.OidcAccounts, privacyDispatchAccount)
	return l
}

// privateListenerHub is a gcp:ClusteredNatsHub that may make a project private and runs a session
// listener: the auth service's user alone behind the callout, no session user, and the listener
// declared as the hub's own input, so it requires a token of every caller, connecting by its own
// account's projected token, with the privacy inputs.
func privateListenerHub() *gcp.ClusteredNatsHubArgs {
	args := calloutClusteredHubWithTailnet()
	args.Users, args.SessionUser = []gcp.NATSUserArgs{authServiceUser()}, nil
	args.PrivateProjects = true
	withPrivacySource(ownListener(args))
	return args
}

// withoutPrivacySource clears the listener's privacy inputs, which is args as a consumer wrote it
// before those inputs existed; its token check, an input of its own, stays as it is.
func withoutPrivacySource(args *gcp.ClusteredNatsHubArgs) *gcp.ClusteredNatsHubArgs {
	l := args.Listener
	l.AgentSecretsUrl, l.PublicUrl, l.BrokerAudience, l.PrivacySourceUrl, l.AllProjectsAccounts = "", "", "", "", nil
	return args
}

// podJSON is the listener's pod template as the mocks recorded it, for a byte-for-byte comparison.
func podJSON(t *testing.T, args *gcp.ClusteredNatsHubArgs) string {
	t.Helper()
	b, err := json.MarshalIndent(deploymentPodTemplate(t, runClusteredHub(t, args), clusteredListenerName), "", "  ")
	require.NoError(t, err)
	return string(b)
}

// TestTheClusteredSessionListenerFollowsItsHubsPrivacy: a gcp:ClusteredNatsHub's session listener
// renders the broker settings and the privacy settings exactly when its hub may make a project
// private (R9). With privateProjects false, the inputs set or not, its pod spec and env are the
// bytes of the same hub without them; with it true, the env holds the four broker settings in
// their token-file form, the privacy URL, the flag, ENVOY_ALL_PROJECTS_ACCOUNTS and the token
// check, the pod mounts the broker-audience token at /var/run/secrets/envoy-broker/token, and the
// headless Service over the listener's replicas and the policy's rule for Dispatch's namespace
// exist. gcp:NatsHub, whose hub is never private, refuses privateProjects beside its own listener.
//
// Negative controls, run by hand, each on its own: drop one piece from the private render (the
// privacy URL, the broker token, ENVOY_AGENT_SECRETS_URL, ENVOY_PUBLIC_URL, the flag or
// ENVOY_ALL_PROJECTS_ACCOUNTS), render an audience beside the token file, or mount a token of the
// natsAudience at the broker token's path; each is refused or fails here naming the listener and
// the piece. Render the broker settings with privateProjects false (v52's form); the all-public
// row fails naming ENVOY_AGENT_SECRETS_URL.
func TestTheClusteredSessionListenerFollowsItsHubsPrivacy(t *testing.T) {
	t.Run("all-public: the listener is today's, the privacy inputs set or not", func(t *testing.T) {
		public := privateListenerHub()
		public.PrivateProjects = false
		today := withoutPrivacySource(privateListenerHub())
		today.PrivateProjects = false
		require.Equal(t, podJSON(t, today), podJSON(t, public), "the all-public listener's pod spec and env, privacy inputs set")
		env := containerEnv(t, podContainer(t, deploymentPodTemplate(t, runClusteredHub(t, public), clusteredListenerName), "listener"))
		for _, name := range []string{"ENVOY_AGENT_SECRETS_URL", "ENVOY_AGENT_SECRETS_ACCOUNT_TOKEN_FILE", "ENVOY_PUBLIC_URL",
			"ENVOY_PROJECT_PRIVACY_URL", "ENVOY_PROJECT_PRIVACY_REQUIRED", "ENVOY_ALL_PROJECTS_ACCOUNTS"} {
			require.NotContains(t, env, name, "envoy-nats-listener of an all-public hub renders %s", name)
		}
		m := runClusteredHub(t, public)
		for _, r := range m.Registrations() {
			require.NotEqual(t, clusteredListenerInstances, r.Name, "an all-public hub renders no headless Service")
		}
	})

	t.Run("private: broker settings, privacy settings, token check, volume, Service and policy", func(t *testing.T) {
		m := runClusteredHub(t, privateListenerHub())
		requirePodGolden(t, m, clusteredListenerName, "clustered-hub-listener-pod-private.golden")
		template := deploymentPodTemplate(t, m, clusteredListenerName)
		listener := podContainer(t, template, "listener")
		env := containerEnv(t, listener)
		for name, want := range map[string]any{
			"ENVOY_AGENT_SECRETS_URL":                privacyBrokerURL,
			"ENVOY_AGENT_SECRETS_ACCOUNT_TOKEN_FILE": "/var/run/secrets/envoy-broker/token",
			"ENVOY_PUBLIC_URL":                       privacyPublicURL,
			"ENVOY_PROJECT_PRIVACY_URL":              privacySourceURL,
			"ENVOY_PROJECT_PRIVACY_REQUIRED":         "true",
			"ENVOY_ALL_PROJECTS_ACCOUNTS":            privacyDispatchAccount,
			"ENVOY_OIDC_ISSUER":                      clusteredListenerIssuer,
			"ENVOY_OIDC_AUDIENCE":                    clusteredListenerAudience,
			"ENVOY_OIDC_ACCOUNTS":                    clusteredListenerAccount + "," + privacyDispatchAccount,
		} {
			require.Equal(t, want, env[name], "envoy-nats-listener of a private hub renders %s", name)
		}
		require.NotContains(t, env, "ENVOY_AGENT_SECRETS_GOOGLE_AUDIENCE", "envoy-nats-listener renders no Google audience beside its token file")
		require.Contains(t, listener["volumeMounts"], map[string]any{"name": "envoy-broker-token", "mountPath": "/var/run/secrets/envoy-broker", "readOnly": true})
		require.Contains(t, template["spec"].(map[string]any)["volumes"], map[string]any{"name": "envoy-broker-token", "projected": map[string]any{
			"sources": []any{map[string]any{"serviceAccountToken": map[string]any{
				"audience": privacyBrokerAudience, "expirationSeconds": 3600.0, "path": "token",
			}}},
		}}, "envoy-nats-listener's broker token is its own account's, for the broker's audience and not natsAudience %q", calloutNatsAudience)

		instances := m.Named(t, serviceType, clusteredListenerInstances)
		requireClusteredKubernetesChild(t, instances)
		require.Equal(t, map[string]any{
			"apiVersion": "v1", "kind": "Service",
			"metadata": map[string]any{"name": clusteredListenerInstances, "namespace": "envoy"},
			"spec": map[string]any{
				"clusterIP": "None", "publishNotReadyAddresses": true,
				"selector": listenerPodLabels,
				"ports":    []any{map[string]any{"port": 9020.0, "targetPort": 9020.0}},
			},
		}, instances.Inputs.Mappable())
		rules := m.Named(t, networkPolicyType, clusteredListenerName).Inputs.Mappable()["spec"].(map[string]any)["ingress"].([]any)
		require.Contains(t, rules, namespaceRule(9020, "example-dispatch"), "the policy admits Dispatch's namespace at the listener's port")
	})

	t.Run("private: each missing or wrong input is refused, naming it", func(t *testing.T) {
		for _, row := range []struct {
			name, input, err string
			edit             func(*gcp.ClusteredNatsHubArgs)
		}{
			{"no broker URL", "listener.agentSecretsUrl", "listener.agentSecretsUrl is required with privateProjects",
				func(a *gcp.ClusteredNatsHubArgs) { a.Listener.AgentSecretsUrl = "" }},
			{"no origin", "listener.publicUrl", "listener.publicUrl is required with privateProjects",
				func(a *gcp.ClusteredNatsHubArgs) { a.Listener.PublicUrl = "" }},
			{"no broker audience", "listener.brokerAudience", "listener.brokerAudience is required with privateProjects",
				func(a *gcp.ClusteredNatsHubArgs) { a.Listener.BrokerAudience = "" }},
			{"no privacy URL", "listener.privacySourceUrl", "listener.privacySourceUrl is required with privateProjects",
				func(a *gcp.ClusteredNatsHubArgs) { a.Listener.PrivacySourceUrl = "" }},
			{"no all-projects caller", "listener.allProjectsAccounts", "listener.allProjectsAccounts is required with privateProjects",
				func(a *gcp.ClusteredNatsHubArgs) { a.Listener.AllProjectsAccounts = nil }},
			{"an envoy-nats token at the broker token's path", "listener.brokerAudience",
				"listener.brokerAudience is listener.natsAudience's audience",
				func(a *gcp.ClusteredNatsHubArgs) { a.Listener.BrokerAudience = calloutNatsAudience }},
			{"an all-projects caller the token check does not admit", "listener.allProjectsAccounts",
				`listener.allProjectsAccounts[0] "` + privacyDispatchAccount + `" is not in listener.oidcAccounts`,
				func(a *gcp.ClusteredNatsHubArgs) { a.Listener.OidcAccounts = a.Listener.OidcAccounts[:1] }},
			{"a listener that requires no token", "privateProjects",
				"privateProjects and tailnet.listener: a listener of a hub that may make a project private requires a token of every caller",
				func(a *gcp.ClusteredNatsHubArgs) {
					l := a.Listener
					l.OidcIssuer, l.OidcAudience, l.OidcAccounts = "", "", nil
					a.Listener, a.Tailnet.Listener = nil, l
				}},
		} {
			t.Run(row.name, func(t *testing.T) {
				args := privateListenerHub()
				row.edit(args)
				_, err := gcp.ResolveClusteredNatsHub(args)
				require.ErrorContains(t, err, row.err)
				var input *core.InputError
				require.ErrorAs(t, err, &input)
				require.Equal(t, row.input, input.Input)
				m, err := runClustered(t, "example-clustered-hub", args, nil)
				require.ErrorContains(t, err, row.err)
				require.Empty(t, m.Registrations(), "nothing registered")
			})
		}
	})

	t.Run("gcp:NatsHub refuses privateProjects beside its own listener", func(t *testing.T) {
		args := privateNatsHub()
		args.Listener = calloutListener()
		err := gcp.ResolveNatsHub(args)
		require.ErrorContains(t, err, "privateProjects and listener: the VM hub is never private")
		var input *core.InputError
		require.ErrorAs(t, err, &input)
		require.Equal(t, "privateProjects", input.Input)
		m, err := run(t, args, nil, true)
		require.ErrorContains(t, err, "privateProjects and listener")
		require.Equal(t, []string{gcpProviderType}, m.Types(), "nothing registered beyond the test's provider")
	})
}
