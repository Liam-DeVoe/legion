package testtmp

import "testing"

// exceptions are the paths under packages/envoy this check leaves unread, with the reason each
// places a temporary directory itself on purpose.
var exceptions = map[string]string{
	"testtmp": "this package, where both shapes belong: Dir reads os.TempDir and makes the directory " +
		"itself, naming /tmp when it falls back, so that no other test has to",
}

// Every test of this module that wants a temporary directory of its own takes it from this
// package, so that one place measures a socket path against what Linux holds and one place knows
// where to fall back to (CheckPlacements says what it reads). The walk takes in infra, a module of
// its own under this root, whose tests reach this package through the replace directive in its
// go.mod. packages/daemon/internal/textcheck runs the same check over that module.
func TestNoTestFilePlacesATemporaryDirectoryOfItsOwn(t *testing.T) {
	CheckPlacements(t, "..", exceptions)
}
