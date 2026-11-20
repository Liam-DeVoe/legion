package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The registered controller held its role and followed the controller topic, and then its
// process died while the listener still read it live (its last registration inside the live
// window). The outbox's read names it the role's live holder, the listener forwards the
// provider-errors notice to its own subject, and the row finishes on the listener's 200: the
// notice is stored for a session nothing runs. An operator then starts a replacement controller,
// which registers with Envoy and with the daemon and claims the role, and it must be told once.
//
// The listener hands it the role messages the dead session did not read: while the dead session's
// claim stands, both its session relay (sessionRelays.takeOverFrame, until the session leaves the
// listener's session cache) and the takeover record the replacement's claim writes
// (role_takeover.go) copy the notice to the role's kept lane, whose walk hands it to the
// replacement; once the role claim reaper has freed the dead session's claim, the record the
// reaper writes before it frees it does (packages/envoy/cmd/listener's reapedClaimRecord). These
// cases take the replacement at each of those three moments, and a control whose first
// controller stays connected and reads the notice.

// controllerReplaced is when a case's replacement controller claims the role.
type controllerReplaced int

const (
	// replacedWhileLive: the dead controller still reads live (the shipped five-minute live
	// window).
	replacedWhileLive controllerReplaced = iota
	// replacedOnceUnresponsive: the dead controller reads unresponsive, its claim still standing
	// (a live window of controllerAwayWindow, the gone horizon the shipped hour).
	replacedOnceUnresponsive
	// replacedAfterTheReap: the listener's role claim reaper has freed the dead controller's claim
	// (live window and gone horizon controllerAwayWindow, both reapers every two seconds), so the
	// replacement claims a role no claim names.
	replacedAfterTheReap
)

// controllerReplacedAfterTheNotice runs the case: the first controller registers, claims its
// role and follows the topic; when dies, its process ends before the run of six (its NATS
// connection closed while its registration stays live); the notice is published to it and the
// row finishes; then a replacement claims the role at when. It answers how many times the
// replacement and the first controller were delivered the provider-errors notice, read once a
// further ten seconds have passed, so a second copy that comes later is counted too.
func controllerReplacedAfterTheNotice(t *testing.T, when controllerReplaced, dies bool) (replacementHolds, firstHolds int) {
	t.Helper()
	switch when {
	case replacedOnceUnresponsive:
		t.Setenv("ENVOY_SESSION_LIVE_WINDOW", controllerAwayWindow.String())
	case replacedAfterTheReap:
		t.Setenv("ENVOY_SESSION_LIVE_WINDOW", controllerAwayWindow.String())
		t.Setenv("ENVOY_SESSION_GONE_HORIZON", controllerAwayWindow.String())
		t.Setenv("ENVOY_ROLE_CLAIM_REAP_INTERVAL", "2s")
		t.Setenv("ENVOY_SESSION_REAP_INTERVAL", "2s")
	}
	r := newErrorRunRig(t)
	listener, _, runner := r.controllerStart(t)
	role, topic := claim.ControllerToken(r.project), notify.ControllerTopic(r.project)
	notice := noticeSummary(record.ProviderErrorsNotice, errorRunIssue)

	first, bus := newControllerStandinWithBus(t, listener, runner.natsURL, controllerStartSession+"-first")
	first.register()
	stop := first.heartbeat(topic)
	r.replaceController(first.session)
	first.claimRole(role)
	first.register(topic)
	runner.drain()
	marker := runner.marker()
	testwait.Eventually(t, "the first controller to be delivered "+marker+" before the run", func() bool { return first.holding(marker) == 1 })
	if dies {
		bus.Close()
	}
	if holder, state, err := runner.outbox.notices.RoleHolder(t.Context(), string(role)); err != nil || holder != first.session || state != notify.StateLive {
		t.Fatalf("precondition: the listener names %q %q (%v) as the holder of %s, want %s live", holder, state, err, role, first.session)
	}
	for range 6 {
		r.errorTurn()
	}
	r.wantOneNotice("after six turns with the controller registered,", 6)
	runner.drain()
	if _, _, held := runner.providerErrorsRow(); held {
		t.Fatalf("precondition: the provider-errors row is still held after its publish to %s, which the listener reads live", first.session)
	}
	if dies {
		stop()
	}
	switch when {
	case replacedOnceUnresponsive:
		first.awaitUnresponsive()
	case replacedAfterTheReap:
		idleRigEventually(t, 3*time.Minute, "the role claim reaper freeing "+first.session+"'s claim of "+string(role), func() bool {
			response, err := listener.client.Get(listener.url + "/v1/roles/" + string(role))
			if err != nil {
				return false
			}
			defer response.Body.Close()
			var body struct {
				Holder string `json:"holder"`
			}
			_ = json.NewDecoder(response.Body).Decode(&body)
			return response.StatusCode == http.StatusNotFound && body.Holder == ""
		})
	}
	if dies {
		if n := first.holding(notice); n != 0 {
			t.Fatalf("precondition: the dead controller's relay took the notice %d times", n)
		}
	}

	replacement := newControllerStandin(t, listener, runner.natsURL, controllerStartSession+"-replacement")
	replacement.register()
	replacement.heartbeat()
	r.replaceController(replacement.session)
	replacement.claimRole(role)
	replacement.register(topic)
	runner.drain()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && replacement.holding(notice) == 0 && (dies || first.holding(notice) == 0) {
		time.Sleep(250 * time.Millisecond)
	}
	time.Sleep(10 * time.Second)
	return replacement.holding(notice), first.holding(notice)
}

func TestAProviderErrorsNoticeForAControllerThatDiedReachesTheReplacementThatClaimsWhileItReadsLive(t *testing.T) {
	if got, _ := controllerReplacedAfterTheNotice(t, replacedWhileLive, true); got != 1 {
		t.Fatalf("the replacement controller was delivered the provider-errors notice %d times, want once", got)
	}
}

func TestAProviderErrorsNoticeForAControllerThatDiedReachesTheReplacementThatClaimsOnceItReadsUnresponsive(t *testing.T) {
	if got, _ := controllerReplacedAfterTheNotice(t, replacedOnceUnresponsive, true); got != 1 {
		t.Fatalf("the replacement controller was delivered the provider-errors notice %d times, want once", got)
	}
}

func TestAProviderErrorsNoticeForAControllerThatDiedReachesTheReplacementThatClaimsAfterTheReaperFreedItsClaim(t *testing.T) {
	if got, _ := controllerReplacedAfterTheNotice(t, replacedAfterTheReap, true); got != 1 {
		t.Fatalf("the replacement controller was delivered the provider-errors notice %d times, want once", got)
	}
}

// The control: the first controller stays connected and reads the notice, so nothing is left
// unread for the replacement, and the run is told once in all.
func TestAProviderErrorsNoticeReadByALiveControllerIsNotHandedToItsReplacement(t *testing.T) {
	if replacement, first := controllerReplacedAfterTheNotice(t, replacedWhileLive, false); first != 1 || replacement != 0 {
		t.Fatalf("the live first controller holds the notice %d times and its replacement %d times, want once and none", first, replacement)
	}
}
