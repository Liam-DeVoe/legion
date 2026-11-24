package daemon

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/prompts"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// planRoleLauncher builds the launches of prober, which the plan in force of each issue of
// promptOf defines with the text promptOf gives it, over stateDir. Each launch answers the spawn
// spec of its claim and the path of the copy of the role's prompt that spec names: the role prompt
// path between the headless mechanics and Legion's worker part.
func planRoleLauncher(t *testing.T, stateDir string, promptOf map[string]string) func(c supervise.Claim) (runtime.SpawnSpec, string) {
	t.Helper()
	composer, err := prompts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := specs{stateDir: stateDir, project: "s1", prompts: composer, appOwners: noAppOwners,
		planRole: func(_ context.Context, issue string, role claim.Role) (record.PlanRole, bool, error) {
			prompt, defined := promptOf[issue]
			if !defined || role != "prober" {
				return record.PlanRole{}, false, nil
			}
			return record.PlanRole{Issue: issue, Role: string(role), Type: "testing", Prompt: prompt}, true, nil
		}}
	return func(c supervise.Claim) (runtime.SpawnSpec, string) {
		t.Helper()
		spec, err := s.SpawnSpec(context.Background(), c)
		if err != nil {
			t.Fatalf("the spawn spec of %s: %v", c.Token, err)
		}
		if len(spec.Prompt.RolePromptPaths) != 3 {
			t.Fatalf("the role prompt paths of %s = %q, want the headless mechanics, the copy of the plan's prompt and Legion's worker part", c.Token, spec.Prompt.RolePromptPaths)
		}
		return spec, spec.Prompt.RolePromptPaths[1]
	}
}

// proberOn is the claim of prober, a role of the plan of issue.
func proberOn(t *testing.T, issue string) supervise.Claim {
	t.Helper()
	token, err := claim.NewToken("s1", issue, "prober")
	if err != nil {
		t.Fatal(err)
	}
	return supervise.Claim{Token: token, Project: "s1", Issue: issue, Tree: "LEGION-42", Role: "prober"}
}

// composedPrompt is the one text spec's agent starts with (runtime.ComposePrompt).
func composedPrompt(t *testing.T, spec runtime.SpawnSpec) string {
	t.Helper()
	composed, err := runtime.ComposePrompt(spec.Prompt)
	if err != nil {
		t.Fatal(err)
	}
	return composed
}

// copyHolds says whether the copy at path is there and holds what the launch composed from the
// stated prompt: the prompt between the lines that name it as the owner's.
func copyHolds(path, prompt string) bool {
	held, err := os.ReadFile(path)
	return err == nil && string(held) == ownersPromptText(prompt)
}

// gone says whether nothing is at path.
func gone(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// The copy of a plan role's prompt is the claim's own file, and it lives as long as the claim's
// process: a claim written with its process keeps it, and a claim written with no process
// (suspended, failed, retired, or between one launch and the next) loses it with its other files
// (pruning). The next launch of that claim composes its prompt from the role's text in the stored
// plan, so it is told the same text, from a copy it wrote itself.
func TestTheCopyOfAPlanRolesPromptGoesWhenItsClaimRunsNoProcess(t *testing.T) {
	t.Parallel()
	const prompt = "# Prober\n\nProbe the change."
	stateDir := t.TempDir()
	launch := planRoleLauncher(t, stateDir, map[string]string{"LEGION-43": prompt})
	c := proberOn(t, "LEGION-43")
	spec, copied := launch(c)
	told := composedPrompt(t, spec)
	if !copyHolds(copied, prompt) {
		t.Fatalf("the copy %s of the prober's prompt does not hold the plan's prompt", copied)
	}
	store := pruningStore{dir: runtime.SecretsDir(stateDir), log: quietLogger()}

	running := c
	running.Locator = pluginTestLocator(c.Token)
	store.written(running)
	if !copyHolds(copied, prompt) {
		t.Fatalf("the claim written with its process lost the copy %s of its role's prompt", copied)
	}
	store.written(c)
	if !gone(copied) {
		t.Fatalf("the claim written with no process kept the copy %s of its role's prompt, want it gone with the claim's other files", copied)
	}

	again, copiedAgain := launch(c)
	if relaunched := composedPrompt(t, again); relaunched != told {
		t.Fatalf("the relaunch after the copy went was told\n%s\nwant the text its launch before was told\n%s", relaunched, told)
	}
	if !copyHolds(copiedAgain, prompt) {
		t.Fatalf("the relaunch's copy %s does not hold the plan's prompt", copiedAgain)
	}
}

// The boot's prune (pruneAllBut) keeps the copy of the role's prompt of a claim that records a
// process and removes the copy of a claim that records none, as it does each claim's other files.
func TestTheBootKeepsTheCopyOfALiveClaimAndRemovesTheCopyOfAClaimWithNoProcess(t *testing.T) {
	t.Parallel()
	const livePrompt, endedPrompt = "# Prober\n\nProbe the live change.", "# Prober\n\nProbe the ended change."
	stateDir := t.TempDir()
	launch := planRoleLauncher(t, stateDir, map[string]string{"LEGION-43": livePrompt, "LEGION-44": endedPrompt})
	live, ended := proberOn(t, "LEGION-43"), proberOn(t, "LEGION-44")
	_, liveCopy := launch(live)
	_, endedCopy := launch(ended)
	live.Locator = pluginTestLocator(live.Token)

	pruneAllBut(runtime.SecretsDir(stateDir), []supervise.Claim{live, ended}, quietLogger())

	if !copyHolds(liveCopy, livePrompt) {
		t.Errorf("the boot removed the copy %s of the live claim's role's prompt, want it kept", liveCopy)
	}
	if !gone(endedCopy) {
		t.Errorf("the boot kept the copy %s of the role's prompt of a claim with no process, want it gone", endedCopy)
	}
}

// Two claims of one role whose plans give it one text each hold a copy of their own, so the end of
// one leaves the other's copy whole.
func TestTwoClaimsOfOneRoleWithOnePromptEachHoldACopyOfTheirOwn(t *testing.T) {
	t.Parallel()
	const prompt = "# Prober\n\nProbe the change."
	stateDir := t.TempDir()
	launch := planRoleLauncher(t, stateDir, map[string]string{"LEGION-43": prompt, "LEGION-44": prompt})
	first, second := proberOn(t, "LEGION-43"), proberOn(t, "LEGION-44")
	_, firstCopy := launch(first)
	_, secondCopy := launch(second)
	if firstCopy == secondCopy {
		t.Fatalf("both claims of prober read the copy %s, want a copy each", firstCopy)
	}

	pruningStore{dir: runtime.SecretsDir(stateDir), log: quietLogger()}.written(first)

	if !gone(firstCopy) {
		t.Errorf("the ended claim kept its copy %s", firstCopy)
	}
	if !copyHolds(secondCopy, prompt) {
		t.Errorf("the end of %s took the copy %s of %s, want it whole", first.Token, secondCopy, second.Token)
	}
}
