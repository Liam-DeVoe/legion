package bus

import (
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// A Request answers ErrUnavailable as soon as the life its window pinned ends, though the client's
// report of the change that ended it woke the Request's wait first. nats.go reports a connection's
// loss twice: to connLife's status listener, whose drain goroutine ends the life, and to the
// client's disconnect callback, which closes ConnectionChanged. The two run on goroutines of their
// own, in either order, so the wait can wake on the change, read the life as current and wait
// again, and the life then ends with nothing to wake it: TestARequestWhoseConnectionIsLostAnswers-
// Unavailable waited out its 10 s on a loaded box that way. The row drives that order itself on a
// live connection: the change report, then, once the wait has taken its next channel, the life's
// end alone. The negative control is the same Request with no life's end, which nothing answers
// before its timeout.
func TestARequestAnswersTheEndOfItsLifeAfterTheChangeThatWokeIt(t *testing.T) {
	client, err := ConnectOwningStream([]string{testnats.URL(t)})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	// A role subject: the stream captures agent subjects alone, so nothing acknowledges the
	// Request, and only its timeout or the end of its window's life answers it.
	const subject = "notifications.role.request-life-ends"
	request := func(name string, timeout time.Duration) (*window, <-chan error) {
		w := client.WindowUntil(nil).(*window)
		item := contracts.Envelope{
			EventID: "evt-" + name, Source: "dispatch", SourceEventID: "src-" + name,
			Topic: "notifications.role.request-test", DedupeKey: "envoy.role.forward.dispatch-" + name, IssuedAt: contracts.NowMillis(),
		}
		return w, w.Request(subject, item, RequestOptions{MsgID: ForwardMsgID(name, subject), Timeout: timeout})
	}
	// rearmed reports whether a wait has taken ConnectionChanged's next channel since the last
	// report closed it: nothing else on this idle client takes one.
	rearmed := func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.connChanged != nil
	}

	t.Run("control: a change report alone answers nothing before the timeout", func(t *testing.T) {
		_, answer := request("change-alone", 500*time.Millisecond)
		testwait.WithinPolling(t, 5*time.Second, time.Millisecond, "the Request's wait to take ConnectionChanged", rearmed)
		client.connectionChanged()
		testwait.WithinPolling(t, 5*time.Second, time.Millisecond, "the woken wait to take ConnectionChanged again", rearmed)
		select {
		case err := <-answer:
			if !errors.Is(err, ErrNotAcknowledged) || errors.Is(err, ErrUnavailable) {
				t.Fatalf("a Request whose life did not end answered %v, want ErrNotAcknowledged at its timeout", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a Request with a 500ms timeout gave no answer within 5s")
		}
	})

	t.Run("the life ends after the change report woke the wait", func(t *testing.T) {
		w, answer := request("life-ends", 30*time.Second)
		testwait.WithinPolling(t, 5*time.Second, time.Millisecond, "the Request's wait to take ConnectionChanged", rearmed)
		client.connectionChanged()
		testwait.WithinPolling(t, 5*time.Second, time.Millisecond, "the woken wait to take ConnectionChanged again", rearmed)
		lifeOf(w.conn).end(nats.DISCONNECTED)
		select {
		case err := <-answer:
			if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotAcknowledged) {
				t.Fatalf("a Request whose life ended answered %v, want ErrUnavailable", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a Request whose life ended after the change report gave no answer within 5s: its wait read the life before it ended, and nothing woke it again")
		}
	})
}
