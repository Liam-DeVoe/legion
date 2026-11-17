package daemon

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/leakguard"
)

// testLeakGuard is a leak_guard block as the loader settles it: a knives registry and its overlay.
var testLeakGuard = config.LeakGuard{KnivesRegistries: []leakguard.KnivesRegistry{
	{Repo: "sjawhar/dotfiles", Path: "knives/repos.toml", Ref: "main"},
	{Repo: "acme/private-registry", Path: "knives/repos.local.toml", Ref: "main"},
}}

// leak_guard.knives_registries reaches the Sandbox runtime as the value of the guard's variable:
// the rows in file order as one JSON list, which the runtime tells every role it launches, the
// controller's among them; and nothing with none, so no role is told the variable. The tmux
// runtime is handed nothing: a pane's push runs no leak guard, so nothing in a pane would read it.
func TestKnivesRegistriesReachTheSandboxRuntime(t *testing.T) {
	const rows = `[{"repo":"sjawhar/dotfiles","path":"knives/repos.toml","ref":"main"},` +
		`{"repo":"acme/private-registry","path":"knives/repos.local.toml","ref":"main"}]`
	for _, tc := range []struct {
		name  string
		guard config.LeakGuard
		want  string
	}{
		{"a registry and its overlay", testLeakGuard, rows},
		{"no registry", config.LeakGuard{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := kubernetesConfig(t, "https://127.0.0.1:1")
			k.LeakGuard = tc.guard
			opts, err := sandboxOptions(k, *k.Runtime.Kubernetes, "test", "", lookup(nil), quietLogger())
			if err != nil {
				t.Fatalf("sandboxOptions: %v", err)
			}
			if opts.KnivesRegistries != tc.want {
				t.Errorf("sandbox KnivesRegistries = %q, want %q", opts.KnivesRegistries, tc.want)
			}
		})
	}
}
