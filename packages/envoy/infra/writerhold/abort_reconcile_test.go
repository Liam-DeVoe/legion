package writerhold

import (
	"context"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// scriptedStores is a StreamIDWriter that answers each call with the next of its answers, the last
// again after that, and records each call's families and daemon databases.
type scriptedStores struct {
	mu      sync.Mutex
	answers []StreamIDResult
	calls   [][2][]string
}

func (s *scriptedStores) write(_ context.Context, _ *nats.Conn, _ DaemonDeployments, families, daemonDatabases []string) (StreamIDResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, [2][]string{append([]string(nil), families...), append([]string(nil), daemonDatabases...)})
	return s.answers[min(len(s.calls), len(s.answers))-1], nil
}

func (s *scriptedStores) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// provenHubRestore is a hub-restore hold whose claim 0 was restored and proved, with an abort's
// stop before the prove, so an abort is admitted on its own stop, and the cutover row of daemon/P
// written under it.
func (h *heldStack) provenHubRestore() string {
	h.t.Helper()
	h.seedProtectedStreams()
	id := h.hubRestoreHold()
	h.disks.Remake(0)
	code, _, _ := h.run(actionAbort, "--hold", id, "--reason", "abort over a restore")
	require.Equal(h.t, 1, code)
	code, _, _ = h.run(actionProve, "--hold", id, "--restored", holdtest.Claim(0)+"=pvc-"+holdtest.Pod(0))
	require.Zero(h.t, code, "the prove of the restored claim")
	_, err := h.s.Records.PutCutover(context.Background(), "daemon/P", "done", id)
	require.NoError(h.t, err)
	return id
}

// After a done prove, hub-restore's abort reconciles the daemon stores before it releases (V67r2-1,
// round 4 (b)): its writer is given the cutover rows' daemon/<P> families and the operator's
// --daemon-database entries, and the abort releases only when the writer's result places every
// daemon family in exactly one list and leaves none unreconciled. With no writer it is UNKNOWN; a
// store left unreconciled is refused naming it; a result that places daemon/P in no list is
// UNKNOWN naming it. Each writes a stop and the row stays held; the abort is admitted again after
// its own stop, and with the store reconciled it releases. The control is the abort as it stood,
// which released after its disk check without reading the stores (run by hand: abortReconcile
// skipped, the first abort exits 0).
func TestHoldAbortReconcilesTheDaemonStores(t *testing.T) {
	h := newHeldStack(t)
	id := h.provenHubRestore()
	h.daemonDeployments("P")
	abort := func(what string) (int, string) {
		t.Helper()
		code, _, stderr := h.run(actionAbort, "--hold", id, "--reason", what, "--daemon-database", "P=/run/daemon-p")
		require.Equal(t, natsgrant.OperatorPhaseHeld, h.row().Phase, "%s: the row", what)
		return code, stderr
	}

	code, stderr := abort("no writer")
	require.Equal(t, 2, code, "an abort with daemon stores and no writer")
	require.Contains(t, stderr, ErrNoStoreWriter.Error())
	require.Equal(t, "stop", latestState(h.journal(id), holdrecord.CommandAbort))

	stores := &scriptedStores{answers: []StreamIDResult{
		{Unreconciled: []UnreconciledStore{{Project: "P", Reason: "its database answers nothing"}}},
		{},
		{Reconciled: []ReconciledStore{{Project: "P", StreamID: "s-1"}}},
	}}
	h.d.Readers.Stores = stores.write
	code, stderr = abort("a store left unreconciled")
	require.NotZero(t, code)
	require.Contains(t, stderr, "daemon/P: its database answers nothing")
	code, stderr = abort("a result that places no store")
	require.Equal(t, 2, code)
	require.Contains(t, stderr, "daemon/P is in none of Changed, Gone, Reconciled and Unreconciled")

	code, _, stderr = h.run(actionAbort, "--hold", id, "--reason", "the store reconciled", "--daemon-database", "P=/run/daemon-p")
	require.Zero(t, code, "the abort with every store reconciled: %s", stderr)
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
	require.Equal(t, 3, stores.count())
	require.Equal(t, [2][]string{{"daemon/P"}, {"P=/run/daemon-p"}}, stores.calls[2], "the writer's families and daemon databases")
}

// A crashed abort's rerun repeats the whole abort (round 4, from #4679's model): an abort admitted
// on its own pending intent, whose run crashed between the reconcile's writes and the release
// (here its intent written and the writer run once, the row still held), checks the disks against
// the newest done prove, reconciles every store again and releases. The control is the same rerun
// with a store the second reconcile leaves unreconciled: it stops, and the row stays held, so the
// rerun does not skip the reconcile the crashed run had begun.
func TestHoldACrashedAbortIsRerunWhole(t *testing.T) {
	for _, row := range []struct {
		name    string
		second  StreamIDResult
		release bool
	}{
		{"the control: the rerun's reconcile leaves a store unreconciled", StreamIDResult{Unreconciled: []UnreconciledStore{{Project: "P", Reason: "unread"}}}, false},
		{"the rerun reconciles and releases", StreamIDResult{Reconciled: []ReconciledStore{{Project: "P", StreamID: "s-1"}}}, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHeldStack(t)
			id := h.provenHubRestore()
			h.daemonDeployments("P")
			ctx := context.Background()
			stores := &scriptedStores{answers: []StreamIDResult{{Changed: []StreamIDChange{{Project: "P", From: "s-0", To: "s-1"}}}, row.second}}
			h.d.Readers.Stores = stores.write
			// The crashed run: admitted, its reconcile wrote, then it died before the move.
			_, err := h.s.Records.Admit(ctx, id, holdrecord.Request{Command: holdrecord.CommandAbort}, nil)
			require.NoError(t, err)
			_, err = stores.write(ctx, nil, nil, []string{"daemon/P"}, []string{"P=/run/daemon-p"})
			require.NoError(t, err)

			code, _, stderr := h.run(actionAbort, "--hold", id, "--reason", "the rerun of a crashed abort", "--daemon-database", "P=/run/daemon-p")
			require.Equal(t, 2, stores.count(), "the rerun did not reconcile the stores again")
			if !row.release {
				require.NotZero(t, code, "control: the rerun released over an unreconciled store")
				require.Contains(t, stderr, "daemon/P: unread")
				require.Equal(t, natsgrant.OperatorPhaseHeld, h.row().Phase)
				return
			}
			require.Zero(t, code, "the rerun: %s", stderr)
			require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
			require.True(t, h.journal(id).Complete)
		})
	}
}
