// The operator's limit on how fast this listener takes one agent session's messages: what the two
// settings parse to, what one sender's bucket allows, and what the send and publish routes answer
// a session past it.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/logging"
)

func senderLimitEnvironment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestParseSenderLimit(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		off       bool
		perMinute int
		burst     int
		wantErr   string
	}{
		{name: "unset is no limit", off: true},
		{name: "empty is no limit", env: map[string]string{senderLimitRateEnv: ""}, off: true},
		{name: "zero is no limit", env: map[string]string{senderLimitRateEnv: "0"}, off: true},
		{name: "a rate alone is also its burst", env: map[string]string{senderLimitRateEnv: "4"}, perMinute: 4, burst: 4},
		{name: "a rate and a burst", env: map[string]string{senderLimitRateEnv: "6", senderLimitBurstEnv: "2"}, perMinute: 6, burst: 2},
		{name: "surrounding space is trimmed", env: map[string]string{senderLimitRateEnv: " 4 ", senderLimitBurstEnv: " 8 "}, perMinute: 4, burst: 8},
		{name: "a negative rate", env: map[string]string{senderLimitRateEnv: "-1"}, wantErr: senderLimitRateEnv},
		{name: "a rate that is no number", env: map[string]string{senderLimitRateEnv: "four"}, wantErr: senderLimitRateEnv},
		{name: "a rate no bucket can refill at", env: map[string]string{senderLimitRateEnv: "60001"}, wantErr: senderLimitRateEnv},
		{name: "a burst of zero", env: map[string]string{senderLimitRateEnv: "4", senderLimitBurstEnv: "0"}, wantErr: senderLimitBurstEnv},
		{name: "a burst that is no number", env: map[string]string{senderLimitRateEnv: "4", senderLimitBurstEnv: "many"}, wantErr: senderLimitBurstEnv},
		{name: "a burst with no rate", env: map[string]string{senderLimitBurstEnv: "3"}, wantErr: senderLimitBurstEnv},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit, err := parseSenderLimit(senderLimitEnvironment(tc.env))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one naming %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if tc.off {
				if limit != nil {
					t.Fatalf("limit = %+v, want none", limit)
				}
				return
			}
			if limit == nil || limit.perMinute != tc.perMinute || limit.burst != tc.burst {
				t.Fatalf("limit = %+v, want %d a minute and %d at once", limit, tc.perMinute, tc.burst)
			}
		})
	}
}

func TestSenderLimitKeepsOneBucketPerSender(t *testing.T) {
	limit := newSenderLimit(2, 2)
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := range 2 {
		if wait, ok := limit.take("ses_a", start); !ok {
			t.Fatalf("message %d of the burst was refused, to wait %d s", i+1, wait)
		}
	}
	wait, ok := limit.take("ses_a", start)
	if ok {
		t.Fatal("a third message inside the burst was taken")
	}
	if wait != 30 {
		t.Fatalf("wait = %d s, want 30: two a minute gives one message back each 30 s", wait)
	}
	if _, ok := limit.take("ses_b", start); !ok {
		t.Fatal("another sender was refused for the first sender's messages")
	}
	if _, ok := limit.take("ses_a", start.Add(29*time.Second)); ok {
		t.Fatal("a message was taken before the bucket gave one back")
	}
	if _, ok := limit.take("ses_a", start.Add(30*time.Second)); !ok {
		t.Fatal("a message was refused once the bucket had given one back")
	}
}

// No limit set is the listener as it was: every existing test builds its deps without one.
func TestNoSenderLimitTakesEveryMessage(t *testing.T) {
	var limit *senderLimit
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 1000 {
		if _, ok := limit.take("ses_a", now); !ok {
			t.Fatalf("message %d was refused with no limit set", i+1)
		}
	}
}

func TestLimitedSender(t *testing.T) {
	cases := []struct {
		name string
		body messageBody
		want string
	}{
		{name: "an agent session", body: messageBody{SourceSession: "ses_a"}, want: "ses_a"},
		{name: "an agent session that says so", body: messageBody{Source: "agent", SourceSession: " ses_a "}, want: "ses_a"},
		{name: "a caller that names no session", body: messageBody{}},
		{name: "Dispatch, for a session", body: messageBody{Source: "dispatch", SourceSession: "ses_a"}},
		{name: "a person", body: messageBody{Source: "human", SourceSession: "ses_a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/messages/send", nil)
			if got := limitedSender(request, tc.body); got != tc.want {
				t.Fatalf("limited sender = %q, want %q", got, tc.want)
			}
		})
	}
}

type senderLimitAnswer struct {
	Error             string `json:"error"`
	Reason            string `json:"reason"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	MessagesPerMinute int    `json:"messages_per_minute"`
	Burst             int    `json:"burst"`
}

// requireRateLimited holds the whole refusal: the status, the wait in the header and in the body,
// the words a session reads, and the request's one log line.
func requireRateLimited(t *testing.T, recorder *httptest.ResponseRecorder, logs *bytes.Buffer, line, sender string) {
	t.Helper()
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60: one a minute gives a message back after a minute", got)
	}
	var answer senderLimitAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the refusal: %v (body: %s)", err, recorder.Body.String())
	}
	if answer.Reason != "rate_limited" || answer.RetryAfterSeconds != 60 || answer.MessagesPerMinute != 1 || answer.Burst != 1 {
		t.Fatalf("refusal = %+v, want rate_limited, 60 s, 1 a minute, 1 at once", answer)
	}
	for _, want := range []string{"rate limited", sender, "1 a minute", "was not sent", "after 60 seconds"} {
		if !strings.Contains(answer.Error, want) {
			t.Fatalf("error = %q, want it to say %q", answer.Error, want)
		}
	}
	var recorded struct {
		Msg           string `json:"msg"`
		Outcome       string `json:"outcome"`
		Status        int    `json:"status"`
		SourceSession string `json:"source_session"`
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &recorded); err != nil {
		t.Fatalf("decode the last log line: %v (%s)", err, lines[len(lines)-1])
	}
	if recorded.Msg != line || recorded.Outcome != "rate_limited" || recorded.Status != http.StatusTooManyRequests || recorded.SourceSession != sender {
		t.Fatalf("log line = %+v, want %q rate_limited 429 from %s", recorded, line, sender)
	}
}

func TestSendRouteRefusesASenderPastItsLimit(t *testing.T) {
	client := setupPublishTestClient(t)
	registry, sessions := setupSessionsTest(t, nil, []string{"ses_target"})
	var logs bytes.Buffer
	state := &listenerDeps{client: client, registry: registry, sessions: sessions, senderLimit: newSenderLimit(1, 1)}
	handler := sendHandler(state, logging.NewWithWriter("test", &logs))
	send := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages/send", strings.NewReader(body)))
		return recorder
	}

	if first := send(`{"source_session":"ses_sender","target_session":"ses_target","message":"one"}`); first.Code != http.StatusOK {
		t.Fatalf("the first message: status %d, want 200 (body: %s)", first.Code, first.Body.String())
	}
	requireRateLimited(t, send(`{"source_session":"ses_sender","target_session":"ses_target","message":"two"}`), &logs, "listener send", "ses_sender")

	// The limit is one sender's own, and covers an agent session's messages alone.
	for name, body := range map[string]string{
		"another session":           `{"source_session":"ses_other","target_session":"ses_target","message":"three"}`,
		"Dispatch, for that sender": `{"source":"dispatch","source_session":"ses_sender","target_session":"ses_target","message":"four"}`,
		"a caller naming no sender": `{"target_session":"ses_target","message":"five"}`,
	} {
		if recorder := send(body); recorder.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want 200 (body: %s)", name, recorder.Code, recorder.Body.String())
		}
	}
}

func TestPublishRouteRefusesASenderPastItsLimit(t *testing.T) {
	client := setupPublishTestClient(t)
	registry, sessions := setupSessionsTest(t, nil, nil)
	var logs bytes.Buffer
	state := &listenerDeps{client: client, registry: registry, sessions: sessions, senderLimit: newSenderLimit(1, 1)}
	handler := publishHandler(state, logging.NewWithWriter("test", &logs))
	publish := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages/publish", strings.NewReader(body)))
		return recorder
	}

	if first := publish(`{"source_session":"ses_sender","topic":"notifications.team.builders.all","message":"one"}`); first.Code != http.StatusOK {
		t.Fatalf("the first message: status %d, want 200 (body: %s)", first.Code, first.Body.String())
	}
	// A role message is refused before the route looks for the role's holder, so nothing is
	// forwarded, nothing is kept for a later holder and no wake is reported: nobody holds this
	// role, and a route that went on to look would answer that, not 429.
	requireRateLimited(t, publish(`{"source_session":"ses_sender","topic":"notifications.role.reviewer","message":"two"}`), &logs, "listener publish", "ses_sender")
	requireRateLimited(t, publish(`{"source_session":"ses_sender","topic":"notifications.team.builders.all","message":"three"}`), &logs, "listener publish", "ses_sender")

	if other := publish(`{"source_session":"ses_other","topic":"notifications.team.builders.all","message":"four"}`); other.Code != http.StatusOK {
		t.Fatalf("another session: status %d, want 200 (body: %s)", other.Code, other.Body.String())
	}
}
