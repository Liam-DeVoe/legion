package specgraph

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The history reads the trees of two commits and never their ancestry: main's commits are squash
// commits, so a branch commit is no ancestor of the main commit that holds its change. m2 is the
// squash of b1 onto m1b, so its tree is m1b's with b1's a/x.go, and the one path that differs
// between b1 and m2 is the docs/y.md that main changed. A history that walks parents from m2 back
// to b1 cannot reach b1 and fails this test.
func TestFixtureHistoryReadsTrees(t *testing.T) {
	t.Parallel()
	h, err := ReadFixtureHistory(strings.NewReader(`{"commits":[
		{"id":"m1","files":{"a/x.go":"1","docs/y.md":"1"}},
		{"id":"b1","parent":"m1","files":{"a/x.go":"2","docs/y.md":"1"}},
		{"id":"m1b","parent":"m1","files":{"a/x.go":"1","docs/y.md":"2"}},
		{"id":"m2","parent":"m1b","files":{"a/x.go":"2","docs/y.md":"2"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		from, to string
		want     []string
		ok       bool
	}{
		{"b1", "m2", []string{"docs/y.md"}, true},
		{"m2", "b1", []string{"docs/y.md"}, true},
		{"b1", "b1", nil, true},
		{"m1", "m2", []string{"a/x.go", "docs/y.md"}, true},
		{"b1", "gone", nil, false},
		{"gone", "b1", nil, false},
	} {
		got, ok := h.Changed(tc.from, tc.to)
		if ok != tc.ok || !slices.Equal(got, tc.want) {
			t.Errorf("Changed(%s, %s) = %q, %t; want %q, %t", tc.from, tc.to, got, ok, tc.want, tc.ok)
		}
	}
}

// A path that one tree holds and the other does not is changed, so a move lists both of its paths,
// as git diff --no-renames gives them.
func TestFixtureHistoryListsBothPathsOfAMove(t *testing.T) {
	t.Parallel()
	h := FixtureHistory{Commits: []FixtureCommit{
		{ID: "c1", Files: map[string]string{"p/a.go": "1", "p/data.json": "1"}},
		{ID: "c2", Parent: "c1", Files: map[string]string{"p/a.go": "1", "docs/data.json": "1"}},
	}}
	if got, ok := h.Changed("c1", "c2"); !ok || !slices.Equal(got, []string{"docs/data.json", "p/data.json"}) {
		t.Errorf("Changed(c1, c2) = %q, %t; want both paths of the move", got, ok)
	}
}

// ReadFixtureHistory reads each history.json of the scenarios, in the layout that the repository's
// JSON formatter gives them, and refuses what the strict JSON reader refuses (a field it does not
// know, one written in other letters or two times, a byte that is not UTF-8, text after the
// history, a value of another type, named with the form of history.json and its place), a commit
// with no id and an id on two commits. A path of a tree keeps its own letters.
func TestReadFixtureHistory(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob("testdata/scenarios/*/history.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no history.json under testdata/scenarios (%v)", err)
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		h, err := ReadFixtureHistory(strings.NewReader(string(content)))
		if err != nil || len(h.Commits) == 0 {
			t.Errorf("ReadFixtureHistory(%s) = %d commits, %v", path, len(h.Commits), err)
		}
	}
	for text, want := range map[string]string{
		`{"commits":[{"id":"c1","files":{},"tree":"x"}]}`:           `member "/commits/0/tree" is not in the format`,
		`{"commits":[],"branches":[]}`:                              `member "/branches" is not in the format`,
		`{"commits":[{"ID":"c1","files":{}}]}`:                      `member "/commits/0/ID" is not in the format`,
		`{"commits":[{"id":"c1","id":"c2","files":{}}]}`:            `member "/commits/0/id" is in one object two times`,
		`{"commits":[{"id":"c1","files":{"a.md":"1","a.md":"2"}}]}`: `member "/commits/0/files/a.md" is in one object two times`,
		"{\"commits\":[{\"id\":\"c\xff1\",\"files\":{}}]}":          "it is not UTF-8",
		`{"commits":[{"id":"c1","files":{}}]} {}`:                   "text after the JSON value",
		`{"commits":[{"files":{"a":"1"}}]}`:                         "a commit has no id",
		`{"commits":[{"id":"c1"},{"id":"c1","files":{}}]}`:          `commit "c1" is in the history two times`,
		`{"commits":"c1"}`:                                          `specgraph: history: it is not in the form {"commits":[{"id":"<commit>","parent":"<commit>","files":{"<repository path>":"<content label>"}}, …]}: the value at "/commits" is of another type`,
	} {
		_, err := ReadFixtureHistory(strings.NewReader(text))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ReadFixtureHistory(%s): %v, want %q", text, err, want)
		}
	}
	h, err := ReadFixtureHistory(strings.NewReader(`{"commits":[{"id":"c1","files":{"AGENTS.md":"1","docs/README.md":"1"}}]}`))
	if err != nil || !slices.Equal(slices.Sorted(maps.Keys(h.Commits[0].Files)), []string{"AGENTS.md", "docs/README.md"}) {
		t.Errorf("ReadFixtureHistory of paths in capitals: %+v, %v; want both paths as written", h, err)
	}
}

// GitHistory lists both paths of a moved file: git detects the move in this repository as a
// rename (the control: plain git diff --name-only names only the new path), and a history that
// gave git's rename view would miss the path that left a tie. A commit that git cannot read gives
// ok false, and so does a commit id that git would read as an option.
func TestGitHistoryListsBothPathsOfAMove(t *testing.T) {
	// t.Setenv, so the git commands of the test and of GitHistory read no configuration of the
	// machine; a test that calls t.Setenv cannot run in parallel.
	for name, value := range map[string]string{
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Author A",
		"GIT_AUTHOR_EMAIL":    "author-a@example.com",
		"GIT_COMMITTER_NAME":  "Author A",
		"GIT_COMMITTER_EMAIL": "author-a@example.com",
	} {
		t.Setenv(name, value)
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("p/data.json", `{"a":1}`+"\n")
	write("p/a.go", "package p\n")
	git("add", ".")
	git("commit", "-q", "-m", "A")
	a := git("rev-parse", "HEAD")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("mv", "p/data.json", "docs/data.json")
	git("commit", "-q", "-m", "B")
	b := git("rev-parse", "HEAD")

	if got := git("diff", "--name-only", a, b); got != "docs/data.json" {
		t.Fatalf("control: git diff --name-only A B = %q, want only the new path, so git reads the move as a rename", got)
	}
	h := GitHistory{Dir: dir}
	if got, ok := h.Changed(a, b); !ok || !slices.Equal(got, []string{"docs/data.json", "p/data.json"}) {
		t.Errorf("Changed(A, B) = %q, %t; want both paths of the move", got, ok)
	}
	if got, ok := h.Changed(a, a); !ok || len(got) != 0 {
		t.Errorf("Changed(A, A) = %q, %t; want no path, ok", got, ok)
	}
	if got, ok := h.Changed(a, "0000000000000000000000000000000000000000"); ok {
		t.Errorf("Changed(A, a commit git does not hold) = %q, ok", got)
	}
	leak := filepath.Join(dir, "leak")
	if got, ok := h.Changed("--output="+leak, b); ok {
		t.Errorf("Changed(an option, B) = %q, ok", got)
	}
	if _, err := os.Stat(leak); !os.IsNotExist(err) {
		t.Errorf("git read a commit id as an option and wrote %s (%v)", leak, err)
	}
}
