package supervise

import (
	"slices"
	"testing"
)

// ClaimStates and SuspendSources are kept by hand beside their constants, and a project release
// names how each claim it stopped ended in their words (api.ReleasedClaimEnds), which every plugin
// reads through a strict enum: a state or a source left out of its list would be a word the
// release writes that neither the golden file nor the contracts suite holds. So each list is held
// here to the constants of its type as the compiler reads them from this package's source
// (packageSets), and a constant added and not listed fails.
func TestClaimStatesAndSuspendSourcesNameEveryConstant(t *testing.T) {
	set := packageSets(t)
	for _, roster := range []struct {
		name             string
		listed, declared []string
	}{
		{"ClaimStates", rosterWords(ClaimStates), set.states},
		{"SuspendSources", rosterWords(SuspendSources), set.sources},
	} {
		if len(roster.declared) == 0 {
			t.Fatalf("read no constant of %s's type from the source", roster.name)
		}
		if listed, declared := slices.Sorted(slices.Values(roster.listed)), slices.Sorted(slices.Values(roster.declared)); !slices.Equal(listed, declared) {
			t.Errorf("%s lists %q, want each constant of its type once: %q", roster.name, listed, declared)
		}
	}
}

// rosterWords is each of values as its word.
func rosterWords[T ~string](values []T) []string {
	words := make([]string, len(values))
	for i, value := range values {
		words[i] = string(value)
	}
	return words
}
