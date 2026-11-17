package daemon

import (
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// An organization boots the daemon before it maps a repository, maps one later, and installs the
// repository's App after that: the turnkey order. The repository it maps is left out for the App
// it lacks, and the daemon reads the mapping again of its own on a backoff of 30 s doubling to
// 10 min, starting at 30 s from that mapping, however long the project sat with nothing mapped
// before it. A backoff carried over from the empty mapping would make the first read after the
// mapping wait as long as the empty project's backoff had grown, up to 10 min, so an App installed
// a minute after the mapping is found up to ten minutes later.
func TestAMappingAfterALongEmptyWaitStartsTheBackoffAgainAt30s(t *testing.T) {
	clock := newRecheckWaits()
	installs := &installations{missing: map[string]bool{"review acme/widgets": true}}
	w, client, _, _ := mappingRigWith(t, installs, withNoRepositoryMapped, func(w *workflowRuntime) { w.recheckAfter = clock.after })
	// The project sits with nothing mapped until its backoff has grown to the cap.
	testwait.Eventually(t, "six waits while nothing is mapped", func() bool { return len(clock.recorded()) >= 6 })
	clock.spin.Store(false)
	testwait.Eventually(t, "the refresher waiting for its next read", func() bool { return clock.ticking.Load() > 0 })
	before := len(clock.recorded())
	if got := clock.recorded()[before-1]; got != 10*time.Minute {
		t.Fatalf("the last wait while nothing is mapped = %s, want the 10 min cap", got)
	}

	client.set(dispatch.RepoProject{Repo: "acme/widgets", Project: "LEGION", Default: true})
	w.requestMappingRead()
	testwait.Eventually(t, "acme/widgets left out and the refresher waiting after the mapping's read", func() bool {
		_, refusal, leftOut, _ := w.repositories.lookup("acme/widgets")
		return leftOut && refusal == "the review App is not installed on acme/widgets" && len(clock.recorded()) > before
	})
	if got := clock.recorded()[before]; got != 30*time.Second {
		t.Fatalf("the first wait after acme/widgets was mapped and left out = %s, want 30s: the backoff starts again for the new mapping", got)
	}
}
