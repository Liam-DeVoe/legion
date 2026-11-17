package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sjawhar/legion/daemon/internal/supervise"
)

const taskCostUsage = `usage: legion task-cost --from <RFC 3339> --to <RFC 3339> [--claim <token>] [--take-timeout <seconds>]

Reads the daemon's JSON log on standard input and prints what the window from --from (included)
to --to (not included) says task delivery cost: the turns by cause, the turns each number took,
the numbers written by origin, written to a claim whose phase waited, taken, owed and reached by
no take within the first bound of the daemon's first wait, and finished, the charges by wait, the
retirements, and three verdicts. --claim reads one claim alone. --take-timeout is the
deployment's first bound, worker_take_timeout_seconds (300 when not given), which the log does not
always name. Every figure is a lower bound but the owed numbers, which can also count too many
(the page says when): the log reaches back only as far as the node kept it.

  kubectl -n legion logs deploy/legion-<project>-daemon -c legion --since-time <from> | legion task-cost --from <from> --to <to>`

// taskCostStdin is where `legion task-cost` reads the daemon's log: the process's own standard
// input, which a test replaces.
var taskCostStdin io.Reader = os.Stdin

// runTaskCost is `legion task-cost`, the documentation site's cost reading
// (docs/site/src/content/docs/legion/task-delivery-cost.md): it parses its arguments and reads the
// log with the daemon's own reader (supervise.ParseTaskCostArgs, supervise.ReadTaskCost), and
// prints what that reader answers, adding nothing of its own.
func runTaskCost(_ context.Context, args []string, stdout, stderr io.Writer) int {
	parsed, err := supervise.ParseTaskCostArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stderr, taskCostUsage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "legion task-cost: %v\n%s\n", err, taskCostUsage)
		return 2
	}
	cost, err := supervise.ReadTaskCost(taskCostStdin, parsed)
	if err != nil {
		fmt.Fprintf(stderr, "legion task-cost: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, cost.Text())
	return 0
}
