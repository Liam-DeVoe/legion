package specgraph

import (
	"os"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
)

// TestMain runs the package's tests through cmdtest.Main, which makes the folder that the reader's
// binary is built into (newSweepReader) and removes it once the tests end.
func TestMain(m *testing.M) { os.Exit(cmdtest.Main(m)) }
