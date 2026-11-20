package testnats

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// The package's tests share servers, and each gets one as a fresh container was: a stream and its
// messages left by one test are not there when the next asks for a server, which hands it an
// account of its own.
func TestEachTestGetsTheSharedServerEmpty(t *testing.T) {
	t.Run("leaves a stream and a message", func(t *testing.T) {
		conn := Connect(t, URL(t))
		defer conn.Close()
		js, err := conn.JetStream()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := js.AddStream(&natsgo.StreamConfig{Name: "LEFT_BEHIND", Subjects: []string{"left.>"}}); err != nil {
			t.Fatalf("add stream: %v", err)
		}
		if _, err := js.Publish("left.one", []byte("x")); err != nil {
			t.Fatalf("publish: %v", err)
		}
	})
	t.Run("finds none of it", func(t *testing.T) {
		conn := Connect(t, URL(t))
		defer conn.Close()
		js, err := conn.JetStream()
		if err != nil {
			t.Fatal(err)
		}
		info, err := js.AccountInfo()
		if err != nil {
			t.Fatalf("account info: %v", err)
		}
		if info.Streams != 0 {
			t.Errorf("the second subtest's account holds %d streams, want none of the first's", info.Streams)
		}
	})
}

// A test that publishes a burst without waiting for acks leaves messages the server routes after
// the test ends. They land in that test's own account, so the next test, creating a stream on the
// same subjects, finds none of them.
func TestABurstLeftInFlightDoesNotReachTheNextTest(t *testing.T) {
	const burst = 20000
	config := &natsgo.StreamConfig{Name: "BURST", Subjects: []string{"burst.>"}}
	t.Run("publishes a burst and ends", func(t *testing.T) {
		conn := Connect(t, URL(t))
		defer conn.Close()
		js, err := conn.JetStream()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := js.AddStream(config); err != nil {
			t.Fatalf("add stream: %v", err)
		}
		payload := []byte(strings.Repeat("x", 64))
		for i := range burst {
			if err := conn.Publish(fmt.Sprintf("burst.%d", i%16), payload); err != nil {
				t.Fatalf("publish %d: %v", i, err)
			}
		}
	})
	t.Run("recreates the stream and finds none of it", func(t *testing.T) {
		conn := Connect(t, URL(t))
		defer conn.Close()
		js, err := conn.JetStream()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := js.AddStream(config); err != nil {
			t.Fatalf("add stream: %v", err)
		}
		time.Sleep(time.Second)
		info, err := js.StreamInfo(config.Name)
		if err != nil {
			t.Fatalf("stream info: %v", err)
		}
		if info.State.Msgs != 0 {
			t.Errorf("the recreated stream holds %d of the previous test's %d messages", info.State.Msgs, burst)
		}
	})
}

// A test gets one account however often it asks, so every connection it makes reaches the streams
// any other made; a subtest gets an account of its own, where none of its parent's streams is.
func TestATestGetsOneAccountAndEachSubtestItsOwn(t *testing.T) {
	uri := URL(t)
	if again := URL(t); again != uri {
		t.Fatalf("a second URL for the same test is %q, want its first, %q", again, uri)
	}
	conn := Connect(t, uri)
	defer conn.Close()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&natsgo.StreamConfig{Name: "PARENT", Subjects: []string{"parent.>"}}); err != nil {
		t.Fatalf("add stream: %v", err)
	}
	t.Run("subtest", func(t *testing.T) {
		subURI := URL(t)
		if subURI == uri {
			t.Fatalf("the subtest got its parent's account, %q", uri)
		}
		sub := Connect(t, subURI)
		defer sub.Close()
		subJS, err := sub.JetStream()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := subJS.StreamInfo("PARENT"); !errors.Is(err, natsgo.ErrStreamNotFound) {
			t.Fatalf("the subtest's account answers its parent's stream with %v, want not found", err)
		}
	})
}

// namedTB is a test under another name, so one test can ask a server for accounts as many tests.
type namedTB struct {
	testing.TB
	name string
}

func (n namedTB) Name() string { return n.name }

// ownAccountServer returns an account server of the test's own, whose servers declare accounts
// accounts each and are shut down and removed when the test ends.
func ownAccountServer(t *testing.T, accounts int) *accountServer {
	t.Helper()
	return newAccountServer(t, accounts)
}

// A server whose accounts are all handed out hands out no more: the next test's account is the
// first of a new server, and an account a test is still using keeps its connection and its streams.
func TestTheAccountPastAServersLastIsTheFirstOfANewServer(t *testing.T) {
	server := ownAccountServer(t, 2)
	live := server.url(namedTB{t, "live"})
	conn := Connect(t, live)
	defer conn.Close()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	kv, err := js.CreateKeyValue(&natsgo.KeyValueConfig{Bucket: "live"})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	second := server.url(namedTB{t, "second"})
	third := server.url(namedTB{t, "third"})
	host := func(uri string) string {
		parsed, err := url.Parse(uri)
		if err != nil {
			t.Fatal(err)
		}
		return parsed.Host
	}
	if want := fmt.Sprintf("nats://t2:t2@%s", host(live)); second != want {
		t.Fatalf("the second test's URL is %q, want the first server's second account, %q", second, want)
	}
	if host(third) == host(live) || third != fmt.Sprintf("nats://t1:t1@%s", host(third)) {
		t.Fatalf("the third test's URL is %q, want the first account of a server other than the full one at %s", third, host(live))
	}
	if _, err := kv.Put("after-the-third", []byte("1")); err != nil {
		t.Fatalf("put after the third account was handed out: %v", err)
	}
	if status := conn.Status(); status != natsgo.CONNECTED {
		t.Fatalf("the connection of an account in use is %v after the third account was handed out, want CONNECTED", status)
	}
}

// A start of a server that fails fails the test that asked for it with the server's reason, and
// every test that asks the account server after it at once, rather than each waiting out a start of
// its own: a start that cannot succeed would otherwise cost every test of the package its
// connectTimeout. A flag nats-server does not know makes the start fail.
func TestAFailedStartFailsEveryLaterTestAtOnce(t *testing.T) {
	server := ownAccountServer(t, 1)
	server.args = []string{"-no-such-flag"}
	ask := func(name string) string {
		asking := &fatalRecorder{TB: namedTB{t, name}}
		done := make(chan struct{})
		go func() {
			defer close(done)
			server.url(asking)
		}()
		<-done
		return asking.message
	}
	if first := ask("first"); !strings.Contains(first, "read the NATS configuration") {
		t.Fatalf("the first test's start ended with %q, want the server's refusal of the flag", first)
	}
	if second := ask("second"); !strings.Contains(second, "last start of a NATS server failed") {
		t.Fatalf("the second test ended with %q, want the earlier start's failure rather than a start of its own", second)
	}
}

// Reload refuses a server an account server started, whose accounts other tests hold, and fails
// the test that asked before it touches the server.
func TestReloadRefusesAServerTestsShare(t *testing.T) {
	server := ownAccountServer(t, 1)
	Connect(t, server.url(namedTB{t, "holder"})).Close()
	refused := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.newest.Reload(refused, accountsConfig(2))
	}()
	<-done
	if !strings.Contains(refused.message, "Reload of a server the package's tests share") {
		t.Fatalf("the reload of a shared server ended with %q, want its refusal", refused.message)
	}
	if reloads := strings.Count(server.newest.Log(), "Reloaded server configuration"); reloads != 0 {
		t.Fatalf("the shared server was reloaded %d times", reloads)
	}
}

// An account server whose accounts are all handed out hands out the next while a stream in an
// account another test holds has just served a direct get, as a KV get is, and reloads no server
// to do it. nats-server v2.15.1 reads its options without their lock as it serves a direct get
// (getDirectRequest, server/stream.go:6365), and a reload replaces them under that lock (setOpts,
// server/server.go:1224): so a reload of a server other tests use meets their direct gets. The
// race detector reports that pair in some runs and not in others, so the test reads the server's
// log for the reload itself.
func TestTheNextAccountMeetsNoStreamThatServedADirectGet(t *testing.T) {
	server := ownAccountServer(t, 1)
	uri := server.url(namedTB{t, "serving"})
	conn := Connect(t, uri)
	defer conn.Close()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	kv, err := js.CreateKeyValue(&natsgo.KeyValueConfig{Bucket: "served"})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := kv.Put("key", []byte("1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := kv.Get("key"); err != nil {
		t.Fatalf("the direct get: %v", err)
	}
	server.url(namedTB{t, "next"})
	if _, err := kv.Get("key"); err != nil {
		t.Fatalf("the direct get after the next account was handed out: %v", err)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	serversMu.Lock()
	serving := servers[parsed.Host]
	serversMu.Unlock()
	if reloads := strings.Count(serving.Log(), "Reloaded server configuration"); reloads != 0 {
		t.Errorf("the server holding the serving test's account was reloaded %d times to hand out the next account", reloads)
	}
}
