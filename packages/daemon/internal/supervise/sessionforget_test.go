package supervise

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// A session a claim records but does not hold is cleared only once the Envoy listener no longer
// names it as the claim's role holder (mayForget, review round 16 of #4220). The outbox withholds
// the claim's role messages only while it records that session, and the listener keeps naming
// the session as the role's holder while another claim's live process registers it, so a clear
// made then would send the role's messages to that process. The listener is asked once per clear,
// of the claim's own role; the clear is refused while the cleared session holds the role, live or
// unresponsive, and while the listener gives no answer, and the refusal names the claim to
// suspend. It goes on once no claim names the role, another session holds it, or its holder is
// gone. A claim holding its own session forgets as before and asks nothing (R9).
//
// The control (the pull request records the run): mayForget admitting every clear, as today's
// forget does; the refused rows clear the session while the listener names it.
func TestAForgetOfASessionTheClaimDoesNotHoldWaitsForItsRole(t *testing.T) {
	const holder = claim.Token("legion-legion-LEGION-209-tester")
	type answer struct {
		holder, state string
		err           error
	}
	// processless is a claim suspended over session, which holder recorded first when held, its
	// role lookup answering with a.
	processless := func(t *testing.T, held bool, a answer) (*harness, *int) {
		h := newHarness(t)
		h.reach(StateIdle)
		h.must(RequestStop{Claim: h.token})
		h.wantState(StateRetired)
		if held {
			for _, name := range RecordedHolds(session, sessionFile) {
				h.store.setHold(name, holder)
			}
		}
		asked := 0
		h.deps.RoleHolder = func(_ context.Context, role string) (string, string, error) {
			asked++
			if role != string(h.token) {
				t.Errorf("the forget looked up role %s, want the claim's own, %s", role, h.token)
			}
			return a.holder, a.state, a.err
		}
		h.restart()
		return h, &asked
	}
	clears := []struct {
		name  string
		event func(h *harness) Event
	}{
		{"forget-session", func(h *harness) Event { return RequestForgetSession{Claim: h.token, Session: session} }},
		{"retry in a new session", func(h *harness) Event { return RequestRetry{Claim: h.token, FreshSession: true} }},
	}
	for _, clear := range clears {
		for _, tc := range []struct {
			name    string
			a       answer
			refused string
		}{
			{"refused while the listener names the session as the role's live holder", answer{session, "live", nil}, "legion claims suspend --claim " + string(holder)},
			{"refused while that holder is unresponsive", answer{session, "unresponsive", nil}, "legion claims suspend --claim " + string(holder)},
			{"refused while the listener does not answer", answer{"", "", errors.New("the listener is unreachable")}, "did not say which session holds its role"},
			{"admitted once no claim names the role", answer{}, ""},
			{"admitted while another session holds the role", answer{"ses_another", "live", nil}, ""},
			{"admitted once the holder is gone", answer{session, "gone", nil}, ""},
		} {
			t.Run(clear.name+": "+tc.name, func(t *testing.T) {
				h, asked := processless(t, true, tc.a)
				err := h.handle(clear.event(h))
				if *asked != 1 {
					t.Errorf("the listener was asked %d times, want once", *asked)
				}
				if tc.refused == "" {
					if err != nil {
						t.Fatalf("the clear = %v, want it admitted", err)
					}
					if c := h.claim(); c.Session != "" || c.Abandoned.Session != session {
						t.Fatalf("the claim records %q (abandoned %q), want %s cleared", c.Session, c.Abandoned.Session, session)
					}
					return
				}
				var refused *RefusedError
				if !errors.As(err, &refused) || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("the clear = %v, want it refused naming %q", err, tc.refused)
				}
				if c := h.claim(); c.Session != session || c.SessionFile != sessionFile {
					t.Fatalf("the refused clear left the claim recording %q at %q, want %s kept", c.Session, c.SessionFile, session)
				}
			})
		}
	}
	t.Run("a claim holding its own session forgets without asking", func(t *testing.T) {
		h, asked := processless(t, false, answer{session, "live", nil})
		h.must(RequestForgetSession{Claim: h.token, Session: session})
		if *asked != 0 {
			t.Fatalf("the listener was asked %d times, want none: the claim holds %s", *asked, session)
		}
		if c := h.claim(); c.Session != "" {
			t.Fatalf("the claim still records %q", c.Session)
		}
	})
}
