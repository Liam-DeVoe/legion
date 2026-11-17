package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/kvwatch"
	"github.com/sjawhar/envoy/internal/testwait"
)

// unansweredFirstWatchKV is a role bucket's watch handle whose first fails watches run out of
// their wait unanswered (context.DeadlineExceeded), as a loaded listener's watcher create did
// (INFRA-4240's CPU-starvation control: "role claims watch failed ... context deadline
// exceeded"), and whose later watches are the bucket's own.
type unansweredFirstWatchKV struct {
	jetstream.KeyValue
	fails   int32
	watched atomic.Int32
}

func (k *unansweredFirstWatchKV) Watch(ctx context.Context, keys string, opts ...jetstream.WatchOpt) (jetstream.KeyWatcher, error) {
	if k.watched.Add(1) <= k.fails {
		return nil, fmt.Errorf("create the watcher: %w", context.DeadlineExceeded)
	}
	return k.KeyValue.Watch(ctx, keys, opts...)
}

// heldWaits is a kvwatch AfterFunc that keeps every wait but a scan's idle wait for the test to
// fire, as the real timer would once its delay passed.
type heldWaits struct {
	mu    sync.Mutex
	fires []func()
	delay []time.Duration
}

func (h *heldWaits) after(d time.Duration, f func()) kvwatch.Timer {
	if d == kvwatch.ScanIdleTimeout {
		return time.AfterFunc(d, f)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fires = append(h.fires, f)
	h.delay = append(h.delay, d)
	return heldTimer{}
}

// armed is how many waits the watcher has armed, and the delay of the last.
func (h *heldWaits) armed() (int, time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.fires) == 0 {
		return 0, 0
	}
	return len(h.fires), h.delay[len(h.delay)-1]
}

// fire runs the i-th wait's callback.
func (h *heldWaits) fire(i int) {
	h.mu.Lock()
	f := h.fires[i]
	h.mu.Unlock()
	f()
}

type heldTimer struct{}

func (heldTimer) Stop() bool { return true }

// The role claim watcher makes a first watch that went unanswered again, on its own clock, and the
// view it then reads holds every claim a first watch that succeeded reads: the one a loaded
// listener's start lost to a watcher create that ran out of its wait is a WARN, and the listener's
// role claim view fills a moment later rather than wait for the self-health rebuild. The row's
// watcher is the store's own (newRoleWatcher), on a handle whose first watch times out, put in
// place of the one Open started once that one's view is read as the first try's.
func TestARoleClaimWatchWhoseFirstWatchGoesUnansweredReadsEveryClaimOnItsNextAttempt(t *testing.T) {
	conn, cleanup := connectNATS(t)
	defer cleanup()
	logs := &lockedBuffer{}
	registry, err := Open(conn, WithReplicas(1), WithLogger(slog.New(slog.NewJSONHandler(logs, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(registry.StopWatch)
	for i := range 6 {
		putRoleClaim(t, registry.roles().Legacy(), fmt.Sprintf("role-%d", i), fmt.Sprintf("ses_%d", i%3))
	}
	// The first try that succeeded: Open's own watcher, once it has read all six claims.
	firstTry := testwait.Until(t, "Open's role claim view to hold all six claims", func() (map[string][]string, bool) {
		held, err := registry.RolesBySession()
		count := 0
		for _, roles := range held {
			count += len(roles)
		}
		return held, err == nil && count == 6
	})

	registry.roleWatcher.Stop()
	registry.holdersMu.Lock()
	registry.roleHolders = newRoleHolderCopy(registry)
	registry.holdersMu.Unlock()
	roles := registry.roles()
	seam := &unansweredFirstWatchKV{KeyValue: roles.WatchHandle(), fails: 1}
	waits := &heldWaits{}
	registry.roleWatcher = registry.newRoleWatcher(bus.NewKeyValue(roles.Legacy(), seam, roles.Streams()),
		kvwatch.WithAfterFunc(waits.after), kvwatch.WithStreamProbeInterval(0))
	registry.roleWatcher.Start()

	attempt := testwait.Until(t, "the first attempt's WARN", func() (map[string]any, bool) {
		for _, record := range jsonRecords(logs.String()) {
			if record["level"] == "WARN" && record["msg"] == "role claims first watch attempt failed; it is made again" {
				return record, true
			}
		}
		return nil, false
	})
	if attempt["attempt"] != float64(1) || !strings.Contains(fmt.Sprint(attempt["error"]), "context deadline exceeded") {
		t.Fatalf("the first attempt's WARN = %v, want attempt 1 naming the deadline", attempt)
	}
	count, delay := waits.armed()
	if count != 1 || delay != 5*time.Second {
		t.Fatalf("after the first attempt the watcher armed %d waits, the last of %s; want one, of 5s (the re-read's wait)", count, delay)
	}
	waits.fire(0)
	testwait.Eventually(t, "the second attempt's view to answer", func() bool { return registry.RoleViewErr() == nil })
	retried, err := registry.RolesBySession()
	if err != nil || !maps.EqualFunc(retried, firstTry, slices.Equal) {
		t.Fatalf("the view the second attempt read = %v, %v; want the first try's %v", retried, err, firstTry)
	}
	for _, record := range jsonRecords(logs.String()) {
		if record["level"] == "ERROR" {
			t.Fatalf("a first watch that recovered on its second attempt logged an ERROR: %v", record)
		}
	}
}

// The interest registry's watcher makes a first watch that went unanswered again on its own clock,
// as the role claim watcher does, and the copy it then reads lists every interest a first watch
// that succeeded lists. The row's watcher is the store's own (newInterestWatcher), on a handle
// whose first watch times out, put in place of the one Open started once that one's list is read
// as the first try's.
func TestAnInterestWatchWhoseFirstWatchGoesUnansweredReadsEveryInterestOnItsNextAttempt(t *testing.T) {
	conn, cleanup := connectNATS(t)
	defer cleanup()
	logs := &lockedBuffer{}
	registry, err := Open(conn, WithReplicas(1), WithLogger(slog.New(slog.NewJSONHandler(logs, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(registry.StopWatch)
	interests := registry.watcher.KV()
	for i := range 4 {
		sessionID := fmt.Sprintf("ses_interest_%d", i)
		putInterest(t, interests.Legacy(), Interest{SessionID: sessionID, MachineID: "m1", Topics: []string{"notifications.agent." + sessionID}})
	}
	firstTry := testwait.Until(t, "Open's interest copy to list all four interests", func() ([]Interest, bool) {
		listed, err := registry.List()
		return listed, err == nil && len(listed) == 4
	})

	registry.watcher.Stop()
	registry.mu.Lock()
	registry.cache = newInterestCopy(registry)
	registry.mu.Unlock()
	seam := &unansweredFirstWatchKV{KeyValue: interests.WatchHandle(), fails: 1}
	waits := &heldWaits{}
	registry.watcher = registry.newInterestWatcher(bus.NewKeyValue(interests.Legacy(), seam, interests.Streams()),
		kvwatch.WithAfterFunc(waits.after), kvwatch.WithStreamProbeInterval(0))
	registry.watcher.Start()

	attempt := testwait.Until(t, "the first attempt's WARN", func() (map[string]any, bool) {
		for _, record := range jsonRecords(logs.String()) {
			if record["level"] == "WARN" && record["msg"] == "interest registry first watch attempt failed; it is made again" {
				return record, true
			}
		}
		return nil, false
	})
	if attempt["attempt"] != float64(1) {
		t.Fatalf("the first attempt's WARN = %v, want attempt 1", attempt)
	}
	if count, delay := waits.armed(); count != 1 || delay != 5*time.Second {
		t.Fatalf("after the first attempt the watcher armed %d waits, the last of %s; want one, of 5s (the re-read's wait)", count, delay)
	}
	waits.fire(0)
	retried := testwait.Until(t, "the second attempt's copy to list again", func() ([]Interest, bool) {
		listed, err := registry.List()
		return listed, err == nil
	})
	if !reflect.DeepEqual(retried, firstTry) {
		t.Fatalf("the copy the second attempt read lists %v, want the first try's %v", retried, firstTry)
	}
	for _, record := range jsonRecords(logs.String()) {
		if record["level"] == "ERROR" {
			t.Fatalf("a first watch that recovered on its second attempt logged an ERROR: %v", record)
		}
	}
}

// The role-revision scan's consumer create is one of the open's requests and waits the open's own
// request wait: on a context with no deadline, as a production open's, nats.go holds one create to
// 5 s, so the scan sends it again while the sends fit in the open's 10 s. A create that never
// answers within that wait fails the scan naming the request and the wait, so the listener's start
// that ends on it says why ("role revisions: create the scan's consumer: context deadline exceeded
// (waited 10s)"), where it said a bare "context deadline exceeded". A create unanswered once is
// answered by the next send, and the scan reads every claim. The creates are a seam's, which
// answers each with the deadline at once.
func TestTheRoleRevisionScansConsumerCreateWaitsTheOpensRequestWait(t *testing.T) {
	conn, cleanup := connectNATS(t)
	defer cleanup()
	registry, err := Open(conn, WithReplicas(1))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(registry.StopWatch)
	roles := registry.roles()
	for i := range 3 {
		putRoleClaim(t, roles.Legacy(), fmt.Sprintf("role-%d", i), fmt.Sprintf("ses_%d", i))
	}
	const openWait = 10 * time.Second

	t.Run("a create unanswered once is sent again", func(t *testing.T) {
		seam := &unansweredFirstWatchKV{KeyValue: roles.WatchHandle(), fails: 1}
		revisions, err := roleRevisions(context.Background(), bus.NewKeyValue(roles.Legacy(), seam, roles.Streams()), registry.clock, kvwatch.ScanIdleTimeout, slog.Default(), openWait)
		if err != nil || len(revisions) != 3 {
			t.Fatalf("the scan whose first create went unanswered = %v, %v; want all 3 claims' revisions", revisions, err)
		}
		if sent := seam.watched.Load(); sent != 2 {
			t.Fatalf("the scan sent its create %d times, want 2", sent)
		}
	})

	t.Run("a create never answered within the wait fails naming it", func(t *testing.T) {
		seam := &unansweredFirstWatchKV{KeyValue: roles.WatchHandle(), fails: 1 << 20}
		_, err := roleRevisions(context.Background(), bus.NewKeyValue(roles.Legacy(), seam, roles.Streams()), registry.clock, kvwatch.ScanIdleTimeout, slog.Default(), openWait)
		want := "role revisions: create the scan's consumer: create the watcher: context deadline exceeded (waited 10s)"
		if err == nil || err.Error() != want || !errors.Is(err, kvwatch.ErrScanNotCreated) {
			t.Fatalf("the scan whose create never answered = %v, want %q", err, want)
		}
		if sent := seam.watched.Load(); sent != 2 {
			t.Fatalf("the scan sent its create %d times within the open's 10s, want 2 sends of nats.go's 5s", sent)
		}
	})
}

// jsonRecords is every JSON record in logs, one a line.
func jsonRecords(logs string) []map[string]any {
	var records []map[string]any
	for line := range strings.SplitSeq(logs, "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil {
			records = append(records, record)
		}
	}
	return records
}
