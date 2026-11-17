package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// RoleHolder names every session the listener names as a role's holder, whatever state it gives
// it, since a forget of a session its claim does not hold refuses on every one of them
// (supervise's mayForget, review rounds 17 and 18 of #4220): the live holder of a 200, and the
// lapsed holder of a 404, unresponsive, gone, or with no state from a listener that predates the
// field. A 200 is read within answerBodyLimit, since its title, dir and capabilities are not
// bounded: a holder with many capabilities is named as any other. Only a 404 naming no holder
// under reason `unclaimed`, a role no claim names, is no holder; one naming no holder under any
// other reason, and every other answer, is an error, which the forget refuses on too. The lookup is
// one GET of the role with the listener's bearer.
//
// The controls (the pull request records each run): RoleHolder answering a lapsed holder with no
// state as no holder, as round 17 first did, fails the older listener's row; the 200 read within
// refusalBodyLimit fails the many-capabilities row; any 404 naming no holder read as no holder
// fails the other-reason row.
func TestRoleHolderNamesEveryHolderTheListenerNames(t *testing.T) {
	capabilities := make([]string, 400)
	for i := range capabilities {
		capabilities[i] = fmt.Sprintf("capability-%04d", i)
	}
	long, err := json.Marshal(map[string]any{"role": "legion-x", "holder": "ses_s", "holder_state": "live", "last_seen": 1, "capabilities": capabilities})
	if err != nil {
		t.Fatal(err)
	}
	if len(long) <= refusalBodyLimit {
		t.Fatalf("the long answer is %d bytes, want it past refusalBodyLimit, %d", len(long), refusalBodyLimit)
	}
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		holder, state string
		fails         bool
	}{
		{"a live holder", http.StatusOK, `{"role":"legion-x","holder":"ses_s","holder_state":"live","last_seen":1}`, "ses_s", "live", false},
		{"a live holder with many capabilities", http.StatusOK, string(long), "ses_s", "live", false},
		{"an unresponsive holder", http.StatusNotFound, `{"error":"…","reason":"holder_lapsed","holder":"ses_s","holder_state":"unresponsive"}`, "ses_s", "unresponsive", false},
		{"a gone holder whose claim stands", http.StatusNotFound, `{"error":"…","reason":"holder_lapsed","holder":"ses_s","holder_state":"gone"}`, "ses_s", "gone", false},
		{"an older listener's lapsed holder with no state", http.StatusNotFound, `{"error":"…","reason":"holder_lapsed","holder":"ses_s"}`, "ses_s", "lapsed", false},
		{"a role no claim names", http.StatusNotFound, `{"error":"…","reason":"unclaimed"}`, "", "", false},
		{"a 404 naming no holder under another reason", http.StatusNotFound, `{"error":"…","reason":"holder_lapsed"}`, "", "", true},
		{"a 404 naming no holder and no reason", http.StatusNotFound, `{"error":"no such route"}`, "", "", true},
		{"a 200 naming no holder", http.StatusOK, `{"role":"legion-x"}`, "", "", true},
		{"an unreadable 404", http.StatusNotFound, `not json`, "", "", true},
		{"a refused lookup", http.StatusUnauthorized, `{"error":"unauthorized"}`, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var path, bearer string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path, bearer = r.URL.Path, r.Header.Get("Authorization")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			holder, state, err := New(server.URL, "envoy-bearer").RoleHolder(context.Background(), "legion-legion-legion-942-implementer")
			if path != "/v1/roles/legion-legion-legion-942-implementer" || bearer != "Bearer envoy-bearer" {
				t.Errorf("the lookup asked %q with %q, want the role's path with the listener's bearer", path, bearer)
			}
			if tc.fails {
				if err == nil {
					t.Fatalf("RoleHolder = %q (%q), want an error for that answer", holder, state)
				}
				return
			}
			if err != nil || holder != tc.holder || state != tc.state {
				t.Fatalf("RoleHolder = %q (%q), %v; want %q (%q)", holder, state, err, tc.holder, tc.state)
			}
		})
	}
}
