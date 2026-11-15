package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
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
