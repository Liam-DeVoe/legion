package jjmerge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// mergeTreeOptions sets git's merge options to their documented defaults (git v2.47.3,
// Documentation/config/merge.txt and gitattributes(5)), sets merge.default empty, points
// core.attributesFile at /dev/null and sets diff.algorithm to histogram, so neither a global or
// repository merge option, nor a default driver, nor the user's attributes file changes the
// merge, and no configured diff.algorithm fails it. merge.renames "Defaults to the value
// of diff.renames", which defaults to true; merge.directoryRenames "Defaults to conflict";
// merge.renameLimit "currently defaults to 7000"; merge.conflictStyle "The default is merge";
// merge.renormalize is off unless a configuration sets it. merge.default names the merge driver
// of each path that no attribute gives one. An empty merge.default names no driver, so git merges
// each such path with its built-in text merge. A name such as text would not do that: git looks a
// name up among the drivers a configuration defines before its built-in ones, so under
// merge.default=text a configuration's `merge.text.driver` merges each such path. The one
// configuration the empty value does not close is a driver that a configuration defines with an
// empty name (`merge..driver`), since git looks the empty name up among those drivers too.
// core.attributesFile names the user's attributes file, which can give a path a merge driver, and
// /dev/null gives none. A `merge.default = union` in a global or a repository's configuration, or
// a user's attributes file with `f merge=union`, makes git merge a content conflict cleanly. What
// still applies is the repository's own attributes (the .gitattributes of the working copy or of
// git's index when git runs in a git worktree, and $GIT_DIR/info/attributes) and the machine's
// system attributes file, as for any git merge in the repository. merge-ort, git's merge, uses
// histogram whatever a configuration says (merge-ort.c's merge_start hardcodes it), so a
// configured diff.algorithm changes no merge. git 2.47.3, the worker image's git, reads that
// value first, though (merge-recursive.c's merge_recursive_config), and dies on one it cannot
// parse. A newer git may not: git 2.56.1 merges under such a value, as CI measured. The entry
// keeps the merge from depending on which git runs. The merge (mergeTree) and the reading
// command (Comparison) both give git this list. A caller outside the package reads it through
// MergeTreeOptions, which answers a copy, so no caller changes the list the merge gets.
var mergeTreeOptions = [...]string{
	"-c", "merge.renames=true",
	"-c", "merge.directoryRenames=conflict",
	"-c", "merge.renameLimit=7000",
	"-c", "merge.conflictStyle=merge",
	"-c", "merge.renormalize=false",
	"-c", "merge.default=",
	"-c", "core.attributesFile=/dev/null",
	"-c", "diff.algorithm=histogram",
}

// MergeTreeOptions is the option list the merge gives `git merge-tree`, as a copy. A person who
// compares a merge with git's merge of its parents gives git the same list, or a configuration
// file can make git's merge another one.
func MergeTreeOptions() []string {
	return slices.Clone(mergeTreeOptions[:])
}

// Comparison is the command that prints how merge, a merge commit, differs from git's merge of
// its two parents (`git merge-tree --write-tree`): how it resolved git's conflicts. It gives git
// the merge's option list, so the machine's configuration does not make git's merge another one.
// git rev-parse gives merge-tree the parents' full commit ids, as the merge gives them: git names
// a file it puts aside in a conflict after the revision it was given (p~<id>), so a merge-tree of
// <merge>^1 would name a path the merge does not hold. flags go to git diff, as --name-only does.
// `legion merge --help` prints it, and so do the daemon's messages after a merge forward lands
// (record's readMerge) and its refusal of a completion at a head that a merge forward replaced
// (workflow's mergedForwardRefusal).
func Comparison(merge string, flags ...string) string {
	diff := strings.Join(append([]string{"git", "diff"}, flags...), " ")
	return fmt.Sprintf(`%s "$(git %s merge-tree --write-tree $(git rev-parse %s^1 %s^2) | head -n 1)" %s`,
		diff, strings.Join(mergeTreeOptions[:], " "), merge, merge, merge)
}

// minimumGit is the first git whose merge-tree has --write-tree, which the merge reads.
var minimumGit = [2]int{2, 38}

// carrierEmail is the email of the author and the committer of each carrier. A commit of another
// author is no carrier to the leftover sweep (notCarriers).
const carrierEmail = "legion-merge@localhost"

// carrierIdentity is the author and committer of the commits the merge writes with git plumbing.
// It is set on those children alone, so jj's own commits, the merge among them, keep the role's
// identity.
var carrierIdentity = []string{
	"GIT_AUTHOR_NAME=legion merge", "GIT_AUTHOR_EMAIL=" + carrierEmail,
	"GIT_COMMITTER_NAME=legion merge", "GIT_COMMITTER_EMAIL=" + carrierEmail,
}

// bookmarkPrefix begins every bookmark the merge makes and every run's prefix. The jj
// workspace's name and a dash follow it, so a run touches the commits of its own workspace alone:
// on a tmux clone that several issues share, each issue's workspace has a name of its own.
const bookmarkPrefix = "legion-merge-"

// The roles of the carriers, each an index of carrierRoles. g holds git's tree. k1, k2 and k3
// hold git's tree with each conflicted path at its stage 1, 2 or 3 version. A merge that git makes
// cleanly has g alone.
const (
	carrierG = iota
	carrierK1
	carrierK2
	carrierK3
)

// carrierRoles is the name of each role, which ends its carrier's bookmark. The bookmark's pattern
// and the run's carriers both read this one list, so a role that the list lacks is an index out
// of range where the run writes that carrier, which the compiler refuses.
var carrierRoles = [...]string{carrierG: "g", carrierK1: "k1", carrierK2: "k2", carrierK3: "k3"}

// runTrailer begins the line `Legion-Merge-Run: <run prefix>` that each jj commit of a run
// carries from the moment jj makes it: jj's merge of the carriers, and the merge until its last
// describe takes the line out. A run that something killed leaves these commits behind, and the
// next run in the workspace finds them by this line, as it finds the carriers by their bookmarks.
// The run finds its own commits by it too, never by the children of the two parents, where
// another merge of the same parents can appear. So a merge that carries Legion-Merge and no
// Legion-Merge-Run line is one whose tree the run made git's.
const runTrailer = "Legion-Merge-Run: "

// lockName is the file in the workspace's .jj directory that a run holds locked from its start
// to its end.
const lockName = "legion-merge.lock"

// Request is one merge to make.
type Request struct {
	// Workspace is the root of the jj workspace, an absolute path. Every command runs there.
	Workspace string
	// Message is the merge commit's description, as `jj new -m` takes it.
	Message string
	// Revisions are the parents as the caller wrote them, each a jj revset that names one commit:
	// the first parent, then the second.
	Revisions [2]string
	// Stderr is where the merge names, one line each, a commit of a run that it keeps rather than
	// abandons, with why it keeps it (keep), and a bookmark of a carrier's name that it leaves on a
	// commit that is no carrier. Nil names nothing.
	Stderr io.Writer
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
// It works in the steps of dispatch://INFRA-4149's plan ("What it does, in order"). A run holds
// the workspace's lock (lock) from its start to its end, so one run at a time goes on in a
// workspace. Only the first jj command reads the working copy and only the last moves it; every
// one between carries --ignore-working-copy. git's versions reach jj as carriers: commits git
// plumbing writes, with no parent in the repository, which bookmarks under this workspace's
// prefix make visible. Each jj commit of the run carries the run's Legion-Merge-Run line until
// the merge's last describe takes it out of the merge. On every failure after the carriers are
// written, Merge removes what it made, so the workspace is as it found it: no new visible commit,
// no bookmark and no git ref of its own, and the working copy where it was. A run that something
// killed leaves its carriers, their bookmarks and the commits that carry its line behind, and the
// next run in the same workspace removes them first, all but what keep keeps.
func Merge(ctx context.Context, run Runner, request Request) (Result, error) {
	gitDir, err := workspace.GitDir(request.Workspace)
	if err != nil {
		return Result{}, fmt.Errorf("find the git directory of the workspace %s: %w", request.Workspace, err)
	}
	held, err := lock(request.Workspace)
	if err != nil {
		return Result{}, err
	}
	// Closing the file ends the lock, at the end of the run.
	defer held.Close()
	stderr := request.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	m := &merger{run: run, gitDir: gitDir, request: request, stderr: stderr}
	name, err := m.workspaceName(ctx)
	if err != nil {
		return Result{}, err
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return Result{}, fmt.Errorf("make the run's id: %w", err)
	}
	// Both prefixes are set before any method that reads them runs: with an empty run prefix,
	// ofRun and own would match the line of every run in the workspace.
	m.prefix = bookmarkPrefix + name + "-"
	m.runPrefix = m.prefix + hex.EncodeToString(random) + "-"
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
	result, err := m.build(ctx, parents, tree)
	if err != nil {
		// The undo runs to its end when the caller's context ended the run: a merge that stops
		// half made is what the undo is there to prevent.
		if undoErr := m.remove(context.WithoutCancel(ctx), m.own, m.merge); undoErr != nil {
			err = fmt.Errorf("%w\nThe undo of what this run made also failed: %v", err, undoErr)
		}
		return Result{}, err
	}
	return result, nil
}

// lock holds the workspace's merge lock, <workspace>/.jj/legion-merge.lock, or refuses when
// another run holds it. The leftover sweep at the start of a run takes every commit of this
// workspace's runs, so the lock keeps it off the commits of a run that is still going. flock's
// lock ends with the process that holds it, so a run that something killed leaves no lock.
func lock(workspace string) (*os.File, error) {
	path := filepath.Join(workspace, ".jj", lockName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the merge lock of the workspace: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another legion merge is running in the workspace %s. Wait for it to end, then run the merge again", workspace)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return file, nil
}

// merger is one run of Merge.
type merger struct {
	run     Runner
	gitDir  string
	request Request
	stderr  io.Writer
	// prefix begins every run prefix of this workspace, and runPrefix is this run's: prefix, the
	// run's random id and a dash. Merge sets both at once, before any method reads them. Each
	// carrier's bookmark is its run's prefix and its role, and each jj commit of a run carries the
	// line runTrailer and its run's prefix.
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
	// The carriers' commit ids, by role (carrierRoles).
	var carriers [len(carrierRoles)]string
	var err error
	if carriers[carrierG], err = m.commitTree(ctx, tree.Tree, "", fmt.Sprintf("git's merge of %s and %s", short(parents[0]), short(parents[1]))); err != nil {
		return Result{}, err
	}
	if len(conflicted) > 0 {
		stages, err := m.stageTrees(ctx, tree, conflicted)
		if err != nil {
			return Result{}, err
		}
		// jj's conflict markers quote each side's description, so each names whose version it holds.
		// k1 has no parent, and k2 and k3 stand on k1, their merge base.
		for _, k := range []struct {
			role, stage int
			text        string
		}{
			{carrierK1, 1, fmt.Sprintf("git's merge base of %s and %s", m.request.Revisions[0], m.request.Revisions[1])},
			{carrierK2, 2, fmt.Sprintf("%s %s", m.request.Revisions[0], short(parents[0]))},
			{carrierK3, 3, fmt.Sprintf("%s %s", m.request.Revisions[1], short(parents[1]))},
		} {
			parent := ""
			if k.role != carrierK1 {
				parent = carriers[carrierK1]
			}
			if carriers[k.role], err = m.commitTree(ctx, stages[k.stage-1], parent, k.text); err != nil {
				return Result{}, err
			}
		}
	}
	// Bookmarks and not tags: a tag is in immutable_heads(), and jj abandons no immutable commit.
	// A merge that git makes cleanly has g alone, so the other roles hold no id.
	var refs strings.Builder
	for role, id := range carriers {
		if id != "" {
			fmt.Fprintf(&refs, "create refs/heads/%s%s %s\n", m.runPrefix, carrierRoles[role], id)
		}
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

	// Each jj commit of the run carries the run's line from the moment jj makes it: jj writes a
	// second message as a paragraph of its own after the first, and the configured trailers join
	// that paragraph. So a run killed at any later step leaves a commit the next run finds, and
	// the run finds its own by the line, never another merge of the same commits.
	marker := runTrailer + m.runPrefix
	// The source is the tree the merge takes: git's, or, where git conflicts, X, jj's merge of
	// git's own three versions of each conflicted path over K1, their one merge base. X equals
	// git's tree on every other path.
	source := carriers[carrierG]
	if len(conflicted) > 0 {
		if _, err := m.jj(ctx, "new", "--no-edit", "--message=jj's merge of git's versions of the paths git conflicts in", "--message="+marker, carriers[carrierK2], carriers[carrierK3]); err != nil {
			return Result{}, err
		}
		x, err := m.one(ctx, m.ofRun(fmt.Sprintf("children(%s) & children(%s)", carriers[carrierK2], carriers[carrierK3])), "change_id")
		if err != nil {
			return Result{}, fmt.Errorf("find jj's merge of the carriers %s and %s: %w", carriers[carrierK2], carriers[carrierK3], err)
		}
		source = x
	}

	_, newErr := m.jj(ctx, "new", "--no-edit", "--message="+m.request.Message, "--message="+marker, parents[0], parents[1])
	// The merge is the one child of both parents that carries the run's line, read also when jj
	// new failed: a merge jj made before it failed is still one for the undo to abandon.
	made, err := m.list(ctx, m.ofRun(fmt.Sprintf("children(%s) & children(%s)", parents[0], parents[1])), "change_id")
	if err != nil {
		return Result{}, errors.Join(newErr, err)
	}
	if len(made) == 1 {
		m.merge = made[0]
	}
	if newErr != nil {
		return Result{}, newErr
	}
	if len(made) != 1 {
		return Result{}, fmt.Errorf("jj new made %d children of %s and %s that carry the line %q, not one: %s", len(made), parents[0], parents[1], marker, strings.Join(made, " "))
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
	back, err := m.diffPaths(ctx, carriers[carrierG], m.merge)
	if err != nil {
		return Result{}, err
	}
	if other := slices.DeleteFunc(back, func(path string) bool { return conflict[path] }); len(other) > 0 {
		return Result{}, fmt.Errorf("after the restore, the merge differs from git's merge at paths git does not conflict in: %s", quotedPaths(other))
	}
	// The describe is the last write to the merge, once its tree is git's: it takes the run's
	// line out and puts Legion-Merge last, after the trailers jj wrote, since jj appends the
	// configured trailers after the description it is given, so a trailer passed with -m would
	// come before the role's own. A run killed before the describe leaves a merge that still
	// carries the run's line, which the next run removes.
	description, err := m.jj(ctx, "log", "--no-graph", "-r", m.merge, "-T", "description")
	if err != nil {
		return Result{}, err
	}
	if _, err := m.jj(ctx, "describe", "--message="+withTrailer(withoutLine(string(description), marker), "Legion-Merge: "+tree.Tree), m.merge); err != nil {
		return Result{}, err
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
	// The merge no longer carries the run's line, so this removes the carriers and X alone.
	if err := m.remove(ctx, m.own, ""); err != nil {
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

// ofRun is the commits of revset that carry this run's line.
func (m *merger) ofRun(revset string) string {
	return fmt.Sprintf("%s & description(substring:%s)", revset, workspace.JJString(runTrailer+m.runPrefix))
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
		return "", fmt.Errorf("jj names %d workspaces whose working-copy commit is this workspace's, not one: %q. "+
			"Run jj new in this workspace to give it a working-copy commit of its own. Then run the merge again", len(names), names)
	}
	return names[0], nil
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
	case parseErr != nil && !clean:
		// git exits 1 also where it cannot merge at all, as for jj's root commit as a parent
		// ("not something we can merge"), and then prints no merge: what it said on stderr is
		// the reason.
		return gitMerge{}, errors.Join(err, parseErr)
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

// RootFile is the jj fileset that names path alone, a repository-relative path: a path with
// spaces, quotes, parentheses or a leading dash is still that one file.
func RootFile(path string) string {
	return "root-file:" + workspace.JJString(path)
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
