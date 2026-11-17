package daemon

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// servingNoneMessage is the one ERROR line the daemon writes each time it comes to serve no
// repository, as an operator's log filter names it; its cause attribute says why.
const servingNoneMessage = "workflow: the daemon serves no repository, so it admits nothing and reads the mapping again"

// servingNoneLogRig is the mapping rig logging to logged, with each of the refresher's reads of
// its own held until the test ticks clock, so a test sees whether a read of its own logs again.
func servingNoneLogRig(t *testing.T, installs *installations, opts ...func(*workflowRuntime)) (*workflowRuntime, *mappingDispatch, *installations, *syncBuffer, *recheckWaits) {
	t.Helper()
	logged, clock := &syncBuffer{}, newRecheckWaits()
	clock.spin.Store(false)
	opts = append([]func(*workflowRuntime){func(w *workflowRuntime) {
		w.log, w.recheckAfter = slog.New(slog.NewTextHandler(logged, nil)), clock.after
	}}, opts...)
	w, client, installs, _ := mappingRigWith(t, installs, opts...)
	return w, client, installs, logged, clock
}

// wantServingNone fails t unless logged's ERROR lines are exactly one serving-none line for each
// of causes, in order, each naming the project.
func wantServingNone(t *testing.T, logged *syncBuffer, when string, causes ...string) {
	t.Helper()
	var lines []string
	for line := range strings.SplitSeq(logged.String(), "\n") {
		if strings.Contains(line, "level=ERROR") {
			lines = append(lines, line)
		}
	}
	ok := len(lines) == len(causes)
	for i := 0; ok && i < len(causes); i++ {
		ok = strings.Contains(lines[i], `msg="`+servingNoneMessage+`"`) && strings.Contains(lines[i], causes[i]) && strings.Contains(lines[i], "project=LEGION")
	}
	if !ok {
		t.Fatalf("%s: ERROR lines = %q, want one %q line for each cause of %q", when, lines, servingNoneMessage, causes)
	}
}

// readNow asks the refresher for a read of the mapping and waits for one that covers the request.
func readNow(t *testing.T, w *workflowRuntime) {
	t.Helper()
	read := w.requestMappingRead()
	testwait.Eventually(t, "the read of the mapping asked for", func() bool { return w.mappingApplied.Load() >= read })
}

// readAgain has the refresher make n reads of its own, one per tick, and waits for the last to
// finish: the refresher is waiting for its next tick again.
func readAgain(t *testing.T, clock *recheckWaits, n int) {
	t.Helper()
	testwait.Eventually(t, "the refresher waiting for a read of its own", func() bool { return clock.ticking.Load() > 0 })
	waiting := clock.ticking.Load()
	for range n {
		select {
		case clock.tick <- time.Now():
		case <-time.After(10 * time.Second):
			t.Fatal("the refresher never took the tick of its next read of its own")
		}
	}
	testwait.Eventually(t, "the last read of its own finished", func() bool { return clock.ticking.Load() >= waiting+int64(n) })
}

// Every path that leaves the daemon serving no repository logs it once, at ERROR, on one line
// whose cause says why: the project maps no repository (api.NoRepositoryMapped), or every
// repository it maps is left out (noRepositoryServed). That holds at boot, and on a read of the
// mapping that unmaps the last served repository or leaves out every one, and the reads of its
// own it then makes on its backoff say nothing more. Serving one again ends the episode, so the
// next one logs again; a new cause while it serves none is no transition and logs nothing (its
// cause is in legion state and the refresher's waiting reason). A project that serves a
// repository logs no ERROR, whatever it leaves out beside it.
func TestEveryPathIntoServingNoneLogsOneErrorLineWithItsCause(t *testing.T) {
	noneMapped, noneServed := api.NoRepositoryMapped("LEGION"), noRepositoryServed("LEGION")
	t.Run("no repository mapped at boot", func(t *testing.T) {
		_, _, _, logged, clock := servingNoneLogRig(t, &installations{}, withNoRepositoryMapped)
		wantServingNone(t, logged, "at boot", noneMapped)
		readAgain(t, clock, 3)
		wantServingNone(t, logged, "after three reads of its own", noneMapped)
	})
	t.Run("every mapped repository lacks its App at boot", func(t *testing.T) {
		_, _, _, logged, clock := servingNoneLogRig(t, &installations{missing: map[string]bool{"review acme/widgets": true, "review zeta/tools": true}})
		wantServingNone(t, logged, "at boot", noneServed)
		readAgain(t, clock, 3)
		wantServingNone(t, logged, "after three reads of its own", noneServed)
	})
	t.Run("the last repository unmapped while it runs", func(t *testing.T) {
		w, client, _, logged, clock := servingNoneLogRig(t, &installations{})
		wantServingNone(t, logged, "at boot, serving both")
		client.set()
		readNow(t, w)
		wantServingNone(t, logged, "once the last repository is unmapped", noneMapped)
		readAgain(t, clock, 3)
		wantServingNone(t, logged, "after three reads of its own", noneMapped)
		client.set(dispatch.RepoProject{Repo: "acme/widgets", Project: "LEGION", Default: true})
		readNow(t, w)
		if !served(w, "acme/widgets") {
			t.Fatal("acme/widgets not served once mapped again")
		}
		client.set()
		readNow(t, w)
		wantServingNone(t, logged, "once it is unmapped a second time", noneMapped, noneMapped)
	})
	t.Run("every repository loses its App on a read", func(t *testing.T) {
		w, _, installs, logged, clock := servingNoneLogRig(t, &installations{})
		installs.setMissing(true, "review acme/widgets", "review zeta/tools")
		readNow(t, w)
		wantServingNone(t, logged, "once every repository lacks its App", noneServed)
		readAgain(t, clock, 3)
		wantServingNone(t, logged, "after three reads of its own", noneServed)
	})
	t.Run("a new cause while it serves none", func(t *testing.T) {
		w, client, _, logged, _ := servingNoneLogRig(t, &installations{missing: map[string]bool{"review acme/widgets": true}}, withNoRepositoryMapped)
		client.set(dispatch.RepoProject{Repo: "acme/widgets", Project: "LEGION", Default: true})
		readNow(t, w)
		if waiting := w.repositories.awaitingRead(); len(waiting) != 1 || waiting[0] != noneServed {
			t.Fatalf("awaiting a read = %q, want the new cause %q", waiting, noneServed)
		}
		wantServingNone(t, logged, "once a repository lacking its App is mapped, still serving none", noneMapped)
	})
	t.Run("a project that serves a repository", func(t *testing.T) {
		w, client, installs, logged, _ := servingNoneLogRig(t, &installations{})
		installs.setMissing(true, "review zeta/tools")
		readNow(t, w)
		client.set(dispatch.RepoProject{Repo: "acme/widgets", Project: "LEGION", Default: true})
		readNow(t, w)
		if !served(w, "acme/widgets") {
			t.Fatal("acme/widgets not served")
		}
		wantServingNone(t, logged, "while acme/widgets is served")
	})
}
