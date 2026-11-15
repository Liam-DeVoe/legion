// packages/envoy/cmd/agent-secrets/secret_create.go
//
// The stretch of `secret create` between the store making a secret and that secret holding its
// value, or being gone. On GCP the secret is made, empty, before its value is read
// (newSecret.begin), and from then until store succeeds or discard has run, neither a failure
// nor a signal may leave the empty secret behind, and nothing may delete a secret that may hold
// the value. Each store call of the stretch has a deadline (storeCallDeadline), and a second
// signal ends the form at once rather than waiting on the call the first one's cleanup waits on.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// promptBusy is held by the value prompt for as long as it reads (readHiddenAtTerminal locks it
// first and unlocks it last, after any end by a signal). A signal the prompt catches, it ends the
// form by itself once it has restored the terminal, so watchCreateSignals waits on promptBusy
// rather than ending the form under the prompt.
var promptBusy sync.Mutex

// createSignals are the signals that end a form while it holds a secret it has not seen through:
// a hang-up, an interrupt, a quit and a termination.
var createSignals = []os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM}

// storeCallDeadline bounds each store call `secret create` makes from CreateSecret until the value
// is stored or the discard is done, so that a call the store never answers holds neither the form
// nor a signal waiting on it (pendingCreate) for longer. It is 60 s, Secret Manager's Go client's
// own default on each of those calls (CreateSecret, AddSecretVersion, ListSecretVersions and
// DeleteSecret: defaultCallOptions in cloud.google.com/go/secretmanager/apiv1), which that client
// applies only to a call whose context carries no deadline of its own. The AWS SDK puts none on a
// call - its defaults bound the dial, at 30 s, and the TLS handshake, at 10 s, not the wait for an
// answer - so AWS's one CreateSecret takes the same 60 s. Tests shorten it.
var storeCallDeadline = 60 * time.Second

// withStoreDeadline is ctx bounded by storeCallDeadline, for one store call.
func withStoreDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, storeCallDeadline)
}

// storeCallDeadlineText is storeCallDeadline as a line says it: whole seconds as such ("60s"),
// any other length as Go writes it.
func storeCallDeadlineText() string {
	if storeCallDeadline >= time.Second && storeCallDeadline%time.Second == 0 {
		return strconv.FormatInt(int64(storeCallDeadline/time.Second), 10) + "s"
	}
	return storeCallDeadline.String()
}

// onSecondSignal, while set, says what a second signal ends the form on (endAtOnce): the state the
// store may be left in and the command that clears it. Create sets it beside onSignalDeath
// (pendingCreate.endingAtOnce); nil otherwise.
var onSecondSignal func(sig os.Signal)

// cleanupBegun is closed once a signal's end of the form has begun its cleanup (endBySignal), which
// is after the prompt, where it was reading, has restored the terminal: a second signal ends the
// form at once from then on (endAtOnce), and never under the prompt.
var (
	cleanupBegun = make(chan struct{})
	beginCleanup = sync.OnceFunc(func() { close(cleanupBegun) })
)

// watchCreateSignals catches each of createSignals that this process did not inherit as ignored,
// from now until stop is called, and ends the form by the first that comes (endBySignal), which
// runs onSignalDeath first. While the value prompt reads, a signal is the prompt's to act on, so
// that end waits for the prompt to finish first (promptBusy). It keeps watching once it has a
// signal: a second one, once the first one's cleanup has begun, ends the form at once by that
// second signal (endAtOnce), rather than waiting on a store call the cleanup waits on. stop never
// returns once a signal was taken, since the form is ending by it; a signal that came with the
// stop still ends the form. It works through os/signal alone, on every platform this command
// builds for; how the form then ends by the signal is the platform's (raiseSignal).
func watchCreateSignals() (stop func()) {
	var watched []os.Signal
	for _, sig := range createSignals {
		if !signal.Ignored(sig) {
			watched = append(watched, sig)
		}
	}
	if len(watched) == 0 {
		// signal.Notify naming no signal relays every signal.
		return func() {}
	}
	signals := make(chan os.Signal, len(watched))
	signal.Notify(signals, watched...)
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		select {
		case sig := <-signals:
			go endByCaughtSignal(sig)
		case <-done:
			close(exited)
			return
		}
		second := <-signals
		<-cleanupBegun
		endAtOnce(second)
	}()
	return sync.OnceFunc(func() {
		close(done)
		<-exited
		signal.Stop(signals)
		select {
		case sig := <-signals:
			endByCaughtSignal(sig)
		default:
		}
	})
}

// endByCaughtSignal ends the form by sig, which watchCreateSignals caught, once the prompt, where
// it is reading, has finished.
func endByCaughtSignal(sig os.Signal) {
	promptBusy.Lock()
	endBySignal(sig)
}

// endBySignal ends the form by sig, a hang-up, an interrupt, a quit or a termination that came
// while it held a secret it had not seen through, or while the prompt read a value: from the
// prompt's reader, once it has restored the terminal, or from watchCreateSignals. It runs
// onSignalDeath first, when that is set, during which a second signal ends the form at once
// (watchCreateSignals, endAtOnce). Then it ends the process by sig (exitBySignal).
func endBySignal(sig os.Signal) {
	if cleanup := onSignalDeath; cleanup != nil {
		beginCleanup()
		cleanup()
	}
	exitBySignal(sig)
}

// endAtOnce ends the form by sig, a second signal that came while the first one's cleanup
// (onSignalDeath) may still wait on a store call, without waiting on it: onSecondSignal first says
// what the store may be left holding and the command that clears it.
func endAtOnce(sig os.Signal) {
	if say := onSecondSignal; say != nil {
		say(sig)
	}
	exitBySignal(sig)
}

// exitBySignal ends the process by sig: back at its default action, the platform raises it where
// it can (raiseSignal), and the exit status a shell gives for sig is the fallback.
func exitBySignal(sig os.Signal) {
	signal.Reset(sig)
	if raiseSignal(sig) {
		time.Sleep(time.Second)
	}
	os.Exit(signalExitCode(sig))
}

// signalExitCode is the status a shell reports for a process sig ended.
func signalExitCode(sig os.Signal) int {
	switch sig {
	case syscall.SIGHUP:
		return exitHangup
	case syscall.SIGTERM:
		return exitTerminated
	case syscall.SIGQUIT:
		return exitQuit
	default:
		return exitInterrupted
	}
}

// signalNoun names sig as a line says it.
func signalNoun(sig os.Signal) string {
	switch sig {
	case syscall.SIGHUP:
		return "hang-up"
	case syscall.SIGINT:
		return "interrupt"
	case syscall.SIGQUIT:
		return "quit"
	case syscall.SIGTERM:
		return "termination"
	}
	return sig.String()
}

// discardOutcome is what a failed newSecret.begin or a newSecret.discard left in the store. A
// discard answers an error exactly when its outcome is one of the kept ones, the error naming what
// stays and the command that clears or checks it; begin's own failure is an error whatever its
// outcome. pendingCreate.rereadOutcome reads it to decide whether the broker rereads the name.
type discardOutcome int

const (
	// madeNothing: nothing was made, so there was nothing to remove (AWS, or a create the store
	// refused before acting).
	madeNothing discardOutcome = iota
	// deletedEmpty: the empty secret this create made is deleted, or was already gone.
	deletedEmpty
	// keptEmpty: the store refused the delete; this create's empty secret stays.
	keptEmpty
	// keptMayHoldValue: the secret holds a version, whether this create or another writer added
	// it, or this create's add may still land, so it stays.
	keptMayHoldValue
	// keptOthers: the name is held by a secret this create did not make, or one that changed before
	// the delete, so it is left as it is.
	keptOthers
	// keptUnknown: what the name holds could not be read, or the secret's versions could not be
	// listed, so it stays.
	keptUnknown
)

// pendingCreate owns a create from before its first store call until its value is stored or its
// discard has run. mu is held across begin, store and discard, so a signal waits for the call in
// flight before cleaning up. The backend settles a failed begin itself; discard runs only after
// begin succeeds. A second signal waits for none of it (endingAtOnce).
type pendingCreate struct {
	s              *secretSession
	form, name     string
	stdout, stderr io.Writer
	created        newSecret

	mu        sync.Mutex
	begun     bool
	stored    bool
	discarded bool
	left      error // what discard left to report, nil for nothing
}

// watch makes p what a signal ends the form on, onSignalDeath and onSecondSignal, and starts
// watchCreateSignals. The func it answers ends the stretch: it stops the watch and clears both,
// once, however often it is called, so a form calls it as soon as the stretch is over and defers
// it as well.
func (p *pendingCreate) watch() (end func()) {
	onSignalDeath, onSecondSignal = p.interrupted, p.endingAtOnce
	stop := watchCreateSignals()
	return sync.OnceFunc(func() {
		stop()
		onSignalDeath, onSecondSignal = nil, nil
	})
}

// begin makes any value-free store call while signal cleanup waits for its answer. Its failure is
// the form's to print; p.left keeps it as well only where something stays (a kept outcome), so a
// signal that comes before the form prints it says what stays, and a failure that left nothing
// (madeNothing, deletedEmpty) is printed once, by the form alone.
func (p *pendingCreate) begin() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.discarded {
		return fmt.Errorf("the create of %s was interrupted before it began", p.name)
	}
	p.begun = true
	outcome, err := p.created.begin(p.s.ctx)
	if err != nil {
		p.discarded = true
		if outcome != madeNothing && outcome != deletedEmpty {
			p.left = err
		}
		p.rereadOutcome(outcome)
	}
	return err
}

// store writes value as the secret's first value.
func (p *pendingCreate) store(value string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.discarded {
		return fmt.Errorf("the secret %s was discarded before its value was stored", p.name)
	}
	err := p.created.store(p.s.ctx, value)
	p.stored = err == nil
	return err
}

// abandon discards what create made after err, the failure that stopped the value being stored,
// and answers the error the form reports: err itself when nothing stays, so a usage error still
// exits 2 having written nothing, and otherwise err beside what stays, which exits 1, the store
// holding a secret it did not hold before.
func (p *pendingCreate) abandon(err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if left := p.discardLocked(); left != nil {
		return fmt.Errorf("%v; %v", p.s.refusedAs(err), p.s.refusedAs(left))
	}
	return err
}

// interrupted is onSignalDeath while p is pending: unless the value is stored, it discards what
// create made, saying on stderr what stays, if anything; the signal then decides the exit status.
func (p *pendingCreate) interrupted() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.begun {
		p.discarded = true
		return
	}
	if p.stored {
		return
	}
	if left := p.discardLocked(); left != nil {
		secretFailIn(p.stderr, p.form, p.s, left)
	}
}

// endingAtOnce is onSecondSignal while p is pending: it says what the store may be left holding,
// should the form end now without waiting on the store call in flight, and the command that clears
// it (newSecret.unsettled). It takes no lock, since the first signal's cleanup may hold mu across
// that very call.
func (p *pendingCreate) endingAtOnce(sig os.Signal) {
	fmt.Fprintf(p.stderr, "agent-secrets %s: a second signal (%s) ends the create before the first one's cleanup is done: %s\n", p.form, signalNoun(sig), p.created.unsettled())
}

// discardLocked runs the discard once, under mu, and answers what it left to report, nil when
// nothing stays. After deleting the empty secret it asks the broker to reread the name, so a
// refusal or a wait for a value the broker logged for the secret meanwhile ends at once; the
// create has failed already, so that reread's own failure changes nothing it reports. A secret
// that may hold the value is reread with its answer printed, which says whether the broker
// serves it.
func (p *pendingCreate) discardLocked() error {
	if p.discarded {
		return p.left
	}
	p.discarded = true
	outcome, err := p.created.discard(p.s.ctx)
	p.rereadOutcome(outcome)
	p.left = err
	return p.left
}

// rereadOutcome updates the broker after either recovery from begin or the later discard.
func (p *pendingCreate) rereadOutcome(outcome discardOutcome) {
	switch outcome {
	case deletedEmpty:
		_, _ = p.s.broker.RereadSecret(context.WithoutCancel(p.s.ctx), p.name)
	case keptMayHoldValue:
		reread(p.s, p.stdout, p.stderr, p.form, p.name, true, nil)
	}
}
