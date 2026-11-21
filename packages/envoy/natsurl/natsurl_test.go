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
		{"a list nats.go reads as written", []string{" nats://127.0.0.1:4222 ", "tls://legion@host:4223", "ws://host/nats?x=1"}, 0, ""},
		{"a first URL with a user name and no scheme", []string{"legion@host:4222"}, 0, ""},
		{"a URL that does not parse", []string{"nats://host:4222", "nats://user:head/tail@host:4222"}, 2, UnparsedReason},
		{"an @ after the host", []string{"nats://head/tail@host:4222"}, 1, UserinfoPastHostReason},
		{"a later URL with a user name and no scheme", []string{"nats://user:8472", "tail@host:4222"}, 2, TailReason},
		{"a later URL with a user name and a scheme nats.go does not dial", []string{"nats://head", "x://tail@host:4222"}, 2, TailReason},
		{"a websocket path that stops parsing once decoded", []string{"ws://host/%25zz?x=1"}, 1, HandshakeReason},
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
