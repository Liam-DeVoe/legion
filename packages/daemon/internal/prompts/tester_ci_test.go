package prompts

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// testerCIWords is the word file of the check that the tester reads no CI: one pattern a line,
// each a candidate, matched without regard to case, of a sentence that sends the tester to a CI
// job, a CI run or a result that Dispatch takes from CI.
const testerCIWords = "../../../../scripts/e2e/tester-ci-words.txt"

// The tester reads no CI: no text the tester's prompt is composed of sends it to a CI job, a CI
// run or a result that Dispatch takes from CI. Each file Compose gives the tester is read with its
// lines joined, so a phrase a hard-wrapped text breaks over two lines is still found, against each
// pattern of the word file, which holds the eight patterns the check names. A hit is a candidate a
// reader judges, and none stands in these texts. This shows what the texts say, never that a
// tester obeys them.
func TestTheTesterTextsSendItToNoCI(t *testing.T) {
	body, err := os.ReadFile(testerCIWords)
	if err != nil {
		t.Fatalf("read the word file: %v", err)
	}
	var words []string
	for _, line := range strings.Split(string(body), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			words = append(words, line)
		}
	}
	want := []string{"read CI", "gh pr checks", "gh run view", "gh run rerun", "check-runs", "CI run still going", "results from CI", "derived from its CI run"}
	if !slices.Equal(words, want) {
		t.Fatalf("the word file holds %q, want the eight patterns %q", words, want)
	}
	patterns := make([]*regexp.Regexp, len(words))
	for i, word := range words {
		patterns[i] = regexp.MustCompile("(?i)" + word)
	}

	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	parts, err := composer.Compose(claim.RoleTester, false)
	if err != nil {
		t.Fatalf("Compose the tester: %v", err)
	}
	if len(parts.RolePromptPaths) != 6 {
		t.Fatalf("Compose gives the tester %d files %q, want the six the check names", len(parts.RolePromptPaths), parts.RolePromptPaths)
	}
	for _, path := range parts.RolePromptPaths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := foldWhitespace(string(body))
		for _, pattern := range patterns {
			for _, at := range pattern.FindAllStringIndex(text, -1) {
				t.Errorf("%s sends the tester to CI: %q", path, text[max(0, at[0]-80):min(len(text), at[1]+80)])
			}
		}
	}
}
