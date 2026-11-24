package api

import (
	"context"

	"github.com/sjawhar/envoy/internal/dispatch/admission"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// No admission unit, outside place or pinned request-pool connection is held across a wait on
// another server - the Envoy listener, NATS, GitHub, the embedder, the secrets broker - or across
// a wait on another goroutine's such call. A unit held across one lets a server slow to answer
// fill a class with requests doing no work, and every request that waits on nothing is refused
// 429 OVERLOADED until it answers. awayOnNetwork is the one way a request lets go for such a wait,
// as a caller's resolution does around an issuer (identity.Membership.Confirm,
// serviceTokenActor): admission.GiveBack before the wait and admission.Away.TakeBack after it, on
// the request's own goroutine, since only that goroutine may park or resume its ticket
// (admission.Ticket.Waited). A goroutine of its own that waits - a fetch, a stage, a lane - makes
// no pair: the request waiting on it does.

// awayOnNetwork runs wait, a wait on another server, holding nothing of the server's room: it
// gives ctx's request's unit back (admission.GiveBack, which lets go of its pinned connection
// too), runs wait on a context of its own connection mark (store.ForConcurrentRead), so a
// statement the wait makes - the GitHub client's read of the rate-limit ledger - takes a
// connection for that one statement and gives it back rather than pinning one again for the
// whole wait, and takes the unit back once wait has returned (admission.Away.TakeBack). ctx is
// the request's own context, never one a bound of the wait's derives from it: the take-back waits
// on ctx, and one a passed bound had ended would leave the request running without its unit.
//
// It answers the take-back's error ahead of wait's own: a take-back refused (admission's
// *Refusal, which the ticket records, so nothing of the request runs past it) is the request's
// answer, which reaches writeHandlerError and is answered 429 OVERLOADED; a caller that reads
// every other error of the wait as "unavailable" or "unreadable" reads that one first
// (refusedTakeBack). A request refused already is answered that refusal without waiting again. A
// request that holds a transaction, a cursor or a connection of its own must end it before it
// waits, since a held connection whose unit was given back breaks admission's count: the wait is
// not run and the request is answered the pool's ErrNestedAcquire.
func awayOnNetwork(ctx context.Context, wait func(context.Context) error) error {
	if refused := refusedTakeBack(ctx); refused != nil {
		return refused
	}
	away := admission.GiveBack(ctx)
	waiting, err := store.ForConcurrentRead(ctx)
	if err == nil {
		err = wait(waiting)
	}
	if back := away.TakeBack(ctx); back != nil {
		return back
	}
	return err
}

// refusedTakeBack is the refusal ctx's request met taking its unit back after a wait on another
// server (awayOnNetwork), and nil where it met none.
func refusedTakeBack(ctx context.Context) error {
	if refusal := admission.TicketFrom(ctx).RefusedTakeBack(); refusal != nil {
		return refusal
	}
	return nil
}
