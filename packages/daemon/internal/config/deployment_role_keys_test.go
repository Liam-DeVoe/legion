package config

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// checkerRole is a role of the deployment, `checker`, with its prompt file beside the
// configuration (rolesFile writes it).
const checkerRole = "roles: {checker: {type: testing, prompt_file: checker.md, model: checker-model}}\n"

// worker_backends takes a role the deployment defines as a key, and Claude Code runs it; a file
// that defines no such role is refused with the text of any other name that is no issue role.
func TestWorkerBackendsTakesARoleOfTheDeployment(t *testing.T) {
	cfg, err := Load(rolesFile(t, checkerRole+"worker_backends: {checker: claude}\n", "checker.md"), noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.BackendFor("checker"); got != runtime.BackendClaude {
		t.Errorf("BackendFor(checker) = %q, want claude", got)
	}

	_, err = Load(rolesFile(t, "worker_backends: {checker: claude}\n"), noEnv)
	want := `worker_backends names "checker", which is not an issue role (architect, planner, implementer, tester, reviewer, merger)`
	if err == nil || err.Error() != want {
		t.Fatalf("Load without roles = %v, want %q", err, want)
	}
}

// The check across keys runs once every key it judges is set, so a name no role holds is refused
// through the daemon's load and through the check's load alike, with a list that names the roles
// of the deployment beside Legion's; a name outside the role grammar is refused when the key is
// read, before any other key is.
func TestTheCheckAcrossKeysSeesEveryKeyItJudges(t *testing.T) {
	want := `worker_backends names "ghost", which is not an issue role (architect, planner, implementer, tester, reviewer, merger, checker)`
	path := rolesFile(t, checkerRole+"worker_backends: {ghost: omp}\n", "checker.md")
	for name, load := range map[string]func(string, func(string) string) (Config, error){"Load": Load, "LoadForValidation": LoadForValidation} {
		if _, err := load(path, noEnv); err == nil || err.Error() != want {
			t.Errorf("%s = %v, want %q", name, err, want)
		}
	}

	_, err := Load(rolesFile(t, "worker_backends: {Checker: omp}\n"), noEnv)
	want = `worker_backends names "Checker", which is not an issue role (architect, planner, implementer, tester, reviewer, merger)`
	if err == nil || err.Error() != want {
		t.Fatalf("Load of a name outside the grammar = %v, want %q", err, want)
	}
}
