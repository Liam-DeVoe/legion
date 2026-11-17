package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestTheIdleRigRoleProcessRegistersThroughABusyListener: the rig's role process registers with
// the Envoy listener whatever answer the listener's load makes it give, as a plugin does. A
// listener whose bus did not answer within its own budget answers 503 with Retry-After, and the
// process calls again after that wait; an answer that comes later than any deadline the process's
// client keeps is one the process waits for. Either way the registration lands. The process used
// to end at either one, and its pod was never ready: on a machine at load5 1,100 to 2,000 the
// listener answered POST /v1/interests/subscribe after the 20 s the client gave it.
func TestTheIdleRigRoleProcessRegistersThroughABusyListener(t *testing.T) {
	t.Run("a 503 asking for another call", func(t *testing.T) {
		listener := &idleRigBusyListener{first: func(w http.ResponseWriter, _ *idleRigRoleProcess) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"the bus did not answer"}`, http.StatusServiceUnavailable)
		}}
		p := listener.start(t)
		if err := p.subscribe(nil); err != nil {
			t.Fatalf("subscribe through a listener that answered 503 first: %v, want the registration landed on the call after", err)
		}
		calls := listener.callTimes()
		if len(calls) != 2 {
			t.Fatalf("the listener was called %d times, want 2: the 503 and the call after it", len(calls))
		}
		if gap := calls[1].Sub(calls[0]); gap < time.Second {
			t.Errorf("the second call came %s after the first, want the 1 s the 503's Retry-After asked for", gap)
		}
	})
	t.Run("an answer held past the client's own deadline", func(t *testing.T) {
		listener := &idleRigBusyListener{first: func(w http.ResponseWriter, p *idleRigRoleProcess) {
			// A second, and the client's whole deadline on top when it keeps one: an answer later
			// than anything this process gives a call itself.
			time.Sleep(time.Second + p.client.Timeout)
			idleRigBusyListenerRegistered(w, p)
		}}
		p := listener.start(t)
		if err := p.subscribe(nil); err != nil {
			t.Fatalf("subscribe through a listener that held its answer: %v, want the registration landed", err)
		}
		if calls := listener.callTimes(); len(calls) != 1 {
			t.Errorf("the listener was called %d times, want once: the call it held", len(calls))
		}
	})
}

// idleRigBusyListener is an Envoy listener's POST /v1/interests/subscribe that answers its first
// call with first and every later one with the registration the role process asks for.
type idleRigBusyListener struct {
	first func(w http.ResponseWriter, p *idleRigRoleProcess)

	process *idleRigRoleProcess
	mu      sync.Mutex
	calls   []time.Time
}

// start serves the listener until t ends and answers a role process whose ENVOY_URL names it,
// built as the role process builds itself, with an environment and an argv of its pod's shape.
func (l *idleRigBusyListener) start(t *testing.T) *idleRigRoleProcess {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/interests/subscribe" {
			http.NotFound(w, r)
			return
		}
		l.mu.Lock()
		l.calls = append(l.calls, time.Now())
		first, p := len(l.calls) == 1, l.process
		l.mu.Unlock()
		if first {
			l.first(w, p)
			return
		}
		idleRigBusyListenerRegistered(w, p)
	}))
	t.Cleanup(server.Close)
	for name, value := range map[string]string{
		"LEGION_SANDBOX": "pod-1", "LEGION_ISSUE": "", "LEGION_ROLE": "implementer", "LEGION_GENERATION": "1",
		"LEGION_DAEMON_URL": "http://127.0.0.1:1", "ENVOY_URL": server.URL, "ENVOY_NATS_URL": "nats://127.0.0.1:1",
	} {
		t.Setenv(name, value)
	}
	p, err := newIdleRigRoleProcess("http://127.0.0.1:1", []string{"legion", "--connect", "tcp://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.process = p
	l.mu.Unlock()
	return p
}

func (l *idleRigBusyListener) callTimes() []time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]time.Time(nil), l.calls...)
}

// idleRigBusyListenerRegistered is the listener's answer to a registration it took: the session's
// own consumer, relayed on its deliver subject.
func idleRigBusyListenerRegistered(w http.ResponseWriter, p *idleRigRoleProcess) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"delivery":{"mode":%q,"subject":%q}}`, idleRigDeliveryConsumer, idleRigDeliverSubjectPrefix+p.session)
}
