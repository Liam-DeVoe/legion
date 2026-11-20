package daemon

import (
	"context"
	"time"

	"github.com/sjawhar/legion/daemon/internal/notify"
)

// providerErrorsSenderHeartbeat is how often the daemon registers its provider-errors sender at the
// Envoy listener (providerErrorsSender): well inside the listener's live window (five minutes
// after a registration), so the session reads live between two registrations, and far inside its
// gone horizon (an hour past that), after which the listener reads it gone and gives a message on
// its agent subject up with an agent wake report again.
const providerErrorsSenderHeartbeat = time.Minute

// providerErrorsSenderTimeout bounds one registration: the listener answers a registration that
// writes no consumer within its own route budget, seconds, so a registration that has not
// answered in this long is one the next heartbeat makes again.
const providerErrorsSenderTimeout = 30 * time.Second

// providerErrorsSender registers the session the daemon publishes its provider-errors notice as
// (notify.DaemonSession) at the Envoy listener as an own reader, at once and every
// providerErrorsSenderHeartbeat until ctx ends (notify.HTTPPublisher.RegisterOwnReader). The
// listener sends that session the dead letter of each kept copy of the notice it drops unread, on
// the session's agent subject, which intake's own durable reads (intake.ConsumerSpec.OwnSession);
// registered so, the session holds no consumer and starts no relay at the listener, and the
// listener acknowledges each such message as the session's own reader's to take rather than
// holding it for its retry window and then giving it up with an agent wake report naming the
// session, which every daemon of the deployment would read. The registration is needed for that
// alone: the publish route takes the session as the notice's source without one, and intake's
// durable reads the dead letter whatever the listener does with its own copy.
//
// A registration that fails is logged once at WARN, with the listener's words, and the first one
// that succeeds after it once at INFO, so a listener that predates own readers — which refuses the
// registration 409 not_requested — or one the daemon cannot reach costs one line, not one a
// minute; the heartbeat goes on either way, and the dead letters still reach the daemon.
func (r *outbox) providerErrorsSender(ctx context.Context) {
	session := notify.DaemonSession(r.project)
	title := "Legion daemon of " + r.project + ": provider-errors notices"
	failing := false
	register := func() {
		attempt, cancel := context.WithTimeout(ctx, providerErrorsSenderTimeout)
		defer cancel()
		err := r.notices.RegisterOwnReader(attempt, session, title)
		switch {
		case err != nil && ctx.Err() != nil:
		case err != nil && !failing:
			failing = true
			r.log.Warn("outbox: the Envoy listener did not register the provider-errors sender as an own reader; each dead letter of a provider-errors notice then reaches the daemon after the listener's retry window, with an agent wake report naming the sender",
				"session", session, "error", err)
		case err == nil && failing:
			failing = false
			r.log.Info("outbox: the Envoy listener registered the provider-errors sender as an own reader again", "session", session)
		}
	}
	register()
	ticker := time.NewTicker(providerErrorsSenderHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			register()
		}
	}
}
