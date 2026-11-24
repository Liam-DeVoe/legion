// Package dispatchtool is the `dispatch` tool of the daemon's answer loop: Dispatch's own
// `dispatch` command, the one a Legion pane runs, run for an agent that is not running as that
// agent's own session, with the Dispatch token the daemon gives the agent's pane or pod, so
// Dispatch answers each call as it answers the running agent. Per answer, Open makes the empty
// HOME and the state directory every call runs with and Close removes them; Run runs one call the
// model makes.
//
// A call runs only when its command is one of the twenty the tool runs (ReadCommands and
// WriteCommands, checked before any process starts), it names no file of the daemon's host, and
// its answer has made fewer than answers.bounds.gh_calls dispatch calls, counted apart from its gh
// calls. What it posts carries the loop's mark (Mark), and it runs under an environment of nothing
// of the daemon's own, a time bound and an output bound, recorded before it runs and finished
// after (answer_tool_calls, its tool dispatch).
package dispatchtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghtool"
	"github.com/sjawhar/legion/daemon/internal/procgroup"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// Recorder is where every call is recorded (answer_tool_calls): started before it runs and
// finished after. *store.Store is the daemon's.
type Recorder interface {
	StartAnswerToolCall(ctx context.Context, call store.AnswerToolCall) error
	FinishAnswerToolCall(ctx context.Context, question string, seq int, result string, exit *int, at time.Time) error
}

// Host is what every answer's calls run with. Dispatch is the dispatch command the daemon found at
// boot, absolute, which may be a script: the worker image's is a /bin/sh shim that execs bun on
// the bundled CLI. BunDir is bun's directory, the one directory beside /bin on a call's PATH. URL
// is the daemon's dispatch_url and TokenFile its dispatch_token_file, absolute: the token the
// daemon gives the agent's pane or pod, read by the CLI and never by this package. StateDir is
// the daemon's state directory, under whose answers/ each answer's directory lives. Bounds are
// answers.bounds, whose gh_seconds, gh_output_chars and gh_calls bound each call as they bound a
// gh call. Calls records each call, Log takes its line, Now is the clock.
type Host struct {
	Dispatch  string
	BunDir    string
	URL       string
	TokenFile string
	StateDir  string
	Bounds    config.AnswerBounds
	Calls     Recorder
	Log       *slog.Logger
	Now       func() time.Time
}

// Answer is one answer's dispatch environment, Open's: the session every call declares, the
// question, and the empty HOME and the state directory a call runs with. Close removes it.
type Answer struct {
	host     Host
	session  string
	question string
	// dir is <state_dir>/answers/<question>/dispatch, everything below it the answer's own.
	dir, home, state string
}

// Open builds question's dispatch environment for the agent whose session is session, once,
// before its first call: a fresh 0700 directory <state_dir>/answers/<question>/dispatch (whatever
// an earlier run of the same question left there removed first) holding the empty HOME and the
// Dispatch state directory every call runs with.
func Open(host Host, session, question string) (*Answer, error) {
	switch {
	case !filepath.IsAbs(host.Dispatch):
		return nil, fmt.Errorf("the dispatch tool's dispatch %q is not an absolute path", host.Dispatch)
	case !filepath.IsAbs(host.BunDir):
		return nil, fmt.Errorf("the dispatch tool's bun directory %q is not an absolute path", host.BunDir)
	case !filepath.IsAbs(host.TokenFile):
		return nil, fmt.Errorf("the dispatch tool's token file %q is not an absolute path", host.TokenFile)
	case host.URL == "" || host.StateDir == "" || host.Calls == nil || host.Log == nil || host.Now == nil:
		return nil, errors.New("the dispatch tool's host lacks its Dispatch URL, state directory, recorder, log or clock")
	case session == "":
		return nil, fmt.Errorf("the dispatch tool for %s names no session to run as", question)
	}
	name := url.PathEscape(question)
	if question == "" || name == "." || name == ".." {
		return nil, fmt.Errorf("the dispatch tool: %q is no question id a directory can be named for", question)
	}
	dir := filepath.Join(host.StateDir, "answers", name, "dispatch")
	a := &Answer{host: host, session: session, question: question, dir: dir, home: filepath.Join(dir, "home"), state: filepath.Join(dir, "state")}
	if err := os.RemoveAll(a.dir); err != nil {
		return nil, fmt.Errorf("the dispatch tool for %s: clear %s: %w", question, a.dir, err)
	}
	for _, d := range []string{a.home, a.state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			_ = os.RemoveAll(a.dir)
			return nil, fmt.Errorf("the dispatch tool for %s: %w", question, err)
		}
	}
	if err := os.Chmod(a.dir, 0o700); err != nil {
		_ = os.RemoveAll(a.dir)
		return nil, fmt.Errorf("the dispatch tool for %s: %w", question, err)
	}
	return a, nil
}

// Close removes the answer's directory, its HOME and its Dispatch state with it.
func (a *Answer) Close() error {
	if err := os.RemoveAll(a.dir); err != nil {
		return fmt.Errorf("the dispatch tool for %s: remove %s: %w", a.question, a.dir, err)
	}
	return nil
}

// Call is one dispatch call the model makes in an answer: its seq among the answer's calls of
// every tool, from 1, which its record is kept under and its mark names; its number among the
// answer's dispatch calls alone, from 1, an earlier run's counted, which the gh_calls bound is
// read against, so the answer's gh calls spend none of it; its arguments as the model gave them
// (a JSON array of strings, dispatch's arguments without `dispatch`; no shell reads them); and its
// standard input, the one input a `--<field>-file -` reads.
type Call struct {
	Seq   int
	N     int
	Args  string
	Stdin string
}

// Result is what the model reads of a call: dispatch's output, stdout and stderr interleaved and
// cut to the bound with a note saying how much was cut, or the refusal; dispatch's exit status, -1
// when it never ran or was stopped by a signal; and whether it ran.
type Result struct {
	Output string
	Exit   int
	Ran    bool
}

// Run runs call. It is refused, with dispatch never started, when its arguments are no JSON array
// of strings, its number among the answer's dispatch calls is past the gh_calls bound, its command
// is none of the twenty the tool runs, or it names a file of the daemon's host (refusal). Every
// other call runs with the loop's mark added to the body it posts (withMark), the answer's
// environment (env), its HOME the working directory, the gh_seconds bound and the gh_output_chars
// bound. Every call is recorded with what the model reads of it: an allowed one before dispatch
// starts and finished after it ends, a refused one with its refusal; one whose start cannot be
// recorded does not run. Each logs one `answers: dispatch` line.
func (a *Answer) Run(ctx context.Context, call Call) Result {
	started := a.host.Now()
	argv, err := decodeArgs(call.Args)
	if err != nil {
		return a.refuse(ctx, call.Seq, []string{call.Args}, started, err.Error())
	}
	bounds := a.host.Bounds
	switch {
	case call.Seq < 1 || call.N < 1:
		return a.refuse(ctx, call.Seq, argv, started, fmt.Sprintf("call %d is no call of the answer: calls are numbered from 1", call.Seq))
	case bounds.GHCall <= 0 || bounds.GHOutputChars <= 0:
		return a.refuse(ctx, call.Seq, argv, started, "the dispatch tool has no time or output bound (answers.bounds), so it runs nothing")
	case call.N > bounds.GHCalls:
		return a.refuse(ctx, call.Seq, argv, started, fmt.Sprintf("this answer has made its %d dispatch calls (answers.bounds.gh_calls, counted apart from its gh calls); answer with what the calls so far found", bounds.GHCalls))
	}
	if why := refusal(argv); why != "" {
		return a.refuse(ctx, call.Seq, argv, started, why)
	}
	run, stdin := withMark(argv, call.Stdin, Mark(a.question, call.Seq))
	if err := a.host.Calls.StartAnswerToolCall(ctx, a.record(call.Seq, run, started)); err != nil {
		result := Result{Output: fmt.Sprintf("refused: the call could not be recorded, so it did not run: %v", err), Exit: -1}
		a.log(call.Seq, run, result)
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, bounds.GHCall)
	defer cancel()
	result := a.runDispatch(ctx, run, stdin)
	var exit *int
	if result.Ran {
		exit = &result.Exit
	}
	// The call ran: its record is finished whatever the answer's own deadline did meanwhile.
	if err := a.host.Calls.FinishAnswerToolCall(context.WithoutCancel(ctx), a.question, call.Seq, result.Output, exit, a.host.Now()); err != nil {
		a.host.Log.Error("answers: dispatch call not finished in its record", "question", a.question, "seq", call.Seq, "error", err)
	}
	a.log(call.Seq, run, result)
	return result
}

// env is the whole environment a call's dispatch runs with, and nothing of the daemon's own: the
// five DISPATCH variables, which make the CLI act as the agent's session (DISPATCH_HOST and
// DISPATCH_SESSION_ID) on the daemon's Dispatch with the token the daemon gives the agent's pane
// or pod (DISPATCH_URL and DISPATCH_TOKEN_FILE) and keep its session state in the answer's own
// directory (DISPATCH_STATE_DIR); HOME, the answer's empty directory, so no personal Dispatch
// token or configuration of the daemon's host takes part; and PATH. The architect's list names the
// five DISPATCH variables and HOME. PATH is the shim's need and no access: the worker image's
// dispatch is a /bin/sh script that runs dirname and starts bun by name, so it holds bun's
// directory and /bin and nothing else.
func (a *Answer) env() []string {
	path := a.host.BunDir
	if path != "/bin" {
		path += string(filepath.ListSeparator) + "/bin"
	}
	return []string{
		"PATH=" + path,
		"HOME=" + a.home,
		"DISPATCH_HOST=omp",
		"DISPATCH_SESSION_ID=" + a.session,
		"DISPATCH_URL=" + a.host.URL,
		"DISPATCH_TOKEN_FILE=" + a.host.TokenFile,
		"DISPATCH_STATE_DIR=" + a.state,
	}
}

// runDispatch runs dispatch with argv under the call's environment, in the answer's empty HOME,
// stdin its standard input, its stdout and stderr interleaved and cut at gh_output_chars as a gh
// call's are (ghtool.CappedOutput), and stopped, its whole process group, at ctx's end (the
// gh_seconds bound). A dispatch a signal ended ran and answered no status (procgroup.Ended): what
// it printed stands, with the bound's note where ctx's end stopped it and the signal's words where
// something else did, and its exit is -1.
func (a *Answer) runDispatch(ctx context.Context, argv []string, stdin string) Result {
	cmd := exec.CommandContext(ctx, a.host.Dispatch, argv...)
	cmd.Env, cmd.Dir = a.env(), a.home
	cmd.Stdin = strings.NewReader(stdin)
	procgroup.Configure(cmd)
	out := ghtool.NewCappedOutput(a.host.Bounds.GHOutputChars)
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	end := procgroup.Ended(err)
	if err := procgroup.Err(err); err != nil && end.Signal == "" {
		return Result{Output: fmt.Sprintf("dispatch could not run: %v", err), Exit: -1}
	}
	output := out.String()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		output += fmt.Sprintf("\n[stopped: dispatch ran past its %s bound (answers.bounds.gh_seconds)]", a.host.Bounds.GHCall)
	case end.Signal != "":
		output += fmt.Sprintf("\n[dispatch was ended by %s]", end.Signal)
	}
	return Result{Output: output, Exit: cmd.ProcessState.ExitCode(), Ran: true}
}

// decodeArgs reads the model's arguments, a JSON array of strings.
func decodeArgs(raw string) ([]string, error) {
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil || argv == nil {
		return nil, fmt.Errorf("refused: the dispatch tool takes dispatch's arguments as a JSON array of strings, without `dispatch` (%q is none)", raw)
	}
	return argv, nil
}

// refuse records and logs a call refused before dispatch ran it, and answers its refusal.
func (a *Answer) refuse(ctx context.Context, seq int, argv []string, started time.Time, why string) Result {
	if !strings.HasPrefix(why, refusedPrefix) {
		why = refusedPrefix + why
	}
	result := Result{Output: why, Exit: -1}
	ctx = context.WithoutCancel(ctx)
	switch err := a.host.Calls.StartAnswerToolCall(ctx, a.record(seq, argv, started)); {
	case err != nil:
		a.host.Log.Error("answers: refused dispatch call not recorded", "question", a.question, "seq", seq, "error", err)
	default:
		if err := a.host.Calls.FinishAnswerToolCall(ctx, a.question, seq, result.Output, nil, a.host.Now()); err != nil {
			a.host.Log.Error("answers: refused dispatch call not finished in its record", "question", a.question, "seq", seq, "error", err)
		}
	}
	a.log(seq, argv, result)
	return result
}

// refusedPrefix opens every refusal the tool answers and records, which Writes tells a call the
// tool refused before dispatch started by.
const refusedPrefix = "refused: "

// record is the row a call starts with: the dispatch tool's, run as no GitHub App and with no
// owner's gh directory.
func (a *Answer) record(seq int, argv []string, started time.Time) store.AnswerToolCall {
	return store.AnswerToolCall{Question: a.question, Seq: seq, Tool: store.ToolDispatch, Args: argv, StartedAt: started}
}

// log is the call's one line: the question and seq, the session it ran as, its command and its
// arguments as run, and its exit (-1 when dispatch never ran), and for a refused call its refusal.
// The token is in no argument and no line: the CLI reads it from the file DISPATCH_TOKEN_FILE
// names.
func (a *Answer) log(seq int, argv []string, result Result) {
	command := ""
	if len(argv) > 0 {
		command = argv[0]
	}
	attrs := []any{"question", a.question, "seq", seq, "session", a.session, "command", command, "args", argv, "exit", result.Exit, "ran", result.Ran}
	if !result.Ran {
		attrs = append(attrs, "refused", result.Output)
	}
	a.host.Log.Info("answers: dispatch", attrs...)
}
