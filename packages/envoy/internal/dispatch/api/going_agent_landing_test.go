package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/going/agentwire"
)

// TestALandedRequestIsNotSentAgain: each request a run's call sends in-process records that it
// landed in its route's own transaction (going_landings), keyed by the run, the step, the call and
// the request's number. A reasoned close whose reason landed and whose close did not sends only the
// close on a second try of its key. A second try of a key while the first still runs waits for it,
// and then answers from what the first landed, sending nothing again. A message a run's call
// landed whose delivery was never made is sent by the server's recovery pass from its landing's
// recovery point. A landing on an issue adds the issue to the run's issues, and a request its route
// refused writes no landing.
func TestALandedRequestIsNotSentAgain(t *testing.T) {
	rig := newGoingAgentRig(t, nil)
	ctx := context.Background()

	t.Run("a reasoned close whose first request landed sends only the second on a second try", func(t *testing.T) {
		issue := rig.issue(t, "Closed with a reason")
		run, token := rig.working(t, "alice", "close "+issue)
		arguments := map[string]any{"issue": issue, "status": "done", "reason": "Shipped in the last release."}
		rig.approve(t, run, "dispatch_issue_update", arguments)
		var refuse atomic.Bool
		refuse.Store(true)
		rig.hooks.set(t, http.MethodPatch, "/api/v1/issues/"+issue, func(w http.ResponseWriter, r *http.Request, inner http.Handler) {
			if refuse.Load() {
				// The close is refused before its route runs: the reason has landed, the close has not.
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			inner.ServeHTTP(w, r)
		})
		// Every send of the reason reaches its route here, whether or not its transaction commits:
		// one whose landing the key already holds would roll back, and must not be sent at all.
		var reasonsSent atomic.Int32
		rig.hooks.set(t, http.MethodPost, "/api/v1/issues/"+issue+"/messages", func(w http.ResponseWriter, r *http.Request, inner http.Handler) {
			reasonsSent.Add(1)
			inner.ServeHTTP(w, r)
		})
		first, err := rig.call(goingRouteCall(run, token, 3, 1, "dispatch_issue_update", arguments))
		if err != nil || first.Status != http.StatusServiceUnavailable {
			t.Fatalf("the first try answered %d, %v; want the close's 503", first.Status, err)
		}
		if reasons, status := rig.goingReasonsOn(t, issue, arguments["reason"]), rig.issueRead(t, issue).Status; reasons != 1 || status == "done" {
			t.Fatalf("after the first try %s holds %d reasons and status %s, want the reason posted and the issue open", issue, reasons, status)
		}
		refuse.Store(false)
		second, err := rig.call(goingRouteCall(run, token, 3, 1, "dispatch_issue_update", arguments))
		if err != nil || second.Status != http.StatusOK || second.Replayed {
			t.Fatalf("the second try answered %d (replayed %v), %v; want the close sent and answered 200", second.Status, second.Replayed, err)
		}
		if reasons, status := rig.goingReasonsOn(t, issue, arguments["reason"]), rig.issueRead(t, issue).Status; reasons != 1 || status != "done" {
			t.Fatalf("after the second try %s holds %d reasons and status %s, want one reason and the issue done", issue, reasons, status)
		}
		if sent := reasonsSent.Load(); sent != 1 {
			t.Fatalf("the reason was sent %d times, want once: the second try answers it from its landing", sent)
		}
		if landed := rig.count(t, `select count(*) from going_landings where run_id = $1::uuid and step = 3 and call = 1`, run); landed != 2 {
			t.Fatalf("the call's key holds %d landings, want its two requests", landed)
		}
	})

	t.Run("a second try of a key while the first runs waits for it", func(t *testing.T) {
		issue := rig.issue(t, "Closed while a second try waits")
		run, token := rig.working(t, "alice", "close "+issue)
		arguments := map[string]any{"issue": issue, "status": "done", "reason": "Settled while the agent was away."}
		rig.approve(t, run, "dispatch_issue_update", arguments)
		var entered atomic.Int32
		release := make(chan struct{})
		releaseAll := sync.OnceFunc(func() { close(release) })
		t.Cleanup(releaseAll)
		rig.hooks.set(t, http.MethodPatch, "/api/v1/issues/"+issue, func(w http.ResponseWriter, r *http.Request, inner http.Handler) {
			entered.Add(1)
			<-release
			inner.ServeHTTP(w, r)
		})
		type tried struct {
			answer agentwire.CallAnswer
			err    error
		}
		firstDone, secondDone := make(chan tried, 1), make(chan tried, 1)
		go func() {
			answer, err := rig.call(goingRouteCall(run, token, 4, 1, "dispatch_issue_update", arguments))
			firstDone <- tried{answer, err}
		}()
		goingEventually(t, "the first try's close reaching its route", func() bool { return entered.Load() == 1 })
		go func() {
			answer, err := rig.call(goingRouteCall(run, token, 4, 1, "dispatch_issue_update", arguments))
			secondDone <- tried{answer, err}
		}()
		time.Sleep(500 * time.Millisecond)
		select {
		case second := <-secondDone:
			t.Fatalf("the second try answered %+v while the first still ran, want it to wait", second)
		default:
		}
		if closes := entered.Load(); closes != 1 {
			t.Fatalf("%d closes reached the route while the first try ran, want the second try to wait", closes)
		}
		releaseAll()
		first := <-firstDone
		if first.err != nil || first.answer.Status != http.StatusOK {
			t.Fatalf("the first try answered %d, %v; want the close", first.answer.Status, first.err)
		}
		second := <-secondDone
		if second.err != nil || !second.answer.Replayed || second.answer.Status != http.StatusOK {
			t.Fatalf("the second try answered %d (replayed %v), %v; want the close's recorded answer", second.answer.Status, second.answer.Replayed, second.err)
		}
		if reasons, closes := rig.goingReasonsOn(t, issue, arguments["reason"]), entered.Load(); reasons != 1 || closes != 1 {
			t.Fatalf("the two tries posted %d reasons and sent %d closes, want one of each", reasons, closes)
		}
	})

	t.Run("a message whose delivery was not confirmed is replayed by the recovery pass", func(t *testing.T) {
		run, token := rig.working(t, "alice", "tell the planner")
		arguments := map[string]any{"session": goingPlanner, "body": "Have a look at the notes from the run.", "delivery": "btw"}
		rig.approve(t, run, "dispatch_send_to_agent", arguments)
		sends := len(*rig.sent)
		answer, err := rig.call(goingRouteCall(run, token, 5, 1, "dispatch_send_to_agent", arguments))
		if err != nil || answer.Status != http.StatusCreated {
			t.Fatalf("the send answered %d %s, %v; want 201", answer.Status, answer.Body, err)
		}
		var message string
		var recovery []byte
		if err := rig.database.Pool.QueryRow(ctx, `
			select recovery ->> 'message_id', recovery from going_landings where run_id = $1::uuid and step = 5 and call = 1
		`, run).Scan(&message, &recovery); err != nil || message == "" {
			t.Fatalf("the send's landing holds recovery %s (%v), want the message it stored", recovery, err)
		}
		if len(*rig.sent) != sends+1 {
			t.Fatalf("the send reached the listener %d times, want once", len(*rig.sent)-sends)
		}
		// The process ended between the message's commit and its first attempt: the message
		// stands with no attempt, and only its landing names it.
		rig.exec(t, `delete from message_deliveries where message_id = $1::uuid`, message)
		if err := rig.served.resumeOwedDeliveries(ctx); err != nil {
			t.Fatalf("the recovery pass: %v", err)
		}
		if len(*rig.sent) != sends+2 {
			t.Fatalf("after the recovery pass the message reached the listener %d times, want once more", len(*rig.sent)-sends)
		}
		if attempts := rig.count(t, `select count(*) from message_deliveries where message_id = $1::uuid and attempt = 1`, message); attempts != 1 {
			t.Fatalf("the recovery pass left %d first attempts of the message, want the one it made", attempts)
		}
	})

	t.Run("a landing on an issue adds it to the run's issues, and a refused route writes no landing", func(t *testing.T) {
		issue := rig.issue(t, "Retitled by a run")
		run, token := rig.working(t, "alice", "retitle "+issue)
		retitle := map[string]any{"issue": issue, "title": "Retitled through a run"}
		rig.approve(t, run, "dispatch_issue_update", retitle)
		answer, err := rig.call(goingRouteCall(run, token, 6, 1, "dispatch_issue_update", retitle))
		if err != nil || answer.Status != http.StatusOK {
			t.Fatalf("the retitle answered %d %s, %v; want 200", answer.Status, answer.Body, err)
		}
		var recorded struct {
			Status int `json:"status"`
			Body   struct {
				Title string `json:"title"`
			} `json:"body"`
		}
		var row []byte
		if err := rig.database.Pool.QueryRow(ctx, `select answer from going_landings where run_id = $1::uuid and step = 6 and call = 1`, run).Scan(&row); err != nil ||
			json.Unmarshal(row, &recorded) != nil || recorded.Status != http.StatusOK || recorded.Body.Title != "Retitled through a run" {
			t.Fatalf("the retitle's landing holds %s (%v), want its route's 200 and the issue it left", row, err)
		}
		if held := rig.count(t, `select count(*) from going_runs where id = $1::uuid and $2 = any(issues)`, run, issue); held != 1 {
			t.Fatalf("the run's issues do not hold %s, which its landing changed", issue)
		}

		missing := map[string]any{"issue": "DEMO-99999", "title": "Retitled nowhere"}
		rig.approve(t, run, "dispatch_issue_update", missing)
		answer, err = rig.call(goingRouteCall(run, token, 6, 2, "dispatch_issue_update", missing))
		if err != nil || answer.Status != http.StatusNotFound {
			t.Fatalf("the retitle of an issue that does not exist answered %d %s, %v; want its route's 404", answer.Status, answer.Body, err)
		}
		if landed := rig.count(t, `select count(*) from going_landings where run_id = $1::uuid and step = 6 and call = 2`, run); landed != 0 {
			t.Fatalf("the refused retitle left %d landings, want none", landed)
		}
		if held := rig.count(t, `select count(*) from going_runs where id = $1::uuid and 'DEMO-99999' = any(issues)`, run); held != 0 {
			t.Fatal("the run's issues hold the issue its refused request named")
		}
	})
}
