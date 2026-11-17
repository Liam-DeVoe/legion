package leakguard_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/leakguard"
)

// legionYAML is the smallest legion.yaml config.Load takes, as config's own tests write it
// (config's minimalFile), with no leak_guard block.
const legionYAML = `project: DEMO
state_dir: /var/lib/legion
postgres_dsn: postgres://legion@127.0.0.1:5432/legion
dispatch_url: http://127.0.0.1:8080
dispatch_token_file: /var/run/legion/DISPATCH_TOKEN
projects:
  DEMO: {}
gates:
  design: root-issues
github_apps:
  implement:
    app_id: "1"
    private_key: implement-test-key
  review:
    app_id: "2"
    private_key: review-test-key
nats_urls: [nats://127.0.0.1:4222]
`

// The rows config.Load settles from legion.yaml's leak_guard.knives_registries, written as the
// daemon writes them for every role (leakguard.KnivesRegistriesValue), are the rows the guard reads
// back with its own decoder (knivesRows), each in file order: a row the loader takes and the guard
// refuses fails here, rather than at every push a role makes. It is a test of package
// leakguard_test because config imports leakguard, so no test of package leakguard can load a
// legion.yaml.
func TestTheKnivesRegistriesTheLoaderSettlesAreRowsTheGuardReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legion.yaml")
	body := legionYAML + "leak_guard:\n  knives_registries:\n" +
		"    - {repo: invented-org/knives-registry, path: knives/repos.toml, ref: main}\n" +
		"    - repo: invented-org/private-registry\n      path: knives/repos.local.toml\n      ref: v4.0.1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	settled := cfg.LeakGuard.KnivesRegistries
	if len(settled) != 2 {
		t.Fatalf("config.Load settles %v, want the file's two rows: the read below would prove nothing", settled)
	}
	value := leakguard.KnivesRegistriesValue(settled)
	if rows, err := leakguard.KnivesRows(value); err != nil || !slices.Equal(rows, settled) {
		t.Errorf("the guard reads %s as %v, %v; want %v", value, rows, err, settled)
	}
}
