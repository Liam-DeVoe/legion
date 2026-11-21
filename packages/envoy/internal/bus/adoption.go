package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
)

// serverMaxWaiting is the max waiting nats-server gives a pull consumer created with none
// (JSWaitQueueDefaultMax), as every session consumer is.
const serverMaxWaiting = 512

// adoptedConfig is a consumer's configuration as the server answers its info, in the settings no
// family's correction in place reaches: those nats-server refuses to change by an update (v2.15.1
// consumer.go checkNewConsumerConfig, :2542-2595) beside the start, which consumernames.Names checks,
// and headers-only, which an update could change but no family's correction resets.
// jetstream.ConsumerConfig decodes none of the push settings, nor direct or sourcing, so they are
// read from the server's own answer. The policies are read as the server spells them, so a policy
// nats.go has no name for is refused rather than failing the read.
type adoptedConfig struct {
	AckPolicy      string        `json:"ack_policy"`
	OptStartTime   *time.Time    `json:"opt_start_time"`
	ReplayPolicy   string        `json:"replay_policy"`
	MemoryStorage  bool          `json:"mem_storage"`
	HeadersOnly    bool          `json:"headers_only"`
	MaxWaiting     int           `json:"max_waiting"`
	DeliverSubject string        `json:"deliver_subject"`
	DeliverGroup   string        `json:"deliver_group"`
	FlowControl    bool          `json:"flow_control"`
	Heartbeat      time.Duration `json:"idle_heartbeat"`
	Direct         bool          `json:"direct"`
	Sourcing       bool          `json:"sourcing"`
}

// refusals names each setting of c that a family's create never gives: explicit acks, no start
// time, instant replay, the stream's storage, whole messages, no deliver group, flow control, idle
// heartbeat, direct or sourcing; and, by how the family delivers, a pull consumer at the server's
// default max waiting (a session consumer's), or a push consumer delivering to a reply inbox, as
// nats.NewInbox makes one, with no max waiting (the listener's durable).
func (c adoptedConfig) refusals(push bool) []string {
	var why []string
	refuse := func(refused bool, format string, args ...any) {
		if refused {
			why = append(why, fmt.Sprintf(format, args...))
		}
	}
	refuse(c.AckPolicy != "explicit", "ack policy %s, not explicit", c.AckPolicy)
	refuse(c.OptStartTime != nil, "a start time")
	refuse(c.ReplayPolicy != "instant", "replay policy %s, not instant", c.ReplayPolicy)
	refuse(c.MemoryStorage, "memory storage")
	refuse(c.HeadersOnly, "headers only")
	refuse(c.DeliverGroup != "", "the deliver group %q", c.DeliverGroup)
	refuse(c.FlowControl, "flow control")
	refuse(c.Heartbeat != 0, "an idle heartbeat of %s", c.Heartbeat)
	refuse(c.Direct, "direct")
	refuse(c.Sourcing, "sourcing")
	if push {
		refuse(!replyInbox(c.DeliverSubject), "the deliver subject %q, no reply inbox", c.DeliverSubject)
		refuse(c.MaxWaiting != 0, "max waiting %d", c.MaxWaiting)
	} else {
		refuse(c.DeliverSubject != "", "the deliver subject %q, as a push consumer", c.DeliverSubject)
		refuse(c.MaxWaiting != serverMaxWaiting, "max waiting %d, not the server's default %d", c.MaxWaiting, serverMaxWaiting)
	}
	return why
}

// replyInbox reports whether subject is a reply inbox as nats.NewInbox makes one: nats.InboxPrefix
// and one token.
func replyInbox(subject string) bool {
	token, ok := strings.CutPrefix(subject, nats.InboxPrefix)
	return ok && token != "" && !strings.ContainsAny(token, ".*> ")
}

// ConsumerAdoptable reads consumer name of the stream on c, gated as every consumer call is, and
// refuses it as consumernames.ErrUnexpectedConsumer, naming it and each such setting, when it is
// not as its family's create makes it in a setting no correction in place reaches (adoptedConfig):
// push is set for a family that creates push consumers delivering to a reply inbox (the listener's
// durable), and clear for one that creates pull consumers (a session's). A read that fails is its
// own error, never a refusal. A family's consumernames.Config.Adopt asks here; the settings an
// update changes are the family's to correct in place after it.
func (c *Client) ConsumerAdoptable(ctx context.Context, name string, push bool) error {
	reply, err := c.requestGated(ctx, &nats.Msg{Subject: consumerAPI("INFO", name)})
	if err != nil {
		return err
	}
	var response struct {
		Config adoptedConfig       `json:"config"`
		Error  *jetstream.APIError `json:"error"`
	}
	if err := json.Unmarshal(reply.Data, &response); err != nil {
		return fmt.Errorf("bus: read the answer of consumer %s's info: %w", name, err)
	}
	if response.Error != nil {
		return fmt.Errorf("bus: read consumer %s: %w", name, response.Error)
	}
	if why := response.Config.refusals(push); len(why) > 0 {
		return fmt.Errorf("%w: %s has %s, which its family's create never gives and no update corrects", consumernames.ErrUnexpectedConsumer, name, strings.Join(why, ", "))
	}
	return nil
}
