package textcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A plan can leave the reviewer step out, and the concepts page says which duty each left-out step
// hands on. On a task with no reviewer step the implementer's prompt gives the implementer two of
// the reviewer's duties (internal/prompts/roles/implementer.md): it writes the pull request body's
// `Look at first` and `Not proven / risk` lines at each round, and before each completion, its
// retro's included, it answers and resolves each bot review thread still open and names each one it
// declined to its architect. The page's paragraph on left-out steps names both, beside the merger's
// wait for a missing approval. This test reads only the page, for the reason the package doc gives:
// a test that reads a guide puts the guide in its job's filter.
func TestTheConceptsPageNamesTheImplementersDutiesOnATaskWithNoReviewer(t *testing.T) {
	const page = "docs/site/src/content/docs/legion/concepts.md"
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "..", filepath.FromSlash(page)))
	if err != nil {
		t.Fatalf("read %s: %v", page, err)
	}
	var paragraph string
	for _, p := range strings.Split(string(body), "\n\n") {
		if strings.HasPrefix(p, "A step a plan leaves out hands its duty on.") {
			paragraph = strings.Join(strings.Fields(p), " ")
		}
	}
	if paragraph == "" {
		t.Fatalf("%s holds no paragraph that starts %q", page, "A step a plan leaves out hands its duty on.")
	}
	const duties = "The implementer then writes the pull request body's `Look at first` and `Not proven / risk` lines at each round, and before each completion, its retro's included, answers and resolves each bot review thread still open, naming each one it declined to the architect."
	if !strings.Contains(paragraph, duties) {
		t.Errorf("%s does not name the implementer's duties on a task with no reviewer step: want %q in %q", page, duties, paragraph)
	}
}
