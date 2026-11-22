package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// `hold restore` with no --hold after a prepare entry, which the broker refuses 409 hold_mismatch
// (a released row after a prepare entry must name that entry's hold), says so and names the hold
// the route names (RouteError.HoldID, since no route lists the log), with the broker's message
// beside the code, and nothing written. Controls: restore at 702c7749de, which printed the route's
// raw error; at 870ce0c500, which named no id ("hold restore --hold <its hold id>"); and at
// 449251ffc3, which printed no message.
func TestHoldRestoreWithNoHoldAfterAPrepareNamesTheHold(t *testing.T) {
	h := newHeldStack(t)
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	h.deleteRow()

	code, stdout, stderr := h.run(actionRestore, "--reason", "no --hold after a prepare")
	require.Equal(t, 1, code, "restore with no --hold after a prepare entry: %s%s", stdout, stderr)
	require.Contains(t, stderr, "a prepare row of hold "+id)
	require.Contains(t, stderr, "hold restore --hold "+id)
	require.Contains(t, stderr, "a released row after it names that hold", "the broker's message is not printed")
	_, err := h.s.Records.Row(context.Background())
	require.Error(t, err, "the refused restore wrote a row")

	code, _, stderr = h.run(actionRestore, "--hold", id, "--reason", "the prepared hold named")
	require.Zero(t, code, "restore naming the prepared hold: %s", stderr)
	require.Equal(t, id, h.row().HoldID)
}

// `hold restore` with no --hold after a held entry of a hold that has not ended, which the broker
// refuses 400 bad_transition (a released row then would free every client mid-hold), says the
// broker refused a released restore while a hold is held, names the route's answer and its
// message, which since #4679 round 8 names the hold's id when the route admits the held restore
// ("restore the row held with its hold id <id>"), says where the held hold's id was printed
// (`hold prepare` and `hold set`; no route lists the log, and `hold status` reads the lost row
// first) and `hold restore --hold`, and writes nothing. `hold prepare` printed that id, and the
// restore given it brings the row back held. Controls: 870ce0c500, whose line dropped the route's
// answer; 6adfc29d5e, whose line said neither what was refused nor where the id is; and
// 449251ffc3, which printed no message, so no id.
func TestHoldRestoreWithNoHoldAfterAHeldEntryNamesTheRoutesAnswer(t *testing.T) {
	h := newHeldStack(t)
	code, prepared, _ := h.run(actionPrepare, "--workflow", string(holdrecord.WorkflowBucketsCreate), "--reason", "the row's hold")
	require.Zero(t, code)
	id := h.row().HoldID
	require.Contains(t, prepared, id, "hold prepare did not print the hold's id")
	h.heldWithoutTheSteps(id)
	h.deleteRow()

	code, stdout, stderr := h.run(actionStatus)
	require.NotZero(t, code, "hold status read a lost row: %s", stdout)

	code, stdout, stderr = h.run(actionRestore, "--reason", "no --hold after a held entry")
	require.Equal(t, 1, code, "restore with no --hold after a held entry: %s%s", stdout, stderr)
	require.Contains(t, stderr, "the broker refused a released restore while a hold is held")
	require.Contains(t, stderr, "400 bad_transition", "the refusal drops the route's answer")
	require.Contains(t, stderr, "`hold prepare` and `hold set` printed", "the refusal does not say where the hold's id is")
	require.Contains(t, stderr, "hold restore --hold")
	require.Contains(t, stderr, "restore the row held with its hold id "+id, "the broker's message, which names the hold, is not printed")
	_, err := h.s.Records.Row(context.Background())
	require.Error(t, err, "the refused restore wrote a row")

	code, _, stderr = h.run(actionRestore, "--hold", id, "--reason", "the id hold prepare printed")
	require.Zero(t, code, "restore with the printed id: %s", stderr)
	require.Equal(t, natsgrant.OperatorPhaseHeld, h.row().Phase)
}
