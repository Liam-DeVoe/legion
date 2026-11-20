package testnats

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

func TestMain(m *testing.M) { os.Exit(Main(m)) }

// This protects the rig's readiness boundary: a server can accept NATS's
// protocol handshake while JetStream is still starting, so a KV test that returns at the handshake
// races CreateKeyValue. The helper must retry the JetStream operation itself.
func TestWaitForJetStreamRetriesTheOperationUntilItIsReady(t *testing.T) {
	attempts := 0
	waitForJetStream(t, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("JetStream is starting")
		}
		return nil
	})
	if attempts != 3 {
		t.Fatalf("JetStream readiness attempts = %d, want 3", attempts)
	}
}

// A create right after a stream delete can meet nats-server still removing the account's streams
// directory, and is answered with the store failure; RecreateKeyValue tries it again. Every other
// answer is the test's own failure and must not be retried into a slower one or a pass.
func TestRetryStreamStoreFailureRetriesOnlyTheStoreFailure(t *testing.T) {
	storeFailure := &natsgo.APIError{Code: 500, ErrorCode: streamCreateErrorCode, Description: streamStoreFailed}
	t.Run("store failure", func(t *testing.T) {
		attempts := 0
		err := retryStreamStoreFailure(func() error {
			attempts++
			if attempts < 3 {
				return storeFailure
			}
			return nil
		})
		if err != nil || attempts != 3 {
			t.Fatalf("err = %v after %d attempts, want nil after 3", err, attempts)
		}
	})
	t.Run("any other error", func(t *testing.T) {
		other := &natsgo.APIError{Code: 500, ErrorCode: streamCreateErrorCode, Description: "insufficient storage resources available"}
		attempts := 0
		err := retryStreamStoreFailure(func() error {
			attempts++
			return other
		})
		if err != other || attempts != 1 {
			t.Fatalf("err = %v after %d attempts, want %v after 1", err, attempts, other)
		}
	})
}

// fatalRecorder is t with Fatal recorded and the goroutine ended, as t.Fatal does.
type fatalRecorder struct {
	testing.TB
	message string
}

func (f *fatalRecorder) Fatal(args ...any) { f.message = fmt.Sprint(args...); runtime.Goexit() }
func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// reloadGetters is how many connections through the relay
// TestAReloadWithDirectGetsInFlightOnTheSameServerIsNoRace runs direct gets on, one get at a time
// on each in a loop, and reloadRounds how many reloads it makes under them.
const (
	reloadGetters = 4
	reloadRounds  = 50
)

// A test that reloads its own server while its clients' direct gets, as KV gets are, are in flight
// through the relay meets no data race. nats-server v2.15.1 reads its options without their lock as
// it serves a direct get (getDirectRequest, server/stream.go:6365) and the reload replaces them
// under that lock (setOpts, server/server.go:1224). A get the server has read when the relay holds
// the clients is still being served; the barrier waits for it, and only the lock step after the
// barrier orders it before the reload in locks the race detector sees (Server.Reload). With the lock
// step before the hold, between the hold and the barrier, after the reload, or gone, the race
// detector reports the pair and fails this test. Each get counts whether a reload began while it
// was outstanding, so a run in which no get was in flight at a reload fails rather than passing on
// a case it never made.
func TestAReloadWithDirectGetsInFlightOnTheSameServerIsNoRace(t *testing.T) {
	server, uri := Start(t)
	setup := Connect(t, uri)
	defer setup.Close()
	js, err := setup.JetStream()
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
	var begun, served, inFlight atomic.Int64
	stop := make(chan struct{})
	failures := make(chan error, reloadGetters)
	var getters sync.WaitGroup
	var ending sync.Once
	end := func() {
		ending.Do(func() {
			close(stop)
			getters.Wait()
		})
	}
	defer end()
	for range reloadGetters {
		conn := Connect(t, uri)
		t.Cleanup(conn.Close)
		connJS, err := conn.JetStream()
		if err != nil {
			t.Fatal(err)
		}
		connKV, err := connJS.KeyValue("served")
		if err != nil {
			t.Fatalf("open the bucket on a getter's connection: %v", err)
		}
		getters.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				before := begun.Load()
				if _, err := connKV.Get("key"); err != nil {
					failures <- err
					return
				}
				served.Add(1)
				if begun.Load() != before {
					inFlight.Add(1)
				}
			}
		})
	}
	for round := range reloadRounds {
		begun.Add(1)
		if err := server.Reload(t, hostOnlyConfig); err != nil {
			t.Fatalf("the reload of round %d: %v", round, err)
		}
	}
	end()
	close(failures)
	for err := range failures {
		t.Errorf("a direct get across the reloads failed: %v", err)
	}
	if inFlight.Load() == 0 {
		t.Fatalf("no direct get was in flight when a reload began (%d gets across %d reloads), so this test says nothing about the order", served.Load(), reloadRounds)
	}
	t.Logf("%d direct gets across %d reloads, %d of them in flight when a reload began", served.Load(), reloadRounds, inFlight.Load())
}

// StartNkeyAuthorized's readiness wait refuses a server that admits a client with no credential:
// such a server enforces no nkey users, and every refusal a test expects of it would pass for the
// wrong reason. A server started with no configuration, which asks for no credential, stands in
// for one.
func TestTheNkeyReadinessWaitRefusesAServerThatAdmitsAnyone(t *testing.T) {
	_, uri := Start(t)
	wait := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		awaitAuth(wait, uri, false)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the readiness wait is still waiting on a server that admits anyone")
	}
	if want := "admitted a client with no credential"; !strings.Contains(wait.message, want) {
		t.Errorf("the readiness wait ended with %q, want it to say %q", wait.message, want)
	}
}

// A restart keeps its server's URL even when something else takes the URL's port while the server is
// stopped, as another test's server or a free-port pick can on a busy host.
func TestARestartKeepsItsURLWhenThePortIsContendedWhileStopped(t *testing.T) {
	server, uri := Start(t)
	server.Stop(t)
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse %s: %v", uri, err)
	}
	if taken, err := net.Listen("tcp", u.Host); err == nil {
		t.Cleanup(func() { _ = taken.Close() })
	}
	server.Start(t)
	Connect(t, uri).Close()
}

// Server.Log is everything the server has said, which a test reads to see the server's own account
// of what it did: a server stopped and started again says it is ready twice, once for each server
// that ran behind the one URL.
func TestTheLogHoldsWhatTheServerSaidAcrossARestart(t *testing.T) {
	server, _ := Start(t)
	server.Stop(t)
	server.Start(t)
	if ready := strings.Count(server.Log(), "Server is ready"); ready != 2 {
		t.Errorf("the server said it was ready %d times across a stop and a start, want twice:\n%s", ready, server.Log())
	}
}

// A gate's port must lie below the machine's ephemeral range, where no :0 bind can take it while
// the gate is closed. The range is fed in as text, each kernel's answer in turn, so the band is
// pinned whatever range the machine running this test has: a CI runner's default range would hide
// a band that lands inside the 16000-65535 a Legion worker pod (gVisor) has.
func TestTheGateBandLiesBelowTheEphemeralRangeItIsGiven(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		low   int
		first int
	}{
		{name: "gVisor's range in a Legion worker pod", text: "16000\t65535\n", low: 16000, first: 15000},
		{name: "Linux's default range", text: "32768\t60999\n", low: 32768, first: 31768},
		{name: "text that is no range", text: "not a range\n", low: 16000, first: 15000},
		{name: "empty text", text: "", low: 16000, first: 15000},
		{name: "the lowest range that leaves a band", text: "3048\t65535\n", low: 3048, first: 2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, count, err := gatePorts(tc.text)
			if err != nil {
				t.Fatalf("gatePorts(%q) = %v, want a band", tc.text, err)
			}
			if first != tc.first || count != 1000 {
				t.Errorf("gatePorts(%q) = %d ports from %d, want 1000 from %d", tc.text, count, first, tc.first)
			}
			if last := first + count - 1; last >= tc.low {
				t.Errorf("gatePorts(%q) = %d-%d, which reaches the range starting at %d", tc.text, first, last, tc.low)
			}
		})
	}
	t.Run("a range too low to leave a band", func(t *testing.T) {
		first, count, err := gatePorts("1024\t65535\n")
		if err == nil {
			t.Fatalf("gatePorts = %d ports from %d, want a refusal", count, first)
		}
		if !strings.Contains(err.Error(), "1024") {
			t.Errorf("refusal %v does not name the range's first port, 1024", err)
		}
	})
}

// StartGate takes its port from just below this machine's ephemeral range, which it reads from
// ephemeralRangePath. The test reads that file itself, as the rig does, and checks the ports of 20
// gates against it: a StartGate that stopped reading the file and took a band of its own fails
// here on every machine whose range does not start where that band ends.
func TestEveryGatePortLiesJustBelowThisMachinesEphemeralRange(t *testing.T) {
	low := fallbackEphemeralLow
	text, err := os.ReadFile(ephemeralRangePath)
	if fields := strings.Fields(string(text)); err == nil && len(fields) == 2 {
		first, firstErr := strconv.Atoi(fields[0])
		last, lastErr := strconv.Atoi(fields[1])
		if firstErr == nil && lastErr == nil && 0 < first && first <= last && last <= 65535 {
			low = first
		}
	}
	server, _ := Start(t)
	for range 20 {
		gate := StartGate(t, server)
		address, err := url.Parse(gate.URL())
		if err != nil {
			t.Fatalf("parse the gate's URL %s: %v", gate.URL(), err)
		}
		port, err := strconv.Atoi(address.Port())
		if err != nil {
			t.Fatalf("read the port of the gate's URL %s: %v", gate.URL(), err)
		}
		if port >= low || port < low-gatePortCount {
			t.Errorf("a gate took port %d, want one of the %d ports below %d, the first port of this machine's ephemeral range (%s holds %q)", port, gatePortCount, low, ephemeralRangePath, text)
		}
	}
}

// A closed gate keeps its port. While it is closed a dial of its address is refused, as a client's
// failed reconnect needs, a program that binds the port without SO_REUSEADDR is refused, and no
// other gate can take the port; Open then listens on the same address again and a client connects
// through it. Each row is the control of one hold: with the reservation gone the bind succeeds and
// Open fails, with the claim gone the second gate's hold succeeds, and with the reservation
// listening the dial succeeds.
func TestAClosedGateKeepsItsPort(t *testing.T) {
	server, _ := Start(t)
	gate := StartGate(t, server)
	address, err := net.ResolveTCPAddr("tcp", gate.address)
	if err != nil {
		t.Fatalf("resolve the gate's address %s: %v", gate.address, err)
	}
	gate.Close()

	conn, err := net.DialTimeout("tcp", gate.address, 5*time.Second)
	if err == nil {
		_ = conn.Close()
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("a dial of the closed gate at %s: %v, want connection refused", gate.address, err)
	}

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		t.Fatalf("open the other program's socket: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	err = syscall.Bind(fd, &syscall.SockaddrInet4{Port: address.Port, Addr: [4]byte{127, 0, 0, 1}})
	if err == nil {
		// The port was free: hold it listening, as a program that took it would, so the Open below
		// meets it.
		_ = syscall.Listen(fd, 16)
		t.Errorf("another program bound the closed gate's port %d", address.Port)
	} else if !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("another program's bind of the closed gate's port %d: %v, want address already in use", address.Port, err)
	}

	if claim, reservation, err := holdGatePort(address.Port); err == nil {
		_ = syscall.Close(reservation)
		_ = claim.Close()
		t.Errorf("a second gate held the closed gate's port %d", address.Port)
	}

	gate.Open()
	nc, err := natsgo.Connect(gate.URL(), natsgo.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect through the reopened gate at %s: %v", gate.URL(), err)
	}
	nc.Close()
}

// Terminate ends the server for good and gives up the port its URL named, so nothing answers there
// any more; the cleanup that runs at the test's end finds the work done and does it no second time.
func TestTerminateEndsTheServerAndGivesUpItsPort(t *testing.T) {
	server, uri := Start(t)
	Connect(t, uri).Close()
	server.Terminate(t)
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse %s: %v", uri, err)
	}
	if conn, err := net.DialTimeout("tcp", u.Host, dialTimeout); err == nil {
		conn.Close()
		t.Fatalf("%s still answers after the server was terminated", u.Host)
	}
}

// MonitorURL is where the server's /varz and /connz are, which a test reads to see what the server
// makes of its own state.
func TestTheMonitorURLAnswersVarz(t *testing.T) {
	server, _ := Start(t)
	response, err := http.Get(server.MonitorURL() + "/varz")
	if err != nil {
		t.Fatalf("GET %s/varz: %v", server.MonitorURL(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("GET %s/varz: %s, want 200 OK", server.MonitorURL(), response.Status)
	}
}

// A second Grant on one server judges the outcome of its own reload, not the first Grant's: a
// second grant the server refuses fails the test with the server's reason for that grant, which
// comes back from that Grant's own reload.
func TestASecondGrantWaitsForItsOwnReload(t *testing.T) {
	_, public := User(t)
	grant := StartNkeyGranted(t, public, "first.>")
	grant.Grant(t, "second.>")

	refused := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		grant.Grant(refused, "not..a.subject")
	}()
	<-done
	if !strings.Contains(refused.message, "NATS refused the new grant") ||
		!strings.Contains(refused.message, `subject "not..a.subject" is not a valid subject`) {
		t.Fatalf("a second Grant the server refused ended with %q, want the server's refusal", refused.message)
	}
}

// Server.Reload answers with the server's own reason for a configuration the server will not take,
// and the server goes on serving the configuration it had: its user is still admitted and a client
// with no credential still refused.
func TestARefusedReloadAnswersWithTheServersReasonAndKeepsTheOldConfiguration(t *testing.T) {
	seed, public := User(t)
	server, uri := StartConfig(t, publishGrantConfig(public, []string{"first.>"}), []string{"-js"}, false)

	err := server.Reload(t, publishGrantConfig(public, []string{"not..a.subject"}))
	if err == nil {
		t.Fatal("the server took a configuration granting an invalid subject")
	}
	if want := `subject "not..a.subject" is not a valid subject`; !strings.Contains(err.Error(), want) {
		t.Fatalf("the refusal is %v, want it to say %s", err, want)
	}

	conn, err := ConnectSeed(t, uri, seed)
	if err != nil {
		t.Fatalf("the user of the configuration the server kept was refused: %v", err)
	}
	defer conn.Close()
	if err := conn.Publish("first.one", nil); err != nil {
		t.Fatalf("publish to the subject the kept grant allows: %v", err)
	}
	if _, err := ConnectSeed(t, uri, ""); !errors.Is(err, natsgo.ErrAuthorization) {
		t.Fatalf("a client with no credential after the refused reload: %v, want %v", err, natsgo.ErrAuthorization)
	}
}

// reloadRequesters is how many requesters TestAReloadUnderJetStreamRequestsAnswersEveryOne runs on
// each of reloadConnections connections, each keeping one JetStream request in flight, and
// reloadCount how many reloads it makes under them.
const (
	reloadConnections = 4
	reloadRequesters  = 8
	reloadCount       = 200
)

// A reload under clients' JetStream requests answers every request and keeps every connection, and
// under the race detector it is no data race. nats-server writes each service import's account on
// an authorization reload and reads it without the lock while it sets up the response to a request
// through that import (Server.Reload), so Reload holds the clients' messages until the server has
// processed the ones it has, and lets them through once it has reloaded. Before Reload held them,
// this test's reloads drew that race report in each of twenty runs under -race, and so did a Reload
// that held the clients without waiting for the server to finish what it had; each reload here
// changes the user's grant, so each one runs the server's authorization reload, and the server's
// log says so.
func TestAReloadUnderJetStreamRequestsAnswersEveryOne(t *testing.T) {
	seed, public := User(t)
	grant := func(n int) string {
		return publishGrantConfig(public, []string{"$JS.API.>", "_INBOX.>", fmt.Sprintf("reload%d.>", n)})
	}
	server, uri := StartConfig(t, grant(0), []string{"-js"}, false)
	stop := make(chan struct{})
	var answered atomic.Int64
	failures := make(chan error, reloadConnections*reloadRequesters)
	var requesters sync.WaitGroup
	for range reloadConnections {
		conn, err := ConnectSeed(t, uri, seed)
		if err != nil {
			t.Fatalf("connect as the user: %v", err)
		}
		t.Cleanup(conn.Close)
		for range reloadRequesters {
			requesters.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					if _, err := conn.Request("$JS.API.INFO", nil, 5*time.Second); err != nil {
						failures <- err
						return
					}
					answered.Add(1)
				}
			})
		}
	}
	for n := 1; n <= reloadCount; n++ {
		if err := server.Reload(t, grant(n)); err != nil {
			t.Fatalf("reload %d: %v", n, err)
		}
	}
	close(stop)
	requesters.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("a request across the reloads failed: %v", err)
	}
	if answered.Load() == 0 {
		t.Fatal("no request was answered; this test says nothing about a reload under requests")
	}
	if reloads := strings.Count(server.Log(), "Reloaded: authorization nkey users"); reloads != reloadCount {
		t.Fatalf("the server reloaded its nkey users %d times, want %d: a reload that changed no grant tests nothing", reloads, reloadCount)
	}
	t.Logf("%d requests answered across %d reloads", answered.Load(), reloadCount)
}

// nats-server takes in a message whose Nats-Trace-Only header asks for a trace alone and whose
// Nats-Trace-Dest is not a subject, and drops it without processing or counting it (msgtrace.go
// 470-475 and parser.go 512-538 in 2.15.1: no in_msgs for it in /connz). A Reload after one still
// reloads: the hold waits for the server's answer to the relay's PING, which the server sends once
// it has read everything before it, and not for a count of messages the server never reaches.
func TestAReloadAfterATraceOnlyMessageTheServerDropsStillReloads(t *testing.T) {
	seed, public := User(t)
	grant := func(subjects ...string) string { return publishGrantConfig(public, subjects) }
	server, uri := StartConfig(t, grant("traced.>"), []string{"-js"}, false)
	conn, err := ConnectSeed(t, uri, seed)
	if err != nil {
		t.Fatalf("connect as the user: %v", err)
	}
	t.Cleanup(conn.Close)
	message := natsgo.NewMsg("traced.one")
	message.Header.Set("Nats-Trace-Dest", "not..a.subject")
	message.Header.Set("Nats-Trace-Only", "true")
	if err := conn.PublishMsg(message); err != nil {
		t.Fatalf("publish the trace-only message: %v", err)
	}
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush the trace-only message: %v", err)
	}
	if !strings.Contains(server.Log(), `Destination "not..a.subject" is not valid`) {
		t.Fatalf("the server did not drop the trace-only message, so this test says nothing about one:\n%s", server.Log())
	}
	if err := server.Reload(t, grant("traced.>", "more.>")); err != nil {
		t.Fatalf("reload after the trace-only message: %v", err)
	}
	if reloads := strings.Count(server.Log(), "Reloaded: authorization nkey users"); reloads != 1 {
		t.Fatalf("the server reloaded its nkey users %d times, want 1", reloads)
	}
	if _, err := conn.Request("more.echo", nil, 5*time.Second); !errors.Is(err, natsgo.ErrNoResponders) {
		t.Fatalf("a request on the subject the reload granted: %v, want %v from the server that took the grant", err, natsgo.ErrNoResponders)
	}
}

// A Restart serves its new configuration under the same URL: the user the first configuration lets
// subscribe to a subject is refused it once the server restarts on one that does not, and has it
// again once the server restarts on the first.
func TestARestartServesItsNewConfiguration(t *testing.T) {
	seed, public := User(t)
	granted := SubscribeGrantConfig(public, "granted.>")
	server, uri := StartConfig(t, granted, []string{"-js"}, false)
	subscribe := func() error {
		t.Helper()
		conn, err := ConnectSeed(t, uri, seed)
		if err != nil {
			t.Fatalf("connect as the user: %v", err)
		}
		defer conn.Close()
		if _, err := conn.SubscribeSync("granted.subject"); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		if err := conn.Flush(); err != nil {
			t.Fatalf("flush the subscription: %v", err)
		}
		return conn.LastError()
	}
	if err := subscribe(); err != nil {
		t.Fatalf("the first configuration refused the subscription it grants: %v", err)
	}

	server.Restart(t, SubscribeGrantConfig(public, "other.>"))
	if err := subscribe(); !errors.Is(err, natsgo.ErrPermissionViolation) {
		t.Fatalf("after a restart on a configuration that does not grant it, the subscription got %v, want the server's permissions violation", err)
	}

	server.Restart(t, granted)
	if err := subscribe(); err != nil {
		t.Fatalf("after a restart back on the configuration that grants it, the subscription got %v", err)
	}
}

// StartConfig gives the server the arguments it was given and no others, as nats-server's own
// command line does: -DV puts the protocol trace in its log, and without -js among them the server
// has no JetStream, whatever a server started by another helper has.
func TestStartConfigRunsTheArgumentsItWasGivenAndNoOther(t *testing.T) {
	seed, public := User(t)
	config := fmt.Sprintf("authorization {\n  users: [ { nkey: %q } ]\n}\n", public)
	server, uri := StartConfig(t, config, []string{"-DV"}, false)
	conn, err := ConnectSeed(t, uri, seed)
	if err != nil {
		t.Fatalf("connect with the seed: %v", err)
	}
	defer conn.Close()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("JetStream context: %v", err)
	}
	if _, err := js.AccountInfo(); !errors.Is(err, natsgo.ErrJetStreamNotEnabled) {
		t.Fatalf("JetStream account info on a server given no -js: %v, want %v", err, natsgo.ErrJetStreamNotEnabled)
	}
	if !strings.Contains(server.Log(), "[TRC]") {
		t.Errorf("the log of a server given -DV holds no protocol trace:\n%s", server.Log())
	}
}

// A frozen server stops the bytes in either direction without dropping anything: the request times
// out while its connection is still CONNECTED, as a server that has stopped answering leaves it,
// and the traffic resumes when the test thaws it.
func TestAFrozenServerTimesOutARequestWithoutDroppingTheConnection(t *testing.T) {
	server, uri := Start(t)
	conn := Connect(t, uri)
	defer conn.Close()
	sub, err := conn.Subscribe("echo", func(msg *natsgo.Msg) { _ = msg.Respond([]byte("answer")) })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if _, err := conn.Request("echo", nil, connectTimeout); err != nil {
		t.Fatalf("request before the freeze: %v", err)
	}

	thaw := server.Freeze(t)
	if _, err := conn.Request("echo", nil, time.Second); !errors.Is(err, natsgo.ErrTimeout) {
		t.Fatalf("a request to a frozen server: %v, want %v", err, natsgo.ErrTimeout)
	}
	if status := conn.Status(); status != natsgo.CONNECTED {
		t.Fatalf("the connection to a frozen server is %v, want CONNECTED", status)
	}

	thaw()
	answer, err := conn.Request("echo", nil, connectTimeout)
	if err != nil {
		t.Fatalf("request after the thaw: %v", err)
	}
	if string(answer.Data) != "answer" {
		t.Errorf("the answer after the thaw is %q, want %q", answer.Data, "answer")
	}
}

// Two freezes may overlap, and each thaw may be called more than once: neither ends the test
// binary (a second close of one gate panicked it once), a thaw left over from an earlier freeze
// does not end a later one, and the traffic resumes once every thaw has run.
func TestOverlappingFreezesThawInEitherOrderWithoutPanicking(t *testing.T) {
	for _, secondFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("second thawed first %v", secondFirst), func(t *testing.T) {
			server, uri := Start(t)
			conn := Connect(t, uri)
			defer conn.Close()
			sub, err := conn.Subscribe("echo", func(msg *natsgo.Msg) { _ = msg.Respond([]byte("answer")) })
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			defer func() { _ = sub.Unsubscribe() }()

			first, second := server.Freeze(t), server.Freeze(t)
			if secondFirst {
				first, second = second, first
			}
			first()
			second()
			first()
			second()
			if _, err := conn.Request("echo", nil, connectTimeout); err != nil {
				t.Fatalf("request after every thaw: %v", err)
			}

			later := server.Freeze(t)
			first()
			if _, err := conn.Request("echo", nil, time.Second); !errors.Is(err, natsgo.ErrTimeout) {
				t.Fatalf("a request after a stale thaw of an earlier freeze: %v, want %v", err, natsgo.ErrTimeout)
			}
			later()
			if _, err := conn.Request("echo", nil, connectTimeout); err != nil {
				t.Fatalf("request after the later thaw: %v", err)
			}
		})
	}
}

// The anonymous wait ends at once, failing the test, on a server that refuses a client with no
// credential: that server is up and has no no_auth_user, so waiting out the deadline would only
// report the same refusal later.
func TestTheAnonymousWaitRefusesAServerThatRefusesAnyone(t *testing.T) {
	_, public := User(t)
	uri := StartNkeyAuthorized(t, public)
	wait := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		awaitAuth(wait, uri, true)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the anonymous wait is still waiting on a server that refuses anyone")
	}
	if want := "refused a client with no credential"; !strings.Contains(wait.message, want) {
		t.Errorf("the anonymous wait ended with %q, want it to say %q", wait.message, want)
	}
}
