package writerhold

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// failingDisks is the hold's Disks whose nth DiskID call (1-based, counted from failAfter) fails, as
// a Compute API that does not answer.
type failingDisks struct {
	Disks
	mu    sync.Mutex
	calls int
	at    int
}

func (f *failingDisks) DiskID(ctx context.Context, ref DiskRef) (uint64, error) {
	f.mu.Lock()
	f.calls++
	fail := f.calls == f.at
	f.mu.Unlock()
	if fail {
		return 0, errors.New("compute.disks.get answered 503")
	}
	return f.Disks.DiskID(ctx, ref)
}

// failAfter makes the nth DiskID call from now fail.
func (f *failingDisks) failAfter(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.at = f.calls + n
}

// In hub-restore, a read of the disks after the row move that cannot be read is UNKNOWN about the
// disks, not a change: `hold release` (and `hold abort`, through the same disksAfterMove) says it
// could not read the disks again after its move, with the hold released and its done entry
// written, and claims no change it did not see. The control is release at 599c9838f2, whose line
// read "a disk changed after release's last check, outside any hold" over a read that failed.
func TestHoldADiskReadThatFailsAfterTheMoveClaimsNoChange(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	failing := &failingDisks{Disks: h.op.Disks}
	h.op.Disks = failing
	ctx := context.Background()
	id := h.hubRestoreHold()
	h.disks.Remake(0)
	code, _, stderr := h.run(actionProve, "--hold", id, "--restored", holdtest.Claim(0)+"=pvc-"+holdtest.Pod(0))
	require.Zero(t, code, "the prove of claim 0: %s", stderr)
	_, err := h.s.Records.Admit(ctx, id, holdrecord.Request{Command: CommandStreamsCensus}, nil)
	require.NoError(t, err)
	require.NoError(t, h.s.Records.AppendDone(ctx, id, CommandStreamsCensus, nil))

	// The check before the move reads the three claims' disks; the fourth read, the first after the
	// move, fails.
	failing.failAfter(len(h.s.Cluster.Servers) + 1)
	code, _, stderr = h.run(actionRelease, "--hold", id, "--reason", "the disks cannot be read after the move")
	require.Equal(t, 2, code, "release whose read after the move failed: %s", stderr)
	require.Contains(t, stderr, "could not read the disks again after its row move")
	require.NotContains(t, stderr, "a disk changed")
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
	require.Equal(t, holdrecord.StateDone, latestState(h.journal(id), holdrecord.CommandRelease))
}
