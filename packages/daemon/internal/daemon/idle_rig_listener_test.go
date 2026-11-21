package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/testbuild"
	"github.com/sjawhar/legion/daemon/internal/testnats"
	"github.com/sjawhar/legion/daemon/internal/testtmp"
)

// idleRigListener is the real Envoy listener of packages/envoy/cmd/listener, built once per test
// process (testbuild.EnvoyBinary) and run as its own process for the rig: the one component that
// sees a message published to a role topic no live session holds and a message sent to the agent
// subject of a session that is gone, and reports both to the daemon on notifications.legion.wake.
//
// It is started the way scripts/e2e/envoy-session-delivery.sh starts one — a build of this
// checkout's cmd/listener, then PORT, ENVOY_LISTEN_HOST, ENVOY_MACHINE_ID, NATS_URLS and a HOME of
// its own, serving on a copy of a socket held for the whole run (ENVOY_LISTEN_FD), waited on until
// /healthz says healthy — because that is the only configuration it takes.
type idleRigListener struct {
	t      *testing.T
	url    string
	logs   string
	cmd    *exec.Cmd
	client *http.Client
	port   int
	// socket is the listening socket on port that the test holds until t ends (heldSocket). Each
	// start of the listener serves on a copy of it.
	socket *os.File
	stop   func()
}

// startIdleRigListener runs a listener of machine id against natsURL until t ends, and returns it
// once /healthz answers healthy. It serves on a loopback socket the test holds until t ends
// (heldSocket), so no other process can take the port before the listener serves it.
func startIdleRigListener(t *testing.T, natsURL, machine string) *idleRigListener {
	t.Helper()
	socket, port := heldSocket(t)
	return startIdleRigListenerOn(t, natsURL, machine, socket, port)
}

// startIdleRigListenerOn is startIdleRigListener on a socket the caller holds, listening on port,
// so a listener restarted mid-test comes back where every URL already handed out reaches it. The
// listener serves on a copy of the socket, and the caller's copy keeps it listening once the
// listener stops, so from a stop to the restart no other process can take the port: a connection
// made in between waits in the socket's queue, and the restarted listener answers it.
func startIdleRigListenerOn(t *testing.T, natsURL, machine string, socket *os.File, port int) *idleRigListener {
	t.Helper()
	binary := testbuild.EnvoyBinary(t, "./cmd/listener")
	// The listener's HOME is also where it runs, and the only path the test writes under it is
	// the log below.
	home := testtmp.Dir(t, "listener.log")
	logs := filepath.Join(home, "listener.log")
	file, err := os.Create(logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = home
	// The unplaceable window is ten seconds rather than the shipped minute: a copy stored on the
	// agent subject of a session with no record is held for that window before the listener gives
	// it up and reports the wake (cmd/listener's unplaceableAgentDelivery), so a pod woken that
	// way comes back inside the rig's pod wait with room to spare. ExtraFiles' first entry is
	// descriptor 3 in the listener (ENVOY_LISTEN_FD), and PORT names that socket's port, which the
	// listener checks against the socket.
	cmd.Env = append(os.Environ(),
		"HOME="+home, "PORT="+fmt.Sprint(port), "ENVOY_LISTEN_HOST=127.0.0.1", "ENVOY_LISTEN_FD=3",
		"ENVOY_MACHINE_ID="+machine, "NATS_URLS="+natsURL, "ENVOY_UNPLACEABLE_RETRY_WINDOW=10s")
	cmd.ExtraFiles = []*os.File{socket}
	cmd.Stdout, cmd.Stderr = file, file
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start the Envoy listener: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cmd.Wait()
	}()
	l := &idleRigListener{t: t, url: "http://127.0.0.1:" + fmt.Sprint(port), logs: logs, cmd: cmd, port: port,
		socket: socket, client: &http.Client{Timeout: 10 * time.Second}}
	var once sync.Once
	l.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Error("the Envoy listener did not stop")
			}
		})
	}
	t.Cleanup(l.stop)
	defer func() {
		if t.Failed() {
			t.Logf("the Envoy listener's own log:\n%s", l.log())
		}
	}()
	idleRigEventually(t, idleRigListenerStartWait, "the Envoy listener answering /healthz healthy", func() bool {
		response, err := l.client.Get(l.url + "/healthz")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		var body struct {
			Status string `json:"status"`
		}
		if json.NewDecoder(response.Body).Decode(&body) != nil {
			return false
		}
		return body.Status == "healthy"
	})
	return l
}

// log is the listener's own output so far, which a failure names.
func (l *idleRigListener) log() string {
	read, err := os.ReadFile(l.logs)
	if err != nil {
		return "(the listener's log could not be read: " + err.Error() + ")"
	}
	return string(read)
}

// publish publishes one message to a topic, as a sender's envoy_publish does. It answers the
// listener's status and body, since a publish to a role topic no live session holds is answered
// 202 kept or 404, and either answer says what the listener found of the role's holder.
func (l *idleRigListener) publish(body map[string]any) (int, string) {
	l.t.Helper()
	return l.call("/v1/messages/publish", body)
}

// send sends one message to a session's agent subject, as a sender's envoy_send does.
func (l *idleRigListener) send(body map[string]any) (int, string) {
	l.t.Helper()
	return l.call("/v1/messages/send", body)
}

// freeClaims removes a session's interest record and frees every role claim the session holds,
// as an operator's DELETE /v1/interests/<session> does (the listener's forced release). It is
// how the rig frees the claim a released session left standing at once: the listener's role
// claim reaper frees it only past the live window and at its own interval, neither of which the
// rig's listener shortens. It answers the listener's status and body.
func (l *idleRigListener) freeClaims(session string) (int, string) {
	l.t.Helper()
	request, err := http.NewRequest(http.MethodDelete, l.url+"/v1/interests/"+session, nil)
	if err != nil {
		l.t.Fatal(err)
	}
	return l.answer(request)
}

func (l *idleRigListener) call(path string, body map[string]any) (int, string) {
	l.t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		l.t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, l.url+path, bytes.NewReader(encoded))
	if err != nil {
		l.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	return l.answer(request)
}

// answer makes one request of the listener and answers its status and body.
func (l *idleRigListener) answer(request *http.Request) (int, string) {
	l.t.Helper()
	response, err := l.client.Do(request)
	if err != nil {
		l.t.Fatalf("%s %s: %v", request.Method, request.URL.Path, err)
	}
	defer response.Body.Close()
	read, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		l.t.Fatal(err)
	}
	return response.StatusCode, string(read)
}

// idleRigFreePort is a loopback port bound and given back at once. A freed port is any program's
// to take (docs/testing.md): no process may be started on it, which is why the rig's listener
// serves on a socket the test holds (startIdleRigListener), and it is no reliable refused address
// either, which this module would take from a testport.Refused it does not have. Its one caller,
// the leak check's download host, fails rather than passes if another program answers there.
func idleRigFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// No other process can take the rig listener's port while the listener serves, nor from a stop to
// the restart that brings it back on the same port (idleWakeRig.awaitListenerReady): the test holds
// the socket for its whole life and each start serves a copy of it. Another socket's bind of the
// port, as any process on the machine might make, fails with EADDRINUSE at each step, and a
// request made between the stop and the restart waits for the restarted listener, which answers
// it. The control is the same bind once the listener has stopped and the test has let go of the
// socket: it succeeds, so the check sees a port that is free.
func TestNoOtherProcessTakesTheIdleRigListenersPortAcrossARestart(t *testing.T) {
	natsURL := testnats.URL(t)
	first := startIdleRigListener(t, natsURL, "idlerig")
	taken := func(when string) {
		t.Helper()
		if err := idleRigBind(first.port); !errors.Is(err, syscall.EADDRINUSE) {
			t.Fatalf("%s, another socket's bind of the listener's port %d answered %v, want EADDRINUSE", when, first.port, err)
		}
	}
	taken("while the listener serves")

	first.stop()
	taken("between the listener's stop and its restart")
	// The connection and its request are made before the restart, so only the socket's queue can
	// carry them to the restarted listener. /livez answers 204 and reads nothing of NATS.
	waiting, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(first.port)), 10*time.Second)
	if err != nil {
		t.Fatalf("between the listener's stop and its restart, a connection to port %d failed: %v", first.port, err)
	}
	defer waiting.Close()
	if _, err := fmt.Fprintf(waiting, "GET /livez HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: close\r\n\r\n", first.port); err != nil {
		t.Fatalf("write a request to port %d between the listener's stop and its restart: %v", first.port, err)
	}

	second := startIdleRigListenerOn(t, natsURL, "idlerig", first.socket, first.port)
	taken("while the restarted listener serves")
	if err := waiting.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(waiting), nil)
	if err != nil {
		t.Fatalf("the restarted listener did not answer the request made between the stop and the restart: %v\n%s", err, second.log())
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("the restarted listener answered the request made before it started with %d, want 204 from /livez", response.StatusCode)
	}

	second.stop()
	if err := first.socket.Close(); err != nil {
		t.Fatalf("let go of the listener's socket: %v", err)
	}
	if err := idleRigBind(first.port); err != nil {
		t.Fatalf("once the listener stopped and the test let go of its socket, another socket's bind of port %d answered %v, want it bound: the check cannot see a free port", first.port, err)
	}
}

// idleRigBind binds port on loopback from a socket of its own, as another process on the machine
// might, and closes that socket at once if it was bound. It answers the bind's error.
func idleRigBind(port int) error {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return listener.Close()
}
