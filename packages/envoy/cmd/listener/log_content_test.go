package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/metrics"
)

const logContentMarker = "the-body-a-private-project-must-not-log"

// recordLine writes one request record of a message whose payload summary holds the marker, under
// privacy and decision, and answers the line it wrote.
func recordLine(t *testing.T, privacy *listenerPrivacy, decision *scopeDecision) string {
	t.Helper()
	var out bytes.Buffer
	rec := &requestRecord{logger: logging.NewWithWriter("test-machine", &out), line: "listener send", laneKey: "target_session", privacy: privacy, decision: decision}
	item := contracts.Envelope{EventID: "evt-1", Source: "agent", SourceSession: "ses_a", PayloadSummary: logContentMarker}
	rec.item = &item
	rec.write(200, outcomeAccepted)
	return out.String()
}

// TestNoListenerLogLineCarriesContentWithPrivateProjects: in the required mode a send's or a
// publish's line carries the message's payload_summary only when the privacy answer its own
// decision read lists no private project, and the "listener received" line, which makes no
// decision, carries none; a line that keeps the event id, source and outcome is written either
// way. In the all-public mode every line carries it, as at the merge base. Controls: the same line
// under an answer listing no private project carries the body, so the row fails for the private
// project it names.
func TestNoListenerLogLineCarriesContentWithPrivateProjects(t *testing.T) {
	source := newPrivacySource(t, privacyListingPRIV)
	required := &listenerPrivacy{required: true, counters: newPrivacyCounters(metrics.New())}
	required.k7 = testK7Reader(source.server.URL, k7ReadTimeout)
	decided := func() *scopeDecision {
		decision := &scopeDecision{ctx: context.Background(), privacy: required}
		_, _ = decision.answer()
		return decision
	}

	if line := recordLine(t, required, decided()); strings.Contains(line, logContentMarker) || !strings.Contains(line, `"event_id":"evt-1"`) {
		t.Errorf("with PRIV private the send's line is %s; want the event id and no body", line)
	}
	if line := recordLine(t, required, nil); strings.Contains(line, logContentMarker) {
		t.Errorf("a required-mode line that made no decision carries the body: %s", line)
	}
	if attrs := logPayload(required, nil, logContentMarker); len(attrs) != 0 {
		t.Errorf("the listener received line's attributes in the required mode are %v, want none", attrs)
	}

	source.answer(privacyNone)
	if line := recordLine(t, required, decided()); !strings.Contains(line, logContentMarker) {
		t.Errorf("control: with no private project the send's line is %s; want the body", line)
	}
	if line := recordLine(t, nil, nil); !strings.Contains(line, logContentMarker) {
		t.Errorf("all-public: the send's line is %s; want the body, as at the merge base", line)
	}
	if attrs := logPayload(nil, nil, logContentMarker); len(attrs) != 1 {
		t.Errorf("all-public: the listener received line's attributes are %v, want the body", attrs)
	}
}
