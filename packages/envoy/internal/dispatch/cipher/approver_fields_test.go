package cipher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestTakesApproverFieldsFollowsTheBrokerWithinItsWindow: TakesApproverFields answers from one read
// of the broker's GET /v1/settings for ApproverFieldsTTL, on a clock the test holds, and reads again
// once that window has passed, so a broker that starts or stops saying approver_fields true is
// followed within the window and not before. A read that fails answers false even after a read
// that said true, and so does a caller whose context ends before the read answers. Each step names
// the reads it expects, so a window that never lapses, or one that lapses early, fails here.
//
// Control: fresh answering every remembered read as fresh (its window never lapsing) fails the
// first flip: the broker says true, and the client goes on answering the false it read first.
func TestTakesApproverFieldsFollowsTheBrokerWithinItsWindow(t *testing.T) {
	var (
		mu      sync.Mutex
		says    = `{"secret_type_tag":true}`
		status  = http.StatusOK
		reads   int
		holding chan struct{}
	)
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/settings" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		reads++
		body, code, hold := says, status, holding
		mu.Unlock()
		if hold != nil {
			<-hold
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	defer broker.Close()
	answer := func(body string, code int) {
		mu.Lock()
		defer mu.Unlock()
		says, status = body, code
	}
	readCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return reads
	}

	client := New(broker.URL, tokenFile(t, "account-token"))
	start := time.Date(2026, 11, 22, 19, 0, 0, 0, time.UTC)
	now := start
	client.approverFields.now = func() time.Time { return now }
	ctx := context.Background()
	step := func(at time.Duration, want bool, wantReads int, what string) {
		t.Helper()
		now = start.Add(at)
		if got := client.TakesApproverFields(ctx); got != want {
			t.Fatalf("%s (at +%s): TakesApproverFields = %t, want %t", what, at, got, want)
		}
		if got := readCount(); got != wantReads {
			t.Fatalf("%s (at +%s): the broker's settings were read %d times, want %d", what, at, got, wantReads)
		}
	}

	step(0, false, 1, "a broker whose settings name no approver_fields")
	answer(`{"secret_type_tag":true,"approver_fields":true}`, http.StatusOK)
	step(ApproverFieldsTTL-time.Nanosecond, false, 1, "the broker now says approver_fields, inside the first read's window")
	step(ApproverFieldsTTL, true, 2, "the broker now says approver_fields, at the end of the first read's window")
	answer(`{"secret_type_tag":true}`, http.StatusOK)
	step(2*ApproverFieldsTTL-time.Nanosecond, true, 2, "the broker no longer says approver_fields, inside the second read's window")
	step(2*ApproverFieldsTTL, false, 3, "the broker no longer says approver_fields, at the end of the second read's window")

	answer(`{"secret_type_tag":true,"approver_fields":true}`, http.StatusOK)
	step(3*ApproverFieldsTTL, true, 4, "the broker says approver_fields again")
	answer(`{"code":"INTERNAL","error":"down"}`, http.StatusInternalServerError)
	step(4*ApproverFieldsTTL, false, 5, "a failed read after one that said true")
	step(4*ApproverFieldsTTL, false, 6, "a failed read is not remembered: the next ask reads again")

	// A caller whose context ends before the read answers is answered false, and the read goes on:
	// the next caller joins it, or finds what it remembered, and is answered the broker's true
	// with no read of its own.
	answer(`{"secret_type_tag":true,"approver_fields":true}`, http.StatusOK)
	release := make(chan struct{})
	mu.Lock()
	holding = release
	mu.Unlock()
	now = start.Add(5 * ApproverFieldsTTL)
	ended, cancel := context.WithCancel(ctx)
	cancel()
	if client.TakesApproverFields(ended) {
		t.Fatal("a caller whose context ended before the settings read answered was answered true, want false")
	}
	mu.Lock()
	holding = nil
	mu.Unlock()
	close(release)
	// The clock stays where it is: the read left running reads it.
	if !client.TakesApproverFields(ctx) {
		t.Fatal("the next caller, after the ended caller's read was let answer, was answered false, want the broker's true")
	}
	if got := readCount(); got != 7 {
		t.Fatalf("the next caller made the broker's settings be read %d times in all, want 7: it joins the read left running, or finds what that read remembered", got)
	}
}
