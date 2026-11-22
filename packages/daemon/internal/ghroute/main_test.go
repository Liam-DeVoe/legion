package ghroute

import (
	"os"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/ghroute/ghroutetest"
)

// TestMain gives the bounds of this package's waits on a program it starts, the help read's
// (helpTimeout) and the git reads' (GitReadBound), what is left of this test binary's deadline
// (ghroutetest.BoundByTheTestDeadline): the help-read rows run ReadHelp on stand-in gh scripts,
// and one whose read a loaded machine made outlast the product's 30 s would read that stop in
// place of the one the row makes.
func TestMain(m *testing.M) {
	ghroutetest.BoundByTheTestDeadline()
	os.Exit(m.Run())
}
