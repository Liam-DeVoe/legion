package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/store/storetext"
)

// A dispatch_url whose made-up password holds an unencoded "@" leaves the password's tail where
// the host goes, and the resolver's error names that host: the client's error, and the text the
// outbox stores as a write's last_error (storetext.FarSideError, which the state route serves as
// pendingStatusWrites[].lastError), hold no part of the tail, in its ASCII form or in the `xn--`
// form the client dials a tail that is not ASCII by. The error is still a TransientError, so a
// later attempt is still made.
func TestTheDispatchClientsErrorHoldsNoTailOfThePassword(t *testing.T) {
	passwordAt := "legion:p" + "@"
	for name, tail := range map[string]string{
		"an ASCII tail":            "madeuptail0123.invalid",
		"a tail that is not ASCII": "madeuptail0123\u00f6rd.invalid",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New("http://"+passwordAt+tail, "dispatch-token").Handoff(context.Background(), "handoff-1")
			if err == nil {
				t.Fatal("Handoff = nil, want the failed dial")
			}
			var transient *TransientError
			if !errors.As(err, &transient) {
				t.Errorf("Handoff = %v, want a TransientError", err)
			}
			for what, text := range map[string]string{"the error": err.Error(), "the stored last_error": storetext.FarSideError(err.Error())} {
				if lower := strings.ToLower(text); strings.Contains(lower, "madeuptail") || strings.Contains(lower, "xn--") {
					t.Errorf("%s %q holds part of the password's tail", what, text)
				}
			}
		})
	}
}
