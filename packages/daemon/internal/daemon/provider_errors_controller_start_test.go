package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/testnats"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// controllerStartSession is the session the error-run rig registers as the project's controller
// (errorRunRig.recordRegistration), which the stand-in below is at the Envoy listener.
const controllerStartSession = "ses-controller"

// A Legion controller starts in this order (packages/pi-legion's controller-session.ts): its
// session is registered with Envoy when Oh My Pi starts, then it registers with the daemon, and
// only once that answers does it claim the controller role and register the controller topic, a
// lane the listener lets only the role's holder follow. The daemon writes the provider-errors
// notice of a run of six as soon as the registration is recorded, so its publish can come before
// the session follows the topic. A message the stream stores before the topic is in the session
// consumer's filter is delivered only while the consumer has not looked past it, and it looks at
// every message stored for it (here the copy the listener kept for the role and hands to the role's
// new holder) and at every pull it makes. So the notice must wait for the session to follow the
// topic: published before that, it reaches nobody, and the run is never told.
//
// The stand-in follows that order against the real Envoy listener; the notice of the run of six
// must reach it once, and once only.
func TestAProviderErrorsNoticeReachesAControllerThatFollowsItsTopicAfterTheRegistration(t *testing.T) {
	r := newErrorRunRig(t)
	for range 6 {
		r.errorTurn()
	}
	listener, controller, runner := r.controllerStart(t)
	role, topic := claim.ControllerToken(r.project), notify.ControllerTopic(r.project)

	// A message for the controller role while no session holds it, which the listener keeps for
	// the role's next holder.
	status, body := listener.publish(map[string]any{
		"topic": notify.RoleTopicPrefix + string(role), "message": controllerKeptMessage,
		"source": "agent", "source_session": idleRigSender, "idempotency_key": "controller-kept-1",
	})
	if status != http.StatusAccepted {
		t.Fatalf("publish to the controller role no session holds = %d %s, want 202 kept", status, body)
	}

	r.registerController()
	r.wantOneNotice("after six turns and the controller's registration,", 6)
	runner.drain()

	controller.claimRole(role)
	testwait.Eventually(t, "the controller to be handed the message kept for its role", func() bool {
		return controller.holding(controllerKeptMessage) == 1
	})
	controller.register(topic)
	runner.drain()

	controller.wantProviderErrorsOnce(runner, "after it followed the controller topic,")
	// Told once: the next admission pass and the next attempts publish nothing more.
	r.offered.Add(1)
	r.runtime.controllerRegistered()
	if notices := r.notices(); len(notices) != 0 {
		t.Fatalf("after the notice reached the controller, the next admission pass left %d provider-errors notices in the outbox, want none: %+v", len(notices), notices)
	}
	runner.drain()
	controller.wantProviderErrorsOnce(runner, "after another admission pass,")
}

// The control: a controller that holds its role and follows the topic before it registers with
// the daemon is told of the run once, by the notice's first attempt.
func TestAProviderErrorsNoticeReachesAControllerThatAlreadyFollowsItsTopic(t *testing.T) {
	r := newErrorRunRig(t)
	for range 6 {
		r.errorTurn()
	}
	_, controller, runner := r.controllerStart(t)
	controller.claimRole(claim.ControllerToken(r.project))
	controller.register(notify.ControllerTopic(r.project))

	r.registerController()
	r.wantOneNotice("after six turns and the controller's registration,", 6)
	runner.drain()
	controller.wantProviderErrorsOnce(runner, "after the notice's first attempt,")
	r.offered.Add(1)
	r.runtime.controllerRegistered()
	runner.drain()
	controller.wantProviderErrorsOnce(runner, "after another admission pass,")
}

// A registered controller whose session does not follow the topic for longer than the outbox's
// longest retry period. The notice's row waits on the outbox's backoff (record.RetryBackoff):
// each attempt is due one backoff after the last, a drain before then runs nothing, and from the
// seventh attempt on every attempt is RetryBackoffMost after the last, so the row never spins. The
// row holds nothing else of its issue behind it: a notice of another kind written for the issue
// after it runs at once. Once the session follows, the next attempt tells it once.
func TestAProviderErrorsNoticeWaitsOnItsBackoffUntilAControllerFollowsAndHoldsNothingBehindIt(t *testing.T) {
	r := newErrorRunRig(t)
	for range 6 {
		r.errorTurn()
	}
	_, controller, runner := r.controllerStart(t)
	r.registerController()
	r.wantOneNotice("after six turns and the controller's registration,", 6)
	tick, err := record.NewOutboxRow(errorRunIssue, record.ControllerNotice{Kind: record.TickNotice}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(context.Background(), r.pool, func(tx pgx.Tx) error {
		return r.runtime.records.Enqueue(context.Background(), tx, tick)
	}); err != nil {
		t.Fatalf("queue a tick notice of %s behind the provider-errors notice: %v", errorRunIssue, err)
	}

	runner.drainAfter(time.Minute)
	var ticks int
	if err := r.pool.QueryRow(context.Background(), "select count(*) from outbox where kind = $1 and payload->>'kind' = $2",
		string(record.OutboxKindControllerNotice), string(record.TickNotice)).Scan(&ticks); err != nil {
		t.Fatal(err)
	}
	if ticks != 0 {
		t.Fatalf("the tick notice of %s written after the provider-errors notice is still in the outbox, want it run while the provider-errors notice waits", errorRunIssue)
	}
	waited := time.Duration(0)
	for attempt := 1; attempt <= 9; attempt++ {
		attempts, dueIn, held := runner.providerErrorsRow()
		if !held || attempts != attempt {
			t.Fatalf("after attempt %d the provider-errors row is held %t with %d attempts, want it waiting with %d", attempt, held, attempts, attempt)
		}
		if want := record.RetryBackoff(attempt - 1); dueIn != want {
			t.Fatalf("after attempt %d the provider-errors row is due in %s, want its backoff of %s", attempt, dueIn, want)
		}
		runner.drainAfter(dueIn / 2)
		if again, _, _ := runner.providerErrorsRow(); again != attempt {
			t.Fatalf("a drain half way through the backoff after attempt %d ran the row: %d attempts", attempt, again)
		}
		runner.drainAfter(dueIn - dueIn/2)
		waited += dueIn
	}
	if waited <= record.RetryBackoffMost {
		t.Fatalf("the case waited %s, want longer than the longest retry period, %s", waited, record.RetryBackoffMost)
	}
	if _, dueIn, _ := runner.providerErrorsRow(); dueIn != record.RetryBackoffMost {
		t.Fatalf("after ten attempts the row is due in %s, want %s", dueIn, record.RetryBackoffMost)
	}

	controller.claimRole(claim.ControllerToken(r.project))
	controller.register(notify.ControllerTopic(r.project))
	runner.drainAfter(record.RetryBackoffMost)
	if _, _, held := runner.providerErrorsRow(); held {
		t.Fatal("the provider-errors row is still in the outbox after the controller followed and its next attempt ran")
	}
	controller.wantProviderErrorsOnce(runner, "after it followed the controller topic past the longest retry period,")
	runner.drain()
	controller.wantProviderErrorsOnce(runner, "after one more drain,")
}

// A controller restarts after its registration and before it follows the topic: the first
// session never follows, and a new session registers with Envoy, registers with the daemon as the
// controller, claims the role and follows. The notice reaches the new session once, and the first
// session nothing; the new registration's admission pass writes no second notice.
func TestAProviderErrorsNoticeReachesTheSessionAControllerRestartsAsBeforeItFollows(t *testing.T) {
	r := newErrorRunRig(t)
	for range 6 {
		r.errorTurn()
	}
	listener, first, runner := r.controllerStart(t)
	r.registerController()
	r.wantOneNotice("after six turns and the controller's registration,", 6)
	runner.drain()
	if _, _, held := runner.providerErrorsRow(); !held {
		t.Fatal("the provider-errors row finished while no session followed the controller topic, want it waiting")
	}

	restarted := newControllerStandin(t, listener, runner.natsURL, controllerStartSession+"-restarted")
	restarted.register()
	stored, found, err := r.st.Controller(r.ctx, r.project)
	if err != nil || !found {
		t.Fatalf("read the controller record = %t, %v", found, err)
	}
	if ok, err := r.st.RegisterController(r.ctx, r.project, stored.Generation, restarted.session, []byte("secret-2"), time.Now(), 0); err != nil || !ok {
		t.Fatalf("register the restarted controller = %t, %v", ok, err)
	}
	r.offered.Add(1)
	r.runtime.controllerRegistered()
	if notices := r.notices(); len(notices) != 1 {
		t.Fatalf("after the restarted controller's registration the outbox holds %d provider-errors notices, want the one still waiting: %+v", len(notices), notices)
	}
	restarted.claimRole(claim.ControllerToken(r.project))
	restarted.register(notify.ControllerTopic(r.project))
	runner.drain()
	restarted.wantProviderErrorsOnce(runner, "after the restarted session followed the controller topic,")
	if n := first.holding(noticeSummary(record.ProviderErrorsNotice, errorRunIssue)); n != 0 {
		t.Fatalf("the first session, which never followed the topic, was delivered the notice %d times, want none", n)
	}
}

// controllerKeptMessage is the message the listener keeps for the controller role while no
// session holds it.
const controllerKeptMessage = "a message for the controller role, kept while no session held it"

// controllerStart is the Envoy side of a controller's start: a real listener on the package's
// NATS server, the controller session's stand-in, registered with its own subject alone as its
// plugin registers it when Oh My Pi starts, and the daemon's outbox publishing through that
// listener.
func (r *errorRunRig) controllerStart(t *testing.T) (*idleRigListener, *controllerStandin, *controllerStartOutbox) {
	t.Helper()
	natsURL := testnats.URL(t)
	listener := startIdleRigListener(t, natsURL, "controller-start")
	controller := newControllerStandin(t, listener, natsURL, controllerStartSession)
	controller.register()
	// The store keeps a row's due moment to the microsecond, so the rig's clock is held to it,
	// and a backoff read back off the row is the span the outbox added.
	clock := time.Now().UTC().Truncate(time.Microsecond)
	runner := &controllerStartOutbox{t: t, at: &clock, natsURL: natsURL, outbox: &outbox{log: quietLogger(), pool: r.pool,
		dispatchProject: errorRunProject, records: r.runtime.records, notices: notify.New(listener.url, ""),
		project: r.project, now: func() time.Time { return clock }}}
	runner.publisher = notify.New(listener.url, "")
	runner.topic = notify.ControllerTopic(r.project)
	return listener, controller, runner
}

// controllerStartOutbox is the daemon's outbox over the rig's database, drained by hand on a clock
// of its own. drain runs it an hour after the last drain, past any backoff a waiting row was
// given, so a row that waits runs again at the next drain; drainAfter runs it a given span later.
type controllerStartOutbox struct {
	t         *testing.T
	at        *time.Time
	natsURL   string
	outbox    *outbox
	publisher *notify.HTTPPublisher
	topic     string
	markers   int
}

func (o *controllerStartOutbox) drain() {
	o.t.Helper()
	o.drainAfter(time.Hour)
}

func (o *controllerStartOutbox) drainAfter(span time.Duration) {
	o.t.Helper()
	*o.at = o.at.Add(span)
	if err := o.outbox.RunOnce(context.Background()); err != nil {
		o.t.Fatalf("drain the outbox: %v", err)
	}
}

// providerErrorsRow is the outbox's provider-errors notice row: its attempts and how long after
// the rig's clock it is due, and false when the outbox holds none.
func (o *controllerStartOutbox) providerErrorsRow() (attempts int, dueIn time.Duration, held bool) {
	o.t.Helper()
	var next time.Time
	err := o.outbox.pool.QueryRow(context.Background(), `select attempts, next_at from outbox
		where kind = $1 and payload->>'kind' = $2`, string(record.OutboxKindControllerNotice), string(record.ProviderErrorsNotice)).Scan(&attempts, &next)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, false
	}
	if err != nil {
		o.t.Fatalf("read the provider-errors row: %v", err)
	}
	return attempts, next.Sub(*o.at), true
}

// marker publishes one more notice to the controller topic and answers its words: the stream
// delivers a session's messages in the order it stored them, so once the controller holds the
// marker it holds every earlier message it will ever be delivered.
func (o *controllerStartOutbox) marker() string {
	o.t.Helper()
	o.markers++
	words := "marker " + strconv.Itoa(o.markers)
	if err := o.publisher.Publish(context.Background(), o.topic, words, record.Notice{Kind: record.TickNotice},
		"controller-start-marker-"+strconv.Itoa(o.markers)); err != nil {
		o.t.Fatalf("publish %s to the controller topic: %v", words, err)
	}
	return words
}

// controllerStandin is a Legion controller's Oh My Pi session as the Envoy listener sees it: a
// session registered with consumer delivery, whose consumer the listener relays to the session's
// own NATS connection, answered `received` and then `done` as a plugin answers each frame
// (idleRigRoleProcess.relayed).
type controllerStandin struct {
	t        *testing.T
	listener *idleRigListener
	session  string
	bus      *nats.Conn
	mu       sync.Mutex
	seen     map[uint64]bool
	messages []idleRigEnvelope
}

func newControllerStandin(t *testing.T, listener *idleRigListener, natsURL, session string) *controllerStandin {
	t.Helper()
	c := &controllerStandin{t: t, listener: listener, session: session, seen: map[uint64]bool{}}
	c.bus = testnats.Connect(t, natsURL)
	if _, err := c.bus.Subscribe(idleRigDeliverSubjectPrefix+session, c.relayed); err != nil {
		t.Fatalf("subscribe to the relay subject of %s: %v", session, err)
	}
	if err := c.bus.Flush(); err != nil {
		t.Fatalf("flush the relay subscription of %s: %v", session, err)
	}
	return c
}

// register registers the session with topics beside its own subject, as the plugin registers
// every subject it follows, with its own consumer relayed to it.
func (c *controllerStandin) register(topics ...string) {
	c.t.Helper()
	if topics == nil {
		topics = []string{}
	}
	status, body := c.listener.call("/v1/interests/subscribe", map[string]any{
		"session_id": c.session, "dir": "/legion/controller", "topics": topics, "host": "127.0.0.1",
		"title": "controller", "self_subscribed": true, "capabilities": []string{"prompt_async"},
		"plugin": "controller-standin", "plugin_version": "0", "delivery": map[string]any{"consumer": true, "epoch": "e1"},
	})
	if status != http.StatusOK {
		c.t.Fatalf("register %s with %v = %d %s", c.session, topics, status, body)
	}
	var answer struct {
		Delivery *struct {
			Mode string `json:"mode"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil || answer.Delivery == nil || answer.Delivery.Mode != idleRigDeliveryConsumer {
		c.t.Fatalf("register %s answered %s, want its own consumer's delivery", c.session, body)
	}
}

// claimRole claims role for the session, as the plugin's role claim does.
func (c *controllerStandin) claimRole(role claim.Token) {
	c.t.Helper()
	if status, body := c.listener.call("/v1/roles/set", map[string]any{"role": string(role), "session_id": c.session}); status != http.StatusOK {
		c.t.Fatalf("claim %s for %s = %d %s", role, c.session, status, body)
	}
}

func (c *controllerStandin) relayed(msg *nats.Msg) {
	if msg.Header.Get(idleRigProbeHeader) == "1" {
		if msg.Reply != "" {
			_ = c.bus.Publish(msg.Reply, []byte(idleRigRelayDone))
		}
		return
	}
	var item idleRigEnvelope
	if json.Unmarshal(msg.Data, &item) != nil || msg.Reply == "" {
		return
	}
	sequence, err := strconv.ParseUint(msg.Header.Get(idleRigStreamSeqHeader), 10, 64)
	if err != nil {
		return
	}
	_ = c.bus.Publish(msg.Reply, []byte(idleRigRelayReceived))
	c.mu.Lock()
	if !c.seen[sequence] {
		c.seen[sequence] = true
		c.messages = append(c.messages, item)
	}
	c.mu.Unlock()
	_ = c.bus.Publish(msg.Reply, []byte(idleRigRelayDone))
	_ = c.bus.Flush()
}

// holding is how many of the messages the session was delivered say words, in their summary or
// their payload.
func (c *controllerStandin) holding(words string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, item := range c.messages {
		if strings.Contains(item.PayloadSummary, words) || strings.Contains(item.Payload, words) {
			n++
		}
	}
	return n
}

// wantProviderErrorsOnce fails the case unless the session was delivered the provider-errors
// notice of the issue exactly once, read once a marker published after it has arrived.
func (c *controllerStandin) wantProviderErrorsOnce(runner *controllerStartOutbox, when string) {
	c.t.Helper()
	marker := runner.marker()
	testwait.Eventually(c.t, "the controller to be delivered "+marker, func() bool { return c.holding(marker) == 1 })
	notice := noticeSummary(record.ProviderErrorsNotice, errorRunIssue)
	if n := c.holding(notice); n != 1 {
		c.mu.Lock()
		delivered := append([]idleRigEnvelope(nil), c.messages...)
		c.mu.Unlock()
		c.t.Fatalf("%s the controller session was delivered %q %d times, want once: %+v", when, notice, n, delivered)
	}
}
