package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	configpkg "github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// taskCostLog is three lines of a daemon's log, as its JSON handler writes them: a task's value
// written, its take accepted, and its turn started.
const taskCostLog = `{"time":"2026-11-14T09:00:05.000Z","level":"INFO","msg":"supervise: a task value was written","claim":"legion-infra-infra-2811-tester","task":1,"origin":"phase-start","finished":false,"session":"ses_tester","epoch":"2026-11-14T08:00:00Z","revision":7,"cause":"queued"}
{"time":"2026-11-14T09:00:06.000Z","level":"INFO","msg":"supervise: a take was answered","claim":"legion-infra-infra-2811-tester","task":1,"revision":7,"outcome":"accepted","hold":"","repeat":false}
{"time":"2026-11-14T09:00:40.000Z","level":"INFO","msg":"supervise: a turn started","claim":"legion-infra-infra-2811-tester","generation":1,"task":1}
`

// taskCostPushPathLog is four lines of a daemon from before the task value, as its JSON handler
// wrote them: its start, a turn of no cause, a charge of the push path, which names no wait, and
// that turn's end. It holds no task value written and no take answered.
const taskCostPushPathLog = `{"time":"2026-11-14T09:00:01.000Z","level":"INFO","msg":"legion daemon started","project":"INFRA"}
{"time":"2026-11-14T09:01:00.000Z","level":"INFO","msg":"supervise: a turn started","claim":"legion-infra-infra-2811-tester","generation":1}
{"time":"2026-11-14T09:01:05.000Z","level":"WARN","msg":"supervise: prompt failure","claim":"legion-infra-infra-2811-tester","why":"acknowledged, and no turn started within 5s","promptFailures":1,"limit":3}
{"time":"2026-11-14T09:01:35.000Z","level":"INFO","msg":"supervise: a turn ended","claim":"legion-infra-infra-2811-tester","generation":1}
`

// taskCost runs `legion task-cost` with stdin piped in, as the documentation site's command
// pipes the daemon's log to it.
func taskCost(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	previous := taskCostStdin
	t.Cleanup(func() { taskCostStdin = previous })
	taskCostStdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), append([]string{"legion", "task-cost"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// `legion task-cost` prints what the daemon's own reader answers for the window and the claim it
// is given, and nothing of its own: its output is supervise's reading of the same lines, and its
// exit status is 0, or 1 for a reading the reader says holds no evidence (a claim no line names).
func TestTaskCostPrintsTheDaemonsOwnReading(t *testing.T) {
	for _, args := range [][]string{
		{"--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z"},
		{"--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z", "--claim", "legion-infra-infra-2811-tester"},
		{"--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z", "--claim", "legion-infra-infra-2811-implementer"},
	} {
		parsed, err := supervise.ParseTaskCostArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		want, err := supervise.ReadTaskCost(strings.NewReader(taskCostLog), parsed)
		if err != nil {
			t.Fatal(err)
		}
		wantCode := 0
		if want.NoEvidence() {
			wantCode = 1
		}
		code, stdout, stderr := taskCost(t, taskCostLog, args...)
		if code != wantCode || stderr != "" || stdout != want.Text() {
			t.Errorf("legion task-cost %q = %d, stdout %q, stderr %q; want %d and the reading %q", args, code, stdout, stderr, wantCode, want.Text())
		}
	}
	if _, stdout, _ := taskCost(t, taskCostLog, "--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z"); !strings.Contains(stdout, "turns started: 1 (by a task number 1, with no cause 0)\n") {
		t.Errorf("legion task-cost over the three lines printed %q, want the turn its task started", stdout)
	}
}

// A window that holds no evidence of the take model, of the claim asked for, is judged by nothing:
// one that holds no daemon line — an empty standard input, as a wrong kubectl selector pipes, a
// window the log does not reach, a claim no line names — and one whose daemon lines hold no task
// value written and no take answered, as the log of a daemon from before the task value does.
// `legion task-cost` prints the reading, with the line that says why it judges nothing and each of
// its three verdicts as "no evidence", none as a pass, and exits 1, so a check reading the command
// cannot take a reading of nothing, or of a daemon the change has not reached, for three passes.
// Exit 2 stays the usage error's, for arguments the command cannot take.
//
// The negative control is the window that holds the three lines: the command exits 0, and its
// verdicts are judged, each a pass.
func TestTaskCostExitsOneWhenTheWindowHoldsNoDaemonLine(t *testing.T) {
	const window = "daemon lines in the window: 0\n"
	for _, tc := range []struct {
		name, stdin string
		args        []string
		// says is what the output says of the window's lines.
		says string
	}{
		{"an empty standard input", "", []string{"--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z"}, window},
		{"a window the log does not reach", taskCostLog, []string{"--from", "2026-11-13T09:00:00Z", "--to", "2026-11-13T10:00:00Z"}, window},
		{"a claim no line names", taskCostLog, []string{"--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z",
			"--claim", "legion-infra-infra-2811-implementer"}, window},
		{"daemon lines with no task value written and no take answered", taskCostPushPathLog,
			[]string{"--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z"},
			"daemon lines in the window: 4, the first at 2026-11-14T09:00:01.000Z, the last at 2026-11-14T09:01:35.000Z\n" +
				"task values written and takes answered in the window: 0, so no verdict is judged\n"},
	} {
		code, stdout, stderr := taskCost(t, tc.stdin, tc.args...)
		if code != 1 || stderr != "" || !strings.Contains(stdout, tc.says) ||
			strings.Count(stdout, ": no evidence\n") != 3 || strings.Contains(stdout, ": pass\n") {
			t.Errorf("%s: legion task-cost %q = %d, stdout %q, stderr %q; want 1, %q, and each of the three verdicts %q, none a pass",
				tc.name, tc.args, code, stdout, stderr, tc.says, "no evidence")
		}
	}
	code, stdout, stderr := taskCost(t, taskCostLog, "--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z")
	if code != 0 || stderr != "" || strings.Contains(stdout, "no evidence") || strings.Count(stdout, ": pass\n") != 3 {
		t.Errorf("legion task-cost over the three lines = %d, stdout %q, stderr %q; want 0 and its three verdicts judged, each a pass", code, stdout, stderr)
	}
}

// Arguments the reading cannot take are a usage error, exit 2, naming what is wrong and the
// command's help, and no reading is printed; a help flag prints the usage on stdout and exits 0
// without reading the log.
func TestTaskCostRefusesArgumentsItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--from", "2026-11-14T09:00:00Z"}, "--to is required"},
		{[]string{"--from", "an hour ago", "--to", "2026-11-14T10:00:00Z"}, `--from "an hour ago" is not an RFC 3339 time`},
		{[]string{"--from", "2026-11-14T10:00:00Z", "--to", "2026-11-14T09:00:00Z"}, "is not after --from"},
		{[]string{"--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z", "daemon.log"}, `unexpected argument "daemon.log"`},
		{[]string{"--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z", "--take-timeout", "0"}, "--take-timeout 0 is not a positive number of seconds"},
		{[]string{"--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z", "--since", "30m"}, "unknown flag: --since"},
	} {
		code, stdout, stderr := taskCost(t, taskCostLog, tc.args...)
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "legion task-cost: ") || !strings.Contains(stderr, tc.want) ||
			!strings.HasSuffix(stderr, "Run 'legion task-cost --help' for usage.\n") {
			t.Errorf("legion task-cost %q = %d, stdout %q, stderr %q; want 2 naming %q and the command's help", tc.args, code, stdout, stderr, tc.want)
		}
	}
	code, stdout, stderr := taskCost(t, "", "--help")
	if code != 0 || stderr != "" ||
		!strings.Contains(stdout, "legion task-cost --from <RFC 3339> --to <RFC 3339> [--claim <token>] [--take-timeout <seconds>]") ||
		!strings.Contains(stdout, "kubectl -n legion logs deploy/legion-<project>-daemon -c legion | legion task-cost") ||
		strings.Contains(stdout, "--since-time") {
		t.Errorf("legion task-cost --help = %d, stdout %q, stderr %q; want 0 and the usage with the documented pipe, which reads the container's whole log", code, stdout, stderr)
	}
	for _, name := range []string{"--from", "--to", "--claim", "--take-timeout"} {
		if !strings.Contains(stdout, "      "+name+" ") {
			t.Errorf("legion task-cost --help lists no %s flag:\n%s", name, stdout)
		}
	}
}

// The first bound the reading judges takes by when --take-timeout is not given is the setting's
// own default: the bound a daemon runs with whose legion.yaml names no
// worker_take_timeout_seconds, as the shipped example names none.
func TestTaskCostTakeTimeoutDefaultsToTheSetting(t *testing.T) {
	defaults, err := configpkg.LoadForValidation(stageShippedExample(t, func(example string) string { return example }), nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := supervise.ParseTaskCostArgs([]string{"--from", "2026-11-14T09:00:00Z", "--to", "2026-11-14T10:00:00Z"})
	if err != nil || parsed.Take != defaults.WorkerTakeTimeout {
		t.Fatalf("legion task-cost with no --take-timeout judges by %s (%v), want worker_take_timeout_seconds's default %s",
			parsed.Take, err, defaults.WorkerTakeTimeout)
	}
}
