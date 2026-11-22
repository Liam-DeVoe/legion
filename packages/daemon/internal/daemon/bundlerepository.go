package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// tellBundleRepositoryRefused is the store's take of an issue's saved workspace bundle for a pod
// of project (savedWorkspaceBundle, store.Store.TakeWorkspaceBundleForRestore) as the runtime
// asks it, with the one thing the daemon owes a person added to it: the bundle the store refused
// for its repository — of another repository than the one the asking pod was built from, or with
// either name unknown — is told on the issue itself (bundleRepositoryMessage, written through
// write). Every other outcome passes through untouched, and the refusal passes through as it
// was: the pod still starts on what its own fetch provisioned. A bundle of an issue project does
// not record is answered nothing by the store, so no message is ever written on another
// project's issue from here.
//
// Whether the bundle's repository and the pod's are two repositories is left's to say, and the
// daemon hands it the rule a pod built from a repository is read by too (repositories.left,
// repositoryDrift), so the restore and the update round never disagree about which names are one
// repository: a configured rename's two names are one repository to both.
//
// The issue is told once per bundle row, not once per pod that asks. The refusal is the bundle's
// one restore attempt, and the store records it and moves the row to workspace_bundles_refused in
// the transaction that answers it, so the next pod of the issue is answered
// runtime.WorkspaceBundleNone and reaches no message at all.
//
// It is this wrapper and not the launcher's that logs the refusal, and the launcher logs none:
// only a person can act on it — the work is a bundle of commits of a repository the pod's clone
// is not of, and nothing in the daemon ever puts a refused bundle back — and this is the one
// place that knows whether the message landed. A log line reaches an operator who reads the
// daemon's logs. The message reaches the agent that did the work. The log line is one for every
// such refusal, and its bundleRepository and podRepository say which names the store compared,
// an empty one being the name nothing read.
//
// write is never nil: every daemon builds this over the plan's own write (plan.writeBodyOnIssue), a
// method value that answers an error in a deployment with no workflow, which has no outbox to
// write through and no Dispatch issue to write on, and such a deployment takes the error arm
// below, where the error is logged and the work stays in the database. A rig that builds the
// runtime by hand passes a writer of its own (controller_sweep_kind_test.go's
// noWriteOnIssue).
func tellBundleRepositoryRefused(st *store.Store, project string, left func(saved, pod string) bool,
	write func(ctx context.Context, issue, body string) error, log *slog.Logger) runtime.SavedWorkspaceBundle {
	if log == nil {
		log = slog.Default()
	}
	take := savedWorkspaceBundle(st, project, left)
	return func(ctx context.Context, issue, podUID, podRepo string, podCreated time.Time) (runtime.WorkspaceBundle, runtime.WorkspaceBundleTake, error) {
		bundle, outcome, err := take(ctx, issue, podUID, podRepo, podCreated)
		if err != nil || outcome != runtime.WorkspaceBundleRefusedRepository {
			return bundle, outcome, err
		}
		if err := write(ctx, issue, bundleRepositoryMessage(issue, podRepo, bundle)); err != nil {
			if ctx.Err() == nil {
				log.Error("supervise: write on the issue that its saved workspace bundle is refused for its repository; the work is kept in workspace_bundles_refused and nothing tells the issue again",
					"issue", issue, "pod", podUID, "bundleRepository", bundle.Repo, "podRepository", podRepo,
					"head", bundle.Head, "saved", bundle.At, "release", bundle.Release, "error", err)
			}
			return bundle, outcome, nil
		}
		log.Warn("supervise: the saved workspace bundle of this issue is refused for its repository, so no pod restores it; the issue is told and the work is kept in workspace_bundles_refused",
			"issue", issue, "pod", podUID, "bundleRepository", bundle.Repo, "podRepository", podRepo,
			"head", bundle.Head, "saved", bundle.At, "release", bundle.Release)
		return bundle, outcome, nil
	}
}

// bundleRepositoryMessage is what issue is told when the daemon refuses to restore its saved
// work into the pod built from podRepo: the repository the work is of, the commit the saved
// working copy sat on, each bookmark that pointed into the work, the moment the release saved it,
// and where the daemon keeps the work — the table the refusal moved it to
// (store.Store.TakeWorkspaceBundleForRestore) and the issue and release that find its row there,
// which is what an operator asked for the work reads. Another agent reads it, so it follows
// ASD-STE100.
//
// It names the repository the work is of and never the pod's, on purpose. The work is in that
// repository's history, and a reader sent to the pod's repository would look for commits that
// are not there. The moment of the save is the reader's other handle on the work: it says how
// much of the issue's work the bundle can hold. It says nothing of the issue's pods to come:
// Dispatch can move the issue back, and a refused bundle is not restored then either.
//
// Each sentence on the repositories says only what the store read. A bundle that names no
// repository (b.Repo empty) is work nothing recorded the repository of, so the message says that
// rather than name one, and says only that the pod can be a clone of another. An empty podRepo is
// a pod whose repository the daemon could not read, so the message says that, and not that the
// pod is a clone of another repository, which nothing read either.
func bundleRepositoryMessage(issue, podRepo string, b runtime.WorkspaceBundle) string {
	var lines []string
	for _, name := range slices.Sorted(maps.Keys(b.Bookmarks)) {
		lines = append(lines, fmt.Sprintf("- %s at commit `%s`", codeSpan(name), b.Bookmarks[name]))
	}
	bookmarks := "The saved work has no bookmark."
	if len(lines) > 0 {
		bookmarks = fmt.Sprintf("The saved work has these bookmarks:\n\n%s", strings.Join(lines, "\n"))
	}
	var repository string
	switch {
	case b.Repo == "":
		repository = "The daemon did not record the repository that the work is of. " +
			"The new pod of this issue can be a clone of a different repository."
	case podRepo == "":
		repository = "The work is of the repository `" + b.Repo + "`. " +
			"The daemon cannot read the repository that the new pod of this issue is a clone of."
	default:
		repository = "The work is of the repository `" + b.Repo + "`. " +
			"The new pod of this issue is a clone of a different repository."
	}
	return fmt.Sprintf(`The daemon did not restore the saved work of this issue.

A release of this issue's pod saved the work that no remote had. The daemon saved that work at %s. %s The daemon puts saved work only into a clone of the repository that the work is of.

The saved work has the head commit `+"`%s`"+`. %s

The daemon keeps the saved work in its database, in the table `+"`workspace_bundles_refused`"+`. The row has the issue `+"`%s`"+` and the release_id `+"`%d`"+`. The `+"`bundle`"+` column of that row is a git bundle. The daemon does not send this work to a pod, and the daemon does not delete it. Ask an operator for the work if you need it.`,
		b.At.UTC().Format(time.RFC3339), repository, b.Head, bookmarks, issue, b.Release)
}

// codeSpan is name inside a Markdown code span that name itself cannot break. A bookmark name
// is whatever the pod's jj answered the release (runtime.WorkspaceBundle.Bookmarks), so it is
// not the daemon's text: one holding a backtick would close the span and leave the rest of the
// message in and out of code, and one holding a line break would end the list item and put the
// remainder of the name at the start of a line, where it reads as a sentence the daemon never
// wrote.
//
// The fence is one backtick longer than the longest run of backticks in the name, which is how
// a code span holds a backtick (CommonMark), with a space inside each fence when the name
// starts or ends with one, which is how that span keeps the backtick rather than eating it.
// Every line break becomes a space: a code span holds none, and the name is still whole and
// still one item of the list.
func codeSpan(name string) string {
	name = strings.NewReplacer("\n", " ", "\r", " ").Replace(name)
	longest, run := 0, 0
	for _, r := range name {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	fence := strings.Repeat("`", longest+1)
	pad := ""
	if strings.HasPrefix(name, "`") || strings.HasSuffix(name, "`") {
		pad = " "
	}
	return fence + pad + name + pad + fence
}
