package cistore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/cistore"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/webhook"
)

const (
	// The verdict App's id and GitHub Actions' App's, as GitHub's own JSON carries them.
	verdictApp = 5341240
	actionsApp = 15368

	verdictSecret = "s"
	verdictHead   = "abcdef1234567890abcdef1234567890abcdef12"
)

// verdictListener is the listener's real webhook handler, configured with the verdict App, over
// its real CI store and summary loop, with a subscription to acme/widgets#42's settlements.
type verdictListener struct {
	t       *testing.T
	handler func(event, body string)
	sub     *nats.Subscription
}

func newVerdictListener(t *testing.T) *verdictListener {
	t.Helper()
	_, uri := testnats.Start(t)
	client, err := bus.ConnectOwningStream([]string{uri}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("bus connect: %v", err)
	}
	t.Cleanup(client.Close)
	store := openListener(t, client.Conn)
	loop, endLoop := context.WithCancel(t.Context())
	t.Cleanup(endLoop)
	cistore.StartSummaryLoop(loop, store, client, 100*time.Millisecond, 20*time.Millisecond, metrics.New().NewGauge("held", ""), logging.New("e2e"))
	sub, err := client.Conn.SubscribeSync("notifications.github.acme.widgets.pr.42.checks")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	handler := webhook.GitHubHandler(verdictSecret, "@legion", fmt.Sprint(verdictApp), client, store, webhook.NewBodyBudget(webhook.DefaultBodyBudget, nil))
	deliveries := 0
	return &verdictListener{t: t, sub: sub, handler: func(event, body string) {
		t.Helper()
		deliveries++
		req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("d-verdict-%d", deliveries))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-Hub-Signature-256", sign(verdictSecret, []byte(body)))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("handler status = %d, body=%s", rr.Code, rr.Body.String())
		}
	}}
}

// run delivers a completed check run of name on the head, created by app.
func (l *verdictListener) run(id int, name, conclusion string, app int) {
	l.t.Helper()
	l.handler("check_run", fmt.Sprintf(`{
		"action": "completed",
		"check_run": {"id": %d, "name": %q, "status": "completed", "conclusion": %q, "completed_at": "2026-11-20T03:00:00Z",
			"head_sha": %q, "app": {"id": %d}, "check_suite": {"id": 900}, "pull_requests": [{"number": 42}]},
		"repository": {"name": "widgets", "owner": {"login": "acme"}, "full_name": "acme/widgets"}
	}`, id, name, conclusion, verdictHead, app))
}

// suite delivers a check suite of the head in action and status, created by app.
func (l *verdictListener) suite(id int, action, status, conclusion string, app int) {
	l.t.Helper()
	l.handler("check_suite", fmt.Sprintf(`{
		"action": %q,
		"check_suite": {"id": %d, "status": %q, "conclusion": %q, "updated_at": "2026-11-20T03:00:00Z",
			"head_sha": %q, "app": {"id": %d}, "latest_check_runs_count": 0, "pull_requests": [{"number": 42}]},
		"repository": {"name": "widgets", "owner": {"login": "acme"}, "full_name": "acme/widgets"}
	}`, action, id, status, conclusion, verdictHead, app))
}

// settlement is the head's next settlement, which must arrive.
func (l *verdictListener) settlement(why string) cistore.Summary {
	l.t.Helper()
	msg, err := l.sub.NextMsg(3 * time.Second)
	if err != nil {
		l.t.Fatalf("no settlement was published %s: %v", why, err)
	}
	var env contracts.Envelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		l.t.Fatalf("checks envelope not JSON: %v", err)
	}
	var sum cistore.Summary
	if err := json.Unmarshal([]byte(env.Payload), &sum); err != nil {
		l.t.Fatalf("checks payload not JSON: %v\n%s", err, env.Payload)
	}
	return sum
}

// The legion-verdict run is the Legion daemon's own output, not a CI result, so the listener's CI
// store leaves it out of the head's settlement, whichever App created it: the verdict App's run is
// its own, and another App's is an untrusted verdict. A failure written after a reviewer's changes
// therefore wakes no implementer a second time.
func TestLegionVerdictRunIsLeftOutOfTheSettlement(t *testing.T) {
	l := newVerdictListener(t)
	l.run(2, contracts.LegionVerdictCheck, "failure", verdictApp)
	l.run(3, contracts.LegionVerdictCheck, "failure", actionsApp)
	l.run(1, "build", "success", actionsApp)
	settled := l.settlement("for the head's one CI check")
	if len(settled.CheckRuns) != 1 || settled.CheckRuns[0].Name != "build" {
		t.Fatalf("the settlement names %+v, want build alone", settled.CheckRuns)
	}
	if settled.Passed.Count != 1 || settled.Failed.Count != 0 {
		t.Fatalf("settlement = %+v, want build passing and nothing failing", settled)
	}
}

// GitHub makes an empty check suite for the verdict App on each push, queued until the App writes
// a run in it. The CI store leaves it out, in each action GitHub sends it in, so a head whose only
// unfinished suite is that one settles on its CI as any other head does.
func TestVerdictAppSuiteLeavesTheHeadToSettle(t *testing.T) {
	l := newVerdictListener(t)
	l.suite(800, "requested", "queued", "", verdictApp)
	l.suite(800, "rerequested", "queued", "", verdictApp)
	l.suite(900, "completed", "completed", "success", actionsApp)
	l.run(1, "build", "success", actionsApp)
	settled := l.settlement("with the verdict App's queued suite at the head")
	if settled.SHA != verdictHead || settled.Passed.Count != 1 || settled.Failed.Count != 0 {
		t.Fatalf("settlement = %+v, want the head settled on its one passing check", settled)
	}
}
