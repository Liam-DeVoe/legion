package writerhold

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
)

// hold abort prints what each stopped command left in the journal's order of the stops, as its
// intent carries them under `left.<the stop's index>` (holdrecord's Entry.Left). A hold whose
// command stopped twelve times puts stops at indexes past 9, which a sort of the keys as strings
// puts out of order ("left.11" before "left.3"). The control is printStopRecords as it stood
// (journalread.go's stopRecords, the keys sorted as strings), which printed the stop at index 11
// before the one at index 3.
func TestHoldAbortPrintsEachStopsRecordInTheJournalsOrder(t *testing.T) {
	d := holdrecord.Done{Workflow: holdrecord.WorkflowBucketsCreate, Ops: []holdrecord.Operation{
		{Command: holdrecord.CommandSet, State: holdrecord.StateIntent},
		{Command: holdrecord.CommandSet, State: holdrecord.StateDone},
	}}
	inputs := map[string]string{}
	var want []string
	for run := range 12 {
		d.Ops = append(d.Ops, holdrecord.Operation{Command: "buckets create", State: holdrecord.StateIntent})
		stop := len(d.Ops)
		left := fmt.Sprintf("run %d left KV_envoy_roles created", run)
		d.Ops = append(d.Ops, holdrecord.Operation{Command: "buckets create", State: holdrecord.StateStop,
			Outputs: map[string]string{"reason": "refused", "left": left}})
		inputs[fmt.Sprintf("left.%d", stop)] = "buckets create: " + left
		want = append(want, "left by a stopped command: buckets create: "+left)
	}
	d.Ops = append(d.Ops, holdrecord.Operation{Command: holdrecord.CommandAbort, State: holdrecord.StateIntent, Inputs: inputs})

	var out bytes.Buffer
	printStopRecords(&out, d)
	require.Equal(t, strings.Join(want, "\n")+"\n", out.String())
}
