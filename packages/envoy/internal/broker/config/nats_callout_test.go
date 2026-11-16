package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nkeys"
)

// natsSeeds writes a user, an account and a curve seed to files of the test's own and returns
// their paths, and the seeds themselves, which no refusal may print.
func natsSeeds(t *testing.T) (user, account, curve string, seeds []string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, create func() (nkeys.KeyPair, error)) string {
		pair, err := create()
		if err != nil {
			t.Fatal(err)
		}
		seed, err := pair.Seed()
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, string(seed))
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, append(seed, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return write("user.seed", nkeys.CreateUser), write("issuer.seed", nkeys.CreateAccount), write("xkey.seed", nkeys.CreateCurveKeys), seeds
}

// The NATS auth service's settings: none set is no service; any set needs the hub's URLs and the
// three seed files, each holding a seed of its own kind; an audience for a kind of platform token
// needs that kind's issuer; and the shared Envoy bearer is gone, its variables refused naming the
// account token file that replaces it.
func TestNATSCalloutSettings(t *testing.T) {
	user, account, curve, seeds := natsSeeds(t)
	full := func() map[string]string {
		e := validEnv()
		e["BROKER_NATS_URLS"] = "nats://hub-a:4222, nats://hub-b:4222"
		e["BROKER_NATS_SEED_FILE"] = user
		e["BROKER_NATS_ISSUER_SEED_FILE"] = account
		e["BROKER_NATS_XKEY_SEED_FILE"] = curve
		e["BROKER_NATS_SERVICES"] = "listener/dev-stack/*=system:serviceaccount:example-ns:example-listener"
		return e
	}

	t.Run("none set is no service", func(t *testing.T) {
		cfg, err := Load(env(validEnv()))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.NATS.Enabled() || len(cfg.NATS.URLs) != 0 || cfg.NATS.SeedFile != "" {
			t.Fatalf("an environment naming no NATS setting configured %+v", cfg.NATS)
		}
	})
	t.Run("every setting", func(t *testing.T) {
		e := full()
		e["BROKER_K8S_OIDC_ISSUER"], e["BROKER_K8S_OIDC_AUDIENCE"] = "https://issuer.example", "broker"
		e["BROKER_NATS_K8S_AUDIENCE"] = "envoy-nats"
		e["BROKER_NATS_GOOGLE_AUDIENCE"] = "envoy-nats-google"
		cfg, err := Load(env(e))
		if err != nil {
			t.Fatal(err)
		}
		want := NATSCallout{
			URLs: []string{"nats://hub-a:4222", "nats://hub-b:4222"}, SeedFile: user, IssuerSeedFile: account, XKeySeedFile: curve,
			Services: e["BROKER_NATS_SERVICES"], K8sAudience: "envoy-nats", GoogleAudience: "envoy-nats-google",
		}
		if !cfg.NATS.Enabled() || strings.Join(cfg.NATS.URLs, " ") != strings.Join(want.URLs, " ") ||
			cfg.NATS.SeedFile != want.SeedFile || cfg.NATS.IssuerSeedFile != want.IssuerSeedFile || cfg.NATS.XKeySeedFile != want.XKeySeedFile ||
			cfg.NATS.Services != want.Services || cfg.NATS.K8sAudience != want.K8sAudience || cfg.NATS.GoogleAudience != want.GoogleAudience {
			t.Fatalf("read %+v, want %+v", cfg.NATS, want)
		}
	})

	refusals := []struct {
		name  string
		edit  func(map[string]string)
		names string
	}{
		{"no URLs", func(e map[string]string) { delete(e, "BROKER_NATS_URLS") }, "BROKER_NATS_URLS"},
		{"no seed file", func(e map[string]string) { delete(e, "BROKER_NATS_SEED_FILE") }, "BROKER_NATS_SEED_FILE"},
		{"no issuer seed file", func(e map[string]string) { delete(e, "BROKER_NATS_ISSUER_SEED_FILE") }, "BROKER_NATS_ISSUER_SEED_FILE"},
		{"no xkey seed file", func(e map[string]string) { delete(e, "BROKER_NATS_XKEY_SEED_FILE") }, "BROKER_NATS_XKEY_SEED_FILE"},
		{"only the services", func(e map[string]string) {
			for _, name := range []string{"BROKER_NATS_URLS", "BROKER_NATS_SEED_FILE", "BROKER_NATS_ISSUER_SEED_FILE", "BROKER_NATS_XKEY_SEED_FILE"} {
				delete(e, name)
			}
		}, "BROKER_NATS_URLS"},
		{"a user seed in the issuer file", func(e map[string]string) { e["BROKER_NATS_ISSUER_SEED_FILE"] = user }, "BROKER_NATS_ISSUER_SEED_FILE"},
		{"a user seed in the xkey file", func(e map[string]string) { e["BROKER_NATS_XKEY_SEED_FILE"] = user }, "BROKER_NATS_XKEY_SEED_FILE"},
		{"an account seed in the seed file", func(e map[string]string) { e["BROKER_NATS_SEED_FILE"] = account }, "BROKER_NATS_SEED_FILE"},
		{"a seed file that is not there", func(e map[string]string) { e["BROKER_NATS_SEED_FILE"] = user + ".missing" }, "BROKER_NATS_SEED_FILE"},
		{"a Kubernetes audience with no Kubernetes issuer", func(e map[string]string) { e["BROKER_NATS_K8S_AUDIENCE"] = "envoy-nats" }, "BROKER_K8S_OIDC_ISSUER"},
		{"the shared Envoy bearer", func(e map[string]string) { e["BROKER_ENVOY_TOKEN"] = "shared" }, "BROKER_ENVOY_ACCOUNT_TOKEN_FILE"},
		{"the shared Envoy bearer's file", func(e map[string]string) { e["BROKER_ENVOY_TOKEN_FILE"] = "/run/token" }, "BROKER_ENVOY_ACCOUNT_TOKEN_FILE"},
		{"an Envoy URL with no account token", func(e map[string]string) { e["BROKER_ENVOY_URL"] = "https://listener.example" }, "BROKER_ENVOY_ACCOUNT_TOKEN_FILE"},
	}
	for _, row := range refusals {
		t.Run(row.name, func(t *testing.T) {
			e := full()
			row.edit(e)
			_, err := Load(env(e))
			if err == nil || !strings.Contains(err.Error(), row.names) {
				t.Fatalf("want a refusal naming %s, got %v", row.names, err)
			}
			for _, seed := range seeds {
				if strings.Contains(err.Error(), seed) {
					t.Fatalf("the refusal prints a seed: %v", err)
				}
			}
		})
	}
	t.Run("an Envoy URL and its account token file", func(t *testing.T) {
		e := validEnv()
		e["BROKER_ENVOY_URL"] = "https://listener.example"
		e["BROKER_ENVOY_ACCOUNT_TOKEN_FILE"] = "/var/run/secrets/tokens/envoy"
		cfg, err := Load(env(e))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.EnvoyAccountTokenFile != "/var/run/secrets/tokens/envoy" {
			t.Fatalf("read %q", cfg.EnvoyAccountTokenFile)
		}
	})
}
