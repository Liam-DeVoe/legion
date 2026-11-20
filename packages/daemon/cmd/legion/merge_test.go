package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/jjmerge"
)

// The rows of dispatch://INFRA-4149's behaviour table for `legion merge`, B1 to B17 and H1. Each
// builds its history in a repository of its own under t.TempDir() with the real git and jj, and
// the rows that make a merge run it as a role's pane runs it: a JJ_CONFIG overlay whose
// commit_trailers writes the role's line, and git.abandon-unreachable-commits=false in the
// repository's configuration, as the production clones have it. B1, B2, B3, B7 and B15 run the
// built binary with the role prompt's own argument shapes; the others run the command in this
// process (run), whose children inherit the environment the row sets.

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

// B1: the minimal case with two merge bases, through the built binary. The merge holds git's b
// with no conflict, where a plain jj new of the same parents holds c.
func TestLegionMergeHoldsGitsMergeOverTwoMergeBases(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(2)
	wantMinimalCaseMerged(t, r)
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

// B2: the minimal case with three merge bases, through the built binary: as B1.
func TestLegionMergeHoldsGitsMergeOverThreeMergeBases(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(3)
	wantMinimalCaseMerged(t, r)
}

// B3: one merge base and a content conflict, through the built binary. The merge holds a jj
// conflict whose sides are git's stage 2 and 3 over git's stage 1, the output names the path and
// git's message, and its last lines are the next step with the merge's change id.
func TestLegionMergeShowsGitsContentConflictAsAJJConflict(t *testing.T) {
	r := newMergeRepo(t)
	r.contentConflict()
	code, stdout, stderr := r.binary(r.ws, "-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 1 || stderr != "" {
		t.Fatalf("legion merge = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := strings.Fields(r.read("resolve", "-r", "@-", "--list")); !slices.Equal(got, []string{"f", "2-sided", "conflict"}) {
		t.Errorf("jj resolve -r @- --list = %q, want f alone", got)
	}
	if got := r.show("@-", "f"); !regexp.MustCompile(`^<<<<<<< .*\nours\n\|\|\|\|\|\|\| .*\nbase\n=======\ntheirs\n>>>>>>> .*\n$`).MatchString(got) {
		t.Errorf("the merge's f is\n%s\nwant a conflict of git's ours and theirs over git's base", got)
	}
	for _, want := range []string{"  CONFLICT (content): Merge conflict in f\n", "  f: a jj conflict\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if want := mergeNextStep(r.ws, r.template("@-", "change_id"), "f"); !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout ends\n%s\nwant it to end with the next step\n%s", stdout, want)
	}
}

// B4: one side renames r to r2, the other edits r. git follows the rename, so the merge holds
// r2 with the edit and no r, with no conflict. A plain jj new conflicts at r.
func TestLegionMergeFollowsARenameJJConflictsOn(t *testing.T) {
	r := newMergeRepo(t)
	r.renameAndEdit()
	wantRenameFollowed(t, r)
	if _, conflict := r.plainMerge(mergeFirst, "main", "r"); conflict != "true" {
		t.Errorf("plain jj new of the same parents holds no conflict, want one at r: the pinned jj no longer conflicts here, so read the decision of dispatch://INFRA-4149 again")
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

// B5: several merge bases, a file jj alone reverts and a file git conflicts in. The first holds
// git's text, and the output names it as a path where jj's own merge differed; the second is a
// jj conflict with git's sides.
func TestLegionMergeKeepsGitsTextBesideAConflict(t *testing.T) {
	r := newMergeRepo(t)
	r.commit("base", []string{"root()"}, map[string]string{"f": "c\n", "g": "base\n"})
	r.commit("b1", []string{"base"}, map[string]string{"f": "b\n", "own1": "1\n"})
	r.commit("b2", []string{"base"}, map[string]string{"f": "b\n", "own2": "2\n"})
	r.commit(mergeFirst, []string{"b1", "b2"}, map[string]string{"g": "one\n"})
	r.commit("main", []string{"b2", "b1"}, map[string]string{"g": "two\n"})
	r.publish("main")
	r.workOn("base")
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 1 {
		t.Fatalf("legion merge = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := r.show("@-", "f"); got != "b\n" {
		t.Errorf("the merge holds %q at f, want git's b", got)
	}
	if !strings.Contains(stdout, "The merge holds git's merge at each path:\n  f\n") {
		t.Errorf("stdout does not name f as a path where jj's own merge differed:\n%s", stdout)
	}
	if got := strings.Fields(r.read("resolve", "-r", "@-", "--list")); !slices.Equal(got, []string{"g", "2-sided", "conflict"}) {
		t.Errorf("jj resolve -r @- --list = %q, want g alone", got)
	}
	if got := r.show("@-", "g"); !regexp.MustCompile(`^<<<<<<< .*\none\n\|\|\|\|\|\|\| .*\nbase\n=======\ntwo\n>>>>>>> .*\n$`).MatchString(got) {
		t.Errorf("the merge's g is\n%s\nwant a conflict of git's one and two over git's base", got)
	}
}

// B6: several merge bases, one file with a hunk git conflicts on and a second hunk jj alone
// reverts. Outside the conflict's region the file holds git's text.
func TestLegionMergeKeepsGitsTextOutsideAConflictRegion(t *testing.T) {
	r := newMergeRepo(t)
	filler := strings.Repeat("filler\n", 10)
	r.commit("base", []string{"root()"}, map[string]string{"h": "A=c\n" + filler + "B=base\n"})
	r.commit("b1", []string{"base"}, map[string]string{"h": "A=b\n" + filler + "B=base\n", "own1": "1\n"})
	r.commit("b2", []string{"base"}, map[string]string{"h": "A=b\n" + filler + "B=base\n", "own2": "2\n"})
	r.commit(mergeFirst, []string{"b1", "b2"}, map[string]string{"h": "A=b\n" + filler + "B=one\n"})
	r.commit("main", []string{"b2", "b1"}, map[string]string{"h": "A=b\n" + filler + "B=two\n"})
	r.publish("main")
	r.workOn("base")
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 1 {
		t.Fatalf("legion merge = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	want := regexp.MustCompile(`^A=b\n` + filler + `<<<<<<< .*\nB=one\n\|\|\|\|\|\|\| .*\nB=base\n=======\nB=two\n>>>>>>> .*\n$`)
	if got := r.show("@-", "h"); !want.MatchString(got) {
		t.Errorf("the merge's h is\n%s\nwant git's A=b outside a conflict of B=one and B=two over B=base", got)
	}
}

// B7: two merges of the same two commits that resolve them differently. git conflicts at f, and
// the merge holds a jj conflict there; a plain jj new holds the base's x with no conflict.
func TestLegionMergeShowsTheConflictOfTwoResolutions(t *testing.T) {
	r := newMergeRepo(t)
	r.commit("base", []string{"root()"}, map[string]string{"f": "x\n"})
	r.commit("y", []string{"base"}, map[string]string{"f": "y\n"})
	r.commit("z", []string{"base"}, map[string]string{"f": "z\n"})
	r.commit(mergeFirst, []string{"y", "z"}, map[string]string{"f": "y\n"})
	r.commit("main", []string{"y", "z"}, map[string]string{"f": "z\n"})
	r.publish("main")
	r.workOn("base")
	code, stdout, stderr := r.binary(r.ws, "-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 1 || stderr != "" {
		t.Fatalf("legion merge = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := strings.Fields(r.read("resolve", "-r", "@-", "--list")); len(got) == 0 || got[0] != "f" || len(got) != 3 {
		t.Errorf("jj resolve -r @- --list = %q, want a conflict at f alone", got)
	}
	if content, conflict := r.plainMerge(mergeFirst, "main", "f"); content != "x\n" || conflict != "false" {
		t.Errorf("plain jj new of the same parents holds %q with conflict %s, want x with none: %s", content, conflict, noLongerReproduces)
	}
}

// B8: a rename/rename conflict. The merge's tree is git's, g and h and no f, and the output names
// the three paths, git's message, and each path as a conflict of git's that jj merged.
func TestLegionMergeNamesARenameConflictJJMerged(t *testing.T) {
	r := newMergeRepo(t)
	text := "one\ntwo\nthree\n"
	r.commit("base", []string{"root()"}, map[string]string{"f": text})
	r.commit(mergeFirst, []string{"base"}, map[string]string{"g": text}, "f")
	r.commit("main", []string{"base"}, map[string]string{"h": text}, "f")
	r.publish("main")
	r.workOn("base")
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 1 {
		t.Fatalf("legion merge = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got, want := r.git("rev-parse", r.id("@-")+"^{tree}"), r.gitTree(mergeFirst, "main"); got != want {
		t.Errorf("the merge's tree is %s, want git's %s", got, want)
	}
	if got := strings.Fields(r.read("file", "list", "-r", "@-")); !slices.Equal(got, []string{"g", "h"}) {
		t.Errorf("the merge holds the files %q, want g and h", got)
	}
	if !regexp.MustCompile(`\n  CONFLICT \(rename/rename\): f renamed to g in [0-9a-f]+ and to h in [0-9a-f]+\.\n`).MatchString(stdout) {
		t.Errorf("stdout lacks git's rename/rename message:\n%s", stdout)
	}
	for _, path := range []string{"f", "g", "h"} {
		if want := "\n  " + path + ": a conflict of git's that jj merged\n"; !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

// B9: both sides change a binary file, and the merge holds a jj conflict at it.
func TestLegionMergeShowsABinaryConflict(t *testing.T) {
	r := newMergeRepo(t)
	r.commit("base", []string{"root()"}, map[string]string{"bin": "bin\x00ary\nbase\n"})
	r.commit(mergeFirst, []string{"base"}, map[string]string{"bin": "bin\x00ary\nours\n"})
	r.commit("main", []string{"base"}, map[string]string{"bin": "bin\x00ary\ntheirs\n"})
	r.publish("main")
	r.workOn("base")
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 1 {
		t.Fatalf("legion merge = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := strings.Fields(r.read("resolve", "-r", "@-", "--list")); len(got) == 0 || got[0] != "bin" {
		t.Errorf("jj resolve -r @- --list = %q, want a conflict at bin", got)
	}
	if !strings.Contains(stdout, "\n  bin: a jj conflict\n") {
		t.Errorf("stdout does not name bin as a jj conflict:\n%s", stdout)
	}
}

// stepRunner is jjmerge.Exec but for the first command step picks out, which fail answers in its
// place: a merge that fails at that one step.
type stepRunner struct {
	step func(args []string, ran [][]string) bool
	fail func(ctx context.Context, c jjmerge.Command) ([]byte, error)
	ran  [][]string
	hit  bool
}

func (s *stepRunner) Run(ctx context.Context, c jjmerge.Command) ([]byte, error) {
	hit := !s.hit && s.step(c.Args, s.ran)
	s.ran = append(s.ran, c.Args)
	if hit {
		s.hit = true
		return s.fail(ctx, c)
	}
	return jjmerge.Exec{}.Run(ctx, c)
}

// B10: a merge that fails after its carriers exist exits 3 and leaves the repository as it found
// it: no new visible commit, the working copy where it was with its content, no bookmark or git
// ref of its own, no carrier visible.
func TestLegionMergeUndoesAFailedMerge(t *testing.T) {
	restored := func(ran [][]string) bool {
		return slices.ContainsFunc(ran, func(args []string) bool { return slices.Contains(args, "restore") })
	}
	for _, tc := range []struct {
		name string
		step func(args []string, ran [][]string) bool
		fail func(ctx context.Context, c jjmerge.Command) ([]byte, error)
		want string
	}{
		{
			name: "the restore fails",
			step: func(args []string, _ [][]string) bool { return slices.Contains(args, "restore") },
			fail: func(_ context.Context, c jjmerge.Command) ([]byte, error) {
				return nil, &jjmerge.ExitError{Args: c.Args, Code: 1, Stderr: "the injected failure of the restore"}
			},
			want: "the injected failure of the restore",
		},
		{
			name: "the read-back lists a path git does not conflict in",
			step: func(args []string, ran [][]string) bool { return slices.Contains(args, "diff") && restored(ran) },
			fail: func(ctx context.Context, c jjmerge.Command) ([]byte, error) {
				out, err := jjmerge.Exec{}.Run(ctx, c)
				return append(out, "not-a-conflict\x00not-a-conflict\x00"...), err
			},
			want: `the merge differs from git's merge at paths git does not conflict in: "not-a-conflict"`,
		},
		{
			name: "jj new of the merge fails",
			step: func(args []string, _ [][]string) bool {
				return slices.Contains(args, "new") && slices.Contains(args, "--message="+mergeMessage)
			},
			fail: func(_ context.Context, c jjmerge.Command) ([]byte, error) {
				return nil, &jjmerge.ExitError{Args: c.Args, Code: 1, Stderr: "the injected failure of jj new"}
			},
			want: "the injected failure of jj new",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRepo(t)
			r.contentConflict()
			r.setEnv()
			before := r.snapshot()
			runner := &stepRunner{step: tc.step, fail: tc.fail}
			var stdout, stderr strings.Builder
			code := runMerge(context.Background(), runner, r.ws, mergeMessage, []string{mergeFirst, mergeSecond}, &stdout, &stderr)
			if !runner.hit {
				t.Fatalf("the merge never reached the step that fails; it ran %q", runner.ran)
			}
			if code != 3 || !strings.Contains(stderr.String(), tc.want) || !strings.HasSuffix(stderr.String(), mergeRefusedNextStep) {
				t.Errorf("legion merge = %d, want 3 naming %q and ending with the next step\nstdout:\n%s\nstderr:\n%s", code, tc.want, stdout.String(), stderr.String())
			}
			r.wantNothingMade(before)
		})
	}
}

// B11: after a clean merge (B1) and after one with conflicts (B3), the repository holds the
// commits it held before, the merge and the working copy on it, and nothing of the merge's own:
// no bookmark or git ref, no carrier and no jj merge of carriers.
func TestLegionMergeLeavesNoCarrier(t *testing.T) {
	for _, tc := range []struct {
		name    string
		history func(r *mergeRepo)
		code    int
	}{
		{"after B1", func(r *mergeRepo) { r.minimalCase(2) }, 0},
		{"after B3", (*mergeRepo).contentConflict, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRepo(t)
			tc.history(r)
			before := r.snapshot()
			if code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond); code != tc.code {
				t.Fatalf("legion merge = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.code, stdout, stderr)
			}
			if left := r.leftovers(); len(left) > 0 {
				t.Errorf("the merge left %q", left)
			}
			r.wantWorkingCopyOnMerge()
			want := slices.Sorted(slices.Values(append(slices.Clone(before.visible), r.id("@-"), r.id("@"))))
			if got := r.visible(); !slices.Equal(got, want) {
				t.Errorf("the visible commits are\n%q\nwant those before the command, the merge and the working copy\n%q", got, want)
			}
			heads := r.read("log", "--no-graph", "-r", "visible_heads()", "-T", `author.email() ++ " " ++ description.first_line() ++ "\n"`)
			for _, head := range strings.Split(strings.TrimSpace(heads), "\n") {
				if strings.HasPrefix(head, "legion-merge@localhost ") || strings.Contains(head, "jj's merge of git's versions") {
					t.Errorf("visible_heads() holds %q, a carrier or a jj merge of carriers", head)
				}
			}
		})
	}
}

// B12: the merge's option list closes each setting of the machine's and the repository's
// configuration that changes git's merge: git's merge options, the default merge driver
// (merge.default) and the user's attributes file (core.attributesFile). B4's history with
// merge.renames=false and merge.conflictStyle=diff3 in the repository's configuration gives B4's
// result. B3's history with merge.default=union and an attributes file of `* merge=union` gives
// B3's jj conflict at f, where git's own merge under either setting alone is clean. The list does
// not close the repository's own attributes, as for any git merge in it: a .gitattributes that git
// reads in a git worktree, $GIT_DIR/info/attributes and the system attributes file still choose a
// path's merge driver.
func TestLegionMergeReadsNoMergeConfiguration(t *testing.T) {
	t.Run("git's merge options", func(t *testing.T) {
		r := newMergeRepo(t)
		r.renameAndEdit()
		r.git("config", "merge.renames", "false")
		r.git("config", "merge.conflictStyle", "diff3")
		wantRenameFollowed(t, r)
	})
	t.Run("the default merge driver and the user's attributes file", func(t *testing.T) {
		r := newMergeRepo(t)
		r.contentConflict()
		attributes := filepath.Join(r.dir, "attributes")
		if err := os.WriteFile(attributes, []byte("* merge=union\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r.git("config", "merge.default", "union")
		r.git("config", "core.attributesFile", attributes)
		// The controls: git's own merge is clean under each setting alone, so each of the two
		// options is what makes the merge below conflict.
		for _, closed := range []string{"merge.default=text", "core.attributesFile=/dev/null"} {
			if out, err := r.fixture.Try(r.ws, "git", "-c", closed, "merge-tree", "--write-tree", r.id(mergeFirst), r.id("main")); err != nil {
				t.Fatalf("git -c %s merge-tree = %v, want a clean merge under the other setting alone:\n%s", closed, err, out)
			}
		}
		code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
		if code != 1 {
			t.Fatalf("legion merge = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if got := strings.Fields(r.read("resolve", "-r", "@-", "--list")); !slices.Equal(got, []string{"f", "2-sided", "conflict"}) {
			t.Errorf("jj resolve -r @- --list = %q, want f alone", got)
		}
	})
}

// B13: B3's merge, pushed with its conflict, is refused. The role resolves f in the working
// copy, describes it, starts a new turn with jj new, and another role's file lands in the new
// working copy; the next step, run as the output gives it, moves the resolution and nothing else
// into the merge, which keeps its change id and description, and the push is accepted.
func TestLegionMergeNextStepMovesTheResolutionIntoTheMerge(t *testing.T) {
	r := newMergeRepo(t)
	r.contentConflict()
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 1 {
		t.Fatalf("legion merge = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	change := r.template("@-", "change_id")
	description := r.template("@-", "description")
	r.jj("bookmark", "create", "merged", "-r", change)
	if out, err := r.fixture.With("JJ_CONFIG="+r.overlay).Try(r.ws, "jj", "git", "push", "--bookmark", "merged"); err == nil || !strings.Contains(out, "since it has conflicts") {
		t.Fatalf("jj git push of the merge with its conflict = %v, want it refused since it has conflicts:\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(r.ws, "f"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.jj("describe", "-m", "wip")
	r.jj("new")
	if err := os.WriteFile(filepath.Join(r.ws, "other"), []byte("another role's file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.jj("status")
	var squash string
	for line := range strings.Lines(stdout) {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "jj -R ") && strings.Contains(line, " squash ") {
			squash = line
		}
	}
	if squash == "" {
		t.Fatalf("stdout gives no squash command:\n%s", stdout)
	}
	sh := exec.Command("sh", "-c", squash)
	sh.Dir, sh.Env = r.ws, r.env()
	if out, err := sh.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", squash, err, out)
	}
	if got := r.template(change, `parents.len() ++ " " ++ conflict`); got != "2 false" {
		t.Errorf("the change %s is %q, want the merge, two parents and no conflict", change, got)
	}
	if got := r.show(change, "f"); got != "resolved\n" {
		t.Errorf("the merge holds %q at f, want the resolution", got)
	}
	if got := strings.Fields(r.read("file", "list", "-r", change)); slices.Contains(got, "other") {
		t.Errorf("the merge holds the files %q, want no other: only the resolution goes into the merge", got)
	}
	if got := r.show("@", "other"); got != "another role's file\n" {
		t.Errorf("the working copy holds %q at other, want the other role's file", got)
	}
	if got := r.template(change, "description"); got != description {
		t.Errorf("the merge's description is now\n%s\nwant the one the merge wrote\n%s", got, description)
	}
	comparison := exec.Command("sh", "-c", mergeComparison(r.id(change), "--name-only"))
	comparison.Dir, comparison.Env = r.ws, r.env()
	if out, err := comparison.Output(); err != nil || string(out) != "f\n" {
		t.Errorf("the comparison with git's merge lists %q (%v), want f alone", out, err)
	}
	if out, err := r.fixture.With("JJ_CONFIG="+r.overlay).Try(r.ws, "jj", "git", "push", "--bookmark", "merged"); err != nil {
		t.Errorf("jj git push of the resolved merge: %v\n%s", err, out)
	}
}

// B14: each refusal exits 3 naming its reason, with the next step as its last lines, and each
// usage error exits 2; none makes anything.
func TestLegionMergeRefusesAndMakesNothing(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("legion merge's rows drive a real git: %v", err)
	}
	for _, tc := range []struct {
		name   string
		args   []string
		oldGit bool
		// noMessage leaves -m out of the command line, which every other row gives.
		noMessage bool
		code      int
		want      string
	}{
		{name: "one revision", args: []string{mergeFirst}, code: 3, want: "name two revisions, the first parent and then the second: 1 given"},
		{name: "three revisions", args: []string{mergeFirst, mergeSecond, "base"}, code: 3, want: "name two revisions, the first parent and then the second: 3 given"},
		{name: "a revset that names no commit", args: []string{mergeFirst, "none()"}, code: 3, want: "none() names 0 commits, not one"},
		{name: "a revset that names two", args: []string{mergeFirst, "x | y"}, code: 3, want: "x | y names 2 commits, not one"},
		{name: "a parent that holds a jj conflict", args: []string{"conflicted", mergeSecond}, code: 3, want: "conflicted, commit "},
		{name: "git 2.37", args: []string{mergeFirst, mergeSecond}, oldGit: true, code: 3, want: `git 2.38 or later is needed for git merge-tree --write-tree, and the git on PATH says "git version 2.37.0"`},
		{name: "no -m", args: []string{mergeFirst, mergeSecond}, noMessage: true, code: 2, want: "legion merge: name the description of the merge commit with -m <message>\nRun 'legion merge --help' for usage.\n"},
		{name: "an unknown flag", args: []string{"--no-such-flag", mergeFirst, mergeSecond}, code: 2, want: "legion merge: unknown flag: --no-such-flag\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRepo(t)
			r.commit("base", []string{"root()"}, map[string]string{"f": "base\n"})
			r.commit("x", []string{"base"}, map[string]string{"f": "x\n"})
			r.commit("y", []string{"base"}, map[string]string{"f": "y\n"})
			r.commit("conflicted", []string{"x", "y"}, nil)
			r.commit(mergeFirst, []string{"x"}, map[string]string{"g": "g\n"})
			r.commit("main", []string{"x"}, map[string]string{"h": "h\n"})
			r.publish("main")
			r.workOn("base")
			if got := r.template("conflicted", "conflict"); got != "true" {
				t.Fatalf("the commit conflicted holds no conflict: %q", got)
			}
			r.setEnv()
			if tc.oldGit {
				bin := filepath.Join(r.dir, "bin")
				script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'git version 2.37.0'; exit 0; fi\nexec " + realGit + " \"$@\"\n"
				if err := os.Mkdir(bin, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))
			}
			before := r.snapshot()
			args := append([]string{"-R", r.ws}, tc.args...)
			if !tc.noMessage {
				args = append([]string{"-m", mergeMessage}, args...)
			}
			var stdout, stderr strings.Builder
			code := run(context.Background(), append([]string{"legion", "merge"}, args...), &stdout, &stderr)
			if code != tc.code || !strings.Contains(stderr.String(), tc.want) || stdout.String() != "" {
				t.Errorf("legion merge %s = %d, want %d naming %q\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), code, tc.code, tc.want, stdout.String(), stderr.String())
			}
			if tc.code == 3 && !strings.HasSuffix(stderr.String(), mergeRefusedNextStep) {
				t.Errorf("stderr ends\n%s\nwant the next step of exit 3:\n%s", stderr.String(), mergeRefusedNextStep)
			}
			r.wantNothingMade(before)
		})
	}
}

// B15: the pod's layout. The workspace is added with jj workspace add to a colocated clone, and
// the command runs with -R from a directory outside it: as B1.
func TestLegionMergeRunsInAWorkspaceOfASharedClone(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(2)
	ws := filepath.Join(r.dir, "ACME-1")
	r.jj("workspace", "add", ws, "--name", "ACME-1")
	clone := *r
	r.ws = ws
	r.workOn("base")
	cloneAt := clone.id("@")
	elsewhere := t.TempDir()
	code, stdout, stderr := r.binary(elsewhere, "-R", ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 0 || stderr != "" {
		t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
	// git runs in the clone, whose git directory its workspaces share: a workspace of a jj that
	// colocates none has no .git of its own.
	if left := clone.leftovers(); len(left) > 0 {
		t.Errorf("the merge left %q", left)
	}
	if got := clone.id("@"); got != cloneAt {
		t.Errorf("the clone's own working copy is on %s, want %s: the merge moved the working copy of a workspace it was not run in", got, cloneAt)
	}
	if content, conflict := r.plainMerge(mergeFirst, "main", "f"); content != "c\n" || conflict != "false" {
		t.Errorf("plain jj new of the same parents holds %q with conflict %s, want c with none: %s", content, conflict, noLongerReproduces)
	}
}

// B16: what a killed run in this workspace left where git conflicts, and a carrier of another
// workspace's. This workspace's are the carriers k2 and k3 with their bookmarks, and X, jj's merge
// of the two, which carries the killed run's Legion-Merge-Run line from the moment jj made it.
// Each carrier has the carriers' own identity, as a run writes it. A run of B1 removes this
// workspace's and leaves the other's.
func TestLegionMergeRemovesOnlyThisWorkspacesLeftovers(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(2)
	r.jj("workspace", "add", filepath.Join(r.dir, "other"), "--name", "other")
	// A carrier holds the tree of the commit that of names. k2 and k3 hold two trees, so git
	// writes two commits for them, not one.
	carrier := func(workspace, role, of string) (id, bookmark string) {
		id = r.writeCarrier(r.git("rev-parse", r.id(of)+"^{tree}"), "a carrier a killed run in "+workspace+" left")
		bookmark = "legion-merge-" + workspace + "-0123456789abcdef-" + role
		r.git("update-ref", "refs/heads/"+bookmark, id)
		return id, bookmark
	}
	k2, k2Bookmark := carrier("default", "k2", "b1")
	k3, k3Bookmark := carrier("default", "k3", "b2")
	theirs, theirBookmark := carrier("other", "g", "b1")
	r.jj("git", "import")
	r.jj("new", "--no-edit", k2, k3, "-m", "jj's merge of git's versions a killed run left", "-m", "Legion-Merge-Run: legion-merge-default-0123456789abcdef-")
	x := r.id(fmt.Sprintf("children(%s) & children(%s)", k2, k3))
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 0 {
		t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
	for _, id := range []string{k2, k3, x} {
		if got := r.template(id, "hidden"); got != "true" {
			t.Errorf("this workspace's leftover %s is still visible", id)
		}
	}
	if got := r.template(theirs, `hidden ++ " " ++ bookmarks`); got != "false "+theirBookmark {
		t.Errorf("the other workspace's carrier %s reads %q, want visible with its bookmark %s", theirs, got, theirBookmark)
	}
	bookmarks := r.read("bookmark", "list", "--all", "-T", `name ++ "\n"`)
	refs := r.git("for-each-ref", "--format=%(refname)")
	for _, mine := range []string{k2Bookmark, k3Bookmark} {
		if strings.Contains(bookmarks, mine) {
			t.Errorf("this workspace's leftover bookmark %s is still there:\n%s", mine, bookmarks)
		}
		if strings.Contains(refs, mine) {
			t.Errorf("git's refs are\n%s\nwant %s gone", refs, mine)
		}
	}
	if !strings.Contains(refs, theirBookmark) {
		t.Errorf("git's refs are\n%s\nwant %s kept", refs, theirBookmark)
	}
}

// B17: under the pane's overlay, the merge's description is the -m text, then the role's trailer,
// then `Legion-Merge: <tree>` as its last line, where the tree is the first line git merge-tree
// prints for the two parents with the merge's option list. Where the -m text ends in a block of
// trailers, the role's trailer and Legion-Merge join that block, as jj writes the role's trailer
// given that text alone: git reads only the last paragraph as trailers. A trailer that the block
// folds onto a next line, which starts with a space, keeps the block one.
func TestLegionMergeWritesItsTrailerLast(t *testing.T) {
	for _, tc := range []struct {
		name    string
		history func(r *mergeRepo)
		code    int
		message string
		// want is the description up to git's tree id, which the row reads once its history is made.
		want string
	}{
		{"B1", func(r *mergeRepo) { r.minimalCase(2) }, 0, mergeMessage, mergeMessage + "\n\nLegion-Role: implementer\nLegion-Merge: "},
		{"B3", (*mergeRepo).contentConflict, 1, mergeMessage, mergeMessage + "\n\nLegion-Role: implementer\nLegion-Merge: "},
		{"B1 with a trailer in the message", func(r *mergeRepo) { r.minimalCase(2) }, 0, mergeMessage + "\n\nSigned-off-by: a <a@b>", mergeMessage + "\n\nSigned-off-by: a <a@b>\nLegion-Role: implementer\nLegion-Merge: "},
		{"B1 with a folded trailer in the message", func(r *mergeRepo) { r.minimalCase(2) }, 0, mergeMessage + "\n\nKey: value\n  continued here", mergeMessage + "\n\nKey: value\n  continued here\nLegion-Role: implementer\nLegion-Merge: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRepo(t)
			tc.history(r)
			if code, stdout, stderr := r.merge("-R", r.ws, "-m", tc.message, mergeFirst, mergeSecond); code != tc.code {
				t.Fatalf("legion merge = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.code, stdout, stderr)
			}
			want := tc.want + r.gitTree(mergeFirst, "main") + "\n"
			if got := r.template("@-", "description"); got != want {
				t.Errorf("the merge's description is\n%q\nwant\n%q", got, want)
			}
		})
	}
}

// H1: `legion merge --help` names the next step of each exit status, the case of a legion with no
// merge command, that only the resolution goes into the merge, naming a merge by its change id
// after a squash, the comparison with the merge's own option list, the files the merge brings in,
// and never jj diff -r <merge>.
func TestLegionMergeHelpSaysEachNextStepAndTheReading(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := run(context.Background(), []string{"legion", "merge", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("legion merge --help = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	options := strings.Join(jjmerge.MergeTreeOptions(), " ")
	for _, want := range []string{
		"jj-vcs/jj#6369",
		"  0  The command made the merge, and git merged with no conflict.\n     Push the merge.\n",
		"  1  The command made the merge, and the merge holds the conflicts of git's that the output lists.\n",
		`Read each path that the output names as "a conflict of git's that jj merged".`,
		"Resolve each jj conflict in the working-copy commit.",
		"jj squash --from '<change id>+::@' --into <change id> --use-destination-message <each listed path>",
		"If you keep a part of the resolution for a later turn, that part stays in a commit above the merge.",
		"  2  The command line has a usage error, for example an unknown flag or no -m. The command made nothing.\n",
		`If legion says 'unknown command "merge"', the legion that you use has no merge command.`,
		"Then make no merge. This rule applies also when a skill or a role prompt names jj new.\n     Tell the architect.",
		"  3  The command refused the merge or failed, and it gives the reason. The command made nothing.\n     Act on the reason.\n     Do not make the merge with jj new.\n",
		"Put only the resolution of git's conflicts into the merge.\nPut each other change into a commit above the merge.\n",
		"A squash into the merge gives the merge a new commit id. The change id of the merge does not change.\nUse the change id to name the merge in jj commands.\n",
		`git diff "$(git ` + options + ` merge-tree --write-tree $(git rev-parse <merge>^1 <merge>^2) | head -n 1)" <merge>`,
		`git diff --name-only "$(git ` + options + ` merge-tree --write-tree $(git rev-parse <merge>^1 <merge>^2) | head -n 1)" <merge>`,
		"git diff --stat <merge>^1 <merge>",
		"Do not read a merge with jj diff -r <merge>.",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("legion merge --help lacks %q:\n%s", want, stdout.String())
		}
	}
}

// While another run holds the workspace's lock, the command refuses with exit 3 and makes
// nothing: the leftover sweep at the start of a run would otherwise take the commits of the run
// that is still going.
func TestLegionMergeRefusesWhileAnotherRunHoldsTheWorkspace(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(2)
	r.setEnv()
	before := r.snapshot()
	held, err := os.OpenFile(filepath.Join(r.ws, ".jj", "legion-merge.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold the merge lock: %v", err)
	}
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	want := "legion merge: another legion merge is running in the workspace " + r.ws + ". Wait for it to end, then run the merge again\n"
	if code != 3 || !strings.Contains(stderr, want) || !strings.HasSuffix(stderr, mergeRefusedNextStep) || stdout != "" {
		t.Errorf("legion merge with the lock held = %d, want 3 naming %q and ending with the next step\nstdout:\n%s\nstderr:\n%s", code, want, stdout, stderr)
	}
	r.wantNothingMade(before)
}

// killRunner is jjmerge.Exec until the first command step picks out; that command and every one
// after it fail without running, the undo's included, as for a run something killed (SIGKILL,
// the memory guard, a pane's SIGHUP) at that step.
type killRunner struct {
	step   func(args []string) bool
	killed bool
}

func (k *killRunner) Run(ctx context.Context, c jjmerge.Command) ([]byte, error) {
	if k.killed || k.step(c.Args) {
		k.killed = true
		return nil, &jjmerge.ExitError{Args: c.Args, Code: -1, Stderr: "killed"}
	}
	return jjmerge.Exec{}.Run(ctx, c)
}

// A run killed after its jj new made the merge commit leaves that merge holding jj's own merge
// of the parents, c where git's merge holds b, and, once its describe ran, the Legion-Merge
// trailer that says the command made it. The next run in the workspace removes what the killed
// run left, as it removes the carriers: the visible commits are then those before the killed
// run, the new merge and the working copy on it.
func TestLegionMergeRerunRemovesTheMergeAKilledRunLeft(t *testing.T) {
	for _, tc := range []struct {
		name string
		step func(args []string) bool
	}{
		{"killed at the describe", func(args []string) bool { return slices.Contains(args, "describe") }},
		{"killed at the restore", func(args []string) bool { return slices.Contains(args, "restore") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRepo(t)
			r.minimalCase(2)
			r.setEnv()
			before := r.snapshot()
			killer := &killRunner{step: tc.step}
			var stdout, stderr strings.Builder
			runMerge(context.Background(), killer, r.ws, mergeMessage, []string{mergeFirst, mergeSecond}, &stdout, &stderr)
			if !killer.killed {
				t.Fatalf("the run never reached the step it is killed at\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
			}
			code, out, errOut := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
			if code != 0 {
				t.Fatalf("legion merge after the killed run = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
			want := slices.Sorted(slices.Values(append(slices.Clone(before.visible), r.id("@-"), r.id("@"))))
			if got := r.visible(); !slices.Equal(got, want) {
				var merges []string
				for _, id := range strings.Fields(r.read("log", "--no-graph", "-r", fmt.Sprintf("children(%s) & children(%s)", r.id(mergeFirst), r.id("main")), "-T", `commit_id ++ "\n"`)) {
					merges = append(merges, fmt.Sprintf("%s holds %q at f, description %q", id, r.show(id, "f"), r.template(id, "description")))
				}
				t.Errorf("the visible commits are\n%q\nwant those before the killed run, the merge and the working copy\n%q\nthe merges of the two parents:\n%s", got, want, strings.Join(merges, "\n"))
			}
		})
	}
}

// Another merge of the same two parents that appears while the run makes its own, as a second
// `legion merge` in the same workspace or a role's `jj new` of the two would make it. The run
// either makes its merge, holding git's merge, or exits 3 having made nothing: then no merge
// commit of its own stays, and the only new visible commit is the other merge. A merge of its
// own left by an exit 3 holds jj's merge of the parents, c where git's holds b, and the help's
// "The command made nothing" is then false.
func TestLegionMergeMakesNothingWhenAnotherMergeOfTheParentsAppears(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(2)
	r.setEnv()
	first, second := r.id(mergeFirst), r.id("main")
	before := r.snapshot()
	runner := &stepRunner{
		step: func(args []string, _ [][]string) bool {
			return slices.Contains(args, "new") && slices.Contains(args, "--message="+mergeMessage)
		},
		fail: func(ctx context.Context, c jjmerge.Command) ([]byte, error) {
			other := jjmerge.Command{Dir: c.Dir, Args: []string{"jj", "--ignore-working-copy", "new", "--no-edit", first, second, "-m", "another run's merge"}}
			if _, err := (jjmerge.Exec{}).Run(ctx, other); err != nil {
				return nil, err
			}
			return jjmerge.Exec{}.Run(ctx, c)
		},
	}
	var stdout, stderr strings.Builder
	code := runMerge(context.Background(), runner, r.ws, mergeMessage, []string{mergeFirst, mergeSecond}, &stdout, &stderr)
	if !runner.hit {
		t.Fatalf("the merge never reached its jj new; it ran %q", runner.ran)
	}
	other := r.id(fmt.Sprintf(`children(%s) & children(%s) & description(substring:"another run's merge")`, first, second))
	switch code {
	case 0:
		r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
	case 3:
		want := slices.Sorted(slices.Values(append(slices.Clone(before.visible), other)))
		if got := r.visible(); !slices.Equal(got, want) {
			var merges []string
			for _, id := range strings.Fields(r.read("log", "--no-graph", "-r", fmt.Sprintf("children(%s) & children(%s)", first, second), "-T", `commit_id ++ "\n"`)) {
				merges = append(merges, fmt.Sprintf("%s holds %q at f, description %q", id, r.show(id, "f"), r.template(id, "description")))
			}
			t.Errorf("legion merge exited 3, and the visible commits are\n%q\nwant those before the command and the other merge\n%q\nthe merges of the two parents:\n%s\nstderr:\n%s", got, want, strings.Join(merges, "\n"), stderr.String())
		}
	default:
		t.Errorf("legion merge = %d, want 0 or 3\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
}

// A carrier a killed run left is a visible head, and a role can build on it: a commit of its own
// work on the carrier, git's merged tree, which is not the working copy. The next run removes
// the carrier, but no abandon of its may reach the role's work (jjmerge's remove: "no abandon
// here reaches the repository's own work"): the run refuses, naming the commit, or leaves it
// visible with its file.
func TestLegionMergeKeepsARolesWorkOnALeftoverCarrier(t *testing.T) {
	r := newMergeRepo(t)
	r.minimalCase(2)
	carrier := r.git("commit-tree", r.gitTree(mergeFirst, "main"), "-m", "a carrier a killed run in default left")
	r.git("update-ref", "refs/heads/legion-merge-default-0123456789abcdef-g", carrier)
	r.jj("git", "import")
	r.jj("new", carrier, "-m", "the role's work on git's tree")
	if err := os.WriteFile(filepath.Join(r.ws, "work"), []byte("the role's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.jj("status")
	// By its commit id, which still names the commit once it is hidden.
	work := r.id("@")
	r.workOn("base")
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if got := r.template(work, "hidden"); got != "false" {
		t.Errorf("legion merge = %d, and the role's commit %s on the leftover carrier is hidden: its work was abandoned\nstdout:\n%s\nstderr:\n%s", code, work, stdout, stderr)
	} else if got := r.show(work, "work"); got != "the role's work\n" {
		t.Errorf("the role's commit %s holds %q at work, want its own file", work, got)
	}
}

// A merge a stopped run left, which still carries the run's Legion-Merge-Run line, that a
// bookmark names or that a remote bookmark holds: a role put its bookmark on the merge, or pushed
// it, and a tracked remote bookmark leaves it mutable. An abandon of the merge deletes the
// bookmark, or hides a commit the remote holds, with exit 0 and nothing on stderr. The next run of
// B1 keeps the merge visible with its bookmarks, and names it on stderr.
func TestLegionMergeKeepsALeftoverMergeABookmarkNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		// hold puts what holds the stopped merge on it, and answers why stderr says it stays.
		hold func(r *mergeRepo, stopped string) string
	}{
		{"a bookmark names it", func(r *mergeRepo, stopped string) string {
			r.jj("bookmark", "create", "legion/ACME-2", "-r", stopped)
			return "the bookmark legion/ACME-2 names it"
		}},
		{"a remote bookmark holds it", func(r *mergeRepo, stopped string) string {
			r.jj("bookmark", "create", "legion/ACME-2", "-r", stopped)
			r.jj("git", "push", "--bookmark", "legion/ACME-2")
			r.jj("bookmark", "delete", "legion/ACME-2")
			if got := r.template(stopped, `self.contained_in("bookmarks()") ++ " " ++ self.contained_in("mutable()")`); got != "false true" {
				r.t.Fatalf("the pushed merge reads %q, want no local bookmark on it and mutable", got)
			}
			return "a remote bookmark is on it or on a commit above it"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRepo(t)
			r.minimalCase(2)
			first, second := r.id(mergeFirst), r.id("main")
			r.jj("new", "--no-edit", first, second, "-m", "a merge a stopped run left", "-m", "Legion-Merge-Run: legion-merge-default-0123456789abcdef-")
			stopped := r.id(fmt.Sprintf(`children(%s) & children(%s) & description(substring:"a merge a stopped run left")`, first, second))
			why := tc.hold(r, stopped)
			bookmarks := r.template(stopped, "bookmarks")
			code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
			if code != 0 {
				t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			r.wantMergeHolds(mergeFirst, "main", "f", "b\n")
			if got := r.template(stopped, `hidden ++ " " ++ bookmarks`); got != "false "+bookmarks {
				t.Errorf("the stopped run's merge %s reads %q, want it visible with %q\nstderr:\n%s", stopped, got, bookmarks, stderr)
			}
			if want := "legion merge: kept commit " + stopped + ", which a legion merge made, because " + why + ".\n"; !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		})
	}
}

// The sweep takes for a carrier only a commit of the carriers' own author, legion-merge@localhost,
// with no parent in the repository or on another carrier. A role's commit on root() that a
// carrier's bookmark names has no parent in the repository either: the run leaves it visible
// with its bookmark, and names the bookmark on stderr. A role's commit on a carrier that a run
// wrote keeps the carrier, which the run names on stderr, and the role's work stays visible.
func TestLegionMergeTellsACarrierByItsAuthor(t *testing.T) {
	const bookmark = "legion-merge-default-0123456789abcdef-g"
	t.Run("a role's commit on root() that a carrier's bookmark names", func(t *testing.T) {
		r := newMergeRepo(t)
		r.minimalCase(2)
		r.commit("work", []string{"root()"}, map[string]string{"work": "the role's work\n"})
		work := r.id("work")
		r.jj("bookmark", "delete", "work")
		r.jj("bookmark", "create", bookmark, "-r", work)
		r.workOn("base")
		code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
		if code != 0 {
			t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if got := r.template(work, `hidden ++ " " ++ bookmarks`); got != "false "+bookmark {
			t.Errorf("the role's commit %s reads %q, want it visible with its bookmark %s\nstderr:\n%s", work, got, bookmark, stderr)
		}
		if want := "legion merge: left the bookmark " + bookmark + " where it is, on commit " + work + ", which is not a commit a legion merge wrote.\n"; !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	})
	t.Run("a role's commit on a carrier a run wrote", func(t *testing.T) {
		r := newMergeRepo(t)
		r.minimalCase(2)
		carrier := r.writeCarrier(r.gitTree(mergeFirst, "main"), "a carrier a killed run in default left")
		r.git("update-ref", "refs/heads/"+bookmark, carrier)
		r.jj("git", "import")
		r.jj("new", carrier, "-m", "the role's work on git's tree")
		if err := os.WriteFile(filepath.Join(r.ws, "work"), []byte("the role's work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r.jj("status")
		work := r.id("@")
		r.workOn("base")
		code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
		if code != 0 {
			t.Fatalf("legion merge = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if got := r.template(work, "hidden"); got != "false" {
			t.Errorf("the role's commit %s on the carrier is hidden: its work was abandoned\nstderr:\n%s", work, stderr)
		} else if got := r.show(work, "work"); got != "the role's work\n" {
			t.Errorf("the role's commit %s holds %q at work, want its own file", work, got)
		}
		if want := "legion merge: kept commit " + carrier + ", which a legion merge made, because commit " + work + " stands on it.\n"; !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	})
}

// A merge whose tree is git's merged tree, made where git gives a file/directory conflict, which
// jj merges from git's versions with no conflict: git keeps the file aside as df~<first parent's
// id>. The help's comparison of that merge with git's merge of its parents prints no path, since
// the two trees are one. Given <merge>^1 and <merge>^2, git names the aside file df~<merge>^1,
// the revision as written, so the comparison names a difference the merge does not hold.
func TestLegionMergeHelpsComparisonListsNothingForGitsOwnTree(t *testing.T) {
	r := newMergeRepo(t)
	r.commit("base", []string{"root()"}, map[string]string{"keep": "keep\n"})
	r.commit(mergeFirst, []string{"base"}, map[string]string{"df": "a file\n"})
	r.commit("main", []string{"base"}, map[string]string{"df/x": "a file in a directory\n"})
	r.publish("main")
	r.workOn("base")
	code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
	if code != 1 || !strings.Contains(stdout, "CONFLICT (file/directory)") {
		t.Fatalf("legion merge = %d, want 1 with git's file/directory conflict\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	merge := r.id("@-")
	if got, want := r.git("rev-parse", merge+"^{tree}"), r.gitTree(mergeFirst, "main"); got != want {
		t.Fatalf("the merge's tree is %s, want git's merged tree %s", got, want)
	}
	comparison := exec.Command("sh", "-c", mergeComparison(merge, "--name-only"))
	comparison.Dir, comparison.Env = r.ws, r.env()
	if out, err := comparison.Output(); err != nil || string(out) != "" {
		t.Errorf("%s printed %q (%v), want nothing: the merge holds git's merged tree", mergeComparison(merge, "--name-only"), out, err)
	}
}

// A bookmark of a carrier's shape under this workspace's prefix, on a commit of the repository
// rather than on a carrier: a role's commit on base, or the first parent the run itself names. A
// carrier has no parent in the repository, so nothing a carrier's removal does reaches the
// repository's own commits (dispatch://INFRA-4149's plan, step 4). The run abandons neither
// commit: it refuses, or it leaves the commit visible. At 03a652db it refused such a bookmark and
// made nothing; a sweep that abandons whatever such a bookmark names loses the role's work, and
// takes the first parent and its bookmark before the run resolves it.
func TestLegionMergeAbandonsNoRepositoryCommitACarriersBookmarkNames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(r *mergeRepo) string
	}{
		{"a role's commit on base", func(r *mergeRepo) string {
			r.commit("work", []string{"base"}, map[string]string{"work": "the role's work\n"})
			id := r.id("work")
			r.jj("bookmark", "delete", "work")
			return id
		}},
		{"the first parent", func(r *mergeRepo) string { return r.id(mergeFirst) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMergeRepo(t)
			r.minimalCase(2)
			target := tc.target(r)
			r.jj("bookmark", "create", "legion-merge-default-0123456789abcdef-g", "-r", target)
			r.workOn("base")
			code, stdout, stderr := r.merge("-R", r.ws, "-m", mergeMessage, mergeFirst, mergeSecond)
			if got := r.template(target, "hidden"); got != "false" {
				t.Errorf("legion merge = %d, and the repository's commit %s, which the bookmark legion-merge-default-0123456789abcdef-g names, is hidden: the run abandoned it\nstdout:\n%s\nstderr:\n%s", code, target, stdout, stderr)
			}
		})
	}
}
