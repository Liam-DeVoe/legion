//go:build multiprocess

// Two listener builds for one machine id, as a roll from one release to the next runs them
// (INFRA-557): one build that compiles in a stream subject the other does not know, and, when
// ENVOY_LISTENER_OLD_BINARY names one, a build from before the delivery lease. Each row starts the
// builds' real binaries through multiprocess_harness_test.go's harness.
// TestTwoBuildsSkipOrDoubleNoMessage checks that no message on any subject is skipped, doubled, or
// dropped by both while the machine's delivery passes from one build to the other.
// TestTwoBuildsAnOlderPodRelaysATopicOnlyTheNewerBuildsSubjectCaptures checks that while the older
// build holds the machine's delivery, a session it registered receives a message the newer build
// publishes on a topic only the newer build's subject captures. Like the rows beside them they use
// nothing a build before INFRA-557 (819dc283) lacks but cmdtest.Build's tags, so they run there
// with that harness beside them.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/cmdtest"
)

const (
	// tbEvery is how often a row publishes, each time on the next of its topics in turn.
	tbEvery = 40 * time.Millisecond
	// tbNewerSubject is the stream subject only the envoy_twobuild build compiles in
	// (internal/bus/stream_extra_twobuild.go).
	tbNewerSubject = "notifications.twobuild.>"
	// tbSubjectsRead bounds how long a plain pod that connected before the other build added
	// tbNewerSubject takes to judge a topic on it captured: its client reads the deployed stream's
	// subjects at each ensure of the stream, which runs at connect and after every reconnect, and
	// every streamSubjectsRefresh (internal/bus/nats.go, a minute) from its connect, each read
	// within streamSubjectsRead (10 s). The 5 s past those is the registrations that observe it.
	tbSubjectsRead = 75 * time.Second
)

// tbTargets is a topic on every subject the plain build's stream stores (bus.StreamSubjects),
// each with its own session, named for the subject so a report names the subject that lost or
// doubled a message. A subject no session may follow is left out: /v1/interests/subscribe drops
// a topic reaching a private stream subject other than the session's own agent subject
// (bus.ReachesPrivateStreamSubject: the Legion controller lane and the failed-delivery lanes),
// so a session on one would never be relayed.
//
// `notifications.twobuild.>`, the subject only the other build compiles in
// (internal/bus/stream_extra_twobuild.go), gets no session in this row, whose plain pod registers
// every session before a pod of the other build has added the subject to the stream;
// TestTwoBuildsAnOlderPodRelaysATopicOnlyTheNewerBuildsSubjectCaptures measures one. A pod of the
// plain build serves it: a registration's followed topics and a session consumer's filter are
// judged on the subjects the deployed stream carries, as the pod's own client last read them
// (bus.Client.DeployedStreamCaptures, read at each ensure of the stream, which runs at connect and
// after every reconnect, and every streamSubjectsRefresh, a minute), and no build's ensure drops a
// subject it does not compile in. So once a pod of the other build has added the subject, a
// session following a topic on it is served by a pod of either build that has read the stream
// since, whichever build holds the term; a pod that has not read it yet leaves the topic out of a
// registration and names it in the answer's warning.
func tbTargets() []mpTarget {
	subjects := bus.StreamSubjects()
	var targets []mpTarget
	for _, subject := range subjects {
		prefix := strings.TrimSuffix(subject, ">")
		name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(prefix, bus.NotificationTopicPrefix), "."), ".", "_")
		sessionID := "ses_tb_" + name
		if prefix == "notifications.agent." {
			targets = append(targets, mpInbox(sessionID))
			continue
		}
		topic := prefix + "mp-twobuild"
		// The target holds no role, so a controller notice lane is private to it as every other
		// private subject is (bus.ReachesPrivateStreamSubject's roles, a parameter 2af83d53fc (#2603) added).
		if bus.ReachesPrivateStreamSubject(sessionID, topic, nil) {
			continue
		}
		targets = append(targets, mpTarget{topic: topic, session: sessionID})
	}
	return targets
}

// tbRegister registers every target's session through the pod, on consumer delivery, subscribed to
// its topic unless the topic is its own inbox.
func tbRegister(t *testing.T, pod *mpPod, serving *mpNATSSession, targets []mpTarget) {
	t.Helper()
	for _, target := range targets {
		if target == mpInbox(target.session) {
			pod.registerNATS(t, serving, target.session)
			continue
		}
		pod.registerNATS(t, serving, target.session, target.topic)
	}
}

// tbKill SIGKILLs the pod between two publishes, once every message published so far has reached
// its session and the durable holds none unacknowledged, and resumes publishing a second after the
// pod has exited. A message a killed pod holds is TestTwoPodsSIGKILLHandsOverInOrderWithin15s's;
// here the kill is the hand-over from one build to the other. Nor is a message sent over core
// NATS, which the server hands the killed pod before it sees that pod's connection close,
// publishable then: a core message is delivered at most once, and the second after the exit is
// the server's to drop the dead pod's subscription. A message the previous steps skipped is
// named here, by awaitAll, before the kill.
func tbKill(t *testing.T, publisher *mpPublisher, serving *mpNATSSession, pod *mpPod) time.Time {
	t.Helper()
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	serving.awaitAll(t, slices.Clone(publisher.sent), 30*time.Second)
	mpEventually(t, 10*time.Second, "the durable to hold no message unacknowledged", func() bool { return publisher.ackPending() == 0 })
	killed := pod.kill(t)
	pod.WaitExit(t, "SIGKILL")
	time.Sleep(time.Second)
	return killed
}

// tbRequireEachOnce waits for every message to reach its session, then past the ack wait for any
// repeat, and fails the test unless each reached it exactly once.
func tbRequireEachOnce(t *testing.T, publisher *mpPublisher, serving *mpNATSSession) {
	t.Helper()
	messages := publisher.all()
	serving.awaitAll(t, messages, 30*time.Second)
	time.Sleep(mpQuiet)
	serving.requireKnown(t, messages)
	serving.requireOnce(t, messages)
	perSession := map[string]int{}
	for _, m := range messages {
		perSession[m.session]++
	}
	t.Logf("%d messages, each delivered exactly once: %v", len(messages), perSession)
}

// No message on any subject is skipped, doubled, or dropped by both while two builds share the
// machine's delivery: a plain build and one built with the envoy_twobuild tag, which compiles in a
// stream subject more, through a graceful and a hard take-over in each direction. The plain build is
// active and the other build standby; a SIGTERM of the plain pod makes the other build active; a
// plain pod starts as its standby; a SIGKILL of the other build makes the plain build active again.
// A message is published every 40 ms throughout, on each subject the plain build's stream stores
// (tbTargets), each to a session of its own: each session takes each of its messages exactly once.
// The subject only the other build adds has no session here: a session following it is
// TestTwoBuildsAnOlderPodRelaysATopicOnlyTheNewerBuildsSubjectCaptures's.
func TestTwoBuildsSkipOrDoubleNoMessage(t *testing.T) {
	plain := cmdtest.Build(t, "envoy-listener")

	t.Run("a build with a stream subject more", func(t *testing.T) {
		extra := cmdtest.Build(t, "envoy-listener-twobuild", "envoy_twobuild")
		uri := mpNATS(t)
		serving := mpStartNATSSessions(t, uri)
		targets := tbTargets()
		plainA := mpStartPod(t, plain, uri, "pod-a")
		mpWaitActive(t, plainA, 30*time.Second)
		tbRegister(t, plainA, serving, targets)
		publisher := mpConnectPublisher(t, uri)
		stop := publisher.everyOn(tbEvery, targets...)
		time.Sleep(3 * time.Second)

		extraB := mpStartPod(t, extra, uri, "pod-b")
		mpWaitStandby(t, extraB, 30*time.Second)
		time.Sleep(3 * time.Second)

		signalled := plainA.Terminate(t)
		// A build before INFRA-557 keeps the durable through its shutdown, and the standby binds
		// only after it.
		mpWaitActive(t, extraB, 60*time.Second)
		t.Logf("%s (envoy_twobuild) took over %s after %s's SIGTERM", extraB.instance, time.Since(signalled).Round(time.Millisecond), plainA.instance)
		plainA.WaitExit(t, "SIGTERM")
		time.Sleep(3 * time.Second)

		plainC := mpStartPod(t, plain, uri, "pod-c")
		mpWaitStandby(t, plainC, 30*time.Second)
		time.Sleep(3 * time.Second)

		killed := tbKill(t, publisher, serving, extraB)
		mpWaitActive(t, plainC, 30*time.Second)
		t.Logf("%s (plain) took over %s after %s's SIGKILL", plainC.instance, time.Since(killed).Round(time.Millisecond), extraB.instance)
		time.Sleep(3 * time.Second)
		stop()

		tbRequireEachOnce(t, publisher, serving)
	})
}

// While two builds serve the machine during a roll, the older build delivers a message on a topic
// only the newer build's stream subject captures: the plain pod A is the machine's active pod and
// so runs the relay of the session's consumer, the envoy_twobuild pod B is its standby, B publishes
// on a topic under notifications.twobuild.> through its own POST /v1/messages/publish, and the
// session A registered takes that message exactly once within mpGracefulBound of the publish, A
// active and B standby at the publish and at the receipt.
//
// B's start puts the subject on the deployed stream: every pod, a standby too, connects through
// bus.ConnectOwningStream, whose ensure (ensureStreamWithConfig) adds the subjects its build
// compiles and keeps every one it finds, which the row reads back from the stream itself. A, which
// connected before B, reads the stream's subjects again at its next streamSubjectsRefresh. That
// read has no line of its own, so the row watches for it where it shows: a registration through A
// of a probe session following another topic under the subject, which A answers with a warning
// naming the topic until it has read the subject (splitUncapturedTopics), once a second until
// tbSubjectsRead. The session itself then registers through A once, which answers 200 storing the
// topic and with no warning.
//
// Before #2461 the plain build filtered a session's consumer on the subjects it compiles
// (bus.StreamCaptures) rather than on the deployed stream's (bus.Client.DeployedStreamCaptures,
// sessionDelivery.sessionConsumerFilter), so the topic was out of the session consumer's filter
// and the message never reached the session.
func TestTwoBuildsAnOlderPodRelaysATopicOnlyTheNewerBuildsSubjectCaptures(t *testing.T) {
	const (
		sessionID = "ses_tb_newer_subject"
		topic     = "notifications.twobuild.mp-newer-subject"
		probeID   = "ses_tb_newer_subject_probe"
		probe     = "notifications.twobuild.mp-newer-subject-probe"
		event     = "evt-tb-newer-subject"
	)
	if bus.StreamCaptures(topic) {
		t.Fatalf("the plain build compiles a subject capturing %s; the row needs a topic only the envoy_twobuild build's subject captures", topic)
	}
	plain := cmdtest.Build(t, "envoy-listener")
	newer := cmdtest.Build(t, "envoy-listener-twobuild", "envoy_twobuild")
	uri := mpNATS(t)
	serving := mpStartNATSSessions(t, uri)
	mpJournalOf(t).readsFrom(uri)

	started := time.Now()
	plainA := mpStartPod(t, plain, uri, "pod-a")
	mpWaitActive(t, plainA, 30*time.Second)
	newerB := mpStartPod(t, newer, uri, "pod-b")
	mpWaitStandby(t, newerB, 30*time.Second)
	added := time.Now()
	tbRequireStreamSubject(t, uri, tbNewerSubject)

	tbAwaitCaptured(t, plainA, serving, probeID, probe, tbSubjectsRead)
	t.Logf("%s (plain) judged %s captured %s after %s (envoy_twobuild) was standby, %s after %s started",
		plainA.instance, probe, time.Since(added).Round(time.Millisecond), newerB.instance, time.Since(started).Round(time.Millisecond), plainA.instance)
	if topics, warnings := tbRegisterOn(t, plainA, serving, sessionID, topic); !slices.Contains(topics, topic) || len(warnings) > 0 {
		t.Fatalf("%s's registration through %s stored %v with warnings %q; want %s stored and no warning", sessionID, plainA.instance, topics, warnings, topic)
	}

	tbRequireRoles(t, plainA, newerB, "at the publish")
	published := time.Now()
	body := fmt.Sprintf(`{"topic":%q,"message":%q}`, topic, "a message the newer build publishes mp-event="+event)
	if answer := newerB.call(http.MethodPost, "/v1/messages/publish", body); answer.status != http.StatusOK {
		t.Fatalf("publish %s through %s: status %d %s %v", body, newerB.instance, answer.status, answer.body, answer.err)
	}
	m := mpMessage{event: event, session: sessionID, published: published}
	mpJournalOf(t).publish(m, topic)
	serving.awaitAll(t, []mpMessage{m}, mpGracefulBound)
	tbRequireRoles(t, plainA, newerB, "at the receipt")
	if at, took := serving.first(sessionID, event); took {
		t.Logf("%s reached %s %s after %s (envoy_twobuild, standby) published it on %s, relayed by %s (plain, active)",
			event, sessionID, at.Sub(published).Round(time.Millisecond), newerB.instance, topic, plainA.instance)
	}
	time.Sleep(mpQuiet)
	serving.requireKnown(t, []mpMessage{m})
	serving.requireOnce(t, []mpMessage{m})
}

// tbRequireStreamSubject fails the test unless the deployed stream carries subject, as a client of
// the test's own reads the stream.
func tbRequireStreamSubject(t *testing.T, natsURL, subject string) {
	t.Helper()
	client, err := bus.Connect([]string{natsURL})
	if err != nil {
		t.Fatalf("connect the stream's reader: %v", err)
	}
	defer client.Close()
	info, err := client.JS().StreamInfo(bus.Stream)
	if err != nil {
		t.Fatalf("read the stream %s: %v", bus.Stream, err)
	}
	if !slices.Contains(info.Config.Subjects, subject) {
		t.Fatalf("the stream %s carries %v, without %s", bus.Stream, info.Config.Subjects, subject)
	}
}

// tbAwaitCaptured registers sessionID through the pod following topic, once a second, until the
// pod answers with no warning, and fails the test when it still warns past within. A pod that has
// not read the stream's subjects since the subject capturing topic was added leaves the topic out
// of the registration and names it in the answer's warning, so the first answer without one is the
// pod's read of that subject.
func tbAwaitCaptured(t *testing.T, pod *mpPod, serving *mpNATSSession, sessionID, topic string, within time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(within); ; time.Sleep(time.Second) {
		_, warnings := tbRegisterOn(t, pod, serving, sessionID, topic)
		if len(warnings) == 0 {
			return
		}
		for _, warning := range warnings {
			if !strings.Contains(warning, topic) {
				t.Fatalf("%s's registration through %s warned of more than %s: %q", sessionID, pod.instance, topic, warnings)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still left %s out of %s's registration %s after the other build added its subject: %q", pod.instance, topic, sessionID, within, warnings)
		}
	}
}

// tbRegisterOn registers sessionID through the pod on consumer delivery, following topic, requires
// a 200, and returns the topics the answer stored and its warnings.
func tbRegisterOn(t *testing.T, pod *mpPod, serving *mpNATSSession, sessionID, topic string) (topics, warnings []string) {
	t.Helper()
	body := mpNATSRegistration(t, serving, sessionID, topic)
	answer := pod.call(http.MethodPost, "/v1/interests/subscribe", body)
	if answer.status != http.StatusOK {
		t.Fatalf("subscribe %s through %s: status %d %s %v", body, pod.instance, answer.status, answer.body, answer.err)
	}
	var registered struct {
		Topics   []string `json:"topics"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(answer.body), &registered); err != nil {
		t.Fatalf("decode %s's registration answer %s: %v", sessionID, answer.body, err)
	}
	return registered.Topics, registered.Warnings
}

// tbRequireRoles fails the test unless active is the machine's active pod and standby its standby,
// at the moment when names.
func tbRequireRoles(t *testing.T, active, standby *mpPod, when string) {
	t.Helper()
	if !active.active() {
		t.Fatalf("%s %s is not the machine's active pod", when, active.instance)
	}
	if !standby.standby() {
		t.Fatalf("%s %s is not the machine's standby", when, standby.instance)
	}
}
