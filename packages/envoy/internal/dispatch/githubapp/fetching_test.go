package githubapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/testwait"
)

// A fetch of the client's caches runs on a context its callers' cancel does not reach (ttlCache.get),
// so a caller that leaves before it answers leaves it running, and a rate limit GitHub answers it
// with is still recorded. Fetching answers true from before the call joins the fetch until the
// fetch has answered, whether or not its caller stayed, which the cache's stored answer shows.
func TestFetchingCountsAFetchWhoseCallerLeft(t *testing.T) {
	client := &Client{
		repositories:      newTTLCache[string, int64](time.Minute, time.Now),
		installations:     newTTLCache[string, []Installation](time.Minute, time.Now),
		installationRepos: newTTLCache[int64, []string](time.Minute, time.Now),
	}
	gate := make(chan struct{})
	started := make(chan struct{})
	ctx, leave := context.WithCancel(context.Background())
	defer leave()
	answered := make(chan error, 1)
	go func() {
		_, err := client.repositories.get(ctx, "owner/repo", func(context.Context) (int64, error) {
			close(started)
			<-gate
			return 7, nil
		})
		answered <- err
	}()
	<-started
	leave()
	if err := <-answered; !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller that left was answered %v, want its own context's cancel", err)
	}
	if !client.Fetching() {
		t.Fatal("Fetching answered false while the fetch whose caller left still ran")
	}
	close(gate)
	testwait.Eventually(t, "the fetch to answer and Fetching to fall", func() bool { return !client.Fetching() })
	if value, fresh := client.repositories.fresh("owner/repo"); !fresh || value != 7 {
		t.Fatalf("the cache holds %d (fresh %v) once Fetching fell, want the fetch's 7: Fetching fell before the fetch answered", value, fresh)
	}
}
