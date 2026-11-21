package natsurl

import "testing"

// Check answers each rule's reason at the position of the URL that breaks it, and lets through
// what nats.go reads as written. The URLs carry no credential: what a refusal prints is held by
// the callers' tests (internal/bus credential_printed_test.go, the daemon's natsauth
// printed_test.go), which put made-up credentials in these places.
func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name     string
		urls     []string
		position int
		reason   string
	}{
		// Check reads each URL alone and models no mixing of websocket and other schemes, which
		// nats.go refuses for a list (ErrMixingWebsocketSchemes), so the rows keep each list to one
		// kind.
		{"a nats and tls list whose URLs each pass", []string{" nats://127.0.0.1:4222 ", "tls://legion@host:4223"}, 0, ""},
		{"a websocket URL with a path and a query that pass", []string{"ws://host/nats?x=1"}, 0, ""},
		{"a first URL with a user name and no scheme", []string{"legion@host:4222"}, 0, ""},
		{"a URL that does not parse", []string{"nats://host:4222", "nats://user:head/tail@host:4222"}, 2, UnparsedReason},
		{"an @ after the host", []string{"nats://head/tail@host:4222"}, 1, UserinfoPastHostReason},
		{"a later URL with a user name and no scheme", []string{"nats://user:8472", "tail@host:4222"}, 2, TailReason},
		{"a later URL with a user name and a scheme no NATS server is written with", []string{"nats://head", "x://tail@host:4222"}, 2, TailReason},
		{"a websocket path that stops parsing once decoded", []string{"ws://host/%25zz?x=1"}, 1, HandshakeReason},
		// nats.go gives a later URL with no scheme the pool's, ws or wss once the first URL names
		// it, and then runs the websocket handshake on it; in a nats list the same URL is no
		// websocket URL and passes.
		{"a ws list's later URL with no scheme whose path stops parsing once decoded", []string{"ws://host:4222", "host:4223/%25zz?x=1"}, 2, HandshakeReason},
		{"a wss list's later URL with no scheme whose path stops parsing once decoded", []string{"WSS://host:4222", "host:4223/%25zz?x=1"}, 2, HandshakeReason},
		{"a nats list's later URL with no scheme and the same path", []string{"nats://host:4222", "host:4223/%25zz?x=1"}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trimmed, position, reason := Check(tc.urls)
			if position != tc.position || reason != tc.reason {
				t.Fatalf("Check = %d, %q; want %d, %q", position, reason, tc.position, tc.reason)
			}
			if len(trimmed) != len(tc.urls) {
				t.Fatalf("Check returned %d URLs for %d", len(trimmed), len(tc.urls))
			}
		})
	}
	if trimmed, _, _ := Check([]string{" nats://host:4222\t"}); trimmed[0] != "nats://host:4222" {
		t.Fatalf("Check kept the white space: %q", trimmed[0])
	}
}

// EntryRefusal refuses a comma, which nats.go reads as a list of servers once its caller joins
// the entries, before Refusal's rules, and is Refusal otherwise.
func TestEntryRefusal(t *testing.T) {
	for _, tc := range []struct{ server, reason string }{
		{"nats://host:4222", ""},
		{"nats://host:4222,nats://host:4223", CommaReason},
		{"nats://" + "user" + ":" + "head,tail@host:4222", CommaReason},
		{"nats://head/tail@host:4222", UserinfoPastHostReason},
		{"ws://host/%25zz", HandshakeReason},
	} {
		if got := EntryRefusal(tc.server); got != tc.reason {
			t.Errorf("EntryRefusal(%q) = %q, want %q", tc.server, got, tc.reason)
		}
	}
}
