package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/config"
)

// renderedListenerEnv is the environment of the container named listener in the pod spec golden
// name, which envoy-infra renders under packages/envoy/infra/gcp/testdata: each variable with a
// plain value, as the container receives it. A variable drawn from a Secret or a field has no
// value there and is left out; a golden holding no such container fails the test.
func renderedListenerEnv(t *testing.T, name string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "infra", "gcp", "testdata", name))
	if err != nil {
		t.Fatalf("read the rendered pod %s: %v", name, err)
	}
	var pod struct {
		Spec struct {
			Containers []struct {
				Name string `json:"name"`
				Env  []struct {
					Name  string  `json:"name"`
					Value *string `json:"value"`
				} `json:"env"`
			} `json:"containers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &pod); err != nil {
		t.Fatalf("decode the rendered pod %s: %v", name, err)
	}
	for _, container := range pod.Spec.Containers {
		if container.Name != "listener" {
			continue
		}
		env := make(map[string]string, len(container.Env))
		for _, variable := range container.Env {
			if variable.Value != nil {
				env[variable.Name] = *variable.Value
			}
		}
		if len(env) == 0 {
			t.Fatalf("the listener container of %s renders no environment", name)
		}
		return env
	}
	t.Fatalf("%s holds no container named listener", name)
	return nil
}

// TestTheRenderedListenerEnvResolves holds the listener envoy-infra renders to the listener's own
// rule for its broker settings: the environment of each golden a listener of a hub that may make
// a project private is rendered as is fed to resolveListenerBrokerConfig, which accepts it, in the
// token-file form, with the origin it renders. A render the listener refused would be a listener
// that does not start (main makes the refusal fatal), so this is the start's own check of what the
// component renders.
//
// Control: the private environment without ENVOY_PUBLIC_URL; the resolver refuses it, and the
// test fails naming the variable.
func TestTheRenderedListenerEnvResolves(t *testing.T) {
	for _, golden := range []string{
		"clustered-hub-listener-pod-private.golden",
	} {
		t.Run(golden, func(t *testing.T) {
			env := renderedListenerEnv(t, golden)
			config, err := resolveListenerBrokerConfig(func(name string) string { return env[name] })
			if err != nil {
				t.Fatalf("the listener refuses the environment %s renders: %v", golden, err)
			}
			if !config.configured() || config.AccountTokenFile == "" || config.GoogleAudience != "" || config.Origin != env[EnvPublicURL] {
				t.Fatalf("%s resolves to %+v, want the broker check in its token-file form with the origin it renders", golden, config)
			}

			delete(env, EnvPublicURL)
			if _, err := resolveListenerBrokerConfig(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), EnvPublicURL) {
				t.Fatalf("%s without %s resolves (err = %v), want a refusal naming %s", golden, EnvPublicURL, err, EnvPublicURL)
			}
		})
	}
}

// loadRenderedEnv is config.Load over env alone, as the listener's start reads it: each of env's
// variables set, and each privacy variable env does not hold set blank, which config reads as
// unset, so nothing of the test process's own environment decides the mode.
func loadRenderedEnv(t *testing.T, env map[string]string) (config.Service, error) {
	t.Helper()
	for _, name := range []string{config.PrivacyRequiredEnvVar, config.PrivacyURLEnvVar} {
		t.Setenv(name, "")
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
	return config.Load(9020)
}

// TestTheRenderedListenerEnvLoads holds each listener envoy-infra renders to the listener's own
// reading of its privacy mode (config.Load): the all-public render loads in the all-public mode,
// with no privacy source, and each render of a listener on a hub that may make a project private
// loads in the required mode, reading its privacy from the broker it renders, ENVOY_AGENT_SECRETS_URL:
// the base URL the listener joins /v1/project-privacy to (newK7Reader), and the source each
// replica reports to Dispatch's proof, which checks it against its inventory's.
//
// Control: the private environment without ENVOY_PROJECT_PRIVACY_REQUIRED; config.Load refuses it
// as a privacy source without the flag, and the test fails naming the variable.
func TestTheRenderedListenerEnvLoads(t *testing.T) {
	for _, row := range []struct {
		golden   string
		required bool
	}{
		{"clustered-hub-listener-pod-service-identity.golden", false},
		{"clustered-hub-listener-pod-private.golden", true},
		{"dispatch-listener-pod-private.golden", true},
	} {
		t.Run(row.golden, func(t *testing.T) {
			env := renderedListenerEnv(t, row.golden)
			loaded, err := loadRenderedEnv(t, env)
			if err != nil {
				t.Fatalf("the listener refuses the environment %s renders: %v", row.golden, err)
			}
			if !row.required {
				if loaded.PrivacyRequired || loaded.PrivacyURL != "" {
					t.Fatalf("%s loads required %t from %q, want the all-public mode with no privacy source", row.golden, loaded.PrivacyRequired, loaded.PrivacyURL)
				}
				return
			}
			if !loaded.PrivacyRequired || loaded.PrivacyURL != env[EnvBrokerURL] {
				t.Fatalf("%s loads required %t from %q, want the required mode reading the broker it renders, %s=%q", row.golden, loaded.PrivacyRequired, loaded.PrivacyURL, EnvBrokerURL, env[EnvBrokerURL])
			}
			delete(env, config.PrivacyRequiredEnvVar)
			if _, err := loadRenderedEnv(t, env); err == nil || !strings.Contains(err.Error(), config.PrivacyRequiredEnvVar) {
				t.Fatalf("%s without %s loads (err = %v), want a refusal naming %s", row.golden, config.PrivacyRequiredEnvVar, err, config.PrivacyRequiredEnvVar)
			}
		})
	}
}
