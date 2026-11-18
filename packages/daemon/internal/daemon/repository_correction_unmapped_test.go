package daemon

// A recorded issue Dispatch moves onto a repository no mapping names. Since INFRA-2543 any
// repository both role Apps are installed on records an issue, read from GitHub at each
// preparation (prepareUnmapped), and a recorded issue's record follows Dispatch onto such a
// repository by its own reads at that preparation (prepareRecorded), always, whatever #3730's
// recorded set keeps: both Apps installed, the correction applies at the name and default branch
// GitHub answered then (intake.DispatchIssue.RepositoryRead) through the one writer
// (workflow.Engine.ApplyRepositoryChange, record.Store.CorrectIssueRepository); a role App not
// installed is a refusal the preparation carries on the fact (intake.DispatchIssue.RepositoryRefusal),
// and the change waits on the record with it; a read GitHub did not answer, or a fact no
// preparation read GitHub for, applies nothing and says nothing. A waiting change onto such a
// repository is applied at the issue's next event or at the next boot: the served-set follow never
// fires for such a repository. These tests run the daemon's own preparation and handlers over a
// real PostgreSQL and NATS, with a GitHub that names unmappedRepo as unmappedNamed and nothing
// mapping it to any project.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// recordOnWidgets records key as the running root of a live tree on acme/widgets at main, at
// Dispatch sequence 1, holding slot, with nothing waiting and no correction: the issue each test
// here moves. Its tree runs (record.TreeLive), so no pass over it takes it out of the workflow,
// and nothing but the correction can move what the tests read.
func recordOnWidgets(t *testing.T, w *workflowRuntime, key string, slot int) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		if err := w.records.PutIssue(ctx, tx, record.Issue{Key: key, Tree: key, Project: "LEGION", Title: key, Phase: phase.Implementing,
			Generation: correctionGeneration, Status: "in_progress", DispatchStatus: "in_progress", Rank: "A", HandedOver: true,
			LastDispatchSeq: 1, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets"}); err != nil {
			return err
		}
		if err := w.records.PutSlot(ctx, tx, record.Slot{Issue: key, Index: slot, AdmittedAt: correctionMoment}); err != nil {
			return err
		}
		return openTreeTx(ctx, tx, "LEGION", key)
	}); err != nil {
		t.Fatalf("record %s: %v", key, err)
	}
}

// onUnmapped fails unless key's record is on unmappedNamed at branch, GitHub's own spelling and
// the default branch GitHub answered, having left acme/widgets, with nothing waiting and its
// generation where it was.
func onUnmapped(t *testing.T, w *workflowRuntime, key, branch string) {
	t.Helper()
	got := recordedIssue(t, w, key)
	if got == nil || got.Repo != unmappedNamed || got.GitHubRepo != unmappedNamed || got.DefaultBranch != branch {
		t.Fatalf("%s = %+v, want it on %s at %s, GitHub's own spelling and default branch", key, got, unmappedNamed, branch)
	}
	if got.RepoCorrection == nil || got.RepoCorrection.Before != "acme/widgets" {
		t.Fatalf("the correction of %s = %+v, want acme/widgets left", key, got.RepoCorrection)
	}
	if got.RepoChange != nil {
		t.Fatalf("%s still waits on %+v, want nothing waiting", key, got.RepoChange)
	}
	if got.Generation != correctionGeneration {
		t.Fatalf("%s is at generation %d, want %d: a correction takes no new generation", key, got.Generation, correctionGeneration)
	}
}

// onWidgets fails unless key's record is still on acme/widgets at main, uncorrected, and answers
// it.
func onWidgets(t *testing.T, w *workflowRuntime, key string) record.Issue {
	t.Helper()
	got := recordedIssue(t, w, key)
	if got == nil || got.Repo != "acme/widgets" || got.DefaultBranch != "main" || got.RepoCorrection != nil {
		t.Fatalf("%s = %+v, want it still on acme/widgets at main, uncorrected", key, got)
	}
	return *got
}

// waitingNote is what an issue whose change onto unmappedRepo waits for role's App is told.
func waitingNote(role string) string {
	return "Legion did not move this issue off acme/widgets: the " + role + " App is not installed on " + unmappedRepo + ". Install it on " + unmappedRepo +
		", or set another repository (the issue header's Repository picker, or dispatch issue-update --repo owner/name). " +
		"Legion moves the issue at its next event or at Legion's next boot, once both Legion Apps are installed on " + unmappedRepo + "."
}

// changingGitHub is a GitHub that names the rig's two mapped repositories at main and unmappedRepo
// as unmappedNamed at the default branch it holds now, which a test changes (rename), counting the
// reads of unmappedRepo.
type changingGitHub struct {
	branch atomic.Pointer[string]
	reads  atomic.Int64
}

func newChangingGitHub(t *testing.T, branch string) (*changingGitHub, string) {
	t.Helper()
	g := &changingGitHub{}
	g.rename(branch)
	url := githubtest.Server(t, githubtest.Spec{Handle: func(w http.ResponseWriter, r *http.Request) {
		var name, head string
		switch strings.ToLower(strings.TrimPrefix(r.URL.Path, "/repos/")) {
		case "acme/widgets":
			name, head = "acme/widgets", "main"
		case "zeta/tools":
			name, head = "zeta/tools", "main"
		case unmappedRepo:
			g.reads.Add(1)
			name, head = unmappedNamed, *g.branch.Load()
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"full_name":"` + name + `","default_branch":"` + head + `"}`))
	}})
	return g, url
}

// rename has GitHub answer unmappedRepo's default branch as branch from now on.
func (g *changingGitHub) rename(branch string) { g.branch.Store(&branch) }

// changingRig is unmappedRig over a changingGitHub whose unmappedRepo starts at trunk.
func changingRig(t *testing.T, installs *installations, opts ...func(*workflowRuntime)) (*workflowRuntime, *mappingDispatch, *changingGitHub) {
	t.Helper()
	github, url := newChangingGitHub(t, "trunk")
	w, client, _, _ := unmappedRig(t, installs, append([]func(*workflowRuntime){func(w *workflowRuntime) {
		w.defaultBranches = newDefaultBranches(roleTokens{}, w.repositories, url, defaultBranchTTL, time.Now)
	}}, opts...)...)
	return w, client, github
}

// keepAnEntry admits LEGION-1100, a root handed to Legion on unmappedRepo, which has the daemon
// keep #3730's read of the repository (its recorded set, and defaultBranches' own read) at trunk,
// and fails unless both hold it: the kept read the corrections below must not write.
func keepAnEntry(t *testing.T, w *workflowRuntime, client *mappingDispatch) {
	t.Helper()
	deliver(t, w, client, issue("LEGION-1100", unmappedRepo, "todo", true, "", 1))
	if got := recordedIssue(t, w, "LEGION-1100"); got == nil || got.Repo != unmappedNamed || got.DefaultBranch != "trunk" {
		t.Fatalf("LEGION-1100 = %+v, want it admitted on %s at trunk, which keeps the read", got, unmappedNamed)
	}
	if held := w.repositories.lookup(unmappedRepo); held.source != fromRecorded || held.read.DefaultBranch != "trunk" {
		t.Fatalf("lookup(%s) = %+v, want the recorded set to keep it at trunk", unmappedRepo, held)
	}
	if kept, read := w.defaultBranches.Kept(unmappedRepo); !read || kept.DefaultBranch != "trunk" {
		t.Fatalf("defaultBranches keeps %+v (%v) for %s, want trunk", kept, read, unmappedRepo)
	}
}

// A live event that moves a recorded issue onto a repository no mapping names, both role Apps
// installed on it, corrects the record onto it there and then, at GitHub's own spelling and
// default branch, with nothing said on the issue and no read of the mapping asked for: the reads
// are the preparation's own, made as an admission onto the repository makes them. GitHub intake
// hears the repository from the correction's commit, as it does from a recording's. Every
// correction reads GitHub itself, so a second issue moved onto the same repository reads it again
// rather than taking the read the first kept.
func TestRepositoryCorrectionFollowsAnEventOntoARepositoryNoMappingNamesBothAppsAreInstalledOn(t *testing.T) {
	w, client, installs, stream := unmappedRig(t, &installations{})
	const first, second = "LEGION-1001", "LEGION-1002"
	recordOnWidgets(t, w, first, 0)
	recordOnWidgets(t, w, second, 1)
	heard := "notifications.github." + strings.ReplaceAll(unmappedNamed, "/", ".") + ".>"
	requested := w.mappingRequested.Load()

	deliver(t, w, client, issue(first, unmappedRepo, "in_progress", true, "", 2))
	onUnmapped(t, w, first, "trunk")
	if notes := repositoryNotes(t, w, first); len(notes) != 0 {
		t.Fatalf("notes on %s = %q, want none: its record followed Dispatch onto a repository both Apps are installed on", first, notes)
	}
	if filters := githubFilterSubjects(t, stream); !slices.Contains(filters, heard) {
		t.Fatalf("GitHub filters = %v, want %s heard from the correction that names it", filters, heard)
	}

	deliver(t, w, client, issue(second, unmappedRepo, "in_progress", true, "", 2))
	onUnmapped(t, w, second, "trunk")
	for _, role := range []string{"implement " + unmappedRepo, "review " + unmappedRepo} {
		if reads := installs.reads(role); reads != 2 {
			t.Fatalf("the two corrections made %d reads of %q, want one each: a correction writes no kept read", reads, role)
		}
	}
	if asked := w.mappingRequested.Load(); asked != requested {
		t.Fatalf("the corrections asked for %d reads of the mapping, want none: the mapping has nothing to say about %s", asked-requested, unmappedRepo)
	}
}

// A recorded issue Dispatch moves onto a repository no mapping names that a role App is not
// installed on keeps its record's repository, the change waiting on the record with GitHub's
// refusal, the one the preparation carried on the fact, and the issue told once however many
// events read the same resolution, and told that the change is applied at its next event or at
// the next boot. Nothing of the refusal is kept, so the event after the App is installed reads
// GitHub again and applies the change, with no mapping change and no restart.
func TestRepositoryCorrectionOntoARepositoryNoMappingNamesWaitsWhileARoleAppIsNotInstalled(t *testing.T) {
	installs := &installations{}
	installs.setMissing(true, "review "+unmappedRepo)
	w, client, installs, _ := unmappedRig(t, installs)
	const key = "LEGION-1011"
	recordOnWidgets(t, w, key, 0)

	deliver(t, w, client, issue(key, unmappedRepo, "in_progress", true, "", 2))
	deliver(t, w, client, issue(key, unmappedRepo, "in_progress", true, "", 3))
	got := onWidgets(t, w, key)
	want := record.RepositoryChange{Pending: unmappedRepo, Reason: "the review App is not installed on " + unmappedRepo}
	if got.RepoChange == nil || *got.RepoChange != want {
		t.Fatalf("the waiting change of %s = %+v, want %+v", key, got.RepoChange, want)
	}
	if notes := repositoryNotes(t, w, key); len(notes) != 1 || notes[0] != waitingNote("review") {
		t.Fatalf("notes on %s = %q, want it told once, saying %q", key, notes, waitingNote("review"))
	}
	if reads := installs.reads("review " + unmappedRepo); reads != 2 {
		t.Fatalf("the two events made %d reads of the review App's installation, want one each: no refusal is kept", reads)
	}

	installs.setMissing(false, "review "+unmappedRepo)
	deliver(t, w, client, issue(key, unmappedRepo, "in_progress", true, "", 4))
	onUnmapped(t, w, key, "trunk")
}

// A correction onto a repository no mapping names writes GitHub's answer at the correction and
// never the read #3730 kept of the repository at an earlier preparation (the recorded set, and the
// default-branch read defaultBranches holds within its TTL): an admission on unmappedRepo keeps
// that read at trunk first, then GitHub's answer changes, and the issue is corrected. A default
// branch renamed since is the one the record takes; an App uninstalled since leaves the change
// waiting with GitHub's reason and the record where it was, as for an App never installed; a read
// GitHub does not answer writes nothing; and a hand-over, which no preparation read GitHub for
// (admit's consumeHandover hands the engine only Dispatch's resolution), writes nothing either.
// Each row fails on the code as merged, which took the kept read.
func TestRepositoryCorrectionOntoARepositoryNoMappingNamesWritesGitHubsAnswerAtTheCorrection(t *testing.T) {
	const key = "LEGION-1041"
	for _, tc := range []struct {
		name string
		// change is what changes on GitHub after the read was kept.
		change func(github *changingGitHub, installs *installations)
		// correct moves key onto unmappedRepo as the row's pass does.
		correct func(t *testing.T, w *workflowRuntime, client *mappingDispatch)
		// branch is the default branch the record is corrected at, "" for a record left on
		// acme/widgets; waits is the change left waiting there, nil for none.
		branch string
		waits  *record.RepositoryChange
		note   string
	}{
		{name: "a default branch renamed since the read was kept",
			change:  func(github *changingGitHub, _ *installations) { github.rename("release/v2") },
			correct: deliverCorrection,
			branch:  "release/v2"},
		{name: "an App uninstalled since the read was kept",
			change:  func(_ *changingGitHub, installs *installations) { installs.setMissing(true, "review "+unmappedRepo) },
			correct: deliverCorrection,
			waits:   &record.RepositoryChange{Pending: unmappedRepo, Reason: "the review App is not installed on " + unmappedRepo},
			note:    waitingNote("review")},
		{name: "a read GitHub does not answer",
			change: func(_ *changingGitHub, installs *installations) {
				installs.setFailing(errors.New("GitHub answered 502"), "implement "+unmappedRepo)
			},
			correct: deliverCorrection},
		{name: "a hand-over, which no preparation read GitHub for",
			change:  func(github *changingGitHub, _ *installations) { github.rename("release/v2") },
			correct: handOverCorrection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quickInstallReads(t)
			installs := &installations{}
			w, client, github := changingRig(t, installs)
			recordOnWidgets(t, w, key, 0)
			keepAnEntry(t, w, client)
			tc.change(github, installs)

			tc.correct(t, w, client)
			if tc.branch != "" {
				onUnmapped(t, w, key, tc.branch)
				return
			}
			got := onWidgets(t, w, key)
			if (got.RepoChange == nil) != (tc.waits == nil) || got.RepoChange != nil && *got.RepoChange != *tc.waits {
				t.Fatalf("the waiting change of %s = %+v, want %+v", key, got.RepoChange, tc.waits)
			}
			notes := repositoryNotes(t, w, key)
			if tc.note == "" && len(notes) != 0 || tc.note != "" && (len(notes) != 1 || notes[0] != tc.note) {
				t.Fatalf("notes on %s = %q, want %q", key, notes, tc.note)
			}
		})
	}
}

// deliverCorrection delivers the live event that moves LEGION-1041 onto unmappedRepo, prepared as
// the Dispatch consumer prepares it.
func deliverCorrection(t *testing.T, w *workflowRuntime, client *mappingDispatch) {
	t.Helper()
	deliver(t, w, client, issue("LEGION-1041", unmappedRepo, "in_progress", true, "", 2))
}

// handOverCorrection applies the correction admission's outbox hand-over applies
// (admit's consumeHandover): the engine's own pass over a fact that carries Dispatch's resolution
// and nothing a preparation read.
func handOverCorrection(t *testing.T, w *workflowRuntime, _ *mappingDispatch) {
	t.Helper()
	ctx := context.Background()
	scoped, committed := intake.WithCommitHooks(ctx)
	if err := pgx.BeginFunc(scoped, w.pool, func(tx pgx.Tx) error {
		stored, err := w.records.Issue(scoped, tx, "LEGION-1041")
		if err != nil {
			return err
		}
		return w.engine.ApplyRepositoryChange(scoped, tx, *stored, intake.DispatchIssue{Key: "LEGION-1041", ResolvedRepo: unmappedRepo})
	}); err != nil {
		t.Fatalf("apply the hand-over's correction: %v", err)
	}
	committed()
}

// The boot's listing reads GitHub for no repository no mapping names, a recorded issue's change
// onto one included: it runs inside the boot's bounded reconcile, so the change applies nothing
// there, whatever read the daemon keeps of the repository, and the boot asks for the follow
// instead (requestFollow), which the refresher makes once the boot is over and whose own
// preparation of the issue reads GitHub and corrects the record at GitHub's answer then, with no
// event of the issue and no mapping change. The read kept here is at trunk while GitHub now
// answers release/v2: a boot that wrote the kept read would leave the record at trunk, which the
// follow would then find on the repository Dispatch names and leave.
func TestTheBootsListingLeavesACorrectionOntoARepositoryNoMappingNamesToTheFollowAfterIt(t *testing.T) {
	ctx := context.Background()
	installs := &bootCountedInstallations{installations: &installations{}}
	w, client, github := changingRig(t, installs.installations, func(w *workflowRuntime) { w.installs = installs })
	w.bootID = "unmapped-correction-boot"
	const key = "LEGION-1031"
	recordOnWidgets(t, w, key, 0)
	keepAnEntry(t, w, client)
	github.rename("release/v2")
	client.show(issue(key, unmappedRepo, "in_progress", true, "", 1))
	follows := w.followRequested.Load()
	before := installs.reads("implement " + unmappedRepo)

	if err := w.reconcile(context.WithValue(ctx, bootReconcile{}, true)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if reads := installs.atBoot.Load(); reads != 0 {
		t.Fatalf("the boot's listing made %d installation reads, want none: it runs inside the boot's bounded reconcile", reads)
	}
	if asked := w.followRequested.Load(); asked == follows {
		t.Fatal("the boot's listing asked for no follow, want one for the change it left unread")
	}
	testwait.Eventually(t, key+" corrected onto "+unmappedNamed+" by the follow after the boot", func() bool {
		got := recordedIssue(t, w, key)
		return got != nil && got.Repo == unmappedNamed
	})
	onUnmapped(t, w, key, "release/v2")
	if notes := repositoryNotes(t, w, key); len(notes) != 0 {
		t.Fatalf("notes on %s = %q, want none", key, notes)
	}
	if reads := installs.reads("implement "+unmappedRepo) - before; reads != 1 {
		t.Fatalf("%s's implement App was read %d times after the boot began, want once, by the follow", unmappedRepo, reads)
	}
}
