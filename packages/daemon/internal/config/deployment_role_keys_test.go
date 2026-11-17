package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

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

// role_config_files and worker_stall_backstop_hours take a role the deployment defines as a key,
// and each check of the key walks it as it walks Legion's roles: its overlays are refused while
// it runs on Claude Code, and its bound below its own stall ladder. A file that defines no such
// role is refused with the text of any other name that is no Legion role, and a name no role
// holds with a list that names the roles of the deployment beside Legion's.
func TestTheRoleKeyedSettingsTakeARoleOfTheDeployment(t *testing.T) {
	cfg, err := Load(rolesFile(t, checkerRole+"role_config_files: {checker: [/etc/x.yml]}\nworker_stall_backstop_hours: {checker: 2}\n", "checker.md"), noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.RoleConfigFiles["checker"], []string{"/etc/x.yml"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RoleConfigFiles[checker] = %v, want %v", got, want)
	}
	if got := cfg.WorkerStallBackstops["checker"]; got != 2*time.Hour {
		t.Errorf("WorkerStallBackstops[checker] = %s, want 2h", got)
	}

	for _, tc := range []struct{ name, body, want string }{
		{"overlays without roles", "role_config_files: {checker: [/etc/x.yml]}\n",
			`role_config_files names "checker", which is not a Legion role (architect, planner, implementer, tester, reviewer, merger, controller)`},
		{"a backstop without roles", "worker_stall_backstop_hours: {checker: 2}\n",
			`worker_stall_backstop_hours names "checker", which is not a Legion role (architect, planner, implementer, tester, reviewer, merger, controller)`},
		{"overlays of a name no role holds", checkerRole + "role_config_files: {ghost: [/etc/x.yml]}\n",
			`role_config_files names "ghost", which is not a Legion role (architect, planner, implementer, tester, reviewer, merger, controller, checker)`},
		{"a backstop of a name no role holds", checkerRole + "worker_stall_backstop_hours: {ghost: 2}\n",
			`worker_stall_backstop_hours names "ghost", which is not a Legion role (architect, planner, implementer, tester, reviewer, merger, controller, checker)`},
		{"overlays of a name outside the grammar", checkerRole + "role_config_files: {Checker: [/etc/x.yml]}\n",
			`role_config_files names "Checker", which is not a Legion role (architect, planner, implementer, tester, reviewer, merger, controller)`},
		{"overlays of a role on Claude Code", checkerRole + "worker_backends: {checker: claude}\nrole_config_files: {checker: [/etc/x.yml]}\n",
			"role_config_files.checker names Oh My Pi overlays, and worker_backends.checker is claude, which reads none: drop the role from one of the two"},
		{"a backstop below the role's own ladder", checkerRole + "worker_stall_backstop_hours: {checker: 1}\n",
			"worker_stall_backstop_hours.checker (1h0m0s) is below the whole stall ladder the checker's own plugin runs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(rolesFile(t, tc.body, "checker.md"), noEnv)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("Load = %v, want %q", err, tc.want)
			}
		})
	}
}

// A project's `memory` overlays reach every role of a memory-group tree, so a role of the
// deployment on Claude Code, which reads no PI_CONFIG_FILES, refuses them as an issue role of
// Legion's on Claude Code does; on Oh My Pi it takes them.
func TestADeploymentRolesOverlaysAreJudgedAsAnIssueRolesAre(t *testing.T) {
	const block = "{mode: on, config_files: [/etc/x.yml]}"
	_, err := Load(configFileWith(t, memoryFile(block, checkerRole+"worker_backends: {checker: claude}\n"), "checker.md"), noEnv)
	want := "projects.DEMO.memory names config_files, and worker_backends.checker is claude, which reads no PI_CONFIG_FILES: put every issue role on omp, or take the overlays out of the block"
	if err == nil || err.Error() != want {
		t.Fatalf("Load with the role on Claude Code = %v, want %q", err, want)
	}
	if _, err := Load(configFileWith(t, memoryFile(block, checkerRole+"worker_backends: {checker: omp}\n"), "checker.md"), noEnv); err != nil {
		t.Fatalf("Load with the role on Oh My Pi: %v", err)
	}
}
