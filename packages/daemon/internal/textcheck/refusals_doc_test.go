package textcheck

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The owner's guide to register_plan lists each reason Legion refuses a plan for, under "What
// Legion refuses" in skills/legion-architect/references/plan.md. internal/plan holds the code's
// reasons to internal/plan/refusals_doc_extract.txt, a committed copy of that list, and this half
// holds the copy to the guide, in two halves for the reason reclaim_doc_test.go gives: a test that
// reads the guide puts the guide in its job's filter.
const (
	refusalsExtractPath = "../plan/refusals_doc_extract.txt"
	ownersPlanGuide     = "../../../../skills/legion-architect/references/plan.md"
	refusalsHeading     = "## What Legion refuses"
)

// listedReason is a reason as a list item of the guide writes it: a code span that ends with a
// full stop, on a line that opens a list item.
var listedReason = regexp.MustCompile("`([^`]+\\.)`")

// Each line of the extract is a reason the guide's list writes, word for word, and each reason the
// list writes is a line of the extract. So a reason the code gives that the guide leaves out, or
// writes in other words, fails here or in internal/plan. The fault is a guide whose list falls
// behind the code; the control is the guide without its line of tasks that no task needs, under
// which that line of the extract is in no list item.
func TestTheOwnersGuideListsEachRefusal(t *testing.T) {
	extract, err := os.ReadFile(refusalsExtractPath)
	if err != nil {
		t.Fatalf("read %s: %v", refusalsExtractPath, err)
	}
	var reasons []string
	for _, line := range strings.Split(string(extract), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			reasons = append(reasons, line)
		}
	}
	guide, err := os.ReadFile(ownersPlanGuide)
	if err != nil {
		t.Fatalf("read %s: %v", ownersPlanGuide, err)
	}
	_, section, found := strings.Cut(string(guide), refusalsHeading+"\n")
	if !found {
		t.Fatalf("%s holds no section %q", ownersPlanGuide, refusalsHeading)
	}
	if next := strings.Index(section, "\n## "); next >= 0 {
		section = section[:next]
	}
	var listed []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "- `") {
			for _, match := range listedReason.FindAllStringSubmatch(line, -1) {
				listed = append(listed, match[1])
			}
		}
	}
	if len(reasons) == 0 || len(listed) == 0 {
		t.Fatalf("the extract holds %d reasons and the guide's list %d, so nothing was compared", len(reasons), len(listed))
	}
	for _, reason := range reasons {
		if !slices.Contains(listed, reason) {
			t.Errorf("the reason %q of %s is no item of the guide's list in %s", reason, refusalsExtractPath, ownersPlanGuide)
		}
	}
	for _, reason := range listed {
		if !slices.Contains(reasons, reason) {
			t.Errorf("the guide's list writes the reason %q, which is no line of %s", reason, refusalsExtractPath)
		}
	}
}
