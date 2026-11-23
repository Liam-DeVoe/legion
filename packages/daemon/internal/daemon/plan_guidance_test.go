package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/prompts"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The deployment's plan guidance reaches the composed prompt of each owner, a tree's root
// architect and a sub-architect, after its own addressing and before the deployment's
// instructions, and no other agent's. Each launch is built twice, by a daemon whose deployment
// sets the guidance and by one that sets none. Both owners are told the read of earlier plans and
// asked to choose a fresh or an earlier agent for each step, with guidance or with none. A phase
// worker's prompt is the same text in both builds, byte for byte: a deployment that sets the
// guidance changes no other agent's prompt.
func TestOnlyAnOwnersPromptHoldsThePlanGuidance(t *testing.T) {
	t.Parallel()
	composer, err := prompts.New(t.TempDir())
	if err != nil {
		t.Fatalf("compose the shipped prompts: %v", err)
	}
	stateDir := t.TempDir()
	const (
		planted      = "Plant 7f3a: give each review to another model family."
		header       = "# Plan guidance of this deployment (s1)"
		deployment   = "Deployment rule 7f3a."
		earlierPlans = "`dispatch plans --project <PROJECT>`"
		freshOrEarly = "For each step, decide whether a fresh agent works it or the agent of an earlier step"
	)
	guidance, err := config.WritePlanGuidance([]byte(planted+"\n"), stateDir, "s1")
	if err != nil {
		t.Fatalf("WritePlanGuidance: %v", err)
	}
	instructions, err := config.WriteDeploymentInstructions([]byte(deployment+"\n"), stateDir, "s1")
	if err != nil {
		t.Fatalf("WriteDeploymentInstructions: %v", err)
	}
	builds := []struct {
		name string
		s    specs
	}{
		{"with", specs{stateDir: stateDir, project: "s1", prompts: composer, instructions: instructions, planGuidance: guidance, appOwners: noAppOwners}},
		{"without", specs{stateDir: stateDir, project: "s1", prompts: composer, instructions: instructions, appOwners: noAppOwners}},
	}
	launches := []struct {
		name, issue string
		role        claim.Role
		owner       bool
	}{
		{"the root architect", "S1-1", claim.RoleArchitect, true},
		{"a sub-architect", "S1-2", claim.RoleArchitect, true},
		{"the planner", "S1-2", claim.RolePlanner, false},
		{"the implementer", "S1-2", claim.RoleImplementer, false},
		{"the tester", "S1-2", claim.RoleTester, false},
		{"the reviewer", "S1-2", claim.RoleReviewer, false},
		{"the merger", "S1-2", claim.RoleMerger, false},
	}
	composed := map[string]map[string]string{}
	for _, build := range builds {
		composed[build.name] = map[string]string{}
		for _, launch := range launches {
			t.Run(build.name+"/"+launch.name, func(t *testing.T) {
				token, err := claim.NewToken("s1", launch.issue, launch.role)
				if err != nil {
					t.Fatal(err)
				}
				spec, err := build.s.SpawnSpec(context.Background(), supervise.Claim{Token: token, Issue: launch.issue, Tree: "S1-1", Role: launch.role})
				if err != nil {
					t.Fatalf("SpawnSpec: %v", err)
				}
				text, err := runtime.ComposePrompt(spec.Prompt)
				if err != nil {
					t.Fatalf("ComposePrompt: %v", err)
				}
				composed[build.name][launch.name] = text
				guided := build.name == "with" && launch.owner

				if launch.owner {
					for _, phrase := range []string{earlierPlans, "implementer (implementation)", freshOrEarly} {
						if !strings.Contains(text, phrase) {
							t.Errorf("the prompt omits %q", phrase)
						}
					}
				} else if !strings.Contains(text, deployment) {
					t.Errorf("the prompt omits the deployment's instructions %q", deployment)
				}
				want := 0
				if guided {
					want = 1
				}
				for _, phrase := range []string{header, planted} {
					if held := strings.Count(text, phrase); held != want {
						t.Errorf("the prompt holds %q %d times, want %d: once in an owner's prompt of a deployment that sets the guidance, and nowhere else", phrase, held, want)
					}
				}
				if !guided {
					return
				}
				// The order anchors on the launch's own addressing, which no role part holds, and on
				// text the guidance copy alone holds: the guidance starts after the addressing ends,
				// and the deployment's instructions start after the guidance ends.
				addressing := spec.Prompt.Addressing
				addressingEnds := strings.Index(text, addressing) + len(addressing)
				guidanceStarts, guidanceEnds := strings.Index(text, header), strings.Index(text, planted)+len(planted)
				deploymentStarts := strings.Index(text, deployment)
				if addressing == "" || strings.Count(text, addressing) != 1 || addressingEnds > guidanceStarts || guidanceEnds > deploymentStarts {
					t.Errorf("the guidance is not between the launch's addressing and the deployment's instructions: the addressing (held %d times) ends at byte %d, the guidance runs from %d to %d, the deployment's instructions start at %d",
						strings.Count(text, addressing), addressingEnds, guidanceStarts, guidanceEnds, deploymentStarts)
				}
			})
		}
	}
	for _, launch := range launches {
		if !launch.owner && composed["with"][launch.name] != composed["without"][launch.name] {
			t.Errorf("%s's prompt differs between a deployment that sets the plan guidance and one that sets none", launch.name)
		}
	}
}
