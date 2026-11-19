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
reads the one before), and only as far as the node kept it. A window that holds, of the claim
asked for, a daemon line and a task value written or a take answered judges all three verdicts,
each a pass or a fail. In any other window that holds a daemon line of the claim asked for, a fail
proved from where a claim stood at --from (a number whose first bound passed in the window with no
take, or a charge in the window of a worker whose turn began within the second bound after a take
accepted before --from) is evidence for its own verdict alone, and each verdict no such fail fails
prints "no evidence": a pass carried into the window is no evidence. A window with no daemon line
of the claim asked for judges no verdict, and neither does the log of a daemon from before the
task value, of one not yet rolled out, or of an older container --previous reads. The command
exits 1 when any verdict prints "no evidence", and 0 when it judges all three, a fail included.`

// taskCostStdin is where `legion task-cost` reads the daemon's log: the process's own standard
// input, which a test replaces.
var taskCostStdin io.Reader = os.Stdin

// newTaskCostCommand is `legion task-cost`, the documentation site's cost reading
// (docs/site/src/content/docs/legion/task-delivery-cost.md): it takes the flags the daemon's own
// reader defines (supervise.AddTaskCostFlags), reads the log with that reader
// (supervise.ReadTaskCost), and prints what the reader answers, adding nothing of its own. Its exit
// status is 2 for arguments it cannot take, as every command's usage error is, and 1 where it
// gives no reading a check can pass: a log it could not read, and a window that does not judge
// every verdict (TaskCost.EveryVerdictJudged), where one prints "no evidence" — a window with no
// daemon line, or with daemon lines but no task value written and no take answered, whose only
// evidence, if any, is a fail proved from the state carried into the window, which judges its own
// verdict alone. A verdict with no evidence is a failure of the input, not of the arguments, and a
// check reading the command must never take it for a pass. A window that judges every verdict
// exits 0, a fail included: the exit says whether the window could be judged, and the verdict
// lines say what was found.
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
		if !cost.EveryVerdictJudged() {
			return 1
		}
		return 0
	})
	return cmd
}
