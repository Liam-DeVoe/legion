package gcp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
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

// listenerAudience is the listener's own. Equal to another audience the app presents a token for
// or verifies one under, a token minted for that one would be a bearer the listener admits too,
// so each such pair is refused, naming both inputs, before anything registers; five distinct
// audiences resolve.
func TestDispatchRefusesAListenerAudienceAnotherAudienceShares(t *testing.T) {
	const brokerAudience = "example-broker-audience"
	setBroker := func(a *gcp.DispatchArgs) {
		a.AgentSecretsUrl, a.AgentSecretsAudience = "https://example-broker.internal.example", brokerAudience
	}
	for _, row := range []struct {
		name, other string
		edit        func(*gcp.DispatchArgs)
	}{
		{"natsAudience", "natsAudience", func(a *gcp.DispatchArgs) { setNatsAudience(a); a.ListenerAudience = dispatchNatsAudience }},
		{"agentSecretsAudience", "agentSecretsAudience", func(a *gcp.DispatchArgs) { setBroker(a); a.ListenerAudience = brokerAudience }},
		{"oidcAudience", "oidcAudience", func(a *gcp.DispatchArgs) {
			a.OidcAudience = dispatchOidcAudience
			a.ListenerAudience = dispatchOidcAudience
		}},
		{"azureFederationAudience", "azureFederationAudience", func(a *gcp.DispatchArgs) {
			setAzureFederation(a)
			a.AzureFederationAudience = pulumi.StringRef("api://example-audience")
			a.ListenerAudience = "api://example-audience"
		}},
		{"the keyless Azure sign-in's default audience", "azureFederationAudience (unset, so api://AzureADTokenExchange)", func(a *gcp.DispatchArgs) {
			setAzureFederation(a)
			a.ListenerAudience = "api://AzureADTokenExchange"
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			args := dispatchArgs()
			row.edit(args)
			_, err := gcp.ResolveDispatch(args)
			var input *core.InputError
			require.ErrorAs(t, err, &input)
			require.Equal(t, "listenerAudience", input.Input, "%v", err)
			require.ErrorContains(t, err, "listenerAudience is also "+row.other+":")
			m, err := runDispatch(t, args, true)
			require.Error(t, err)
			require.ElementsMatch(t, []string{gcpProviderType, randomProviderType, tailscaleProviderType}, m.Types(),
				"nothing registered beyond the test's providers")
		})
	}
	args := dispatchServiceIdentityArgs()
	setBroker(args)
	args.OidcAudience = dispatchOidcAudience
	setAzureFederation(args)
	_, err := gcp.ResolveDispatch(args)
	require.NoError(t, err, "five distinct audiences")
}

// setGatedStart is natsAudience where the app's start may wait for its first NATS connect: the
// app Deployment rolls (two replicas, RollingUpdate) and its NATS is in the cluster, not behind
// the front door's SOCKS5 proxy.
func setGatedStart(a *gcp.DispatchArgs) {
	setNatsAudience(a)
	setInClusterNats(a)
	a.Replicas = pulumi.IntRef(2)
}

// todaysAppProbes are envoy-dispatch's three probes as a render outside the gate carries them:
// startup and liveness ask only that the server answers (/livez), and readiness reads /healthz.
func todaysAppProbes() map[string]any {
	answers := map[string]any{"command": []any{"curl", "-sS", "-o", "/dev/null", "--max-time", "8", "http://127.0.0.1:8766/livez"}}
	return map[string]any{
		"startupProbe":   map[string]any{"exec": answers, "periodSeconds": 10.0, "timeoutSeconds": 9.0, "failureThreshold": 180.0},
		"livenessProbe":  map[string]any{"exec": answers, "periodSeconds": 10.0, "timeoutSeconds": 9.0, "failureThreshold": 3.0},
		"readinessProbe": map[string]any{"exec": map[string]any{"command": []any{"curl", "-fsS", "http://127.0.0.1:8766/healthz"}}, "initialDelaySeconds": 5.0, "periodSeconds": 10.0, "timeoutSeconds": 3.0, "failureThreshold": 3.0},
	}
}

// appProbes are the three probes of envoy-dispatch in the app's pod.
func appProbes(t *testing.T, m *pulumitest.Mocks) map[string]any {
	t.Helper()
	container := podContainer(t, deploymentPodTemplate(t, m, "example-dispatch"), "envoy-dispatch")
	return map[string]any{"startupProbe": container["startupProbe"], "livenessProbe": container["livenessProbe"], "readinessProbe": container["readinessProbe"]}
}

// envoy-dispatch's start waits for its first NATS connect only where that wait leaves Dispatch
// served and waits on nothing but NATS: with natsAudience, an app Deployment that rolls, and NATS
// in the cluster. The startup probe there passes once /healthz answers 200 reporting "nats":true,
// in the 30 minutes the migrations get. A render missing any one of the three keeps today's
// startup probe, and every render keeps today's readiness and liveness.
func TestDispatchAppStartWaitsForNATSOnlyWhereItRolls(t *testing.T) {
	gated := map[string]any{
		"exec":          map[string]any{"command": []any{"/bin/sh", "-c", `curl -fsS --max-time 8 http://127.0.0.1:8766/healthz | grep -qF '"nats":true'`}},
		"periodSeconds": 10.0, "timeoutSeconds": 9.0, "failureThreshold": 180.0,
	}
	for _, row := range []struct {
		name  string
		edit  func(*gcp.DispatchArgs)
		gated bool
	}{
		{"none of the three", func(*gcp.DispatchArgs) {}, false},
		{"natsAudience on a rolling app with NATS in the cluster", setGatedStart, true},
		{"natsAudience at one replica, where the Deployment is Recreate", func(a *gcp.DispatchArgs) { setGatedStart(a); a.Replicas = pulumi.IntRef(1) }, false},
		{"natsAudience through the front door's SOCKS5 proxy", func(a *gcp.DispatchArgs) { setNatsAudience(a); a.Replicas = pulumi.IntRef(2) }, false},
		{"a rolling app with NATS in the cluster and no natsAudience", func(a *gcp.DispatchArgs) { setInClusterNats(a); a.Replicas = pulumi.IntRef(2) }, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			args := dispatchArgs()
			row.edit(args)
			m, err := runDispatch(t, args, true)
			require.NoError(t, err)
			want := todaysAppProbes()
			if row.gated {
				want["startupProbe"] = gated
			}
			require.Equal(t, want, appProbes(t, m))
		})
	}
}

// healthzStandIn is envoy-dispatch's HTTP server as its probes see it, at the address it
// returns: /livez answers 204, as the server's does, and /healthz answers status and body.
func healthzStandIn(t *testing.T, status int, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/livez":
			w.WriteHeader(http.StatusNoContent)
		case "/healthz":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

// probePasses runs an exec probe's command as the kubelet does, its argv as rendered with no
// shell around it, against addr in place of the app's loopback port, and reports whether it
// exited 0. A command that names that port other than once, or that does not run at all, fails
// the test rather than reading as a failed probe.
func probePasses(t *testing.T, probe any, addr string) bool {
	t.Helper()
	command := probe.(map[string]any)["exec"].(map[string]any)["command"].([]any)
	argv := make([]string, len(command))
	pointed := 0
	for i, arg := range command {
		argv[i] = strings.ReplaceAll(arg.(string), "127.0.0.1:8766", addr)
		if argv[i] != arg {
			pointed++
		}
	}
	require.Equal(t, 1, pointed, "the probe names the app's loopback port once: %q", command)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("%q did not run: %v", argv, err)
	}
	t.Logf("%q against %s: %v %s", argv[len(argv)-1], addr, err, strings.TrimSpace(string(output)))
	return err == nil
}

// The gated startup probe, run as the kubelet runs it against a stand-in for the app's /healthz,
// fails while the server reports "nats":false, its NATS credential refused, so that pod never
// goes ready, and while /healthz answers 503; it passes once the server reports "nats":true.
// Today's startup probe, which every render outside the gate keeps, passes the refused
// credential: that pod goes ready while it cannot publish, which is what the gate exists for.
func TestDispatchGatedStartupProbeWaitsForTheFirstNATSConnect(t *testing.T) {
	gatedArgs := dispatchArgs()
	setGatedStart(gatedArgs)
	m, err := runDispatch(t, gatedArgs, true)
	require.NoError(t, err)
	gated := appProbes(t, m)["startupProbe"]
	todayArgs := dispatchArgs()
	setNatsAudience(todayArgs)
	m, err = runDispatch(t, todayArgs, true)
	require.NoError(t, err)
	today := appProbes(t, m)["startupProbe"]

	refused := healthzStandIn(t, http.StatusOK, `{"ok":true,"db":true,"nats":false}`)
	connected := healthzStandIn(t, http.StatusOK, `{"ok":true,"db":true,"nats":true}`)
	databaseDown := healthzStandIn(t, http.StatusServiceUnavailable, `{"ok":false,"db":false,"nats":true}`)
	require.False(t, probePasses(t, gated, refused), "the gated startup probe passed a server whose NATS credential is refused")
	require.False(t, probePasses(t, gated, databaseDown), "the gated startup probe passed a /healthz answering 503")
	require.True(t, probePasses(t, gated, connected), "the gated startup probe failed a server connected to NATS")
	require.True(t, probePasses(t, today, refused), "today's startup probe failed a server that answers")
}
