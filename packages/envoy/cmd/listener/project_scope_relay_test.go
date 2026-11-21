package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/session"
	"github.com/sjawhar/envoy/internal/testwait"
)

// The subjects the relay rows store: a Dispatch issue event of each project, which a session
// following notifications.dispatch.issue.> takes on its consumer, and which the relay decides by
// the project the subject names (contracts.SubjectProject).
const (
	privIssueSubject = "notifications.dispatch.issue.PRIV.PRIV-1.comment.created"
	pubIssueSubject  = "notifications.dispatch.issue.PUB.PUB-1.comment.created"
	everyIssueTopic  = "notifications.dispatch.issue.>"
)

// privacyRig is a listener's /v1 and session relays in a privacy mode, every piece real but the
// broker and the privacy source: the edge rig of a listener serving no edge projects (its broker
// stub and credential), the privacy source's stand-in (privacySource), and the listener's privacy
// mode wired as main wires it, its counters on the rig's registry.
type privacyRig struct {
	*edgeRig
	source  *privacySource
	privacy *listenerPrivacy
}

// newPrivacyRig is the rig of a listener in the required mode, or the all-public mode when
// required is false; its source answers that no project is private until a row says otherwise.
func newPrivacyRig(t *testing.T, required bool) *privacyRig {
	t.Helper()
	edge := newEdgeRigServing(t, nil)
	source := newPrivacySource(t, privacyNone)
	logger := edge.harness.config.logger
	privacy := &listenerPrivacy{required: required, counters: newPrivacyCounters(edge.met), logger: logger}
	if required {
		privacy.k7 = newK7Reader(source.server.URL, listenerBrokerAccount(edgeAccountToken), privacy.counters.unknown, logger)
	}
	edge.deps.privacy = privacy
	edge.relays.setPrivacy(privacy)
	return &privacyRig{edgeRig: edge, source: source, privacy: privacy}
}

// memberToken is a token the broker answers as session's, acting for a person who is a restricted
// member or a full member, its project scope person and its list projects.
func (r *privacyRig) memberToken(session string, restricted bool, projects ...string) string {
	token := "member-token-of-" + session
	answer := sessionAnswer(session, "member@example.com", r.caller.JKT())
	answer.Projects = slices.Sorted(slices.Values(projects))
	answer.Restricted = &restricted
	r.broker.Says(token, answer)
	return token
}

// session registers sessionID for consumer delivery under a member token, following topics, and
// returns the stand-in its frames reach.
func (r *privacyRig) session(t *testing.T, sessionID string, restricted bool, projects []string, topics ...string) *sessionStandIn {
	t.Helper()
	standIn := startSessionStandIn(t, r.url, sessionID, answerDone)
	requireStatus(t, r.subscribe(t, r.memberToken(sessionID, restricted, projects...), sessionID, topics...), http.StatusOK, "the registration of "+sessionID)
	return standIn
}

// counter is the value the rig's /metrics reports for series, which must be present.
func (r *privacyRig) counter(t *testing.T, series string) int64 {
	t.Helper()
	recorder := httptest.NewRecorder()
	r.met.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return counterValue(t, recorder.Body.String(), series)
}

// requireHoldGap fails the test unless frame carries the privacy hold's gap mark.
func requireHoldGap(t *testing.T, frame relayedFrame, asked string) {
	t.Helper()
	if frame.msg.Header.Get(relayHeaderGap) != "true" || frame.msg.Header.Get(relayHeaderGapReason) != privacyHoldGap {
		t.Fatalf("%s: frame %d carries Envoy-Gap %q and Envoy-Gap-Reason %q, want true and %s", asked, frame.seq,
			frame.msg.Header.Get(relayHeaderGap), frame.msg.Header.Get(relayHeaderGapReason), privacyHoldGap)
	}
}

// requireNoGap fails the test when frame carries a gap mark.
func requireNoGap(t *testing.T, frame relayedFrame, asked string) {
	t.Helper()
	if gap := frame.msg.Header.Get(relayHeaderGap); gap != "" {
		t.Fatalf("%s: frame %d carries Envoy-Gap %q (reason %q), want none", asked, frame.seq, gap, frame.msg.Header.Get(relayHeaderGapReason))
	}
}

// TestTheHoldDoesNotStall: 1,100 PRIV frames are held for E while the privacy read is unknown,
// then E's own frame and a PUB frame, which E's token lists: E receives both at once, the first
// marked Envoy-Gap with reason privacy_hold once a full buffer was dropped, the second unmarked,
// and no PRIV frame; the hold dropped four full lanes (1,024 frames) and kept the last 76; when a
// read then answers PRIV public, E receives those 76 in stream order. Held frames are acknowledged
// upstream, so the consumer's MaxAckPending (1,024) never fills with them. Control
// (TestTheHoldDoesNotStallControl): with a lane wide enough to hold all 1,100, nothing is dropped,
// and E's own frame carries no gap mark, so the gap row fails for the drop it names.
func TestTheHoldDoesNotStall(t *testing.T) {
	rig, standIn, priv := holdPRIVFrames(t, 1100, holdLimits.laneFrames)
	own, _ := rig.storeEvent(t, contracts.LegacyAgentSubject("ses_hold_e"))
	pub, _ := rig.storeEvent(t, pubIssueSubject)
	frames := standIn.collect(t, 2, 60*time.Second)
	requireFrames(t, frames, own, pub)
	requireHoldGap(t, frames[0], "E's own frame after the drops")
	requireNoGap(t, frames[1], "the PUB frame after it")
	if dropped := rig.counter(t, `envoy_listener_held_dropped_total{reason="`+heldDroppedLane+`"}`); dropped != 1024 {
		t.Fatalf("the hold dropped %d frames as full lanes, want 1024 (four lanes of 256)", dropped)
	}
	rig.source.answer(privacyNone)
	requireFrames(t, standIn.collect(t, 76, 60*time.Second), priv[1024:]...)
	standIn.none(t, time.Second)
}

// TestTheHoldDoesNotStallControl is TestTheHoldDoesNotStall's control: a lane wide enough for every
// frame drops none, so E's own frame carries no gap mark, and all 1,100 PRIV frames reach E once a
// read answers.
func TestTheHoldDoesNotStallControl(t *testing.T) {
	rig, standIn, priv := holdPRIVFrames(t, 1100, 2048)
	own, _ := rig.storeEvent(t, contracts.LegacyAgentSubject("ses_hold_e"))
	frames := standIn.collect(t, 1, 60*time.Second)
	requireFrames(t, frames, own)
	requireNoGap(t, frames[0], "control: E's own frame with no frame dropped")
	rig.source.answer(privacyNone)
	requireFrames(t, standIn.collect(t, len(priv), 60*time.Second), priv...)
}

// holdPRIVFrames is the start both hold rows share: a required-mode rig whose lanes hold
// laneFrames, E registered as a full member's session listing PUB and following every issue event,
// the source failing, and n PRIV frames stored, which the relay holds for E.
func holdPRIVFrames(t *testing.T, n, laneFrames int) (*privacyRig, *sessionStandIn, []contracts.Envelope) {
	t.Helper()
	limits := holdLimits
	holdLimits.laneFrames = laneFrames
	holdLimits.redecide = 500 * time.Millisecond
	t.Cleanup(func() { holdLimits = limits })
	rig := newPrivacyRig(t, true)
	standIn := rig.session(t, "ses_hold_e", false, []string{"PUB"}, everyIssueTopic)
	rig.source.failWith(http.StatusBadGateway)
	priv := make([]contracts.Envelope, 0, n)
	for range n {
		item, _ := rig.storeEvent(t, privIssueSubject)
		priv = append(priv, item)
	}
	return rig, standIn, priv
}

// TestARestartWhileFramesAreHeldMarksTheGap: a listener that starts relaying a session whose record
// carries the held marker (its predecessor held frames for it, which are lost) marks the session's
// first frame Envoy-Gap with reason privacy_hold and clears the marker. Control: the same session
// with no marker gets its first frame unmarked, so the row fails for the marker it names.
func TestARestartWhileFramesAreHeldMarksTheGap(t *testing.T) {
	for _, marked := range []bool{true, false} {
		t.Run(fmt.Sprintf("held marker %v", marked), func(t *testing.T) {
			rig := newPrivacyRig(t, true)
			sessionID := fmt.Sprintf("ses_restart_held_%v", marked)
			full := false
			if err := rig.deps.sessions.Put(sessionID, session.SessionEntry{MachineID: "test-machine", Projects: []string{}, Restricted: &full, Held: marked}); err != nil {
				t.Fatalf("write the predecessor's record: %v", err)
			}
			standIn := rig.session(t, sessionID, false, nil)
			own, _ := rig.storeEvent(t, contracts.LegacyAgentSubject(sessionID))
			frames := standIn.collect(t, 1, 30*time.Second)
			requireFrames(t, frames, own)
			if !marked {
				requireNoGap(t, frames[0], "control: the first frame of a session with no held marker")
				return
			}
			requireHoldGap(t, frames[0], "the first frame after a restart that lost held frames")
			testwait.Eventually(t, "the held marker to be cleared", func() bool {
				entry, err := rig.deps.sessions.Get(sessionID)
				return err == nil && !entry.Held
			})
		})
	}
}

// TestEveryTokenGetsTheGitHubEventsItsInterestNames: in the required mode a GitHub event is a
// message of no project that every token sees, so each session whose interest names its
// repository gets it, whatever its token lists or whether its person is restricted, and no read
// of the privacy source decides it. Controls, each breaking the interest check: a session that
// follows another repository, and one that follows no GitHub topic, get nothing.
func TestEveryTokenGetsTheGitHubEventsItsInterestNames(t *testing.T) {
	rig := newPrivacyRig(t, true)
	rig.source.answer(privacyListingPRIV)
	widgets := "notifications.github.acme.widgets.>"
	restricted := rig.session(t, "ses_github_restricted", true, []string{"PRIV"}, widgets)
	full := rig.session(t, "ses_github_full", false, nil, widgets)
	other := rig.session(t, "ses_github_other_repo", true, []string{"OPS"}, "notifications.github.acme.gadgets.>")
	none := rig.session(t, "ses_github_none", false, nil)
	before := rig.source.reads()
	event := rig.storeGithubEvent(t, "acme/widgets", "pull_request.4.opened")
	requireFrames(t, restricted.collect(t, 1, 30*time.Second), event)
	requireFrames(t, full.collect(t, 1, 30*time.Second), event)
	other.none(t, time.Second)
	none.none(t, time.Second)
	if reads := rig.source.reads(); reads != before {
		t.Fatalf("relaying a GitHub event read the privacy source %d times, want none", reads-before)
	}
}

// TestARouteAnswers503WhileK7CannotAnswer: with the privacy read unknown, POST /v1/roles/set of a
// PRIV role by a full member's session whose token lists no project answers 503 refilling and
// writes no claim. Control: the source answering that no project is private; the claim is
// written, so the row fails for the unknown read it names.
func TestARouteAnswers503WhileK7CannotAnswer(t *testing.T) {
	rig := newPrivacyRig(t, true)
	const holder = "ses_route_503"
	token := rig.memberToken(holder, false)
	requireStatus(t, rig.subscribe(t, token, holder), http.StatusOK, "the holder's registration")
	claim := fmt.Sprintf(`{"session_id":%q,"role":"PRIV.lead-reviewer","soft":true}`, holder)
	rig.source.failWith(http.StatusBadGateway)
	answer := rig.call(t, http.MethodPost, token, "/v1/roles/set", claim)
	requireStatus(t, answer, http.StatusServiceUnavailable, "a claim of PRIV.lead-reviewer while the privacy read fails")
	if got, err := rig.deps.registry.RoleClaim(t.Context(), contracts.RoleClaimKey("PRIV", "lead-reviewer")); err != nil || got.HolderSessionID != "" {
		t.Fatalf("the refused claim wrote holder %q, %v; want no claim", got.HolderSessionID, err)
	}
	rig.source.answer(privacyNone)
	requireStatus(t, rig.call(t, http.MethodPost, token, "/v1/roles/set", claim), http.StatusOK, "control: the same claim once the source answers")
	if got, err := rig.deps.registry.RoleClaim(t.Context(), contracts.RoleClaimKey("PRIV", "lead-reviewer")); err != nil || got.HolderSessionID != holder {
		t.Fatalf("control: the claim wrote holder %q, %v; want %s", got.HolderSessionID, err, holder)
	}
}
