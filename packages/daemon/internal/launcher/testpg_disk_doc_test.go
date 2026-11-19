package launcher

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// An agent whose test databases vanish in the middle of a run reads
// skill://legion-worker/references/tools.md to learn why. Where the test database is on a disk of
// its own, the launcher's check resets the cluster under its floor (checkTestPGDisk), and the one
// trace that reset leaves is the line legion-testpg writes, which starts `legion-testpg: reset`
// (testPGProgram's name). So the reference must name that line and give a `kubectl logs` command
// that shows it, on the `roles` container, whose log holds it: a reference that names another
// line, or sends the agent to another container's log, leaves the agent nothing to find and no
// cause. The command is held whole, as the one code span the agent copies: the container it names
// with -c, and a filter after it for the line. The skill is read from the repository by relative
// path, as internal/prompts' TestThePodMemoryRuleAndTheToolsSkillSendTheSameRunsToCI reads it,
// with its whitespace collapsed, since the skill hard-wraps.
func TestPGDiskTheToolsReferenceNamesTheResetLineAndTheCommandThatShowsIt(t *testing.T) {
	skillPath := filepath.Join("..", "..", "..", "..", "skills", "legion-worker", "references", "tools.md")
	body, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read %s, the reference an agent whose test databases vanished reads: %v", skillPath, err)
	}
	resetLine := testPGProgram + ": reset"
	var named bool
	var commands []string
	for _, span := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(strings.Join(strings.Fields(string(body)), " "), -1) {
		named = named || strings.HasPrefix(span[1], resetLine)
		if strings.HasPrefix(span[1], "kubectl logs ") {
			commands = append(commands, span[1])
		}
	}
	if !named {
		t.Errorf("%s names no line starting %q, the line a reset of the test database writes", skillPath, resetLine)
	}
	for _, command := range commands {
		read, filter, _ := strings.Cut(command, "|")
		if loggedContainer(strings.Fields(read)) == "roles" && strings.Contains(filter, resetLine) {
			return
		}
	}
	t.Errorf("%s gives the kubectl logs commands %q, and none reads the roles container (-c roles) and filters it for %q",
		skillPath, commands, resetLine)
}

// loggedContainer is the container a `kubectl logs` command's words name, in any of the forms
// kubectl reads (-c roles, -c=roles, --container roles, --container=roles), or "" where they name
// none.
func loggedContainer(words []string) string {
	for i, word := range words {
		for _, flag := range []string{"-c", "--container"} {
			if value, ok := strings.CutPrefix(word, flag+"="); ok {
				return value
			}
			if word == flag && i+1 < len(words) {
				return words[i+1]
			}
		}
	}
	return ""
}
