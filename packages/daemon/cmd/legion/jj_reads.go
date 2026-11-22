package main

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	legionclaim "github.com/sjawhar/legion/daemon/internal/claim" // main_test.go's `claim` helper holds the bare name
)

// This file holds the step's reads (jjStep, jj.go): a read of commits with jjCommitTemplate (read,
// readEach) and of commit ids (ids, eachID), each with --ignore-working-copy (jjStep.jj), and the
// revsets of a list of commit or change ids in as few words as keep each under jjWordBound bytes
// (jjWords, jjChanges, jjCommitIDs), with the bytes Linux counts against jjArgMax (jjSize); and
// how the step and the expansion run a jj command and name its failure (jj, output, command,
// runJJCommand, jjErrorLine).

// jjCommitTemplate prints, for each commit, its change id, its commit id, whether it is hidden,
// whether it is the working-copy commit of the workspace the command runs in
// (current_working_copy, which jj reads from the operation, --ignore-working-copy or not), its
// author's email and the value of its first Legion-Role trailer, each ended by NUL, which none of
// them holds.
const jjCommitTemplate = `change_id ++ "\0" ++ commit_id ++ "\0" ++ hidden ++ "\0" ++ current_working_copy ++ "\0" ++ author.email() ++ "\0" ++ trailers.filter(|t| t.key() == "Legion-Role").map(|t| t.value()).join("\n") ++ "\0"`

// jjCommitRead is one commit as jjCommitTemplate prints it.
type jjCommitRead struct {
	change, commit  string
	hidden, working bool
	email           string
	role            legionclaim.Role
}

// readEach is each commit of the revsets of words, read with jjCommitTemplate (read), one command
// for each, and each commit once, in the order the reads print them (jjEachOnce).
func (s *jjStep) readEach(words []string) ([]jjCommitRead, error) {
	return jjEachOnce(words, s.read, func(c jjCommitRead) string { return c.commit })
}

// read is each commit of revset, read with jjCommitTemplate.
func (s *jjStep) read(revset string) ([]jjCommitRead, error) {
	out, err := s.jj(s.env, "--color=never", "log", "--no-graph", "-r", revset, "-T", jjCommitTemplate)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	if len(fields)%6 != 1 || fields[len(fields)-1] != "" {
		return nil, fmt.Errorf("jj log -r %s printed %q, which is not one record of six fields for each commit", revset, out)
	}
	commits := make([]jjCommitRead, 0, len(fields)/6)
	for i := 0; i+6 < len(fields); i += 6 {
		role, _, _ := strings.Cut(fields[i+5], "\n")
		commits = append(commits, jjCommitRead{
			change: fields[i], commit: fields[i+1], hidden: fields[i+2] == "true", working: fields[i+3] == "true",
			email: fields[i+4], role: legionclaim.Role(role),
		})
	}
	return commits, nil
}

// ids reads the commits of revset in one command: each one's full id, once, in jj's order, and
// the id of the one that is the working-copy commit of the workspace the command runs in
// (current_working_copy, which jj reads from the operation, --ignore-working-copy or not), or ""
// where none is. A read jj refuses, or one that cannot start, is a jjStepFailure, its one error.
func (s *jjStep) ids(revset string) (ids []string, working string, err error) {
	out, err := s.jj(s.env, "--color=never", "log", "--no-graph", "-r", revset, "-T", `commit_id ++ if(current_working_copy, " @") ++ "\n"`)
	if err != nil {
		return nil, "", err
	}
	for line := range strings.Lines(out) {
		id, mark, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		if id == "" {
			continue
		}
		ids = append(ids, id)
		if mark == "@" {
			working = id
		}
	}
	return ids, working, nil
}

// eachID is the full id of each commit of revsets, read with one command for each (ids), each
// once, in the order the reads print them (jjEachOnce).
func (s *jjStep) eachID(revsets []string) ([]string, error) {
	return jjEachOnce(revsets, func(revset string) ([]string, error) {
		ids, _, err := s.ids(revset)
		return ids, err
	}, func(id string) string { return id })
}

// jjEachOnce is each item of revsets, read with one command for each (read), and each item once
// by its commit id, as id reads it, in the order the reads print them.
func jjEachOnce[T any](revsets []string, read func(string) ([]T, error), id func(T) string) ([]T, error) {
	var items []T
	seen := map[string]bool{}
	for _, revset := range revsets {
		got, err := read(revset)
		if err != nil {
			return nil, err
		}
		for _, item := range got {
			if key := id(item); !seen[key] {
				seen[key] = true
				items = append(items, item)
			}
		}
	}
	return items, nil
}

// jjChanges is the revsets of the commits of each of changes, by change id: `change_id(<change>)`
// for each, joined by ` | ` into as few revsets as keep each under jjWordBound bytes (jjWords).
func jjChanges(changes []string) []string {
	named := make([]string, len(changes))
	for i, change := range changes {
		named[i] = "change_id(" + change + ")"
	}
	return jjWords("", named, "")
}

// jjCommitIDs is the term `commit_id(<id>)` of each of ids, full commit ids, each of which
// commit_id() names exactly, for a read of them in as few revsets as keep each under jjWordBound
// bytes (jjWords).
func jjCommitIDs(ids []string) []string {
	named := make([]string, len(ids))
	for i, id := range ids {
		named[i] = "commit_id(" + id + ")"
	}
	return named
}

// jjWordBound is the bound, in bytes, under which the step keeps each word it builds of a list of
// commit or change ids: the revset of one of its reads (jjWords), and a word of a revision argument
// that gives the push its commits as their ids or as its primary form (revisionGiven). Linux
// refuses one argv word over 131,072 bytes, and such a list grows with the commits it names, which
// a long history makes thousands. It is a variable so that this package's test binary, run as
// legion, can lower it (TestMain); nothing in the legion binary changes it.
var jjWordBound = 100_000

// jjArgMax is the bound, in bytes, under which the step keeps each command of its own that grows
// with the commits it names, the push's (checkArgMax) and each metaedit's (jjStep.metaedit): each
// string of its arguments and of its environment with the NUL that ends it, and the pointer to each
// (jjSize), which Linux refuses past 2,097,152 bytes, a quarter of the usual 8 MiB stack
// (execve(2)). It is a variable so that this package's test binary, run as legion, can lower it
// (TestMain); nothing in the legion binary changes it.
var jjArgMax = 2_097_152

// jjSize is the bytes that Linux counts against jjArgMax (execve(2)) for words, strings of a
// command's arguments or environment: each with the NUL that ends it, and the pointer to it.
func jjSize(words ...string) int {
	size := 0
	for _, word := range words {
		size += len(word) + 1 + int(unsafe.Sizeof(uintptr(0)))
	}
	return size
}

// jjWords is terms in as few revsets as keep each under jjWordBound bytes: each prefix, then terms
// joined by ` | `, then suffix, the terms in order. A term that no revset of its own keeps under
// the bound is a revset alone. A read of each, merged (readEach), is one read of them all where
// prefix and suffix take the union of the terms' commits to the union of what they take of each,
// as `::`, `&`, `~` and `|` do.
func jjWords(prefix string, terms []string, suffix string) []string {
	var words []string
	var word strings.Builder
	for _, term := range terms {
		if word.Len() > 0 && word.Len()+len(" | ")+len(term)+len(suffix) >= jjWordBound {
			words = append(words, word.String()+suffix)
			word.Reset()
		}
		if word.Len() == 0 {
			word.WriteString(prefix)
		} else {
			word.WriteString(" | ")
		}
		word.WriteString(term)
	}
	if word.Len() > 0 {
		words = append(words, word.String()+suffix)
	}
	return words
}

// jj runs one jj command of the step that takes no snapshot and answers what it printed on
// standard output (output): next, with the caller's options (jjPush.options), then
// --ignore-working-copy, then args, in env.
func (s *jjStep) jj(env []string, args ...string) (string, error) {
	stdout, _, err := s.output(env, slices.Concat(s.push.options, []string{"--ignore-working-copy"}, args))
	return stdout, err
}

// output runs next with args in env (command) and answers what it printed on standard output and
// on standard error. A command that cannot start or exits non-zero is a jjStepFailure, with what
// it printed on standard error.
func (s *jjStep) output(env, args []string) (stdout, stderr string, err error) {
	stdout, stderr, code, err := s.command(env, args)
	if err != nil {
		return "", "", err
	}
	if code != 0 {
		return "", "", jjStepFailure{code: code, output: stderr}
	}
	return stdout, stderr, nil
}

// command runs next with args in env and waits for it (runJJCommand).
func (s *jjStep) command(env, args []string) (stdout, stderr string, code int, err error) {
	return runJJCommand(s.next, env, args)
}

// runJJCommand runs next with args in env and waits for it, as a child in this process's group,
// with no standard input: each command of the step (jjStep.command) and the read of the aliases
// (readJJAliases). It answers what the command printed and its status, a death by a signal being
// 128 and the signal's number, as a shell reads it. A file the kernel refuses to run runs with
// /bin/sh, as execWrapped runs it. A command that cannot start is a jjStepFailure whose status is
// execWrapped's for the same failure.
func runJJCommand(next string, env, args []string) (stdout, stderr string, code int, err error) {
	var out, errOut bytes.Buffer
	run := func(path string, argv []string) error {
		out.Reset()
		errOut.Reset()
		cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Stdout: &out, Stderr: &errOut}
		return cmd.Run()
	}
	err = run(next, append([]string{next}, args...))
	if errors.Is(err, syscall.ENOEXEC) {
		err = run("/bin/sh", append([]string{"/bin/sh", next}, args...))
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		}
		return out.String(), errOut.String(), code, nil
	case err != nil:
		code = 126
		if errors.Is(err, syscall.ENOENT) {
			code = 127
		}
		return "", "", 0, jjStepFailure{code: code, output: fmt.Sprintf("legion jj: run %s: %v\n", next, err)}
	}
	return out.String(), errOut.String(), 0, nil
}

// jjErrorLine is the line that names the failure of a jj command that exited code, command being
// what stands for the command: the first line of output, its standard error, that starts with
// `Error: `, jj's error, which may follow a notice such as the `Creating bookmark push-<change> for
// revision <change>` of `jj git push -c`; else the first line that is not blank, or a line that
// says it printed nothing. The step names it where it cannot read a dry run, and the expansion
// where its read of the aliases fails (readJJAliases).
func jjErrorLine(output, command string, code int) string {
	first := ""
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, "Error: ") {
			return strings.TrimSpace(line)
		}
		if first == "" {
			first = strings.TrimSpace(line)
		}
	}
	if first != "" {
		return first
	}
	return fmt.Sprintf("%s exited %d and printed nothing", command, code)
}
