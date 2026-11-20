package main

import (
	"net/http"
	"testing"
)

// TestLeaverHoldBrokerCredentials is the broker half of c-leaver-end-to-end. A person whose
// Google refresh has been refused is held at Dispatch before this call. The next new enrollment
// under their machine login must be refused by the real broker. Removing the broker hold gate
// makes this fail with 201, so it proves the gate is on this route rather than merely that
// Dispatch held the person's personal token.
func TestLeaverHoldBrokerCredentials(t *testing.T) {
	r := newLeaverRig(t)
	ada := r.person(t, adaEmail)

	r.refuse(adaEmail)
	r.ageConfirmation(t, adaEmail, confirmationAge)
	expectAnswer(t, "ada@example.com's personal token, the first request after Google refuses her", http.StatusUnauthorized, "MEMBERSHIP_HELD").got(r.personalToken(t, ada.dsp))

	expectAnswer(t, "ada@example.com's new enrollment after Dispatch holds her", http.StatusForbidden, "MEMBERSHIP_HELD").got(r.enrol(t, ada))
}
