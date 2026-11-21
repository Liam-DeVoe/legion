package writerhold

import (
	"bytes"
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// The reconcile check (reconcileStreamIDs, which the census and hub-restore's abort run on their
// writer's result) refuses a store the writer marks gone at an instance other than the one the
// operator's --daemon-database file states: a store is gone only at the address its Deployment and
// the operator's file both name, so a 3D000 from another server is no evidence that the live store
// is gone. It is UNKNOWN naming the store and both instances, and the result records nothing. This
// is a check beside the writer's own (consumernames.ReconcileDaemonStreamIDs refuses that store
// before it connects). The control is the same store gone at the instance the file states, which
// is accepted and recorded as gone.
func TestHoldAGoneStoreAtAnotherInstanceIsRefused(t *testing.T) {
	gone := func(instance, stated string) StreamIDWriter {
		return func(context.Context, *nats.Conn, DaemonDeployments, []string, []string) (StreamIDResult, error) {
			return StreamIDResult{Gone: []GoneStore{{Project: "P", Database: "daemon_p", Instance: instance, StatedInstance: stated,
				Answer: `FATAL: database "daemon_p" does not exist (SQLSTATE 3D000)`}}}, nil
		}
	}
	var out bytes.Buffer
	outputs, err := reconcileStreamIDs(context.Background(), nil, gone("proj:region:inst-p", "proj:region:inst-p"), nil, []string{"daemon/P"}, nil, &out)
	require.NoError(t, err, "control: a store gone at the instance the operator's file states")
	require.Contains(t, outputs, "gone.daemon/P")

	for _, row := range []struct{ name, instance, stated string }{
		{"another instance", "proj:region:inst-p", "proj:region:another"},
		{"no instance named", "", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			out.Reset()
			outputs, err := reconcileStreamIDs(context.Background(), nil, gone(row.instance, row.stated), nil, []string{"daemon/P"}, nil, &out)
			var unknown *Unknown
			require.ErrorAs(t, err, &unknown, "a store gone at an instance the operator's file does not state")
			require.ErrorContains(t, err, "daemon/P")
			require.Nil(t, outputs)
			require.NotContains(t, out.String(), "GONE daemon/P", "the refused store was printed gone")
		})
	}
}
