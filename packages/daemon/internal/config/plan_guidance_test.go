package config

import (
	"os"
	"path/filepath"
	"strings"
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

// The copy an owner's prompt reads is headed with the deployment's name and Legion's own sentence
// that the guidance binds no plan, then the operator's file verbatim. A configured file with
// nothing in it is a misconfiguration, never an empty guidance, and a file that cannot be read is
// refused naming the operator's path, as the deployment's instructions are.
func TestPlanGuidanceCopyCarriesItsHeaderAndRefusesAnUnusableFile(t *testing.T) {
	t.Run("the copy", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")

		written, err := WritePlanGuidance([]byte("Review on another model family.\n"), stateDir, "demo")
		if err != nil {
			t.Fatalf("WritePlanGuidance: %v", err)
		}
		if want := filepath.Join(stateDir, "plan-guidance.md"); written != want {
			t.Errorf("path = %q, want %q", written, want)
		}
		got, err := os.ReadFile(written)
		if err != nil {
			t.Fatal(err)
		}
		want := "# Plan guidance of this deployment (demo)\n\n" +
			"Legion requires none of this guidance and refuses no plan for a step it lacks.\n\n" +
			"Review on another model family.\n"
		if string(got) != want {
			t.Errorf("contents = %q, want %q", got, want)
		}
	})

	dir := t.TempDir()
	blank := filepath.Join(dir, "blank.md")
	if err := os.WriteFile(blank, []byte("\n  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(dir, "absent.md")
	for _, tc := range []struct {
		name, path, want string
	}{
		{name: "blank", path: blank, want: "plan guidance file " + blank + " is empty"},
		{name: "absent", path: absent, want: "plan guidance file " + absent + " could not be read: "},
		{name: "a directory", path: dir, want: "plan guidance file " + dir + " could not be read: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadPlanGuidance(tc.path); err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to start %q", err, tc.want)
			}
		})
	}
}
