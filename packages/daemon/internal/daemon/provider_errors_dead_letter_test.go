package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/testnats"
	"github.com/sjawhar/legion/daemon/internal/testtmp"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The registered controller's process died while the listener still read it live, so the
// provider-errors notice was forwarded to its own subject and the row finished on the listener's
// 200. The listener's role claim reaper then freed the dead controller's claim and wrote its
// takeover record, and the next controller claimed the role more than a kept copy's hour later:
// the listener copies the notice to the role's kept lane and drops the copy unread, with a dead
// letter to the notice's source session, the daemon's own (notify.DaemonSession). These cases run
// the listener built with the envoytestclock tag and move its clock for a kept copy's hour past
// that hour for the claim that finds the copy (startKeptClockListener), so the copy's hour ends at
// once, as an hour's wait would end it; every other clock of the listener stays the wall clock, and
// the clock is put back once the claim has found the copy, so the reaper's next takeover record is
// dated on the wall clock. The daemon's side is as workflow.run wires it: intake's durable on the
// sender's own agent subject (intake.ConsumerSpec.OwnSession), applied through the engine and
// admission, and the sender's registration at the listener (outbox.providerErrorsSender).

// deadLetterOffset is how far a case moves the listener's clock for a kept copy's hour: past the
// hour (roleKeepWindow) and inside twice it, past which a copy no walk claimed is dropped as stale
// with no dead letter (roleKeepStaleAge).
const deadLetterOffset = time.Hour + 5*time.Minute

// deadLetterRig is the error-run rig with a kept-clock listener, the daemon's own durable on its
// provider-errors sender's subject, applied through the rig's handlers, and that sender
// registered at the listener.
type deadLetterRig struct {
	*errorRunRig
	natsURL  string
	listener *idleRigListener
	runner   *controllerStartOutbox
	offset   string
	role     claim.Token
	topic    string
	notice   string
	stream   jetstream.Stream
}

func newDeadLetterRig(t *testing.T) *deadLetterRig {
	t.Helper()
	t.Setenv("ENVOY_SESSION_LIVE_WINDOW", controllerAwayWindow.String())
	t.Setenv("ENVOY_SESSION_GONE_HORIZON", controllerAwayWindow.String())
	t.Setenv("ENVOY_ROLE_CLAIM_REAP_INTERVAL", "2s")
	t.Setenv("ENVOY_SESSION_REAP_INTERVAL", "2s")
	r := newErrorRunRig(t)
	natsURL := testnats.URL(t)
	offset := filepath.Join(testtmp.Dir(t, "kept-clock-offset"), "kept-clock-offset")
	listener := startKeptClockListener(t, natsURL, "controller-dead-letter", offset)
	clock := time.Now().UTC().Truncate(time.Microsecond)
	runner := &controllerStartOutbox{t: t, at: &clock, natsURL: natsURL, outbox: &outbox{log: quietLogger(), pool: r.pool,
		dispatchProject: errorRunProject, records: r.runtime.records, notices: notify.New(listener.url, ""),
		project: r.project, now: func() time.Time { return clock }}}
	runner.publisher = notify.New(listener.url, "")
	role := claim.ControllerToken(r.project)
	runner.topic = notify.RoleTopicPrefix + string(role)
	d := &deadLetterRig{errorRunRig: r, natsURL: natsURL, listener: listener, runner: runner, offset: offset, role: role,
		topic: runner.topic, notice: noticeSummary(record.ProviderErrorsNotice, errorRunIssue)}

	conn := testnats.Connect(t, natsURL)
	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatalf("open JetStream: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	consumers, err := intake.OpenConsumers(ctx, js, r.runtime.pool, intake.ConsumerSpec{
		Project: errorRunProject, OwnSession: notify.DaemonSession(r.project),
		AckWait: 5 * time.Second, NakDelay: 100 * time.Millisecond, Logger: quietLogger(),
	})
	if err != nil {
		cancel()
		t.Fatalf("open the intake consumers: %v", err)
	}
	if d.stream, err = js.Stream(ctx, "ENVOY_NOTIFICATIONS"); err != nil {
		cancel()
		t.Fatalf("open the notification stream: %v", err)
	}
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		if err := consumers.Run(ctx, r.runtime.handlers...); err != nil {
			t.Errorf("intake stopped: %v", err)
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		runner.outbox.providerErrorsSender(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		<-done
	})
	session := notify.DaemonSession(r.project)
	testwait.Eventually(t, "the listener to register the provider-errors sender "+session, func() bool {
		response, err := listener.client.Get(listener.url + "/v1/registry/" + session)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
	return d
}

// moveKeptClock moves the listener's clock for a kept copy's hour by span from now on, 0 putting it
// back on the wall clock.
func (d *deadLetterRig) moveKeptClock(span time.Duration) {
	d.t.Helper()
	if err := os.WriteFile(d.offset, []byte(span.String()), 0o600); err != nil {
		d.t.Fatalf("move the listener's kept-copy clock: %v", err)
	}
}

// controller registers a controller session with the listener, makes it the registered
// controller, claims the role with it and follows the controller topic, with a heartbeat that keeps
// its registration live until stop. A session that dies has its NATS connection closed once it
// holds the role, so its process is gone while its registration stays live: the listener reads it
// live and forwards to it, and nothing reads what it forwards.
func (d *deadLetterRig) controller(session string, dies bool) (c *controllerStandin, stop func()) {
	d.t.Helper()
	c, bus := newControllerStandinWithBus(d.t, d.listener, d.natsURL, session)
	c.register()
	stop = c.heartbeat(d.topic)
	d.replaceController(c.session)
	c.claimRole(d.role)
	c.register(d.topic)
	if dies {
		bus.Close()
	}
	return c, stop
}

// awaitReaped waits until the listener's role claim reaper has freed the controller role's claim
// that holder held.
func (d *deadLetterRig) awaitReaped(holder string) {
	d.t.Helper()
	idleRigEventually(d.t, 3*time.Minute, "the role claim reaper freeing "+holder+"'s claim of "+string(d.role), func() bool {
		response, err := d.listener.client.Get(d.listener.url + "/v1/roles/" + string(d.role))
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

// awaitPublishedAgain drains the outbox until it has published one more provider-errors notice than
// the before copies it had recorded and holds no provider-errors row, and fails the case when that
// does not happen within two minutes: the dead letter reaching the daemon's own durable, admission
// owing the run again and writing its notice, and the outbox publishing it to the controller that
// holds the role.
func (d *deadLetterRig) awaitPublishedAgain(before int) {
	d.t.Helper()
	idleRigEventually(d.t, 2*time.Minute, "a provider-errors notice published again after its copy's dead letter", func() bool {
		d.runner.drain()
		_, _, held := d.runner.providerErrorsRow()
		return !held && len(d.copies()) > before
	})
}

// providerErrorsCopy is one row of provider_errors_copies, in the order it was recorded.
type providerErrorsCopy struct {
	eventID    string
	deadLetter string
}

// copies is every copy of a provider-errors notice the outbox recorded, in order.
func (d *deadLetterRig) copies() []providerErrorsCopy {
	d.t.Helper()
	rows, err := d.pool.Query(d.ctx, "select event_id, coalesce(dead_letter, '') from provider_errors_copies order by recorded")
	if err != nil {
		d.t.Fatalf("read the provider-errors notice copies: %v", err)
	}
	defer rows.Close()
	var copies []providerErrorsCopy
	for rows.Next() {
		var c providerErrorsCopy
		if err := rows.Scan(&c.eventID, &c.deadLetter); err != nil {
			d.t.Fatalf("read the provider-errors notice copies: %v", err)
		}
		copies = append(copies, c)
	}
	if err := rows.Err(); err != nil {
		d.t.Fatalf("read the provider-errors notice copies: %v", err)
	}
	return copies
}

// deadLetters is each dead letter the stream holds on the provider-errors sender's agent subject,
// by the event id of the copy it names, with its dead_letter field.
func (d *deadLetterRig) deadLetters() map[string]string {
	d.t.Helper()
	ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
	defer cancel()
	consumer, err := d.stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{"notifications.agent." + notify.DaemonSession(d.project)},
	})
	if err != nil {
		d.t.Fatalf("read the provider-errors sender's agent subject: %v", err)
	}
	info, err := consumer.Info(ctx)
	if err != nil {
		d.t.Fatalf("read the provider-errors sender's agent subject: %v", err)
	}
	letters := map[string]string{}
	for range info.NumPending {
		message, err := consumer.Next(jetstream.FetchMaxWait(5 * time.Second))
		if err != nil {
			d.t.Fatalf("read the provider-errors sender's agent subject: %v", err)
		}
		var letter struct {
			Source        string `json:"source"`
			SourceEventID string `json:"source_event_id"`
			DeadLetter    string `json:"dead_letter"`
		}
		if err := json.Unmarshal(message.Data(), &letter); err != nil {
			d.t.Fatalf("decode a message on the provider-errors sender's agent subject: %v", err)
		}
		if letter.Source == "envoy" {
			letters[letter.SourceEventID] = letter.DeadLetter
		}
	}
	return letters
}

// runOfSix is the implementer's run of six provider-error turns with holder, the registered
// controller, holding its role live, and its notice published to holder.
func (d *deadLetterRig) runOfSix(holder string) {
	d.t.Helper()
	if got, state, err := d.runner.outbox.notices.RoleHolder(d.ctx, string(d.role)); err != nil || got != holder || state != notify.StateLive {
		d.t.Fatalf("precondition: the listener names %q %q (%v) as the holder of %s, want %s live", got, state, err, d.role, holder)
	}
	for range 6 {
		d.errorTurn()
	}
	d.wantOneNotice("after six turns with the controller registered,", 6)
	d.runner.drain()
	if _, _, held := d.runner.providerErrorsRow(); held {
		d.t.Fatalf("precondition: the provider-errors row is still held after its publish to %s, which the listener reads live", holder)
	}
	if copies := d.copies(); len(copies) != 1 {
		d.t.Fatalf("precondition: the outbox recorded %d copies of the notice it published, want one: %+v", len(copies), copies)
	}
}

// awaitHolding waits until c was delivered the provider-errors notice once, then waits ten seconds
// more, so a second copy that comes later is read too.
func (d *deadLetterRig) awaitHolding(c *controllerStandin) {
	d.t.Helper()
	idleRigEventually(d.t, time.Minute, c.session+" to be delivered the provider-errors notice", func() bool { return c.holding(d.notice) >= 1 })
	time.Sleep(10 * time.Second)
}

// The after-the-window case: the dead controller's copy is dropped unread with a dead letter
// whose dead_letter field is expired, its run is owed again, and the controller that claimed the
// role is told once, by the notice published again. The first copy carries that dead letter, the
// second none, and the dead controller was handed nothing.
func TestAProviderErrorsNoticeWhoseCopyOutlivesItsHourIsToldTheNextControllerOnce(t *testing.T) {
	d := newDeadLetterRig(t)
	first, stop := d.controller(controllerStartSession+"-first", true)
	d.runOfSix(first.session)
	stop()
	d.awaitReaped(first.session)

	d.moveKeptClock(deadLetterOffset)
	next, _ := d.controller(controllerStartSession+"-next", false)
	d.awaitPublishedAgain(1)
	d.moveKeptClock(0)
	d.awaitHolding(next)

	if n := next.holding(d.notice); n != 1 {
		t.Fatalf("the next controller was delivered the provider-errors notice %d times, want once", n)
	}
	if n := first.holding(d.notice); n != 0 {
		t.Fatalf("the dead controller's relay took the notice %d times, want none", n)
	}
	copies := d.copies()
	if len(copies) != 2 || copies[0].deadLetter != "expired" || copies[1].deadLetter != "" {
		t.Fatalf("the provider-errors notice copies are %+v, want the dead controller's with its expired dead letter and the next controller's with none", copies)
	}
	if letters := d.deadLetters(); len(letters) != 1 || letters[copies[0].eventID] != "expired" {
		t.Fatalf("the sender's agent subject holds the dead letters %v, want one, expired, of copy %s", letters, copies[0].eventID)
	}
	if _, _, held := d.runner.providerErrorsRow(); held {
		t.Fatal("the outbox still holds a provider-errors row after the next controller took the notice")
	}
}

// The owe-again loop: the notice published again goes to a controller that died the same way, so
// its copy too is dropped unread and owed again, and the third controller, which reads, is told
// once. Its copy carries no dead letter: a copy a controller takes ends the loop.
func TestAProviderErrorsNoticeIsOwedAgainUntilAControllerTakesIt(t *testing.T) {
	d := newDeadLetterRig(t)
	first, stopFirst := d.controller(controllerStartSession+"-first", true)
	d.runOfSix(first.session)
	stopFirst()
	d.awaitReaped(first.session)

	d.moveKeptClock(deadLetterOffset)
	second, stopSecond := d.controller(controllerStartSession+"-second", true)
	d.awaitPublishedAgain(1)
	d.moveKeptClock(0)
	stopSecond()
	d.awaitReaped(second.session)

	d.moveKeptClock(deadLetterOffset)
	third, _ := d.controller(controllerStartSession+"-third", false)
	d.awaitPublishedAgain(2)
	d.moveKeptClock(0)
	d.awaitHolding(third)

	for _, c := range []struct {
		standin *controllerStandin
		want    int
	}{{first, 0}, {second, 0}, {third, 1}} {
		if n := c.standin.holding(d.notice); n != c.want {
			t.Fatalf("controller %s was delivered the provider-errors notice %d times, want %d", c.standin.session, n, c.want)
		}
	}
	copies := d.copies()
	if len(copies) != 3 || copies[0].deadLetter != "expired" || copies[1].deadLetter != "expired" || copies[2].deadLetter != "" {
		t.Fatalf("the provider-errors notice copies are %+v, want two dropped expired and the third controller's with no dead letter", copies)
	}
	if letters := d.deadLetters(); len(letters) != 2 || letters[copies[0].eventID] != "expired" || letters[copies[1].eventID] != "expired" {
		t.Fatalf("the sender's agent subject holds the dead letters %v, want the expired ones of the two dropped copies", letters)
	}
	if _, _, held := d.runner.providerErrorsRow(); held {
		t.Fatal("the outbox still holds a provider-errors row after the third controller took the notice")
	}
}

// The control: the first controller reads the notice and then dies, on the same path as the
// after-the-window case, so nothing it was forwarded is left unread on its subject past its
// floor, and the controller that claims the role past a kept copy's hour is handed nothing: the
// listener sends no dead letter, the run is owed nothing again, and the one copy carries no dead
// letter.
func TestAProviderErrorsNoticeAControllerReadIsSentNoDeadLetter(t *testing.T) {
	d := newDeadLetterRig(t)
	first, bus := newControllerStandinWithBus(t, d.listener, d.natsURL, controllerStartSession+"-first")
	first.register()
	stop := first.heartbeat(d.topic)
	d.replaceController(first.session)
	first.claimRole(d.role)
	first.register(d.topic)
	d.runOfSix(first.session)
	d.awaitHolding(first)
	bus.Close()
	stop()
	d.awaitReaped(first.session)

	d.moveKeptClock(deadLetterOffset)
	next, _ := d.controller(controllerStartSession+"-next", false)
	time.Sleep(20 * time.Second)
	d.runner.drain()
	d.moveKeptClock(0)

	if n := next.holding(d.notice); n != 0 {
		t.Fatalf("the controller that claimed after the first read the notice was delivered it %d times, want none", n)
	}
	if n := first.holding(d.notice); n != 1 {
		t.Fatalf("the first controller was delivered the provider-errors notice %d times, want once", n)
	}
	if copies := d.copies(); len(copies) != 1 || copies[0].deadLetter != "" {
		t.Fatalf("the provider-errors notice copies are %+v, want the one the first controller read, with no dead letter", copies)
	}
	if letters := d.deadLetters(); len(letters) != 0 {
		t.Fatalf("the sender's agent subject holds the dead letters %v, want none", letters)
	}
	if _, _, held := d.runner.providerErrorsRow(); held {
		t.Fatal("the outbox holds a provider-errors row owed again for a notice a controller read")
	}
}
