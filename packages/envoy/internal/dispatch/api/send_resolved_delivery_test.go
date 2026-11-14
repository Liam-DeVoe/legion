package api

import (
	"context"
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
		// The send lands and its answer, envelope and all, misses the client's window.
		{name: "the listener answered too late", target: live, answer: func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
			_, _ = w.Write([]byte(`{"event_id":"envelope-late","recipient":"s1"}`))
		}, wantFailure: true},
		{name: "the listener published the send", target: live, answer: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"event_id":"envelope-1","recipient":"s1"}`))
		}, wantEnvelope: true},
		{name: "the stream already held the send", target: live, answer: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"event_id":"envelope-2","recipient":"s1","duplicate":true}`))
		}, wantDup: true},
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
			type sent struct {
				envelopeID *string
				duplicate  bool
				failure    string
			}
			answered := make(chan sent, 1)
			go func() {
				envelopeID, duplicate, failure := s.sendResolvedDelivery(
					context.Background(), tc.target, "body", "m1:steer", nil, contracts.ExpectsReplyOptional, []byte(`{}`))
				answered <- sent{envelopeID, duplicate, failure}
			}()
			got := testwait.Until(t, "sendResolvedDelivery to answer", func() (sent, bool) {
				select {
				case answer := <-answered:
					return answer, true
				default:
				}
				if armed := clock.Armed(); len(armed) > 0 {
					clock.Set(armed[0])
				}
				return sent{}, false
			})
			if (got.envelopeID != nil) != tc.wantEnvelope || got.duplicate != tc.wantDup || (got.failure != "") != tc.wantFailure {
				t.Fatalf("sendResolvedDelivery = (envelope %v, duplicate %v, failure %q), want envelope %v, duplicate %v, failure %v",
					got.envelopeID, got.duplicate, got.failure, tc.wantEnvelope, tc.wantDup, tc.wantFailure)
			}
		})
	}
}
