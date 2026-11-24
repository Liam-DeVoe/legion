package bus

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/testnats"
)

// The keeper's lines about a bucket its grant refuses (keepBuckets).
const (
	keeperGrantRefusedMsg = "envoy nats keeper may not raise a bucket under its grant; it asks again after the backoff"
	keeperGrantCoversMsg  = "envoy nats keeper's grant now covers a bucket the server refused it"
	keeperRaiseFailedMsg  = "envoy nats keeper could not raise a bucket"
	keeperRaisedMsg       = "envoy nats keeper raised a bucket"
)

// keeperTestClock is the clock a test runs a keeper's no-grant backoff on (withKeeperClock): it
// moves only when the test advances it, so a row passes keeperNoGrantBackoff without waiting it.
type keeperTestClock struct {
	mu sync.Mutex
	at time.Time
}

func newKeeperTestClock() *keeperTestClock {
	return &keeperTestClock{at: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)}
}

func (c *keeperTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *keeperTestClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// A keeper whose NATS user may not read one of the buckets it keeps - the known-session bucket on
// a hub whose users' render predates its grant - asks the server about that bucket once per
// keeperNoGrantBackoff, not once per pass, and logs the refusal once, while it raises the bucket
// it may read each time an operator lowers it. The user's grant leaves out that bucket's
// STREAM.INFO alone, the request the server refused in production every 30 s; the relay in front of
// the client's one member counts the sends of that request, which the server never answers, so the
// count is the requests and not the answers. A send that took longer than its read limit to be
// refused is sent again by design (readAgain), so the count is read as its growth: none over
// several passes inside one backoff, some once the backoff has passed, none again after that.
func TestTheKeeperAsksABucketItsGrantRefusesOncePerBackoffAndRaisesTheOthersEachPass(t *testing.T) {
	const granted, refused = "envoy_keeper_granted", "envoy_keeper_refused"
	refusedRead := "$JS.API.STREAM.INFO.KV_" + refused
	seed, user := testnats.User(t)
	_, admin := testnats.User(t)
	cluster := testnats.StartClusterConfig(t, 3, fmt.Sprintf(
		"authorization {\n  users = [ {nkey: %q, permissions: {publish: {allow: [\">\"], deny: [%q]}}}, {nkey: %q} ]\n}\nno_auth_user: %q\n",
		user, refusedRead, admin, admin))
	js := cluster.JetStream(t)
	adminConn := testnats.Connect(t, cluster.URL())
	t.Cleanup(adminConn.Close)
	for _, bucket := range []string{granted, refused} {
		cluster.CreateKeyValue(t, adminConn, &nats.KeyValueConfig{Bucket: bucket, Storage: nats.FileStorage})
	}
	logs := captureLogs(t)
	relay := startUnansweredRelayOn(t, cluster.Servers[0].URL(), refusedRead)
	clock := newKeeperTestClock()
	t.Setenv("NATS_NKEY_SEED", seed)
	client, err := ConnectOwningStream([]string{relay.url}, WithReplicas(3), WithKeptBuckets(granted, refused),
		WithKeepInterval(testKeepInterval), withKeeperClock(clock.now))
	if err != nil {
		t.Fatalf("start the keeper's client: %v", err)
	}
	t.Cleanup(client.Close)
	keepers := []*Client{client}

	// Three times over, the granted bucket is lowered and raised again, each raise a pass of its own.
	for round := range 3 {
		stream := "KV_" + granted
		if round > 0 {
			lowered := testnats.AwaitSettledStream(t, js, stream).Config
			lowered.Replicas = 1
			if _, err := js.UpdateStream(&lowered); err != nil {
				t.Fatalf("lower %s to one copy: %v", stream, err)
			}
		}
		awaitKept(t, js, stream, "three copies", func(info *nats.StreamInfo) bool { return info.Config.Replicas == 3 })
	}
	first := relay.sent()
	keeperPasses(t, keepers, 4)
	held := relay.sent()
	if first == 0 || held != first {
		t.Fatalf("sends of %s: %d after the granted bucket's three raises, %d after four passes more; want at least one and then none: one ask per backoff, not one per pass", refusedRead, first, held)
	}

	clock.advance(keeperNoGrantBackoff)
	keeperPasses(t, keepers, 2)
	again := relay.sent()
	keeperPasses(t, keepers, 4)
	if after := relay.sent(); again <= held || after != again {
		t.Fatalf("sends of %s: %d before the backoff passed, %d two passes after it, %d four passes later; want more once it passed and then no more", refusedRead, held, again, after)
	}
	t.Logf("sends of %s: %d in the first backoff over the granted bucket's three raises and four passes, %d after the second ask", refusedRead, held, again)

	warnings := logs.recordsWithMsg(t, keeperGrantRefusedMsg)
	if len(warnings) != 1 || warnings[0]["bucket"] != refused || warnings[0]["probe_again_in"] != keeperNoGrantBackoff.String() {
		t.Fatalf("the keeper's refusal lines = %v, want one, for %s, naming the next ask in %s", warnings, refused, keeperNoGrantBackoff)
	}
	if failures := logs.recordsWithMsg(t, keeperRaiseFailedMsg); len(failures) != 0 {
		t.Fatalf("the keeper logged failed raises %v, want none: a refusal for want of a grant is its own line, once", failures)
	}
	if raises := logs.recordsWithMsg(t, keeperRaisedMsg); len(raises) < 3 {
		t.Fatalf("the keeper logged raises %v, want one for each of the granted bucket's three lowerings", raises)
	}
}

// answeringJetStream answers the keeper's read of each bucket's stream (raiseReplicas) with the
// error answers names for it at the time of the read, or, with none, a stream at three copies, which
// needs no raise, and counts the reads of each stream. The keeper sends it nothing else.
type answeringJetStream struct {
	nats.JetStreamContext
	mu      sync.Mutex
	answers map[string]error
	reads   map[string]int
}

func newAnsweringJetStream() *answeringJetStream {
	return &answeringJetStream{answers: map[string]error{}, reads: map[string]int{}}
}

func (js *answeringJetStream) StreamInfo(stream string, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	js.reads[stream]++
	if err := js.answers[stream]; err != nil {
		return nil, err
	}
	return &nats.StreamInfo{Config: nats.StreamConfig{Name: stream, Replicas: 3}}, nil
}

func (js *answeringJetStream) answer(stream string, err error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	js.answers[stream] = err
}

func (js *answeringJetStream) readsOf(stream string) int {
	js.mu.Lock()
	defer js.mu.Unlock()
	return js.reads[stream]
}

// grantRefusal is the error a client's read of stream ends with when the server refuses it under
// the connection's grant: the server's violation, named ErrPublishDenied (deniedPublishError).
func grantRefusal(stream string) error {
	subject := "$JS.API.STREAM.INFO." + stream
	return deniedPublishError(subject, fmt.Errorf("%w: Permissions Violation for Publish to %q", nats.ErrPermissionViolation, subject))
}

// keeperBucketsUnderTest is a keeper's bucket pass (keepBuckets) over granted and refused, on js and
// clock, one call a pass.
func keeperBucketsUnderTest(t *testing.T, js nats.JetStreamContext, clock *keeperTestClock, granted, refused string) func() {
	t.Helper()
	client := &Client{name: "keeper-grant-test", stopCh: make(chan struct{})}
	record := &refusedBuckets{now: clock.now, until: map[string]time.Time{}}
	return func() { client.keepBuckets(t.Context(), js, []string{granted, refused}, 3, record) }
}

// A bucket whose read the server refuses for want of a grant is asked once per keeperNoGrantBackoff,
// to the nanosecond, and its refusal logged once with the time to the next ask; the bucket beside
// it is asked every pass.
func TestTheKeeperAsksABucketItsGrantRefusesOncePerBackoff(t *testing.T) {
	const granted, refused = "envoy_granted", "envoy_refused"
	logs := captureLogs(t)
	js, clock := newAnsweringJetStream(), newKeeperTestClock()
	js.answer("KV_"+refused, grantRefusal("KV_"+refused))
	pass := keeperBucketsUnderTest(t, js, clock, granted, refused)

	for range 4 {
		pass()
	}
	if got := js.readsOf("KV_" + refused); got != 1 {
		t.Fatalf("reads of the refused bucket over four passes = %d, want 1", got)
	}
	if got := js.readsOf("KV_" + granted); got != 4 {
		t.Fatalf("reads of the granted bucket over four passes = %d, want 4: a refusal holds only its own bucket", got)
	}
	clock.advance(keeperNoGrantBackoff - time.Nanosecond)
	pass()
	if got := js.readsOf("KV_" + refused); got != 1 {
		t.Fatalf("reads of the refused bucket a nanosecond before the backoff passed = %d, want 1", got)
	}
	clock.advance(time.Nanosecond)
	pass()
	if got := js.readsOf("KV_" + refused); got != 2 {
		t.Fatalf("reads of the refused bucket once the backoff passed = %d, want 2", got)
	}
	pass()
	if got := js.readsOf("KV_" + refused); got != 2 {
		t.Fatalf("reads of the refused bucket on the pass after its second refusal = %d, want 2: that refusal holds it again", got)
	}

	warnings := logs.recordsWithMsg(t, keeperGrantRefusedMsg)
	if len(warnings) != 1 || warnings[0]["bucket"] != refused || warnings[0]["probe_again_in"] != keeperNoGrantBackoff.String() {
		t.Fatalf("the keeper's refusal lines = %v, want one, for %s, naming the next ask in %s", warnings, refused, keeperNoGrantBackoff)
	}
	if failures := logs.recordsWithMsg(t, keeperRaiseFailedMsg); len(failures) != 0 {
		t.Fatalf("the keeper logged failed raises %v, want none", failures)
	}
}

// The ask that ends a backoff logs what it met: another failure its own line, and again on the next
// pass, which asks again since nothing refused it for want of a grant; an answer one line that the
// grant now covers the bucket, once; and a refusal after that answer starts a new episode, with its
// line again.
func TestTheKeeperLogsTheAskThatEndsABucketsBackoff(t *testing.T) {
	const granted, refused = "envoy_granted", "envoy_refused"
	stream := "KV_" + refused
	logs := captureLogs(t)
	js, clock := newAnsweringJetStream(), newKeeperTestClock()
	pass := keeperBucketsUnderTest(t, js, clock, granted, refused)
	lines := func(message string) int { return len(logs.recordsWithMsg(t, message)) }
	want := func(when string, reads, refusals, failures, covers int) {
		t.Helper()
		got := []int{js.readsOf(stream), lines(keeperGrantRefusedMsg), lines(keeperRaiseFailedMsg), lines(keeperGrantCoversMsg)}
		if fmt.Sprint(got) != fmt.Sprint([]int{reads, refusals, failures, covers}) {
			t.Fatalf("%s: reads, refusal lines, failure lines, grant lines = %v, want %v", when, got, []int{reads, refusals, failures, covers})
		}
	}

	js.answer(stream, grantRefusal(stream))
	pass()
	want("after the refusal", 1, 1, 0, 0)

	other := errors.New("nats: JetStream system temporarily unavailable")
	js.answer(stream, other)
	clock.advance(keeperNoGrantBackoff)
	pass()
	want("after another failure at the end of the backoff", 2, 1, 1, 0)
	if failure := logs.recordsWithMsg(t, keeperRaiseFailedMsg)[0]; failure["bucket"] != refused {
		t.Fatalf("the failure line = %v, want it for %s", failure, refused)
	}
	pass()
	want("on the pass after the other failure", 3, 1, 2, 0)

	js.answer(stream, nil)
	pass()
	want("after the answer", 4, 1, 2, 1)
	if covered := logs.recordsWithMsg(t, keeperGrantCoversMsg)[0]; covered["bucket"] != refused {
		t.Fatalf("the grant line = %v, want it for %s", covered, refused)
	}
	pass()
	want("on the pass after the answer", 5, 1, 2, 1)

	js.answer(stream, grantRefusal(stream))
	pass()
	want("after a refusal once more", 6, 2, 2, 1)
	pass()
	want("on the pass after that refusal", 6, 2, 2, 1)
	if got := js.readsOf("KV_" + granted); got != 7 {
		t.Fatalf("reads of the granted bucket over seven passes = %d, want 7", got)
	}
}
