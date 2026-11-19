package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// The clean stop is one DELETE of the session's own key, with the listener's bearer, and it names
// both of the conditions it holds under: the client instance whose registration it means to
// remove (client_instance_id) and the moment the stop belongs to (if_not_registered_after). An
// instance id is a client's own string, so it is escaped rather than written into the query raw.
func TestDeleteSessionAsksTheSessionRouteWithTheBearerTheMomentAndTheInstance(t *testing.T) {
	var method, path, query, bearer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, query, bearer = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	goneAt := time.Now()

	stopped, err := New(server.URL+"/", "envoy-bearer").DeleteSession(context.Background(), "ses_implementer",
		DeleteConditions{ClientInstanceID: "pi/omp 1+1", NotRegisteredAfter: goneAt})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !stopped.Deleted {
		t.Fatalf("DeleteSession = %+v, want the registration taken as deleted", stopped)
	}
	if method != http.MethodDelete || path != "/v1/sessions/ses_implementer" || bearer != "Bearer envoy-bearer" {
		t.Fatalf("%s %s with %q, want a DELETE of the session's key with the listener's bearer", method, path, bearer)
	}
	want := fmt.Sprintf("if_not_registered_after=%d&client_instance_id=pi%%2Fomp+1%%2B1", goneAt.UnixMilli())
	if query != want {
		t.Fatalf("query = %q, want both conditions, the id escaped: %q", query, want)
	}
}

// An entry that names no client process — one a client predating the field wrote — leaves the
// moment to judge the delete alone, and the parameter is left out rather than sent empty: the
// listener compares no instance for an empty value as for an absent one
// (session.InstanceMatches), so the two are the same delete and the query states only the
// condition the stop really holds.
func TestDeleteSessionLeavesOutAnInstanceTheEntryDoesNotName(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	goneAt := time.Now()

	if _, err := New(server.URL, "").DeleteSession(context.Background(), "ses_old_client",
		DeleteConditions{NotRegisteredAfter: goneAt}); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if want := fmt.Sprintf("if_not_registered_after=%d", goneAt.UnixMilli()); query != want {
		t.Fatalf("query = %q, want the moment alone, %q", query, want)
	}
}

// A listener that kept the registration says so, and the caller is told which it was: that answer
// is the one that stops a slow stop deleting the registration a relaunched process wrote under
// the same session id (dispatch://INFRA-392).
func TestDeleteSessionReportsARegistrationTheListenerKept(t *testing.T) {
	const reason = "the registration belongs to another client instance"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"deleted":false,"reason":"`+reason+`"}`)
	}))
	defer server.Close()

	stopped, err := New(server.URL, "").DeleteSession(context.Background(), "ses_relaunched",
		DeleteConditions{ClientInstanceID: "inst_dead", NotRegisteredAfter: time.Now()})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if stopped.Deleted || stopped.Reason != reason {
		t.Fatalf("DeleteSession = %+v, want the registration kept with the listener's reason", stopped)
	}
}

// A registry that holds no record for the session has already reached the outcome the stop asks
// for, so its 404 is no failure; a refusal that leaves the registration standing is one, and says
// what the listener answered so an operator can see why the session still holds its role.
func TestDeleteSessionTellsAnAlreadyGoneSessionFromARefusal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "the listener removed it", status: http.StatusOK, body: `{"deleted":true}`},
		{name: "an older listener removed it without saying so", status: http.StatusOK, body: ``},
		{name: "the registry holds no record of it", status: http.StatusNotFound, body: `{"error":"session not found"}`},
		{name: "the bus could not be reached", status: http.StatusServiceUnavailable, body: `{"error":"session registry unavailable"}`,
			wantErr: "session registry unavailable"},
		{name: "the bearer is wrong", status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`, wantErr: "401"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			stopped, err := New(server.URL, "").DeleteSession(context.Background(), "ses_implementer",
				DeleteConditions{ClientInstanceID: "inst_dead", NotRegisteredAfter: time.Now()})
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("DeleteSession = %v, want the session taken as stopped", err)
			case tc.wantErr == "" && !stopped.Deleted:
				t.Fatalf("DeleteSession = %+v, want the registration taken as deleted", stopped)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("DeleteSession = %v, want a failure naming %q", err, tc.wantErr)
			}
		})
	}
}

// The read before the stop is one GET of the session's registry entry, with the listener's bearer
// and the session id escaped into the path, and it asks for both selectors. view=registration is
// the registration-only answer, which a listener carrying it gives from the session bucket alone
// — no claims view and no interest record, so no read that the stop does not need can refuse it.
// include=unresponsive is what a listener predating that selector needs in order to answer a
// record past its live window at all, which is every record the stop is owed for: it ignores the
// unknown selector and answers the full row. What the read answers is the entry's own client
// instance and its last registration.
func TestReadSessionRegistrationAsksTheRegistryForTheEntrysInstance(t *testing.T) {
	var method, path, query, bearer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, query, bearer = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"session_id":"ses/dead","client_instance_id":"inst_dead","last_seen":1762560000000}`)
	}))
	defer server.Close()

	registration, err := New(server.URL, "envoy-bearer").ReadSessionRegistration(context.Background(), "ses/dead")
	if err != nil {
		t.Fatalf("ReadSessionRegistration: %v", err)
	}
	want := SessionRegistration{Registered: true, ClientInstanceID: "inst_dead", LastSeen: 1762560000000}
	if registration != want {
		t.Fatalf("ReadSessionRegistration = %+v, want %+v", registration, want)
	}
	if method != http.MethodGet || path != "/v1/registry/ses/dead" || bearer != "Bearer envoy-bearer" {
		t.Fatalf("%s %s with %q, want a GET of the session's registry entry with the listener's bearer", method, path, bearer)
	}
	if query != "include=unresponsive&view=registration" {
		t.Fatalf("query = %q, want both the unresponsive record and the registration view asked for", query)
	}
}

// A registry that holds no record of the session answers 404, and that is the outcome the stop
// wanted rather than a failure: the caller is told there is no registration, with no error, and
// sends no delete.
func TestReadSessionRegistrationReadsA404AsNoRegistration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"no live session ses_gone"}`)
	}))
	defer server.Close()

	registration, err := New(server.URL, "").ReadSessionRegistration(context.Background(), "ses_gone")
	if err != nil {
		t.Fatalf("ReadSessionRegistration = %v, want a registry holding none taken as no failure", err)
	}
	if registration != (SessionRegistration{}) {
		t.Fatalf("ReadSessionRegistration = %+v, want no registration at all", registration)
	}
}

// A listener predating the view selector answers the whole session row — its topics, its roles,
// its capabilities — and nothing bounds how many topics a long-lived session subscribes to, so
// the entry is read within answerBodyLimit rather than refusalBodyLimit, which bounds
// an error body. A row cut off at that limit would not decode, and the stop of the busiest
// session would be reported as a listener that did not answer and send no delete at all.
func TestReadSessionRegistrationReadsAnEntryPastTheRefusalBodyLimit(t *testing.T) {
	topics := make([]string, 0, 400)
	for i := range 400 {
		topics = append(topics, fmt.Sprintf("notifications.dispatch.issue.infra-%04d", i))
	}
	entry, err := json.Marshal(map[string]any{
		"session_id": "ses_busy", "client_instance_id": "inst_busy", "last_seen": 1762560000000,
		"topics": topics,
	})
	if err != nil {
		t.Fatalf("build the entry: %v", err)
	}
	if len(entry) <= refusalBodyLimit {
		t.Fatalf("the entry is %d bytes, want one past the %d-byte refusal limit", len(entry), refusalBodyLimit)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(entry)
	}))
	defer server.Close()

	registration, err := New(server.URL, "").ReadSessionRegistration(context.Background(), "ses_busy")
	if err != nil {
		t.Fatalf("ReadSessionRegistration of a %d-byte entry: %v", len(entry), err)
	}
	want := SessionRegistration{Registered: true, ClientInstanceID: "inst_busy", LastSeen: 1762560000000}
	if registration != want {
		t.Fatalf("ReadSessionRegistration = %+v, want %+v", registration, want)
	}
}

// An answer this client cannot read is no registration it may name in a delete, and neither is a
// listener that refused the read: both are failures, and the refusal names the status so an
// operator can see why no stop was sent.
func TestReadSessionRegistrationTellsARefusalFromAnEntry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "the body is not an entry at all", status: http.StatusOK, body: `not json`, wantErr: "read the registration"},
		{name: "the bus could not be reached", status: http.StatusServiceUnavailable,
			body: `{"error":"session registry unavailable"}`, wantErr: "503"},
		{name: "the listener failed", status: http.StatusInternalServerError, body: `{"error":"boom"}`, wantErr: "500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			registration, err := New(server.URL, "").ReadSessionRegistration(context.Background(), "ses_implementer")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ReadSessionRegistration = %+v, %v, want a failure naming %q", registration, err, tc.wantErr)
			}
		})
	}
}

// heldListener answers each request hold after it arrived, with a 200 and body, or ends it the
// moment the request's own context ends first: a listener that takes hold to answer the clean
// stop's read or delete. It stands in for the connection itself, so in a synctest bubble the hold
// is the bubble's clock and the run waits for nothing.
type heldListener struct {
	hold time.Duration
	body string
}

func (l heldListener) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case <-time.After(l.hold):
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(l.body)), Request: r}, nil
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
}

// The clean stop's read and delete have one bound, the context the daemon's stop gives them (its
// budget, sessionStop.send in packages/daemon/internal/daemon/daemon.go), and none of the client's
// own: requestTimeout bounds a publish and a role lookup alone. So a listener that answers past
// requestTimeout but inside the budget is waited for, and one that answers after the budget is
// given up at the budget. Each row runs in a synctest bubble, whose clock moves only while every
// goroutine in it waits, so the holds and the budget are times on that clock, not on the run's.
func TestTheCleanStopsReadAndDeleteEndAtTheirContextAlone(t *testing.T) {
	const budget = 30 * time.Second
	for _, request := range []struct {
		name string
		body string
		send func(ctx context.Context, p *HTTPPublisher) error
	}{
		{name: "the registry read", body: `{"session_id":"ses_dead","client_instance_id":"inst_dead","last_seen":1762560000000}`,
			send: func(ctx context.Context, p *HTTPPublisher) error {
				_, err := p.ReadSessionRegistration(ctx, "ses_dead")
				return err
			}},
		{name: "the session delete", body: `{"deleted":true}`,
			send: func(ctx context.Context, p *HTTPPublisher) error {
				_, err := p.DeleteSession(ctx, "ses_dead", DeleteConditions{ClientInstanceID: "inst_dead", NotRegisteredAfter: time.Now()})
				return err
			}},
	} {
		for _, hold := range []time.Duration{requestTimeout + time.Second, budget + time.Minute} {
			t.Run(fmt.Sprintf("%s, answered after %s", request.name, hold), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					p := New("http://listener.invalid", "envoy-bearer")
					p.stops.Transport = heldListener{hold: hold, body: request.body}
					ctx, cancel := context.WithTimeout(context.Background(), budget)
					defer cancel()
					began := time.Now()
					err := request.send(ctx, p)
					took := time.Since(began)
					if hold < budget {
						if err != nil || took != hold {
							t.Fatalf("%s answered %s into a budget of %s ended after %s with %v, want the answer it waited for", request.name, hold, budget, took, err)
						}
						return
					}
					if !errors.Is(err, context.DeadlineExceeded) || took != budget {
						t.Fatalf("%s the listener would answer after %s ended after %s with %v, want it given up at its budget of %s", request.name, hold, took, err, budget)
					}
				})
			})
		}
	}
}
