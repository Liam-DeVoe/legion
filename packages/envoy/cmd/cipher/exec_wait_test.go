package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/cipheraddr"
	"github.com/sjawhar/envoy/internal/cipher/record"
)

// waitRequestID is the one request waitBroker serves, and waitValue the value its grant releases
// for WAIT_ME.
const (
	waitRequestID = "req-wait"
	waitValue     = "wait-value"
)

// waitBroker is a fake broker with one request, WAIT_ME's waitRequestID, which turns granted
// grantAfter after its start: the moment the first status read reaches it, or, with
// afterFirstHold, the moment the first status read it held for its whole wait answers pending.
// With honoursWait it answers a status read as the broker does a GET /v1/requests/{id}?wait=N:
// held until the request is granted or N seconds pass, whichever is first; without it, it answers
// at once, as a broker from before the parameter does. It checks every proof as the broker does
// (proofKey.verify), so a client that signed its query would be refused. It records each status
// read (waitRead) and the release, each moment with the time meter counted this process held up
// by then, so a row can time the client, another process, from the read the grant ended.
type waitBroker struct {
	*httptest.Server
	meter     *stallMeter
	mu        sync.Mutex
	grantAt   time.Time  // when the request turns granted; zero until its start
	reads     []waitRead // each status read, in the order they reached the broker
	released  time.Time  // when POST /v1/grants/{id}/values released its value
	heldAtRel time.Duration
	badProofs int
}

// waitRead is one status read: the wait it asked ("" for none), when it reached the broker and
// when the broker answered it, whether it answered granted, and the time the meter counted this
// process held up by each of those two moments.
type waitRead struct {
	wait                      string
	reached, answered         time.Time
	granted                   bool
	heldAtReach, heldAtAnswer time.Duration
}

func newWaitBroker(t *testing.T, key *proofKey, honoursWait, afterFirstHold bool, grantAfter time.Duration) *waitBroker {
	t.Helper()
	b := &waitBroker{meter: startStallMeter(t)}
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
		read := waitRead{wait: r.URL.Query().Get("wait"), reached: time.Now(), heldAtReach: b.meter.heldUp()}
		b.mu.Lock()
		first := len(b.reads) == 0
		if first && !afterFirstHold {
			b.grantAt = read.reached.Add(grantAfter)
		}
		grantAt := b.grantAt
		b.mu.Unlock()
		if seconds, err := strconv.Atoi(read.wait); honoursWait && err == nil && seconds >= 1 && seconds <= record.MaxRequestWaitSeconds {
			hold := time.Duration(seconds) * time.Second
			if !grantAt.IsZero() {
				hold = min(time.Until(grantAt), hold)
			}
			select {
			case <-time.After(hold):
			case <-r.Context().Done():
				return
			}
		}
		b.mu.Lock()
		if b.grantAt.IsZero() {
			// afterFirstHold: this read is the first, and its hold has just ended undecided.
			b.grantAt = time.Now().Add(grantAfter)
		}
		granted := !time.Now().Before(b.grantAt)
		read.granted, read.answered, read.heldAtAnswer = granted, time.Now(), b.meter.heldUp()
		b.reads = append(b.reads, read)
		b.mu.Unlock()
		writeJSON(w, state(granted))
	})
	mux.HandleFunc("POST /v1/grants/grant-wait/values", func(w http.ResponseWriter, r *http.Request) {
		if !verified(w, r) {
			return
		}
		b.mu.Lock()
		b.released, b.heldAtRel = time.Now(), b.meter.heldUp()
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

// waits is each status read's wait parameter, in order. b.mu is held.
func (b *waitBroker) waits() []string {
	waits := make([]string, len(b.reads))
	for i, read := range b.reads {
		waits[i] = read.wait
	}
	return waits
}

// requireReleaseFromTheGrantedRead fails t unless the client released the value straight from the
// answer of its last status read, the one that answered granted: the release, timed from that
// answer (spanOnTime), was not late on its own by more than commandLateSlack, the client being
// another process. Timing from the read's answer rather than from the grant leaves out what the
// fake itself took to answer, this process's own wake. It answers that read. b.mu is held.
func (b *waitBroker) requireReleaseFromTheGrantedRead(t *testing.T) waitRead {
	t.Helper()
	if len(b.reads) == 0 || b.released.IsZero() {
		t.Fatalf("the broker served %d status reads (waits %q) and released at %v; want reads, then a release", len(b.reads), b.waits(), b.released)
	}
	last := b.reads[len(b.reads)-1]
	if !last.granted {
		t.Fatalf("the last status read answered pending (waits %q); want the release to follow the read that answered granted", b.waits())
	}
	spanOnTime(t, "the release after the read that answered granted", last.answered, b.released, last.heldAtAnswer, b.heldAtRel, 0, commandLateSlack)
	return last
}

// runWaiting runs `agent-secrets WAIT_ME --wait 1m -- sh -c …` against b, failing t unless the
// child ran with the granted value.
func runWaiting(t *testing.T, b *waitBroker, key *proofKey) {
	t.Helper()
	stdout, stderr, exit := runAgentSecrets(t, buildAgentSecrets(t), b.URL, key.keyDir, nil,
		"WAIT_ME", "--wait", "1m", "--", "sh", "-c", `test "$WAIT_ME" = `+waitValue+` && echo ran-the-child`)
	b.mu.Lock()
	badProofs := b.badProofs
	b.mu.Unlock()
	if badProofs != 0 {
		t.Fatalf("the broker refused %d proofs: a proof signs the path alone, never the query (stderr %q)", badProofs, stderr)
	}
	if exit != 0 || !strings.Contains(stdout, "ran-the-child") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want the child run with the granted value", exit, stdout, stderr)
	}
}

// TestExecLearnsOfTheGrantWithinASecond: against a broker that holds each `wait` read, a request
// granted 3 s after its first status read reached the broker is learnt of from a read the broker
// held across the grant, the read the grant ended, and its value released straight from that
// read's answer (R1), in at most 2 status reads, each asking a wait of 1 to
// record.MaxRequestWaitSeconds seconds. The release is timed from that answer, not from the grant
// on the real clock, so neither the fake's own wake nor a machine that starved the client fails
// it; the client's own lateness is held to commandLateSlack. The loop before this change read at
// 2 s and 6 s, without a wait, so it learnt of the grant from a read made after it, which fails
// the row whatever the timing.
func TestExecLearnsOfTheGrantWithinASecond(t *testing.T) {
	const grantAfter = 3 * time.Second
	key := newProofKey(t)
	b := newWaitBroker(t, key, true, false, grantAfter)
	runWaiting(t, b, key)
	b.mu.Lock()
	defer b.mu.Unlock()
	last := b.requireReleaseFromTheGrantedRead(t)
	if !last.reached.Before(b.grantAt) {
		t.Errorf("the read that answered granted reached the broker %s after the grant (waits %q); want the read the grant ended, held across it", last.reached.Sub(b.grantAt).Round(time.Millisecond), b.waits())
	}
	if len(b.reads) > 2 {
		t.Errorf("the broker served %d status reads (waits %q), want at most 2", len(b.reads), b.waits())
	}
	for _, wait := range b.waits() {
		if seconds, err := strconv.Atoi(wait); err != nil || seconds < 1 || seconds > record.MaxRequestWaitSeconds {
			t.Errorf("a status read asked wait=%q, want a whole number of seconds from 1 to %d (waits %q)", wait, record.MaxRequestWaitSeconds, b.waits())
		}
	}
}

// TestExecLearnsOfAGrantAfterAFullHold: R1's usual case, a person who decides after the first
// held read has run its whole wait. The fake holds that read for all record.MaxRequestWaitSeconds
// and grants 300 ms after it answers pending. The client sends its next read with no sleep, so that
// read reaches the broker no later than lateSlack after the full hold's answer, beyond the time
// this process was held up meanwhile; a loop that slept its 2 s backoff after every pending answer,
// a held one too, is twice that late. The value is released straight from the answer of that read,
// held until the grant or, from a client the machine starved past it, answered at once, in 2
// status reads, the release timed from that answer (commandLateSlack, the client being another
// process) rather than from the grant on the real clock.
func TestExecLearnsOfAGrantAfterAFullHold(t *testing.T) {
	key := newProofKey(t)
	b := newWaitBroker(t, key, true, true, 300*time.Millisecond)
	runWaiting(t, b, key)
	b.mu.Lock()
	defer b.mu.Unlock()
	full := strconv.Itoa(record.MaxRequestWaitSeconds)
	if len(b.reads) != 2 || b.reads[0].wait != full || b.reads[0].granted {
		t.Fatalf("the broker served status reads with waits %q; want 2, the first asking %s, the whole wait, and answered pending", b.waits(), full)
	}
	hold, next := b.reads[0], b.reads[1]
	spanOnTime(t, "the read after the full hold, sent with no sleep", hold.answered, next.reached, hold.heldAtAnswer, next.heldAtReach, 0, lateSlack)
	b.requireReleaseFromTheGrantedRead(t)
}

// TestTheWaitLimitEndsInsideTheClientsCallTimeout: a held read of the longest wait the broker
// takes ends before the whole-call timeout of the client every call goes out on (newClient's
// http.Client) cuts it. A timeout that fell to the limit or under it would cut every undecided
// held read, which the exec form reads as a failed read and answers with its backoff, so R1's
// second would become up to ten.
func TestTheWaitLimitEndsInsideTheClientsCallTimeout(t *testing.T) {
	timeout := newClient(cipheraddr.New("http://broker.invalid")).http.Timeout
	if limit := time.Duration(record.MaxRequestWaitSeconds) * time.Second; timeout <= limit {
		t.Fatalf("record.MaxRequestWaitSeconds is %s, want under the client's whole-call timeout (%s)", limit, timeout)
	}
}

// TestExecBacksOffAgainstABrokerThatIgnoresWait: a broker from before the parameter answers each
// read at once, pending until 9 s after the first status read reached it. The loop does not read
// it in a tight loop: an answer that is still pending well before the wait it asked has run out
// makes it sleep its old backoff, so the child runs after at most 6 reads.
func TestExecBacksOffAgainstABrokerThatIgnoresWait(t *testing.T) {
	key := newProofKey(t)
	b := newWaitBroker(t, key, false, false, 9*time.Second)
	runWaiting(t, b, key)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.reads) > 6 {
		t.Errorf("the broker served %d status reads (waits %q), want at most 6: the loop must back off from a broker that answers at once", len(b.reads), b.waits())
	}
}
