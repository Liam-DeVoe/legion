package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// A reviewer's own clone (workspace/reviewgit.go) is made from its issue's shared clone at the
// deterministic place workspace.ReviewLocation names, at the head of the issue's pull request the
// daemon records, under the issue's turn, which every provision and removal of the issue's
// workspace takes, and handed the repository's store lock free, for the clone to take around its
// steps on the shared clone, the checkout places, and a way to GitHub at the issue's recorded
// repository with the repository owner's read lease, for a head the shared clone lacks
// (workspace.OriginWithToken). A clone that cannot be made fails the call, naming the issue; under
// a runtime that provisions its own (a Sandbox pod), the host makes none.
func TestAReviewCloneIsMadeUnderItsIssuesTurnWithTheStoreLockFree(t *testing.T) {
	repo := ghrepo.MustParse("acme/widgets")
	stateDir := t.TempDir()
	located, err := workspace.Location(stateDir, repo, "S1-2")
	if err != nil {
		t.Fatal(err)
	}
	rt := fake.NewRuntime()
	var made []workspace.ReviewRequest
	var refuse error
	var r *outbox
	r = &outbox{
		stateDir: stateDir, tokens: outboxTokens{}, log: quietLogger(),
		supervisor: &supervisor{deps: supervise.Deps{Runtime: rt, RefreshGate: supervise.NewRefreshGate(0)}},
		provisionReview: func(_ context.Context, request workspace.ReviewRequest) error {
			made = append(made, request)
			turn, release := r.provisions.issues.take(located.Dir)
			defer release()
			if turn.TryLock() {
				turn.Unlock()
				t.Error("the review clone was made outside its issue's turn")
			}
			shared, releaseStore := r.provisions.stores.take(located.Clone)
			defer releaseStore()
			if request.StoreLock != sync.Locker(shared) {
				t.Errorf("the review clone was handed store lock %p, want its repository's %p", request.StoreLock, shared)
			} else if !shared.TryLock() {
				t.Error("the review clone was handed its repository's store lock held, want it free for the clone to take")
			} else {
				shared.Unlock()
			}
			if request.CheckoutSlot == nil {
				t.Error("the review clone was handed no checkout places")
			}
			return refuse
		},
	}
	const head = "0123456789abcdef0123456789abcdef01234567"
	if err := r.ensureReviewClone(context.Background(), "S1-2", repo, "release/v7.1", head); err != nil {
		t.Fatalf("ensureReviewClone: %v", err)
	}
	review, err := workspace.ReviewLocation(stateDir, repo, "S1-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 1 {
		t.Fatalf("made %d review clones, want one", len(made))
	}
	if got := made[0]; got.Clone != located.Clone || got.Dir != review || got.Repo != repo || got.Issue != "S1-2" || got.DefaultBranch != "release/v7.1" || got.Head != head {
		t.Errorf("the review clone was asked of %+v, want the shared clone %s into %s for S1-2 of %s at %s, release/v7.1 its default", got, located.Clone, review, repo, head)
	}
	if made[0].Origin.IsZero() || made[0].Origin.URL != workspace.GitHubURL(repo) {
		t.Errorf("the review clone was handed the way to origin %q (zero: %v), want %s, the issue's recorded repository", made[0].Origin.URL, made[0].Origin.IsZero(), workspace.GitHubURL(repo))
	}

	refuse = errors.New("the shared clone records no branch")
	if err := r.ensureReviewClone(context.Background(), "S1-2", repo, "main", head); err == nil || !strings.Contains(err.Error(), "the review clone of S1-2") || !strings.Contains(err.Error(), "records no branch") {
		t.Errorf("ensureReviewClone with a clone that failed = %v, want the failure naming the issue", err)
	}

	made, refuse = nil, nil
	rt.InPod = true
	if err := r.ensureReviewClone(context.Background(), "S1-2", repo, "main", head); err != nil {
		t.Fatalf("ensureReviewClone under a runtime that provisions its own: %v", err)
	}
	if len(made) != 0 {
		t.Errorf("a pod's review clone was made on the host %d times, want none", len(made))
	}
}

// The removal of an issue's workspace at its linger's expiry removes the reviewer's own clone of
// the issue with it, so no reviewed issue leaves a whole checkout on the daemon's host; and a
// removal whose issue has no review clone removes nothing else.
func TestAWorkspaceRemovalRemovesTheReviewCloneWithIt(t *testing.T) {
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	until := time.Now().Add(-time.Minute)
	issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION", Tree: "LEGION-208", Title: "Workflow", Phase: phase.Done, Generation: 2, Status: "done", LingerUntil: &until}
	putOutboxIssue(t, pool, records, issue)
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	removals := 0
	runner := &outbox{
		dispatchProject: "LEGION",
		pool:            pool, records: records, supervisor: sup, project: "legion", log: quietLogger(),
		stateDir: t.TempDir(),
		remove:   func(context.Context, workspace.Workspace, sync.Locker) error { removals++; return nil },
	}
	review, err := workspace.ReviewLocation(runner.stateDir, ghrepo.MustParse(issue.Repo), issue.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(review, ".jj"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(review, "notes.txt"), []byte("head\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	row := mustOutboxRow(t, issue.Key, record.WorkspaceRemove{Linger: issue.Generation}, time.Now())
	if err := runner.execute(context.Background(), row); err != nil {
		t.Fatalf("execute workspace removal: %v", err)
	}
	if removals != 1 {
		t.Fatalf("the issue's workspace was removed %d times, want once", removals)
	}
	for _, gone := range []string{review, review + ".removing"} {
		if _, err := os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there after the workspace's removal: %v", gone, err)
		}
	}
	if err := runner.execute(context.Background(), row); err != nil {
		t.Fatalf("a removal with no review clone left: %v", err)
	}
}
