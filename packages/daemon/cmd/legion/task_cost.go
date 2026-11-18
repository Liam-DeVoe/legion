package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// taskCostLong is `legion task-cost --help`'s description of the reading.
const taskCostLong = `Reads the daemon's JSON log on standard input, the current container's log from its start, and
prints what the window from --from (included) to --to (not included) says task delivery cost: the
turns by cause, the turns each number took, the numbers written by origin, written to a claim
whose phase waited, taken, owed and reached by no take within the first bound of the daemon's
first wait, and finished, the charges by wait, the retirements, and three verdicts. The lines
before --from say only where each claim stands when the window opens: every figure counts the
window alone. --claim reads one claim alone. --take-timeout is the deployment's first bound,
worker_take_timeout_seconds (300 when not given), which the log does not always name. Every figure
is a lower bound but the owed numbers and the turns each number took, which can also count too
many (the page says when): the log reaches back no further than the current container (--previous
reads the one before), and only as far as the node kept it. A window that holds no daemon line,
of the claim asked for, or daemon lines but no task value written and no take answered (the log
of a daemon from before the task value, of one not yet rolled out, or of an older container
--previous reads), prints each verdict as "no evidence" and exits 1.`

// taskCostStdin is where `legion task-cost` reads the daemon's log: the process's own standard
// input, which a test replaces.
var taskCostStdin io.Reader = os.Stdin

// newTaskCostCommand is `legion task-cost`, the documentation site's cost reading
// (docs/site/src/content/docs/legion/task-delivery-cost.md): it takes the flags the daemon's own
// reader defines (supervise.AddTaskCostFlags), reads the log with that reader
// (supervise.ReadTaskCost), and prints what the reader answers, adding nothing of its own. Its exit
// status is 2 for arguments it cannot take, as every command's usage error is, and 1 where it
// gives no reading a check can pass: a log it could not read, and a window that holds no evidence
// of the take model (TaskCost.NoEvidence) — no daemon line, or daemon lines but no task value
// written and no take answered — whose verdicts it prints as "no evidence". A reading with no
// evidence is a failure of the input, not of the arguments, and a check reading the command must
// never take it for a pass.
func newTaskCostCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "task-cost --from <RFC 3339> --to <RFC 3339> [--claim <token>] [--take-timeout <seconds>]",
		Short:   "read the daemon's JSON log on stdin and print what a window of it says task delivery cost: turns by cause, numbers written, taken and finished, charges, retirements and three verdicts",
		Long:    taskCostLong,
		Example: "  kubectl -n legion logs deploy/legion-<project>-daemon -c legion | legion task-cost --from <from> --to <to>",
		Args:    noArguments,
	}
	given := supervise.AddTaskCostFlags(cmd.Flags())
	cmd.RunE = runs(func(context.Context, []string) int {
		parsed, err := given.Args()
		if err != nil {
			return refuse(cmd, "%v", err)
		}
		cost, err := supervise.ReadTaskCost(taskCostStdin, parsed)
		if err != nil {
			fmt.Fprintf(stderr, "legion task-cost: %v\n", err)
			return 1
		}
		fmt.Fprint(stdout, cost.Text())
		if cost.NoEvidence() {
			return 1
		}
		return 0
	})
	return cmd
}
