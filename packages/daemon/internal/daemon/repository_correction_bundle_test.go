package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// The work a release saved out of a pod is work of the repository that pod was built from. A
// correction of the issue's repository in Dispatch moves the issue to another repository, its
// next pod is a clone of that one, and the store refuses the bundle to that pod
// (store.TakeWorkspaceBundleForRestore) and moves it to workspace_bundles_refused. Nothing in the
// daemon puts that work back, so the daemon says so where the work is asked for: a message on the
// Dispatch issue, naming the repository the work is of, the commit the saved working copy sat on,
// the bookmarks that pointed into it, the moment of the save, and the table, issue and release of
// the row that keeps it.
//
// It is said once per bundle row. The refusal is the bundle's one attempt and the store records
// it as it answers, so the pod after it is answered nothing and reaches no message.
func TestRepositoryCorrectionTellsTheIssueWhereItsSavedWorkIs(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Project: "LEGION", Tree: "LEGION-208", Title: "root", Phase: phase.Implementing, Generation: 1,
		Status: "in_progress", Rank: "U", LastDispatchSeq: 1}
	putOutboxIssue(t, pool, records, issue)
	st := outboxTreeStore(t, pool, issue.Key)

	at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	saved := runtime.WorkspaceBundle{
		Bundle:    []byte("PACK the work of the old repository"),
		Head:      "f2f134541b30dfa5160a5d1ce5e17b76c967c661",
		Tips:      []string{"f2f134541b30dfa5160a5d1ce5e17b76c967c661"},
		Bookmarks: map[string]string{"legion/LEGION-208": "dffe88ae2f793e13436cef3921adc988433fbd4e"},
		At:        at,
		PodUID:    releasedPodUID,
		Repo:      issue.Repo,
	}
	if err := st.PutWorkspaceBundle(ctx, issue.Key, 11, saved); err != nil {
		t.Fatalf("save the released workspace's bundle: %v", err)
	}
	// Dispatch moved the issue to another repository, and the record followed the correction
	// (record.(*Postgres).CorrectIssueRepository).
	correctRecord(t, pool, records, issue.Key, "zeta/tools", "main", time.Now().UTC())

	var told []string
	take := tellBundleRepositoryRefused(st, "legion", unrenamed,
		func(_ context.Context, key, body string) error {
			if key != issue.Key {
				t.Errorf("the daemon wrote on %s, want %s: the work is that issue's", key, issue.Key)
			}
			told = append(told, body)
			return nil
		}, quietLogger())

	// The pod the correction's own replacement made, a clone of zeta/tools, created after the
	// save.
	refused, outcome, err := take(ctx, issue.Key, "pod-of-the-new-repository", "zeta/tools", at.Add(time.Minute))
	if err != nil || outcome != runtime.WorkspaceBundleRefusedRepository {
		t.Fatalf("the pod of the new repository was answered %v (%v), want the bundle refused for its repository", outcome, err)
	}
	if len(refused.Bundle) != 0 {
		t.Fatalf("the refused pod was answered %d bytes of bundle, want none", len(refused.Bundle))
	}
	// The pod after it: an eviction's, an update round's, a park and unpark's. The attempt is
	// spent, so it is answered nothing and the issue is told nothing more.
	if _, outcome, err := take(ctx, issue.Key, "pod-after-an-eviction", "zeta/tools", at.Add(time.Hour)); err != nil || outcome != runtime.WorkspaceBundleNone {
		t.Fatalf("the pod after the refusal was answered %v (%v), want nothing: the refusal was the bundle's one attempt", outcome, err)
	}
	if len(told) != 1 {
		t.Fatalf("the daemon wrote %d messages on the issue, want the one the bundle row earns:\n%s", len(told), strings.Join(told, "\n---\n"))
	}

	body := told[0]
	t.Logf("the daemon writes this on the issue:\n%s", body)
	for _, want := range []string{
		"acme/widgets",
		saved.Head,
		"legion/LEGION-208",
		"dffe88ae2f793e13436cef3921adc988433fbd4e",
		at.Format(time.RFC3339),
		"in the table `workspace_bundles_refused`. The row has the issue `LEGION-208` and the release_id `11`.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the message on the issue does not name %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "zeta/tools") {
		t.Errorf("the message names the repository the issue is on now, which holds none of this work:\n%s", body)
	}

	// The work itself is in the row the message names, which is what an operator reads.
	var kept []byte
	if err := pool.QueryRow(ctx, `select bundle from workspace_bundles_refused where issue = $1 and release_id = $2`,
		issue.Key, 11).Scan(&kept); err != nil || string(kept) != string(saved.Bundle) {
		t.Fatalf("the row the message names holds %q (%v), want the %d bytes saved", kept, err, len(saved.Bundle))
	}
}

// A take's refusal for the repository can commit while the commit's reply is lost and the store
// cannot say, within the take's bound, that it committed: the take answers
// store.ErrCommitOutcomeUnknown (store.Store.TakeWorkspaceBundleForRestore), and the bundle is in
// workspace_bundles_refused all the same. No later take answers that refusal, since the row is
// gone from workspace_bundles, so the daemon reads that table for a refusal of the issue to the
// pod that asked (store.Store.RefusedWorkspaceBundle) and, finding it, tells the issue and answers
// the refusal as it does one that committed. Here the store answers again only half a pause after
// the take's bound has ended, so the take's own wait for the outcome cannot learn it and the
// read's first try is refused: the read is tried again until it answers (refusalRead), so the
// next try finds the refusal whatever order the goroutines run in. Control: without that read the
// take answers its error, and the issue is told nothing.
func TestABundleRefusalWhoseCommitOutcomeStaysUnknownIsToldOnTheIssue(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Project: "LEGION", Tree: "LEGION-208", Title: "root", Phase: phase.Implementing, Generation: 1,
		Status: "in_progress", Rank: "U", LastDispatchSeq: 1}
	putOutboxIssue(t, pool, records, issue)
	st := outboxTreeStore(t, pool, issue.Key)

	at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	saved := runtime.WorkspaceBundle{
		Bundle:    []byte("PACK the work of the old repository"),
		Head:      "f2f134541b30dfa5160a5d1ce5e17b76c967c661",
		Bookmarks: map[string]string{"legion/LEGION-208": "dffe88ae2f793e13436cef3921adc988433fbd4e"},
		At:        at,
		PodUID:    releasedPodUID,
		Repo:      issue.Repo,
	}
	if err := st.PutWorkspaceBundle(ctx, issue.Key, 11, saved); err != nil {
		t.Fatalf("save the released workspace's bundle: %v", err)
	}
	correctRecord(t, pool, records, issue.Key, "zeta/tools", "main", time.Now().UTC())

	faulted, fault := faultedStoreOf(t, st)
	logs, log := watchedLog()
	var told []string
	take := tellBundleRepositoryRefused(faulted, "legion", unrenamed,
		func(_ context.Context, key, body string) error {
			if key != issue.Key {
				t.Errorf("the daemon wrote on %s, want %s: the work is that issue's", key, issue.Key)
			}
			told = append(told, body)
			return nil
		}, log)

	fired := fault.Cut("update workspace_bundles", testdb.CutAfterCommitRefusing)
	taking, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// The store answers again half a pause after the take's bound has ended, which is when the
	// take stops waiting for the outcome: the read's first try, made at once, is refused.
	stop := context.AfterFunc(taking, func() { time.AfterFunc(bundleRefusalReadPause/2, func() { fault.Refuse(false) }) })
	defer stop()
	refused, outcome, err := take(taking, issue.Key, "pod-of-the-new-repository", "zeta/tools", at.Add(time.Minute))
	testdb.Met(t, fired)
	if len(told) != 1 {
		t.Fatalf("the daemon wrote %d messages on the issue, the take answering %v (%v); want the one the refusal earns:\n%s",
			len(told), outcome, err, strings.Join(told, "\n---\n"))
	}
	if err != nil || outcome != runtime.WorkspaceBundleRefusedRepository || len(refused.Bundle) != 0 {
		t.Fatalf("the pod of the new repository was answered %v with %d bytes (%v), want the bundle refused for its repository and nothing sent",
			outcome, len(refused.Bundle), err)
	}
	if got := count(logs, "did not learn whether it committed"); got != 1 {
		t.Fatalf("the daemon logged %d lines of a take whose outcome stayed unknown, want 1: the refusal was read back, not answered by the take; log=%q", got, logs.String())
	}
	body := told[0]
	for _, want := range []string{
		"acme/widgets",
		saved.Head,
		"legion/LEGION-208",
		"dffe88ae2f793e13436cef3921adc988433fbd4e",
		at.Format(time.RFC3339),
		"in the table `workspace_bundles_refused`. The row has the issue `LEGION-208` and the release_id `11`.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the message on the issue does not name %q:\n%s", want, body)
		}
	}
}

// A rename is no move. A deployment that configures acme/widgets as renamed to acme/gadgets reads
// the two names as one repository wherever it compares them (repositories.same), so a pod built
// from the new name is a clone of the repository the saved work is of, as no pod built from the
// old name is replaced for it either (repositoryDrift). The clone the pod holds has the saved
// commits, and the bundle is sent to it, with nothing written on the issue.
func TestARenamedRepositorysBundleIsRestored(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Project: "LEGION", Tree: "LEGION-208", Title: "root", Phase: phase.Implementing, Generation: 1,
		Status: "in_progress", Rank: "U", LastDispatchSeq: 1}
	putOutboxIssue(t, pool, records, issue)
	st := outboxTreeStore(t, pool, issue.Key)

	at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	saved := runtime.WorkspaceBundle{Bundle: []byte("PACK the released work"),
		Head: "f2f134541b30dfa5160a5d1ce5e17b76c967c661", At: at, PodUID: releasedPodUID, Repo: issue.Repo}
	if err := st.PutWorkspaceBundle(ctx, issue.Key, 11, saved); err != nil {
		t.Fatalf("save the released workspace's bundle: %v", err)
	}
	correctRecord(t, pool, records, issue.Key, "acme/gadgets", "main", time.Now().UTC())

	mapping := newRepositories("LEGION", ghrepo.MustRenames(ghrepo.MustRename("acme/widgets", "acme/gadgets")))
	var told []string
	take := tellBundleRepositoryRefused(st, "legion", mapping.same,
		func(_ context.Context, _, body string) error {
			told = append(told, body)
			return nil
		}, quietLogger())

	got, outcome, err := take(ctx, issue.Key, "pod-of-the-new-name", "acme/gadgets", at.Add(time.Minute))
	if err != nil || outcome != runtime.WorkspaceBundleSend || string(got.Bundle) != string(saved.Bundle) {
		t.Fatalf("the pod of the rename's new name was answered %v with %q (%v), want the saved work sent", outcome, got.Bundle, err)
	}
	if len(told) != 0 {
		t.Fatalf("the daemon wrote on the issue for a bundle it restored:\n%s", strings.Join(told, "\n---\n"))
	}
}

// The store refuses a bundle for its repository where either name it compares is unknown: a
// bundle whose row names no repository — one an earlier release saved, before the column or after
// a roll back to it — and a pod whose repository the runtime could not read. Neither is ever taken
// for a match, so the work is kept and the issue is told, and the message says only what the
// store read: that the daemon did not record the work's repository, or that it cannot read the
// pod's, and never that the pod is a clone of a different repository, which nothing read. The
// daemon's log says the same refusal in one line for every case, whose bundleRepository and
// podRepository name what the store compared. Control: a take that reads an empty name as no
// change sends the work, and writes nothing.
func TestABundleRefusedForAnUnknownRepositoryIsKeptAndTheIssueTold(t *testing.T) {
	for _, tc := range []struct {
		name         string
		bundle, pod  string
		says, wrongs []string
	}{
		{name: "the bundle names no repository", bundle: "", pod: "acme/widgets",
			says:   []string{"The daemon did not record the repository that the work is of."},
			wrongs: []string{"of the repository ``", "is a clone of a different repository."}},
		{name: "the pod's repository is unread", bundle: "acme/widgets", pod: "",
			says: []string{"The work is of the repository `acme/widgets`.",
				"The daemon cannot read the repository that the new pod of this issue is a clone of."},
			wrongs: []string{"is a clone of a different repository."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := isolatedOutboxPool(t)
			records := record.NewStore()
			issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
				Project: "LEGION", Tree: "LEGION-208", Title: "root", Phase: phase.Implementing, Generation: 1,
				Status: "in_progress", Rank: "U", LastDispatchSeq: 1}
			putOutboxIssue(t, pool, records, issue)
			st := outboxTreeStore(t, pool, issue.Key)

			at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
			saved := runtime.WorkspaceBundle{Bundle: []byte("PACK the work of an unknown repository"),
				Head: "f2f134541b30dfa5160a5d1ce5e17b76c967c661", At: at, PodUID: releasedPodUID, Repo: "acme/widgets"}
			if err := st.PutWorkspaceBundle(ctx, issue.Key, 11, saved); err != nil {
				t.Fatalf("save the released workspace's bundle: %v", err)
			}
			// The save refuses a bundle that names no repository, so the row an earlier release
			// left with none is written here.
			if tc.bundle == "" {
				if _, err := pool.Exec(ctx,
					`update workspace_bundles set repo = $2 where issue = $1`,
					issue.Key, tc.bundle); err != nil {
					t.Fatalf("leave the bundle of %s on %q: %v", issue.Key, tc.bundle, err)
				}
			}
			var told []string
			var logged syncBuffer
			take := tellBundleRepositoryRefused(st, "legion", unrenamed,
				func(_ context.Context, _, body string) error {
					told = append(told, body)
					return nil
				}, slog.New(slog.NewJSONHandler(&logged, nil)))

			got, outcome, err := take(ctx, issue.Key, "pod-replacement", tc.pod, at.Add(time.Minute))
			if err != nil || outcome != runtime.WorkspaceBundleRefusedRepository || len(got.Bundle) != 0 {
				t.Fatalf("the replacement pod was answered %v with %d bytes (%v), want the bundle refused and nothing sent", outcome, len(got.Bundle), err)
			}
			if len(told) != 1 {
				t.Fatalf("the daemon wrote %d messages on the issue, want one:\n%s", len(told), strings.Join(told, "\n---\n"))
			}
			body := told[0]
			t.Logf("the daemon writes this on the issue:\n%s", body)
			for _, want := range append(tc.says, saved.Head,
				"in the table `workspace_bundles_refused`. The row has the issue `LEGION-208` and the release_id `11`.") {
				if !strings.Contains(body, want) {
					t.Errorf("the message on the issue does not say %q:\n%s", want, body)
				}
			}
			for _, wrong := range tc.wrongs {
				if strings.Contains(body, wrong) {
					t.Errorf("the message on the issue says %q, which nothing read:\n%s", wrong, body)
				}
			}
			var line struct {
				Msg              string `json:"msg"`
				BundleRepository string `json:"bundleRepository"`
				PodRepository    string `json:"podRepository"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(logged.String())), &line); err != nil {
				t.Fatalf("the daemon logged %q, want one JSON line: %v", logged.String(), err)
			}
			if line.Msg != "supervise: the saved workspace bundle of this issue is refused for its repository, so no pod restores it; the issue is told and the work is kept in workspace_bundles_refused" ||
				line.BundleRepository != tc.bundle || line.PodRepository != tc.pod {
				t.Errorf("the daemon logged %+v, want the one refusal line naming the bundle's %q and the pod's %q", line, tc.bundle, tc.pod)
			}
		})
	}
}

// Every other outcome of the take passes through untouched and writes nothing on the issue: a
// bundle sent to the pod that replaces the released one, a pod the store refuses as no newer
// than the save, and an issue with no bundle at all. Only the repository refusal is a thing no
// pod can ever undo, and only it is written where a person reads it.
func TestRepositoryCorrectionWritesNothingOnTheOtherBundleOutcomes(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Project: "LEGION", Tree: "LEGION-208", Title: "root", Phase: phase.Implementing, Generation: 1,
		Status: "in_progress", Rank: "U", LastDispatchSeq: 1}
	putOutboxIssue(t, pool, records, issue)
	st := outboxTreeStore(t, pool, issue.Key)

	at := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	saved := runtime.WorkspaceBundle{Bundle: []byte("PACK the released work"),
		Head: "f2f134541b30dfa5160a5d1ce5e17b76c967c661", At: at, PodUID: releasedPodUID, Repo: issue.Repo}

	var told []string
	take := tellBundleRepositoryRefused(st, "legion", unrenamed,
		func(_ context.Context, _, body string) error {
			told = append(told, body)
			return nil
		}, quietLogger())

	if _, outcome, err := take(ctx, issue.Key, "pod-before-any-release", issue.Repo, at); err != nil || outcome != runtime.WorkspaceBundleNone {
		t.Fatalf("an issue that was never released answered %v (%v), want nothing", outcome, err)
	}
	if err := st.PutWorkspaceBundle(ctx, issue.Key, 11, saved); err != nil {
		t.Fatalf("save the released workspace's bundle: %v", err)
	}
	if _, outcome, err := take(ctx, issue.Key, "pod-replacement", issue.Repo, at.Add(time.Minute)); err != nil || outcome != runtime.WorkspaceBundleSend {
		t.Fatalf("the pod that replaces the released one answered %v (%v), want the saved work sent", outcome, err)
	}
	// A second release's bundle, and a pod stamped before its save: the older refusal.
	older := saved
	older.At, older.PodUID = at.Add(time.Hour), "pod-of-the-second-release"
	if err := st.PutWorkspaceBundle(ctx, issue.Key, 12, older); err != nil {
		t.Fatalf("save the second release's bundle: %v", err)
	}
	if _, outcome, err := take(ctx, issue.Key, "pod-skewed-replacement", issue.Repo, at); err != nil || outcome != runtime.WorkspaceBundleRefusedOlder {
		t.Fatalf("a pod no newer than the save answered %v (%v), want it refused as older", outcome, err)
	}
	if len(told) != 0 {
		t.Fatalf("the daemon wrote on the issue for an outcome a pod can still act on:\n%s", strings.Join(told, "\n---\n"))
	}
}

// A bookmark name is whatever the released pod's jj answered, never the daemon's own text, so
// the message renders each one inside a code span the name cannot break (codeSpan). An ordinary
// name reads as one backtick either side. A name holding a backtick keeps that backtick and
// closes its span all the same, so the sentences after the list are out of code as they were,
// and a name holding a line break stays one item of the list rather than putting the rest of
// itself at the start of a line, where it would read as a sentence the daemon never wrote.
func TestTheBundleMessageHoldsAnOddBookmarkNameWhole(t *testing.T) {
	const commit = "dffe88ae2f793e13436cef3921adc988433fbd4e"
	message := func(name string) string {
		return bundleRepositoryMessage("LEGION-208", "zeta/tools", runtime.WorkspaceBundle{
			Repo: "acme/widgets", Head: "f2f134541b30dfa5160a5d1ce5e17b76c967c661",
			Bookmarks: map[string]string{name: commit},
			At:        time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC),
		})
	}
	plain := message("legion/LEGION-208")
	if !strings.Contains(plain, "- `legion/LEGION-208` at commit `"+commit+"`") {
		t.Fatalf("an ordinary bookmark name is not one code span:\n%s", plain)
	}
	odd := message("legion/`echo hi`\nThe daemon says this.")
	t.Logf("a bookmark name holding a backtick and a line break reads:\n%s", odd)
	if !strings.Contains(odd, "- ``legion/`echo hi` The daemon says this.`` at commit `"+commit+"`") {
		t.Fatalf("the odd bookmark name is not held whole in one item:\n%s", odd)
	}
	if strings.Count(odd, "\n") != strings.Count(plain, "\n") {
		t.Fatalf("the odd bookmark name added %d line breaks to the message:\n%s",
			strings.Count(odd, "\n")-strings.Count(plain, "\n"), odd)
	}
	if strings.Count(odd, "`")%2 != 0 {
		t.Fatalf("the message holds an odd number of backticks, so a code span is left open:\n%s", odd)
	}
}
