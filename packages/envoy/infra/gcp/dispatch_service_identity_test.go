package gcp_test

import (
	"path"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/core"
	"github.com/sjawhar/envoy/infra/gcp"
	"github.com/sjawhar/envoy/infra/internal/pulumitest"
	"github.com/sjawhar/envoy/internal/testnats"
)

// dispatchNatsAudience is the audience the app's and the listener's projected ServiceAccount token
// is minted for behind the auth service, the broker's BROKER_NATS_K8S_AUDIENCE, and
// dispatchListenerAudience the one the app's token for its own listener is minted for, the
// listener's ENVOY_OIDC_AUDIENCE.
const (
	dispatchNatsAudience     = "example-nats-audience"
	dispatchListenerAudience = "example-listener-audience"
)

// setNatsAudience moves both NATS clients behind the auth service: natsAudience in
// natsSeedSecret's place, since a connection presents one identity.
func setNatsAudience(a *gcp.DispatchArgs) {
	a.NatsSeedSecret = nil
	a.NatsAudience = dispatchNatsAudience
}

// setListenerAudience turns the app's listener calls onto the app's own token.
func setListenerAudience(a *gcp.DispatchArgs) {
	a.ListenerAudience = dispatchListenerAudience
}

// dispatchServiceIdentityArgs is dispatchArgs with both NATS clients behind the auth service and
// the app calling its listener by its own token: no shared credential of any kind.
func dispatchServiceIdentityArgs() *gcp.DispatchArgs {
	args := dispatchArgs()
	setNatsAudience(args)
	setListenerAudience(args)
	return args
}

// With both inputs set, the app's pod carries Dispatch's two projected tokens, for the NATS
// audience and for its listener's, and no seed or bearer; the listener's carries its own NATS
// token and the OIDC pair it verifies the app's token with.
func TestDispatchPodWithServiceIdentityMatchesGolden(t *testing.T) {
	m, err := runDispatch(t, dispatchServiceIdentityArgs(), true)
	require.NoError(t, err)
	requirePodGolden(t, m, "example-dispatch", "dispatch-pod-with-service-identity.golden")
	requirePodGolden(t, m, "example-dispatch-listener", "dispatch-listener-pod-with-service-identity.golden")
}

// projectedAudience is the audience of the projected ServiceAccount token at file in container's
// pod: the volume container mounts at file's directory projects exactly one source, a token
// written to file's base name. A file on no such volume fails the test, naming it.
func projectedAudience(t *testing.T, template, container map[string]any, file string) string {
	t.Helper()
	var volumeName string
	for _, item := range container["volumeMounts"].([]any) {
		mount := item.(map[string]any)
		if mount["mountPath"] == path.Dir(file) {
			require.Equal(t, true, mount["readOnly"], "%s is mounted read-only", file)
			volumeName = mount["name"].(string)
		}
	}
	require.NotEmpty(t, volumeName, "no volume of %s is mounted at %s's directory", container["name"], file)
	for _, item := range template["spec"].(map[string]any)["volumes"].([]any) {
		volume := item.(map[string]any)
		if volume["name"] != volumeName {
			continue
		}
		projected, ok := volume["projected"].(map[string]any)
		require.True(t, ok, "%s lies on volume %s, which projects nothing", file, volumeName)
		sources := projected["sources"].([]any)
		require.Len(t, sources, 1, "volume %s projects one token", volumeName)
		token := sources[0].(map[string]any)["serviceAccountToken"].(map[string]any)
		require.Equal(t, path.Base(file), token["path"], "volume %s writes its token to %s", volumeName, file)
		require.Equal(t, 3600.0, token["expirationSeconds"], "the auth service admits a platform token living at most an hour")
		return token["audience"].(string)
	}
	t.Fatalf("no volume %s in the pod", volumeName)
	return ""
}

// podMountsTheSeed reports whether a pod template has a volume reading the NATS seed's
// SecretProviderClass.
func podMountsTheSeed(template map[string]any) bool {
	for _, item := range template["spec"].(map[string]any)["volumes"].([]any) {
		csi, ok := item.(map[string]any)["csi"].(map[string]any)
		if ok && csi["volumeAttributes"].(map[string]any)["secretProviderClass"] == "example-dispatch-nats-seed" {
			return true
		}
	}
	return false
}

// registered reports whether the component registered a resource of type typ named name.
func registered(m *pulumitest.Mocks, typ, name string) bool {
	for _, r := range m.Registrations() {
		if r.Type == typ && r.Name == name {
			return true
		}
	}
	return false
}

// natsAudience moves the app's and the listener's NATS connections behind the auth service: each
// presents a projected token of the pods' ServiceAccount for natsAudience, read from the file
// NATS_SERVICE_TOKEN_FILE names, and nothing of the seed is rendered, mounted, classed or granted.
// listenerAudience moves the app's calls to its listener onto its own token: the app presents a
// projected token for listenerAudience from the file ENVOY_ACCOUNT_TOKEN_FILE names, the
// listener verifies it with ENVOY_OIDC_ISSUER, this cluster's own issuer, and
// ENVOY_OIDC_AUDIENCE, and no shared bearer exists: no ENVOY_TOKEN, no ENVOY_API_TOKEN, no
// Secret and no generated password for it. Each input moves its own half alone, and the other
// half renders as it does today.
func TestDispatchRunsByItsServiceIdentity(t *testing.T) {
	listenerBearer := map[string]any{"name": "example-dispatch-listener", "key": "ENVOY_API_TOKEN"}
	for _, row := range []struct {
		name           string
		edit           func(*gcp.DispatchArgs)
		nats, listener bool
	}{
		{"natsAudience alone", setNatsAudience, true, false},
		{"listenerAudience alone", setListenerAudience, false, true},
		{"both", func(a *gcp.DispatchArgs) { setNatsAudience(a); setListenerAudience(a) }, true, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			args := dispatchArgs()
			row.edit(args)
			m, err := runDispatch(t, args, true)
			require.NoError(t, err)
			appTemplate := deploymentPodTemplate(t, m, "example-dispatch")
			appContainer := podContainer(t, appTemplate, "envoy-dispatch")
			app := containerEnv(t, appContainer)
			listenerTemplate := deploymentPodTemplate(t, m, "example-dispatch-listener")
			listenerContainer := podContainer(t, listenerTemplate, "envoy-listener")
			listener := containerEnv(t, listenerContainer)

			for _, pod := range []struct {
				name                string
				template, container map[string]any
				env                 map[string]any
			}{
				{"envoy-dispatch", appTemplate, appContainer, app},
				{"envoy-listener", listenerTemplate, listenerContainer, listener},
			} {
				if !row.nats {
					require.Equal(t, "/var/run/secrets/envoy/nats-seed", pod.env["NATS_NKEY_SEED_FILE"], "%s keeps its seed", pod.name)
					require.NotContains(t, pod.env, "NATS_SERVICE_TOKEN_FILE", pod.name)
					require.True(t, podMountsTheSeed(pod.template), "%s mounts the seed", pod.name)
					continue
				}
				require.NotContains(t, pod.env, "NATS_NKEY_SEED_FILE", "%s connects as one identity, and its bus refuses a seed beside the token", pod.name)
				require.False(t, podMountsTheSeed(pod.template), "%s mounts no seed", pod.name)
				file, ok := pod.env["NATS_SERVICE_TOKEN_FILE"].(string)
				require.True(t, ok, "%s presents a platform token to the auth service", pod.name)
				require.Equal(t, dispatchNatsAudience, projectedAudience(t, pod.template, pod.container, file), "%s's NATS token is for natsAudience", pod.name)
			}
			require.Equal(t, !row.nats, registered(m, "kubernetes:secrets-store.csi.x-k8s.io/v1:SecretProviderClass", "example-dispatch-nats-seed"), "the seed's SecretProviderClass")
			require.Equal(t, !row.nats, registered(m, "gcp:secretmanager/secretIamMember:SecretIamMember", "example-dispatch-nats-seed-accessor"), "the seed's grant")

			if !row.listener {
				require.Equal(t, listenerBearer, app["ENVOY_TOKEN"])
				require.Equal(t, listenerBearer, listener["ENVOY_API_TOKEN"])
				require.NotContains(t, app, "ENVOY_ACCOUNT_TOKEN_FILE")
				for _, absent := range []string{"ENVOY_OIDC_ISSUER", "ENVOY_OIDC_AUDIENCE"} {
					require.NotContains(t, listener, absent)
				}
				require.True(t, registered(m, "kubernetes:core/v1:Secret", "example-dispatch-listener"))
				require.True(t, registered(m, "random:index/randomPassword:RandomPassword", "example-dispatch-listener-token"))
				return
			}
			require.NotContains(t, app, "ENVOY_TOKEN", "Dispatch refuses ENVOY_TOKEN beside ENVOY_ACCOUNT_TOKEN_FILE")
			file, ok := app["ENVOY_ACCOUNT_TOKEN_FILE"].(string)
			require.True(t, ok, "the app presents its own token to its listener")
			require.Equal(t, dispatchListenerAudience, projectedAudience(t, appTemplate, appContainer, file), "the app's listener token is for listenerAudience")
			require.NotContains(t, listener, "ENVOY_API_TOKEN", "no caller holds a shared bearer")
			require.Equal(t, azureIssuer, listener["ENVOY_OIDC_ISSUER"], "the listener verifies tokens this cluster minted")
			require.Equal(t, dispatchListenerAudience, listener["ENVOY_OIDC_AUDIENCE"])
			require.False(t, registered(m, "kubernetes:core/v1:Secret", "example-dispatch-listener"), "no Secret holds a shared bearer")
			require.False(t, registered(m, "random:index/randomPassword:RandomPassword", "example-dispatch-listener-token"), "no shared bearer is generated")
		})
	}
}

// natsAudience is the pods' one NATS identity, so natsSeedSecret beside it is refused, and
// without it natsSeedSecret stays required. Each audience is held to what a projected token's
// audience and an environment variable can carry: no seed, no whitespace, no control character.
// Each refusal names its input, and nothing registers.
func TestDispatchServiceIdentityRefusals(t *testing.T) {
	for _, row := range []struct {
		name, input, err string
		edit             func(*gcp.DispatchArgs)
	}{
		{"a seed secret beside natsAudience", "natsSeedSecret", "natsSeedSecret is set beside natsAudience", func(a *gcp.DispatchArgs) {
			a.NatsAudience = dispatchNatsAudience
		}},
		{"neither", "natsSeedSecret", "natsSeedSecret is required unless natsAudience is set", func(a *gcp.DispatchArgs) {
			a.NatsSeedSecret = nil
		}},
		{"whitespace in natsAudience", "natsAudience", "natsAudience holds whitespace or a control character", func(a *gcp.DispatchArgs) {
			setNatsAudience(a)
			a.NatsAudience = "example nats audience"
		}},
		{"a control character in natsAudience", "natsAudience", "natsAudience holds whitespace or a control character", func(a *gcp.DispatchArgs) {
			setNatsAudience(a)
			a.NatsAudience = "example\x00nats"
		}},
		{"whitespace in listenerAudience", "listenerAudience", "listenerAudience holds whitespace or a control character", func(a *gcp.DispatchArgs) {
			a.ListenerAudience = "example listener audience"
		}},
		{"a control character in listenerAudience", "listenerAudience", "listenerAudience holds whitespace or a control character", func(a *gcp.DispatchArgs) {
			a.ListenerAudience = "example\x7flistener"
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			args := dispatchArgs()
			row.edit(args)
			_, err := gcp.ResolveDispatch(args)
			var input *core.InputError
			require.ErrorAs(t, err, &input)
			require.Equal(t, row.input, input.Input, "%v", err)
			require.ErrorContains(t, err, row.err)
			m, err := runDispatch(t, args, true)
			require.Error(t, err)
			require.ElementsMatch(t, []string{gcpProviderType, randomProviderType, tailscaleProviderType}, m.Types(),
				"nothing registered beyond the test's providers")
		})
	}
	// natsUrl stays required with natsAudience: the audience says how the pods connect, not where.
	args := dispatchServiceIdentityArgs()
	args.NatsUrl = nil
	_, err := gcp.ResolveDispatch(args)
	require.ErrorContains(t, err, "natsUrl is required")
}

// A seed in either audience is refused before any other refusal, as a seed in every plain input
// is (dispatchSeededInput): here ahead of the missing natsUrl, which the resolver refuses before
// it reads either audience, so the refusal names the seed's input and never the URL.
func TestDispatchRefusesASeedInAnAudienceFirst(t *testing.T) {
	seed, _ := testnats.User(t)
	for input, edit := range map[string]func(*gcp.DispatchArgs){
		"natsAudience":     func(a *gcp.DispatchArgs) { setNatsAudience(a); a.NatsAudience = seed },
		"listenerAudience": func(a *gcp.DispatchArgs) { a.ListenerAudience = seed },
	} {
		t.Run(input, func(t *testing.T) {
			args := dispatchArgs()
			edit(args)
			args.NatsUrl = nil
			_, err := gcp.ResolveDispatch(args)
			require.EqualError(t, err, input+" holds an nkey seed (not echoed)")
		})
	}
}
