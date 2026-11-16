//go:build natslive

// A session consumer's own messages, in both forms of its agent subject, behind other sessions'
// traffic, against a real nats-server image. The module links nats-server v2.15.1 in process,
// whose scan of a consumer's stored messages sees every filter, so only an image can run the
// server whose scan does not: nats-server v2.10.29, the copy of nats:2.10 the natslive matrix and
// the Legion daemon's rig run (serverScansEveryFilter says why, and which servers).
//
// The file sits behind the natslive tag for the reason stream_max_consumers_live_test.go gives, and
// the envoy-go CI job runs it in the same step.

package bus

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

// otherSessionsTraffic is how many messages for other sessions each case stores between the
// consumer's position and its own messages: more than the distinct subjects a message block holds
// once they are stored, which is where nats-server v2.10.29 stops reading a multi-filter
// consumer's stored messages one by one and reads them by subject instead
// (server/filestore.go:2267, firstMatchingMulti, `mb.fss.Size() < lseq-start`). Measured on
// v2.10.29 before the fix, with the calls each case below makes: with none of this traffic both
// cases passed, and with 20 messages over two other sessions' subjects both failed.
const otherSessionsTraffic = 24

// The consumer a session gets from the listener is handed every message stored on its own agent
// subject, in the old form and under a project, however much other sessions' traffic stands
// between its position and that message, on the server the hub runs and on nats:2.10, and is
// handed nothing of that traffic. Each case drives the calls cmd/listener makes, in its order: a
// registration's Ensure, the relay's RestartAtFloor and Pull; a release's Delete; and a
// registration again.
//
// On v2.10.29 the consumer filtering on both forms (notifications.agent.*.<session> beside
// notifications.agent.<session>) skipped the old-form message in both cases, before the fix, and
// handed over the new-form one alone: the server's read by subject
// (server/sublist.go:1750-1784, intersectStree) follows the wildcard at a level and never the
// literal beside it, so it finds nothing on the old form, and the consumer then moves its next
// sequence past the stream's last (server/consumer.go:3700-3708, getNextMsg), while its info
// still reports the delivered sequence it had (server/consumer.go:2705-2708). A consumer on the
// old form alone, the merge base's filter, created at the same start over the same stored
// messages, was handed the old-form message there.
//
//   - "stored while the session was released" is the Legion daemon's idle release: the
//     registration's create starts the consumer at the session's resume record, below the
//     traffic and the messages stored while it had none.
//   - "sent while the pull waits" is a live session: the pull has already found the stream's end,
//     and the traffic is stored without waking the consumer, since none of it is on its filters.
//
// Each case also reads the filters the server was given: both forms as they are on the hub's
// server, and on v2.10.29 the old form widened to every session's (scanSafeFilters), whose other
// sessions' messages the pull acknowledges and never hands over (handsOver), which the traffic
// stored after the session's own messages checks.
func TestASessionConsumerHandsOverItsOwnMessagesInBothFormsBehindOtherSessionsTraffic(t *testing.T) {
	const session = "ses_scan"
	subjects := []string{contracts.LegacyAgentSubject(session)}
	filters := map[string][]string{
		"hub":  {"notifications.agent.*.ses_scan", "notifications.agent.ses_scan"},
		"2.10": {"notifications.agent.*", "notifications.agent.*.ses_scan"},
	}
	requireFilters := func(t *testing.T, consumers *SessionConsumers, image string) {
		t.Helper()
		info, err := consumers.Info(context.Background(), session)
		if err != nil {
			t.Fatalf("read the consumer of %s: %v", session, err)
		}
		want, ok := filters[image]
		if !ok {
			t.Fatalf("no filters are written down for image %s", image)
		}
		if !slices.Equal(info.Config.FilterSubjects, want) {
			t.Fatalf("the consumer of %s filters on %q on %s, want %q", session, info.Config.FilterSubjects, image, want)
		}
	}
	for _, image := range testnats.Images {
		t.Run(image.Name+"/stored while the session was released", func(t *testing.T) {
			owner, consumers := liveSessionConsumers(t, image.Ref)
			ctx := context.Background()
			register := func() *SessionPull {
				t.Helper()
				if _, err := consumers.Ensure(ctx, session, subjects, 0); err != nil {
					t.Fatalf("ensure the consumer of %s: %v", session, err)
				}
				if _, err := consumers.RestartAtFloor(ctx, session, subjects); err != nil {
					t.Fatalf("restart the consumer of %s at its floor: %v", session, err)
				}
				requireFilters(t, consumers, image.Name)
				return pullSession(t, consumers, session)
			}
			publishOthers(t, owner, 2)
			register().Stop()
			if _, err := consumers.Delete(ctx, session, nil); err != nil {
				t.Fatalf("delete the consumer of %s at its release: %v", session, err)
			}
			publishOthers(t, owner, otherSessionsTraffic)
			want := publishOwn(t, owner, session)
			publishOthers(t, owner, 4)
			requireHandedOver(t, register(), want)
		})
		t.Run(image.Name+"/sent while the pull waits", func(t *testing.T) {
			owner, consumers := liveSessionConsumers(t, image.Ref)
			ctx := context.Background()
			publishOthers(t, owner, 2)
			if _, err := consumers.Ensure(ctx, session, subjects, 0); err != nil {
				t.Fatalf("ensure the consumer of %s: %v", session, err)
			}
			if _, err := consumers.RestartAtFloor(ctx, session, subjects); err != nil {
				t.Fatalf("restart the consumer of %s at its floor: %v", session, err)
			}
			requireFilters(t, consumers, image.Name)
			pull := pullSession(t, consumers, session)
			// The pull request reaches the server and the consumer reads to the stream's end before
			// the traffic is stored: nothing is handed over meanwhile.
			requireNoMessage(t, pull, liveQuietWindow)
			publishOthers(t, owner, otherSessionsTraffic)
			requireHandedOver(t, pull, publishOwn(t, owner, session))
		})
	}
}

// liveQuietWindow is how long a case waits to see that a pull hands over nothing.
const liveQuietWindow = 500 * time.Millisecond

// liveSessionConsumers starts image, owns the stream on it and connects SessionConsumers beside
// the owner, both closed when the test ends.
func liveSessionConsumers(t *testing.T, image string) (*Client, *SessionConsumers) {
	t.Helper()
	_, uri := testnats.StartImageConfig(t, image, "jetstream {}\n", liveServerArgs, true)
	owner, err := ConnectOwningStream([]string{uri}, WithEnvironment(liveServerEnvironment))
	if err != nil {
		t.Fatalf("connect the stream's owner: %v", err)
	}
	t.Cleanup(owner.Close)
	consumers, err := ConnectSessionConsumers([]string{uri}, 0, nil, WithEnvironment(liveServerEnvironment))
	if err != nil {
		t.Fatalf("connect session consumers: %v", err)
	}
	t.Cleanup(consumers.Close)
	return owner, consumers
}

// publishOthers stores n messages on two other sessions' agent subjects, in the old form, in turn.
func publishOthers(t *testing.T, owner *Client, n int) {
	t.Helper()
	for i := range n {
		publishStored(t, owner, contracts.LegacyAgentSubject(fmt.Sprintf("ses_other_%d", i%2)), "another session's message")
	}
}

// ownMessage is one message stored for the session: its stream sequence and subject.
type ownMessage struct {
	seq     uint64
	subject string
}

// publishOwn stores one message on session's agent subject in each form, the old form first, and
// returns them in stream order.
func publishOwn(t *testing.T, owner *Client, session string) []ownMessage {
	t.Helper()
	var own []ownMessage
	for _, subject := range []string{contracts.LegacyAgentSubject(session), contracts.AgentSubject("INFRA", session)} {
		own = append(own, ownMessage{seq: publishStored(t, owner, subject, "the session's own message"), subject: subject})
	}
	return own
}

// requireHandedOver requires pull to hand over want, in order, and nothing after it.
func requireHandedOver(t *testing.T, pull *SessionPull, want []ownMessage) {
	t.Helper()
	for _, message := range want {
		got := next(t, pull)
		if got.StreamSeq != message.seq || got.Subject != message.subject {
			t.Fatalf("handed over %d on %s, want %d on %s", got.StreamSeq, got.Subject, message.seq, message.subject)
		}
		if err := got.Ack(); err != nil {
			t.Fatalf("ack %d: %v", got.StreamSeq, err)
		}
	}
	requireNoMessage(t, pull, liveQuietWindow)
}
