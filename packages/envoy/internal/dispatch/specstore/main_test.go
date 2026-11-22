package specstore

import (
	"os"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// TestMain gives the package's database tests their store (storetest.Open).
func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }
