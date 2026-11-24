package dispatchtool

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/ghtool"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// ReadCommands are the dispatch commands the tool runs that write nothing, as the CLI's own help
// says of each (`dispatch --help`, packages/contracts/src/dispatch-tools.ts): with them the agent
// reads any issue, its documents, comments, asks and messages, those written after it stopped
// included, and its own session's answers, limits, steps, plans, results and inbox.
var ReadCommands = []string{
	"read", "doc-read", "search", "related", "issues", "projects", "priorities", "open-asks",
	"answers", "whoami", "limits", "handoff-read", "steps", "plans", "results", "inbox",
}

// WriteCommands are the dispatch commands the tool runs that write: replies in the agent's name,
// and the resolution of a comment thread. Every other write — a status, a claim, an ask, a
// document edit, a handoff, a review, an issue — changes an issue, its lifecycle, its documents or
// its design gate, and Legion reads several of them as a person's move when the writer holds no
// claim in the tree, which a stopped agent's session does not.
var WriteCommands = []string{"comment", "message", "send-to-agent", "resolve-comment"}

// marked are the write commands whose body the loop's mark is added to (withMark).
var marked = []string{"comment", "message", "send-to-agent"}

// commandRefusal is the reason a command outside ReadCommands and WriteCommands is refused, which
// the model reads and its reply says.
const commandRefusal = "the daemon runs only these dispatch commands for a stopped agent: the reads %s; and the writes %s. " +
	"Your session holds no claim now, so a status write, a claim, an ask or a document edit would stand as a person's move, " +
	"and the daemon writes only replies in your name. A request for such a write is work your tools cannot do now: say so in your reply."

// fileRefusal is the reason an option that hands dispatch a file of the daemon's host is refused.
const fileRefusal = "`%s` hands dispatch a file of the daemon's host, and the daemon hands it none: " +
	"give the text as the call's stdin and write `--<field>-file -`, which reads it."

// refusal is the reason argv is refused before dispatch starts, "" for none: its command, the
// first argument, is none of ReadCommands and WriteCommands, a command the CLI adds later among
// them, since the tool keeps the list of what it runs and none of what it refuses; or, for every
// command, an option names a file of the host: a `--<field>-file` or `--<field>-json-file` whose
// value is not `-`, in either of the forms the CLI reads (`--x-file v` and `--x-file=v`), or a
// picture to upload (`--image v`, `--image=v`). The CLI reads every argument that starts with `--`
// as an option, and an option's value as the rest of its word after `=` or else as the next
// argument unless that one starts with `--`, so the options are read here as it reads them.
func refusal(argv []string) string {
	if len(argv) == 0 || !slices.Contains(ReadCommands, argv[0]) && !slices.Contains(WriteCommands, argv[0]) {
		command := "dispatch"
		if len(argv) > 0 {
			command += " " + argv[0]
		}
		return fmt.Sprintf("`%s` is none of the commands this tool runs. ", command) +
			fmt.Sprintf(commandRefusal, strings.Join(ReadCommands, ", "), strings.Join(WriteCommands, ", "))
	}
	for i := 1; i < len(argv); i++ {
		name, value, ok, inline := option(argv, i)
		if name == "" {
			continue
		}
		switch {
		case name == "--image":
			return fmt.Sprintf(fileRefusal, argv[i])
		case strings.HasSuffix(name, "-file") && (!ok || value != "-"):
			given := argv[i]
			if ok && !inline {
				given += " " + value
			}
			return fmt.Sprintf(fileRefusal, given)
		}
	}
	return ""
}

// option reads argv[i] as the CLI reads an option: its name, "" for an argument that is none, and
// its value, the rest of its word after `=`, else the next argument unless that one is an option
// too, with whether it has one and whether that value is the rest of its word (inline).
func option(argv []string, i int) (name, value string, ok, inline bool) {
	if !strings.HasPrefix(argv[i], "--") {
		return "", "", false, false
	}
	if name, value, ok := strings.Cut(argv[i], "="); ok {
		return name, value, true, true
	}
	if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
		return argv[i], argv[i+1], true, false
	}
	return argv[i], "", false, false
}

// Mark is what the tool adds to the body of each comment, message and agent's message a call
// posts: the loop's marker (intake.AnswerMarker) naming the call, <question>.<seq>, the answer key
// a gh call's Legion footer names, after a blank line, so the loop answers no message its own tool
// posted (answerloop's carriesLoopMark), and so its own reply's marker, <question> and no more,
// which the loop reads a thread for before posting there again, is never this one.
func Mark(question string, seq int) string {
	return fmt.Sprintf("\n\n%s%s.%d -->", intake.AnswerMarker, question, seq)
}

// withMark is argv and stdin as dispatch runs them: for a comment, a message or an agent's message,
// mark added to its body wherever the body comes from, the value of `--body` (or `--body=`), or
// stdin where the body's option is `--body-file -` (or `--body-file=-`); every other call as given.
func withMark(argv []string, stdin, mark string) ([]string, string) {
	if !slices.Contains(marked, argv[0]) {
		return argv, stdin
	}
	run := slices.Clone(argv)
	for i := 1; i < len(run); i++ {
		name, value, ok, inline := option(run, i)
		switch {
		case !ok:
		case name == "--body" && inline:
			run[i] += mark
		case name == "--body":
			run[i+1] += mark
			i++
		case name == "--body-file" && value == "-":
			stdin += mark
		}
	}
	return run, stdin
}

// UnknownOutcome is the result a restart records for a dispatch call the daemon stopped while it
// ran: Dispatch is not read for it, since the loop's mark is no key Dispatch can be searched by,
// so the model is told to read before it writes again.
const UnknownOutcome = "The daemon stopped while this call ran; what it did is unknown. Read Dispatch before you repeat a write."

// Write is one dispatch call that writes which the answer loop ran for a question (Writes): its
// seq among the question's calls, its command, and dispatch's exit status, nil where dispatch
// answered none, since it could not start or the daemon stopped while it ran.
type Write struct {
	Seq     int
	Command string
	Exit    *int
}

// Writes are the dispatch calls of calls, as answer_tool_calls records them, that write and that
// dispatch ran, or may have, in their order: every command but ReadCommands, the CLI's own reads,
// so a command the tool does not know counts as a write, and a call the daemon stopped during
// counts too. A call the tool refused before dispatch started, which it records with its refusal
// and no exit, is none, and neither is a call of another tool.
func Writes(calls []store.AnswerToolCall) []Write {
	var written []Write
	for _, call := range calls {
		if call.Tool != store.ToolDispatch || call.RefusedBeforeRun() {
			continue
		}
		command := ""
		if len(call.Args) > 0 {
			command = call.Args[0]
		}
		if slices.Contains(ReadCommands, command) {
			continue
		}
		written = append(written, Write{Seq: call.Seq, Command: command, Exit: call.Exit})
	}
	return written
}

// CallWrites says whether a call the model asks for, by args, its arguments as Call carries them
// (a JSON array of strings), runs one of WriteCommands, the four writes the tool runs. Every other
// call writes nothing: a read, and a command the tool does not run or arguments that are no such
// array, which Run refuses before dispatch starts. The answer refuses each call that writes while
// the project is paused (internal/answer's toolSet).
func CallWrites(args string) bool {
	argv, err := ghtool.DecodeArgs(args, "dispatch")
	return err == nil && len(argv) > 0 && slices.Contains(WriteCommands, argv[0])
}
