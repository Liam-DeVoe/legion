package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/listenerwire"
)

// PublishAs posts the publish Publish posts, keep false, with the session it names as the
// envelope's source_session, and answers the event id of the envelope the listener's 200 names;
// Publish names no source session and reads no event id. A 404 for a role with no holder is the
// ErrNoHolder Publish answers.
func TestPublishAsNamesItsSessionAndAnswersTheStoredEventID(t *testing.T) {
	var sources []string
	noHolder := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body listenerwire.PublishRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Keep == nil || *body.Keep {
			t.Errorf("publish keep = %v, want false", body.Keep)
		}
		sources = append(sources, body.SourceSession)
		if noHolder {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"role has no holder","reason":"unclaimed"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(listenerwire.PublishResponse{Envelope: listenerwire.Envelope{EventID: "evt-stored", Source: "agent"}})
	}))
	defer server.Close()
	publisher := New(server.URL, "")

	eventID, err := publisher.PublishAs(context.Background(), "notifications.role.legion-capture-controller", "provider-errors on CAPTURE-7",
		map[string]string{"kind": "provider-errors"}, "legion-outbox:9", DaemonSession("capture"))
	if err != nil || eventID != "evt-stored" {
		t.Fatalf("PublishAs = %q, %v, want the stored envelope's event id evt-stored", eventID, err)
	}
	if err := publisher.Publish(context.Background(), "notifications.role.legion-capture-controller", "tick", map[string]string{"kind": "tick"}, "legion-outbox:10"); err != nil {
		t.Fatalf("Publish = %v", err)
	}
	if len(sources) != 2 || sources[0] != "legion-daemon-capture" || sources[1] != "" {
		t.Fatalf("the publishes named the source sessions %q, want legion-daemon-capture and then none", sources)
	}
	noHolder = true
	if _, err := publisher.PublishAs(context.Background(), "notifications.role.legion-capture-controller", "provider-errors on CAPTURE-7",
		map[string]string{"kind": "provider-errors"}, "legion-outbox:9", DaemonSession("capture")); !errors.Is(err, ErrNoHolder) {
		t.Fatalf("PublishAs to a role with no holder = %v, want ErrNoHolder", err)
	}
}

// RegisterOwnReader registers the session as an own reader, following no topic and asking for no
// consumer, and takes only an answer of mode own_reader: a listener that predates own readers
// refuses the registration (409 not_requested), and one that answered another mode reads the
// session as no own reader; both are errors naming what the listener said.
func TestRegisterOwnReaderTakesOnlyAnOwnReaderAnswer(t *testing.T) {
	answer := func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(listenerwire.SubscribeResponse{Delivery: &listenerwire.DeliveryAnswer{Mode: OwnReaderMode}})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/interests/subscribe" {
			t.Errorf("request to %s, want /v1/interests/subscribe", r.URL.Path)
		}
		var body listenerwire.SubscribeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.SessionID != "legion-daemon-capture" || body.Topics == nil || len(body.Topics) != 0 || body.Delivery == nil || !body.Delivery.OwnReader || body.Delivery.Consumer {
			t.Errorf("registration %+v, want legion-daemon-capture, no topic, own_reader and no consumer", body)
		}
		answer(w)
	}))
	defer server.Close()
	publisher := New(server.URL, "")
	if err := publisher.RegisterOwnReader(context.Background(), DaemonSession("capture"), "Legion daemon"); err != nil {
		t.Fatalf("RegisterOwnReader = %v", err)
	}
	answer = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"asked for no session consumer","reason":"not_requested"}`))
	}
	if err := publisher.RegisterOwnReader(context.Background(), DaemonSession("capture"), "Legion daemon"); err == nil || !strings.Contains(err.Error(), "not_requested") {
		t.Fatalf("RegisterOwnReader against a listener before own readers = %v, want its refusal named", err)
	}
	answer = func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(listenerwire.SubscribeResponse{Delivery: &listenerwire.DeliveryAnswer{Mode: "consumer"}})
	}
	if err := publisher.RegisterOwnReader(context.Background(), DaemonSession("capture"), "Legion daemon"); err == nil || !strings.Contains(err.Error(), "own_reader") {
		t.Fatalf("RegisterOwnReader answered mode consumer = %v, want an error", err)
	}
}
