package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// takingStore is the supervise store with a release's take of the claims' issue pod standing
// while taking is set, as the store answers while a teardown past its last read deletes that pod:
// the launch check answers the take's named wait (supervise.PodTakenLaunchError) and the
// operator's resume is recorded as owed to that release (OweResume). It is installed before the
// claim is created, since a machine takes its deps where it is built.
type takingStore struct {
	supervise.Store
	taking atomic.Bool
}

const (
	takenRelease = int64(7)
	takenSandbox = "the-issue-pod"
)

func (s *takingStore) CheckLaunch(ctx context.Context, c supervise.Claim) error {
	if s.taking.Load() {
		return &supervise.PodTakenLaunchError{Issue: c.Issue, Release: takenRelease, Sandbox: takenSandbox}
	}
	return s.Store.CheckLaunch(ctx, c)
}

func (s *takingStore) OweResume(ctx context.Context, c supervise.Claim) (int64, string, error) {
	if s.taking.Load() {
		return takenRelease, takenSandbox, nil
	}
	return s.Store.OweResume(ctx, c)
}

// The operator's routes over a claim whose issue pod a release's teardown has taken. The resume
// is owed: it answers 202 with the claim still suspended and the owed resume's sentence
// (OperatorClaim.ResumeOwed), and starts nothing. A spawn and a retry, which nothing of the
// request runs later, answer 409 with the launch's wait and ask the operator to ask again once
// the release ends. None of them answers 500. The control is the same resume with no take
// standing, which launches at once and answers 200 with no sentence.
func TestTheOperatorsRoutesAnswerALaunchAReleasesTakeRefuses(t *testing.T) {
	waits := &supervise.PodTakenLaunchError{Issue: spawnBody().Issue, Release: takenRelease, Sandbox: takenSandbox}
	route := func(request string) string {
		return "/legion/v1/operator/claims/" + string(architectToken) + "/" + request
	}
	starts := func(h *harness) int { return len(h.runtime.CallsOf("Resume")) + len(h.runtime.CallsOf("Spawn")) }
	// spawned is the architect launched and ready, over the store that takes its pod on
	// taking.
	spawned := func(t *testing.T) (*harness, *takingStore) {
		t.Helper()
		h := newHarness(t)
		store := &takingStore{Store: h.supervisor.deps.Store}
		h.supervisor.deps.Store = store
		if recorder := h.operator(http.MethodPost, "/legion/v1/operator/claims", spawnBody()); recorder.Code != http.StatusCreated {
			t.Fatalf("spawn = %d, want 201; body %s", recorder.Code, recorder.Body)
		}
		h.ready(architectToken, "ses_architect", 1)
		return h, store
	}
	suspended := func(t *testing.T) (*harness, *takingStore) {
		t.Helper()
		h, store := spawned(t)
		if recorder := h.operator(http.MethodPost, route("suspend"), nil); recorder.Code != http.StatusOK {
			t.Fatalf("suspend = %d, want 200; body %s", recorder.Code, recorder.Body)
		}
		return h, store
	}

	t.Run("the operator's resume is owed and answers 202 with its sentence", func(t *testing.T) {
		h, store := suspended(t)
		store.taking.Store(true)
		before := starts(h)
		recorder := h.operator(http.MethodPost, route("resume"), nil)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("resume = %d, want 202; body %s", recorder.Code, recorder.Body)
		}
		var answer OperatorClaim
		decodeInto(t, recorder, &answer)
		want := (&supervise.ResumeOwedError{Issue: waits.Issue, Release: takenRelease, Sandbox: takenSandbox}).Error()
		if answer.ResumeOwed != want || answer.State != string(supervise.StateSuspended) {
			t.Fatalf("resume answered the claim %s with resumeOwed %q, want it suspended with %q", answer.State, answer.ResumeOwed, want)
		}
		if n := starts(h) - before; n != 0 {
			t.Fatalf("the owed resume asked the runtime for %d starts, want none while the take stands", n)
		}
	})

	t.Run("a retry answers 409 and asks again once the release ends", func(t *testing.T) {
		h, store := spawned(t)
		m, _ := h.supervisor.Machine(architectToken)
		refused := fake.SpawnResult{Err: errors.New("the runtime refused the relaunch")}
		h.runtime.ScriptResume(refused, refused)
		if err := m.Handle(h.ctx, supervise.RuntimeObservation{Observation: runtime.Observation{
			Locator: *m.Claim().Locator, Kind: runtime.Gone}}); err == nil {
			t.Fatal("the relaunches the runtime refused were taken, want the refusal")
		}
		if failed := h.stored(architectToken); failed.State != supervise.StateFailed {
			t.Fatalf("the architect is %s, want it failed before its retry", failed.State)
		}
		store.taking.Store(true)
		before := starts(h)
		wantRefusal(t, h.operator(http.MethodPost, route("retry"), nil), http.StatusConflict,
			fmt.Sprintf("retry %s refused: %v; ask again once the release ends", architectToken, waits))
		if n := starts(h) - before; n != 0 {
			t.Fatalf("the refused retry asked the runtime for %d starts, want none", n)
		}
	})

	t.Run("a spawn answers 409 and asks again once the release ends", func(t *testing.T) {
		h := newHarness(t)
		store := &takingStore{Store: h.supervisor.deps.Store}
		store.taking.Store(true)
		h.supervisor.deps.Store = store
		wantRefusal(t, h.operator(http.MethodPost, "/legion/v1/operator/claims", spawnBody()), http.StatusConflict,
			fmt.Sprintf("spawn %s refused: %v; ask again once the release ends", architectToken, waits))
		if n := starts(h); n != 0 {
			t.Fatalf("the refused spawn asked the runtime for %d starts, want none", n)
		}
	})

	t.Run("control: the resume with no take standing launches and answers 200", func(t *testing.T) {
		h, _ := suspended(t)
		before := starts(h)
		recorder := h.operator(http.MethodPost, route("resume"), nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("resume = %d, want 200; body %s", recorder.Code, recorder.Body)
		}
		var answer OperatorClaim
		decodeInto(t, recorder, &answer)
		if answer.ResumeOwed != "" || answer.State != string(supervise.StateLaunching) {
			t.Fatalf("resume answered the claim %s with resumeOwed %q, want it launching with none", answer.State, answer.ResumeOwed)
		}
		if n := starts(h) - before; n != 1 {
			t.Fatalf("the resume asked the runtime for %d starts, want one", n)
		}
	})
}
