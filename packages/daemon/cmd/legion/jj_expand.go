package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
)

// This file expands a jj command line as jj expands it, for the tmux form of `legion jj` (runJJ):
// the command word as clap finds it (jjCommandWord), jj's built-in commands (jjBuiltins), the one
// read of every alias and of ui.default-command (readJJAliases), and the expansion (expandJJ). It
// is a port of the pod's wrapper's reading of a line (packages/daemon/docker/jj), which expands
// the line itself before it hands legion jj a push, so the --real form expands nothing.

// jjBuiltins are the commands jj has built in, by clap's names: each command's name and every
// other name clap gives it, hidden ones included. They are the 51 names of the pod's wrapper's
// builtins= line (packages/daemon/docker/jj), the names jj 0.45.1-sami warns of where an alias
// has one ("Cannot define an alias that overrides the built-in command"); jj 0.38.0 has each of
// them but arrange and converge. jj ignores an alias of such a name, so a command word among them
// is never expanded, and the expansion needs no read.
var jjBuiltins = []string{
	"abandon", "absorb", "arrange", "bisect", "bookmark", "commit", "config", "converge", "debug", "describe",
	"diff", "diffedit", "duplicate", "edit", "evolog", "evolution-log", "file", "fix", "gerrit", "git",
	"help", "interdiff", "log", "metaedit", "new", "next", "obslog", "op", "operation", "parallelize",
	"prev", "rebase", "redo", "resolve", "restore", "revert", "root", "run", "show", "sign",
	"simplify-parents", "sparse", "split", "squash", "status", "tag", "undo", "unsign", "util", "version",
	"workspace",
}

// jjWordValues are jj's global options whose value is the next word, as clap reads them in front
// of the command word: `jj --help`'s Global Options, the same in jj 0.38.0 and 0.45.1-sami
// (docker/jj's command_word). Each of them but -R takes its value after `=` too, and -R attached
// to it.
var jjWordValues = []string{"-R", "--repository", "--at-operation", "--at-op", "--color", "--config", "--config-file"}

// jjWordFlags are jj's global flags, which clap reads in front of the command word.
var jjWordFlags = []string{"--ignore-working-copy", "--no-integrate-operation", "--ignore-immutable", "--debug", "--quiet", "--no-pager"}

// jjCommandWord is the index in args of jj's command word as clap finds it, the first word that is
// no global option and no option's value, or -1 for none, as the pod's wrapper finds it
// (command_word). A global option's value is the next word (jjWordValues) or is attached to it
// (`-R<path>`, `--config=<value>`), a flag stands alone (jjWordFlags), and clap reads a word that
// starts with -h or -V as help or version (jjHelpWord), and `--` as no command word. An empty word
// and a lone `-` are command words. Any other option in front of the command word is one clap does
// not know, and clap stops there: jj then reads no command word, and puts ui.default-command in
// front of the line, so the answer is -1.
func jjCommandWord(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case slices.Contains(jjWordValues, arg):
			i++
		case slices.Contains(jjWordFlags, arg), arg == "--", jjHelpWord(arg), jjAttachedValue(arg):
		case len(arg) > 1 && arg[0] == '-':
			return -1
		default:
			return i
		}
	}
	return -1
}

// jjAttachedValue is whether arg is a global option of jjWordValues with its value in the same
// word: `-R<path>`, `-R=<path>` or `--<name>=<value>`.
func jjAttachedValue(arg string) bool {
	if len(arg) > 2 && strings.HasPrefix(arg, "-R") {
		return true
	}
	return slices.ContainsFunc(jjWordValues[1:], func(name string) bool { return strings.HasPrefix(arg, name+"=") })
}

// jjHelpWord is whether arg is a word clap reads as help or version: --help, --version, or a word
// that starts with -h or -V (`-hV` among them). jj puts no ui.default-command in front of a line
// that holds one and has no command word: measured with jj 0.38.0 and 0.45.1-sami, `jj -h`, `jj
// --help`, `jj -V`, `jj --version` and `jj -hV` print and push nothing with a push as the default
// command.
func jjHelpWord(arg string) bool {
	return arg == "--help" || arg == "--version" || strings.HasPrefix(arg, "-h") || strings.HasPrefix(arg, "-V")
}

// jjCompleting is whether the environment's COMPLETE names a shell: jj then prints shell
// completions and runs no command (clap's CompleteEnv), and a read of its configuration would
// print completions too, as the pod's wrapper notes. Empty or `0` is no completion.
func jjCompleting() bool {
	value := os.Getenv("COMPLETE")
	return value != "" && value != "0"
}

// jjAliasRead is the one read of the configuration that the expansion makes: each alias and
// ui.default-command, a line each, its name and its value in JSON (json()), which jj 0.38.0,
// 0.45.1-sami and 0.46.0 print alike (testdata/jj-config-list). jj reads aliases and
// ui.default-command from its defaults, the user's files and the configuration of the repository
// the working directory is in, and from neither the repository -R names nor a --config or
// --config-file: measured with jj 0.38.0 and 0.45.1-sami, `jj -R <repo> q` is `unrecognized
// subcommand` where only <repo> defines q, and `jj --config aliases.q=… q` warns and refuses q. So
// the read runs in the working directory with none of the role's options, and with
// --include-defaults, since jj's defaults hold aliases of their own (b, ci, desc, st). The read is
// itself a `jj config` command, so it sees an alias that a [[--scope]] with --when.commands =
// ["config"] defines, which jj runs for no other command: such an alias is expanded as the pod's
// wrapper expands it, and a push it names runs with the step where jj alone refuses it (a limit of
// the expansion, D19).
var jjAliasRead = []string{"--ignore-working-copy", "--color=never", "config", "list", "--include-defaults", "-T",
	`if(name.starts_with("aliases.") || name == "ui.default-command", json(name) ++ " " ++ json(value) ++ "\n")`}

// jjOverridesBuiltin is jj's warning of an alias that has the name of a built-in command, which
// every jj command prints on standard error, the read included; its group is the name. jj 0.45.1-sami
// ends the line with a full stop, and jj 0.38.0 does not.
var jjOverridesBuiltin = regexp.MustCompile(`Cannot define an alias that overrides the built-in command '([^']*)'`)

// jjAliases is what the read (jjAliasRead) gives the expansion: values, each value in JSON by its
// name as jj lists it (`aliases.<name>`, `aliases.<name>.definition`, `ui.default-command`);
// builtins, each name that jj warns is a built-in command (jjOverridesBuiltin), whatever
// jjBuiltins holds; and quoted, whether jj lists an alias by a quoted name (`aliases."a.b"`), which
// only a command word that is no bare name (jjBareName) can be.
type jjAliases struct {
	values   map[string]json.RawMessage
	builtins map[string]bool
	quoted   bool
}

// jjUnexpandable is a command line that legion jj cannot expand (expandJJ): what of it, and why.
// runJJ prints its one line, and the command runs as the role wrote it.
type jjUnexpandable struct{ what, why string }

func (u jjUnexpandable) Error() string { return "could not expand " + u.what + ": " + u.why }

// readJJAliases runs the read (jjAliasRead) as each command of the step runs (jjStep.command):
// next, in env, in the working directory, as a child in this process's group, and reads what it
// printed (parseJJAliases). A read that cannot start is jjUnexpandable too.
func readJJAliases(next string, env []string) (jjAliases, error) {
	stdout, stderr, code, err := (&jjStep{next: next}).command(env, jjAliasRead)
	var failed jjStepFailure
	if errors.As(err, &failed) {
		stderr, code = failed.output, failed.code
	}
	return parseJJAliases(stdout, stderr, code)
}

// parseJJAliases reads what the read (jjAliasRead) printed on standard output and standard error,
// and its exit status, code. A read that exited non-zero, a configuration that does not parse among
// them, or whose standard output holds what is no name and value in JSON, is jjUnexpandable.
func parseJJAliases(stdout, stderr string, code int) (jjAliases, error) {
	if code != 0 {
		return jjAliases{}, jjUnexpandable{"the command", "jj config list failed: " + jjErrorLine(stderr, "jj config list", code)}
	}
	a := jjAliases{values: map[string]json.RawMessage{}, builtins: map[string]bool{}}
	for _, warned := range jjOverridesBuiltin.FindAllStringSubmatch(stderr, -1) {
		a.builtins[warned[1]] = true
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	for {
		var name string
		var value json.RawMessage
		err := decoder.Decode(&name)
		if errors.Is(err, io.EOF) {
			return a, nil
		}
		if err == nil {
			err = decoder.Decode(&value)
		}
		if err != nil {
			return jjAliases{}, jjUnexpandable{"the command", "jj config list printed what is no name and value in JSON: " + err.Error()}
		}
		a.values[name] = value
		if rest, ok := strings.CutPrefix(name, "aliases."); ok && (strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, "'")) {
			a.quoted = true
		}
	}
}

// jjBareName is whether word is a TOML bare key, the only name jj lists an alias by unquoted.
func jjBareName(word string) bool {
	return word != "" && strings.Trim(word, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-") == ""
}

// defaultCommand is ui.default-command as jj reads it: a list of strings, or one string, which is
// one command word, or `log` where nothing sets it.
func (a jjAliases) defaultCommand() ([]string, error) {
	value, set := a.values["ui.default-command"]
	if !set {
		return []string{"log"}, nil
	}
	var word string
	if json.Unmarshal(value, &word) == nil {
		return []string{word}, nil
	}
	var words []string
	if json.Unmarshal(value, &words) != nil {
		return nil, jjUnexpandable{"ui.default-command", fmt.Sprintf("it is %s, which is neither a string nor a list of strings", value)}
	}
	return words, nil
}

// alias is the definition of the alias that word names, and whether one does. An alias is a list
// of strings, `aliases.<word> = […]`, or a table whose definition is one, which jj 0.45.1-sami
// runs and the pod's wrapper expands; a table with no definition names no alias. A value that is
// no list of strings is jjUnexpandable, as is a table with `enabled = false`, which jj 0.45.1-sami
// runs and jj 0.46.0 refuses. A word that is no bare name (jjBareName) names no alias unless jj
// lists one by a quoted name, which legion jj does not read, and which is jjUnexpandable.
//
// Two limits of the expansion (D19) are here. A table with `enabled = false` runs as the role
// wrote it, so under jj 0.45.1-sami its push goes with no step, on both forms, since the pod's
// wrapper leaves it to jj too. jj 0.38.0 refuses every table-form alias (`Invalid type or value
// for aliases.<word>`), and the expansion takes it as jj 0.45.1-sami does, so under jj 0.38.0 its
// push runs with the step where jj alone refuses it.
func (a jjAliases) alias(word string) ([]string, bool, error) {
	if !jjBareName(word) {
		if a.quoted {
			return nil, false, jjUnexpandable{fmt.Sprintf("the command word %q", word), "it is no bare name, and the configuration names an alias by a quoted name, which legion jj does not read"}
		}
		return nil, false, nil
	}
	name := "aliases." + word
	value, plain := a.values[name]
	if !plain {
		var defined bool
		if value, defined = a.values[name+".definition"]; !defined {
			return nil, false, nil
		}
		if string(a.values[name+".enabled"]) == "false" {
			return nil, false, jjUnexpandable{name, "its enabled is false, which jj 0.45.1-sami ignores and jj 0.46.0 obeys"}
		}
		name += ".definition"
	}
	var words []string
	if json.Unmarshal(value, &words) != nil {
		return nil, false, jjUnexpandable{name, fmt.Sprintf("it is %s, which is no list of strings", value)}
	}
	return words, true, nil
}

// expandJJ is args as jj expands them, where the expansion ends at a built-in command word, which
// readJJPush then reads as it reads a line the role wrote; it is nil where jj runs no built-in
// command, or where legion jj cannot expand args (jjUnexpandable), and the command then runs with
// the role's own words. A line whose command word is a built-in name (jjBuiltins) is args, and a
// line with no command word that holds a word of help or version (jjHelpWord) is nil, each with no
// read. Every other line costs one read through next in env (readJJAliases).
//
// The expansion is jj's: ui.default-command in front of a line with no command word, once; then
// an alias's words in place of its command word, and the alias of an alias the same way. An alias
// whose words hold a command word joins the chain of the line; one whose words are options alone
// (`q = ["--quiet"]`) does not, so `jj q q version` expands, as jj 0.45.1-sami runs it. A name that
// would join the chain twice is a cycle, which jj refuses (`Recursive alias definition involving`),
// and is jjUnexpandable. Each expansion adds a name to the chain or takes a word of the line, so
// the expansion ends. A word that jj warns is a built-in command ends it as one of jjBuiltins does.
// A word that no alias names, and a line that an alias leaves with no command word, end it with no
// built-in command: jj refuses the word, or runs no push.
func expandJJ(args []string, next string, env []string) ([]string, error) {
	at := jjCommandWord(args)
	switch {
	case at < 0 && slices.ContainsFunc(args, jjHelpWord):
		return nil, nil
	case at >= 0 && slices.Contains(jjBuiltins, args[at]):
		return args, nil
	}
	aliases, err := readJJAliases(next, env)
	if err != nil {
		return nil, err
	}
	line := args
	if at < 0 {
		words, err := aliases.defaultCommand()
		if err != nil {
			return nil, err
		}
		line = slices.Concat(words, args)
	}
	chain := map[string]bool{}
	for {
		at = jjCommandWord(line)
		if at < 0 {
			return nil, nil
		}
		word := line[at]
		if slices.Contains(jjBuiltins, word) || aliases.builtins[word] {
			return line, nil
		}
		words, ok, err := aliases.alias(word)
		if err != nil || !ok {
			return nil, err
		}
		if jjCommandWord(words) >= 0 {
			if chain[word] {
				return nil, jjUnexpandable{"aliases." + word, "jj would expand it a second time on this command line, a cycle jj refuses"}
			}
			chain[word] = true
		}
		line = slices.Concat(line[:at], words, line[at+1:])
	}
}
