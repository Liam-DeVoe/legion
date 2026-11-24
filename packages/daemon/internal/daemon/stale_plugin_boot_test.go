package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// The boot's stale-plugin pass holds the API's start until it returns, so it is bounded as a
// whole (stalePluginBootBound). Over a store that takes each read and never answers, the pass's
// own bound ends its first read of two stale claims of a role that is none of Legion's, long
// before that read's own bound (stalePluginReadBound), and the pass reads no more: one line names
// the two claims left to the next sweep. No line of a failed read is written for a read the
// pass's own bound ended. The test hands the pass a bound far below the read's own and reads what
// the pass did, so a pass bounded only read by read fails it: its first read would end at the
// read's own bound and the pass would go on to the second.
func TestTheBootsStalePluginPassHoldsTheBootForOneBoundWhateverTheFleet(t *testing.T) {
	t.Parallel()
	const bound = 100 * time.Millisecond
	var ended []error
	hung := func(ctx context.Context, issue string, role claim.Role) (runtime.Backend, error) {
		<-ctx.Done()
		ended = append(ended, ctx.Err())
		return "", fmt.Errorf("the role %s of the plan of %s: %w", role, issue, ctx.Err())
	}
	logs := &syncBuffer{}
	s := stalePassSupervision(t, logs, hung)
	s.claims = staleProbers(t, 2)

	s.bootStalePlugins(bound)

	if len(ended) != 1 || !errors.Is(ended[0], context.DeadlineExceeded) {
		t.Fatalf("the pass made %d reads, which ended %v; want one, ended by the pass's own bound", len(ended), ended)
	}
	lines := loggedLines(t, logs.String(), "supervise: the stale-plugin pass's bound ended before it read every claim")
	if len(lines) != 1 || lines[0]["claims"] != float64(2) {
		t.Fatalf("the lines naming the claims left = %+v, want one naming 2; it logged:\n%s", lines, logs.String())
	}
	if failed := loggedLines(t, logs.String(), stalePassReadFailed); len(failed) != 0 {
		t.Fatalf("the pass wrote %d lines of a failed read for reads its own bound ended, want none", len(failed))
	}
}
