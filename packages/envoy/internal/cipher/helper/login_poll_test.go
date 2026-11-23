// packages/envoy/internal/cipher/helper/login_poll_test.go
//go:build linux

package helper

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/sjawhar/envoy/cipheraddr"
	"github.com/sjawhar/envoy/internal/testwait"
)

// TestALoginPollSettlesOnTheBrokersRefusal: a poll the broker answers access_denied settles the
// login "denied", and one it answers expired_token "expired", at any status that is a refusal
// (oauthrefusal.Refusal). The real broker answers access_denied 403, as authelia's OAuth library
// does, which is the fake's own status for it; expired_token is 400 there.
func TestALoginPollSettlesOnTheBrokersRefusal(t *testing.T) {
	for _, c := range []struct {
		status  int    // the answer's status in place of the fake's own; 0 keeps it
		outcome string // the device code grant's answer: "denied" (access_denied) or "expired" (expired_token)
	}{
		{0, "denied"},
		{0, "expired"},
		{http.StatusForbidden, "expired"},
		{http.StatusUnauthorized, "denied"},
	} {
		t.Run(fmt.Sprintf("%d %s", c.status, c.outcome), func(t *testing.T) {
			f := newFakeBroker(t)
			b := &Broker{URL: cipheraddr.New(f.srv.URL), OperatorFile: operatorFile(t, "ada@example.com"), MachineFile: machineFilePath(t), HTTP: f.srv.Client()}
			t.Cleanup(b.Close)
			f.mu.Lock()
			f.loginOutcome, f.oauthErrorStatus = c.outcome, c.status
			f.mu.Unlock()
			if _, err := b.Login(context.Background(), "host-a"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { return b.LoginStatus().State == c.outcome })
		})
	}
}

// TestALoginPollTheBrokerCouldNotAnswerSettlesNothing: the device code grant settles a login only
// from an answer whose status is a refusal (oauthrefusal.Refusal). A 5xx, a 408 or a 429 whose
// body names access_denied or expired_token is a broker that could not answer, so the login stays
// pending and the helper polls again, 5 s further apart; the same error at the broker's own status
// then settles it, at that next poll.
func TestALoginPollTheBrokerCouldNotAnswerSettlesNothing(t *testing.T) {
	for _, c := range []struct {
		status  int
		outcome string // the device code grant's answer: "denied" (access_denied) or "expired" (expired_token)
	}{
		{http.StatusServiceUnavailable, "denied"},
		{http.StatusServiceUnavailable, "expired"},
		{http.StatusInternalServerError, "expired"},
		{http.StatusRequestTimeout, "denied"},
		{http.StatusTooManyRequests, "denied"},
	} {
		t.Run(fmt.Sprintf("%d %s", c.status, c.outcome), func(t *testing.T) {
			f := newFakeBroker(t)
			b := &Broker{URL: cipheraddr.New(f.srv.URL), OperatorFile: operatorFile(t, "ada@example.com"), MachineFile: machineFilePath(t), HTTP: f.srv.Client()}
			t.Cleanup(b.Close)
			f.mu.Lock()
			f.loginOutcome, f.oauthErrorStatus = c.outcome, c.status
			f.mu.Unlock()
			if _, err := b.Login(context.Background(), "host-a"); err != nil {
				t.Fatal(err)
			}
			// The first poll is answered at c.status; once it is, every later one is answered at
			// the fake's own status, under the lock the fake answers under.
			if !testwait.Holds(30*time.Second, 5*time.Millisecond, func() bool {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.devicePolls >= 1 {
					f.oauthErrorStatus = 0
				}
				return f.devicePolls >= 1
			}) {
				t.Fatal("the helper never polled the device code")
			}
			// The second poll comes 1 + 5 s after the first: the broker's interval and the step
			// an outage adds.
			if !testwait.Holds(30*time.Second, 5*time.Millisecond, func() bool { return b.LoginStatus().State == c.outcome }) {
				t.Fatalf("login-status %+v, want %s", b.LoginStatus(), c.outcome)
			}
			// A login settles at most once, and polls no more once it has.
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.devicePolls < 2 {
				t.Fatalf("the login settled %s after %d poll, want a second: the first, answered %d naming its error, settled it", c.outcome, f.devicePolls, c.status)
			}
		})
	}
}

// pollAnswer answers one device code poll of the fake broker (fakeBroker.devicePollAnswer).
type pollAnswer func(w http.ResponseWriter)

// answering is an OAuth error answer of status naming code ("" names none), with header's pairs.
func answering(status int, code string, header ...string) pollAnswer {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		writeJSON(w, status, map[string]string{"error": code})
	}
}

// hangingUp answers a poll with no answer at all: the connection closes once the request is read.
func hangingUp(w http.ResponseWriter) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		conn.Close()
	}
}

// pollRig is a Broker logged in against the fake broker, whose first polls are answered in turn
// by answers and every later one by the fake itself (an approval), and whose waits before each
// poll are recorded, not waited (Broker.testLoginWait). lapseAt, when not 0, is the wait that
// lapses the code, after which the helper polls no more; a negative one also leaves the code 2 s
// at the first wait, and lapses it at the wait -lapseAt.
type pollRig struct {
	f     *fakeBroker
	b     *Broker
	mu    sync.Mutex
	waits []time.Duration
	until []time.Duration // each wait's time left of the code's lifetime, read as it was handed over
}

func newPollRig(t *testing.T, lapseAt int, answers ...pollAnswer) *pollRig {
	t.Helper()
	r := &pollRig{f: newFakeBroker(t)}
	r.f.mu.Lock()
	r.f.devicePollAnswer = func(poll int, w http.ResponseWriter) bool {
		if poll > len(answers) {
			return false
		}
		answers[poll-1](w)
		return true
	}
	r.f.mu.Unlock()
	r.b = &Broker{URL: cipheraddr.New(r.f.srv.URL), OperatorFile: operatorFile(t, "ada@example.com"), MachineFile: machineFilePath(t), HTTP: r.f.srv.Client(),
		testLoginWait: func(wait time.Duration, da *oauth2.DeviceAuthResponse) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.waits, r.until = append(r.waits, wait), append(r.until, time.Until(da.Expiry))
			if lapseAt < 0 && len(r.waits) == 1 {
				da.Expiry = time.Now().Add(2 * time.Second)
			}
			if len(r.waits) == lapseAt || len(r.waits) == -lapseAt {
				da.Expiry = time.Now().Add(-time.Second)
			}
		}}
	t.Cleanup(r.b.Close)
	if _, err := r.b.Login(context.Background(), "host-a"); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *pollRig) seen() ([]time.Duration, []time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.waits), slices.Clone(r.until)
}

func (r *pollRig) polls() int {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return r.f.devicePolls
}

// TestALoginPollGrowsItsIntervalAsRFC8628Has: each poll waits the interval first, the fake's 1 s.
// RFC 8628 section 3.5 grows it by 5 s for that poll and every later one on a slow_down, at
// whatever status its body names it, and on a poll the broker did not answer or answered with an
// outage, once for an answer that is both; authorization_pending, and a refusal naming no deciding
// error, keep it. These are the TypeScript device login's rules.
func TestALoginPollGrowsItsIntervalAsRFC8628Has(t *testing.T) {
	const s = time.Second
	for _, c := range []struct {
		name    string
		answers []pollAnswer // the first polls' answers; the next is the approval
		want    []time.Duration
	}{
		{"no answer, then a 503, then the approval", []pollAnswer{hangingUp, answering(http.StatusServiceUnavailable, "")}, []time.Duration{s, 6 * s, 11 * s}},
		{"a 429 and a 408 naming access_denied, then the approval", []pollAnswer{answering(http.StatusTooManyRequests, "access_denied"), answering(http.StatusRequestTimeout, "access_denied")}, []time.Duration{s, 6 * s, 11 * s}},
		{"a slow_down, a 503, authorization_pending, then the approval", []pollAnswer{answering(http.StatusBadRequest, "slow_down"), answering(http.StatusServiceUnavailable, ""), answering(http.StatusBadRequest, "authorization_pending")}, []time.Duration{s, 6 * s, 11 * s, 11 * s}},
		{"a 503 naming authorization_pending, then the approval", []pollAnswer{answering(http.StatusServiceUnavailable, "authorization_pending")}, []time.Duration{s, 6 * s}},
		{"a 429 naming slow_down, then the approval", []pollAnswer{answering(http.StatusTooManyRequests, "slow_down")}, []time.Duration{s, 6 * s}},
		// Controls: authorization_pending, and a refusal naming no deciding error, keep the interval.
		{"a 503, a 401 invalid_client, then the approval", []pollAnswer{answering(http.StatusServiceUnavailable, ""), answering(http.StatusUnauthorized, "invalid_client")}, []time.Duration{s, 6 * s, 6 * s}},
		{"authorization_pending twice, then the approval", []pollAnswer{answering(http.StatusBadRequest, "authorization_pending"), answering(http.StatusBadRequest, "authorization_pending")}, []time.Duration{s, s, s}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newPollRig(t, 0, c.answers...)
			waitFor(t, func() bool { return r.b.LoginStatus().State == "issued" })
			if got, _ := r.seen(); !slices.Equal(got, c.want) {
				t.Fatalf("the polls waited %v, want %v", got, c.want)
			}
		})
	}
}

// TestALoginPollWaitsOutAnOutagesRetryAfter: an outage's Retry-After, in seconds or as an HTTP
// date, is waited before the next poll where it asks longer than the interval, though never past
// the code's lifetime. The broker's own 429 carries Retry-After: 30.
func TestALoginPollWaitsOutAnOutagesRetryAfter(t *testing.T) {
	t.Run("Retry-After: 30 on a 429", func(t *testing.T) {
		r := newPollRig(t, 0, answering(http.StatusTooManyRequests, "invalid_request", "Retry-After", "30"))
		waitFor(t, func() bool { return r.b.LoginStatus().State == "issued" })
		if got, _ := r.seen(); !slices.Equal(got, []time.Duration{time.Second, 30 * time.Second}) {
			t.Fatalf("the polls waited %v, want [1s 30s]", got)
		}
	})
	t.Run("an HTTP date on a 503", func(t *testing.T) {
		at := time.Now().Add(31 * time.Second).Truncate(time.Second)
		r := newPollRig(t, 0, answering(http.StatusServiceUnavailable, "", "Retry-After", at.UTC().Format(http.TimeFormat)))
		waitFor(t, func() bool { return r.b.LoginStatus().State == "issued" })
		got, _ := r.seen()
		// Read between the answer and now, so no clock decides the row.
		if len(got) != 2 || got[1] > 31*time.Second || got[1] < time.Until(at) {
			t.Fatalf("the polls waited %v, want 1 s and then until %s", got, at)
		}
	})
	t.Run("a Retry-After past the code's lifetime", func(t *testing.T) {
		r := newPollRig(t, 0, answering(http.StatusTooManyRequests, "", "Retry-After", "86400"))
		waitFor(t, func() bool { return r.b.LoginStatus().State == "issued" })
		got, until := r.seen()
		// The fake's code lives 900 s; the wait is what was left of it, read before it was handed over.
		if len(got) != 2 || got[1] > 900*time.Second || got[1] < until[1] {
			t.Fatalf("the polls waited %v with %v of the code left, want 1 s and then the code's lifetime", got, until)
		}
	})
}

// TestALoginThatLapsesAfterUnansweredPollsNamesThem: a code whose lifetime passes after polls the
// broker did not answer, or answered with an outage, records "expired" with a Reason naming how
// many came in a row just before; the lapse itself is no poll. An answered poll since the last
// outage, and the lapse names none.
func TestALoginThatLapsesAfterUnansweredPollsNamesThem(t *testing.T) {
	for _, c := range []struct {
		name    string
		answers []pollAnswer // the polls' answers; the wait after them lapses the code
		reason  string
	}{
		{"a 503, then no answer", []pollAnswer{answering(http.StatusServiceUnavailable, ""), hangingUp}, "the broker was unreachable or busy for the last 2 polls"},
		// Each poll counts, and authorization_pending is an answer that ends the run.
		{"a 503, then authorization_pending", []pollAnswer{answering(http.StatusServiceUnavailable, ""), answering(http.StatusBadRequest, "authorization_pending")}, ""},
		{"a 503, authorization_pending, a 503", []pollAnswer{answering(http.StatusServiceUnavailable, ""), answering(http.StatusBadRequest, "authorization_pending"), answering(http.StatusServiceUnavailable, "")}, "the broker was unreachable or busy for the last poll"},
		{"a refusal, then a 429", []pollAnswer{answering(http.StatusUnauthorized, "invalid_client"), answering(http.StatusTooManyRequests, "")}, "the broker was unreachable or busy for the last poll"},
		// Control: the outage was followed by an answer.
		{"a 503, then a refusal", []pollAnswer{answering(http.StatusServiceUnavailable, ""), answering(http.StatusUnauthorized, "invalid_client")}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newPollRig(t, len(c.answers)+1, c.answers...)
			waitFor(t, func() bool { return r.b.LoginStatus().State == "expired" })
			if got := r.b.LoginStatus().Reason; got != c.reason {
				t.Fatalf("the expired login's reason %q, want %q", got, c.reason)
			}
			if got := r.polls(); got != len(c.answers) {
				t.Fatalf("the helper polled %d times, want %d", got, len(c.answers))
			}
		})
	}
}

// TestALoginPollsWaitEndsAtTheCodesLifetime: no wait passes the code's lifetime. With 2 s of it
// left, a 503 that grows the interval to 6 s waits what is left, and a code that lapses in that
// wait is polled no more and records the outage before it.
func TestALoginPollsWaitEndsAtTheCodesLifetime(t *testing.T) {
	r := newPollRig(t, -2, answering(http.StatusServiceUnavailable, ""))
	waitFor(t, func() bool { return r.b.LoginStatus().State == "expired" })
	got, _ := r.seen()
	if len(got) != 2 || got[0] != time.Second || got[1] > 2*time.Second {
		t.Fatalf("the polls waited %v, want 1 s and then no more than the 2 s left", got)
	}
	if reason := r.b.LoginStatus().Reason; reason != "the broker was unreachable or busy for the last poll" || r.polls() != 1 {
		t.Fatalf("the lapse recorded %q after %d polls, want the last poll named after 1", reason, r.polls())
	}
}
