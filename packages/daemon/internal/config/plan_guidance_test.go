package config

import (
	"path/filepath"
	"testing"
)

// `plan_guidance` is a path of the deployment's, resolved against the directory of the file that
// names it as `instructions` is, and never against the daemon's working directory. Load reads no
// file at the path: boot reads it, so the path loads with nothing on disk.
func TestPlanGuidanceIsAPathKeyReadAtBoot(t *testing.T) {
	t.Run("a relative path resolves against the file's directory", func(t *testing.T) {
		path := writeConfigFile(t, minimalFile+"plan_guidance: rules/plan-guidance.md\n")

		cfg, err := Load(path, noEnv)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if want := filepath.Join(filepath.Dir(path), "rules/plan-guidance.md"); cfg.PlanGuidancePath != want {
			t.Errorf("PlanGuidancePath = %q, want %q", cfg.PlanGuidancePath, want)
		}
	})

	t.Run("a file that names none loads no guidance", func(t *testing.T) {
		cfg, err := Load(writeConfigFile(t, minimalFile), noEnv)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.PlanGuidancePath != "" {
			t.Errorf("PlanGuidancePath = %q, want none", cfg.PlanGuidancePath)
		}
	})

	t.Run("an empty value is refused", func(t *testing.T) {
		_, err := Load(writeConfigFile(t, minimalFile+"plan_guidance: \"\"\n"), noEnv)
		if want := "plan_guidance must not be empty"; err == nil || err.Error() != want {
			t.Fatalf("Load error = %v, want %q", err, want)
		}
	})
}
