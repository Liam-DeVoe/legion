package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/id"
	"github.com/sjawhar/envoy/internal/testnats"
)

// A listener whose NATS user holds exactly the grant envoy-infra renders for its machine
// (testnats.ListenerGrant) takes its delivery lease, renews it, delivers a notification through
// the delivery ledger, records a registered session's last-seen time, and releases the lease on
// SIGTERM, and the server refuses it nothing. Each write to the listener state bucket is read
// back by key, a direct get on the key's own subject, which the grant allows: a read by revision
// is a direct get on the stream's bare subject, which it refuses, so the lease was never held and
// no last-seen record was written.
func TestAListenerUnderItsMachinesGrantHoldsTheLeaseAndRecordsLastSeen(t *testing.T) {
	const (
		machineID = "box-granted"
		instance  = "pod-granted"
		sessionID = "ses_granted"
		topic     = "notifications.github.acme.granted.pr.1"
	)
	seed, public := testnats.User(t)
	granted := testnats.StartGranted(t, testnats.GrantedUser{Public: public, Grant: testnats.ListenerGrant(t, machineID)})
	listener := startListenerProcess(t, cmdtest.Build(t, "envoy-listener"), granted.URL(), machineID,
		"NATS_NKEY_SEED="+seed, "ENVOY_INSTANCE_ID="+instance)
	listener.waitHealthy(t)

	admin, err := bus.Connect([]string{granted.AdminURL}, bus.WithName("grant-test-admin"))
	if err != nil {
		t.Fatalf("connect as the admin: %v", err)
	}
	t.Cleanup(admin.Close)
	state, err := bus.OpenKeyValue(t.Context(), admin.Conn, admin.JS(), bus.ListenerStateBucket)
	if err != nil {
		t.Fatalf("open the listener state bucket: %v", err)
	}
	leaseKey := "lease." + bus.ListenerDurable(machineID)
	readLeaseKey := func() (leaseRecord, natsgo.KeyValueEntry) {
		t.Helper()
		entry, err := state.Get(leaseKey)
		if err != nil {
			t.Fatalf("read %s: %v", leaseKey, err)
		}
		record, err := decodeLeaseRecord(entry.Value())
		if err != nil {
			t.Fatalf("decode %s %s: %v", leaseKey, entry.Value(), err)
		}
		return record, entry
	}
	held, taken := readLeaseKey()
	if held.Holder != instance || held.Released {
		t.Fatalf("%s = %+v, want held by %s", leaseKey, held, instance)
	}
	waitFor(t, 5*time.Second, "the holder to renew the lease", func() bool {
		record, entry := readLeaseKey()
		return record.Holder == instance && entry.Revision() > taken.Revision()
	})

	standIn := startSessionStandIn(t, granted.AdminURL, sessionID, answerDone)
	registerThroughListener(t, listener, subscribeBody{
		SessionID: sessionID, Topics: []string{topic}, Title: "granted", Dir: "/test/" + sessionID,
		Capabilities: []string{"btw"}, SelfSubscribed: true, Host: machineID,
		Delivery: &subscribeDelivery{Consumer: true, Epoch: "grant-" + id.New()},
	})
	publisher := connectPublisher(t, granted.AdminURL)
	item := newEnvelope("github", topic, "pull request 1 was opened")
	publish(t, publisher, item)
	if frame := standIn.collect(t, 1, 15*time.Second)[0]; frame.item.EventID != item.EventID {
		t.Fatalf("the frame relayed to %s carries event %s, want %s", sessionID, frame.item.EventID, item.EventID)
	}
	// The relay of a session's own consumer costs no ledger write: this listener hands it
	// nothing. The one delivery it makes itself is the role-lane forward, which records the pair
	// delivered once the stream stored the forward, so that is the `delivery.` key this grant must
	// allow.
	const role = "granted-keeper"
	if status, answer := callListener(t, listener.port, http.MethodPost, "/v1/roles/set",
		fmt.Sprintf(`{"session_id":%q,"role":%q}`, sessionID, role)); status != http.StatusOK {
		t.Fatalf("claim %s for %s: status %d %s", role, sessionID, status, answer)
	}
	publish(t, publisher, newEnvelope("dispatch", contracts.RoleTopicPrefix+role, "a role message for its holder"))
	ledger := "delivery." + bus.ListenerDurable(machineID) + "."
	waitFor(t, 15*time.Second, "the role forward to record its delivery in the ledger", func() bool {
		keys, err := state.Legacy().Keys()
		return err == nil && hasKeyWithPrefix(keys, ledger)
	})
	if _, err := state.Get("seen." + sessionID); err != nil {
		t.Fatalf("read %s's last-seen record: %v\n%s", sessionID, err, listener.Output.String())
	}

	listener.Terminate(t)
	listener.WaitExit(t, "SIGTERM")
	if released, _ := readLeaseKey(); !released.Released || released.Holder != instance {
		t.Fatalf("%s after SIGTERM = %+v, want released by %s", leaseKey, released, instance)
	}
	if log := granted.Log(); strings.Contains(log, "Violation") {
		t.Fatalf("the server refused the listener what its grant should allow:\n%s", log)
	}
}

// registerThroughListener registers body against a started listener process, as a session's
// plugin does: one POST of /v1/interests/subscribe, which must be answered 200.
func registerThroughListener(t *testing.T, listener *listenerProcess, body subscribeBody) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s's registration: %v", body.SessionID, err)
	}
	status, answer := callListener(t, listener.port, http.MethodPost, "/v1/interests/subscribe", string(encoded))
	if status != http.StatusOK {
		t.Fatalf("register %s: status %d %s", body.SessionID, status, answer)
	}
}

func hasKeyWithPrefix(keys []string, prefix string) bool {
	for _, key := range keys {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// One machine's grant writes its own delivery lease as the lease does (Create, a read back by key,
// Update) and its own clean-hand-back mark, and reads that mark back as the pod taking the record
// does; it is refused a write to another machine's lease key, hand-back mark or delivery ledger:
// no machine's user can take or clear another machine's lease, or vouch for another machine's
// hand-over.
func TestAMachinesGrantRefusesAnotherMachinesLeaseMarkAndLedger(t *testing.T) {
	seed, public := testnats.User(t)
	granted := testnats.StartGranted(t, testnats.GrantedUser{Public: public, Grant: testnats.ListenerGrant(t, "box-a")})
	errs := make(chan error, 8)
	conn, err := testnats.ConnectSeed(t, granted.URL(), seed,
		natsgo.ErrorHandler(func(_ *natsgo.Conn, _ *natsgo.Subscription, err error) { errs <- err }))
	if err != nil {
		t.Fatalf("connect as box-a's user: %v", err)
	}
	t.Cleanup(conn.Close)
	kv, err := openListenerState(t.Context(), conn, 1, 2*time.Second)
	if err != nil {
		t.Fatalf("open the listener state bucket under box-a's grant: %v", err)
	}

	own := "lease." + bus.ListenerDurable("box-a")
	taken, err := kv.Create(own, []byte(`{"holder":"pod-a","boot":"0123456789abcdef"}`))
	if err != nil {
		t.Fatalf("take box-a's lease: %v", err)
	}
	if entry, err := kv.Get(own); err != nil || entry.Revision() != taken {
		t.Fatalf("read back box-a's lease = %v, %v; want revision %d", entry, err, taken)
	}
	if _, err := kv.Update(own, []byte(`{"holder":"pod-a","boot":"0123456789abcdef","released":true}`), taken); err != nil {
		t.Fatalf("release box-a's lease: %v", err)
	}
	mark := bus.ListenerHandBackPrefix(bus.ListenerDurable("box-a")) + strconv.FormatUint(taken, 10)
	if _, err := kv.Put(mark, []byte(`{"instance":"pod-a","boot":"0123456789abcdef","revision":1}`)); err != nil {
		t.Fatalf("mark box-a's hand-back: %v", err)
	}
	if entry, err := kv.Get(mark); err != nil || len(entry.Value()) == 0 {
		t.Fatalf("read back box-a's hand-back mark = %v, %v; want the mark the stop wrote", entry, err)
	}
	select {
	case err := <-errs:
		t.Fatalf("the server refused box-a's own lease or mark: %v", err)
	default:
	}

	for _, refused := range []struct {
		key   string
		write func(key string) error
	}{
		{"lease." + bus.ListenerDurable("box-b"), func(key string) error {
			_, err := kv.Create(key, []byte(`{"holder":"pod-a","boot":"0123456789abcdef"}`))
			return err
		}},
		{bus.ListenerHandBackPrefix(bus.ListenerDurable("box-b")) + "7", func(key string) error {
			_, err := kv.Put(key, []byte(`{"instance":"pod-a","boot":"0123456789abcdef","revision":7}`))
			return err
		}},
		{"delivery." + bus.ListenerDurable("box-b") + ".0123456789abcdef0123456789abcdef.ses_x", func(key string) error {
			_, err := kv.Put(key, []byte(`{"state":"delivered"}`))
			return err
		}},
	} {
		if err := refused.write(refused.key); err == nil {
			t.Fatalf("box-a's user wrote %s", refused.key)
		}
		subject := "$KV." + bus.ListenerStateBucket + "." + refused.key
		select {
		case err := <-errs:
			if !errors.Is(err, natsgo.ErrPermissionViolation) || !strings.Contains(err.Error(), `Publish to "`+subject+`"`) {
				t.Fatalf("the server's answer to the write of %s: %v, want its refusal", refused.key, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the server did not refuse box-a's write of %s", refused.key)
		}
	}
}

// A listener whose grant lacks the create of its own durable consumer never receives: it takes
// the machine's lease, its create is refused, and it delivers nothing. It stays up and tries the
// create again, naming the refusal at ERROR on every try (durableGrantRefusedLine), with the
// refused subject and the server's own text, and from the first refused try on /healthz answers
// 503 and /livez 204: unready while the refusal lasts, and never restarted for it, so it recovers
// at its next try once the grant is fixed. The infra live row
// TestHostPermissionsOnRealServers/*/without_its_durable's_consumer_create_the_listener_never_reaches_healthy
// watches it the same way for a minute. Before, #4343's start exited 1 at the refusal, and before
// that a holder answered 200 "degraded" while it retried, which kept it ready.
func TestAListenerWhoseGrantRefusesItsDurablesCreateStaysLiveAndUnready(t *testing.T) {
	t.Parallel()
	const (
		machineID = "box-no-create"
		instance  = "pod-a"
		// liveHealthWindow is how long packages/envoy/infra's live row watches this answer.
		liveHealthWindow = 60 * time.Second
	)
	seed, public := testnats.User(t)
	grant := testnats.ListenerGrant(t, machineID)
	// Three entries of the listener's grant admit the create of its own durable: the durable's own
	// subject, and the session consumers' two wildcards, which reach every consumer of the stream
	// (infra/core sessionConsumerCalls). All three go, or the create the test refuses still stands.
	durableCreate := "$JS.API.CONSUMER.CREATE." + bus.Stream + "." + bus.ListenerDurable(machineID)
	refused := []string{durableCreate, "$JS.API.CONSUMER.CREATE." + bus.Stream + ".*", "$JS.API.CONSUMER.CREATE." + bus.Stream + ".*.>"}
	held := len(grant.Publish.Allow)
	grant.Publish.Allow = slices.DeleteFunc(grant.Publish.Allow, func(subject string) bool { return slices.Contains(refused, subject) })
	if len(grant.Publish.Allow) != held-len(refused) {
		t.Fatalf("the listener's grant does not hold each of %q exactly once: %q", refused, grant.Publish.Allow)
	}
	granted := testnats.StartGranted(t, testnats.GrantedUser{Public: public, Grant: grant})
	listener := startListenerProcess(t, cmdtest.Build(t, "envoy-listener"), granted.URL(), machineID,
		"NATS_NKEY_SEED="+seed, "ENVOY_INSTANCE_ID="+instance)

	refusals := func() int {
		return countRecords(listener, durableGrantRefusedLine, func(record map[string]any) bool {
			return record["level"] == "ERROR" && strings.HasPrefix(fmt.Sprint(record["subject"]), "$JS.API.CONSUMER.CREATE."+bus.Stream+".") &&
				strings.Contains(fmt.Sprint(record["error"]), `Permissions Violation for Publish to "`+fmt.Sprint(record["subject"])+`"`)
		})
	}
	waitFor(t, 30*time.Second, "the listener to name its refused durable create at ERROR", func() bool { return refusals() > 0 })
	for deadline := time.Now().Add(liveHealthWindow); time.Now().Before(deadline); time.Sleep(time.Second) {
		select {
		case <-listener.Exited:
			t.Fatalf("the listener exited at its refused durable create:\n%s", listener.Output.String())
		default:
		}
		if status, body := callListener(t, listener.port, http.MethodGet, "/healthz", ""); status == http.StatusOK {
			t.Fatalf("/healthz answered %d %s while the grant refused the durable's create, want it unready:\n%s",
				status, body, listener.Output.String())
		}
		if status, body := callListener(t, listener.port, http.MethodGet, "/livez", ""); status != http.StatusNoContent {
			t.Fatalf("/livez answered %d %s while the grant refused the durable's create, want 204:\n%s",
				status, body, listener.Output.String())
		}
	}
	if got := refusals(); got < 2 {
		t.Fatalf("the listener named the refusal %d times in %s, want it named again at each try:\n%s", got, liveHealthWindow, listener.Output.String())
	}
	for _, line := range []string{durableRefusedLine, "durable consumer read failed; retrying"} {
		if strings.Contains(listener.Output.String(), line) {
			t.Fatalf("the listener logged %q for a refusal of its grant, want only %q:\n%s", line, durableGrantRefusedLine, listener.Output.String())
		}
	}
}

// A session relay's reply subscription, _INBOX.<id>.>, that the server refuses follows the rule a
// refused durable follows (TestAListenerWhoseGrantRefusesItsDurablesCreateStaysLiveAndUnready):
// the listener stays up and not ready, /livez 204 and /healthz 503 "starting", and opens the
// session consumers' connection and the subscription again (openStartConnection), naming the
// refusal at ERROR at each try (relayRepliesRefusedLine) with the refused subject and the
// server's text, after a wait of 1 s and then 2 s (nextRefusedWait); once the subscription is let
// through, a later try opens and the listener is ready. No grant of a real server refuses that
// subscription and keeps _INBOX.<id>.*, the one every request's replies come on, since the server
// matches a subscription's > as a token any * of the grant takes; so a relay (startGate) answers
// the session consumers' subscription as a server whose grant does not carry it answers, with its
// violation. Before, the start read no refusal of the subscription and served with a relay no
// reply reached.
func TestAListenerWhoseRelaysReplySubscriptionIsRefusedNamesItAtEachTryAndStaysUnready(t *testing.T) {
	t.Parallel()
	gate := startStartGate(t, testnats.URL(t))
	gate.refuseSubscriptions("envoy-session-consumers", func(subject string) bool {
		return strings.HasPrefix(subject, "_INBOX.") && strings.HasSuffix(subject, ".>")
	})
	listener := startListenerProcess(t, cmdtest.Build(t, "envoy-listener"), gate.url, "box-no-replies", "ENVOY_INSTANCE_ID=pod-a")
	const while = "its relay's reply subscription was refused"
	refusals := func() []map[string]any {
		var refused []map[string]any
		for _, record := range linesIn(listener.Output.String(), relayRepliesRefusedLine, map[string]any{"level": "ERROR"}) {
			subject, _ := record["subject"].(string)
			text, _ := record["error"].(string)
			if strings.HasPrefix(subject, "_INBOX.") && strings.HasSuffix(subject, ".>") &&
				strings.Contains(text, `Permissions Violation for Subscription to "`+subject+`"`) {
				refused = append(refused, record)
			}
		}
		return refused
	}
	for deadline := time.Now().Add(30 * time.Second); len(refusals()) < 2; time.Sleep(200 * time.Millisecond) {
		requireLiveAndStarting(t, listener, while)
		if time.Now().After(deadline) {
			t.Fatalf("the listener named the refused reply subscription %d times at ERROR within 30 s, want it named at each try:\n%s",
				len(refusals()), listener.Output.String())
		}
	}
	for index, wait := range []string{"1s", "2s"} {
		if record := refusals()[index]; record["retry_in"] != wait {
			t.Fatalf("refusal %d = %v, want retry_in %s", index+1, record, wait)
		}
	}
	requireLiveAndStarting(t, listener, while)
	gate.refuseSubscriptions("envoy-session-consumers", nil)
	listener.waitHealthy(t)
}
