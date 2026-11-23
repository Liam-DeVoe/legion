package replay

import (
	"os"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/testpg"
)

// TestMain drops the migrated template seedStore clones its databases from (testpg.Main). A replay
// makes and migrates a database of its own (openReplayDatabase) and clones none.
func TestMain(m *testing.M) { os.Exit(testpg.Main("legion_replay_test", m.Run)) }
