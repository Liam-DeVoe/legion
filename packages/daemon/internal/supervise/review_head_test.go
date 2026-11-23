package supervise

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// recordedHead is the head of the issue's pull request as a test's record holds it, which the
// machine reads through Deps.ReviewHead: a test moves it, and the machine's goroutines read it.
type recordedHead struct {
	mu   sync.Mutex
	head string
	err  error
	read int
}

func (r *recordedHead) set(head string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.head = head
}

func (r *recordedHead) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *recordedHead) reads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read
}

func (r *recordedHead) answer(context.Context, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.read++
	return r.head, r.err
}

// newReviewerHarness is a machine over a queued reviewer's claim whose record holds head, and
// whose launches give the reviewer head (testSpecs.setReviewHead) until the test moves both.
func newReviewerHarness(t *testing.T, head string) (*harness, *recordedHead) {
	t.Helper()
	h := newBareHarness(t)
	recorded := &recordedHead{head: head}
	h.deps.ReviewHead = recorded.answer
	h.specs.setReviewHead(head)
	c := queuedClaim()
	c.Token, c.Role = "legion-LEGION-209-reviewer", claim.RoleReviewer
	h.token = c.Token
	if err := h.store.PutClaim(h.ctx, c); err != nil {
		t.Fatal(err)
	}
	h.start(c)
	return h, recorded
}

// A reviewer works only in a clone of its own made at the head its launch read (the daemon's
// spawn spec, runtime.SpawnSpec.ReviewHead: the daemon's host makes the clone there on tmux, and
// the pod's role-init does in a pod), and nothing moves that clone while its process runs. So a
// task the reviewer is handed once the head its record holds has moved is not sent to that
// process: the process is replaced first (a relaunch of the same session), and the task goes to
// the process launched at the record's head once it is ready. A task at the head the process was
// launched at is sent at once, with no relaunch.
func TestAReviewersTaskAtAnotherHeadGoesToAProcessLaunchedAtIt(t *testing.T) {
	h, recorded := newReviewerHarness(t, "head-1")
	h.reach(StateIdle)
	h.wantPrompts(1)
	if resumes := h.calls("Resume"); len(resumes) != 0 {
		t.Fatalf("a task at the head the process was launched at relaunched it: %+v", resumes)
	}

	// The implementer pushes, and the reviewer's next round is handed to it.
	recorded.set("head-2")
	h.specs.setReviewHead("head-2")
	h.must(RequestDeliver{Claim: h.token, Task: "review round two"})
	h.m.Wait()
	h.wantPrompts(1)
	resumes := h.wantCalls("Resume", 1)
	if got := resumes[0].Spec.ReviewHead; got != "head-2" {
		t.Fatalf("the relaunch gave the reviewer head %q, want head-2", got)
	}
	h.wantState(StateLaunching)
	if p := h.pending(); p.Task != "review round two" || !p.DeliveredAt.IsZero() {
		t.Fatalf("the pending delivery is %+v, want the round's task, unsent", p)
	}

	h.relaunched()
	h.m.Wait()
	prompts := h.wantPrompts(2)
	if !strings.Contains(prompts[1].Message, "review round two") {
		t.Fatalf("the relaunched reviewer was sent %q, want the round's task", prompts[1].Message)
	}
	h.wantCalls("Resume", 1)
}

// The head a process was launched at lives in the machine's memory alone, so a machine a daemon
// restart rebuilt does not know where its reviewer's clone stands: the reviewer's next task goes
// to a process launched at the record's head, as a task after the head moved does. A record that
// holds no head (an issue with no pull request) holds no task back, and a record that cannot be
// read holds the task back, unsent and with no relaunch, until it answers.
func TestAReviewersTaskWaitsForAProcessWhoseHeadTheMachineKnows(t *testing.T) {
	t.Run("after a restart", func(t *testing.T) {
		h, _ := newReviewerHarness(t, "head-1")
		h.reach(StateIdle)
		h.restart()
		h.must(RequestDeliver{Claim: h.token, Task: "review round two"})
		h.m.Wait()
		h.wantPrompts(1)
		if got := h.wantCalls("Resume", 1)[0].Spec.ReviewHead; got != "head-1" {
			t.Fatalf("the relaunch gave the reviewer head %q, want head-1", got)
		}
		h.relaunched()
		h.m.Wait()
		h.wantPrompts(2)
	})
	t.Run("no head recorded", func(t *testing.T) {
		h, _ := newReviewerHarness(t, "")
		h.reach(StateIdle)
		h.restart()
		h.must(RequestDeliver{Claim: h.token, Task: "review round two"})
		h.m.Wait()
		h.wantPrompts(2)
		h.wantCalls("Resume", 0)
	})
	t.Run("the record does not answer", func(t *testing.T) {
		h, recorded := newReviewerHarness(t, "head-1")
		h.reach(StateIdle)
		recorded.fail(errors.New("the store is down"))
		h.must(RequestDeliver{Claim: h.token, Task: "review round two"})
		h.m.Wait()
		h.wantPrompts(1)
		h.wantCalls("Resume", 0)
		h.wantState(StateIdle)
		recorded.fail(nil)
		h.must(IssueTurnEnded{Claim: h.token})
		h.m.Wait()
		h.wantPrompts(2)
	})
	t.Run("a relaunch whose launch still gives another head is raised once", func(t *testing.T) {
		h, recorded := newReviewerHarness(t, "")
		h.reach(StateIdle)
		recorded.set("head-2")
		h.must(RequestDeliver{Claim: h.token, Task: "review round two"})
		h.m.Wait()
		h.wantCalls("Resume", 1)
		h.relaunched()
		h.m.Wait()
		h.wantPrompts(1)
		h.wantCalls("Resume", 1)
		if lines := h.logs.lines("its launch gave it another", "head=head-2"); len(lines) != 1 {
			t.Fatalf("the refusal logged %q, want one line naming head-2", lines)
		}
		// The record moves on, and the spec builder now gives the reviewer that head.
		recorded.set("head-3")
		h.specs.setReviewHead("head-3")
		h.must(IssueTurnEnded{Claim: h.token})
		h.m.Wait()
		h.wantCalls("Resume", 2)
		h.relaunched()
		h.m.Wait()
		h.wantPrompts(2)
	})
}

// Only a reviewer's task asks for the head: every other role works the issue's shared workspace,
// which every push reaches, and its task reads no record of a head.
func TestOnlyAReviewersTaskReadsTheHead(t *testing.T) {
	h := newBareHarness(t)
	recorded := &recordedHead{head: "head-1"}
	h.deps.ReviewHead = recorded.answer
	c := queuedClaim()
	if err := h.store.PutClaim(h.ctx, c); err != nil {
		t.Fatal(err)
	}
	h.start(c)
	h.reach(StateIdle)
	recorded.set("head-2")
	h.must(RequestDeliver{Claim: h.token, Task: "implement the review's findings"})
	h.m.Wait()
	h.wantPrompts(2)
	h.wantCalls("Resume", 0)
	if n := recorded.reads(); n != 0 {
		t.Fatalf("the implementer's sends read the head %d times, want none", n)
	}
}
