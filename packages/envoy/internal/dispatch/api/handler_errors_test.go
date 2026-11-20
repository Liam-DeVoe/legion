package api

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/logtest"
)

// A request whose person the identity header names, but whom the people store cannot record, is
// answered 500 INTERNAL, and the operator's line says the handler failed without naming the
// person: the error the store returns says what failed and not whose row it was, because a log
// line is read by whoever reads the server's logs (CodeQL go/clear-text-logging).
func TestAPersonTheStoreCannotRecordIsAnsweredAndLoggedWithoutTheirAddress(t *testing.T) {
	const address = "person-7@example.com"
	logs := &logtest.Recorder{}
	logtest.SetDefault(t, logs)
	database := storetest.Open(t)
	// The people store writes through a pool of its own, closed before the request, so the
	// person's record fails in the store's own database call, as it does when the database is
	// out of reach.
	unreachable := storetest.SecondProcess(t, database)
	unreachable.Pool.Close()
	handler, _, _ := newTestServer(t, testServerOptions{
		store:    database,
		identity: identity.HeaderIdentity{Header: "X-Dispatch-User", People: store.NewPgPeopleStore(unreachable.Pool, testSigningKey, nil)},
	})

	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/whoami", nil, address)
	answer := decodeBody[struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}](t, response)
	if response.Code != http.StatusInternalServerError || answer.Code != codeInternal {
		t.Fatalf("whoami for a person the store cannot record: status=%d body=%s, want 500 %s", response.Code, response.Body.String(), codeInternal)
	}
	failed := 0
	for _, record := range logs.Records() {
		if strings.Contains(record.Message, address) {
			t.Errorf("a log line's message names the person: %q", record.Message)
		}
		record.Attrs(func(attribute slog.Attr) bool {
			if strings.Contains(attribute.Value.String(), address) {
				t.Errorf("the log line %q names the person in %s: %q", record.Message, attribute.Key, attribute.Value.String())
			}
			return true
		})
		if record.Message == "dispatch: API handler failed" {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("the log holds %d lines saying the handler failed, want the one for this request", failed)
	}
}
