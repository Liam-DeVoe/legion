package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/brokeraddr"
	"github.com/sjawhar/envoy/internal/broker/record"
)

// waitRequestID is the one request waitBroker serves, and waitValue the value its grant releases
// for WAIT_ME.
const (
	waitRequestID = "req-wait"
	waitValue     = "wait-value"
)

// waitRead is waitBroker's record of one status read of waitRequestID. Its began and answered are
// steps of the broker's record (waitBroker.step), which order it against the grant, the release
// and the other reads with no clock.
type waitRead struct {
	wait     string // the read's wait parameter, "" for none
	held     bool   // the broker held it: it honours wait, and the read asked 1 to record.MaxRequestWaitSeconds
	ranOut   bool   // its hold ran its whole wait, undecided
	began    int    // the step at which it reached the broker
	answered int    // the step at which the broker answered it, 0 if it never did
	state    string // the state it answered
}

// waitBroker is a fake broker with one request, WAIT_ME's waitRequestID, which it grants while its
// grantOnRead-th status read is in flight: once it holds that read, so the grant ends the hold and
// the read answers granted, or, for a read it answers at once, just after that read's pending
// answer, so the command is between reads when the grant comes. With honoursWait it answers a
// status read as the broker does a GET /v1/requests/{id}?wait=N: held until the request is granted
// or N seconds pass, whichever is first; without it, it answers at once, as a broker from before
// the parameter does. It checks every proof as the broker does (proofKey.verify), so a client that
// signed its query would be refused. It numbers each read's arrival and answer, the grant and the
// release in the order they happen, and the rows judge the command by that order and by the
// command's own record of the steps of its wait (commandStep), never by a clock.
type waitBroker struct {
	*httptest.Server
	decided   chan struct{} // closed by the grant
	mu        sync.Mutex
	steps     int        // the steps of the record taken so far
	reads     []waitRead // each status read, in the order they arrived
	granted   int        // the step of the grant, 0 until it is made
	released  int        // the step at which POST /v1/grants/{id}/values released the value, 0 until it does
	badProofs int
}

// step is the number of the next step of b's record. b.mu is held.
func (b *waitBroker) step() int {
	b.steps++
	return b.steps
}

// grant decides the request granted, as the record's next step, and ends every hold that waits on
// the decision. b.mu is held.
func (b *waitBroker) grant() {
	b.granted = b.step()
	close(b.decided)
}

// record is b's record as text, for a row's log and its failures. b.mu is held.
func (b *waitBroker) record() string {
	var s strings.Builder
	for i, read := range b.reads {
		fmt.Fprintf(&s, "read %d (wait=%q", i+1, read.wait)
		if read.held {
			s.WriteString(", held")
		}
		if read.ranOut {
			s.WriteString(", ran its whole wait")
		}
		fmt.Fprintf(&s, ") arrived at step %d and ", read.began)
		if read.answered == 0 {
			s.WriteString("was never answered; ")
		} else {
			fmt.Fprintf(&s, "answered %s at step %d; ", read.state, read.answered)
		}
	}
	fmt.Fprintf(&s, "granted at step %d, released at step %d (0: never)", b.granted, b.released)
	return s.String()
}

// learnedOfTheGrant fails t unless the command learned of the grant as R1 asks, through a read the
// grant answered: the first read to answer granted reached the broker before the grant was made,
// the broker held it and the grant ended that hold; no read followed it; and the value was
// released after it answered. A loop that reads only between sleeps learns of a grant made while
// it sleeps through a read sent after the grant, at its next poll. It returns that read's number,
// from 1. b.mu is held.
func (b *waitBroker) learnedOfTheGrant(t *testing.T) int {
	t.Helper()
	i := slices.IndexFunc(b.reads, func(read waitRead) bool { return read.state == "granted" })
	if i < 0 {
		t.Fatalf("no status read answered granted (%s)", b.record())
	}
	read := b.reads[i]
	if read.began > b.granted {
		t.Errorf("the command learned of the grant through read %d, sent after the grant was made: at its next poll, not through a read held when the grant came (%s)", i+1, b.record())
	} else if !read.held || read.ranOut {
		t.Errorf("read %d, sent before the grant, was not held until the grant ended its hold (%s)", i+1, b.record())
	}
	if len(b.reads) > i+1 {
		t.Errorf("the command read the request %d more times after read %d told it of the grant (%s)", len(b.reads)-i-1, i+1, b.record())
	}
	if b.released < read.answered {
		t.Errorf("the value was not released after read %d told the command of the grant (%s)", i+1, b.record())
	}
	return i + 1
}

func newWaitBroker(t *testing.T, key *proofKey, honoursWait bool, grantOnRead int) *waitBroker {
	t.Helper()
	b := &waitBroker{decided: make(chan struct{})}
	var publicURL string
	verified := func(w http.ResponseWriter, r *http.Request) bool {
		if err := key.verify(r, publicURL); err != nil {
			b.mu.Lock()
			b.badProofs++
			b.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"PROOF_INVALID","error":"proof invalid: htm/htu do not match the request"}`))
			return false
		}
		return true
	}
	state := func(granted bool) map[string]any {
		answer := map[string]any{"state": "pending", "grant_id": nil, "record_id": "rec-wait", "approver": "ada@example.com", "decided_at": nil, "decision": nil}
		if granted {
			answer["state"], answer["grant_id"] = "granted", "grant-wait"
		}
		return answer
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/requests", func(w http.ResponseWriter, r *http.Request) {
		if !verified(w, r) {
			return
		}
		writeJSON(w, map[string]any{
			"request_id": waitRequestID, "state": "pending", "grant_id": nil, "record_id": "rec-wait",
			"approver": "ada@example.com", "secrets": []map[string]string{{"name": "WAIT_ME", "decision": "approval"}},
		})
	})
	mux.HandleFunc("GET /v1/requests/"+waitRequestID, func(w http.ResponseWriter, r *http.Request) {
		if !verified(w, r) {
			return
		}
		wait := r.URL.Query().Get("wait")
		seconds, err := strconv.Atoi(wait)
		held := honoursWait && err == nil && seconds >= 1 && seconds <= record.MaxRequestWaitSeconds
		b.mu.Lock()
		b.reads = append(b.reads, waitRead{wait: wait, held: held, began: b.step()})
		n := len(b.reads)
		if n == grantOnRead && held {
			// The grant comes while this read is held, and ends its hold below.
			b.grant()
		}
		b.mu.Unlock()
		ranOut := false
		if held {
			select {
			case <-b.decided:
			// The broker answers a held read pending once the wait it asked has passed
			// (readRequest), and the command times that hold on its own clock: after a hold that
			// ran its whole wait it reads again at once, after a shorter one it sleeps its backoff
			// first. So a hold no grant ends lasts the wait it asked, the broker's setting it
			// copies; one that ended on anything sooner would be the early answer of a broker from
			// before ?wait=, which TestExecBacksOffAgainstABrokerThatIgnoresWait covers.
			case <-time.After(time.Duration(seconds) * time.Second):
				ranOut = true
			case <-r.Context().Done():
				return
			}
		}
		b.mu.Lock()
		read := &b.reads[n-1]
		read.ranOut = ranOut
		read.state = "pending"
		if b.granted != 0 {
			read.state = "granted"
		}
		read.answered = b.step()
		if n == grantOnRead && !held {
			// A read answered at once: the grant comes after its pending answer, before the
			// command can send the next read.
			b.grant()
		}
		answer := state(read.state == "granted")
		b.mu.Unlock()
		writeJSON(w, answer)
	})
	mux.HandleFunc("POST /v1/grants/grant-wait/values", func(w http.ResponseWriter, r *http.Request) {
		if !verified(w, r) {
			return
		}
		b.mu.Lock()
		b.released = b.step()
		b.mu.Unlock()
		writeJSON(w, map[string]any{"values": map[string]string{"WAIT_ME": waitValue}, "expires_at": time.Now().Add(time.Hour)})
	})
	mux.HandleFunc("GET /v1/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, fakeSettings)
	})
	b.Server = httptest.NewServer(mux)
	t.Cleanup(b.Close)
	publicURL = b.URL
	return b
}

// commandStep is one line of the command's own record of its wait (waitRecordFile), which it
// writes before the step: a status read, or a backoff sleep of the length the line names.
type commandStep struct {
	line  string        // the line as the command wrote it
	read  bool          // a status read; else a backoff sleep
	sleep time.Duration // a sleep's length
}

// commandRecord reads the steps the command wrote to path, failing t on a line that names neither
// a read nor a sleep.
func commandRecord(t *testing.T, path string) []commandStep {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the command's record of its wait: %v", err)
	}
	var steps []commandStep
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSuffix(line, "\n")
		length, isSleep := strings.CutPrefix(line, "sleep ")
		switch {
		case strings.HasPrefix(line, "read wait="):
			steps = append(steps, commandStep{line: line, read: true})
		case isSleep:
			sleep, err := time.ParseDuration(length)
			if err != nil {
				t.Fatalf("the command's record of its wait holds %q, a sleep of no length: %v", line, err)
			}
			steps = append(steps, commandStep{line: line, sleep: sleep})
		default:
			t.Fatalf("the command's record of its wait holds %q, neither a read nor a sleep", line)
		}
	}
	return steps
}

// recordText is steps as the command wrote them, for a row's log and its failures.
func recordText(steps []commandStep) string {
	lines := make([]string, len(steps))
	for i, step := range steps {
		lines[i] = step.line
	}
	return strings.Join(lines, "; ")
}

// sleepsBetweenReads is, for each read of steps after the first, the lengths of the backoff sleeps
// the command began between the read before it and that read.
func sleepsBetweenReads(steps []commandStep) [][]time.Duration {
	var gaps [][]time.Duration
	var sleeps []time.Duration
	read := false
	for _, step := range steps {
		switch {
		case step.read && read:
			gaps = append(gaps, sleeps)
			sleeps = nil
		case step.read:
			read = true
		case read:
			sleeps = append(sleeps, step.sleep)
		}
	}
	return gaps
}

// runWaiting runs `agent-secrets WAIT_ME --wait 1m -- sh -c …` against b with waitRecordFile set,
// and returns the steps of the command's record. It fails t unless the child ran with the granted
// value and the record names one read for each status read b served, so that its reads are b's,
// in the same order.
func runWaiting(t *testing.T, b *waitBroker, key *proofKey) []commandStep {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wait-record")
	stdout, stderr, exit := runAgentSecrets(t, buildAgentSecrets(t), b.URL, key.keyDir, []string{waitRecordFile + "=" + path},
		"WAIT_ME", "--wait", "1m", "--", "sh", "-c", `test "$WAIT_ME" = `+waitValue+` && echo ran-the-child`)
	b.mu.Lock()
	badProofs, served := b.badProofs, len(b.reads)
	b.mu.Unlock()
	if badProofs != 0 {
		t.Fatalf("the broker refused %d proofs: a proof signs the path alone, never the query (stderr %q)", badProofs, stderr)
	}
	if exit != 0 || !strings.Contains(stdout, "ran-the-child") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want the child run with the granted value", exit, stdout, stderr)
	}
	steps := commandRecord(t, path)
	sent := 0
	for _, step := range steps {
		if step.read {
			sent++
		}
	}
	if sent != served {
		t.Fatalf("the command's record names %d status reads (%s), the broker served %d", sent, recordText(steps), served)
	}
	return steps
}

// TestExecLearnsOfTheGrantWithinASecond: against a broker that holds each `wait` read, a grant made
// while the command's status read is held reaches the command through that read, which the grant
// ends: no read follows it, and the value is released (the last broker call before the child
// runs) after it has answered (R1, learnedOfTheGrant). It takes at most 2 status reads, each
// asking a wait of 1 to record.MaxRequestWaitSeconds seconds. The loop before this change sent no
// wait, so the broker answered its first read at once and the grant came while it slept: it
// learned of the grant only through the next read, sent after it.
func TestExecLearnsOfTheGrantWithinASecond(t *testing.T) {
	key := newProofKey(t)
	b := newWaitBroker(t, key, true, 1)
	steps := runWaiting(t, b, key)
	b.mu.Lock()
	defer b.mu.Unlock()
	t.Logf("the broker's record: %s; the command's: %s", b.record(), recordText(steps))
	b.learnedOfTheGrant(t)
	if len(b.reads) > 2 {
		t.Errorf("the broker served %d status reads, want at most 2 (%s)", len(b.reads), b.record())
	}
	for _, read := range b.reads {
		if seconds, err := strconv.Atoi(read.wait); err != nil || seconds < 1 || seconds > record.MaxRequestWaitSeconds {
			t.Errorf("a status read asked wait=%q, want a whole number of seconds from 1 to %d (%s)", read.wait, record.MaxRequestWaitSeconds, b.record())
		}
	}
}

// TestExecLearnsOfAGrantAfterAFullHold: R1's usual case, a person who decides after the first held
// read has run its whole wait. The fake holds that read for all record.MaxRequestWaitSeconds and
// answers it pending, then grants while the next read is held; the command learns of the grant
// through that read, which the grant ends (learnedOfTheGrant), in 2 status reads. The command's
// own record shows it sent that read with no backoff sleep after the full hold: a loop that slept
// its backoff after every pending answer, a held one too, would begin a 2 s sleep there and
// learn of a grant made during it only through its next read.
func TestExecLearnsOfAGrantAfterAFullHold(t *testing.T) {
	key := newProofKey(t)
	b := newWaitBroker(t, key, true, 2)
	steps := runWaiting(t, b, key)
	b.mu.Lock()
	defer b.mu.Unlock()
	t.Logf("the broker's record: %s; the command's: %s", b.record(), recordText(steps))
	full := strconv.Itoa(record.MaxRequestWaitSeconds)
	if len(b.reads) == 0 || b.reads[0].wait != full {
		t.Fatalf("the first status read did not ask wait=%s, the whole wait (%s)", full, b.record())
	}
	if first := b.reads[0]; !first.ranOut || first.state != "pending" {
		t.Errorf("read 1 was not held for its whole wait and answered pending (%s)", b.record())
	}
	b.learnedOfTheGrant(t)
	if gaps := sleepsBetweenReads(steps); len(gaps) > 0 && len(gaps[0]) > 0 {
		t.Errorf("the command began the backoff sleeps %v between read 1, held its whole wait, and read 2: want none (the command's record: %s)", gaps[0], recordText(steps))
	}
	if len(b.reads) != 2 {
		t.Errorf("the broker served %d status reads, want 2: the full hold, then the read the grant ends (%s)", len(b.reads), b.record())
	}
}

// TestTheWaitLimitEndsInsideTheClientsCallTimeout: a held read of the longest wait the broker
// takes ends before the whole-call timeout of the client every call goes out on (newClient's
// http.Client) cuts it. A timeout that fell to the limit or under it would cut every undecided
// held read, which the exec form reads as a failed read and answers with its backoff, so R1's
// second would become up to ten.
func TestTheWaitLimitEndsInsideTheClientsCallTimeout(t *testing.T) {
	timeout := newClient(brokeraddr.New("http://broker.invalid")).http.Timeout
	if limit := time.Duration(record.MaxRequestWaitSeconds) * time.Second; timeout <= limit {
		t.Fatalf("record.MaxRequestWaitSeconds is %s, want under the client's whole-call timeout (%s)", limit, timeout)
	}
}

// TestExecBacksOffAgainstABrokerThatIgnoresWait: a broker from before the parameter answers each
// read at once; this one answers pending until it has answered the second read, then grants. The
// loop does not read it in a tight loop: an answer that is still pending well before the wait it
// asked has run out makes it sleep its old backoff, at least 2 s, before it reads again, which the
// command's own record shows as a sleep it began between each such read and the next.
func TestExecBacksOffAgainstABrokerThatIgnoresWait(t *testing.T) {
	// leastBackoff is the exec form's first backoff, which nextBackoff only raises.
	const leastBackoff = 2 * time.Second
	key := newProofKey(t)
	b := newWaitBroker(t, key, false, 2)
	steps := runWaiting(t, b, key)
	b.mu.Lock()
	defer b.mu.Unlock()
	t.Logf("the broker's record: %s; the command's: %s", b.record(), recordText(steps))
	if len(b.reads) != 3 {
		t.Errorf("the broker served %d status reads, want 3: two it answered pending at once, then the one that found the grant (%s)", len(b.reads), b.record())
	}
	for i, sleeps := range sleepsBetweenReads(steps) {
		if b.reads[i].state == "pending" && !slices.ContainsFunc(sleeps, func(d time.Duration) bool { return d >= leastBackoff }) {
			t.Errorf("between read %d, answered pending at once, and read %d the command began the backoff sleeps %v: want one of at least %s (the command's record: %s; the broker's: %s)", i+1, i+2, sleeps, leastBackoff, recordText(steps), b.record())
		}
	}
}
