package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/prompts"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// pullRequestHead is the head of an issue's pull request as a test's record holds it: the daemon
// reads it at each reviewer launch (specs.reviewHead) and at each send to a reviewer
// (supervise.Deps.ReviewHead), as workflowRuntime.reviewHead serves both in production.
type pullRequestHead struct {
	mu   sync.Mutex
	head string
}

func (p *pullRequestHead) set(head string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.head = head
}

func (p *pullRequestHead) read(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.head, nil
}

// On tmux a reviewer outlives its round: its next round reaches the process its first launch
// started, whose clone the daemon's host made at that launch's head (specs.reviewerGit). Once the
// pull request's head has moved, the next round is not sent to that process: the reviewer is
// started again, the launch's spec is built again, and that build brings the reviewer's own clone
// to the new head (ensureReviewClone) before the process that reads the round starts. The round
// goes to that process once it is ready, and nothing of it reaches the process at the old head.
func TestALiveTmuxReviewersNextRoundIsReadInACloneAtTheNewHead(t *testing.T) {
	composer, err := prompts.New(t.TempDir())
	if err != nil {
		t.Fatalf("compose the shipped prompts: %v", err)
	}
	const first, second = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	recorded := &pullRequestHead{head: first}
	var mu sync.Mutex
	var clones []string
	cloned := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(clones)
	}
	repo := ghrepo.MustParse("acme/widgets")
	builder := specs{
		stateDir: t.TempDir(), project: "legion", prompts: composer, appOwners: noAppOwners,
		repository: func(context.Context, string) (ghrepo.Repository, string, error) { return repo, "main", nil },
		identity: func(context.Context, claim.Role, ghrepo.Repository) (runtime.GitIdentity, error) {
			return runtime.GitIdentity{Name: "legion-review[bot]", Email: "9+legion-review[bot]@users.noreply.github.com"}, nil
		},
		hostWorkspaces:  true,
		ensureWorkspace: func(context.Context, string, ghrepo.Repository, string) error { return nil },
		reviewHead:      recorded.read,
		ensureReviewClone: func(_ context.Context, _ string, _ ghrepo.Repository, _, at string) error {
			mu.Lock()
			defer mu.Unlock()
			clones = append(clones, at)
			return nil
		},
	}

	ctx := context.Background()
	rt, conns := fake.NewRuntime(), fake.NewConns()
	sup := newSupervisor(ctx, nil, "legion", t.TempDir(), quietLogger())
	sup.deps = supervise.Deps{
		Runtime: rt, Conns: conns, Store: &outboxClaimStore{}, Specs: builder, Clock: stillClock{}, Log: quietLogger(),
		Limits:         supervise.Limits{LaunchFailures: 2, PromptFailures: 2, PromptRetires: 2},
		Timeouts:       supervise.Timeouts{Boot: time.Second, RegistrationIntervals: 2, RPC: time.Second, Probe: time.Second, Stop: time.Second, LaunchBackoff: time.Millisecond},
		PluginContract: api.DaemonAPIVersion,
		RefreshGate:    supervise.NewRefreshGate(0),
		ReviewHead:     recorded.read,
	}
	t.Cleanup(sup.stop)
	token, err := claim.NewToken("legion", "LEGION-209", claim.RoleReviewer)
	if err != nil {
		t.Fatal(err)
	}
	machine, _, err := sup.Create(ctx, supervise.Claim{Token: token, Project: "legion", Tree: "LEGION-208",
		Issue: "LEGION-209", Role: claim.RoleReviewer, State: supervise.StateQueued}, "")
	if err != nil {
		t.Fatalf("create the reviewer's claim: %v", err)
	}
	conn := fake.NewConn()
	conns.Register(token, conn)
	handle := func(events ...supervise.Event) {
		t.Helper()
		for _, ev := range events {
			if err := machine.Handle(ctx, ev); err != nil {
				t.Fatalf("handle %T: %v", ev, err)
			}
		}
		machine.Wait()
	}
	ready := func() {
		t.Helper()
		generation := machine.Claim().Generation
		handle(supervise.RequestRegister{Claim: token, Generation: generation, Session: "ses-reviewer",
			SessionFile: "/sessions/reviewer.jsonl", PluginContract: api.DaemonAPIVersion},
			supervise.RequestReady{Claim: token, Generation: generation, Session: "ses-reviewer"})
	}

	handle(supervise.RequestSpawn{Claim: token})
	ready()
	handle(supervise.RequestDeliver{Claim: token, Task: "review round one", Phase: phase.Reviewing, Generation: 1})
	handle(supervise.StreamTurnStart{Claim: token}, supervise.StreamTurnEnd{Claim: token})
	if got := cloned(); !slices.Equal(got, []string{first}) {
		t.Fatalf("the reviewer's clone was made at %q, want once at the first head", got)
	}
	if prompts := conn.Prompts(); len(prompts) != 1 || !strings.Contains(prompts[0].Message, "review round one") {
		t.Fatalf("the reviewer was sent %+v, want round one", prompts)
	}

	// The implementer pushes the review's fixes; the reviewer's next round is handed to it.
	recorded.set(second)
	handle(supervise.RequestDeliver{Claim: token, Task: "review round two", Phase: phase.Reviewing, Generation: 1})
	if prompts := conn.Prompts(); len(prompts) != 1 {
		t.Fatalf("round two reached the process whose clone stands at the first head: %+v", prompts)
	}
	if got := cloned(); !slices.Equal(got, []string{first, second}) {
		t.Fatalf("the reviewer's clone was brought to %q, want the first head and then the second", got)
	}
	resumes := rt.CallsOf("Resume")
	if len(resumes) != 1 || resumes[0].Spec.ReviewHead != second {
		t.Fatalf("the reviewer was resumed %+v, want once, at the second head", resumes)
	}

	ready()
	prompts := conn.Prompts()
	if len(prompts) != 2 || !strings.Contains(prompts[1].Message, "review round two") {
		t.Fatalf("the reviewer was sent %+v, want round two once its process at the second head is ready", prompts)
	}
}

// headSpecs builds every launch as outboxSpecs does, giving a reviewer the head its record holds
// at that build, as the daemon's own spec builder does (specs.reviewHead).
type headSpecs struct{ recorded *pullRequestHead }

func (s headSpecs) SpawnSpec(ctx context.Context, c supervise.Claim) (runtime.SpawnSpec, error) {
	spec, err := outboxSpecs{}.SpawnSpec(ctx, c)
	if err != nil || c.Role != claim.RoleReviewer {
		return spec, err
	}
	spec.ReviewHead, err = s.recorded.read(ctx, c.Issue)
	return spec, err
}

// A message the daemon holds for a reviewer — a merge forward's review, the ask of its verdict
// past review — reaches the reviewer's session through Envoy, not through a send of the
// reviewer's machine, so it is held to the same rule as a task: while the process the reviewer
// runs was launched at another head than the one its record holds, the row publishes nothing and
// waits, the reviewer is started again at the record's head, and the row publishes once that
// process is ready. A reviewer this daemon re-adopted knows no launch's head, so it is started
// again too.
func TestAMessageHeldForAReviewerWaitsForAProcessAtTheRecordedHead(t *testing.T) {
	publisher := &holderPublisher{}
	runner, sup, _ := runnerIn(t, phase.Retro, "in_progress", publisher, quietLogger())
	recorded := &pullRequestHead{head: "sha-2"}
	sup.deps.ReviewHead = recorded.read
	sup.deps.Specs = headSpecs{recorded: recorded}
	rt := sup.deps.Runtime.(*fake.Runtime)
	conns := sup.deps.Conns.(*fake.Conns)

	ctx := context.Background()
	token := mustClaimToken(t, "LEGION-208", claim.RoleReviewer)
	live := liveClaim(1, token, sup.deps.PluginContract)
	live.Token, live.Project, live.Tree, live.Issue, live.Role, live.State = token, "legion", "LEGION-208", "LEGION-208", claim.RoleReviewer, supervise.StateIdle
	machine, _, err := sup.Create(ctx, live, "")
	if err != nil {
		t.Fatalf("create the re-adopted reviewer's claim: %v", err)
	}
	conns.Register(token, fake.NewConn())

	payload := record.MergeForwardMessage{Role: claim.RoleReviewer, Step: record.MergeForwardReview, Head: "sha-1", Landed: "sha-2",
		Base: "main", Phase: phase.Retro, PullRequest: 86}
	row := mustOutboxRow(t, "LEGION-208", payload, time.Now())
	issue := record.Issue{Key: "LEGION-208", Tree: "LEGION-208", Repo: "acme/widgets", Phase: phase.Retro}
	held := heldMessage{issue: issue, token: token, role: claim.RoleReviewer, what: "the merge-forward review of LEGION-208",
		message: "read what changed since sha-1 and send your verdict at sha-2", attributes: []any{"row", row.ID},
		claimEnded: "ended", delivered: "delivered"}
	attempt := func() error {
		t.Helper()
		err := runner.deliverHeld(ctx, row, held)
		machine.Wait()
		return err
	}
	if err := attempt(); !errors.Is(err, wait.ErrWaiting) {
		t.Fatalf("the first attempt = %v, want a wait for the reviewer at sha-2", err)
	}
	if attempts, _ := publisher.snapshot(); attempts != 0 {
		t.Fatalf("the first attempt published %d times to the reviewer at an unknown head, want none", attempts)
	}
	resumes := rt.CallsOf("Resume")
	if len(resumes) != 1 || resumes[0].Spec.ReviewHead != "sha-2" {
		t.Fatalf("the reviewer was resumed %+v, want once, at sha-2", resumes)
	}
	if err := attempt(); !errors.Is(err, wait.ErrWaiting) {
		t.Fatalf("the attempt while the reviewer launches = %v, want a wait", err)
	}

	generation := machine.Claim().Generation
	for _, ev := range []supervise.Event{
		supervise.RequestRegister{Claim: token, Generation: generation, Session: live.Session, SessionFile: live.SessionFile, PluginContract: sup.deps.PluginContract},
		supervise.RequestReady{Claim: token, Generation: generation, Session: live.Session},
	} {
		if err := machine.Handle(ctx, ev); err != nil {
			t.Fatalf("handle %T: %v", ev, err)
		}
	}
	machine.Wait()
	if err := attempt(); err != nil {
		t.Fatalf("the attempt once the reviewer is ready at sha-2 = %v, want the message published", err)
	}
	if _, delivered := publisher.snapshot(); len(delivered) != 1 || delivered[0].message != held.message {
		t.Fatalf("delivered %+v, want the review once", delivered)
	}
	if resumes := rt.CallsOf("Resume"); len(resumes) != 1 {
		t.Fatalf("the reviewer was resumed %d times, want once", len(resumes))
	}
}
