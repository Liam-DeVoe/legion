package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A write Dispatch answers with an outage status - a 408 a proxy in front of it answered, a 429, a
// 5xx - is retried (callRetry), and the retry's answer is the call's: the first attempt said
// nothing of the write (RFC 9110 sections 15.5.9 and 15.6, RFC 6585 section 4). A 400 is
// Dispatch's answer and is not asked again.
func TestCallRetryAsksAgainAfterAnOutage(t *testing.T) {
	for _, tc := range []struct {
		first    int
		want     int
		attempts int32
	}{
		{http.StatusRequestTimeout, http.StatusCreated, 2},
		{http.StatusTooManyRequests, http.StatusCreated, 2},
		{http.StatusBadGateway, http.StatusCreated, 2},
		{http.StatusBadRequest, http.StatusBadRequest, 1},
	} {
		t.Run(fmt.Sprint(tc.first), func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if attempts.Add(1) == 1 {
					w.WriteHeader(tc.first)
					return
				}
				w.WriteHeader(http.StatusCreated)
			}))
			defer server.Close()
			res := newAPI(server.URL, "token", 1).callRetry(context.Background(), "POST", "/api/v1/issues", who{}, map[string]any{})
			if res.status != tc.want || attempts.Load() != tc.attempts {
				t.Fatalf("callRetry after a %d = %d in %d attempts, want %d in %d", tc.first, res.status, attempts.Load(), tc.want, tc.attempts)
			}
		})
	}
}

// Only a 4xx that is no outage refuses an anchored write's quote, which the seed then anchors on
// another sentence; a 408 or a 429 that outlasted write's retries says nothing of the quote, and
// the seed stops on it, as it does on a 5xx and on an error that is no answer at all.
func TestOnlyAnAnswerThatIsNoOutageRefusesAQuote(t *testing.T) {
	for _, tc := range []struct {
		err     error
		refused bool
	}{
		{&seedError{status: http.StatusBadRequest, code: "ANCHOR_MISSING"}, true},
		{&seedError{status: http.StatusRequestEntityTooLarge, code: "REQUEST_TOO_LARGE"}, true},
		{&seedError{status: http.StatusRequestTimeout}, false},
		{&seedError{status: http.StatusTooManyRequests, code: "RATE_LIMITED"}, false},
		{&seedError{status: http.StatusServiceUnavailable}, false},
		{fmt.Errorf("comment c1: %w", context.Canceled), false},
	} {
		if _, refused := quoteRefusal(tc.err); refused != tc.refused {
			t.Errorf("quoteRefusal(%v) = %t, want %t", tc.err, refused, tc.refused)
		}
	}
}
