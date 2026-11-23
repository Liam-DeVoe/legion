// packages/envoy/internal/cipher/helper/login_poll_test.go
//go:build linux

package helper

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/sjawhar/envoy/cipheraddr"
	"github.com/sjawhar/envoy/internal/cipher/oauthwire"
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

// scriptedPolls stands in for x/oauth2's DeviceAccessToken (Broker.testDeviceAccessToken): its
// calls are answered in turn by answer, which is handed each call's index and the device
// authorization pollLogin polls, and each call's da.Interval is recorded.
type scriptedPolls struct {
	mu        sync.Mutex
	intervals []int64
	answer    func(call int, da *oauth2.DeviceAuthResponse) (*oauth2.Token, error)
}

func (s *scriptedPolls) poll(_ context.Context, da *oauth2.DeviceAuthResponse) (*oauth2.Token, error) {
	s.mu.Lock()
	s.intervals = append(s.intervals, da.Interval)
	call := len(s.intervals) - 1
	s.mu.Unlock()
	return s.answer(call, da)
}

func (s *scriptedPolls) seen() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.intervals)
}

// answeredPoll is DeviceAccessToken's error for a poll the broker answered status naming code.
func answeredPoll(status int, code string) error {
	return &oauth2.RetrieveError{Response: &http.Response{StatusCode: status}, ErrorCode: code}
}

// unansweredPoll is DeviceAccessToken's error for a poll that got no answer: its transport's.
var unansweredPoll = &url.Error{Op: "Post", URL: "http://broker.invalid/v1/oauth/token", Err: syscall.ECONNREFUSED}

// approvedPoll is the token answer of an approved device code, with the broker's extras.
func approvedPoll() *oauth2.Token {
	// Named as the fake broker's writeTokens names its tokens.
	access, refresh := fmt.Sprintf("access-%d", 1), fmt.Sprintf("refresh-%d", 1)
	return (&oauth2.Token{AccessToken: access, TokenType: "DPoP", RefreshToken: refresh, Expiry: time.Now().Add(15 * time.Minute)}).
		WithExtra(map[string]any{oauthwire.ExtraCredentialID: "lcred-approved", oauthwire.ExtraCredentialExpiresAt: time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)})
}

// scriptedBroker is a Broker whose login polls are scripted's, after a device authorization the
// fake broker answers with an interval of 1 s.
func scriptedBroker(t *testing.T, scripted *scriptedPolls) *Broker {
	t.Helper()
	f := newFakeBroker(t)
	b := &Broker{URL: cipheraddr.New(f.srv.URL), OperatorFile: operatorFile(t, "ada@example.com"), MachineFile: machineFilePath(t), HTTP: f.srv.Client(), testDeviceAccessToken: scripted.poll}
	t.Cleanup(b.Close)
	if _, err := b.Login(context.Background(), "host-a"); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestALoginPollTheBrokerDidNotAnswerPollsLessOften: RFC 8628 section 3.5 has a client poll less
// often after a connection timeout. x/oauth2 sets each call's ticker once, from da.Interval, so a
// poll the broker did not answer, or answered with an outage, raises it 5 s before the next call,
// and every call after polls 5 s further apart. A refusal that settles nothing keeps the interval.
func TestALoginPollTheBrokerDidNotAnswerPollsLessOften(t *testing.T) {
	for _, c := range []struct {
		name    string
		answers []error // the calls' answers in turn; past them, the approval
		want    []int64
	}{
		{"no answer, then a 503, then the approval", []error{unansweredPoll, answeredPoll(http.StatusServiceUnavailable, "")}, []int64{1, 6, 11}},
		{"a 429 and a 408 naming access_denied, then the approval", []error{answeredPoll(http.StatusTooManyRequests, "access_denied"), answeredPoll(http.StatusRequestTimeout, "access_denied")}, []int64{1, 6, 11}},
		// Control: a refusal naming no deciding error is answered, and keeps the interval.
		{"a 503, a 401 invalid_client, then the approval", []error{answeredPoll(http.StatusServiceUnavailable, ""), answeredPoll(http.StatusUnauthorized, "invalid_client")}, []int64{1, 6, 6}},
	} {
		t.Run(c.name, func(t *testing.T) {
			scripted := &scriptedPolls{answer: func(call int, _ *oauth2.DeviceAuthResponse) (*oauth2.Token, error) {
				if call < len(c.answers) {
					return nil, c.answers[call]
				}
				return approvedPoll(), nil
			}}
			b := scriptedBroker(t, scripted)
			waitFor(t, func() bool { return b.LoginStatus().State == "issued" })
			if got := scripted.seen(); !slices.Equal(got, c.want) {
				t.Fatalf("the calls polled at intervals %v, want %v", got, c.want)
			}
		})
	}
}

// TestALoginThatLapsesAfterUnansweredPollsNamesThem: a code whose lifetime passes after polls the
// broker did not answer, or answered with an outage, records "expired" with a Reason naming how
// many came in a row just before; the lapse itself (x/oauth2's wait ending on the code's deadline)
// is no poll. An answered poll since the last outage, and the lapse names none.
func TestALoginThatLapsesAfterUnansweredPollsNamesThem(t *testing.T) {
	for _, c := range []struct {
		name    string
		answers []error // the calls' answers in turn; the next call lapses the code
		reason  string
	}{
		{"a 503, then no answer", []error{answeredPoll(http.StatusServiceUnavailable, ""), unansweredPoll}, "the broker was unreachable or busy for the last 2 polls"},
		{"a refusal, then a 429", []error{answeredPoll(http.StatusUnauthorized, "invalid_client"), answeredPoll(http.StatusTooManyRequests, "")}, "the broker was unreachable or busy for the last poll"},
		// Control: the outage was followed by an answer.
		{"a 503, then a refusal", []error{answeredPoll(http.StatusServiceUnavailable, ""), answeredPoll(http.StatusUnauthorized, "invalid_client")}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			scripted := &scriptedPolls{answer: func(call int, da *oauth2.DeviceAuthResponse) (*oauth2.Token, error) {
				if call < len(c.answers) {
					return nil, c.answers[call]
				}
				// The code's lifetime passes during this call's wait, which x/oauth2 ends with its
				// deadline's own error.
				da.Expiry = time.Now().Add(-time.Second)
				return nil, context.DeadlineExceeded
			}}
			b := scriptedBroker(t, scripted)
			waitFor(t, func() bool { return b.LoginStatus().State == "expired" })
			if got := b.LoginStatus().Reason; got != c.reason {
				t.Fatalf("the expired login's reason %q, want %q", got, c.reason)
			}
			if got := len(scripted.seen()); got != len(c.answers)+1 {
				t.Fatalf("the helper polled %d times, want %d", got, len(c.answers)+1)
			}
		})
	}
}
