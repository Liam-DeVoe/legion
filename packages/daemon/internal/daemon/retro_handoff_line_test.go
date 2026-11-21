package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skills/legion-retro/SKILL.md, step 1, which the implementer reads in retro, names a line of
// main's phase-assignment task by its words: "the review at the version your task's `Handoff:`
// line names (`--phase review --version <n>`)". Main's task wrote " Handoff: %s version %d
// (dispatch handoff-read --issue %s --phase %s --version %d)." (workflow/effects.go:task at
// d46ec4e0). INFRA-3686's R8: "A sentence that a prompt or a skill names by its words, such as
// \"this resume is your design-approved wake\", also stays word for word." The branch's task gives
// the handoff as handoff_phase and handoff_version and has no such line.
func TestTheRetroSkillsHandoffLineIsInThePhaseAssignment(t *testing.T) {
	skill, err := os.ReadFile(filepath.Join(goldenRepositoryRoot, "skills", "legion-retro", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	const says = "the version your task's `Handoff:` line names"
	if !strings.Contains(strings.Join(strings.Fields(string(skill)), " "), says) {
		t.Skipf("skills/legion-retro/SKILL.md no longer says %q", says)
	}
	held, err := os.ReadFile(goldenTextsPath)
	if err != nil {
		t.Fatal(err)
	}
	var golden goldenFile
	if err := json.Unmarshal(held, &golden); err != nil {
		t.Fatal(err)
	}
	for _, sample := range golden.Samples {
		if sample.Kind != "phase-assignment" || !strings.Contains(sample.Text, "handoff_version:") {
			continue
		}
		if !strings.Contains(sample.Text, "Handoff:") {
			t.Errorf("skills/legion-retro/SKILL.md says %q, but %s, a phase-assignment that names a handoff, has no `Handoff:` line:\n%s", says, sample.id(), sample.Text)
		}
		return
	}
	t.Fatal("no phase-assignment sample names a handoff")
}
