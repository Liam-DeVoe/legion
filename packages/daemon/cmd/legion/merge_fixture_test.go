package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/jjmerge"
)

// mergeMessage is the -m every row gives.
const mergeMessage = `Merge "main"@origin into legion/ACME-1`

// The two revisions as a forward merge writes them: the issue's bookmark, and the base as a
// JSON-quoted jj string literal at origin.
const (
	mergeFirst  = "legion/ACME-1"
	mergeSecond = `"main"@origin`
)

// mergeOverlay is a pane's JJ_CONFIG overlay: jj writes its commit_trailers into every
// description it writes, the merge's among them.
const mergeOverlay = `[templates]
commit_trailers = '"Legion-Role: implementer"'
`

// noLongerReproduces is the failure of a control row: a plain `jj new` of the same parents no
// longer gives jj's wrong merge, so the decision that roles merge with git's merge rests on a
// measurement this jj no longer makes.
const noLongerReproduces = "the pinned jj no longer reproduces jj-vcs/jj#6369: read the decision of dispatch://INFRA-4149 (merge with git's merge-tree) again"

// mergeRepo is one row's colocated jj repository: its workspace, the home and the overlay every
// command of the row runs under, and the bare remote origin where the row publishes main.
type mergeRepo struct {
	t       *testing.T
	dir     string
	home    string
	overlay string
	ws      string
	fixture fixturegit.Runner
}

// newMergeRepo is an empty colocated repository at <dir>/workspace. A missing git or jj fails the
// row: CI's daemon-go job installs jj at the image's pin, and a row that skipped would pass there
// on nothing.
func newMergeRepo(t *testing.T) *mergeRepo {
	t.Helper()
	for _, tool := range []string{"git", "jj"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("legion merge's rows drive a real %s: %v", tool, err)
		}
	}
	dir := t.TempDir()
	r := &mergeRepo{t: t, dir: dir, home: filepath.Join(dir, "home"), overlay: filepath.Join(dir, "overlay.toml"), ws: filepath.Join(dir, "workspace")}
	if err := os.Mkdir(r.home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.overlay, []byte(mergeOverlay), 0o600); err != nil {
		t.Fatal(err)
	}
	r.fixture = fixturegit.Isolated(t, r.home)
	r.fixture.Run(dir, fixturegit.JJInit(r.ws, true)...)
	r.jj("config", "set", "--repo", "git.abandon-unreachable-commits", "false")
	return r
}

// jj runs a fixture's jj command in the workspace and answers its output, trimmed.
func (r *mergeRepo) jj(args ...string) string {
	r.t.Helper()
	return r.fixture.Run(r.ws, append([]string{"jj"}, args...)...)
}

// read is a jj command that reads the repository as it stands and changes nothing, its standard
// output alone and untrimmed: it neither snapshots the working copy nor imports git's refs.
func (r *mergeRepo) read(args ...string) string {
	r.t.Helper()
	return r.fixture.Out(r.ws, append([]string{"jj", "--ignore-working-copy"}, args...)...)
}

// git runs a fixture's git command in the workspace and answers its output, trimmed.
func (r *mergeRepo) git(args ...string) string {
	r.t.Helper()
	return r.fixture.Run(r.ws, append([]string{"git"}, args...)...)
}

// carrierIdentity is the author and committer of each carrier a run writes with git plumbing
// (jjmerge's carrierIdentity). The sweep takes a commit of another author for no carrier.
var carrierIdentity = []string{
	"GIT_AUTHOR_NAME=legion merge", "GIT_AUTHOR_EMAIL=legion-merge@localhost",
	"GIT_COMMITTER_NAME=legion merge", "GIT_COMMITTER_EMAIL=legion-merge@localhost",
}

// writeCarrier writes a commit of tree with no parent under carrierIdentity, as a run writes a
// carrier, and answers its id.
func (r *mergeRepo) writeCarrier(tree, message string) string {
	r.t.Helper()
	return r.fixture.With(carrierIdentity...).Run(r.ws, "git", "commit-tree", tree, "-m", message)
}

// commit makes the commit name, a child of parents holding files and without removed, with name
// as its description and its bookmark.
func (r *mergeRepo) commit(name string, parents []string, files map[string]string, removed ...string) {
	r.t.Helper()
	r.jj(append(append([]string{"new"}, parents...), "-m", name)...)
	for path, content := range files {
		path = filepath.Join(r.ws, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	for _, path := range removed {
		if err := os.Remove(filepath.Join(r.ws, path)); err != nil {
			r.t.Fatal(err)
		}
	}
	r.jj("bookmark", "create", name, "-r", "@")
}

// publish pushes the commit name as main to origin, a bare repository beside the workspace, and
// fetches it, so "main"@origin names it as the base of a forward merge.
func (r *mergeRepo) publish(name string) {
	r.t.Helper()
	bare := filepath.Join(r.dir, "origin.git")
	if _, err := os.Stat(bare); err != nil {
		r.fixture.Run(r.dir, fixturegit.InitBare(bare, "main")...)
		r.git("remote", "add", "origin", bare)
	}
	r.git("push", "--quiet", "origin", r.id(name)+":refs/heads/main")
	r.jj("git", "fetch", "--remote", "origin")
}

// workOn puts the working copy on a new commit on base that holds a file of the role's own work,
// which no merge may touch.
func (r *mergeRepo) workOn(base string) {
	r.t.Helper()
	r.jj("new", base)
	if err := os.WriteFile(filepath.Join(r.ws, "wip"), []byte("work in progress\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.jj("status")
}

// id is the commit id revset names.
func (r *mergeRepo) id(revset string) string {
	r.t.Helper()
	return r.template(revset, "commit_id")
}

// template is one template's value for the one commit revset names.
func (r *mergeRepo) template(revset, template string) string {
	r.t.Helper()
	return r.read("log", "--no-graph", "-r", revset, "-T", template)
}

// show is what path holds in revset, its conflict as jj materializes it in git's marker style
// where it holds one.
func (r *mergeRepo) show(revset, path string) string {
	r.t.Helper()
	return r.read("--config=ui.conflict-marker-style=git", "file", "show", "-r", revset, "--", path)
}

// visible is every visible commit's id, sorted.
func (r *mergeRepo) visible() []string {
	r.t.Helper()
	return slices.Sorted(slices.Values(strings.Fields(r.read("log", "--no-graph", "-r", "all()", "-T", `commit_id ++ "\n"`))))
}

// leftovers is every bookmark and git ref of the repository that is a merge's own.
func (r *mergeRepo) leftovers() []string {
	r.t.Helper()
	var left []string
	for _, name := range strings.Fields(r.read("bookmark", "list", "--all", "-T", `name ++ "@" ++ remote ++ "\n"`)) {
		if strings.Contains(name, "legion-merge-") {
			left = append(left, "bookmark "+name)
		}
	}
	for _, ref := range strings.Fields(r.git("for-each-ref", "--format=%(refname)")) {
		if strings.Contains(ref, "legion-merge-") {
			left = append(left, "ref "+ref)
		}
	}
	return left
}

// gitTree is the first line `git merge-tree --write-tree` prints for first and second with the
// merge's option list: git's merged tree.
func (r *mergeRepo) gitTree(first, second string) string {
	r.t.Helper()
	args := append(append([]string{"git"}, jjmerge.MergeTreeOptions()...), "merge-tree", "--write-tree", r.id(first), r.id(second))
	out, err := r.fixture.Try(r.ws, args...)
	if line, _, _ := strings.Cut(out, "\n"); err == nil || len(line) == 40 {
		return line
	}
	r.t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	return ""
}

// mergeBases is how many merge bases git finds for first and second.
func (r *mergeRepo) mergeBases(first, second string) int {
	r.t.Helper()
	return len(strings.Fields(r.git("merge-base", "--all", r.id(first), r.id(second))))
}

// plainMerge is the merge a role made before legion merge, `jj new --no-edit` of the same
// parents: what it holds at path, and whether it holds a conflict.
func (r *mergeRepo) plainMerge(first, second, path string) (content, conflict string) {
	r.t.Helper()
	firstID, secondID := r.id(first), r.id(second)
	r.jj("new", "--no-edit", firstID, secondID, "-m", "plain jj new")
	plain := fmt.Sprintf(`children(%s) & children(%s) & description(substring:"plain jj new")`, firstID, secondID)
	return r.show(plain, path), r.template(plain, "conflict")
}

// env is the environment of the row's every merge: the fixture's home, configuration and
// identity, and the pane's overlay.
func (r *mergeRepo) env() []string {
	return append(fixtureenv.Isolated(r.home), "JJ_CONFIG="+r.overlay)
}

// binary runs the built legion's merge in dir with args, as a role's shell runs it, and answers
// its exit status and what it printed on each stream.
func (r *mergeRepo) binary(dir string, args ...string) (int, string, string) {
	r.t.Helper()
	cmd := exec.Command(buildLegion(r.t), append([]string{"merge"}, args...)...)
	cmd.Dir, cmd.Env = dir, r.env()
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		r.t.Fatalf("run legion merge: %v", err)
	}
	return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String()
}

// setEnv sets the row's environment on this process until the row ends, for the merge this
// process runs: its children inherit it.
func (r *mergeRepo) setEnv() {
	for _, entry := range r.env() {
		key, value, _ := strings.Cut(entry, "=")
		r.t.Setenv(key, value)
	}
}

// merge runs `legion merge args…` in this process, as run reads a command line, and answers its
// exit status and what it printed on each stream.
func (r *mergeRepo) merge(args ...string) (int, string, string) {
	r.t.Helper()
	r.setEnv()
	var stdout, stderr strings.Builder
	code := run(context.Background(), append([]string{"legion", "merge"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// minimalCase is the minimal case of jj-vcs/jj#6369: f holds c at base; b1, b2 and b3 each
// change it to b and add a file of their own; legion/ACME-1 merges them in one order and main,
// at origin, in another, so the two have `bases` merge bases, b1 and b2 or all three. git's merge
// holds b; jj's holds c.
func (r *mergeRepo) minimalCase(bases int) {
	r.t.Helper()
	r.commit("base", []string{"root()"}, map[string]string{"f": "c\n"})
	for i := 1; i <= 3; i++ {
		r.commit("b"+strconv.Itoa(i), []string{"base"}, map[string]string{"f": "b\n", "own" + strconv.Itoa(i): strconv.Itoa(i) + "\n"})
	}
	first, second := []string{"b1", "b2"}, []string{"b2", "b1"}
	if bases == 3 {
		first, second = []string{"b1", "b2", "b3"}, []string{"b3", "b2", "b1"}
	}
	r.commit(mergeFirst, first, nil)
	r.commit("main", second, nil)
	r.publish("main")
	r.workOn("base")
	if got := r.mergeBases(mergeFirst, "main"); got != bases {
		r.t.Fatalf("the history has %d merge bases, want %d", got, bases)
	}
}

// contentConflict is one merge base and a conflict git and jj both see: base holds base in f,
// legion/ACME-1 ours and main theirs.
func (r *mergeRepo) contentConflict() {
	r.t.Helper()
	r.commit("base", []string{"root()"}, map[string]string{"f": "base\n", "g": "same\n"})
	r.commit(mergeFirst, []string{"base"}, map[string]string{"f": "ours\n"})
	r.commit("main", []string{"base"}, map[string]string{"f": "theirs\n"})
	r.publish("main")
	r.workOn("base")
}

// renameAndEdit is B4's history: legion/ACME-1 renames r to r2, main edits r.
func (r *mergeRepo) renameAndEdit() {
	r.t.Helper()
	text := "1\n2\n3\n4\n5\n6\n7\n8\n"
	r.commit("base", []string{"root()"}, map[string]string{"r": text})
	r.commit(mergeFirst, []string{"base"}, map[string]string{"r2": text}, "r")
	r.commit("main", []string{"base"}, map[string]string{"r": strings.Replace(text, "8\n", "eight\n", 1)})
	r.publish("main")
	r.workOn("base")
}

// wantMergeHolds fails the row unless the working copy is an empty commit on one merge of first
// and second whose path holds content with no conflict.
func (r *mergeRepo) wantMergeHolds(first, second, path, content string) {
	r.t.Helper()
	r.wantWorkingCopyOnMerge()
	if got, want := r.template("@-", `parents.map(|c| c.commit_id()).join(" ")`), r.id(first)+" "+r.id(second); got != want {
		r.t.Errorf("the merge's parents are %q, want %s then %s: %q", got, first, second, want)
	}
	if got := r.show("@-", path); got != content {
		r.t.Errorf("the merge holds %q at %s, want git's %q", got, path, content)
	}
	if got := r.template("@-", "conflict"); got != "false" {
		r.t.Errorf("jj log -r @- -T conflict = %q, want false", got)
	}
}

// wantWorkingCopyOnMerge fails the row unless the working copy is an empty commit whose one
// parent is the merge, @-.
func (r *mergeRepo) wantWorkingCopyOnMerge() {
	r.t.Helper()
	if got := r.template("@", `empty ++ " " ++ parents.map(|c| c.commit_id()).join(" ")`); got != "true "+r.id("@-") {
		r.t.Errorf("the working copy is %q, want an empty commit whose one parent is the merge", got)
	}
}

// wantNothingMade fails the row unless the repository is as before shows it: the same visible
// commits, the working copy on the same commit with the role's file as it was, and no bookmark
// or git ref of a merge.
func (r *mergeRepo) wantNothingMade(before mergeSnapshot) {
	r.t.Helper()
	after := r.snapshot()
	if !slices.Equal(after.visible, before.visible) {
		r.t.Errorf("the visible commits are now\n%q\nwant those before the command\n%q", after.visible, before.visible)
	}
	if after.at != before.at {
		r.t.Errorf("the working copy is on %s, want %s, where it was", after.at, before.at)
	}
	if got, err := os.ReadFile(filepath.Join(r.ws, "wip")); err != nil || string(got) != "work in progress\n" {
		r.t.Errorf("the working copy's file wip holds %q (%v), want what the role wrote", got, err)
	}
	if left := r.leftovers(); len(left) > 0 {
		r.t.Errorf("the command left %q", left)
	}
}

// mergeSnapshot is what wantNothingMade compares.
type mergeSnapshot struct {
	visible []string
	at      string
}

// snapshot reads the repository after jj has read the working copy, as a role's next command
// would.
func (r *mergeRepo) snapshot() mergeSnapshot {
	r.t.Helper()
	r.jj("status")
	return mergeSnapshot{visible: r.visible(), at: r.id("@")}
}

// mergeNextStep is the last lines of exit 1, for a merge that holds a jj conflict at each of
// paths and no path that jj merged.
func mergeNextStep(ws, change string, paths ...string) string {
	return "Next step:\n" +
		"  1. Resolve each jj conflict in the working-copy commit.\n" +
		"  2. Move the resolution into the merge with this command:\n" +
		"     " + squashCommand(ws, change, paths) + "\n" +
		"  3. Push the merge.\n" +
		"If you keep a part of the resolution for a later turn, that part stays in a commit above the merge.\n" +
		"Then jj git push refuses the merge, because the merge still holds the conflict.\n" +
		"The command in step 2 moves the resolution into the merge.\n"
}

// wantMinimalCaseMerged runs the built binary on minimalCase's history and holds B1's result,
// with its control: a plain jj new of the same parents holds c with no conflict.
func wantMinimalCaseMerged(t *testing.T, r *mergeRepo) {
	t.Helper()
	code, stdout, stderr := r.binary(r.ws, "-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 0 || stderr != "" {
		t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
	if content, conflict := r.plainMerge(mergeFirst, "main", "f"); content != "c\n" || conflict != "false" {
		t.Errorf("plain jj new of the same parents holds %q with conflict %s, want c with none: %s", content, conflict, noLongerReproduces)
	}
}

// wantRenameFollowed runs the merge on renameAndEdit's history and holds B4's result.
func wantRenameFollowed(t *testing.T, r *mergeRepo) {
	t.Helper()
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 0 {
		t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	r.wantMergeHolds(mergeFirst, "main", "r2", "1\n2\n3\n4\n5\n6\n7\neight\n")
	if got := strings.Fields(r.read("file", "list", "-r", "@-")); !slices.Equal(got, []string{"r2"}) {
		t.Errorf("the merge holds the files %q, want r2 alone", got)
	}
}
