package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/clock/clocktest"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/envoy"
	"github.com/sjawhar/envoy/internal/testwait"
)

// The dashboard reads a failed attempt row that carries an envelope id as one its session answered
// with an error (rowAnsweredWithError in packages/dispatch/web/src/features/conversation/delivery.ts),
// and offers it no same-mode Retry. That holds only while every failure sendResolvedDelivery reports
// comes with no envelope id: one beside a failure would read as the session's own error answer.
func TestSendResolvedDeliveryReportsNoEnvelopeWithAnyFailure(t *testing.T) {
	session := "s1"
	live := ResolvedMention{Target: "session:s1", Delivery: "steer", SessionID: &session}
	for _, tc := range []struct {
		name         string
		target       ResolvedMention
		answer       http.HandlerFunc
		wantEnvelope bool
		wantDup      bool
		wantFailure  bool
		wantQueued   bool
	}{
		{name: "the target did not resolve", target: ResolvedMention{Target: "role:planner", Delivery: "steer", ResolveError: "no live holder for role planner"}, wantFailure: true},
		{name: "no live session", target: ResolvedMention{Target: "session:s9", Delivery: "steer"}, wantFailure: true},
		{name: "the listener refused the send", target: live, answer: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no live session s1"}`))
		}, wantFailure: true},
		{name: "the listener failed without a reason", target: live, answer: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}, wantFailure: true},
		{name: "the listener answered something unreadable", target: live, answer: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"event_id":`))
		}, wantFailure: true},
		// The send lands and its answer, envelope and all, misses the client's window. The body is
		// read first: net/http watches a request's connection for the caller going away only once
		// its body has been read, so the context ends when the client's window ends the call.
		{name: "the listener answered too late", target: live, answer: func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
			_, _ = w.Write([]byte(`{"event_id":"envelope-late","recipient":"s1"}`))
		}, wantFailure: true},
		{name: "the listener published the send", target: live, answer: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"event_id":"envelope-1","recipient":"s1"}`))
		}, wantEnvelope: true},
		{name: "the stream already held the send", target: live, answer: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"event_id":"envelope-2","recipient":"s1","duplicate":true}`))
		}, wantDup: true},
		// A stopped target: the listener stored the message for the session and said so, so the
		// send carries its envelope and the attempt it settles is queued, never failed.
		{name: "the listener stored the send for a stopped session", target: live, answer: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"event_id":"envelope-3","recipient":"s1","state":"stopped","last_seen":1700000000000}`))
		}, wantEnvelope: true, wantQueued: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := "http://127.0.0.1:1"
			if tc.answer != nil {
				listener := httptest.NewServer(tc.answer)
				t.Cleanup(listener.Close)
				url = listener.URL
			}
			// The client's window runs on this clock, which the test moves to each call's end: a
			// listener that never answers costs the test nothing.
			clock := clocktest.New(time.Now())
			s := &server{deps: Deps{Envoy: envoy.New(url, envoy.WithClock(clock))}, clock: clock}
			answered := make(chan sendOutcome, 1)
			go func() {
				answered <- s.sendResolvedDelivery(
					context.Background(), tc.target, "body", "m1:steer", nil, contracts.ExpectsReplyOptional, []byte(`{}`), false)
			}()
			outcome := testwait.Until(t, "sendResolvedDelivery to answer", func() (sendOutcome, bool) {
				select {
				case answer := <-answered:
					return answer, true
				default:
				}
				if armed := clock.Armed(); len(armed) > 0 {
					clock.Set(armed[0])
				}
				return sendOutcome{}, false
			})
			if (outcome.envelopeID != nil) != tc.wantEnvelope || outcome.duplicate != tc.wantDup ||
				outcome.queued != tc.wantQueued || (outcome.failure != "") != tc.wantFailure {
				t.Fatalf("sendResolvedDelivery = %+v, want envelope %v, duplicate %v, queued %v, failure %v",
					outcome, tc.wantEnvelope, tc.wantDup, tc.wantQueued, tc.wantFailure)
			}
		})
	}
}
