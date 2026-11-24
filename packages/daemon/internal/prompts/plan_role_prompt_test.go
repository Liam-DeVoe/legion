package prompts

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The prompt of a role an owner's plan defines is composed as a role of the deployment's is
// (ComposeStep): the headless mechanics, the role's own prompt, then Legion's worker part, and the
// role's own text reaches the composed prompt as it was written.
func TestARoleOfAPlansPromptIsComposedAsADeploymentRolesIs(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const own = "# Prober\n\nProbe the change."
	prober := filepath.Join(t.TempDir(), "prober.md")
	if err := os.WriteFile(prober, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := composer.ComposeStep(prober).RolePromptPaths
	want := []string{filepath.Join(composer.sharedDir, "mechanics", "headless.md"), prober, filepath.Join(composer.goDir, "worker-common.md")}
	if !slices.Equal(paths, want) {
		t.Fatalf("ComposeStep(prober.md) = %q, want %q", paths, want)
	}
	var text strings.Builder
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text.Write(body)
		text.WriteString("\n")
	}
	if composed := text.String(); !strings.Contains(composed, own) {
		t.Fatalf("the prober's composed prompt lacks its own text %q:\n%s", own, composed)
	}
}
