// The operator's limit on how fast this listener takes one agent session's messages, and the
// refusal a session past it is answered with.

package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/ratelimit"
)

// The two settings of the limit. senderLimitRateEnv is how many messages one agent session may
// send in a minute: unset, empty or 0 sets no limit, which is the listener as it was before the
// setting existed. senderLimitBurstEnv is how many of them it may send at once, and is the rate
// when unset, so a session may send a minute's messages together and then waits for each to come
// back. Both are an operator's: no caller of /v1 reads or changes them.
const (
	senderLimitRateEnv  = "ENVOY_SENDER_MESSAGES_PER_MINUTE"
	senderLimitBurstEnv = "ENVOY_SENDER_MESSAGE_BURST"
)

// senderLimitMaxRate is the highest rate the setting takes: a thousand messages a second. A
// bucket refills one message every minute/rate, and a rate past the nanoseconds of a minute would
// make that zero, which a bucket reads as no limit at all; no session sends a thousand messages a
// second, so the bound refuses a mistyped value rather than a wanted one.
const senderLimitMaxRate = 60000

// senderLimitReason is the refusal's reason field, the word a client branches on.
const senderLimitReason = "rate_limited"

// senderLimit is the operator's limit: one token bucket per sending session, held in this
// process. A nil limit is no limit, so every site calls it without asking whether one is set, and
// the many tests that build their deps without one run the routes as they always ran.
//
// The buckets are this listener's own and are not shared with another listener or kept across a
// restart: a session sends through the one listener its client is configured with, and a restart
// gives each sender a full bucket again, which is one burst and no more.
type senderLimit struct {
	perMinute int
	burst     int
	every     time.Duration
	buckets   *ratelimit.Keyed
}

func newSenderLimit(perMinute, burst int) *senderLimit {
	every := time.Minute / time.Duration(perMinute)
	return &senderLimit{
		perMinute: perMinute,
		burst:     burst,
		every:     every,
		buckets:   ratelimit.NewKeyed(ratelimit.Limit{Burst: burst, Every: every}),
	}
}

// parseSenderLimit reads the limit from environment, nil when the operator set none. A value that
// is not a whole number in range is refused by the variable's name, and the listener does not
// start: an operator who wrote a limit meant one, so starting with none in its place would serve
// a listener nobody asked for. A burst with no rate is refused for the same reason.
func parseSenderLimit(environment func(string) (string, bool)) (*senderLimit, error) {
	rawRate, _ := environment(senderLimitRateEnv)
	rawBurst, _ := environment(senderLimitBurstEnv)
	rawRate, rawBurst = strings.TrimSpace(rawRate), strings.TrimSpace(rawBurst)
	perMinute := 0
	if rawRate != "" {
		parsed, err := strconv.Atoi(rawRate)
		if err != nil || parsed < 0 || parsed > senderLimitMaxRate {
			return nil, fmt.Errorf("invalid %s %q: want a whole number from 0 (no limit) to %d", senderLimitRateEnv, rawRate, senderLimitMaxRate)
		}
		perMinute = parsed
	}
	if perMinute == 0 {
		if rawBurst != "" {
			return nil, fmt.Errorf("%s is set and %s sets no limit: set the rate, or unset the burst", senderLimitBurstEnv, senderLimitRateEnv)
		}
		return nil, nil
	}
	burst := perMinute
	if rawBurst != "" {
		parsed, err := strconv.Atoi(rawBurst)
		if err != nil || parsed < 1 {
			return nil, fmt.Errorf("invalid %s %q: want a whole number of at least 1", senderLimitBurstEnv, rawBurst)
		}
		burst = parsed
	}
	return newSenderLimit(perMinute, burst), nil
}

// describe is the limit as the start-up line states it.
func (l *senderLimit) describe() string {
	if l == nil {
		return "none (" + senderLimitRateEnv + " is unset)"
	}
	return fmt.Sprintf("%d a minute for each agent session, %d at once", l.perMinute, l.burst)
}

// take spends one message of sender's bucket at now. A refused take spends nothing and answers
// the whole seconds until the bucket gives a message back.
func (l *senderLimit) take(sender string, now time.Time) (retryAfterSeconds int, ok bool) {
	if l == nil || l.buckets.AllowAt(sender, now) {
		return 0, true
	}
	return ratelimit.RetryAfterSeconds(l.every), false
}

// limitedSender names the agent session a send or a publish is counted against, or "" for a
// request the limit does not cover. The limit is on what agent sessions send each other: a
// message a session's own caller names it for, with the source "agent" or none, which the
// envelope records as "agent" (messageEnvelope). A caller that presents a broker token is the
// session that token names, whatever its body says (sessionNotBound refuses a body naming
// another). Every other source is some other system's delivery - Dispatch's, a webhook's, a
// person's - and a request naming no session at all has no sender to count, which is how a
// Legion daemon's own notices arrive (packages/daemon/internal/notify).
func limitedSender(r *http.Request, body messageBody) string {
	if source := strings.TrimSpace(body.Source); source != "" && source != "agent" {
		return ""
	}
	if caller, ok := brokerCaller(r); ok && caller.Session != "" {
		return caller.Session
	}
	return strings.TrimSpace(body.SourceSession)
}

// senderLimitRefusal is the 429's body. Error is what a session reads: both clients show a
// refusal's error to the agent as it is written and retry no status below 500
// (packages/envoy-client/src/transport.ts), so the sentence is the whole of what tells a sender
// that its sending is limited, that this message went nowhere, and when to send it again.
type senderLimitRefusal struct {
	Error             string `json:"error"`
	Reason            string `json:"reason"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	MessagesPerMinute int    `json:"messages_per_minute"`
	Burst             int    `json:"burst"`
}

// refuseSenderPastLimit answers a send or a publish of a session past the operator's limit with
// a 429 and reports true; any other request it leaves to the route. It runs once the envelope is
// built and before the route reads a session, resolves a role or touches the bus, so a refused
// message is stored nowhere, kept for no later holder and reports no wake, and its one log line
// names the sender and the message's summary.
func refuseSenderPastLimit(w http.ResponseWriter, r *http.Request, rec *requestRecord, d *listenerDeps, body messageBody) bool {
	sender := limitedSender(r, body)
	if sender == "" {
		return false
	}
	retryAfter, ok := d.senderLimit.take(sender, time.Now())
	if ok {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	rec.refuseWith(w, http.StatusTooManyRequests, outcomeRateLimited, senderLimitRefusal{
		Error: fmt.Sprintf("rate limited: session %s has sent messages faster than this Envoy listener's operator allows "+
			"(%d a minute, %d at once). This message was not sent, and nothing was kept for later. Send it again after %d seconds.",
			sender, d.senderLimit.perMinute, d.senderLimit.burst, retryAfter),
		Reason:            senderLimitReason,
		RetryAfterSeconds: retryAfter,
		MessagesPerMinute: d.senderLimit.perMinute,
		Burst:             d.senderLimit.burst,
	})
	return true
}
