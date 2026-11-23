// The tests of daemon-pane.go hold its restatements of the daemon to the daemon's own functions:
// deploymentRolesLine to daemon.DeploymentRolesFragment and stepPromptPaths to
// prompts.Composer.ComposeStep. The file belongs to no module either: daemon-pane.ts
// (goInDaemonModule) compiles it with the program as cmd/daemon-pane/main_test.go of a checkout's
// daemon module, and daemon-pane.test.ts runs it in this checkout's module alone, whose daemon has
// each function a restatement copies. No row runs it against another checkout: an older one lacks
// those functions, so the file does not compile there, while the program itself does.
package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/daemon"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/prompts"
)

// TestTheRigsRestatementsAreTheDaemons holds the sentence that tells an architect the roles of
// its deployment, for a role that names a model and one that names none, and the parts of a step
// of a role of the deployment, to the daemon's own.
func TestTheRigsRestatementsAreTheDaemons(t *testing.T) {
	roles := []rigRole{{name: "checker", typ: "testing", model: "checker-model"}, {name: "critic", typ: "review"}}
	defined := []config.Role{
		{Name: "checker", Type: plan.TypeTesting, Model: "checker-model"},
		{Name: "critic", Type: plan.TypeReview},
	}
	if got, want := deploymentRolesLine(roles), daemon.DeploymentRolesFragment(defined); got != want {
		t.Errorf("deploymentRolesLine = %q, want the daemon's %q", got, want)
	}

	stateDir := t.TempDir()
	composer, err := prompts.New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	promptFile := filepath.Join(t.TempDir(), "checker.md")
	if got, want := stepPromptPaths(stateDir, composer, promptFile), composer.ComposeStep(promptFile).RolePromptPaths; !slices.Equal(got, want) {
		t.Errorf("stepPromptPaths = %q, want the daemon's %q", got, want)
	}
}
