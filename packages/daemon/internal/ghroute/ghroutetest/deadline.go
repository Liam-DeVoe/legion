package ghroutetest

import (
	"flag"
	"time"

	"github.com/sjawhar/legion/daemon/internal/programbound"
)

// BoundByTheTestDeadline gives every bound registered with programbound, ghroute's help read
// (ReadHelp) and git reads (GitReadBound) and the leak guard's reads among them, what is left of
// this test binary's deadline less 30 s when its wait starts, as cmd/legion's TestMain does for
// `legion gh` (its boundByTheDeadline). Each product bound is sized for the real program, and a
// stand-in that a loaded machine makes slow to start and answer would meet it; a program that never
// answers still fails its row by name before go test's alarm. Under -timeout 0, which gives no
// deadline, the product's bounds hold. ghroute's and internal/ghtool's TestMain call it before
// m.Run, which starts go test's own alarm a moment later, so this deadline is never the later of
// the two.
func BoundByTheTestDeadline() {
	if !flag.Parsed() {
		flag.Parse()
	}
	if timeout := flag.Lookup("test.timeout").Value.(flag.Getter).Get().(time.Duration); timeout > 0 {
		programbound.SetDeadline(time.Now().Add(timeout))
	}
}
