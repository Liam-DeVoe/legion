package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sjawhar/legion/daemon/internal/jjmerge"
	"github.com/sjawhar/legion/daemon/internal/runtime/shellprefix"
)

// newMergeCommand is `legion merge`, the one way a role makes the merge commit of two commits:
// jj merges two commits that have several merge bases wrongly and silently (jj-vcs/jj#6369), and
// the merge this command makes holds git's merge (jjmerge.Merge). A role or a person types it, so
// it is in help.
func newMergeCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "merge [-R <workspace>] -m <message> <first> <second>",
		Short: "make the merge commit of two commits in a jj workspace, holding git's merge of them",
		Long:  mergeHelp(),
	}
	repository := cmd.Flags().StringP("repository", "R", "", "a directory of the jj workspace (default: the current directory)")
	message := cmd.Flags().StringP("message", "m", "", "the description of the merge commit (required)")
	// A count of revisions other than two is the merge's own refusal, exit 3, so any count of
	// words passes here. A flag cobra marks required fails as a plain error, which run answers
	// with 1, so a missing -m is a usage error of this command's own.
	cmd.Args = func(*cobra.Command, []string) error {
		if *message == "" {
			return usageError{errors.New("name the description of the merge commit with -m <message>")}
		}
		return nil
	}
	cmd.RunE = runs(func(ctx context.Context, args []string) int {
		return runMerge(ctx, jjmerge.Exec{}, *repository, *message, args, stdout, stderr)
	})
	return cmd
}

// mergeRefusedNextStep is the last lines of every refusal and failure, exit 3.
const mergeRefusedNextStep = "Next step: act on the reason above.\nDo not make the merge with jj new.\n"

// runMerge is `legion merge` once cobra has read its flags: it merges revisions in the jj
// workspace that the directory repository is in, running each git and jj through runner. It
// answers 0 for a merge git made cleanly and 1 for a merge that holds git's conflicts, each with
// the merge on stdout, and 3 for a refusal or a failure, with the reason on stderr and nothing
// made. A commit of an earlier run that the merge keeps, because another commit stands on it, and
// a bookmark of a carrier's name that it leaves on a commit no legion merge wrote, are named on
// stderr too, and the merge goes on.
func runMerge(ctx context.Context, runner jjmerge.Runner, repository, message string, revisions []string, stdout, stderr io.Writer) int {
	root, err := mergeWorkspace(repository)
	if err == nil {
		var result jjmerge.Result
		if result, err = jjmerge.Merge(ctx, runner, jjmerge.Request{Workspace: root, Message: message, Revisions: revisions, Stderr: stderr}); err == nil {
			return reportMerge(stdout, root, revisions, result)
		}
	}
	fmt.Fprintf(stderr, "legion merge: %v\n%s", err, mergeRefusedNextStep)
	return 3
}

// mergeWorkspace is the root of the jj workspace dir is in: dir, or the nearest directory above it
// that holds .jj, where jj itself finds its workspace from the directory it runs in. An empty dir
// is the current directory. The merge runs every git and jj there and reads the git directory
// from there.
func mergeWorkspace(dir string) (string, error) {
	start, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("find the workspace: %w", err)
	}
	for at := start; ; at = filepath.Dir(at) {
		if info, err := os.Stat(filepath.Join(at, ".jj")); err == nil && info.IsDir() {
			return at, nil
		}
		if filepath.Dir(at) == at {
			return "", fmt.Errorf("%s is in no jj workspace: neither it nor a directory above it holds .jj", start)
		}
	}
}

// Labels of what the merge holds at a path git conflicts in. The exit-1 next step and the help
// name them in these words.
const (
	jjConflictLabel = "a jj conflict"
	jjMergedLabel   = "a conflict of git's that jj merged"
)

// reportMerge prints the merge on stdout and answers its exit status: 0 when git merged cleanly,
// and 1 with the next step as the last lines when the merge holds git's conflicts.
func reportMerge(stdout io.Writer, root string, revisions []string, result jjmerge.Result) int {
	fmt.Fprintf(stdout, "The merge commit is %s. Its change id is %s.\n", result.CommitID, result.ChangeID)
	fmt.Fprintf(stdout, "Its first parent is %s (%s). Its second parent is %s (%s).\n",
		revisions[0], result.Parents[0], revisions[1], result.Parents[1])
	fmt.Fprintf(stdout, "git's merged tree is %s. The last line of the description is \"Legion-Merge: %s\".\n", result.Tree, result.Tree)
	if len(result.Differed) > 0 {
		fmt.Fprintln(stdout, "jj's own merge of the two parents is different from git's merge at these paths. The merge holds git's merge at each path:")
		for _, path := range result.Differed {
			fmt.Fprintf(stdout, "  %s\n", shownPath(path))
		}
	}
	fmt.Fprintln(stdout, "The working copy is an empty commit on top of the merge.")
	if len(result.Conflicts) == 0 {
		return 0
	}
	fmt.Fprintln(stdout, "git's conflicts, in git's words:")
	for _, message := range result.Messages {
		fmt.Fprintf(stdout, "  %s\n", strings.ReplaceAll(message.Text, "\n", "\n  "))
	}
	fmt.Fprintln(stdout, "What the merge holds at each path of git's conflicts:")
	var paths []string
	var merged, conflicts bool
	for _, conflict := range result.Conflicts {
		label := jjMergedLabel
		if conflict.JJConflict {
			label, conflicts = jjConflictLabel, true
		} else {
			merged = true
		}
		fmt.Fprintf(stdout, "  %s: %s\n", shownPath(conflict.Path), label)
		paths = append(paths, conflict.Path)
	}
	var steps []string
	if merged {
		steps = append(steps, fmt.Sprintf("Read each path that is %q. Make sure that the merge holds the correct content there.", jjMergedLabel))
	}
	if conflicts {
		steps = append(steps, "Resolve each jj conflict in the working-copy commit.")
	}
	steps = append(steps,
		"Move the resolution into the merge with this command:\n     "+squashCommand(root, result.ChangeID, paths),
		"Push the merge.")
	fmt.Fprintln(stdout, "Next step:")
	for i, step := range steps {
		fmt.Fprintf(stdout, "  %d. %s\n", i+1, step)
	}
	// A merge whose every conflicted path jj merged holds no jj conflict, which no push refuses.
	if conflicts {
		fmt.Fprintf(stdout, "If you keep a part of the resolution for a later turn, that part stays in a commit above the merge.\n"+
			"Then jj git push refuses the merge, because the merge still holds the conflict.\n"+
			"The command in step %d moves the resolution into the merge.\n", len(steps)-1)
	}
	return 1
}

// squashCommand is the command that moves a resolution into the merge, written so that a shell
// runs it as it stands from any directory. It takes the paths from every commit between the merge
// and the working copy, since a resolution can sit in more than one, and keeps the merge's
// description, its trailers included: a squash of the working copy alone misses a resolution a
// later turn's `jj new` left below it.
func squashCommand(root, change string, paths []string) string {
	words := []string{"jj", "-R", shellprefix.Literal(root), "squash", "--from", shellprefix.Literal(change + "+::@"), "--into", change, "--use-destination-message"}
	for _, path := range paths {
		words = append(words, shellprefix.Literal(jjmerge.RootFile(path)))
	}
	return strings.Join(words, " ")
}

// shownPath is path as a line of the output shows it: as it is, or Go-quoted when it holds a
// character that would change the line, as a newline does.
func shownPath(path string) string {
	if quoted := strconv.Quote(path); quoted[1:len(quoted)-1] != path {
		return quoted
	}
	return path
}

// mergeComparison is the command that prints how merge differs from git's merge of its parents,
// with the merge's own option list, so no configuration file makes git's merge another one. git
// rev-parse gives merge-tree the parents' full commit ids, as the merge gives them: git names a
// file it puts aside in a conflict after the revision it was given (p~<id>), so a merge-tree of
// <merge>^1 would name a path the merge does not hold. flags go to git diff, as --name-only does.
func mergeComparison(merge string, flags ...string) string {
	diff := strings.Join(append([]string{"git", "diff"}, flags...), " ")
	return fmt.Sprintf(`%s "$(git %s merge-tree --write-tree $(git rev-parse %s^1 %s^2) | head -n 1)" %s`,
		diff, strings.Join(jjmerge.MergeTreeOptions(), " "), merge, merge, merge)
}

// mergeHelp is `legion merge --help`'s text: what the command does and why, that one run at a
// time goes on in a workspace and what the next run does with the commits a stopped run left, the
// next step of each exit status, the case of a legion that has no merge command, what goes into
// the merge, how to name a merge, and how to read one.
func mergeHelp() string {
	return `legion merge makes the merge commit of two commits in a jj workspace.
The merge holds git's merge of the two commits (git merge-tree --write-tree).
When two commits have more than one merge base, jj can merge them incorrectly and show no conflict (jj-vcs/jj#6369).
Make each merge of two commits with legion merge.
Do not make a merge with jj new. This rule applies also when a skill or a role prompt names jj new.

<first> is the first parent. <second> is the second parent.
Each one is a jj revset that names one commit, for example legion/ACME-1 or '"main"@origin'.
-m gives the description of the merge.
The command adds the line "Legion-Merge: <git's tree id>" as the last line of the description.
-R names a directory of the jj workspace. The default is the current directory.

On each path that git merges with no conflict, the merge holds git's merge.
On each path where git has a conflict, the merge holds jj's merge of git's three versions of the path.
The output names each such path as "` + jjConflictLabel + `" or as "` + jjMergedLabel + `".
"` + jjMergedLabel + `" means that jj merged git's versions with no jj conflict.
The command then makes the working copy an empty commit on top of the merge.

One legion merge runs in a workspace at a time.
While another legion merge runs in the workspace, the command refuses with exit 3.
Each commit that the command makes in jj carries the line "Legion-Merge-Run: <run>".
When the merge holds git's merge, the command removes this line from the merge and adds the Legion-Merge line.
If a run stops before its end, the next run in the workspace removes the commits that the stopped run made.
It keeps a commit that another commit stands on or that is a working-copy commit, and it names that commit on stderr.

Exit status and next step:
  0  The command made the merge, and git merged with no conflict.
     Push the merge.
  1  The command made the merge, and the merge holds the conflicts of git's that the output lists.
     Read each path that the output names as "` + jjMergedLabel + `".
     Resolve each jj conflict in the working-copy commit.
     Move the resolution into the merge with the squash command that the output gives:
       jj squash --from '<change id>+::@' --into <change id> --use-destination-message <each listed path>
     Push the merge.
     If you keep a part of the resolution for a later turn, that part stays in a commit above the merge.
     Then jj git push refuses the merge, because the merge still holds the conflict.
     The squash command moves the resolution into the merge.
  2  The command line has a usage error, for example an unknown flag or no -m. The command made nothing.
     Correct the command line.
     If legion says 'unknown command "merge"', the legion that you use has no merge command.
     Then make no merge. This rule applies also when a skill or a role prompt names jj new.
     Tell the architect.
  3  The command refused the merge or failed, and it gives the reason. The command made nothing.
     Act on the reason.
     Do not make the merge with jj new.

Put only the resolution of git's conflicts into the merge.
Put each other change into a commit above the merge.

A squash into the merge gives the merge a new commit id. The change id of the merge does not change.
Use the change id to name the merge in jj commands.

Read a merge with git. In these commands, <merge> is the commit id of the merge.
Get it with: jj log -r <change id> --no-graph -T commit_id
git rev-parse gives git the full commit ids of the parents, as the command gives them.
Then git names a file that it puts aside in a conflict (p~<commit id>) as the merge names it.
  The difference between the merge and git's merge of its parents (the resolution of git's conflicts):
    ` + mergeComparison("<merge>") + `
  The files of that difference:
    ` + mergeComparison("<merge>", "--name-only") + `
  The files that the merge brings into its first parent:
    git diff --stat <merge>^1 <merge>
Do not read a merge with jj diff -r <merge>.
jj diff -r compares the merge with jj's own merge of its parents, and that merge can be incorrect.`
}
