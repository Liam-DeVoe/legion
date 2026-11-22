package writerhold

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// Hub-restore's abort records each daemon store it found gone, with the server's 3D000 answer and
// the store's address (its database and instance), in the journal: in its stop entry when another
// store is left unreconciled, and in its done entry when it releases (gone.daemon/<P>, as the
// census's done entry names it), beside each store it rewrote (stream_id.daemon/<P>). Plan §3a,
// hub-restore: a store confirmed gone "is recorded in that stop with the database's answer". The
// control is the abort as it stood: it printed the gone store on stdout alone, its stop held only
// the unreconciled store's text, and its done entry no output.
func TestHoldAbortRecordsTheStoresItFoundGone(t *testing.T) {
	h := newHeldStack(t)
	id := h.provenHubRestore()
	h.daemonDeployments("P", "Q")
	_, err := h.s.Records.PutCutover(t.Context(), "daemon/Q", "done", id)
	require.NoError(t, err)
	gone := GoneStore{Project: "P", Database: "daemon_p", Instance: "proj:region:inst-p", StatedInstance: "proj:region:inst-p",
		Answer: `FATAL: database "daemon_p" does not exist (SQLSTATE 3D000)`}
	stores := &scriptedStores{answers: []StreamIDResult{
		{Gone: []GoneStore{gone}, Unreconciled: []UnreconciledStore{{Project: "Q", Reason: "its server does not answer"}}},
		{Gone: []GoneStore{gone}, Changed: []StreamIDChange{{Project: "Q", From: "stream-0", To: "stream-1"}}},
	}}
	h.d.Readers.Stores = stores.write
	abort := func(what string) (int, string, string) {
		return h.run(actionAbort, "--hold", id, "--reason", what, "--daemon-database", "P=/run/daemon-p", "--daemon-database", "Q=/run/daemon-q")
	}
	recorded := func(what, text string) {
		t.Helper()
		for _, want := range []string{"daemon_p", "proj:region:inst-p", "SQLSTATE 3D000"} {
			require.Contains(t, text, want, "%s does not record the gone store's %s", what, want)
		}
	}

	code, stdout, _ := abort("daemon/Q unreconciled, daemon/P gone")
	require.Equal(t, 2, code, "the abort with daemon/Q unreconciled")
	require.Contains(t, stdout, "GONE daemon/P")
	require.Equal(t, natsgrant.OperatorPhaseHeld, h.row().Phase)
	stop := h.lastEntry(id)
	require.Equal(t, holdrecord.CommandAbort+" "+holdrecord.StateStop, stop.Command+" "+stop.State)
	require.Contains(t, stop.Outputs["reason"], "found gone: daemon/P", "the abort's stop does not name the gone store")
	recorded("the abort's stop", stop.Outputs["reason"])

	code, _, stderr := abort("daemon/Q reconciled, daemon/P gone")
	require.Zero(t, code, "the abort with every store changed or gone: %s", stderr)
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
	done := h.lastEntry(id)
	require.Equal(t, holdrecord.CommandAbort+" "+holdrecord.StateDone, done.Command+" "+done.State)
	require.Contains(t, done.Outputs, "gone.daemon/P", "the abort's done entry does not name the gone store: %v", done.Outputs)
	recorded("the abort's done entry", done.Outputs["gone.daemon/P"])
	require.True(t, strings.HasPrefix(done.Outputs["stream_id.daemon/Q"], "daemon/Q: stream_id"), "the abort's done entry does not record the store it rewrote: %v", done.Outputs)
}
