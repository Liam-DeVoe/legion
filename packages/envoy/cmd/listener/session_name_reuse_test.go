package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/brokertoken/brokertokentest"
	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/internal/testwait"
)

// reusedSessionSecondProjects is the projects the broker token of the session name's second
// registration lists: the public session account's, as the broker's exchange mints it once the
// hold on the name has run out (the broker's TestAReusedSessionNameCarriesOnlyItsPodsProjects).
var reusedSessionSecondProjects = []string{"PUB"}

// TestAHeldPrivateMessageIsNotDeliveredToAReusedSessionName: a session name can be registered
// again by a pod of another account, under a broker token that lists another project, while the
// session's consumer still holds messages stored for the name's earlier holder. In the required
// privacy mode the relay decides each frame it delivers against the projects the session's
// current registration recorded (session.SessionEntry.Projects, from the registering token), so
// the held message of PRIV, a private project, never reaches the session once it is registered
// under a token listing PUB alone, and the held message of PUB does.
//
// The session is registered first under a token listing PRIV, and both messages are stored while
// no relay runs for it (the rig holds no durable binding yet, as while a session is away and its
// relay is stopped), so its consumer holds both undecided when it registers again. The relay
// then starts, and decides each in stream order: PRIV's first.
//
// Controls (each run by hand, then reverted): give the second registration a token listing PRIV,
// which is what an exchange that took a token's projects from the session name would mint; run
// the relay and the routes in the all-public mode (no setPrivacy); and make the relay deliver a
// session whose token lists projects every project's frame (sessionRelays.decideFrame). Under
// each the PRIV message arrives, and the test fails naming it.
func TestAHeldPrivateMessageIsNotDeliveredToAReusedSessionName(t *testing.T) {
	const sessionID = "ses_reused_name"
	rig := newRelayRigUnowned(t, relayPauseFirst, relayPauseMax)
	source := newPrivacySource(t, privacyListingPRIV)
	privacy := &listenerPrivacy{required: true, k7: testK7Reader(source.server.URL, k7ReadTimeout), counters: newPrivacyCounters(metrics.New())}
	rig.deps.privacy = privacy
	rig.relays.setPrivacy(privacy)
	// The routes read the privacy mode they were mounted with, as main mounts them after it.
	rig.mux = http.NewServeMux()
	registerV1Routes(rig.mux, rig.deps, "test-machine", rig.harness.config.logger)
	broker := brokertokentest.NewStubBroker(t)
	handler := apiAuth(listenerAPIAuth{brokers: listenerBrokerTokens(t, broker), logger: rig.harness.config.logger}, rig.mux)
	register := func(t *testing.T, caller *brokertokentest.Caller, token, epoch string, projects []string) {
		t.Helper()
		answer := sessionAnswer(sessionID, "session-person@example.com", caller.JKT())
		answer.ProjectScope, answer.Projects = brokertoken.ProjectScopeNamed, projects
		broker.Says(token, answer)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, brokerTokenRequest(t, caller, http.MethodPost, "/v1/interests/subscribe",
			token, consumerRegistration(sessionID, epoch, "notifications.dispatch.issue.>")))
		if recorder.Code != http.StatusOK {
			t.Fatalf("register %s at %s under a token listing %v: status %d (%s), want 200", sessionID, epoch, projects, recorder.Code, recorder.Body.String())
		}
		entry, err := rig.deps.sessions.Refresh(t.Context(), sessionID)
		if err != nil || !slices.Equal(entry.Projects, projects) {
			t.Fatalf("%s's entry after its registration at %s records projects %v (%v), want %v", sessionID, epoch, entry.Projects, err, projects)
		}
	}

	register(t, brokertokentest.NewCaller(t), "token-of-the-earlier-holder", "epoch-1", []string{"PRIV"})
	awaitSessionConsumerStored(t, rig.harness.client.Conn, sessionID)
	private, privateSeq := rig.storeEvent(t, "notifications.dispatch.issue.PRIV-1.updated")
	public, publicSeq := rig.storeEvent(t, "notifications.dispatch.issue.PUB-1.updated")

	register(t, brokertokentest.NewCaller(t), "token-of-the-reusing-pod", "epoch-2", reusedSessionSecondProjects)
	standIn := startSessionStandIn(t, rig.url, sessionID, answerDone)
	rig.relays.setOwned(true)

	frames := standIn.collect(t, 1, 10*time.Second)
	if frames[0].item.EventID == private.EventID {
		t.Fatalf("%s, registered again under a token listing %v, received the held PRIV message %s (seq %d): a private project's message reached a reused session name",
			sessionID, reusedSessionSecondProjects, private.EventID, privateSeq)
	}
	requireFrames(t, frames, public)
	// The PUB message is answered done, and the PRIV one before it was decided first, so the
	// consumer's ack floor passing both is the relay having withheld the PRIV message, not a
	// delivery still on its way.
	testwait.Eventually(t, "the session's consumer to have acknowledged both held messages", func() bool {
		info, err := rig.consumers.Info(context.Background(), sessionID)
		return err == nil && info.NumAckPending == 0 && info.AckFloor.Stream >= publicSeq
	})
	select {
	case frame := <-standIn.frames:
		t.Fatalf("%s received a second frame, seq %d event %s, want none: the PRIV message %s is withheld", sessionID, frame.seq, frame.item.EventID, private.EventID)
	default:
	}
}
