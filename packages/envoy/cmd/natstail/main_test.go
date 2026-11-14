package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

func TestTailPrintsEnvelopesOnTheSubject(t *testing.T) {
	server, url := testnats.Start(t)
	ctx := testwait.Context(t)

	publisher, err := bus.ConnectOwningStream([]string{url})
	if err != nil {
		t.Fatalf("connect publisher: %v", err)
	}
	t.Cleanup(publisher.Close)

	var output bytes.Buffer
	result := make(chan error, 1)
	go func() {
		result <- tail(ctx, []string{url}, "notifications.dispatch.document.T.d.>", 2, &output)
	}()
	waitForNATSSubscription(t, ctx, server, "notifications.dispatch.document.T.d.>")
	for _, envelope := range []contracts.Envelope{
		{EventID: "artifact-1", Topic: "notifications.dispatch.document.T.d.artifact.version"},
		{EventID: "artifact-2", Topic: "notifications.dispatch.document.T.d.artifact.version"},
		{EventID: "issue-1", Topic: "notifications.dispatch.issue.T-1.ask.opened"},
	} {
		if err := publisher.Publish(envelope); err != nil {
			t.Fatalf("publish %q: %v", envelope.EventID, err)
		}
	}

	if err := testwait.Receive(t, "the tail to end after its two envelopes", result); err != nil {
		t.Fatalf("tail: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("tail output lines = %d, want 2: %q", len(lines), output.String())
	}
	for _, line := range lines {
		var envelope contracts.Envelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("decode output envelope %q: %v", line, err)
		}
		if !strings.HasSuffix(envelope.Topic, ".artifact.version") {
			t.Fatalf("tail topic = %q, want document artifact version", envelope.Topic)
		}
	}
}

func waitForNATSSubscription(t *testing.T, ctx context.Context, server *testnats.Server, subject string) {
	t.Helper()
	endpoint := server.MonitorURL() + "/subsz?subs=1"
	testwait.Until(t, fmt.Sprintf("NATS to register subscription %q at %s", subject, endpoint), func() (error, bool) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatalf("create NATS subscription request: %v", err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err, false
		}
		var status struct {
			Subscriptions []struct {
				Subject string `json:"subject"`
			} `json:"subscriptions_list"`
		}
		err = json.NewDecoder(response.Body).Decode(&status)
		_ = response.Body.Close()
		if err != nil {
			return err, false
		}
		for _, subscription := range status.Subscriptions {
			if subscription.Subject == subject {
				return nil, true
			}
		}
		return nil, false
	})
}
