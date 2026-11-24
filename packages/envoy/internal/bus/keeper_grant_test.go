package bus

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// The keeper's lines about a bucket its grant refuses (keepBuckets).
const (
	keeperGrantRefusedMsg = "envoy nats keeper may not raise a bucket under its grant; it asks again after the backoff"
	keeperGrantCoversMsg  = "envoy nats keeper's grant now covers a bucket the server refused it"
	keeperRaiseFailedMsg  = "envoy nats keeper could not raise a bucket"
	keeperRaisedMsg       = "envoy nats keeper raised a bucket"
)

// keeperTestClock is a stepped client clock (keeperClockOption, keeperBucketPass) at a fixed start,
// so a row passes keeperNoGrantBackoff without waiting it.
func keeperTestClock() *steppedClock {
	return &steppedClock{now: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)}
}

// keeperGrantUsers is the authorization block of a cluster whose user may publish everything but
// denied, and whose admin, the no_auth_user, may publish everything.
func keeperGrantUsers(user, admin, denied string) string {
	return fmt.Sprintf("authorization {\n  users = [ {nkey: %q, permissions: {publish: {allow: [\">\"], deny: [%q]}}}, {nkey: %q} ]\n}\nno_auth_user: %q\n",
		user, denied, admin, admin)
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
	refusedRead := openSubject(refused)
	seed, user := testnats.User(t)
	_, admin := testnats.User(t)
	cluster := testnats.StartClusterConfig(t, 3, keeperGrantUsers(user, admin, refusedRead))
	js := cluster.JetStream(t)
	adminConn := testnats.Connect(t, cluster.URL())
	t.Cleanup(adminConn.Close)
	for _, bucket := range []string{granted, refused} {
		cluster.CreateKeyValue(t, adminConn, &nats.KeyValueConfig{Bucket: bucket, Storage: nats.FileStorage})
	}
	logs := captureLogs(t)
	relay := startUnansweredRelayOn(t, cluster.Servers[0].URL(), refusedRead)
	clock := keeperTestClock()
	t.Setenv("NATS_NKEY_SEED", seed)
	client, err := ConnectOwningStream([]string{relay.url}, WithReplicas(3), WithKeptBuckets(granted, refused),
		WithKeepInterval(testKeepInterval), keeperClockOption(clock))
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

	clock.Advance(keeperNoGrantBackoff)
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

// A keeper whose NATS user may read a bucket but not update it - a grant with the bucket's
// STREAM.INFO and without its STREAM.UPDATE - logs the refused raise once, naming the update, and
// says nothing of a grant that covers the bucket once an operator has raised the bucket by hand:
// the keeper's next read finds it at its copies and sends no update, so the update is still
// refused. The relay in front of the client's one member counts the keeper's reads of the bucket,
// which the server answers, so the row knows the keeper asked again before it reads the log.
func TestTheKeeperClaimsNoGrantForAnUpdateItHasNotSentAgain(t *testing.T) {
	const refused = "envoy_keeper_update_refused"
	stream := "KV_" + refused
	seed, user := testnats.User(t)
	_, admin := testnats.User(t)
	cluster := testnats.StartClusterConfig(t, 3, keeperGrantUsers(user, admin, updateSubject(refused)))
	js := cluster.JetStream(t)
	adminConn := testnats.Connect(t, cluster.URL())
	t.Cleanup(adminConn.Close)
	cluster.CreateKeyValue(t, adminConn, &nats.KeyValueConfig{Bucket: refused, Storage: nats.FileStorage})
	logs := captureLogs(t)
	relay := startUnansweredRelayOn(t, cluster.Servers[0].URL(), openSubject(refused))
	clock := keeperTestClock()
	t.Setenv("NATS_NKEY_SEED", seed)
	client, err := ConnectOwningStream([]string{relay.url}, WithReplicas(3), WithKeptBuckets(refused),
		WithKeepInterval(testKeepInterval), keeperClockOption(clock))
	if err != nil {
		t.Fatalf("start the keeper's client: %v", err)
	}
	t.Cleanup(client.Close)

	warnings := testwait.Until(t, "the keeper's refusal line for "+refused, func() ([]map[string]any, bool) {
		records := logs.recordsWithMsg(t, keeperGrantRefusedMsg)
		return records, len(records) > 0
	})
	want := `Permissions Violation for Publish to "` + updateSubject(refused) + `"`
	if text, _ := warnings[0]["error"].(string); warnings[0]["bucket"] != refused || !strings.Contains(text, want) {
		t.Fatalf("the keeper's refusal line = %v, want it for %s naming %s", warnings[0], refused, want)
	}

	raised := testnats.AwaitSettledStream(t, js, stream).Config
	raised.Replicas = 3
	if _, err := js.UpdateStream(&raised); err != nil {
		t.Fatalf("raise %s to three copies as an operator: %v", stream, err)
	}
	awaitKept(t, js, stream, "three copies", func(info *nats.StreamInfo) bool { return info.Config.Replicas == 3 })
	before := relay.sent()
	clock.Advance(keeperNoGrantBackoff)
	testwait.Until(t, "the keeper to read "+stream+" again once the backoff passed", func() (int, bool) {
		sent := relay.sent()
		return sent, sent > before
	})
	keeperPasses(t, []*Client{client}, 2)

	if covers := logs.recordsWithMsg(t, keeperGrantCoversMsg); len(covers) != 0 {
		t.Fatalf("the keeper said its grant now covers %s (%v), though the update it was refused has not been sent since", refused, covers)
	}
	if lines := logs.recordsWithMsg(t, keeperGrantRefusedMsg); len(lines) != 1 {
		t.Fatalf("the keeper's refusal lines = %v, want the one", lines)
	}
	if failures := logs.recordsWithMsg(t, keeperRaiseFailedMsg); len(failures) != 0 {
		t.Fatalf("the keeper logged failed raises %v, want none", failures)
	}
}

// answeringJetStream is a JetStream context for the keeper's bucket pass (keepBuckets): it answers
// the read of each bucket's stream with the error set for it, or with the stream at its copies
// (three unless set), and an update with the error set for it, or by taking the update's copies. It
// counts the reads and the updates of each stream. The keeper sends it nothing else.
type answeringJetStream struct {
	nats.JetStreamContext
	mu        sync.Mutex
	copies    map[string]int
	readErr   map[string]error
	updateErr map[string]error
	reads     map[string]int
	updatesOf map[string]int
}

func newAnsweringJetStream() *answeringJetStream {
	return &answeringJetStream{copies: map[string]int{}, readErr: map[string]error{}, updateErr: map[string]error{},
		reads: map[string]int{}, updatesOf: map[string]int{}}
}

func (js *answeringJetStream) info(stream string) *nats.StreamInfo {
	copies, set := js.copies[stream]
	if !set {
		copies = 3
	}
	return &nats.StreamInfo{Config: nats.StreamConfig{Name: stream, Replicas: copies}}
}

func (js *answeringJetStream) StreamInfo(stream string, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	js.reads[stream]++
	if err := js.readErr[stream]; err != nil {
		return nil, err
	}
	return js.info(stream), nil
}

func (js *answeringJetStream) UpdateStream(cfg *nats.StreamConfig, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	js.updatesOf[cfg.Name]++
	if err := js.updateErr[cfg.Name]; err != nil {
		return nil, err
	}
	js.copies[cfg.Name] = cfg.Replicas
	return js.info(cfg.Name), nil
}

// set changes how js answers stream from now on: its copies (0 leaves them), its read's error and
// its update's error.
func (js *answeringJetStream) set(stream string, copies int, readErr, updateErr error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	if copies > 0 {
		js.copies[stream] = copies
	}
	js.readErr[stream] = readErr
	js.updateErr[stream] = updateErr
}

func (js *answeringJetStream) counts(stream string) (reads, updates int) {
	js.mu.Lock()
	defer js.mu.Unlock()
	return js.reads[stream], js.updatesOf[stream]
}

// readRefusal and updateRefusal are the errors a client's read or update of stream ends with when
// the server refuses it under the connection's grant: the server's violation, named
// ErrPublishDenied (deniedPublishError).
func readRefusal(stream string) error { return grantRefusalOf("$JS.API.STREAM.INFO." + stream) }

func updateRefusal(stream string) error { return grantRefusalOf("$JS.API.STREAM.UPDATE." + stream) }

func grantRefusalOf(subject string) error {
	return deniedPublishError(subject, fmt.Errorf("%w: Permissions Violation for Publish to %q", nats.ErrPermissionViolation, subject))
}

// keeperLines counts the keeper's lines logged so far: refusals, failed raises, grant lines and
// raises, in that order.
func keeperLines(t *testing.T, logs *capturedLogs) [4]int {
	t.Helper()
	return [4]int{
		len(logs.recordsWithMsg(t, keeperGrantRefusedMsg)), len(logs.recordsWithMsg(t, keeperRaiseFailedMsg)),
		len(logs.recordsWithMsg(t, keeperGrantCoversMsg)), len(logs.recordsWithMsg(t, keeperRaisedMsg)),
	}
}

// A bucket whose read the server refuses for want of a grant is asked once per keeperNoGrantBackoff,
// to the nanosecond, and its refusal logged once with the time to the next ask; the bucket beside
// it is asked every pass.
func TestTheKeeperAsksABucketItsGrantRefusesOncePerBackoff(t *testing.T) {
	const granted, refused = "envoy_granted", "envoy_refused"
	logs := captureLogs(t)
	js, clock := newAnsweringJetStream(), keeperTestClock()
	js.set("KV_"+refused, 0, readRefusal("KV_"+refused), nil)
	pass := keeperBucketPass(t, js, clock, granted, refused)
	readsOf := func(bucket string) int { reads, _ := js.counts("KV_" + bucket); return reads }

	for range 4 {
		pass()
	}
	if got := readsOf(refused); got != 1 {
		t.Fatalf("reads of the refused bucket over four passes = %d, want 1", got)
	}
	if got := readsOf(granted); got != 4 {
		t.Fatalf("reads of the granted bucket over four passes = %d, want 4: a refusal holds only its own bucket", got)
	}
	clock.Advance(keeperNoGrantBackoff - time.Nanosecond)
	pass()
	if got := readsOf(refused); got != 1 {
		t.Fatalf("reads of the refused bucket a nanosecond before the backoff passed = %d, want 1", got)
	}
	clock.Advance(time.Nanosecond)
	pass()
	if got := readsOf(refused); got != 2 {
		t.Fatalf("reads of the refused bucket once the backoff passed = %d, want 2", got)
	}
	pass()
	if got := readsOf(refused); got != 2 {
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

// The ask that ends a read refusal's backoff logs what it met: another failure its own line, and
// again on the next pass, which asks again since nothing refused it for want of a grant; an answer
// one line that the grant now covers the bucket, once; and a refusal after that answer starts a new
// episode, with its line again.
func TestTheKeeperLogsTheAskThatEndsABucketsBackoff(t *testing.T) {
	const granted, refused = "envoy_granted", "envoy_refused"
	stream := "KV_" + refused
	logs := captureLogs(t)
	js, clock := newAnsweringJetStream(), keeperTestClock()
	pass := keeperBucketPass(t, js, clock, granted, refused)
	want := func(when string, reads int, lines [4]int) {
		t.Helper()
		got, _ := js.counts(stream)
		if got != reads || keeperLines(t, logs) != lines {
			t.Fatalf("%s: reads %d and lines (refusals, failures, grant, raises) %v, want %d and %v", when, got, keeperLines(t, logs), reads, lines)
		}
	}

	js.set(stream, 0, readRefusal(stream), nil)
	pass()
	want("after the refusal", 1, [4]int{1, 0, 0, 0})

	js.set(stream, 0, errors.New("nats: JetStream system temporarily unavailable"), nil)
	clock.Advance(keeperNoGrantBackoff)
	pass()
	want("after another failure at the end of the backoff", 2, [4]int{1, 1, 0, 0})
	if failure := logs.recordsWithMsg(t, keeperRaiseFailedMsg)[0]; failure["bucket"] != refused {
		t.Fatalf("the failure line = %v, want it for %s", failure, refused)
	}
	pass()
	want("on the pass after the other failure", 3, [4]int{1, 2, 0, 0})

	js.set(stream, 0, nil, nil)
	pass()
	want("after the answer", 4, [4]int{1, 2, 1, 0})
	if covered := logs.recordsWithMsg(t, keeperGrantCoversMsg)[0]; covered["bucket"] != refused {
		t.Fatalf("the grant line = %v, want it for %s", covered, refused)
	}
	pass()
	want("on the pass after the answer", 5, [4]int{1, 2, 1, 0})

	js.set(stream, 0, readRefusal(stream), nil)
	pass()
	want("after a refusal once more", 6, [4]int{2, 2, 1, 0})
	pass()
	want("on the pass after that refusal", 6, [4]int{2, 2, 1, 0})
	if got, _ := js.counts("KV_" + granted); got != 7 {
		t.Fatalf("reads of the granted bucket over seven passes = %d, want 7", got)
	}
}

// A read refusal ends at the server's answer that the bucket is not there, as at any other answer
// to the read: the read is what was refused, and the server answered it. An update's refusal after
// that starts an episode of its own, and a refusal of the read while that episode runs, another
// request than the episode's, is logged again, naming the read.
func TestAReadRefusalEndsAtANotFoundAnswerAndARefusedUpdateIsLoggedAgain(t *testing.T) {
	const refused = "envoy_refused"
	stream := "KV_" + refused
	logs := captureLogs(t)
	js, clock := newAnsweringJetStream(), keeperTestClock()
	pass := keeperBucketPass(t, js, clock, refused)
	want := func(when string, reads, updates int, lines [4]int) {
		t.Helper()
		gotReads, gotUpdates := js.counts(stream)
		if gotReads != reads || gotUpdates != updates || keeperLines(t, logs) != lines {
			t.Fatalf("%s: reads %d, updates %d and lines (refusals, failures, grant, raises) %v, want %d, %d and %v",
				when, gotReads, gotUpdates, keeperLines(t, logs), reads, updates, lines)
		}
	}

	js.set(stream, 0, readRefusal(stream), nil)
	pass()
	want("after the read's refusal", 1, 0, [4]int{1, 0, 0, 0})

	js.set(stream, 0, nats.ErrStreamNotFound, nil)
	clock.Advance(keeperNoGrantBackoff)
	pass()
	want("after the answer that the bucket is not there", 2, 0, [4]int{1, 0, 1, 0})
	pass()
	want("on the pass after that answer", 3, 0, [4]int{1, 0, 1, 0})

	js.set(stream, 1, nil, updateRefusal(stream))
	pass()
	want("after the update's refusal", 4, 1, [4]int{2, 0, 1, 0})
	if text, _ := logs.recordsWithMsg(t, keeperGrantRefusedMsg)[1]["error"].(string); !strings.Contains(text, updateRefusal(stream).Error()) {
		t.Fatalf("the second refusal line names %q, want the update's refusal %q", text, updateRefusal(stream))
	}

	js.set(stream, 0, readRefusal(stream), nil)
	clock.Advance(keeperNoGrantBackoff)
	pass()
	want("after the read's refusal again", 5, 1, [4]int{3, 0, 1, 0})
}

// A refused update - a grant with the bucket's STREAM.INFO and without its STREAM.UPDATE - ends only
// at an update the server answers. A read that finds the bucket at its copies, after an operator's
// raise, sends no update and claims no grant, and neither does an answer that the bucket is not
// there; such a read holds nothing back either, so the next pass reads again; once the bucket is
// lowered the update is refused again without a line; and the update the server answers once the
// grant carries it logs the grant line and the raise.
func TestAnUpdateRefusalEndsOnlyAtAnAnsweredUpdate(t *testing.T) {
	const granted, refused = "envoy_granted", "envoy_refused"
	stream := "KV_" + refused
	logs := captureLogs(t)
	js, clock := newAnsweringJetStream(), keeperTestClock()
	pass := keeperBucketPass(t, js, clock, granted, refused)
	want := func(when string, reads, updates int, lines [4]int) {
		t.Helper()
		gotReads, gotUpdates := js.counts(stream)
		if gotReads != reads || gotUpdates != updates || keeperLines(t, logs) != lines {
			t.Fatalf("%s: reads %d, updates %d and lines (refusals, failures, grant, raises) %v, want %d, %d and %v",
				when, gotReads, gotUpdates, keeperLines(t, logs), reads, updates, lines)
		}
	}

	js.set(stream, 1, nil, updateRefusal(stream))
	pass()
	want("after the update's refusal", 1, 1, [4]int{1, 0, 0, 0})
	pass()
	want("on the pass after it, inside the backoff", 1, 1, [4]int{1, 0, 0, 0})

	js.set(stream, 3, nil, updateRefusal(stream))
	clock.Advance(keeperNoGrantBackoff)
	pass()
	want("after the operator's raise", 2, 1, [4]int{1, 0, 0, 0})
	pass()
	want("on the pass after the operator's raise", 3, 1, [4]int{1, 0, 0, 0})

	js.set(stream, 0, nats.ErrStreamNotFound, updateRefusal(stream))
	pass()
	want("after an answer that the bucket is not there", 4, 1, [4]int{1, 0, 0, 0})

	js.set(stream, 1, nil, updateRefusal(stream))
	pass()
	want("after the bucket was lowered again", 5, 2, [4]int{1, 0, 0, 0})
	pass()
	want("on the pass after that refusal", 5, 2, [4]int{1, 0, 0, 0})

	js.set(stream, 0, nil, nil)
	clock.Advance(keeperNoGrantBackoff)
	pass()
	want("after the update the server answered", 6, 3, [4]int{1, 0, 1, 1})
	if raised := logs.recordsWithMsg(t, keeperRaisedMsg)[0]; raised["bucket"] != refused || raised["replicas"] != "1 -> 3" {
		t.Fatalf("the raise line = %v, want %s raised 1 -> 3", raised, refused)
	}
	pass()
	want("on the pass after the raise", 7, 3, [4]int{1, 0, 1, 1})
	if got, _ := js.counts("KV_" + granted); got != 9 {
		t.Fatalf("reads of the granted bucket over nine passes = %d, want 9", got)
	}
}
