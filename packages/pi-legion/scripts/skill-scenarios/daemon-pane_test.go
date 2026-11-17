// The tests of daemon-pane.go's own copies of the daemon, compiled with the program as
// cmd/daemon-pane's main_test.go inside this checkout's daemon module (goInDaemonModule in
// daemon-pane.ts, run by a row of daemon-pane.test.ts). They name the daemon's own functions, which
// an older checkout lacks, so no row compiles them in another checkout: they hold each copy to the
// daemon of the checkout they belong to.
package main

import (
	"os"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/daemon"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The program's rule of an owner's launch answers daemon.OwnsPlan for the rig's claim of each role,
// on the root issue of its tree and on a child under it, and its copy of a plan guidance is the file
// config.WritePlanGuidance writes, so a change of either in the daemon fails here until the program
// takes it.
func TestTheToolRestatesTheDaemonsPlanGuidance(t *testing.T) {
	compared := 0
	for _, role := range claim.Roles {
		for _, issue := range []string{rigRoot, rigChild} {
			token, err := claim.NewToken(rigProject, issue, role)
			if err != nil {
				t.Fatal(err)
			}
			c := supervise.Claim{Token: token, Project: rigProject, Tree: rigRoot, Issue: issue, Role: role, Generation: 1}
			if got, want := ownsPlan(c), daemon.OwnsPlan(c); got != want {
				t.Errorf("ownsPlan of the %s on %s (tree %s) answers %t, daemon.OwnsPlan %t", role, issue, rigRoot, got, want)
			}
			compared++
		}
	}
	if compared == 0 || compared != 2*len(claim.Roles) {
		t.Errorf("compared %d claims, want two for each of the %d roles of claim.Roles, and more than none", compared, len(claim.Roles))
	}

	const guidance = "Plant 7f3a.\n"
	path, err := config.WritePlanGuidance([]byte(guidance), t.TempDir(), rigProject)
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := planGuidanceCopy(rigProject, []byte(guidance)); got != string(written) {
		t.Errorf("planGuidanceCopy(%q, %q) = %q, want the file config.WritePlanGuidance writes, %q", rigProject, guidance, got, written)
	}
}

// The prompt the program prints for a launch, with a plan guidance and without one, is the prompt
// runtime.ComposePrompt composes from the same parts: the program's join of the role's parts, the
// addressing and the guidance copy is the daemon's, part for part and blank line for blank line.
func TestTheToolJoinsAPromptAsTheDaemonDoes(t *testing.T) {
	guidance := []byte("Plant 7f3a.\n")
	copied, err := config.WritePlanGuidance(guidance, t.TempDir(), rigProject)
	if err != nil {
		t.Fatal(err)
	}
	compared := 0
	for _, role := range claim.Roles {
		for _, root := range []bool{true, false} {
			c, err := rigClaim(role, root)
			if err != nil {
				t.Fatal(err)
			}
			stateDir := t.TempDir()
			paths, addressing, err := rolePromptInputs(stateDir, rigProject, c)
			if err != nil {
				t.Fatal(err)
			}
			// The daemon gives the guidance to an owner's launch alone (workerSpawnSpec), so the
			// parts it composes for every other role name no guidance file.
			parts := runtime.PromptParts{RolePromptPaths: paths, Addressing: addressing}
			if daemon.OwnsPlan(c) {
				parts.PlanGuidancePath = copied
			}
			want, err := runtime.ComposePrompt(parts)
			if err != nil {
				t.Fatal(err)
			}
			got, err := guidedPrompt(stateDir, c, guidance)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("the prompt of the %s on %s with a plan guidance is\n%q\nand runtime.ComposePrompt's is\n%q", role, c.Issue, got, want)
			}
			compared++
		}
	}
	if compared == 0 || compared != 2*len(claim.Roles) {
		t.Errorf("compared %d prompts, want two for each of the %d roles of claim.Roles, and more than none", compared, len(claim.Roles))
	}
}
