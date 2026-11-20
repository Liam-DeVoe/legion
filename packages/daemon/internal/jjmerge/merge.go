package jjmerge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// mergeTreeOptions sets each option that changes the result of a git merge to git's documented
// default (git v2.47.3, Documentation/config/merge.txt): merge.renames "Defaults to the value of
// diff.renames", which defaults to true; merge.directoryRenames "Defaults to conflict";
// merge.renameLimit "currently defaults to 7000"; merge.conflictStyle "The default is merge";
// merge.renormalize is off unless a configuration sets it. So no configuration file of the
// machine changes the merge. The merge and the reading command `legion merge --help` prints both
// read it through MergeTreeOptions, which answers a copy, so neither changes the list the other
// gets.
var mergeTreeOptions = [...]string{
	"-c", "merge.renames=true",
	"-c", "merge.directoryRenames=conflict",
	"-c", "merge.renameLimit=7000",
	"-c", "merge.conflictStyle=merge",
	"-c", "merge.renormalize=false",
}

// MergeTreeOptions is the option list the merge gives `git merge-tree`, as a copy. A person who
// compares a merge with git's merge of its parents gives git the same list, or a configuration
// file can make git's merge another one.
func MergeTreeOptions() []string {
	return slices.Clone(mergeTreeOptions[:])
}

// minimumGit is the first git whose merge-tree has --write-tree, which the merge reads.
var minimumGit = [2]int{2, 38}

// carrierIdentity is the author and committer of the commits the merge writes with git plumbing.
// It is set on those children alone, so jj's own commits, the merge among them, keep the role's
// identity.
var carrierIdentity = []string{
	"GIT_AUTHOR_NAME=legion merge", "GIT_AUTHOR_EMAIL=legion-merge@localhost",
	"GIT_COMMITTER_NAME=legion merge", "GIT_COMMITTER_EMAIL=legion-merge@localhost",
}

// bookmarkPrefix begins every bookmark the merge makes. The jj workspace's name and a dash follow
// it, so a run touches the bookmarks of its own workspace alone: on a tmux clone that several
// issues share, each issue's workspace has a name of its own.
const bookmarkPrefix = "legion-merge-"

// carrierBookmark is what follows the workspace's prefix in the name of a carrier's bookmark: the
// run's random id and the carrier's role. Only a name of this shape is a leftover of this
// workspace, so a workspace whose name is another's name and a dash and more never matches here.
var carrierBookmark = regexp.MustCompile(`^[0-9a-f]{16}-(g|k1|k2|k3)$`)

// Command is one git or jj child of the merge.
type Command struct {
	// Dir is the directory the command runs in, the workspace's root: jj reads the configuration
	// of the repository its working directory sits in (docs/testing.md), and prints paths
	// relative to it.
	Dir  string
	Args []string
	// Env is added to the caller's environment for this command alone. The caller's own
	// environment reaches every command unchanged: a pane's JJ_CONFIG is what writes the role's
	// trailers into the merge's description.
	Env   []string
	Stdin []byte
}

// Runner runs one command of the merge and answers what it printed on standard output. A command
// that runs and exits with a status other than 0 answers an *ExitError, with what it printed on
// standard output as well. A test gives the merge a Runner that fails one named step.
type Runner interface {
	Run(ctx context.Context, c Command) ([]byte, error)
}

// ExitError is a command that ran and exited with a status other than 0.
type ExitError struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("%s exited %d: %s", strings.Join(e.Args, " "), e.Code, strings.TrimSpace(e.Stderr))
}

// Exec is the Runner of `legion merge`: each command is a child of this process, the git and jj
// first on its PATH.
type Exec struct{}

func (Exec) Run(ctx context.Context, c Command) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.Bytes(), &ExitError{Args: c.Args, Code: exit.ExitCode(), Stderr: stderr.String()}
	}
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", strings.Join(c.Args, " "), err)
	}
	return stdout.Bytes(), nil
}

// Request is one merge to make.
type Request struct {
	// Workspace is the root of the jj workspace, an absolute path. Every command runs there.
	Workspace string
	// Message is the merge commit's description, as `jj new -m` takes it.
	Message string
	// Revisions are the parents as the caller wrote them, each a jj revset that names one commit:
	// the first parent, then the second. Any other count is refused.
	Revisions []string
}

// Result is the merge the workspace holds once Merge has answered.
type Result struct {
	// CommitID and ChangeID name the merge. A squash into the merge gives it a new commit id and
	// keeps its change id, so the change id is the name that lasts.
	CommitID, ChangeID string
	// Parents are the commit ids of the first parent and the second.
	Parents [2]string
	// Tree is git's merged tree, which the merge's `Legion-Merge` trailer names.
	Tree string
	// Differed are the paths where jj's own merge of the parents differed from git's merge, which
	// the merge holds instead, in jj's order. Paths git conflicts in are under Conflicts instead.
	Differed []string
	// Conflicts are the paths git conflicts in, in git's order, each with what the merge holds.
	// Empty where git merged cleanly.
	Conflicts []Conflict
	// Messages are git's CONFLICT messages, in git's order.
	Messages []Message
}

// Conflict is one path git conflicts in.
type Conflict struct {
	Path string
	// JJConflict is whether the merge holds a jj conflict at Path. Where it is false, jj merged
	// git's three versions of the path cleanly, which the role reads before it pushes: a merge
	// where the path is absent included.
	JJConflict bool
}

// Merge makes the merge commit of request's two revisions in its workspace and puts the working
// copy on an empty commit on top of it. The merge holds git's merge (`git merge-tree
// --write-tree`) on every path git merges cleanly, and on each path git conflicts in, jj's merge
// of git's own three versions of the path over one merge base. Its description is request's
// message, the trailers the caller's jj configuration adds, and `Legion-Merge: <git's tree>` as
// its last line.
//
// It works in the steps of dispatch://INFRA-4149's plan ("What it does, in order"). Only the
// first jj command reads the working copy and only the last moves it; every one between carries
// --ignore-working-copy. git's versions reach jj as carriers: commits git plumbing writes, with
// no parent in the repository, which bookmarks under this workspace's prefix make visible. On
// every failure after the carriers are written, Merge removes what it made, so the workspace is
// as it found it: no new visible commit, no bookmark and no git ref of its own, and the working
// copy where it was. A run that something killed leaves its carriers and their bookmarks behind,
// and the next run in the same workspace removes them first.
func Merge(ctx context.Context, run Runner, request Request) (Result, error) {
	if len(request.Revisions) != 2 {
		return Result{}, fmt.Errorf("name two revisions, the first parent and then the second: %d given (%s)",
			len(request.Revisions), strings.Join(request.Revisions, " "))
	}
	gitDir, err := workspace.GitDir(request.Workspace)
	if err != nil {
		return Result{}, fmt.Errorf("find the git directory of the workspace %s: %w", request.Workspace, err)
	}
	m := &merger{run: run, gitDir: gitDir, request: request}
	name, err := m.workspaceName(ctx)
	if err != nil {
		return Result{}, err
	}
	m.prefix = bookmarkPrefix + name + "-"
	if err := m.removeLeftovers(ctx); err != nil {
		return Result{}, err
	}
	if err := m.checkGit(ctx); err != nil {
		return Result{}, err
	}
	var parents [2]string
	for i, revision := range request.Revisions {
		if parents[i], err = m.resolve(ctx, revision); err != nil {
			return Result{}, err
		}
	}
	if parents[0] == parents[1] {
		return Result{}, fmt.Errorf("%s and %s name one commit, %s: a merge needs two", request.Revisions[0], request.Revisions[1], parents[0])
	}
	tree, err := m.mergeTree(ctx, parents)
	if err != nil {
		return Result{}, err
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return Result{}, fmt.Errorf("make the run's id: %w", err)
	}
	m.runPrefix = m.prefix + hex.EncodeToString(random) + "-"
	result, err := m.build(ctx, parents, tree)
	if err != nil {
		// The undo runs to its end when the caller's context ended the run: a merge that stops
		// half made is what the undo is there to prevent.
		if undoErr := m.remove(context.WithoutCancel(ctx), m.ownBookmark, m.merge); undoErr != nil {
			err = fmt.Errorf("%w\nThe undo of what this run made also failed: %v", err, undoErr)
		}
		return Result{}, err
	}
	return result, nil
}

// merger is one run of Merge.
type merger struct {
	run     Runner
	gitDir  string
	request Request
	// prefix begins every bookmark of this workspace's runs, and runPrefix every bookmark of this
	// run: prefix, the run's random id and a dash.
	prefix, runPrefix string
	// merge is the merge's change id once `jj new` made it: a restore and a describe rewrite the
	// merge, so its first commit id stops naming a visible commit.
	merge string
}

// build is the merge from git's merge-tree on, steps 4 to 10 of the plan: everything Merge undoes
// when it fails.
func (m *merger) build(ctx context.Context, parents [2]string, tree gitMerge) (Result, error) {
	conflicted := tree.conflictedPaths()
	short := func(id string) string { return id[:min(12, len(id))] }
	// The carriers by their role, which ends each one's bookmark: g holds git's tree; k1, k2 and
	// k3 hold git's tree with each conflicted path at its stage 1, 2 or 3 version.
	carriers := map[string]string{}
	var err error
	if carriers["g"], err = m.commitTree(ctx, tree.Tree, "", fmt.Sprintf("git's merge of %s and %s", short(parents[0]), short(parents[1]))); err != nil {
		return Result{}, err
	}
	if len(conflicted) > 0 {
		stages, err := m.stageTrees(ctx, tree, conflicted)
		if err != nil {
			return Result{}, err
		}
		// jj's conflict markers quote each side's description, so each names whose version it holds.
		for _, k := range []struct {
			role   string
			stage  int
			parent string
			text   string
		}{
			{"k1", 1, "", fmt.Sprintf("git's merge base of %s and %s", m.request.Revisions[0], m.request.Revisions[1])},
			{"k2", 2, "k1", fmt.Sprintf("%s %s", m.request.Revisions[0], short(parents[0]))},
			{"k3", 3, "k1", fmt.Sprintf("%s %s", m.request.Revisions[1], short(parents[1]))},
		} {
			if carriers[k.role], err = m.commitTree(ctx, stages[k.stage-1], carriers[k.parent], k.text); err != nil {
				return Result{}, err
			}
		}
	}
	// Bookmarks and not tags: a tag is in immutable_heads(), and jj abandons no immutable commit.
	var refs strings.Builder
	for _, role := range slices.Sorted(maps.Keys(carriers)) {
		fmt.Fprintf(&refs, "create refs/heads/%s%s %s\n", m.runPrefix, role, carriers[role])
	}
	if _, err := m.git(ctx, []byte(refs.String()), nil, "update-ref", "--stdin"); err != nil {
		return Result{}, err
	}
	// Every jj command of the run but its first and its last carries --ignore-working-copy, which
	// skips the import of git's refs that a colocated workspace makes at the start of a command
	// on its own; `jj git import` under that flag imports them all the same (`jj git import
	// --help`). So this import is what makes the carriers visible to jj in every workspace,
	// colocated or not.
	if _, err := m.jj(ctx, "git", "import"); err != nil {
		return Result{}, err
	}

	// The source is the tree the merge takes: git's, or, where git conflicts, X, jj's merge of
	// git's own three versions of each conflicted path over K1, their one merge base. X equals
	// git's tree on every other path.
	source := carriers["g"]
	if len(conflicted) > 0 {
		if _, err := m.jj(ctx, "new", "--no-edit", "--message=jj's merge of git's versions of the paths git conflicts in", carriers["k2"], carriers["k3"]); err != nil {
			return Result{}, err
		}
		x, err := m.one(ctx, fmt.Sprintf("children(%s) & children(%s)", carriers["k2"], carriers["k3"]), "change_id")
		if err != nil {
			return Result{}, fmt.Errorf("find jj's merge of the carriers %s and %s: %w", carriers["k2"], carriers["k3"], err)
		}
		source = x
	}

	siblings := fmt.Sprintf("children(%s) & children(%s)", parents[0], parents[1])
	before, err := m.list(ctx, siblings, "change_id")
	if err != nil {
		return Result{}, err
	}
	_, newErr := m.jj(ctx, "new", "--no-edit", "--message="+m.request.Message, parents[0], parents[1])
	// The merge is the one new child of both parents, read also when jj new failed: a merge jj
	// made before it failed is still one for the undo to abandon.
	after, err := m.list(ctx, siblings, "change_id")
	if err != nil {
		return Result{}, errors.Join(newErr, err)
	}
	made := slices.DeleteFunc(after, func(change string) bool { return slices.Contains(before, change) })
	if len(made) == 1 {
		m.merge = made[0]
	}
	if newErr != nil {
		return Result{}, newErr
	}
	if len(made) != 1 {
		return Result{}, fmt.Errorf("jj new made %d new children of %s and %s, not one: %s", len(made), parents[0], parents[1], strings.Join(made, " "))
	}
	// The trailer goes in after jj wrote the description: jj appends the configured trailers after
	// the description it is given, so a trailer passed with -m would come before the role's own.
	description, err := m.jj(ctx, "log", "--no-graph", "-r", m.merge, "-T", "description")
	if err != nil {
		return Result{}, err
	}
	if _, err := m.jj(ctx, "describe", "--message="+withTrailer(string(description), "Legion-Merge: "+tree.Tree), m.merge); err != nil {
		return Result{}, err
	}
	conflict := make(map[string]bool, len(conflicted))
	for _, path := range conflicted {
		conflict[path] = true
	}
	jjOwn, err := m.diffPaths(ctx, source, m.merge)
	if err != nil {
		return Result{}, err
	}
	differed := slices.DeleteFunc(jjOwn, func(path string) bool { return conflict[path] })

	if _, err := m.jj(ctx, "restore", "--from", source, "--into", m.merge); err != nil {
		return Result{}, err
	}
	back, err := m.diffPaths(ctx, carriers["g"], m.merge)
	if err != nil {
		return Result{}, err
	}
	if other := slices.DeleteFunc(back, func(path string) bool { return conflict[path] }); len(other) > 0 {
		return Result{}, fmt.Errorf("after the restore, the merge differs from git's merge at paths git does not conflict in: %s", quotedPaths(other))
	}
	result := Result{Parents: parents, Tree: tree.Tree, Differed: differed}
	if len(conflicted) > 0 {
		args := []string{"file", "list", "-r", m.merge, "-T", `if(conflict, path ++ "\0")`}
		for _, path := range conflicted {
			args = append(args, RootFile(path))
		}
		out, err := m.jj(ctx, args...)
		if err != nil {
			return Result{}, err
		}
		jjConflicts := nulFields(out)
		for _, path := range conflicted {
			result.Conflicts = append(result.Conflicts, Conflict{Path: path, JJConflict: slices.Contains(jjConflicts, path)})
		}
		for _, message := range tree.Messages {
			if strings.HasPrefix(message.Type, "CONFLICT") {
				result.Messages = append(result.Messages, message)
			}
		}
	}
	if err := m.remove(ctx, m.ownBookmark, ""); err != nil {
		return Result{}, err
	}
	// The merge's commit id is read before the working copy moves, so nothing after the move can
	// fail: a failure there would run the undo, which abandons the merge while the working copy is
	// its child, so jj would rebase the working copy onto the two parents, jj's own merge of them,
	// and the command would still say it made nothing. `jj new` does not rewrite the merge, so the
	// id still names it once the working copy is on top of it.
	commit, err := m.one(ctx, m.merge, "commit_id")
	if err != nil {
		return Result{}, err
	}
	// The one command after the first that reads the working copy, and the run's last: it puts
	// the working copy on an empty commit on top of the merge.
	if _, err := m.jjLive(ctx, "new", m.merge); err != nil {
		return Result{}, err
	}
	result.CommitID, result.ChangeID = commit, m.merge
	return result, nil
}

// workspaceName is the jj workspace's own name, read with the run's first jj command, which
// snapshots the working copy as `jj new` does.
func (m *merger) workspaceName(ctx context.Context) (string, error) {
	out, err := m.jjLive(ctx, "workspace", "list", "-T", `if(target.current_working_copy(), name ++ "\n")`)
	if err != nil {
		return "", err
	}
	names := workspace.OutputLines(string(out))
	if len(names) != 1 {
		return "", fmt.Errorf("jj names %d workspaces whose working-copy commit is this workspace's, not one: %q", len(names), names)
	}
	return names[0], nil
}

// removeLeftovers removes what a run in this workspace that was stopped before its end left: its
// carriers, with their bookmarks, and jj's merge of the carriers. Another workspace's are left
// alone, a run still going among them. A killed run's carrier is otherwise a visible head that
// an idle release saves into the next pod and that a shared tmux clone keeps for every issue.
func (m *merger) removeLeftovers(ctx context.Context) error {
	// A workspace that is not colocated sees a git ref only once it imports it, and a run killed
	// after it wrote its refs never imported them.
	if _, err := m.jj(ctx, "git", "import"); err != nil {
		return err
	}
	if err := m.remove(ctx, m.leftover, ""); err != nil {
		return fmt.Errorf("remove what an earlier merge in this workspace left: %w", err)
	}
	return nil
}

// leftover is whether name is the bookmark of a carrier of a run in this workspace, this run's
// included.
func (m *merger) leftover(name string) bool {
	rest, ok := strings.CutPrefix(name, m.prefix)
	return ok && carrierBookmark.MatchString(rest)
}

// ownBookmark is whether name is the bookmark of one of this run's carriers.
func (m *merger) ownBookmark(name string) bool {
	return strings.HasPrefix(name, m.runPrefix) && m.leftover(name)
}

// remove abandons the carriers whose bookmarks ours matches, with every commit on top of them,
// and merge when it names one; then deletes each of those bookmarks jj still holds and each git
// ref under refs/heads that ours matches. ours is asked only of names under bookmarkPrefix, the
// bookmarks and the git refs that remove lists. One abandon takes the carriers and their
// bookmarks together, so a run killed after it still finds each carrier by its bookmark. The
// bookmarks go through jj, which exports their removal in a colocated workspace; deleting a git
// ref under jj is only for a ref jj never imported or does not export.
func (m *merger) remove(ctx context.Context, ours func(name string) bool, merge string) error {
	out, err := m.jj(ctx, "bookmark", "list", "-T", `if(!remote, name ++ "\0" ++ if(normal_target, normal_target.commit_id()) ++ "\0")`, "glob:"+bookmarkPrefix+"*")
	if err != nil {
		return err
	}
	listed := nulFields(out)
	if len(listed)%2 != 0 {
		return fmt.Errorf("jj bookmark list printed %d fields, not name and target pairs: %q", len(listed), listed)
	}
	var names, targets, untargeted []string
	for i := 0; i < len(listed); i += 2 {
		switch name, target := listed[i], listed[i+1]; {
		case !ours(name):
		case target == "":
			untargeted = append(untargeted, name)
		default:
			names, targets = append(names, name), append(targets, target)
		}
	}
	var abandon []string
	if len(targets) > 0 {
		carriers := "(" + strings.Join(targets, " | ") + ")"
		// A carrier has no parent in the repository, and nothing but a carrier or jj's merge of
		// carriers stands on one. A bookmark of this shape on any other commit is refused rather
		// than read as a carrier, so no abandon here reaches the repository's own work.
		strays, err := m.list(ctx, fmt.Sprintf("(parents(%[1]s::) ~ %[1]s:: ~ root()) | (%[1]s:: & working_copies())", carriers), "commit_id")
		if err != nil {
			return err
		}
		if len(strays) > 0 {
			return fmt.Errorf("the bookmarks %s name commits that are not carriers of a merge: %s is a working copy, or a parent that is not a carrier. Delete the bookmarks with jj bookmark delete, then run the merge again",
				strings.Join(names, " "), strings.Join(strays, " "))
		}
		abandon = append(abandon, carriers+"::")
	}
	if merge != "" {
		abandon = append(abandon, merge)
	}
	if len(abandon) > 0 {
		if _, err := m.jj(ctx, append([]string{"abandon"}, abandon...)...); err != nil {
			return err
		}
	}
	if len(untargeted) > 0 {
		if _, err := m.jj(ctx, append([]string{"bookmark", "delete", "--"}, untargeted...)...); err != nil {
			return err
		}
	}
	refs, err := m.git(ctx, nil, nil, "for-each-ref", "--format=%(refname)", "refs/heads/"+bookmarkPrefix+"*")
	if err != nil {
		return err
	}
	var deletes strings.Builder
	for _, ref := range workspace.OutputLines(string(refs)) {
		if ours(strings.TrimPrefix(ref, "refs/heads/")) {
			fmt.Fprintf(&deletes, "delete %s\n", ref)
		}
	}
	if deletes.Len() == 0 {
		return nil
	}
	if _, err := m.git(ctx, []byte(deletes.String()), nil, "update-ref", "--stdin"); err != nil {
		return err
	}
	// A workspace that is not colocated keeps the record of a ref it imported until it imports
	// again, and would list the ref as a bookmark of the remote `git` until then.
	_, err = m.jj(ctx, "git", "import")
	return err
}

// checkGit refuses a git older than minimumGit, naming its version.
func (m *merger) checkGit(ctx context.Context) error {
	out, err := m.run.Run(ctx, Command{Dir: m.request.Workspace, Args: []string{"git", "version"}})
	if err != nil {
		return err
	}
	said := strings.TrimSpace(string(out))
	fields := strings.Fields(said)
	var version [2]int
	if len(fields) >= 3 {
		parts := strings.SplitN(fields[2], ".", 3)
		if len(parts) >= 2 {
			version[0], _ = strconv.Atoi(parts[0])
			version[1], _ = strconv.Atoi(parts[1])
		}
	}
	if version[0] < minimumGit[0] || version[0] == minimumGit[0] && version[1] < minimumGit[1] {
		return fmt.Errorf("git %d.%d or later is needed for git merge-tree --write-tree, and the git on PATH says %q",
			minimumGit[0], minimumGit[1], said)
	}
	return nil
}

// resolve is the one commit revision names, or a refusal naming the revision: a revset that
// names no commit, one that names more than one, and a commit that holds a jj conflict, which
// git would merge as the plain files jj stores the conflict in.
func (m *merger) resolve(ctx context.Context, revision string) (string, error) {
	out, err := m.jj(ctx, "log", "--no-graph", "--revisions="+revision, "-T", `commit_id ++ " " ++ conflict ++ "\n"`)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", revision, err)
	}
	lines := workspace.OutputLines(string(out))
	if len(lines) != 1 {
		return "", fmt.Errorf("%s names %d commits, not one: %s", revision, len(lines), strings.Join(lines, ", "))
	}
	id, conflict, _ := strings.Cut(lines[0], " ")
	if conflict != "false" {
		return "", fmt.Errorf("%s, commit %s, holds a jj conflict: resolve it first, since git would merge the files jj stores the conflict in as plain text", revision, id)
	}
	return id, nil
}

// mergeTree is git's merge of the two parents. git exits 0 for a clean merge and 1 for one with
// conflicts; anything else, or output that disagrees with the exit, is a refusal.
func (m *merger) mergeTree(ctx context.Context, parents [2]string) (gitMerge, error) {
	args := append(MergeTreeOptions(), "merge-tree", "--write-tree", "-z", parents[0], parents[1])
	out, err := m.git(ctx, nil, nil, args...)
	var exit *ExitError
	clean := err == nil
	if !clean && !(errors.As(err, &exit) && exit.Code == 1) {
		return gitMerge{}, err
	}
	tree, parseErr := parseMergeTree(out)
	switch {
	case parseErr != nil:
		return gitMerge{}, parseErr
	case clean && len(tree.Conflicts) > 0:
		return gitMerge{}, fmt.Errorf("git merge-tree exited 0 and named %d conflicted entries", len(tree.Conflicts))
	case !clean && len(tree.Conflicts) == 0:
		var said []string
		for _, message := range tree.Messages {
			said = append(said, message.Text)
		}
		return gitMerge{}, fmt.Errorf("git merge-tree reported conflicts and named no conflicted path: %s", strings.Join(said, "; "))
	}
	return tree, nil
}

// stageTrees are T1, T2 and T3: git's tree with each path of conflicted, tree's conflicted
// paths, set to its stage 1, 2 or 3 entry, or absent where git gave none, written through a
// temporary index file of the run's own.
func (m *merger) stageTrees(ctx context.Context, tree gitMerge, conflicted []string) ([3]string, error) {
	var trees [3]string
	dir, err := os.MkdirTemp("", "legion-merge-")
	if err != nil {
		return trees, fmt.Errorf("make the directory of the merge's index files: %w", err)
	}
	defer os.RemoveAll(dir)
	absent := strings.Repeat("0", len(tree.Tree))
	entries := make(map[pathStage]entry, len(tree.Conflicts))
	for _, e := range tree.Conflicts {
		entries[pathStage{e.Path, e.Stage}] = e
	}
	for stage := 1; stage <= 3; stage++ {
		env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index-"+strconv.Itoa(stage))}
		if _, err := m.git(ctx, nil, env, "read-tree", tree.Tree); err != nil {
			return trees, err
		}
		var info bytes.Buffer
		for _, path := range conflicted {
			mode, oid := "0", absent
			if e, ok := entries[pathStage{path, stage}]; ok {
				mode, oid = e.Mode, e.OID
			}
			fmt.Fprintf(&info, "%s %s\t%s\x00", mode, oid, path)
		}
		if _, err := m.git(ctx, info.Bytes(), env, "update-index", "-z", "--index-info"); err != nil {
			return trees, err
		}
		out, err := m.git(ctx, nil, env, "write-tree")
		if err != nil {
			return trees, err
		}
		trees[stage-1] = strings.TrimSpace(string(out))
	}
	return trees, nil
}

// commitTree writes a carrier: a commit of tree, on parent where there is one, under the
// carriers' own identity.
func (m *merger) commitTree(ctx context.Context, tree, parent, message string) (string, error) {
	args := []string{"commit-tree", "--no-gpg-sign", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	out, err := m.git(ctx, []byte(message+"\n"), carrierIdentity, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// diffPaths is every path where to differs from from: both paths of a rename, which `jj diff
// --name-only` names only the second of.
func (m *merger) diffPaths(ctx context.Context, from, to string) ([]string, error) {
	out, err := m.jj(ctx, "diff", "--from", from, "--to", to, "-T", `source.path() ++ "\0" ++ target.path() ++ "\0"`)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, path := range nulFields(out) {
		if !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// list is one template keyword of each commit revset names, in jj's order.
func (m *merger) list(ctx context.Context, revset, keyword string) ([]string, error) {
	out, err := m.jj(ctx, "log", "--no-graph", "-r", revset, "-T", keyword+` ++ "\n"`)
	if err != nil {
		return nil, err
	}
	return workspace.OutputLines(string(out)), nil
}

// one is the keyword of the one commit revset names, or an error when it names another count.
func (m *merger) one(ctx context.Context, revset, keyword string) (string, error) {
	values, err := m.list(ctx, revset, keyword)
	if err != nil {
		return "", err
	}
	if len(values) != 1 {
		return "", fmt.Errorf("%s names %d commits, not one", revset, len(values))
	}
	return values[0], nil
}

// jj runs a jj command that neither reads nor moves the working copy. Every jj command prints no
// color, whatever the caller's configuration says, since the merge reads what jj prints.
func (m *merger) jj(ctx context.Context, args ...string) ([]byte, error) {
	return m.run.Run(ctx, Command{Dir: m.request.Workspace, Args: append([]string{"jj", "--ignore-working-copy", "--color=never"}, args...)})
}

// jjLive runs a jj command that reads the working copy and may move it: the first and the last of
// the run.
func (m *merger) jjLive(ctx context.Context, args ...string) ([]byte, error) {
	return m.run.Run(ctx, Command{Dir: m.request.Workspace, Args: append([]string{"jj", "--color=never"}, args...)})
}

// git runs a git command on the workspace's git directory, which a workspace that jj does not
// colocate has no .git to find.
func (m *merger) git(ctx context.Context, stdin []byte, env []string, args ...string) ([]byte, error) {
	return m.run.Run(ctx, Command{Dir: m.request.Workspace, Args: append([]string{"git", "--git-dir=" + m.gitDir}, args...), Env: env, Stdin: stdin})
}

// withTrailer is description with line added as its last trailer: one more line of its last
// paragraph when that paragraph is a block of trailers and not the subject, or a paragraph of its
// own after a blank line.
func withTrailer(description, line string) string {
	text := strings.TrimRight(description, "\n")
	paragraphs := strings.Split(text, "\n\n")
	if last := paragraphs[len(paragraphs)-1]; len(paragraphs) > 1 && trailerBlock(last) {
		return text + "\n" + line + "\n"
	}
	return text + "\n\n" + line + "\n"
}

// trailerLine is one `Key: value` line of a trailer block.
var trailerLine = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*: `)

// trailerBlock is whether every line of paragraph is a trailer.
func trailerBlock(paragraph string) bool {
	for _, line := range strings.Split(paragraph, "\n") {
		if !trailerLine.MatchString(line) {
			return false
		}
	}
	return true
}

// RootFile is the jj fileset that names path alone, a repository-relative path: a path with
// spaces, quotes, parentheses or a leading dash is still that one file.
func RootFile(path string) string {
	return `root-file:"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path) + `"`
}

// nulFields is out's NUL-terminated fields.
func nulFields(out []byte) []string {
	text := strings.TrimSuffix(string(out), "\x00")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\x00")
}

// quotedPaths is paths as one line of a report, each Go-quoted, since a path may hold a newline.
func quotedPaths(paths []string) string {
	quoted := make([]string, len(paths))
	for i, path := range paths {
		quoted[i] = strconv.Quote(path)
	}
	return strings.Join(quoted, " ")
}
