package leakguard

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// aPushOf is a repository whose one commit holds files (path to content), and the push of that
// commit to a ref its destination does not hold, through guard.
func aPushOf(t *testing.T, files map[string]string) func(guard *Guard) error {
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
	return func(guard *Guard) error {
		return guard.Push(bound(t), PushRequest{
			Dir:     dir,
			Remote:  "origin",
			URL:     "https://github.com/invented-org/invented-repo.git",
			Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: noSHA}},
		})
	}
}

// The rule pass reads a push's records as git writes them and holds one batch of them at a time:
// no batch is past the bound but a batch of one record that is past it alone, a line longer than
// the bound is such a batch, and the report is the one a single batch of every record makes.
func TestTheRulePassHoldsOneBatchAtATime(t *testing.T) {
	const limit = 4 << 10
	var many strings.Builder
	for i := range 2000 {
		if i%500 == 7 {
			fmt.Fprintf(&many, "line %d names %s\n", i, theHost)
			continue
		}
		fmt.Fprintf(&many, "line %d of a file of many lines\n", i)
	}
	long := strings.Repeat("a long line of nothing ", 3*limit/23) + theHost
	if len(long) <= limit {
		t.Fatalf("the long line is %d bytes, not past the bound of %d", len(long), limit)
	}
	push := aPushOf(t, map[string]string{"many.md": many.String(), "long.md": "before\n" + long + "\nafter\n"})

	whole := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	whole.batchLimit = 1 << 40
	batches := 0
	whole.onBatch = func([]Record) { batches++ }
	want := refusal(t, push(whole)).Lines
	if len(want) != 5 || batches != 1 {
		t.Fatalf("one batch of every record reports %q in %d batches, want the five lines that name a host in one", want, batches)
	}

	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	guard.batchLimit = limit
	batches, alone := 0, false
	guard.onBatch = func(batch []Record) {
		batches++
		size := 0
		for _, record := range batch {
			size += len(record.Text) + recordHeader
			if record.Text == long && len(batch) == 1 {
				alone = true
			}
		}
		if size > limit && len(batch) > 1 {
			t.Errorf("a batch of %d records holds %d bytes, past the bound of %d", len(batch), size, limit)
		}
	}
	if got := refusal(t, push(guard)).Lines; !slices.Equal(got, want) {
		t.Fatalf("the report is %q, want the one batch's %q", got, want)
	}
	if !alone {
		t.Error("the line longer than the bound was not a batch of its own")
	}
	if batches < 2000*30/limit {
		t.Errorf("the push was scanned in %d batches, too few for the bound of %d", batches, limit)
	}
}

// A path that holds a space is reported as itself. git ends the "+++" line that names such a path
// with a tab, which is no part of the path.
func TestAPathWithASpaceIsReportedAsItself(t *testing.T) {
	push := aPushOf(t, map[string]string{"a named file.md": "the runner answers at " + theHost + "\n"})
	err := push(guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public))
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
			got := refusal(t, push(guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public))).Lines
			today := []string{"packages/<name>/notes.md: " + class, "packages/<name>/notes.md: " + class + " in its path"}
			today = append(today, slices.Repeat([]string{"packages/<name>/notes.md: " + class + " in its path"}, tc.lines)...)
			want := tc.want(today)
			if !slices.Equal(got, want) {
				t.Fatalf("the report is %d lines ending %q, want %d lines ending %q", len(got), got[max(0, len(got)-2):], len(want), want[max(0, len(want)-2):])
			}
		})
	}
}
