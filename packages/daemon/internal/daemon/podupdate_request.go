package daemon

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// requestedPodUpdateReasons is the reasons a round the project's controller asked for records
// (requestPodUpdate). The round's ask states them to each role ("The reasons are: …",
// supervise.UpdateTask), and every later notice of the round names them, as a drift's round
// names what drifted.
const requestedPodUpdateReasons = "the project's controller asked for the replacement of this pod"

// requestPodUpdate opens the update round over issue's pod on the request of the project's
// controller (api.Options.RequestPodUpdate, the route POST /legion/v1/issues/pod-update). The
// round is the one a drift opens, opened the way the sweep opens it: posted to every claim of the
// issue that has run a worker (openPodUpdate), charged to the project's limits as the sweep
// charges an opening (recordPodUpdateOpening), and carried from then on by the sweep alone. Its
// reasons say the controller asked for it (requestedPodUpdateReasons). The sweep asks each role
// of the pod, at the first sweep that reads the round recorded on its claim, to push its work and
// answer ready_for_update (askRound), and replaces the pod once every role it asks has answered,
// or at the round's bound whatever they said (completePodUpdate). No drift is needed for any of
// that: a round open over the pod its claims record goes on, drift or no drift, until its pod is
// replaced or gone (reconcileIssuePodUpdate).
//
// It reads the issue as the sweep groups it — every claim of the issue that has not retired — and
// answers, in this order:
//
//   - ctx ending while a sweep's walk holds the turn the request waits for (podUpdateWalk):
//     refused, the sweep is busy (api.PodUpdateBusy), with nothing read and nothing opened, and
//     the answer asks the caller to wait one sweep interval before it asks again.
//   - no such claim: refused (api.PodUpdateNoClaim). An issue of another project is one, since
//     this daemon runs no claim of it.
//   - a round open over the issue's pod: that round, opened nothing new. A round over the pod the
//     claims record, a round whose claims record no pod at all (podlessRound carries it) and a
//     round whose claims record two pods (a replacement in flight) are each the one this issue's
//     pod is in.
//   - claims in two pods with no round open, or a round open over a pod the claims no longer
//     record, which the next sweep ends: refused, the pod is changing (api.PodUpdatePodChanging).
//     Opened over the pod in its place, the round would write over the record of the round before
//     it that the sweep reads to say what that pod took with it (podWentWithoutItsRound).
//   - no claim records a pod: refused (api.PodUpdateNoPod).
//   - no claim the round could ask — none has a session, or every one that has has failed, so no
//     claim would record the round: refused (api.PodUpdateNoPod).
//   - this daemon's own opening of the round still in flight, posted and not yet recorded by any
//     claim: that opening, posted again with its own reasons, opened nothing new.
//   - a limit of the project that holds the opening back, read as the sweep reads it
//     (beginPodUpdateSweep, podUpdateSweep.holds): refused, naming the limit
//     (api.PodUpdateLimited). The request is not kept: the controller asks again.
//   - otherwise the round is opened, and answered with opened true.
//
// A round it posted, opened or in flight, is answered only once a claim of the issue records it
// in the store (awaitPodRoundRecorded). The post is asynchronous and lives in memory alone
// (supervisor.post): a daemon that stops before a machine takes it loses it, and the record of
// the opening this daemon keeps (podUpdateOpened) goes with it. A round a claim records is the
// store's, which a restart restores, and every sweep from then on carries it, posts it again to
// every claim it blocks, and replaces its pod, drift or no drift (reconcileIssuePodUpdate). So a
// 200 always names a round the sweeps carry to its pod's replacement. A round no claim records
// within the bound is refused (api.PodUpdateNotRecorded): its post may still land, which a later
// request answers as the round open over the pod.
//
// It holds the walk's turn (podUpdateWalk) from its read of the claims to its post, so the claims
// it decides from are no older than the last sweep's walk, the limits it reads are the ones it
// charges, and no sweep's walk opens a round on them in between. It waits for that turn only as
// long as ctx lives, since a sweep's walk holds it across the store reads and writes of every
// issue of the project. The wait for the record is made after, outside that turn: a machine in a
// relaunch can hold the post for minutes, and the sweep's walk must not wait on it.
func (s *supervision) requestPodUpdate(ctx context.Context, issue string) (api.PodUpdateResponse, error) {
	answer, asked, err := s.postRequestedPodUpdate(ctx, issue)
	if err != nil || len(asked) == 0 {
		return answer, err
	}
	if err := s.awaitPodRoundRecorded(ctx, issue, answer.Round, asked); err != nil {
		return api.PodUpdateResponse{}, err
	}
	return answer, nil
}

// postRequestedPodUpdate is requestPodUpdate's decision, made holding the walk's turn
// (podUpdateWalk), and the post of the round it decides on. It answers the round and the claims
// the post asks (askedClaims), whose record of it the answer then waits for; none where the claims
// record the round already.
func (s *supervision) postRequestedPodUpdate(ctx context.Context, issue string) (api.PodUpdateResponse, []claim.Token, error) {
	turn := s.podUpdateWalkTurn()
	select {
	case turn <- struct{}{}:
	case <-ctx.Done():
		// One sweep interval (plan.orphanSweep, a minute in a deployment) is the wait the answer
		// asks for. Only the sweep's walk holds the turn this long, one walk at a time and one a
		// sweep interval, so a walk that outlived the request's whole wait is one running long.
		// Asked again sooner, the request queues behind that same walk; asked a sweep interval
		// later, it comes after the time the sweep itself gives that walk before it walks again.
		retry := s.plan.orphanSweep
		return api.PodUpdateResponse{}, nil, &api.PodUpdateRefused{Refusal: api.PodUpdateBusy, RetryAfter: retry, Message: fmt.Sprintf(
			"a sweep of this daemon's update rounds held its walk over the project's issues until the request for %s ended (%v). Nothing was read and nothing was opened; ask again in %s, once that sweep has moved on",
			issue, ctx.Err(), retry)}
	}
	defer func() { <-turn }()
	claims, err := s.supervisor.Claims(ctx)
	if err != nil {
		return api.PodUpdateResponse{}, nil, fmt.Errorf("read the claims of %s: %w", issue, err)
	}
	_, byIssue := podUpdateGroups(claims)
	group := byIssue[issue]
	if len(group) == 0 {
		return refusePodUpdate(api.PodUpdateNoClaim,
			"this daemon supervises no claim of %s that has not retired, so it runs no pod of that issue", issue)
	}
	pod, split := groupPod(group)
	open := openPodRound(group)
	if open != "" && (split || pod == "" || open == supervise.PodRound(pod)) {
		return api.PodUpdateResponse{Round: open, Reasons: roundReasons(group, open)}, nil, nil
	}
	switch {
	case split:
		return refusePodUpdate(api.PodUpdatePodChanging,
			"the claims of %s record two pods: a replacement of the issue's pod is in flight, and a sweep reads it settled once that replacement ends. Nothing was opened; ask again then", issue)
	case open != "":
		return refusePodUpdate(api.PodUpdatePodChanging,
			"the update round %s of %s is over a pod its claims no longer record, and a sweep ends it once it reads the pod gone. Nothing was opened; ask again then", open, issue)
	case pod == "":
		return refusePodUpdate(api.PodUpdateNoPod,
			"no claim of %s records a process in a pod: each one is suspended or between launches, or runs in no pod at all. Nothing was opened; ask again once a claim of the issue runs in its pod", issue)
	}
	live := s.liveGroup(group)
	asked := askedClaims(live)
	if len(asked) == 0 {
		return refusePodUpdate(api.PodUpdateNoPod,
			"no claim of %s runs a worker in its pod %s that an update round could ask: none has a session, or each one that has has failed. Nothing was opened; retry a failed claim, or ask again once a claim of the issue has registered its agent", issue, pod)
	}
	tokens := make([]claim.Token, len(asked))
	for i, c := range asked {
		tokens[i] = c.Token
	}
	round := supervise.PodRound(pod)
	now := s.plan.clock.Now()
	sweep := s.beginPodUpdateSweep(now, byIssue)
	if opening, again := sweep.reposts(issue, round); again {
		// Posted again, as the sweep posts an opening of its own still in flight: the post that
		// opened it may have gone to claims this read no longer asks, and a machine that records
		// the round already takes nothing from it.
		s.openPodUpdate(ctx, issue, live, pod, opening.reasons)
		return api.PodUpdateResponse{Round: round, Reasons: opening.reasons}, tokens, nil
	}
	if limit, held := sweep.holds(issue); held {
		return refusePodUpdate(api.PodUpdateLimited, "%s", s.podUpdateLimitHolds(issue, limit))
	}
	s.openPodUpdate(ctx, issue, live, pod, requestedPodUpdateReasons)
	s.recordPodUpdateOpening(&sweep, issue, round, requestedPodUpdateReasons, now)
	s.log.Info("supervise: the project's controller asked for the replacement of this issue's pod; its update round is posted",
		"issue", issue, "round", round, "pod", pod, "roles", namesOf(blockingClaims(live)))
	return api.PodUpdateResponse{Round: round, Reasons: requestedPodUpdateReasons, Opened: true}, tokens, nil
}

// defaultPodRoundRecordBound is how long the controller's request waits for a claim to record the
// round it posted (awaitPodRoundRecorded) where supervision sets no bound of its own.
const defaultPodRoundRecordBound = 30 * time.Second

// awaitPodRoundRecorded waits until a claim of issue records round in the store, or the bound
// passes (supervision.podRoundRecordBound, defaultPodRoundRecordBound where unset). Each asked claim's
// machine is handed a mark behind every event posted to it before — the round's post among them —
// which its feeder closes once it has handled them (supervisor.drained), and the store is read
// once a mark closes, so no poll and no clock decides it: a machine that takes the post writes the
// round to its claim's row before it reaches the mark (supervise's openPodUpdate). A machine
// busy in a long decision, a relaunch, holds its mark until that decision ends, and a round no
// claim has recorded at the bound is refused (api.PodUpdateNotRecorded).
func (s *supervision) awaitPodRoundRecorded(ctx context.Context, issue, round string, asked []claim.Token) error {
	bound := s.podRoundRecordBound
	if bound <= 0 {
		bound = defaultPodRoundRecordBound
	}
	waiting, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	handled := make(chan struct{}, len(asked))
	for _, token := range asked {
		done := make(chan struct{})
		s.supervisor.drained(token, done)
		go func() {
			select {
			case <-done:
				handled <- struct{}{}
			case <-waiting.Done():
			}
		}()
	}
	for range asked {
		select {
		case <-handled:
		case <-waiting.Done():
			return s.podRoundUnrecorded(ctx, issue, round, bound)
		}
		recorded, err := s.podRoundRecorded(ctx, issue, round)
		if err != nil {
			return err
		}
		if recorded {
			return nil
		}
	}
	return s.podRoundUnrecorded(ctx, issue, round, bound)
}

// podRoundUnrecorded is the refusal of a round no claim recorded, unless the store, read once
// more, says one has.
func (s *supervision) podRoundUnrecorded(ctx context.Context, issue, round string, bound time.Duration) error {
	if recorded, err := s.podRoundRecorded(ctx, issue, round); err != nil || recorded {
		return err
	}
	return &api.PodUpdateRefused{Refusal: api.PodUpdateNotRecorded, Message: fmt.Sprintf(
		"the daemon posted the update round %s to the claims of %s, and no claim recorded it within %s: a claim's machine is busy, as in a relaunch, or the claim stopped. The round is not one the sweeps carry yet. Read updateRound on the claims of %s, and ask again: a later request answers the round once a claim records it",
		round, issue, bound, issue)}
}

// podRoundRecorded is whether a claim of issue that has not retired records round in the store.
func (s *supervision) podRoundRecorded(ctx context.Context, issue, round string) (bool, error) {
	claims, err := s.supervisor.Claims(ctx)
	if err != nil {
		return false, fmt.Errorf("read the claims of %s for the record of round %s: %w", issue, round, err)
	}
	return slices.ContainsFunc(claims, func(c supervise.Claim) bool {
		return c.Issue == issue && c.State != supervise.StateRetired && memberOf(c, round)
	}), nil
}

// podUpdateLimitHolds is the sentence of a refusal a limit of the project made (requestPodUpdate),
// naming the limit as legion.yaml names it and the number it is set to.
func (s *supervision) podUpdateLimitHolds(issue string, limit podUpdateLimit) string {
	switch limit {
	case limitPodUpdateOpenRounds:
		return fmt.Sprintf("the project's %s (%d) holds back the update round of %s: that many issues hold an open update round now. Nothing was opened; ask again once one of those rounds ends",
			limit, *s.cfg.PodUpdateMaxOpenRounds, issue)
	case limitPodUpdateRoundsPerMinute:
		return fmt.Sprintf("the project's %s (%d) holds back the update round of %s: this daemon opened an update round less than a minute ago. Nothing was opened; ask again in a minute",
			limit, *s.cfg.PodUpdateRoundsPerMinute, issue)
	default:
		return fmt.Sprintf("the project's %s holds back the update round of %s. Nothing was opened; ask again later", limit, issue)
	}
}

// refusePodUpdate is postRequestedPodUpdate's refusal, its sentence written as format says.
func refusePodUpdate(refusal api.PodUpdateRefusal, format string, args ...any) (api.PodUpdateResponse, []claim.Token, error) {
	return api.PodUpdateResponse{}, nil, &api.PodUpdateRefused{Refusal: refusal, Message: fmt.Sprintf(format, args...)}
}
