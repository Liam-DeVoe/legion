package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// A daemon whose runtime runs no pod — the tmux runtime, whose runtime implements none of
// runtime.ProjectPods — refuses the release, a dry run included, before anything of it acts: the
// mark is not set, so the daemon goes on starting what it starts.
func TestProjectReleaseRefusesARuntimeThatRunsNoPod(t *testing.T) {
	mark := &projectRelease{}
	r := &projectReleaser{ctx: context.Background(), project: "legion", mark: mark, defaultBound: time.Minute, now: time.Now, log: quietLogger()}
	for _, dryRun := range []bool{false, true} {
		if _, started, err := r.Start(context.Background(), 0, dryRun); !errors.Is(err, api.ErrProjectReleaseRunsNoPod) || started {
			t.Fatalf("Start(dryRun %v) on a runtime with no pods = started %v, %v; want the tmux refusal", dryRun, started, err)
		}
	}
	if mark.held() {
		t.Fatal("the refused release set the mark")
	}
	if round := r.AskRound(); round != "" {
		t.Fatalf("the refused release asks under %q, want no round", round)
	}
}

// releaseRecord is a project release's store with the release's own record in memory: the start
// records release 1 with the stop set it is given, the finish hands its result to finished, and
// the claims are the ones the machines store (claims).
type releaseRecord struct {
	stop     []store.ProjectReleaseClaim
	claims   *outboxClaimStore
	finished chan json.RawMessage
}

func (s *releaseRecord) StartProjectRelease(_ context.Context, project string, startedAt, boundAt time.Time) (store.ProjectRelease, []store.TakenOverIdleRelease, []store.ProjectReleaseClaim, error) {
	return store.ProjectRelease{ID: 1, Project: project, StartedAt: startedAt, BoundAt: boundAt}, nil, s.stop, nil
}

func (*releaseRecord) RecordProjectRelease(context.Context, int64, json.RawMessage) error { return nil }

func (s *releaseRecord) FinishProjectRelease(_ context.Context, _ int64, result json.RawMessage, _ time.Time) error {
	s.finished <- result
	return nil
}

func (*releaseRecord) ReadProjectRelease(context.Context, string) (store.ProjectRelease, bool, error) {
	return store.ProjectRelease{}, false, nil
}

func (*releaseRecord) ProjectReleaseStopSet(context.Context, string) ([]store.ProjectReleaseClaim, error) {
	return nil, nil
}

func (s *releaseRecord) ClaimByToken(_ context.Context, token claim.Token) (supervise.Claim, bool, error) {
	s.claims.mu.Lock()
	defer s.claims.mu.Unlock()
	c, ok := s.claims.claims[token]
	return c, ok, nil
}

func (s *releaseRecord) Claims(context.Context) ([]supervise.Claim, error) {
	s.claims.mu.Lock()
	defer s.claims.mu.Unlock()
	claims := make([]supervise.Claim, 0, len(s.claims.claims))
	for _, c := range s.claims.claims {
		claims = append(claims, c)
	}
	return claims, nil
}

// releasePods is a runtime's part of a project release over the Sandboxes it lists: each issue
// pod's delete asks its authorize and records the issue it deleted. The API server lists the same
// Sandboxes as its store.
type releasePods struct {
	sandboxes []runtime.ProjectSandbox
	mu        sync.Mutex
	deleted   []string
}

func (p *releasePods) SuspendIssue(ctx context.Context, issue, _ string, authorize func(context.Context) (bool, error)) error {
	if ok, err := authorize(ctx); err != nil || !ok {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deleted = append(p.deleted, issue)
	return nil
}

func (p *releasePods) ProjectSandboxes(context.Context) ([]runtime.ProjectSandbox, error) {
	return p.sandboxes, nil
}

func (p *releasePods) ProjectSandboxesFromAPI(context.Context) ([]runtime.ProjectSandbox, error) {
	return p.sandboxes, nil
}

func (*releasePods) ReadUnpushed(context.Context, string, claim.Role) (runtime.UnpushedWork, error) {
	return runtime.UnpushedWork{}, errors.New("the test's pods read no workspace")
}

func (*releasePods) DeleteProjectSandbox(context.Context, string) error { return nil }

func (*releasePods) ReleaseProjectController(context.Context, claim.Token) error { return nil }

// A claim of the stop set that failed or retired before the release came to its pod holds no
// process, so the release does not wait for it to be stored suspended: the pod is deleted rather
// than left once the wait for its stop runs out, and the claim's line says how it ended, which no
// boot starts.
func TestProjectReleaseDeletesThePodOfAStoppedClaimThatFailedOrRetired(t *testing.T) {
	for _, ended := range []supervise.ClaimState{supervise.StateFailed, supervise.StateRetired} {
		t.Run(string(ended), func(t *testing.T) {
			sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
			token := mustClaimToken(t, "LEGION-208", claim.RoleImplementer)
			if _, _, err := sup.Create(context.Background(), supervise.Claim{Token: token, Project: "legion", Tree: "LEGION-208",
				Issue: "LEGION-208", Role: claim.RoleImplementer, Session: "ses_implementer", State: ended}, ""); err != nil {
				t.Fatal(err)
			}
			records := &releaseRecord{claims: sup.deps.Store.(*outboxClaimStore), finished: make(chan json.RawMessage, 1),
				// The stop set as the release's own transaction read it, while the claim still ran.
				stop: []store.ProjectReleaseClaim{{Token: token, Tree: "LEGION-208", Issue: "LEGION-208", Role: claim.RoleImplementer,
					Session: "ses_implementer", State: supervise.StateWorking}}}
			pods := &releasePods{sandboxes: []runtime.ProjectSandbox{{Name: "legion-legion-legion-208", Kind: runtime.ProjectSandboxIssue,
				Issue: "LEGION-208", Tree: "LEGION-208", PodUID: "uid-208", ConfiguredImage: true}}}
			// A claim that never settles costs the bound and then the stop timeout before its pod is
			// left (stopDeadline), so both are short.
			r := &projectReleaser{ctx: context.Background(), project: "legion", mark: &projectRelease{}, store: records, machines: sup,
				pods: pods, defaultBound: time.Minute, stopTimeout: time.Second, reread: time.Second, poll: 10 * time.Millisecond,
				now: time.Now, log: quietLogger()}
			if _, started, err := r.Start(context.Background(), time.Second, false); err != nil || !started {
				t.Fatalf("Start = started %v, %v; want the release started", started, err)
			}
			var result api.ProjectReleaseResult
			select {
			case raw := <-records.finished:
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the release did not finish")
			}
			if len(result.Sandboxes) != 1 || result.Sandboxes[0].Outcome != api.ReleaseDeletedUnread {
				t.Fatalf("the pod's line is %+v, want it deleted with no read", result.Sandboxes)
			}
			pods.mu.Lock()
			deleted := slices.Clone(pods.deleted)
			pods.mu.Unlock()
			if !slices.Equal(deleted, []string{"LEGION-208"}) {
				t.Fatalf("the release deleted the pods of %v, want LEGION-208's", deleted)
			}
			if len(result.Claims) != 1 || result.Claims[0].Ended != string(ended) || result.Claims[0].BootStarts || result.Claims[0].Detail != "" {
				t.Fatalf("the claim's line is %+v, want it ended %s, which the next boot does not start", result.Claims, ended)
			}
		})
	}
}

// The release's asks end with it: once the release finished, an ask of its round that the outbox
// runs meets the claim-round read as any other update ask does, and since no claim records that
// round it finishes without a publish, while the mark still holds every start until the process
// ends. The route that takes an answer reads the same round as the outbox (AskRound), and neither
// reads the release's round once it finished.
func TestProjectReleaseAsksNothingOnceItFinished(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	putOutboxIssue(t, pool, records, record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
		Project: "LEGION", Tree: "LEGION-208", Title: "Root", Phase: phase.Implementing, Generation: 1, Status: "in_progress"})
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	if _, _, err := sup.Create(ctx, supervise.Claim{Token: mustClaimToken(t, "LEGION-208", claim.RoleImplementer), Project: "legion",
		Tree: "LEGION-208", Issue: "LEGION-208", Role: claim.RoleImplementer, State: supervise.StateQueued}, ""); err != nil {
		t.Fatal(err)
	}
	mark := &projectRelease{}
	release := &releaseRecord{claims: sup.deps.Store.(*outboxClaimStore), finished: make(chan json.RawMessage, 1)}
	r := &projectReleaser{ctx: ctx, project: "legion", mark: mark, store: release, machines: sup, pods: &releasePods{},
		defaultBound: time.Minute, stopTimeout: time.Second, reread: time.Second, poll: 10 * time.Millisecond, now: time.Now, log: quietLogger()}
	if _, started, err := r.Start(ctx, time.Minute, false); err != nil || !started {
		t.Fatalf("Start = started %v, %v; want the release started", started, err)
	}
	select {
	case <-release.finished:
	case <-time.After(30 * time.Second):
		t.Fatal("the release did not finish")
	}
	if !mark.held() {
		t.Fatal("the release's finish cleared the mark, which holds every start until the process ends")
	}
	if round := r.AskRound(); round != "" {
		t.Fatalf("the finished release takes answers under %q, want no round", round)
	}

	publisher := &outboxPublisher{}
	logs := &syncBuffer{}
	runner := &outbox{pool: pool, dispatchProject: "LEGION", records: records, notices: publisher, supervisor: sup, project: "legion",
		log: jsonLogger(logs), now: time.Now, release: mark}
	enqueueOutbox(t, pool, records, mustOutboxRow(t, "LEGION-208", record.UpdateAsk{Role: claim.RoleImplementer, Round: projectReleaseRound(1),
		Pod: "uid-208", Text: "Push your work and answer the release."}, time.Now()))
	if ran := runDue(t, runner, time.Now().UTC()); ran != 1 {
		t.Fatalf("rows run after the release finished = %d, want the ask", ran)
	}
	if published := publisher.publishes(); len(published) != 0 {
		t.Fatalf("the ask of the finished release was published as %#v, want no publish", published)
	}
	if _, lastError, present := outboxRowState(t, pool, "LEGION-208"); present {
		t.Fatalf("the ask of the finished release still waits in the outbox (%q), want it finished", lastError)
	}
	if lines := loggedLines(t, logs.String(), `"msg":"`+askLeftLine+`"`); len(lines) != 1 || lines[0]["round"] != projectReleaseRound(1) {
		t.Fatalf("lines %q = %v in\n%s\nwant one naming the release's round", askLeftLine, lines, logs.String())
	}
}
