package writerhold

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// countedDisks is the hold's Disks with a hook that runs before the nth DiskID call (1-based), so a
// row changes a disk at a known point of a command's readings.
type countedDisks struct {
	Disks
	mu    sync.Mutex
	calls int
	at    int
	hook  func()
}

func (c *countedDisks) DiskID(ctx context.Context, ref DiskRef) (uint64, error) {
	c.mu.Lock()
	c.calls++
	var hook func()
	if c.calls == c.at {
		hook = c.hook
	}
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return c.Disks.DiskID(ctx, ref)
}

// arm runs hook before the nth DiskID call from now.
func (c *countedDisks) arm(n int, hook func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at, c.hook = c.calls+n, hook
}

// In hub-restore, `hold release` reads the disks again after its row move and exits 1, naming any
// change, as abort does (plan §3a, assumption A1: "Each of the two commands reads the disks again
// after its row move and exits 1, naming any change, so the operator learns of it at once"). A
// restore that lands on claim 1 between release's check and its move leaves the row released, the
// release's done entry written, and the exit 1 naming claim 1. The control is release as it stood:
// it checked the disks before its move alone and exited 0.
func TestHoldReleaseReadsTheDisksAgainAfterItsMove(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	counted := &countedDisks{Disks: h.op.Disks}
	h.op.Disks = counted
	ctx := context.Background()
	id := h.hubRestoreHold()
	h.disks.Remake(0)
	code, _, stderr := h.run(actionProve, "--hold", id, "--restored", holdtest.Claim(0)+"=pvc-"+holdtest.Pod(0))
	require.Zero(t, code, "the prove of claim 0: %s", stderr)
	_, err := h.s.Records.Admit(ctx, id, holdrecord.Request{Command: CommandStreamsCensus}, nil)
	require.NoError(t, err, "the census's admission")
	require.NoError(t, h.s.Records.AppendDone(ctx, id, CommandStreamsCensus, nil), "the census's done entry")
	require.True(t, h.journal(id).Complete, "the census did not complete the record")

	// The check before the move reads each of the three claims' disks once; the restore lands at the
	// fourth reading, the first after the move.
	counted.arm(len(h.s.Cluster.Servers)+1, func() { h.disks.Remake(1) })
	code, _, stderr = h.run(actionRelease, "--hold", id, "--reason", "a restore lands between release's check and its move")
	require.Equal(t, 1, code, "release over a disk that changed after its check")
	require.Contains(t, stderr, holdtest.Claim(1))
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
	require.Equal(t, holdrecord.StateDone, latestState(h.journal(id), holdrecord.CommandRelease))
}
