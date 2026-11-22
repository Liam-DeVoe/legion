package leakguard

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// aPush is a push of a repository's one commit to a ref its destination does not hold.
type aPush struct {
	t   *testing.T
	req PushRequest
}

// through is the guard's answer to the push, under the test's bound.
func (p aPush) through(guard *Guard) error {
	return guard.Push(bound(p.t), p.req)
}

// aPushOf is a repository whose one commit holds files (path to content), and its push.
func aPushOf(t *testing.T, files map[string]string) aPush {
	t.Helper()
	dir := t.TempDir()
	fixture := fixturegit.Isolated(t, t.TempDir())
	fixture.Run(dir, fixturegit.InitWorkTree("", "main")...)
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.Run(dir, "git", "add", "-A")
	fixture.Run(dir, "git", "commit", "--quiet", "-m", "a commit of the test's files")
	head := fixture.Run(dir, "git", "rev-parse", "HEAD")
	return aPush{t: t, req: PushRequest{
		Dir:     dir,
		Remote:  "origin",
		URL:     "https://github.com/invented-org/invented-repo.git",
		Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: noSHA}},
	}}
}

// gitFirst is a guard whose git, first on its PATH, is script: a stand-in that may run the real
// git, as fixturegit.CompiledGit names it, by the path script holds.
func gitFirst(t *testing.T, script string) *Guard {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public,
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A record is scanned when it arrives, not when the pass ends: once a record that names a host is
// added, the pass's report holds its line before end is called. A pass that held its records
// and scanned them at its end would hold the whole push.
func TestTheRulePassScansARecordAsItArrives(t *testing.T) {
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	pass, err := guard.rulePass(bound(t), "the push")
	if err != nil {
		t.Fatal(err)
	}
	if err := pass.add(Record{Label: "notes.md", Text: "the runner answers at " + theHost}); err != nil {
		t.Fatal(err)
	}
	want := []string{"notes.md: " + theClass}
	if !slices.Equal(pass.report.lines, want) {
		t.Fatalf("after the record was added the report is %q, want %q", pass.report.lines, want)
	}
	if err := pass.add(Record{Label: "notes.md", Text: "a line of nothing in particular"}); err != nil {
		t.Fatal(err)
	}
	if got := refusal(t, pass.end("the push")).Lines; !slices.Equal(got, want) {
		t.Fatalf("the refusal is %q, want %q", got, want)
	}
}

// The rule pass reads a push's diff as git writes it and scans each line as it arrives. The
// stand-in git writes the diff's first added line, which names a host, and then waits for a file
// that the test makes once the pass's report holds that line; only then does it write the second
// line and exit. A read that waited for the whole diff, or a pass that scanned at its end, would
// leave the file unmade, and the stand-in gives up after 10 s and exits 7.
func TestTheRulePassReadsTheDiffAsGitWritesIt(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "the first line was scanned")
	guard := gitFirst(t, "printf 'diff --git a/f.md b/f.md\\n--- /dev/null\\n+++ b/f.md\\n@@ -0,0 +1,2 @@\\n"+
		"+the runner answers at "+theHost+"\\n'\n"+
		"i=0\nwhile [ ! -e '"+seen+"' ]; do\n  i=$((i+1))\n  [ \"$i\" -gt 100 ] && exit 7\n  sleep 0.1\ndone\n"+
		"printf '+a second line of nothing\\n'\n")
	pass, err := guard.rulePass(bound(t), "the push")
	if err != nil {
		t.Fatal(err)
	}
	made := false
	emit := func(record Record) error {
		if err := pass.add(record); err != nil {
			return err
		}
		if len(pass.report.lines) > 0 && !made {
			made = true
			return os.WriteFile(seen, nil, 0o600)
		}
		return nil
	}
	if err := guard.addedLines(bound(t), t.TempDir(), "HEAD", 1, emit); err != nil {
		t.Fatalf("the read of the diff: %v", err)
	}
	want := []string{"f.md: " + theClass}
	if got := refusal(t, pass.end("the push")).Lines; !slices.Equal(got, want) {
		t.Fatalf("the refusal is %q, want %q", got, want)
	}
}

// Every added line of a push is read whole as git writes it, a line longer than the reader's
// buffer included: a match at the end of a line of 192 KiB is reported, and so is each match among
// many short lines, one line of the report for each, in the order git wrote them.
func TestEveryAddedLineIsReadWholeAsGitWritesIt(t *testing.T) {
	var many strings.Builder
	for i := range 2000 {
		if i%500 == 7 {
			fmt.Fprintf(&many, "line %d names %s\n", i, theHost)
			continue
		}
		fmt.Fprintf(&many, "line %d of a file of many lines\n", i)
	}
	long := strings.Repeat("a long line of nothing ", 192<<10/23) + theHost
	push := aPushOf(t, map[string]string{"many.md": many.String(), "long.md": "before\n" + long + "\nafter\n"})
	err := push.through(guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public))
	want := append([]string{"long.md: " + theClass}, slices.Repeat([]string{"many.md: " + theClass}, 4)...)
	if got := refusal(t, err).Lines; !slices.Equal(got, want) {
		t.Fatalf("the report is %q, want %q", got, want)
	}
}

// A push with no deadline at all, as git's pre-push hook runs one, is answered once git itself
// has exited, though a process git started in the background holds git's standard output, the
// diff's pipe, open for 30 s: each read of git's output ends one procgroup.WaitDelay after git
// exits, the wait exec gives such a process when it copies a child's output itself. With no
// deadline nothing stops git's process group, so the process need not leave the group to outlive
// git, and the test needs nothing a system may lack. The answer is the one a push with no such
// process gets.
func TestAReadEndsAfterGitExitsThoughAnEscapedProcessHoldsThePipe(t *testing.T) {
	push := aPushOf(t, map[string]string{"notes.md": "a line of nothing\nthe runner answers at " + theHost + "\n"})
	want := refusal(t, push.through(guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public))).Lines
	guard := gitFirst(t, "sleep 30 2>/dev/null &\nexec '"+fixturegit.CompiledGit(t)+"' \"$@\"\n")
	started := time.Now()
	err := guard.Push(context.Background(), push.req)
	if took := time.Since(started); took > 20*time.Second {
		t.Errorf("the push was answered after %s, waiting on the escaped process", took.Round(time.Second))
	}
	if got := refusal(t, err).Lines; !slices.Equal(got, want) {
		t.Fatalf("the report is %q, want the one with no escaped process, %q", got, want)
	}
}

// A push with no deadline is answered soon after git's diff exits, though a process git started
// keeps writing to git's standard output, the diff's pipe, after it: once git has exited and one
// WaitDelay has passed, the read takes what the pipe holds then and no more. A read that took
// whatever the pipe held each time would never end while the process wrote. The process writes
// lines of the diff's own shape, which name nothing, until the test stops it (after 40 s, or at
// the test's end), and the answer is the one a push with no such process gets.
func TestAReadEndsAfterGitExitsThoughAProcessKeepsWritingToThePipe(t *testing.T) {
	push := aPushOf(t, map[string]string{"notes.md": "a line of nothing\nthe runner answers at " + theHost + "\n"})
	want := refusal(t, push.through(guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public))).Lines
	stop := filepath.Join(t.TempDir(), "stop")
	stopWriting := func() { _ = os.WriteFile(stop, nil, 0o600) }
	t.Cleanup(stopWriting)
	timer := time.AfterFunc(40*time.Second, stopWriting)
	defer timer.Stop()
	git := fixturegit.CompiledGit(t)
	guard := gitFirst(t, "case \" $* \" in\n"+
		"*\" --unified=0 \"*) '"+git+"' \"$@\" || exit\n"+
		"  (while [ ! -e '"+stop+"' ]; do echo '+a junk line of nothing'; done) 2>/dev/null &\n  exit 0 ;;\n"+
		"*) exec '"+git+"' \"$@\" ;;\nesac\n")
	started := time.Now()
	err := guard.Push(context.Background(), push.req)
	if took := time.Since(started); took > 25*time.Second {
		t.Errorf("the push was answered after %s, reading the writing process", took.Round(time.Second))
	}
	if got := refusal(t, err).Lines; !slices.Equal(got, want) {
		t.Fatalf("the report is %q, want the one with no writing process, %q", got, want)
	}
}

// Where a process git started holds both git's standard output and its standard error, exec waits
// one WaitDelay after git exits for the standard error it copies, and Wait answers
// exec.ErrWaitDelay: the read of the standard output then ends at once, its own wait having run
// out with exec's, rather than one WaitDelay after Wait. So the push takes one WaitDelay more than
// the same push with no such process, as on main, where exec copies both outputs, and not two
// (holderCost).
func TestAReadEndsOneWaitDelayAfterGitExitsThoughAProcessHoldsBothOutputs(t *testing.T) {
	push := aPushOf(t, map[string]string{"notes.md": "a line of nothing\nthe runner answers at " + theHost + "\n"})
	want := refusal(t, push.through(guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public))).Lines
	err := holderCost(t, push.req, func(git, holder string) string {
		return holder + "\n  exec '" + git + "' \"$@\""
	})
	if got := refusal(t, err).Lines; !slices.Equal(got, want) {
		t.Fatalf("the report is %q, want the one with no holding process, %q", got, want)
	}
}

// Where git fails and a process it started holds both its outputs, Wait answers git's
// *exec.ExitError, not exec.ErrWaitDelay, though exec waited one WaitDelay for the standard error
// it copies: the read of the standard output still ends at once, so the diff's git takes one
// WaitDelay more than the same failing git with no such process, as on main, and not two
// (holderCost). The push is refused for git's status.
func TestAReadEndsOneWaitDelayAfterAFailedGitExitsThoughAProcessHoldsBothOutputs(t *testing.T) {
	push := aPushOf(t, map[string]string{"notes.md": "a line of nothing in particular\n"})
	err := holderCost(t, push.req, func(git, holder string) string {
		return holder + "\n  '" + git + "' \"$@\"\n  exit 3"
	})
	says(t, setupFailure(t, err), "the push's commits could not be read: git diff-tree exited 3")
}

// holderCost pushes req twice through a guard whose diff git runs diff(git, holder), first with
// the holder `:` (no process) and then with a process that holds both of git's outputs for 60 s,
// and answers the second push's answer. Its WaitDelay is 20 s, and it holds two bounds:
//   - The push with the holder takes less than one and a half WaitDelays more than the push with
//     none. One WaitDelay is right; a read that waited a second one after Wait takes two. A slow
//     machine's cost lands in both pushes, so one and two stay 10 s from the bound either way.
//   - The push with no holder takes less than half a WaitDelay. A read that waits although
//     nothing holds the pipe, such as one whose own copy of the pipe's write end stays open, is
//     as slow in both pushes and would pass the first bound; it fails this one.
//
// Only the diff's git (`--unified=0`) runs the script; every other git runs as it is.
func holderCost(t *testing.T, req PushRequest, diff func(git, holder string) string) error {
	t.Helper()
	const delay = 20 * time.Second
	git := fixturegit.CompiledGit(t)
	push := func(holder string) (time.Duration, error) {
		guard := gitFirst(t, "case \" $* \" in\n"+
			"*\" --unified=0 \"*) "+diff(git, holder)+" ;;\n"+
			"*) exec '"+git+"' \"$@\" ;;\nesac\n")
		guard.waitDelay = delay
		started := time.Now()
		err := guard.Push(context.Background(), req)
		return time.Since(started), err
	}
	baseline, _ := push(":")
	if baseline >= delay/2 {
		t.Errorf("the push with no holding process took %s, past half a WaitDelay of %s: a read waits although nothing holds the pipe", baseline.Round(time.Second), delay)
	}
	took, err := push("sleep 60 &")
	if took-baseline >= delay*3/2 {
		t.Errorf("the push was answered after %s, %s past the same push with no holding process: past one and a half WaitDelays of %s, the read waited a second one", took.Round(time.Second), (took - baseline).Round(time.Second), delay)
	}
	return err
}

// Once the child has exited and its wait has run out, a read takes what the pipe held at that
// first read and no more, though the pipe's write end is still open and more is written to it
// after: 1000 bytes of A are in the pipe when the reader is marked past the child's exit, one
// read takes some of them, 1000 bytes of B follow, and the reader answers the rest of the A and
// then the end of its output. A pipe the test owns stands for the child's output and the process
// that outlived the child, so nothing here depends on how fast either runs.
func TestAReadAfterTheChildExitedTakesWhatThePipeHeldAndNoMore(t *testing.T) {
	out, in, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	defer in.Close()
	if _, err := in.Write(bytes.Repeat([]byte("A"), 1000)); err != nil {
		t.Fatal(err)
	}
	reader := &pipeReader{ctx: context.Background(), file: out}
	reader.exited.Store(true)
	first := make([]byte, 100)
	n, err := reader.Read(first)
	if err != nil {
		t.Fatalf("the first read: %v", err)
	}
	if _, err := in.Write(bytes.Repeat([]byte("B"), 1000)); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("the rest of the read: %v", err)
	}
	got := string(append(first[:n], rest...))
	if got != strings.Repeat("A", 1000) {
		t.Fatalf("read %d bytes (%d of them B), want the 1000 of A alone", len(got), strings.Count(got, "B"))
	}
}

// A read of a push's diff ends with the push's bound while git's diff has not exited, and a
// process git started has left git's process group (setsid) and holds the diff's pipe open far
// past the bound: the read is refused as a setup failure naming the bound, within a few seconds
// of the bound. The stand-in runs every other git at once and, for the diff, runs the real git
// and then stays in its foreground until the bound stops it. The escaped process holds standard
// output alone: one that held standard error too would keep exec's copy of it running.
func TestADiffReadEndsWithItsBoundThoughAnEscapedProcessHoldsThePipe(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid on this machine")
	}
	push := aPushOf(t, map[string]string{"notes.md": "a line of nothing in particular\n"})
	git := fixturegit.CompiledGit(t)
	guard := gitFirst(t, "case \" $* \" in\n"+
		"*\" --unified=0 \"*) setsid sleep 30 2>/dev/null &\n  '"+git+"' \"$@\"\n  sleep 30 ;;\n"+
		"*) exec '"+git+"' \"$@\" ;;\nesac\n")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	started := time.Now()
	err := guard.Push(ctx, push.req)
	if took := time.Since(started); took > 20*time.Second {
		t.Errorf("the push was answered after %s, past its bound of 4 s and the stop's grace", took.Round(time.Second))
	}
	says(t, setupFailure(t, err), "was stopped by its bound")
}

// A file renamed to a path the rules refuse is read as its new path, which is the path the
// destination learns, and not as the path it was at. The rename moves the file whole, so the
// path is the whole of what the commit names.
func TestARenamedPathIsReadAsItsNewPath(t *testing.T) {
	push := aPushOf(t, map[string]string{"old.md": "a line of nothing in particular\n"})
	fixture := fixturegit.Isolated(t, t.TempDir())
	base := push.req.Updates[0].LocalSHA
	// Built out of two pieces for the reason theHost is (TestTheReportMasksAPathComponent).
	path := "packages/" + "agen" + "t-c/notes.md"
	if err := os.MkdirAll(filepath.Join(push.req.Dir, filepath.Dir(path)), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.Run(push.req.Dir, "git", "mv", "old.md", path)
	fixture.Run(push.req.Dir, "git", "commit", "--quiet", "-m", "a rename")
	push.req.Updates = []Update{{LocalRef: "refs/heads/main", LocalSHA: fixture.Run(push.req.Dir, "git", "rev-parse", "HEAD"), RemoteRef: "refs/heads/main", RemoteSHA: base}}
	const class = "names the private deployment repository or its Dispatch project key"
	want := []string{"packages/<name>/notes.md: " + class, "packages/<name>/notes.md: " + class + " in its path"}
	if got := refusal(t, push.through(guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public))).Lines; !slices.Equal(got, want) {
		t.Fatalf("the report is %q, want %q", got, want)
	}
}

// A path that holds a space is reported as itself. git ends the "+++" line that names such a path
// with a tab, which is no part of the path; where the path also holds a byte git quotes, a tab of
// its own here, git writes it C-quoted with its b/ prefix inside the quotes, and the report names
// the path itself, not that quoted form.
func TestAPathWithASpaceIsReportedAsItself(t *testing.T) {
	push := aPushOf(t, map[string]string{
		"a named file.md":     "the runner answers at " + theHost + "\n",
		"a named\tand tab.md": "the runner answers at " + theHost + "\n",
	})
	err := push.through(guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public))
	// git lists the paths in byte order, and a tab sorts before a space.
	want := []string{"a named\tand tab.md: " + theClass, "a named file.md: " + theClass}
	if got := refusal(t, err).Lines; !slices.Equal(got, want) {
		t.Fatalf("the report is %q, want %q", got, want)
	}
}

// A refusal names at most refusalLines lines and counts the rest in one last line of its own, so a
// push of a file of many lines at a path a path rule refuses, each line refused for the path, is a
// report of bounded size. A refusal of no more lines than the bound is every line, as it was.
func TestARefusalNamesAtMostItsBoundOfLines(t *testing.T) {
	// Built out of two pieces for the reason theHost is (TestTheReportMasksAPathComponent).
	path := "packages/" + "agen" + "t-c/notes.md"
	const class = "names the private deployment repository or its Dispatch project key"
	for _, tc := range []struct {
		name string
		// lines is how many lines the file adds; the report of each is one line, and the path's
		// own record is two more, one of the content rule and one of the path rule.
		lines int
		want  func(today []string) []string
	}{
		{name: "fewer lines than the bound", lines: 3, want: func(today []string) []string { return today }},
		{name: "the bound's own number of lines", lines: refusalLines - 2, want: func(today []string) []string { return today }},
		{
			name:  "one line more than the bound",
			lines: refusalLines - 1,
			want: func(today []string) []string {
				return append(today[:refusalLines:refusalLines], fmt.Sprintf("(1 more line left out: this report names the first %d)", refusalLines))
			},
		},
		{
			name:  "more lines than the bound",
			lines: refusalLines + 5,
			want: func(today []string) []string {
				return append(today[:refusalLines:refusalLines], fmt.Sprintf("(7 more lines left out: this report names the first %d)", refusalLines))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var content strings.Builder
			for i := range tc.lines {
				fmt.Fprintf(&content, "line %d of nothing in particular\n", i)
			}
			push := aPushOf(t, map[string]string{path: content.String()})
			got := refusal(t, push.through(guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public))).Lines
			today := []string{"packages/<name>/notes.md: " + class, "packages/<name>/notes.md: " + class + " in its path"}
			today = append(today, slices.Repeat([]string{"packages/<name>/notes.md: " + class + " in its path"}, tc.lines)...)
			want := tc.want(today)
			if !slices.Equal(got, want) {
				t.Fatalf("the report is %d lines ending %q, want %d lines ending %q", len(got), got[max(0, len(got)-2):], len(want), want[max(0, len(want)-2):])
			}
		})
	}
}
