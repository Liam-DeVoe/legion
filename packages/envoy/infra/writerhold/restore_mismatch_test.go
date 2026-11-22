package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
)

// `hold restore` with no --hold after a prepare entry, which the broker refuses 409 hold_mismatch
// (a released row after a prepare entry must name that entry's hold), says so and names the hold
// the route names (RouteError.HoldID, since no route lists the log), with nothing written. Controls:
// restore at 702c7749de, which printed the route's raw error, and at 870ce0c500, which named no id
// ("hold restore --hold <its hold id>").
func TestHoldRestoreWithNoHoldAfterAPrepareNamesTheHold(t *testing.T) {
	h := newHeldStack(t)
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	h.deleteRow()

	code, stdout, stderr := h.run(actionRestore, "--reason", "no --hold after a prepare")
	require.Equal(t, 1, code, "restore with no --hold after a prepare entry: %s%s", stdout, stderr)
	require.Contains(t, stderr, "a prepare row of hold "+id)
	require.Contains(t, stderr, "hold restore --hold "+id)
	_, err := h.s.Records.Row(context.Background())
	require.Error(t, err, "the refused restore wrote a row")

	code, _, stderr = h.run(actionRestore, "--hold", id, "--reason", "the prepared hold named")
	require.Zero(t, code, "restore naming the prepared hold: %s", stderr)
	require.Equal(t, id, h.row().HoldID)
}

// `hold restore` with no --hold after a held entry of a hold that has not ended, which the broker
// refuses 400 bad_transition (a released row then would free every client mid-hold), says so,
// names the route's answer and `hold restore --hold`, and writes nothing. The route's refusal
// carries no hold id for this code, so the line cannot name the hold. Control: 870ce0c500, whose
// line dropped the route's answer.
func TestHoldRestoreWithNoHoldAfterAHeldEntryNamesTheRoutesAnswer(t *testing.T) {
	h := newHeldStack(t)
	id := h.prepared(holdrecord.WorkflowBucketsCreate)
	h.heldWithoutTheSteps(id)
	h.deleteRow()

	code, stdout, stderr := h.run(actionRestore, "--reason", "no --hold after a held entry")
	require.Equal(t, 1, code, "restore with no --hold after a held entry: %s%s", stdout, stderr)
	require.Contains(t, stderr, "a held row")
	require.Contains(t, stderr, "400 bad_transition", "the refusal drops the route's answer")
	require.Contains(t, stderr, "hold restore --hold")
	_, err := h.s.Records.Row(context.Background())
	require.Error(t, err, "the refused restore wrote a row")
}
