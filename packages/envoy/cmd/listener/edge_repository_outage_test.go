package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
)

// A GitHub event of a repository the session follows, stored while no fresh repository answer
// stands, is withheld and not lost: the relay leaves it unacknowledged, and once an answer naming
// the repository stands again the consumer delivers it once more and the relay hands the session
// that very event. While the answer is unknown the session's agent message, stored after it, is
// relayed first, so the event did not reach the session then. The control is an event of the same
// repository relayed before the outage.
func TestAGitHubEventWaitsOutAnUnknownRepositoryAnswer(t *testing.T) {
	rig := newEdgeRig(t)
	const sessionID = "ses_edge_outage"
	token := rig.personToken(sessionID, "PRIV")
	standIn := startSessionStandIn(t, rig.url, sessionID, answerDone)
	requireStatus(t, rig.subscribe(t, token, sessionID, "notifications.github.acme.pub.>"), http.StatusOK, "the registration")
	before := rig.storeGithubEvent(t, "acme/pub", "pr.1")
	requireFrames(t, standIn.collect(t, 1, 10*time.Second), before)

	rig.repositories.failWith(http.StatusServiceUnavailable)
	rig.clock.advance(edgeRepositoryAnswerLife + time.Second)
	held := rig.storeGithubEvent(t, "acme/pub", "pr.2")
	agent, _ := rig.storeEvent(t, contracts.LegacyAgentSubject(sessionID))
	if frames := standIn.collect(t, 1, 10*time.Second); frames[0].item.EventID != agent.EventID {
		t.Fatalf("the session's next frame is %s on %s, want the agent message: the acme/pub event %s was relayed with no fresh answer",
			frames[0].item.EventID, frames[0].item.Topic, held.EventID)
	}

	rig.repositories.failWith(0)
	rig.tick(t)
	recovered := standIn.collect(t, 1, 2*edgeRepositoryRefresh+10*time.Second)
	if recovered[0].item.EventID != held.EventID {
		t.Fatalf("the session's next frame after the answer stood again is %s on %s, want the acme/pub event %s withheld while it was unknown",
			recovered[0].item.EventID, recovered[0].item.Topic, held.EventID)
	}
}
