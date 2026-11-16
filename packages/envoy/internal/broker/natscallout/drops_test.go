package natscallout

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A drop's detail is built only when it would be logged: add asks for it while no detail waits for
// the next log line, and not once one does, so a flood of drops between two lines builds one
// string rather than one a drop. Every drop is counted all the same.
func TestADropAsksForItsDetailOnlyWhenNoneWaitsToBeLogged(t *testing.T) {
	d := &drops{}
	require.True(t, d.add(dropHubGaveUp), "the first drop was not asked for its detail")
	d.note("the first")
	for range 3 {
		require.False(t, d.add(dropHubGaveUp), "a drop was asked for its detail while one waited to be logged")
	}
	require.EqualValues(t, 4, d.pending[dropHubGaveUp].Load(), "a drop was not counted")
	require.Equal(t, "the first", *d.first.Swap(nil), "the detail logged is not the first drop's")
	require.True(t, d.add(dropUnopened), "the first drop after a log line was not asked for its detail")
}
