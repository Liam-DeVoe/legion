package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/sjawhar/legion/daemon/internal/lookpath"
)

// gitUsage is the help of `legion git`, which reads its own arguments (passThrough), --help among
// them.
const gitUsage = `usage: legion git --real <git> -- <git argument>…
       legion git --after <directory> -- <git argument>…

A git wrapper runs this command, never a person. It runs a git with the arguments after --.
For a git commit, it first adds two trailers: Omp-Session, with the session id that the
dispatch command acts as, and Legion-Role, with LEGION_ROLE. It adds each line only when its
value is valid, and only when git alone would accept the commit's message.

  --real <git>         run <git>, the real git (the pod's form: /usr/bin/git)
  --after <directory>  run the next git on PATH after <directory>, the wrapper's own directory
                       (the form of the git first on a tmux pane's PATH)
`

// wrappedGitAt is the loop guard's variable. Each run of `legion git --after` sets it in the
// environment of the git it runs: the number of runs in this chain that came back through a
// host's git wrapper, and then the path of each git the chain ran, a script or not, in order, all
// joined by `:`, which no PATH entry holds. A host's git wrapper that runs the next git on PATH
// can run the tmux wrapper again, and a listed path that is the same file as a git on PATH tells
// that run that it came back: it goes on past every git the chain ran and adds nothing. Files are
// compared (os.SameFile), not PATH indexes or path strings, since a host's wrapper may put an
// entry in front of PATH or reach a file through another spelling. The count ends a chain whose
// gits run one another without end. A git that git itself starts (an alias's `!git`, a hook)
// never reaches either wrapper, because git puts its own exec path first on its children's PATH,
// so the variable in a real git's environment does no harm. Its name starts with neither LEGION_
// nor GIT_: fixtureenv.Isolated drops both prefixes, and git reserves GIT_.
const wrappedGitAt = "WRAPPED_GIT_AT"

// runGit is `legion git`. Both of Legion's git wrappers run it: the pod's /opt/legion/bin/git
// hands it a commit with --real /usr/bin/git, and the git first on a tmux pane's PATH hands it
// every command with --after and its own directory. It finds the subcommand as the pod's wrapper
// does (gitSubcommand), chooses the git to run (pickAfter for --after), and replaces itself with
// that git (execWrapped). It changes the arguments of a commit only (commitWithTrailers). For every
// other command it prints nothing and starts no other process.
//
// main's signal.NotifyContext catches SIGINT and SIGTERM for the whole process. A caught signal
// would not stop a read that waits (`-F -` from a pipe that stays open, a FIFO with no writer),
// and a cancelled commit would still be made. So runGit puts back the actions those two had at
// the process start (signal.Reset): the Go runtime's own, which ends the process as the signal's
// default action does, or for SIGINT an ignore that the caller set. So an interrupt or a terminate
// ends a read that waits. A signal that came before that ends runGit with the status a shell reads
// a death by that signal as (endedBySignal), before it reads anything or runs git.
//
// This has a limit. The Go runtime puts its own handler over an ignore that the caller set for
// each signal it handles, except SIGHUP and SIGINT (sigInstallGoHandler, runtime/signal_unix.go).
// It does not handle SIGTSTP, SIGTTIN or SIGTTOU (_SigDefault, runtime/sigtab_linux_generic.go).
// The exec sets each handled signal back to its default action. So where the caller ignores a
// signal that the runtime handles, such as SIGTERM, SIGPIPE or SIGQUIT, git alone lives through
// that signal, but legion git and the git it runs die of it. An ignored SIGHUP, SIGINT, SIGTSTP,
// SIGTTIN or SIGTTOU stays ignored.
func runGit(ctx context.Context, args []string, _, stderr io.Writer) int {
	if len(args) > 0 && helpRequested(args[0]) {
		fmt.Fprint(stderr, gitUsage)
		return 0
	}
	if len(args) < 3 || (args[0] != "--real" && args[0] != "--after") || args[1] == "" || args[2] != "--" {
		fmt.Fprint(stderr, "legion git: give --real <git> or --after <directory>, then --, then the git arguments\n"+gitUsage)
		return 2
	}
	signal.Reset(os.Interrupt, syscall.SIGTERM)
	if cause := context.Cause(ctx); cause != nil {
		if status, ok := endedBySignal(cause); ok {
			return status
		}
	}
	gitArgs := args[3:]
	sub := gitSubcommand(gitArgs)
	commit := sub >= 0 && gitArgs[sub] == "commit"
	env := os.Environ()
	if args[0] == "--real" {
		if !commit {
			return execWrapped("legion git", args[1], gitArgs, env, nil, stderr)
		}
		route := stashThroughDescriptor
		if isScript(args[1]) {
			route = stashNone
		}
		return runCommit(args[1], route, gitReader{path: args[1], globals: gitArgs[:sub]}, gitArgs[sub+1:], env, stderr)
	}
	pick, err := pickAfter("git", args[1], os.Getenv("PATH"), os.Getenv(wrappedGitAt))
	if err != nil {
		fmt.Fprintf(stderr, "legion git: %v\n", err)
		return 127
	}
	env = withVariable(env, wrappedGitAt, pick.chain.String())
	if !commit || pick.reentry {
		return execWrapped("legion git", pick.run, gitArgs, env, nil, stderr)
	}
	helper := pick.helper()
	if helper == "" {
		return execWrapped("legion git", pick.run, gitArgs, env, nil, stderr)
	}
	// The helper answers the commit's reads with the chain as it would run it, so a helper that
	// is a host's compiled wrapper comes back as a re-entry and reaches the git after it.
	readChain := pick.chain
	if helper != pick.run {
		readChain.ran = append(slices.Clone(readChain.ran), helper)
	}
	reader := gitReader{path: helper, globals: gitArgs[:sub], env: withVariable(os.Environ(), wrappedGitAt, readChain.String())}
	return runCommit(pick.run, stashOnStandardInput, reader, gitArgs[sub+1:], env, stderr)
}

// runCommit runs git at path with the commit's arguments args as commitWithTrailers makes them,
// read with reader, whose globals are the caller's global options. route is the way a stash
// reaches that git (stashRoute). For --after it is stashOnStandardInput, whether path is a script
// or not. For --real it is stashThroughDescriptor, or stashNone when path is a script.
func runCommit(path string, route stashRoute, reader gitReader, args, env []string, stderr io.Writer) int {
	commit, err := commitWithTrailers(reader, route, args, processEnvironment())
	if err == nil && commit.stdin != nil {
		err = asStandardInput(commit.stdin)
	}
	if err != nil {
		fmt.Fprintf(stderr, "legion git: %v\n", err)
		return 1
	}
	return execWrapped("legion git", path, commit.args, env, commit.keep, stderr)
}

// wrapperChain is a loop guard's value (wrappedGitAt, wrappedJJAt): reentries, the number of runs
// in the chain that came back, and ran, the path of each program the chain ran, in order.
type wrapperChain struct {
	reentries int
	ran       []string
}

// String is the chain as the variable holds it: `<reentries>:<path>:<path>…`.
func (c wrapperChain) String() string {
	return strconv.Itoa(c.reentries) + ":" + strings.Join(c.ran, ":")
}

// parseWrapperChain reads a loop guard's value, and answers what os.Stat says of each listed path
// that is there. A value of another form is no chain.
func parseWrapperChain(value string) (chain wrapperChain, listed []os.FileInfo) {
	count, rest, ok := strings.Cut(value, ":")
	n, err := strconv.ParseUint(count, 10, 31)
	if !ok || err != nil {
		return wrapperChain{}, nil
	}
	chain.reentries = int(n)
	for _, path := range strings.Split(rest, ":") {
		if path == "" {
			continue
		}
		chain.ran = append(chain.ran, path)
		if info, err := os.Stat(path); err == nil {
			listed = append(listed, info)
		}
	}
	return chain, listed
}

// wrapperPick is the program that `legion git --after` or `legion jj --after` runs, and what it
// knows of the programs of that name on PATH.
type wrapperPick struct {
	// run is the program it replaces itself with, and chain the loop guard's value run gets.
	run   string
	chain wrapperChain
	// reentry is whether this run came back through a host's wrapper that an earlier run of the
	// chain chose. Such a run adds nothing: the first run of the chain did what Legion's wrapper
	// adds (a commit's trailers, a push's author step).
	reentry bool
	// after is each program of the name on PATH after the first entry that is the wrapper's own
	// directory, in PATH order: the ones it may run, and the ones the helper is chosen from.
	after []wrapperCandidate
}

// wrapperCandidate is a program that pickAfter found on PATH, and script, whether it is a script
// (isScript), which pickAfter reads once.
type wrapperCandidate struct {
	lookpath.Found
	script bool
}

// pickAfter chooses the program named name for `legion <name> --after own`, from path (PATH),
// marker (the loop guard's variable) and the kind of each candidate after own, which it reads once
// (isScript), and nothing else. A candidate is each program of the name on path outside own
// (lookpath.Others). It runs the first candidate after the first entry that is own, or the first
// candidate when own is on no entry. A script is a candidate too, so a host's own wrapper still
// runs.
//
// A marker that lists the same file as a candidate is a re-entry, one more than the chain counted.
// It runs the first candidate after own that the marker does not list, and the helper when the
// marker lists every one. When the count passes the number of candidates, or the marker lists
// every candidate and there is no helper, the programs on PATH have run one another and come back:
// the chain is a loop, and pickAfter refuses it. A marker that lists no candidate is stale: the run
// starts a new chain. Either way the chain's value for the program it runs lists that program too.
// The error is the loop, or no program of the name after own on a run that is no re-entry.
func pickAfter(name, own, path, marker string) (wrapperPick, error) {
	found, ownAt := lookpath.Others(path, name, own)
	var pick wrapperPick
	for _, f := range found {
		if f.Index > ownAt {
			pick.after = append(pick.after, wrapperCandidate{Found: f, script: isScript(f.Path)})
		}
	}
	chain, listed := parseWrapperChain(marker)
	isListed := func(f lookpath.Found) bool {
		return slices.ContainsFunc(listed, func(info os.FileInfo) bool { return os.SameFile(info, f.Info) })
	}
	pick.reentry = slices.ContainsFunc(found, isListed)
	switch {
	case !pick.reentry:
		if len(pick.after) == 0 {
			return wrapperPick{}, fmt.Errorf("no %s on PATH after %s", name, own)
		}
		chain = wrapperChain{}
		pick.run = pick.after[0].Path
	case chain.reentries < len(found):
		chain.reentries++
		if i := slices.IndexFunc(pick.after, func(c wrapperCandidate) bool { return !isListed(c.Found) }); i >= 0 {
			pick.run = pick.after[i].Path
		} else {
			pick.run = pick.helper()
		}
	}
	if pick.run == "" {
		return wrapperPick{}, fmt.Errorf("the %s programs on PATH after %s run one another in a loop", name, own)
	}
	chain.ran = append(chain.ran, pick.run)
	pick.chain = chain
	return pick, nil
}

// helper is the first program after own that is no script: the git that answers a commit's
// reads, so a host's script never answers them, and the run of a re-entry whose marker lists every
// candidate. It is "" when there is none, and then no trailer is added.
func (p wrapperPick) helper() string {
	for _, c := range p.after {
		if !c.script {
			return c.Path
		}
	}
	return ""
}

// gitSubcommand is the index of git's subcommand in args, or -1 when there is none: the first word
// that is no global option. The options whose value is the next word are the list of the pod's
// wrapper, packages/daemon/docker/git. Every other word that starts with `-` is an option of its
// own, the `--opt=value` spellings included. A word that ends git's own options before any
// subcommand (handle_options, git.c) ends the search with none: --help, -h, --version and -v,
// which git runs as its help and version commands with what follows as their arguments, and
// --exec-path without a value, --html-path, --man-path and --info-path, which print a path and
// end git. So `git --help commit` is no commit.
func gitSubcommand(args []string) int {
	value := false
	for i, arg := range args {
		switch {
		case value:
			value = false
		case slices.Contains([]string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--attr-source", "--shallow-file"}, arg):
			value = true
		case slices.Contains([]string{"--help", "-h", "--version", "-v", "--html-path", "--man-path", "--info-path"}, arg),
			strings.HasPrefix(arg, "--exec-path") && !strings.HasPrefix(arg, "--exec-path="):
			return -1
		case !strings.HasPrefix(arg, "-"):
			return i
		}
	}
	return -1
}

// isScript is whether path is no compiled program (lookpath.Kind): a script with a `#!` line,
// or a file with neither that line nor a compiled program's magic number, which the kernel refuses
// and a shell runs with /bin/sh. Either one is a host's wrapper, not the real git or jj. A file
// this process cannot read counts as a script, since nothing shows that it is a real program.
func isScript(path string) bool {
	_, compiled, err := lookpath.Kind(path)
	return err != nil || !compiled
}

// execWrapped replaces this process, the wrapper command (`legion git`, `legion jj`), with the
// program at path, argv[0] being path. keep is the file whose descriptor args name in place of a
// message git could no longer read (commitStash): it stays open across the exec, since its
// close-on-exec flag is clear. A file the kernel refuses to run (ENOEXEC), a script with no `#!`
// line, runs as execvp runs it: /bin/sh with argv `/bin/sh <path> <args…>` (glibc's
// maybe_script_execute). execWrapped returns only when the exec fails, having said so in command's
// name: 127 when path does not exist, as a shell answers, and 126 otherwise.
func execWrapped(command, path string, args, env []string, keep *os.File, stderr io.Writer) int {
	err := syscall.Exec(path, append([]string{path}, args...), env)
	if errors.Is(err, syscall.ENOEXEC) {
		err = syscall.Exec("/bin/sh", append([]string{"/bin/sh", path}, args...), env)
	}
	runtime.KeepAlive(keep)
	fmt.Fprintf(stderr, "%s: run %s: %v\n", command, path, err)
	if errors.Is(err, syscall.ENOENT) {
		return 127
	}
	return 126
}
