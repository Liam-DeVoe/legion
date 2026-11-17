package supervise

import (
	"slices"
	"testing"
)

// SuspendSources answers a list kept by hand beside its constants, and a project release names
// how each claim it stopped ended in its words (api.ReleasedClaimEnds), which every plugin reads
// through a strict enum: a source left out of the list would be a word the release writes that
// neither the golden file nor the contracts suite holds. So the list is held here to the
// constants of its type as the compiler reads them from this package's source (packageSets), and
// a constant added and not listed fails. The states the same words are built from are held by
// TestStatesIsEveryDeclaredStateOnce.
func TestSuspendSourcesNameEveryConstant(t *testing.T) {
	declared := packageSets(t).sources
	if len(declared) == 0 {
		t.Fatalf("read no constant of SuspendSource's type from the source")
	}
	listed := make([]string, 0, len(suspendSources))
	for _, source := range SuspendSources() {
		listed = append(listed, string(source))
	}
	if listed, declared := slices.Sorted(slices.Values(listed)), slices.Sorted(slices.Values(declared)); !slices.Equal(listed, declared) {
		t.Errorf("SuspendSources lists %q, want each constant of its type once: %q", listed, declared)
	}
}
