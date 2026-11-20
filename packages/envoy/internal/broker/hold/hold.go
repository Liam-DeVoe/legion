// Package hold is the broker's one check of whether Dispatch holds a person's credentials until
// they sign in again: Gate.Check, which each point that serves a person's credential asks. It asks
// Dispatch's person route through internal/broker/projectaccess and keeps every answer it gets in
// person_standing (internal/broker/store), so that while Dispatch cannot be asked it answers from
// the last answer Dispatch gave: a person last answered held stays held, since only Dispatch ends
// a hold, and everyone else is served, with one WARN line a minute per person and
// broker_hold_unchecked_total. The answer is kept in the database rather than in this process, so
// a restart or a second broker process answers alike, and each process keeps the latest answer by
// Dispatch's own clock (the answer's as_of) whatever order the writes arrive in.
//
// A caller asks before it opens a database transaction or takes a lock, as projectaccess
// requires of every caller: Check asks Dispatch and writes person_standing itself.
package hold

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/broker/record"
	"github.com/sjawhar/envoy/internal/broker/store"
	"github.com/sjawhar/envoy/internal/metrics"
)

// ErrHeld is a person whose credentials Dispatch holds until they sign in again.
var ErrHeld = errors.New("hold: Dispatch holds this person's credentials until they sign in again")

// ErrUncheckable is a person whose standing the gate cannot decide: Dispatch refused the broker's
// own account (projectaccess.ErrBrokerRefused), Dispatch could not be asked and person_standing
// could not be read, or a held answer could not be written.
var ErrUncheckable = errors.New("hold: whether Dispatch holds this person cannot be read")

// askTimeout bounds one ask of Dispatch, and writeTimeout the write to person_standing after it,
// together inside brokertoken's 5-second introspection bound.
const askTimeout = 3 * time.Second
const writeTimeout = 1 * time.Second

// failureMemory is how long a failure to ask is remembered per person.
const failureMemory = 5 * time.Second

// logEvery is how often the gate logs the same thing about one person while it goes on: a person
// it serves while Dispatch cannot be asked, and, for every person at once, Dispatch refusing the
// broker's own account.
const logEvery = time.Minute

// maxRemembered bounds the gate's per-person memories of failures, warnings and writes before it
// sweeps the ones whose time has passed. It never forgets a held answer it could not write.
const maxRemembered = 4096

// Config is what a Gate is built from.
type Config struct {
	// Access asks Dispatch's person route (BROKER_PROJECT_ACCESS_URL): the client the token
	// exchange asks too, so the two share one cache of person answers and one call per person.
	Access *projectaccess.Client
	// Store keeps person_standing, the last answer Dispatch gave for each person.
	Store *store.Store
	// Log is where the gate's WARN and ERROR lines go; nil is slog.Default().
	Log *slog.Logger
	// Metrics is the registry broker_hold_unchecked_total is counted on; nil counts nothing.
	Metrics *metrics.Registry
	// Now is the clock the gate's memories are timed by; nil is time.Now.
	Now func() time.Time
}

// Gate is the check. A nil *Gate is a broker with no Dispatch to ask, and holds nobody.
type Gate struct {
	access    *projectaccess.Client
	store     *store.Store
	log       *slog.Logger
	unchecked *metrics.Counter
	now       func() time.Time

	mu sync.Mutex
	// failures is each person's last failure to ask and when it happened, which spares Dispatch
	// a second ask, and the caller a second wait, for failureMemory.
	failures map[string]failure
	// written is the AskedAt of the last answer for each person this gate wrote, so an answer the
	// client answers again from its cache is written once.
	written map[string]time.Time
	// unwritten is each held answer this gate could not write, by its as_of: written again at
	// each check of its person until a write takes it or a later answer from Dispatch replaces
	// it, and answered from while Dispatch cannot be asked.
	unwritten map[string]time.Time
	// warned is when the gate last logged serving each person while Dispatch could not be asked.
	warned map[string]time.Time
	// refusalLogged is when the gate last logged Dispatch refusing the broker's own account.
	refusalLogged time.Time
}

type failure struct {
	at  time.Time
	err error
}

// New builds the gate.
func New(cfg Config) *Gate {
	g := &Gate{
		access: cfg.Access, store: cfg.Store, log: cfg.Log, now: cfg.Now,
		failures: map[string]failure{}, written: map[string]time.Time{},
		unwritten: map[string]time.Time{}, warned: map[string]time.Time{},
	}
	if g.log == nil {
		g.log = slog.Default()
	}
	if g.now == nil {
		g.now = time.Now
	}
	if cfg.Metrics != nil {
		g.unchecked = cfg.Metrics.NewCounter("broker_hold_unchecked_total",
			"Checks of a person the broker served while Dispatch could not be asked whether it holds them, at most one a minute per person, by the last answer Dispatch gave for them: not_held or none.")
	}
	return g
}

// Check answers ErrHeld for a held person and ErrUncheckable when it cannot decide, each wrapped
// with the person, and nil for everyone else. A held answer it cannot write is
// errors.Join(ErrUncheckable, ErrHeld): a route checks ErrUncheckable first and answers 503, and
// the airlock's read of a carried call cuts the call on any error matching ErrHeld. An empty
// person is an error. A nil *Gate answers nil.
//
// It asks Dispatch about person as record.CanonicalLogin spells them, bounded by askTimeout, and
// answers from Dispatch's answer, which it writes to person_standing unless it already wrote that
// answer; the write runs apart from ctx, bounded by writeTimeout, so a caller that hangs up does
// not stop it. Dispatch refusing the broker's own account is ErrUncheckable for every person,
// logged at ERROR once a minute. Every other failure to ask is answered from the later, by as_of,
// of the kept row and a held answer the gate could not write, and is remembered failureMemory
// unless the caller itself hung up.
func (g *Gate) Check(ctx context.Context, person string) error {
	if g == nil {
		return nil
	}
	person = record.CanonicalLogin(person)
	if person == "" {
		return errors.New("hold: a person's hold is checked, and none was named")
	}
	now := g.now()
	g.mu.Lock()
	remembered, failed := g.failures[person]
	g.mu.Unlock()
	if failed && now.Sub(remembered.at) < failureMemory {
		return g.unasked(ctx, person, remembered.err)
	}
	askCtx, cancel := context.WithTimeout(ctx, askTimeout)
	answer, err := g.access.Person(askCtx, person)
	cancel()
	switch {
	case err == nil:
		g.mu.Lock()
		delete(g.failures, person)
		g.mu.Unlock()
		return g.answered(ctx, person, answer)
	case errors.Is(err, projectaccess.ErrBrokerRefused):
		g.logRefusal(person, err)
		return fmt.Errorf("%w: %s: %w", ErrUncheckable, person, err)
	}
	if ctx.Err() != nil {
		// The caller hung up, which is no failure of Dispatch's to remember for the next one.
		return g.unasked(ctx, person, err)
	}
	g.mu.Lock()
	g.sweepLocked(now)
	g.failures[person] = failure{at: now, err: err}
	g.mu.Unlock()
	return g.unasked(ctx, person, err)
}

// answered is the check answered from Dispatch's answer. Each answer is written once, the first
// time the gate sees it, whichever caller's ask fetched it. A held answer that write fails is
// remembered and written again at each check until a write takes it or a later answer from
// Dispatch replaces it; a not-held one is served, and the next answer is written as any is.
func (g *Gate) answered(ctx context.Context, person string, answer projectaccess.PersonAnswer) error {
	g.mu.Lock()
	if asOf, ok := g.unwritten[person]; ok && answer.AsOf.After(asOf) {
		delete(g.unwritten, person)
	}
	write := answer.AskedAt.After(g.written[person])
	if write {
		g.sweepLocked(g.now())
		g.written[person] = answer.AskedAt
	}
	g.mu.Unlock()
	var writeErr error
	if write {
		writeErr = g.keep(ctx, person, answer.Held, answer.AsOf)
		g.mu.Lock()
		switch asOf, remembered := g.unwritten[person]; {
		case writeErr == nil && remembered && !asOf.After(answer.AsOf):
			// The row now holds this answer or a later one, so the remembered one is written.
			delete(g.unwritten, person)
		case writeErr != nil && answer.Held && (!remembered || answer.AsOf.After(asOf)):
			g.unwritten[person] = answer.AsOf
		}
		g.mu.Unlock()
		if writeErr != nil && !answer.Held {
			g.log.Warn("broker: Dispatch's answer that a person is not held could not be kept; serving on it",
				"person", person, "error", writeErr)
		}
	}
	if writeErr == nil {
		writeErr = g.writeUnwritten(ctx, person)
	}
	if !answer.Held {
		return nil
	}
	g.mu.Lock()
	_, unkept := g.unwritten[person]
	g.mu.Unlock()
	if unkept {
		return heldUnwritten(person, writeErr)
	}
	return fmt.Errorf("%w: %s", ErrHeld, person)
}

// unasked is the check answered while Dispatch cannot be asked (cause): from the later, by as_of,
// of the row person_standing keeps and a held answer this gate could not write.
func (g *Gate) unasked(ctx context.Context, person string, cause error) error {
	writeErr := g.writeUnwritten(ctx, person)
	held, answeredAt, found, readErr := g.store.PersonStanding(ctx, person)
	g.mu.Lock()
	unwrittenAsOf, unwritten := g.unwritten[person]
	g.mu.Unlock()
	switch {
	case unwritten && (readErr != nil || !found || !unwrittenAsOf.Before(answeredAt)):
		return heldUnwritten(person, errors.Join(writeErr, fmt.Errorf("Dispatch could not be asked: %w", cause)))
	case readErr != nil:
		return fmt.Errorf("%w: %s: Dispatch could not be asked (%w), and its last answer could not be read: %w", ErrUncheckable, person, cause, readErr)
	case found && held:
		return fmt.Errorf("%w: %s, by Dispatch's last answer, as of %s", ErrHeld, person, answeredAt.UTC().Format(time.RFC3339Nano))
	}
	lastAnswer := "none"
	if found {
		lastAnswer = "not_held"
	}
	now := g.now()
	g.mu.Lock()
	last, logged := g.warned[person]
	due := !logged || now.Sub(last) >= logEvery
	if due {
		g.sweepLocked(now)
		g.warned[person] = now
	}
	g.mu.Unlock()
	if due {
		g.log.Warn("broker: Dispatch could not be asked whether a person is held; serving on its last answer",
			"person", person, "last_answer", lastAnswer, "error", cause)
		g.unchecked.Inc([2]string{"last_answer", lastAnswer})
	}
	return nil
}

// writeUnwritten writes again the held answer for person this gate could not write, if there is
// one, forgets it once a write takes it, and answers the write's failure.
func (g *Gate) writeUnwritten(ctx context.Context, person string) error {
	g.mu.Lock()
	asOf, ok := g.unwritten[person]
	g.mu.Unlock()
	if !ok {
		return nil
	}
	if err := g.keep(ctx, person, true, asOf); err != nil {
		return err
	}
	g.mu.Lock()
	if current, still := g.unwritten[person]; still && current.Equal(asOf) {
		delete(g.unwritten, person)
	}
	g.mu.Unlock()
	return nil
}

// keep writes Dispatch's answer for person to person_standing, apart from the caller's request
// and bounded by writeTimeout.
func (g *Gate) keep(ctx context.Context, person string, held bool, asOf time.Time) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	return g.store.KeepPersonStanding(ctx, person, held, asOf)
}

// logRefusal logs Dispatch refusing the broker's own account at ERROR, once a minute for every
// person at once: it is the deployment's misconfiguration, not this person's.
func (g *Gate) logRefusal(person string, err error) {
	now := g.now()
	g.mu.Lock()
	due := g.refusalLogged.IsZero() || now.Sub(g.refusalLogged) >= logEvery
	if due {
		g.refusalLogged = now
	}
	g.mu.Unlock()
	if due {
		g.log.Error("broker: Dispatch refused the broker's own account, so whether a person is held cannot be read",
			"person", person, "error", err)
	}
}

// heldUnwritten is a held answer for person that no write has kept yet, for cause:
// ErrUncheckable and ErrHeld at once.
func heldUnwritten(person string, cause error) error {
	if cause == nil {
		cause = errors.New("no write has kept it yet")
	}
	return errors.Join(
		fmt.Errorf("%w: %s: Dispatch's answer that holds them could not be kept: %w", ErrUncheckable, person, cause),
		fmt.Errorf("%w: %s", ErrHeld, person))
}

// sweepLocked forgets the failures, warnings and writes whose time has passed once the gate
// remembers maxRemembered of them. A write is forgotten once its answer is older than the client's
// cache keeps one, after which the client answers it no more. g.mu is held.
func (g *Gate) sweepLocked(now time.Time) {
	if len(g.failures)+len(g.warned)+len(g.written) < maxRemembered {
		return
	}
	for person, f := range g.failures {
		if now.Sub(f.at) >= failureMemory {
			delete(g.failures, person)
		}
	}
	for person, at := range g.warned {
		if now.Sub(at) >= logEvery {
			delete(g.warned, person)
		}
	}
	for person, asked := range g.written {
		if now.Sub(asked) >= projectaccess.CacheTTL {
			delete(g.written, person)
		}
	}
}
