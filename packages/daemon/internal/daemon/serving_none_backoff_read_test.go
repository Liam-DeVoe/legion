package daemon

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
)

// The backoff's own reads of the mapping log no ERROR line, even when one of them finds the daemon
// serving none for another cause: the daemon was serving none before that read and still is, so it
// is no transition into serving none, and the one ERROR line of that episode has been logged. The
// cause the daemon serves none for now is in `legion state` and in the refresher's INFO line, whose
// waiting reason names it. The changes come with no mapping event: a row Dispatch holds that no
// settings.repo_project.updated announced, which is what the backoff's reads are for.
func TestABackoffReadThatFindsANewCauseWhileServingNoneLogsNoError(t *testing.T) {
	noneMapped, noneServed := api.NoRepositoryMapped("LEGION"), noRepositoryServed("LEGION")
	w, client, _, logged, clock := servingNoneLogRig(t, &installations{missing: map[string]bool{"review acme/widgets": true}}, withNoRepositoryMapped)
	wantServingNone(t, logged, "at boot", noneMapped)

	client.set(dispatch.RepoProject{Repo: "acme/widgets", Project: "LEGION", Default: true})
	readAgain(t, clock, 1)
	if _, refusal, leftOut, _ := w.repositories.lookup("acme/widgets"); !leftOut || refusal != "the review App is not installed on acme/widgets" {
		t.Fatalf("acme/widgets refusal = %q (left out %t), want it left out for the review App by the read of its own", refusal, leftOut)
	}
	if waiting := w.repositories.awaitingRead(); len(waiting) != 1 || waiting[0] != noneServed {
		t.Fatalf("awaiting a read = %q, want the new cause %q", waiting, noneServed)
	}
	wantServingNone(t, logged, "after a read of its own finds acme/widgets mapped and left out", noneMapped)

	client.set()
	readAgain(t, clock, 1)
	if waiting := w.repositories.awaitingRead(); len(waiting) != 1 || waiting[0] != noneMapped {
		t.Fatalf("awaiting a read = %q, want %q", waiting, noneMapped)
	}
	wantServingNone(t, logged, "after a read of its own finds acme/widgets unmapped", noneMapped)
}
