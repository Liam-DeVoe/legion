package plan

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// refusalsExtract is the committed list of the reasons the owner's guide gives for register_plan's
// refusals; internal/textcheck holds the guide to it.
const refusalsExtract = "refusals_doc_extract.txt"

// placeholder is a name of the plan as the guide writes it: `<id>`, `<ids>`, `<chain>`.
var placeholder = regexp.MustCompile(`<[a-z0-9]+>`)

// readRefusalsExtract is each reason of the extract, its comment lines left out.
func readRefusalsExtract(t *testing.T) []string {
	t.Helper()
	text, err := os.ReadFile(refusalsExtract)
	if err != nil {
		t.Fatalf("read %s: %v", refusalsExtract, err)
	}
	var reasons []string
	for _, line := range strings.Split(string(text), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			reasons = append(reasons, line)
		}
	}
	return reasons
}

// Each reason Validate and Supported give is a line of the extract the owner's guide is held to,
// with each name of the plan as a placeholder, and each line of the extract is a reason one of the
// plans below gets. So a reason the code adds or rewords fails here until the extract, and through
// internal/textcheck the guide, says it too. The fault is a guide that does not list a reason the
// code gives; the control is the extract without its line of tasks that no task needs, under which
// the plan of a and b gets a reason no line matches.
func TestEachRefusalIsALineOfTheOwnersGuide(t *testing.T) {
	templates := readRefusalsExtract(t)
	if len(templates) == 0 {
		t.Fatalf("%s holds no reason, so nothing was compared", refusalsExtract)
	}
	patterns := make([]*regexp.Regexp, len(templates))
	for index, template := range templates {
		patterns[index] = regexp.MustCompile("^" + placeholder.ReplaceAllString(regexp.QuoteMeta(template), ".+") + "$")
	}
	implementer, _ := ParseSteps("implementer")
	withRoles := oneTask("a", "implementer prober")
	withRoles.RoleBlocks = []string{"tester-two"}
	plans := []Plan{
		planOf(t, 1, taskNeeding("a"), taskNeeding("b")),
		planOf(t, 1, taskNeeding("a", "b"), taskNeeding("b")),
		planOf(t, 1, taskNeeding("a"), taskNeeding("d", "x")),
		planOf(t, 1, taskNeeding("a", "b"), taskNeeding("b", "a")),
		oneTask("a", "reviewer implementer"),
		oneTask("a", "tester reviewer"),
		oneTask("a", "implementer reviewer reviewer"),
		oneTask("a", "implementer acceptance"),
		oneTask("a", "implementer:cd"),
		withRoles,
		{Tasks: []Task{{ID: "a", Merges: true, HasSteps: true, Steps: implementer}}},
		{Tasks: []Task{{ID: "f", Owner: "architect"}}},
		{Tasks: []Task{{ID: "f1", PartOf: "f", HasSteps: true, Steps: implementer}}},
		{},
	}
	matched := make([]bool, len(templates))
	d := Deployment{Roles: BuiltinRoles()}
	for _, p := range plans {
		for _, refusal := range append(Supported(p), Validate(p, d)...) {
			reason, found := strings.TrimSuffix(refusal.Reason, notYet), false
			for index, pattern := range patterns {
				if pattern.MatchString(reason) {
					matched[index], found = true, true
				}
			}
			if !found {
				t.Errorf("the reason %q is no line of %s, so the owner's guide does not list it", reason, refusalsExtract)
			}
		}
	}
	for index, template := range templates {
		if !matched[index] {
			t.Errorf("the line %q of %s is a reason none of the plans gets: it names no reason the code gives, or a plan is missing", template, refusalsExtract)
		}
	}
}
