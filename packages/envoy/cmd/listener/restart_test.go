package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/store"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// A NATS server restart leaves the listener following the interest registry, and a shutdown soon
// after one is still ordered. A restart loses the server's ephemeral consumers, each KV watcher's
// ordered consumer among them. nats.go replaces a lost ordered consumer only once it notices the
// missed heartbeats, up to twenty seconds later, so a watcher the reconnect does not replace keeps
// serving the cache it had while another listener's subscriptions go unseen, and a shutdown in
// that window deletes a consumer the server no longer has, which the watcher's end reports at WARN
// because the consumer is gone either way (kvwatch.Watcher.End, LEGION-278). Every ERROR line
// fails the test.
func TestListenerFollowsTheInterestRegistryAcrossNATSRestarts(t *testing.T) {
	server, uri := testnats.Start(t)
	listener := startListenerProcess(t, cmdtest.Build(t, "envoy-listener"), uri, "restart-test")
	listener.waitHealthy(t)

	// writerSession is the session a round's write subscribes.
	writerSession := func(round int) string { return "ses_restart_writer_" + strconv.Itoa(round) }
	// subscribe writes an interest through another listener's registry and waits for this
	// listener's cache to list it, failing as round's write when it does not within 3s of since.
	subscribe := func(round int, since time.Time) {
		writer := testnats.Connect(t, uri)
		registry, err := store.Open(writer)
		if err != nil {
			t.Fatalf("restart %d: open the interest registry as another listener: %v", round, err)
		}
		sessionID := writerSession(round)
		if _, err := registry.Upsert(store.Interest{SessionID: sessionID, MachineID: "other-listener"}, []string{"notifications.agent." + sessionID}); err != nil {
			t.Fatalf("restart %d: subscribe through another listener: %v", round, err)
		}
		writer.Close()
		deadline := time.Now().Add(3 * time.Second)
		for !listsInterest(t, listener.port, sessionID) {
			if time.Now().After(deadline) {
				t.Fatalf("restart %d: %s after the listener reconnected, its interest cache still missed a subscription another listener wrote", round, time.Since(since).Round(time.Millisecond))
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	// One interest before the first restart, so the read after the reconnect has a row to keep: an
	// empty bucket's read keeps nothing and would prove nothing.
	subscribe(0, time.Now())

	for restart := 1; restart <= 3; restart++ {
		// The restart's own reconnect: the first line of the registry's connection reconnecting
		// that the listener logs after this restart began. A count of reconnect lines would not do:
		// the listener's three connections each log one per restart, so the first restart's lines
		// alone would already count up to the third.
		stopped := len(listener.Output.String())
		server.Stop(t)
		server.Start(t)
		reconnected := listenerLineAfter(t, listener, stopped, registryReconnected, "restart "+strconv.Itoa(restart)+": the registry's connection to reconnect")
		reconnectedAt := time.Now()

		// Every watcher a watch installs reads the whole bucket again, and nothing empties the
		// cache first (kvwatch.Cache): the reconnect hook's Rewatch does not resume after the last
		// revision applied, so the read it begins is a read holding every interest written so far.
		// The copy keeps its rows through that read and removes only what a completed read found no
		// evidence of (kvwatch.Copy, packages/envoy/AGENTS.md, "KV cache watchers"), so GET
		// /v1/interests/, which answers from the copy, lists every one of them throughout: a
		// re-read that emptied the cache and refilled it would drop one. The read checked is the
		// first that completed after this restart's reconnect, whatever came before it: kvwatch logs
		// a warm-up line for every read, so a scan that timed out and its re-read, or a read the
		// listener's self-health rebuild began, each add one, and a count of lines would take one
		// of those for the reconnect's. The write below waits for that read, so it cannot land
		// inside it and be counted in it.
		kept := func() {
			for round := range restart {
				if id := writerSession(round); !listsInterest(t, listener.port, id) {
					t.Fatalf("restart %d: while its bucket was read again, the interest cache stopped listing %s, written before the restart", restart, id)
				}
			}
		}
		warmUp := interestReadAfter(t, listener, reconnected, kept, "restart "+strconv.Itoa(restart))
		if warmUp["entries"] != float64(restart) || warmUp["delete_markers"] != float64(0) {
			t.Fatalf("restart %d: the first read of the interest cache that completed after the reconnect = %v, want a read of the whole bucket, its %d interests and no delete markers", restart, warmUp, restart)
		}
		kept()

		subscribe(restart, reconnectedAt)
	}

	listener.Terminate(t)
	listener.WaitExit(t, "SIGTERM")
	if !strings.Contains(listener.Output.String(), "envoy-listener shutdown complete") {
		t.Fatalf("the listener did not finish its ordered shutdown:\n%s", listener.Output.String())
	}
	for _, line := range strings.Split(listener.Output.String(), "\n") {
		if errorLine.MatchString(line) {
			t.Fatalf("NATS restarts and the shutdown after them logged an error: %s", line)
		}
	}
}

// registryReconnected is what the bus logs when the connection the listener's registry reads its
// buckets through, which it names envoy, has reconnected (internal/bus/connect.go). The listener's
// two other connections, envoy-session-consumers and envoy-role-lane, log the same message under
// their own names, so the name and the field after it are part of the match.
const registryReconnected = "envoy nats reconnected name=envoy url="

// listenerLineAfter waits for a line of the listener's output that holds text and starts at or
// past offset from, and returns the offset just past it. The wait is the listener's own
// (listenerWait): it ends when the line is logged, fails at once when the listener exits without
// it, and otherwise ends only at the test binary's deadline, failing as the wait for what.
func listenerLineAfter(t *testing.T, p *listenerProcess, from int, text, what string) int {
	t.Helper()
	end := -1
	exited := false
	wait := listenerWait(t)
	testwait.Holds(wait, 20*time.Millisecond, func() bool {
		select {
		case <-p.Exited:
			exited = true
		default:
		}
		// Read after the exit check, so the output of a listener that has exited is whole.
		output := p.Output.String()
		if at := strings.Index(output[from:], text); at >= 0 {
			if newline := strings.IndexByte(output[from+at:], '\n'); newline >= 0 {
				end = from + at + newline + 1
				return true
			}
		}
		return exited
	})
	if end >= 0 {
		return end
	}
	if exited {
		t.Fatalf("%s: the listener exited with %s first:\n%s", what, p.Cmd.ProcessState, p.Output.String())
	}
	t.Fatalf("%s: the listener had not logged %q in the %v the test binary's deadline left:\n%s", what, text, wait.Round(time.Millisecond), p.Output.String())
	return 0
}

// interestReadAfter waits for the first read of the interest registry's cache that completed and
// whose warm-up line the listener logged at or past offset from, running while on every pass of
// the wait, which is how a caller checks what the cache answers while the read it waits for runs,
// and returns that line. A warm-up line of a scan that timed out is passed over: that read
// completes nothing and the watcher reads the bucket again (kvwatch.Watcher.endScan). The wait is
// the listener's own (listenerWait): it fails at once when the listener exits, and otherwise only
// at the test binary's deadline, failing as the wait of what.
func interestReadAfter(t *testing.T, p *listenerProcess, from int, while func(), what string) map[string]any {
	t.Helper()
	var read map[string]any
	exited := false
	wait := listenerWait(t)
	testwait.Holds(wait, 100*time.Millisecond, func() bool {
		while()
		select {
		case <-p.Exited:
			exited = true
		default:
		}
		// Read after the exit check, so the output of a listener that has exited is whole.
		for line := range strings.SplitSeq(p.Output.String()[from:], "\n") {
			var record map[string]any
			if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "interest registry cache warm-up" && record["outcome"] == "completed" {
				read = record
				return true
			}
		}
		return exited
	})
	if read != nil {
		return read
	}
	if exited {
		t.Fatalf("%s: the listener exited with %s before a read of the interest registry completed after the reconnect:\n%s", what, p.Cmd.ProcessState, p.Output.String())
	}
	t.Fatalf("%s: no read of the interest registry completed after the reconnect in the %v the test binary's deadline left:\n%s", what, wait.Round(time.Millisecond), p.Output.String())
	return nil
}

// listsInterest reports whether the listener's GET /v1/interests/, which answers from its cache,
// lists the session.
func listsInterest(t *testing.T, port int, sessionID string) bool {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/v1/interests/", nil)
	if err != nil {
		t.Fatalf("interests request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+listenerTestToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var interests []store.Interest
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&interests) != nil {
		return false
	}
	for _, interest := range interests {
		if interest.SessionID == sessionID {
			return true
		}
	}
	return false
}
