package leakguard

import (
	"context"
	"fmt"
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

// A read of a push's diff ends with the push's bound, though a process git started has left git's
// process group (setsid) and holds git's standard output, the diff's pipe, open far past it: the
// read is refused as a setup failure naming the bound, within a few seconds of the bound, and does
// not wait for that process. The stand-in git, first on PATH, starts such a process and then runs
// the real git. The process holds standard output alone: one that held standard error too would
// keep exec's copy of it running, and exec closes every pipe it made once WaitDelay passes after
// the bound, which would end the read without the guard's own deadline.
func TestADiffReadEndsWithItsBoundThoughAnEscapedProcessHoldsThePipe(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid on this machine")
	}
	push := aPushOf(t, map[string]string{"notes.md": "a line of nothing in particular\n"})
	dir := t.TempDir()
	script := "#!/bin/sh\nsetsid sleep 15 2>/dev/null &\nexec '" + fixturegit.CompiledGit(t) + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public,
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	started := time.Now()
	err := guard.Push(ctx, push.req)
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("the push was answered after %s, past its bound of 4 s and the stop's grace", took.Round(time.Second))
	}
	says(t, setupFailure(t, err), "was stopped by its bound")
}

// A path that holds a space is reported as itself. git ends the "+++" line that names such a path
// with a tab, which is no part of the path.
func TestAPathWithASpaceIsReportedAsItself(t *testing.T) {
	push := aPushOf(t, map[string]string{"a named file.md": "the runner answers at " + theHost + "\n"})
	err := push.through(guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public))
	want := []string{"a named file.md: " + theClass}
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
