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

// GitHistory's answer is a fact of the two trees alone: no setting of the repository or of the
// machine changes it. A changed path that the list does not hold would carry a result across a
// change of its tie (R8), and R48 names the case: "a check whose test did not run although code
// that the check runs changed". Each row: a clone at dir with commits A and B, the row's setting,
// and the paths that differ between the trees of A and B, or ok false where the clone does not hold
// A. The git commands of the test read no configuration of the machine (GIT_CONFIG_GLOBAL,
// GIT_CONFIG_NOSYSTEM), and a row sets its own.
func TestGitHistoryIsTheSameUnderEachSetting(t *testing.T) {
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
	type clone struct {
		dir string
		t   *testing.T
	}
	git := func(c clone, args ...string) string {
		c.t.Helper()
		out, err := exec.Command("git", append([]string{"-C", c.dir}, args...)...).CombinedOutput()
		if err != nil {
			c.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(c clone, path, content string) {
		c.t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(c.dir, path)), 0o755); err != nil {
			c.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(c.dir, path), []byte(content), 0o600); err != nil {
			c.t.Fatal(err)
		}
	}
	commit := func(c clone, message string) string {
		c.t.Helper()
		git(c, "add", "-A")
		git(c, "commit", "-q", "--allow-empty", "-m", message)
		return git(c, "rev-parse", "HEAD")
	}
	const sub1, sub2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	// submodule commits A with the submodule sub at sub1 and .gitmodules with ignore, then B with sub
	// at sub2: a gitlink names a commit of the submodule, which this clone does not need to hold.
	submodule := func(c clone, ignore string) (string, string) {
		c.t.Helper()
		modules := "[submodule \"sub\"]\n\tpath = sub\n\turl = ./sub.git\n"
		if ignore != "" {
			modules += "\tignore = " + ignore + "\n"
		}
		write(c, ".gitmodules", modules)
		git(c, "update-index", "--add", "--cacheinfo", "160000,"+sub1+",sub")
		git(c, "add", ".gitmodules")
		git(c, "commit", "-q", "-m", "A")
		a := git(c, "rev-parse", "HEAD")
		git(c, "update-index", "--cacheinfo", "160000,"+sub2+",sub")
		git(c, "commit", "-q", "-m", "B")
		return a, git(c, "rev-parse", "HEAD")
	}
	for _, row := range []struct {
		name string
		make func(c clone) (a, b string, h GitHistory) // the commits, after the row's setting
		want []string
		ok   bool
	}{
		{"control: a moved submodule, with no setting", func(c clone) (string, string, GitHistory) {
			a, b := submodule(c, "")
			return a, b, GitHistory{Dir: c.dir}
		}, []string{"sub"}, true},
		{"a moved submodule whose committed .gitmodules sets ignore = all", func(c clone) (string, string, GitHistory) {
			a, b := submodule(c, "all")
			return a, b, GitHistory{Dir: c.dir}
		}, []string{"sub"}, true},
		{"a moved submodule whose .git/config sets ignore = all", func(c clone) (string, string, GitHistory) {
			a, b := submodule(c, "")
			git(c, "config", "submodule.sub.ignore", "all")
			return a, b, GitHistory{Dir: c.dir}
		}, []string{"sub"}, true},
		{"a moved submodule with diff.ignoreSubmodules = all", func(c clone) (string, string, GitHistory) {
			a, b := submodule(c, "")
			git(c, "config", "diff.ignoreSubmodules", "all")
			return a, b, GitHistory{Dir: c.dir}
		}, []string{"sub"}, true},
		{"names with a letter that is not ASCII, a space, a tab, a quote and a line feed, with core.quotePath = true", func(c clone) (string, string, GitHistory) {
			git(c, "config", "core.quotePath", "true")
			a := commit(c, "A")
			for _, name := range []string{"é.md", "a b.md", "a\tb.md", `a"b.md`, "a\nb.md"} {
				write(c, name, "1\n")
			}
			return a, commit(c, "B"), GitHistory{Dir: c.dir}
		}, []string{"a\tb.md", "a\nb.md", "a b.md", `a"b.md`, "é.md"}, true},
		{"a move with diff.renames = copies", func(c clone) (string, string, GitHistory) {
			git(c, "config", "diff.renames", "copies")
			write(c, "p/data.json", `{"a":1}`+"\n")
			a := commit(c, "A")
			git(c, "mv", "p/data.json", "docs.json")
			return a, commit(c, "B"), GitHistory{Dir: c.dir}
		}, []string{"docs.json", "p/data.json"}, true},
		{"a change of a letter's case with core.ignoreCase = true", func(c clone) (string, string, GitHistory) {
			git(c, "config", "core.ignoreCase", "true")
			write(c, "A.go", "package p\n")
			a := commit(c, "A")
			git(c, "mv", "A.go", "a.go")
			return a, commit(c, "B"), GitHistory{Dir: c.dir}
		}, []string{"A.go", "a.go"}, true},
		{"a .gitattributes line that makes each file no text, and a textconv that gives each one text", func(c clone) (string, string, GitHistory) {
			write(c, ".gitattributes", "* -diff\n*.txt diff=same\n")
			git(c, "config", "diff.same.textconv", "echo same")
			write(c, "a.bin", "1\n")
			write(c, "b.txt", "1\n")
			a := commit(c, "A")
			write(c, "a.bin", "2\n")
			write(c, "b.txt", "2\n")
			return a, commit(c, "B"), GitHistory{Dir: c.dir}
		}, []string{"a.bin", "b.txt"}, true},
		{"a replace ref that puts another commit in the place of B", func(c clone) (string, string, GitHistory) {
			write(c, "a.go", "1\n")
			a := commit(c, "A")
			write(c, "b.go", "1\n")
			b := commit(c, "B")
			write(c, "c.go", "1\n")
			git(c, "replace", b, commit(c, "C"))
			return a, b, GitHistory{Dir: c.dir}
		}, []string{"b.go"}, true},
		{"diff.relative = true, with the history at a directory of the clone", func(c clone) (string, string, GitHistory) {
			git(c, "config", "diff.relative", "true")
			write(c, "p/a.go", "1\n")
			write(c, "q.go", "1\n")
			a := commit(c, "A")
			write(c, "p/a.go", "2\n")
			write(c, "q.go", "2\n")
			return a, commit(c, "B"), GitHistory{Dir: filepath.Join(c.dir, "p")}
		}, []string{"p/a.go", "q.go"}, true},
		{"color.ui = always", func(c clone) (string, string, GitHistory) {
			git(c, "config", "color.ui", "always")
			git(c, "config", "color.diff", "always")
			a := commit(c, "A")
			write(c, "a.go", "1\n")
			return a, commit(c, "B"), GitHistory{Dir: c.dir}
		}, []string{"a.go"}, true},
		{"GIT_DIR of the process naming another clone", func(c clone) (string, string, GitHistory) {
			write(c, "a.go", "1\n")
			a := commit(c, "A")
			write(c, "b.go", "1\n")
			b := commit(c, "B")
			other := clone{dir: c.t.TempDir(), t: c.t}
			git(other, "init", "-q")
			c.t.Setenv("GIT_DIR", filepath.Join(other.dir, ".git"))
			return a, b, GitHistory{Dir: c.dir}
		}, []string{"b.go"}, true},
		{"a shallow clone that does not hold A", func(c clone) (string, string, GitHistory) {
			write(c, "a.go", "1\n")
			a := commit(c, "A")
			write(c, "b.go", "1\n")
			b := commit(c, "B")
			shallow := filepath.Join(c.t.TempDir(), "shallow")
			if out, err := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+c.dir, shallow).CombinedOutput(); err != nil {
				c.t.Fatalf("git clone --depth 1: %v\n%s", err, out)
			}
			return a, b, GitHistory{Dir: shallow}
		}, nil, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			c := clone{dir: t.TempDir(), t: t}
			git(c, "init", "-q")
			a, b, h := row.make(c)
			got, ok := h.Changed(a, b)
			if ok != row.ok || !slices.Equal(got, row.want) {
				t.Errorf("Changed(A, B) = %q, %t; want %q, %t", got, ok, row.want, row.ok)
			}
		})
	}
}
