package envoy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/envoy"
)

// The listener's URL (ENVOY_URL) may carry a user and a password, and net/http's transport error
// quotes the request's URL with the user kept and a token written as the whole user part whole. A
// read and a send whose listener cannot be reached fail naming no part of it, with the failure's
// class. The control is the same URL with no user part, whose errors name the same class. The user
// part's values are made for this test.
func TestAListenerURLWithAUserPartIsNamedWithNoneOfIt(t *testing.T) {
	const (
		user     = "made-up-user-7c4d"
		password = "not-a-secret-e83a15"
		token    = "not-a-secret-tok9b6"
	)
	for _, userPart := range []string{user + ":" + password + "@", token + "@", ""} {
		client := envoy.New("http://" + userPart + "127.0.0.1:1")
		_, readErr := client.Sessions(context.Background())
		_, sendErr := client.Send(context.Background(), envoy.SendInput{TargetSession: "made-up-session", Message: "hello", IdempotencyKey: "made-up-key"})
		for name, err := range map[string]error{"the read": readErr, "the send": sendErr} {
			if err == nil || !strings.Contains(err.Error(), "connection refused") {
				t.Fatalf("user part %q, %s: err = %v, want the refused connection's class", userPart, name, err)
			}
			for _, part := range []string{user, password, token} {
				if strings.Contains(err.Error(), part) {
					t.Errorf("user part %q, %s: the error holds a part of the user part: %q", userPart, name, err)
				}
			}
		}
	}
}
