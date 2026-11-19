package daemon

// The sweep's contract with the one rule of whom an update round asks (supervise's
// Claim.OwesUpdateAsk): from the daemon's tick (supervision.reconcilePodUpdates) to the publish of
// each ask it enqueued (the outbox's updateAsk), the sweep asks a claim, and the ask reaches the
// claim's role, exactly where that rule owes it an ask, it has a session, its agent said it is
// ready and a machine of this daemon holds it, over every state, role and fact of a claim, in groups
// of every size and mix an issue's claims make, and over every field of a claim at every depth,
// alone and in pairs. An ask that waited in the outbox while its claim moved on is
// podupdate_undelivered_ask_test.go's.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The sweep owes a claim an ask where the claim's own record owes it one (supervise's
// Claim.OwesUpdateAsk): a claim of the round with a session is owed its ask until an ask of the
// round reached its session or its role answered the round, and a failed claim, one that has left
// the round's pod and a claim in another round are owed none. This is the list before the sweep
// asks whether the claim's agent said it is ready (askRound's supervise.SaidReady), which drops a
// failed claim as it drops every claim that is not ready, so the rule's failed case taken out
// shows here and in no sweep that runs. The control is the ready claim with none of these, which
// the sweep owes its ask.
//
// The negative control is the rule with its failed case taken out: the failed claim is then on
// the list, and its case fails. This test holds each case of the rule as the sweep reads it, and
// the next one (TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoes) holds, over the facts of a
// claim it names, that the sweep refuses and owes no claim by a statement of its own that the
// rule does not make: a sweep that dropped failed claims on its own before it read the rule passes
// this test under that same mutant, and fails the next one.
func TestPodUpdateSweepOwesAnAskWhereTheClaimsOwnRecordOwesOne(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		set  func(*supervise.Claim)
		owed bool
	}{
		{name: "the control: a ready claim of the round with no ask and no answer is owed its ask", set: func(*supervise.Claim) {}, owed: true},
		{name: "a claim an ask of the round reached is owed none", set: func(c *supervise.Claim) { c.UpdateTaskedAt = &at }},
		{name: "a claim that answered the round is owed none", set: func(c *supervise.Claim) { c.UpdateSignalledAt = &at }},
		{name: "a claim that left the round's pod is owed none", set: func(c *supervise.Claim) { c.UpdateLeftRound = podUpdateRound }},
		{name: "a claim that failed is owed none", set: func(c *supervise.Claim) { c.State = supervise.StateFailed }},
		{name: "a claim in another round is owed none", set: func(c *supervise.Claim) { c.UpdateRound = "pod:" + podUpdateReplacementPod }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := supervise.Claim{Token: "legion-legion-implementer", Role: claim.RoleImplementer, Session: "ses_i",
				State: supervise.StateReady, UpdateRound: podUpdateRound}
			tc.set(&c)
			asking := unaskedClaims([]supervise.Claim{c}, podUpdateRound)
			if owed := len(asking) == 1 && asking[0].Token == c.Token; owed != tc.owed || len(asking) > 1 {
				t.Fatalf("the sweep owes an ask to %d claims of one %s claim in round %q, asked %v, answered %v, left %q; want the claim owed=%t",
					len(asking), c.State, c.UpdateRound, c.UpdateTaskedAt, c.UpdateSignalledAt, c.UpdateLeftRound, tc.owed)
			}
		})
	}
}

// sweepFacts is the claims the sweep tests below hold the sweep to the rule over: every state a
// claim can be in (supervise.States), every role a claim can be on (claim.Roles, and the
// controller's, which is none of them), and each of a session, a delivered ask, an answer, a leave
// of the round's pod, another round and a process locator, set or not, which is 64 claims for each
// state and role, in that order. Each claim carries token(role) and records the round's opening at
// at, as the one write of a round on a claim does (supervise's openPodUpdate), so the sweep's ask
// has a bound to state (roundBound); the tests give at as the rig's held clock, so no round they
// build has reached its bound. A state or a role added to those lists joins the tests without an
// edit.
func sweepFacts(at time.Time, token func(claim.Role) claim.Token) []supervise.Claim {
	var claims []supervise.Claim
	for _, state := range supervise.States() {
		for _, role := range sweepRoles() {
			for bits := range 1 << 6 {
				opened := at
				c := supervise.Claim{Token: token(role), Role: role, State: state, UpdateRound: podUpdateRound,
					UpdateOpenedAt: &opened}
				if bits&1 != 0 {
					c.Session = "ses_i"
				}
				if bits&2 != 0 {
					c.UpdateTaskedAt = &at
				}
				if bits&4 != 0 {
					c.UpdateSignalledAt = &at
				}
				if bits&8 != 0 {
					c.UpdateLeftRound = podUpdateRound
				}
				if bits&16 != 0 {
					c.UpdateRound = "pod:" + podUpdateReplacementPod
				}
				if bits&32 != 0 {
					c.Locator = &runtime.Locator{Claim: c.Token, Incarnation: "inc-1"}
				}
				claims = append(claims, c)
			}
		}
	}
	return claims
}

// sweepRoles is every role a claim can be on: claim.Roles, and the controller's.
func sweepRoles() []claim.Role { return append(slices.Clone(claim.Roles), claim.RoleController) }

// sweepPath drives the sweep's whole path over claims a test builds, from the daemon's tick to the
// publish of each ask. The tick is supervision.reconcilePodUpdates, which the orphan sweep calls
// with every claim it read: it groups the claims by issue (reconcileIssuePodUpdate), reads the
// issue's pod and the round open over it, reads the pod's drift, reads the group live from the
// claims' machines (liveGroup), decides whether the round is over (completePodUpdate), and hands
// the live group to openPodUpdate. That hands it to askRound, which reads the sweep's list
// (unaskedClaims), keeps each claim of it whose agent said it is ready and that a machine of this
// daemon holds, reads the round's asks still queued and enqueues one ask for each claim it keeps;
// and openPodUpdate posts PodUpdateOpen to each claim with a session (blockingClaims). The outbox
// then runs each ask's row (the outbox's updateAsk), which publishes it under the claim's own
// machine (supervise's Machine.PublishUpdateAsk). So a statement of the sweep's own placed anywhere
// on that path changes what a test reads here: in reconcilePodUpdates or reconcileIssuePodUpdate,
// in the live read, in openPodUpdate, in askRound, in the list, or in the outbox's publish.
//
// Each claim a test builds is put where every read of it on that path finds it. A machine built
// from the claim itself takes the place of the rig's machine of its token (hold), so the live
// read, askRound's question whether a machine holds the claim, and the publish under the claim's
// lock each read the claim as built. The claims the tick is handed are the same claims, each with
// the issue and the round set and, where a machine holds it, a process in the pod (sweep): those
// are what the tick reads to group the claims and to find the group's pod and the round open over
// it, and the sweep ends a round over any other pod, and opens rather than asks where no claim is
// in one, before openPodUpdate. Every other fact of a claim the tick reads as built, and the
// machine holds the claim whole. A claim of a token no machine holds (the controller's) is read
// live as the tick was handed it (roundGroup), so it is handed with no process: the round is then
// replaced through no claim but one the test built. A group in which no claim waits for the round
// (waitingClaims) is decided before openPodUpdate too (completePodUpdate), and the rule owes no
// claim of such a group an ask, so the path is held to the rule there as well.
//
// The machines a test builds write nothing to the store and answer every name of a claim's session
// as that claim's (sweepClaims), and arm no timer (sweepClock): a test puts hundreds of thousands
// of claims to the path, each a machine of its own for one sweep. The runtime reads every pod there
// and drifted, and records nothing (steadyPod).
// The rig's machines are fed nothing once the path is built: each one's inbox is swapped for one
// no goroutine takes from, and the inbox it had is closed, which ends the goroutine that fed it. So
// each post openPodUpdate makes is kept there for posts to read rather than decided, and no
// machine changes under a test.
type sweepPath struct {
	rig    *podUpdateRig
	posted map[claim.Token]*inbox
	deps   supervise.Deps
	read   []supervise.Claim
}

func newSweepPath(rig *podUpdateRig) *sweepPath {
	p := &sweepPath{rig: rig, posted: map[claim.Token]*inbox{}, deps: rig.sup.deps}
	p.deps.Store, p.deps.Clock, p.deps.Log = sweepClaims{rig.sup.deps.Store}, sweepClock{rig.clock}, slog.New(slog.DiscardHandler)
	rig.s.runtime = steadyPod{rig.rt}
	rig.sup.mu.Lock()
	defer rig.sup.mu.Unlock()
	for token, m := range rig.sup.machines {
		fed := m.inbox
		m.inbox = newInbox()
		fed.close()
		p.posted[token] = m.inbox
	}
	return p
}

// hold puts each claim of group into a machine of its own, in the place of the rig's machine of
// the claim's token. A claim of a token no machine of the rig holds stays unheld.
func (p *sweepPath) hold(group []supervise.Claim) {
	p.rig.t.Helper()
	p.rig.sup.mu.Lock()
	defer p.rig.sup.mu.Unlock()
	for _, c := range group {
		member, ok := p.rig.sup.machines[c.Token]
		if !ok {
			continue
		}
		m, err := supervise.NewMachine(p.rig.sup.ctx, p.deps, c)
		if err != nil {
			p.rig.t.Fatalf("build the machine of %s: %v", describeSwept(c), err)
		}
		member.machine = m
	}
}

// sweep runs the daemon's tick over group, each claim of it read by the tick in the issue and in
// the round over pod, with the rig's drift as the reasons it records, and in a process in pod where
// a machine holds it.
func (p *sweepPath) sweep(group []supervise.Claim, pod string) {
	p.read = append(p.read[:0], group...)
	for i := range p.read {
		c := &p.read[i]
		c.Issue, c.UpdateRound, c.UpdateReasons, c.Locator = podUpdateIssue, supervise.PodRound(pod), podUpdateDrift, nil
		if _, held := p.posted[c.Token]; held {
			c.Locator = podUpdateLocator(c.Token, podUpdateIssue, pod)
		}
	}
	p.rig.s.reconcilePodUpdates(p.rig.ctx, p.read)
}

// posts reports what is wrong with the posts the last sweep made to the claims of group, of the
// tokens a machine of this daemon holds, and clears them. The tick posts PodUpdateOpen of round,
// with the rig's drift as its reasons, once to each claim of group with a session that has not
// retired, and nothing to any other claim; a retired claim is weighed by no round
// (reconcilePodUpdates, roundGroup). Or it posts no opening at all, where it decided the round
// before openPodUpdate because no claim of the group waits for it (completePodUpdate), which it may
// do only where the rule owes no claim of the group with a session an ask; a round so decided may
// be replaced at that sweep, which posts the round's end to its members (endPodUpdates). So it never
// posts the opening to some of those claims and not to others. A claim of a token no machine holds
// has no inbox to read.
func (p *sweepPath) posts(group []supervise.Claim, round string) (wrong []string) {
	got := map[claim.Token]int{}
	posted, ended := false, false
	for token, box := range p.posted {
		box.mu.Lock()
		items := box.items
		box.items = nil
		box.mu.Unlock()
		for _, item := range items {
			ev := item.ev
			if ev == (supervise.PodUpdateOpen{Claim: token, Round: round, Reasons: podUpdateDrift}) {
				got[token]++
				posted = true
				continue
			}
			if ev == (supervise.PodUpdateEnded{Claim: token, Round: round}) {
				ended = true
				continue
			}
			wrong = append(wrong, fmt.Sprintf("the sweep posted %#v to %s, want only PodUpdateOpen or PodUpdateEnded of round %s", ev, token, round))
		}
	}
	for token := range p.posted {
		i := slices.IndexFunc(group, func(c supervise.Claim) bool { return c.Token == token })
		want := posted && i >= 0 && group[i].Session != "" && group[i].State != supervise.StateRetired
		if n := got[token]; (n == 1) != want || n > 1 {
			wrong = append(wrong, fmt.Sprintf("the sweep posted %s the round %d times, want it posted once exactly where the group holds its claim with a session that has not retired, where it posts to any claim (in the group: %t)",
				token, n, i >= 0))
		}
	}
	if posted && ended {
		wrong = append(wrong, fmt.Sprintf("the sweep both posted round %s and ended it at one sweep", round))
	}
	if !posted {
		for _, c := range group {
			_, held := p.posted[c.Token]
			if owed, _ := c.OwesUpdateAsk(round); held && owed && c.Session != "" && c.State != supervise.StateRetired {
				wrong = append(wrong, fmt.Sprintf("the sweep posted the round to no claim of the group, and the rule owes %s an ask", describeSwept(c)))
			}
		}
	}
	return wrong
}

// sweepClaims is the store the machines a sweepPath builds write through: it takes every claim
// write and keeps nothing, so the ask mark a publish writes stays on the machine, and a claim a
// test built with a value the claims table would refuse is not written there. It answers every
// name of a claim's session as held by that claim, as a running daemon's table does for each
// claim's own session, and keeps nothing: the claims a test builds of different roles record the
// same session, and the outbox publishes to a claim only while it holds its session (the outbox's
// heldBack, supervise's Machine.HoldsItsSession), which is no fact of the round's rule.
type sweepClaims struct{ supervise.Store }

func (sweepClaims) PutClaim(context.Context, supervise.Claim) error { return nil }

func (sweepClaims) HoldSessions(_ context.Context, _ string, token claim.Token, holds []supervise.Hold) ([]claim.Token, error) {
	holders := make([]claim.Token, len(holds))
	for i := range holders {
		holders[i] = token
	}
	return holders, nil
}

// sweepClock is the rig's held clock as the machines a sweepPath builds read it, arming nothing: a
// machine built from a launching or registered claim arms its boot watch at once, and a timer per
// claim tried would be kept by the held clock for the whole test.
type sweepClock struct{ *heldClock }

func (sweepClock) AfterFunc(time.Duration, func()) supervise.Cancel { return sweepTimer{} }

type sweepTimer struct{}

func (sweepTimer) Stop() bool { return true }

// steadyPod is the runtime as a sweepPath's tick reads it: every pod is there and has drifted by
// the rig's drift, and nothing of the reads is recorded, since a test reads the pod hundreds of
// thousands of times. Everything else is the rig's runtime.
type steadyPod struct{ runtime.Runtime }

func (steadyPod) PodDrift(context.Context, runtime.Known) ([]string, error) {
	return []string{podUpdateDrift}, nil
}

// sweepRig is the pod-update rig with a ready worker of every workflow role, so a machine holds
// the claim of each role's token, and the controller's token, like any token of no issue, is held
// by none, driven from the daemon's tick to the publish (sweepPath). The round's enqueue is
// recorded rather than written: a test puts groups to the sweep hundreds of thousands of times, and
// the outbox's own rows are the ask-path test's below. Each recorded ask is then run as the
// outbox runs its row (outbox.execute), through the rig's supervisor, so it is published under the
// machine of the claim it names; what reaches each role's topic is read from the listener (heard).
// With nothing written, no ask of the round is ever queued, so each group is asked afresh.
type sweepRig struct {
	rig   *podUpdateRig
	path  *sweepPath
	asked []record.UpdateAsk
	lane  *outbox
	heard *holderPublisher
	row   int64
}

func newSweepRig(t *testing.T) *sweepRig {
	t.Helper()
	workers := make([]podUpdateRole, 0, len(claim.Roles))
	for _, role := range claim.Roles {
		workers = append(workers, readyWorker(role))
	}
	r := &sweepRig{rig: newPodUpdateRig(t, workers...), heard: &holderPublisher{}}
	r.rig.s.log = slog.New(slog.DiscardHandler)
	r.rig.s.enqueue = func(_ context.Context, issue string, rows ...record.OutboxPayload) error {
		for _, row := range rows {
			ask, ok := row.(record.UpdateAsk)
			if !ok || issue != podUpdateIssue {
				t.Fatalf("the sweep enqueued %T on %s, want only update asks of %s", row, issue, podUpdateIssue)
			}
			r.asked = append(r.asked, ask)
		}
		return nil
	}
	r.lane = &outbox{project: podUpdateProject, notices: r.heard, supervisor: r.rig.sup, log: slog.New(slog.DiscardHandler)}
	r.path = newSweepPath(r.rig)
	return r
}

// token is the token of role's claim: the rig's for a workflow role, and for the controller one no
// machine of the rig holds.
func (r *sweepRig) token(role claim.Role) claim.Token {
	if token, ok := r.rig.tokens[role]; ok {
		return token
	}
	return claim.Token("legion-legion-" + string(role))
}

// hold puts group, which holds one claim of a role at most, to the sweep's whole path from the
// daemon's tick to the publish (sweepPath), and reports each claim of it the sweep treats otherwise
// than the rule. The sweep's list (unaskedClaims) must hold a claim exactly where it has a session
// and its own record owes it an ask (supervise's Claim.OwesUpdateAsk); the sweep must enqueue one
// ask of the round for a claim, and that ask's row must reach the claim's role topic once, exactly
// where the list holds the claim, its agent said it is ready (supervise.SaidReady) and a machine of
// this daemon holds it; every row must run without an error; and the tick must post the round to
// each claim with a session that has not retired (sweepPath.posts). asked is how many claims of the
// group are owed an ask that way.
func (r *sweepRig) hold(group []supervise.Claim) (asked int, wrong []string) {
	listed := unaskedClaims(group, podUpdateRound)
	r.asked = r.asked[:0]
	r.path.hold(group)
	r.path.sweep(group, podUpdatePod)
	wrong = r.path.posts(group, podUpdateRound)
	r.heard.mu.Lock()
	r.heard.delivered = r.heard.delivered[:0]
	r.heard.mu.Unlock()
	for _, ask := range r.asked {
		r.row++
		payload, err := json.Marshal(ask)
		if err != nil {
			r.rig.t.Fatalf("encode the ask %+v: %v", ask, err)
		}
		row := record.OutboxRow{ID: r.row, Kind: record.OutboxKindUpdateAsk, Issue: podUpdateIssue, Payload: payload}
		if err := r.lane.execute(r.rig.ctx, row); err != nil {
			wrong = append(wrong, fmt.Sprintf("the row of the ask to the %s of round %s failed: %v", ask.Role, ask.Round, err))
		}
	}
	heard := map[string]int{}
	for _, published := range r.heard.delivered {
		heard[published.topic]++
	}
	for _, c := range group {
		owed, why := c.OwesUpdateAsk(podUpdateRound)
		wantListed := c.Session != "" && owed
		_, held := r.rig.sup.Machine(c.Token)
		wantAsked := wantListed && supervise.SaidReady(c.State) && held
		gotListed, gotAsked := 0, 0
		for _, l := range listed {
			if l.Token == c.Token {
				gotListed++
			}
		}
		for _, a := range r.asked {
			if a.Role == c.Role && a.Round == podUpdateRound {
				gotAsked++
			}
		}
		topic := notify.RoleTopicPrefix + string(c.Token)
		gotHeard := heard[topic]
		delete(heard, topic)
		if wantAsked {
			asked++
		}
		if (gotListed == 1) != wantListed || gotListed > 1 || (gotAsked == 1) != wantAsked || gotAsked > 1 || (gotHeard == 1) != wantAsked || gotHeard > 1 {
			wrong = append(wrong, fmt.Sprintf("%s: the sweep's list holds it %d times, the sweep asks it %d times and %d asks reached its role, where the rule says owed=%t (%v), so want it listed %t, and asked once and reached once %t",
				describeSwept(c), gotListed, gotAsked, gotHeard, owed, why, wantListed, wantAsked))
		}
	}
	for _, a := range r.asked {
		if a.Round != podUpdateRound || !slices.ContainsFunc(group, func(c supervise.Claim) bool { return c.Role == a.Role }) {
			wrong = append(wrong, fmt.Sprintf("the sweep asked %s in round %q, which is no claim of the group's round", a.Role, a.Round))
		}
	}
	for topic, n := range heard {
		wrong = append(wrong, fmt.Sprintf("%d asks reached %s, which is the topic of no claim of the group", n, topic))
	}
	return asked, wrong
}

func describeSwept(c supervise.Claim) string {
	return fmt.Sprintf("a %s %s claim, session %q, locator %t, asked %v, answered %v, left %q, in round %q, pending task %t, told %t",
		c.State, c.Role, c.Session, c.Locator != nil, c.UpdateTaskedAt != nil, c.UpdateSignalledAt != nil, c.UpdateLeftRound,
		c.UpdateRound, c.Pending != nil, c.UpdateToldAt != nil)
}

// sweepRoleCount is how many roles sweepFacts tries, which is also the most claims a group of one
// issue holds: one of each role, the controller's included.
var sweepRoleCount = len(sweepRoles())

// sweepRoleOf is the role, as its index in sweepRoles, of the claim at index i of sweepFacts or of
// a copy of it.
func sweepRoleOf(i int) int { return i / 64 % sweepRoleCount }

// sweepKindsOf is the claims of sweepFacts, by index, that the property test puts in groups of
// four or more and the field test sets a field on: for every state and role, the claim with a
// session, in the round, that no ask of the round reached, that has not answered it and has not
// left its pod — which the rule owes an ask unless it failed — with a process locator and without;
// and that claim refused by one fact alone: an ask of the round reached it, its role answered the
// round, it left the round's pod, or it is in another round; and that claim with no session. A
// claim refused by two of those facts at once is tried alone and in groups of two and three, as
// every claim of sweepFacts is, and not in the larger groups or with a field set: the seven kinds
// a state and role give are what a statement that reads one fact beside the field or the group can
// tell apart, at a ninth of the claims.
func sweepKindsOf(facts []supervise.Claim) []int {
	var kinds []int
	for i := range facts {
		switch i % 64 {
		case 1, 1 | 32, 1 | 2, 1 | 4, 1 | 8, 1 | 16, 0:
			kinds = append(kinds, i)
		}
	}
	return kinds
}

// sweepOwed is whether the rule owes c an ask of the round and c has a session: whether the
// sweep's list holds c (unaskedClaims).
func sweepOwed(c supervise.Claim) bool {
	owed, _ := c.OwesUpdateAsk(podUpdateRound)
	return c.Session != "" && owed
}

// sweepKind is a kind of claim a group is built of: one the rule refuses an ask, one it owes an
// ask (sweepOwed), or either.
type sweepKind int

const (
	sweepRefused sweepKind = iota
	sweepOwedOne
	sweepEither
)

func (k sweepKind) String() string { return [...]string{"refused", "owed", "either"}[k] }

// sweepPools is a set of claims — sweepFacts, or sweepFacts with one more field set — by kind and
// by role, from which groups are built. The roles of a group's other claims, and the claims of
// each role and kind, are each taken in turn, so over a test every claim of a pool stands in
// groups alike, and no claim's place in the set decides what stands beside it.
type sweepPools struct {
	claims []supervise.Claim
	pool   [3][][]int // by kind, then role: indexes into claims
	taken  [3][]int   // by kind, then role: how many claims were taken
	roles  int        // how many roles were taken
}

func newSweepPools(claims []supervise.Claim) *sweepPools {
	p := &sweepPools{claims: claims}
	for k := range p.pool {
		p.pool[k], p.taken[k] = make([][]int, sweepRoleCount), make([]int, sweepRoleCount)
	}
	for i, c := range claims {
		r, k := sweepRoleOf(i), sweepRefused
		if sweepOwed(c) {
			k = sweepOwedOne
		}
		p.pool[k][r] = append(p.pool[k][r], i)
		p.pool[sweepEither][r] = append(p.pool[sweepEither][r], i)
	}
	return p
}

// group puts the claim at index i of the set at index at of a group, beside one claim of each of
// the kinds others names, and returns the group, built in dst. Each other claim is of the next
// role in turn that no claim before it in the group holds and that has a claim of its kind, and it
// is the next claim of that role and kind in turn; where no role left has a claim of its kind, it
// is the next claim of either kind. vary, where it is not nil, is applied to each other claim. So
// a group holds one claim of a role at most, as an issue's claims do.
func (p *sweepPools) group(dst []supervise.Claim, i, at int, others []sweepKind, vary func(*supervise.Claim)) []supervise.Claim {
	dst = dst[:0]
	var held [8]int
	roles := append(held[:0], sweepRoleOf(i))
	for _, kind := range others {
		r, j := p.take(kind, roles)
		roles = append(roles, r)
		dst = append(dst, p.claims[j])
		if vary != nil {
			vary(&dst[len(dst)-1])
		}
	}
	return slices.Insert(dst, at, p.claims[i])
}

// take is the next role in turn that roles does not hold and that has a claim of kind, and the
// index of the next claim of that role and kind; of either kind where no role left has one of
// kind.
func (p *sweepPools) take(kind sweepKind, roles []int) (role, index int) {
	for _, k := range []sweepKind{kind, sweepEither} {
		for range sweepRoleCount {
			r := p.roles % sweepRoleCount
			p.roles++
			if pool := p.pool[k][r]; len(pool) > 0 && !slices.Contains(roles, r) {
				index = pool[p.taken[k][r]%len(pool)]
				p.taken[k][r]++
				return r, index
			}
		}
	}
	panic(fmt.Sprintf("no role is left beside the roles %v, so the group is larger than an issue's claims can make", roles))
}

// sweepShape is one group a claim is put to the sweep in: the kinds of the claims beside it, and
// its place among them, where place < 0 is the claim's index in the set mod the group's size.
type sweepShape struct {
	name   string
	others []sweepKind
	place  int
}

func (s sweepShape) at(i int) int {
	if s.place < 0 {
		return i % (len(s.others) + 1)
	}
	return s.place
}

// sweepPropertyShapes is each group the property test puts a claim to the sweep in: alone; at
// each place of a group of two and of three, beside claims of every mix of the two kinds, owed an
// ask and refused one; and in a group of each size from four to one claim of every role, beside
// claims of which every count from none to all is owed and the rest refused. So every size of
// group an issue's claims make is tried, and every count of owed claims at each size.
func sweepPropertyShapes() []sweepShape {
	shapes := []sweepShape{{name: "alone"}}
	for size := 2; size <= 3; size++ {
		for mix := range 1 << (size - 1) {
			others := make([]sweepKind, size-1)
			for k := range others {
				if others[k] = sweepRefused; mix&(1<<k) != 0 {
					others[k] = sweepOwedOne
				}
			}
			for place := range size {
				shapes = append(shapes, sweepShape{name: fmt.Sprintf("at %d of %d, beside claims %v", place+1, size, others), others: others, place: place})
			}
		}
	}
	for size := 4; size <= sweepRoleCount; size++ {
		for owed := range size {
			others := append(slices.Repeat([]sweepKind{sweepOwedOne}, owed), slices.Repeat([]sweepKind{sweepRefused}, size-1-owed)...)
			shapes = append(shapes, sweepShape{name: fmt.Sprintf("in a group of %d, beside %d claims owed an ask and %d refused one", size, owed, size-1-owed), others: others, place: -1})
		}
	}
	return shapes
}

// The sweep's whole path, from the daemon's tick to the publish (sweepRig.hold), asks a claim, and
// the ask reaches its role, exactly where the claim has a session, its own record owes it an ask
// (supervise's Claim.OwesUpdateAsk), its agent said it is ready and a machine of this daemon holds
// it, over every mix of the facts of a claim sweepFacts names, in groups of every size an issue's
// claims make (sweepPropertyShapes): every claim of sweepFacts alone, and at each place of a group
// of two and of three, beside claims of every mix of the two kinds, one the rule owes an ask and
// one it refuses; and each kind of claim of every state and role (sweepKindsOf) in a group of each
// size from four to one claim of every role, beside claims of every count owed and the rest
// refused. The claims beside it are taken in turn (sweepPools), and each of them holds one more
// field of the claim, a field at any depth taken in turn that keeps it of its kind (claimLeaves).
// Every claim of every group is held to the rule, the claims beside it included, so a group that
// holds two or more claims the sweep owes an ask fails the test where any one of them is not asked
// or not reached.
//
// With the test above it holds the one rule of whom a round asks, which is neither a failed claim
// nor one that has left the round's pod: that test fails when the rule loses a case, and this one
// fails when the sweep refuses or owes a claim by a statement of its own that the rule does not
// make. That statement can stand anywhere from the tick to the publish (sweepPath), and it can read
// the claim or the claims beside it: a filter of its own on one state, on one role or on a locator
// present or absent, its own failed filter put back while the rule's failed case is taken out, its
// left-pod check taken out, a filter on a pending task that holds only in a group of one size or
// only beside a given count of owed claims, a claim refused while another claim of its group is
// owed an ask too, or only the first owed claim of a group asked. The test after it holds the same
// over every field of a claim. None of them fails for a statement of the sweep's own that agrees
// with the rule in every group, such as that failed filter put back with the rule whole: it changes
// no claim's answer, and it shows here the first time the rule changes and the two disagree.
//
// The negative control is the sweep's own failed filter put back with the rule's failed case taken
// out: this test then fails on each failed claim with a session that the rule owes an ask, and the
// test above passes. The test also fails when it saw no claim owed an ask or no claim refused one,
// no group in which the sweep asks two claims, or a size of group in which it asks no claim, since
// such a set shows nothing.
func TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoes(t *testing.T) {
	r := newSweepRig(t)
	at := podUpdateStart
	facts := sweepFacts(at, r.token)
	leaves, unset := claimLeaves()
	if len(unset) > 0 || len(leaves) == 0 {
		t.Fatalf("the claim holds fields this test cannot set (%v), or none it can (%d), so it cannot vary the claims beside each claim", unset, len(leaves))
	}
	pools := newSweepPools(facts)
	next := 0
	// vary sets one more field on a claim beside the claim tried: the next field in turn that the
	// claim leaves empty and whose value keeps it of its kind, owed an ask or refused one.
	vary := func(other *supervise.Claim) {
		owed := sweepOwed(*other)
		for range leaves {
			leaf := leaves[next%len(leaves)]
			next++
			if !leaf.empty(*other) {
				continue
			}
			c := *other
			if leaf.set(&c, at); sweepOwed(c) == owed {
				*other = c
				return
			}
		}
	}
	shapes := sweepPropertyShapes()
	checked, owed, refused, groups, twoAsked := 0, 0, 0, 0, 0
	askedBySize := make([]int, sweepRoleCount+1)
	buf := make([]supervise.Claim, 0, sweepRoleCount)
	every, kinds := make([]int, len(facts)), sweepKindsOf(facts)
	for i := range every {
		every[i] = i
	}
	for _, s := range shapes {
		wrong, seen, example := 0, 0, ""
		tried := every
		if len(s.others) >= 3 {
			tried = kinds
		}
		for n, i := range tried {
			group := pools.group(buf, i, s.at(n), s.others, vary)
			asked, misses := r.hold(group)
			groups++
			seen += len(group)
			owed += asked
			refused += len(group) - asked
			askedBySize[len(group)] += asked
			if asked >= 2 {
				twoAsked++
			}
			wrong += len(misses)
			if example == "" && len(misses) > 0 {
				example = misses[0]
			}
		}
		checked += seen
		if wrong > 0 {
			t.Errorf("%s: the sweep disagrees with the rule on %d of %d claims, for example %s", s.name, wrong, seen, example)
		}
	}
	var noAsk []int
	for size := 1; size <= sweepRoleCount; size++ {
		if askedBySize[size] == 0 {
			noAsk = append(noAsk, size)
		}
	}
	if owed == 0 || refused == 0 || twoAsked == 0 || len(noAsk) > 0 {
		t.Fatalf("checked %d claims of %d groups, %d owed an ask and %d refused one, %d groups in which the sweep asks two claims or more, and groups of the sizes %v in which it asks none: a set with no owed or no refused claim, no group of two asked claims, or a size of group with no ask shows nothing",
			checked, groups, owed, refused, twoAsked, noAsk)
	}
	t.Logf("checked %d claims of %d groups in %d shapes, of every size from 1 to %d: %d owed an ask, %d refused one; in %d groups the sweep asks two claims or more",
		checked, groups, len(shapes), sweepRoleCount, owed, refused, twoAsked)
}

// sweepFieldsEveryClaimSets is the claim's fields that every claim of sweepFacts sets, so the test
// below can set them on no claim, each with why the sweep's tests still hold the sweep over them.
var sweepFieldsEveryClaimSets = map[string]string{
	"Token":          "a claim is known by it; each role's claim has its own, so the test above varies it with the role",
	"Role":           "the test above tries every role",
	"State":          "the test above tries every state",
	"UpdateRound":    "the test above tries the round and another round",
	"UpdateOpenedAt": "every claim of a round records the round's opening, which the ask's bound is read from",
}

// sweepFieldShapes is each group the field test puts a claim to the sweep in: alone, beside one
// claim of another role, and beside one claim of every other role, which is an issue pod with all
// its roles, each claim beside it of either kind.
var sweepFieldShapes = []sweepShape{
	{name: "alone"},
	{name: "beside a claim of another role", others: []sweepKind{sweepEither}, place: -1},
	{name: "beside a claim of every other role", others: slices.Repeat([]sweepKind{sweepEither}, len(claim.Roles)), place: -1},
}

// The sweep's whole path, from the daemon's tick to the publish (sweepRig.hold), asks a claim, and
// the ask reaches its role, exactly where the claim has a session, its own record owes it an ask
// (which it owes neither a failed claim nor one that has left the round's pod), its agent said it
// is ready and a machine of this daemon holds it, whatever else the claim holds. Each exported
// field of supervise.Claim at every depth below it — through structs, pointers, and each slice at
// each of its elements up to launch_failure_limit's shipped value (sweepSliceLength), so each of
// the four budgets, each field of a pending task, of the handshake and of a turn's outcome, with
// one, two and three turn outcomes — is set in turn (claimLeaves), one field at a time: each kind
// of claim of every state and role (sweepKindsOf) that leaves that field empty is put to the sweep
// again with the field set, alone, beside a claim of another role and beside a claim of every other
// role (sweepFieldShapes), each of which holds the field too and is the next claim of its role in
// turn (sweepPools). Then each pair of those fields is set together on the ready implementer the
// rule owes an ask and on that claim refused an ask by each case of the rule or by having no
// session (sweepPairBases). So a filter of the sweep's own on a field the test above does not vary,
// or on two of them together —
// a pending task, a task's text or the moment a turn confirmed it, the round's told mark, the
// claim's deaths or prompt failures, the claim's issue, a claim with more than one or two turn
// outcomes, a pending task on a claim whose round was told — fails here, wherever it stands from
// the tick to the publish, on the claims with a session the rule owes an ask: a sweep that skipped
// every claim holding a task would leave a working role unasked until the round's bound. A field
// added to the claim joins the test without an edit, and a field of a type this test cannot set
// fails it.
//
// The test leaves out the fields every claim of sweepFacts sets (sweepFieldsEveryClaimSets: the
// token, the role, the state, the round and the round's opening), each named with why, and it
// fails when one of them is no field of the claim or is empty on some claim, and when a field it
// does not name is tried on no claim.
//
// Each field is set to one value. So this test does not fail for a filter of the sweep's own on one
// value of a field other than the one it sets: a filter on a claim an operator suspended
// (supervise.SuspendedByOperator) passes it, since SuspendedBy is set to "x" here. A pair is tried
// on the ready implementer and its refused forms alone, so a filter that reads two of these fields
// together with one state or role, or three of them together, passes it too. Compacting and
// ProcessGone are the machine's own state, which its view puts over the claim's record (supervise's
// Machine.View), so the live read finds both false on every claim here, and a filter on either
// placed after the live read passes. Nor does the test fail for a filter that agrees with the rule
// in every group, as the test above says.
//
// The negative control is a filter of the sweep's own on a pending task (Claim.Pending), anywhere
// from the tick to the publish: this test then fails with each field of Pending set, and the test
// above fails too, on the groups beside whose claims a pending task is set. A filter on a pending
// task together with the round's told mark fails the pairs alone. The test also fails when it
// tried no field or no pair, or saw no owed or no refused claim, since such a set shows nothing.
func TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoesWhateverElseTheClaimHolds(t *testing.T) {
	r := newSweepRig(t)
	at := podUpdateStart
	facts := sweepFacts(at, r.token)
	leaves, unset := claimLeaves()
	if len(unset) > 0 {
		t.Fatalf("the fields %v cannot be set, so this test cannot try them", unset)
	}
	top := map[string]bool{}
	for _, f := range reflect.VisibleFields(reflect.TypeOf(supervise.Claim{})) {
		top[f.Name] = true
	}
	for name := range sweepFieldsEveryClaimSets {
		if !top[name] {
			t.Fatalf("%s is named as a field every claim of sweepFacts sets, and the claim has no such field", name)
		}
	}
	checked, owed, refused, fields := 0, 0, 0, 0
	buf := make([]supervise.Claim, 0, sweepRoleCount)
	others := make([]supervise.Claim, len(facts))
	kinds := sweepKindsOf(facts)
	for _, leaf := range leaves {
		for j, c := range facts {
			if others[j] = c; leaf.empty(c) {
				leaf.set(&others[j], at)
			}
		}
		pools := newSweepPools(others)
		tried, wrong, example := 0, 0, ""
		for n, i := range kinds {
			c := facts[i]
			if !leaf.empty(c) {
				continue
			}
			tried++
			for _, s := range sweepFieldShapes {
				group := pools.group(buf, i, s.at(n), s.others, nil)
				asked, misses := r.hold(group)
				checked += len(group)
				owed += asked
				refused += len(group) - asked
				wrong += len(misses)
				if example == "" && len(misses) > 0 {
					example = s.name + ", " + misses[0]
				}
			}
		}
		_, everyClaimSets := sweepFieldsEveryClaimSets[leaf.top]
		switch {
		case everyClaimSets && tried > 0:
			t.Errorf("%s is named as a field every claim of sweepFacts sets, and %d claims leave %s empty: try it, and take it off the list", leaf.top, tried, leaf.name)
		case !everyClaimSets && tried == 0:
			t.Errorf("no claim of sweepFacts leaves %s empty, so this test tried it on no claim: name %s among the fields every claim sets, with why the sweep's tests still hold the sweep over it", leaf.name, leaf.top)
		case tried > 0:
			fields++
		}
		if wrong > 0 {
			t.Errorf("with %s set, the sweep disagrees with the rule on %d claims of %d groups, for example %s; want the sweep to ask a claim, and the ask to reach it, exactly where the rule owes it an ask, it has a session, its agent said it is ready and a machine holds it",
				leaf.name, wrong, tried*len(sweepFieldShapes), example)
		}
	}
	bases := sweepPairBases(at, r.token(claim.RoleImplementer))
	pairs, pairsOwed, pairsWrong := 0, 0, 0
	for i, a := range leaves {
		for _, b := range leaves[i+1:] {
			tried, wrong, example := 0, 0, ""
			for _, base := range bases {
				if !a.empty(base) || !b.empty(base) {
					continue
				}
				c := base
				a.set(&c, at)
				b.set(&c, at)
				asked, misses := r.hold([]supervise.Claim{c})
				tried++
				checked++
				owed += asked
				pairsOwed += asked
				refused += 1 - asked
				wrong += len(misses)
				if example == "" && len(misses) > 0 {
					example = misses[0]
				}
			}
			if tried > 0 {
				pairs++
			}
			if wrong > 0 {
				if pairsWrong++; pairsWrong <= 10 {
					t.Errorf("with %s and %s set together, the sweep disagrees with the rule on %d of %d claims, for example %s",
						a.name, b.name, wrong, tried, example)
				}
			}
		}
	}
	if pairsWrong > 10 {
		t.Errorf("and on %d more pairs of fields", pairsWrong-10)
	}
	if fields == 0 || pairs == 0 || pairsOwed == 0 || owed == 0 || refused == 0 {
		t.Fatalf("checked %d claims over %d fields and %d pairs of fields (%d owed in the pairs), %d owed and %d refused: a set with no field or no pair tried, or no owed or no refused claim, shows nothing",
			checked, fields, pairs, pairsOwed, owed, refused)
	}
	t.Logf("checked %d claims over %d fields of a claim at every depth and %d pairs of them: %d owed an ask, %d refused one", checked, fields, pairs, owed, refused)
}

// sweepPairBases is the claims the field test sets each pair of fields on: the ready implementer of
// the round with a session, which the rule owes an ask, and that claim refused an ask by each case
// of the rule — an ask of the round reached its session, its role answered the round, it left the
// round's pod, it is in another round, it failed — and by having no session. None records a
// process, so every field under the locator is empty on each of them and tried in each pair.
func sweepPairBases(at time.Time, token claim.Token) []supervise.Claim {
	opened := at
	owed := supervise.Claim{Token: token, Role: claim.RoleImplementer, State: supervise.StateReady, Session: "ses_i",
		UpdateRound: podUpdateRound, UpdateOpenedAt: &opened}
	bases := []supervise.Claim{owed}
	for _, refuse := range []func(*supervise.Claim){
		func(c *supervise.Claim) { c.UpdateTaskedAt = &opened },
		func(c *supervise.Claim) { c.UpdateSignalledAt = &opened },
		func(c *supervise.Claim) { c.UpdateLeftRound = podUpdateRound },
		func(c *supervise.Claim) { c.UpdateRound = "pod:" + podUpdateReplacementPod },
		func(c *supervise.Claim) { c.State = supervise.StateFailed },
		func(c *supervise.Claim) { c.Session = "" },
	} {
		c := owed
		refuse(&c)
		bases = append(bases, c)
	}
	return bases
}

// sweepSliceLength is how many elements claimLeaves walks each slice of the claim to:
// launch_failure_limit's shipped value (config.ConfigReference), which is how many turn outcomes a
// claim's window holds (supervise's turnOutcomeWindow) where a deployment leaves the key unset.
func sweepSliceLength() (int, error) {
	for _, key := range config.ConfigReference() {
		if key.Name == "launch_failure_limit" {
			n, err := strconv.Atoi(strings.Trim(key.Default, "`"))
			if err != nil || n < 1 {
				return 0, fmt.Errorf("launch_failure_limit's shipped value %q is no positive number: %v", key.Default, err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("the key table holds no launch_failure_limit")
}

// claimLeaf is one exported field of supervise.Claim at any depth below it, through structs,
// pointers and a slice's elements, that holds a value of its own: a string, a bool, a number or a
// time. name is its path ("Pending.ConfirmedAt", "TurnOutcomes[0].Generation",
// "TurnOutcomes[2].Generation"), and top is the claim's own field it is under.
type claimLeaf struct {
	name, top string
	path      []leafStep
}

// leafStep is one step of a leaf's path: a struct's field (kind reflect.Struct, field its index),
// the value a pointer points to (reflect.Pointer), or a slice's element (reflect.Slice, field its
// index, which gives the slice that index plus one elements).
type leafStep struct {
	kind  reflect.Kind
	field int
}

// claimLeaves is every leaf of supervise.Claim, and the name of each field at any depth that holds
// a value this test cannot set — an interface, a map, a function, a channel, a struct with no
// exported field, or a type that holds itself — which the tests fail on. A slice is walked at each
// of its elements up to sweepSliceLength, the element at index i giving the slice i+1 elements, so
// that a statement that reads how many elements a slice holds (a claim's turn outcomes) is tried
// with one, with two and with three, the most a claim holds under the shipped launch_failure_limit.
func claimLeaves() (leaves []claimLeaf, unset []string) {
	elements, err := sweepSliceLength()
	if err != nil {
		unset = append(unset, fmt.Sprintf("every slice (%v)", err))
	}
	timeType := reflect.TypeOf(time.Time{})
	var walk func(t reflect.Type, name, top string, path []leafStep, seen []reflect.Type)
	walk = func(t reflect.Type, name, top string, path []leafStep, seen []reflect.Type) {
		if t == timeType {
			leaves = append(leaves, claimLeaf{name: name, top: top, path: slices.Clone(path)})
			return
		}
		if slices.Contains(seen, t) {
			unset = append(unset, name+" (a type that holds itself)")
			return
		}
		seen = append(slices.Clone(seen), t)
		switch t.Kind() {
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
			leaves = append(leaves, claimLeaf{name: name, top: top, path: slices.Clone(path)})
		case reflect.Pointer:
			walk(t.Elem(), name, top, append(slices.Clone(path), leafStep{kind: reflect.Pointer}), seen)
		case reflect.Slice:
			for i := range elements {
				walk(t.Elem(), fmt.Sprintf("%s[%d]", name, i), top, append(slices.Clone(path), leafStep{kind: reflect.Slice, field: i}), seen)
			}
		case reflect.Struct:
			exported := 0
			for i := range t.NumField() {
				if f := t.Field(i); f.IsExported() {
					exported++
					walk(f.Type, name+"."+f.Name, top, append(slices.Clone(path), leafStep{kind: reflect.Struct, field: i}), seen)
				}
			}
			if exported == 0 {
				unset = append(unset, fmt.Sprintf("%s (%s, no exported field)", name, t))
			}
		default:
			unset = append(unset, fmt.Sprintf("%s (%s)", name, t))
		}
	}
	claimType := reflect.TypeOf(supervise.Claim{})
	for i := range claimType.NumField() {
		if f := claimType.Field(i); f.IsExported() {
			walk(f.Type, f.Name, f.Name, []leafStep{{kind: reflect.Struct, field: i}}, nil)
		}
	}
	return leaves, unset
}

// empty is whether c leaves the leaf empty: a nil pointer on its path, a slice too short to hold
// the element its path names, or a zero value at its end.
func (l claimLeaf) empty(c supervise.Claim) bool {
	v := reflect.ValueOf(c)
	for _, s := range l.path {
		switch s.kind {
		case reflect.Struct:
			v = v.Field(s.field)
		case reflect.Pointer:
			if v.IsNil() {
				return true
			}
			v = v.Elem()
		case reflect.Slice:
			if v.Len() <= s.field {
				return true
			}
			v = v.Index(s.field)
		}
	}
	return v.IsZero()
}

// set sets the leaf on *c to a value that is not empty: at for a time, "x" for a string, true for
// a bool and 1 for a number. Each pointer and slice on its path is copied first, or made where it
// is nil or too short to hold the element the path names, so no claim that shares one with c is
// written through.
func (l claimLeaf) set(c *supervise.Claim, at time.Time) {
	v := reflect.ValueOf(c).Elem()
	for _, s := range l.path {
		switch s.kind {
		case reflect.Struct:
			v = v.Field(s.field)
		case reflect.Pointer:
			p := reflect.New(v.Type().Elem())
			if !v.IsNil() {
				p.Elem().Set(v.Elem())
			}
			v.Set(p)
			v = p.Elem()
		case reflect.Slice:
			n := max(v.Len(), s.field+1)
			elems := reflect.MakeSlice(v.Type(), n, n)
			reflect.Copy(elems, v)
			v.Set(elems)
			v = elems.Index(s.field)
		}
	}
	switch {
	case v.Type() == reflect.TypeOf(time.Time{}):
		v.Set(reflect.ValueOf(at))
	case v.Kind() == reflect.String:
		v.SetString("x")
	case v.Kind() == reflect.Bool:
		v.SetBool(true)
	case v.CanInt():
		v.SetInt(1)
	case v.CanUint():
		v.SetUint(1)
	case v.CanFloat():
		v.SetFloat(1)
	}
}

// The sweep's ask reaches a ready claim the rule owes an ask whatever else the claim holds, through
// the rig's real outbox: for the rig's ready implementer in the round, the control with nothing
// more set and then each exported field of the claim at every depth that the claim leaves empty,
// set in turn (claimLeaves), the daemon's tick (sweepPath) queues one row of the round's ask, and
// the role-message lane's run of that row delivers one ask to the implementer's topic, exactly
// where the claim's own record owes it one (supervise's Claim.OwesUpdateAsk, which owes a failed
// claim and one that has left the round's pod nothing) and its agent said it is ready; and the
// tick posts the round to the claim once. Through every case the round's ask to another role, the
// tester's, waits in the outbox as a row the lane has not run yet, so the tick asks each case
// beside an ask of the round still queued: askRound skips a claim whose own ask of the round is
// queued (the store's QueuedUpdateAsks), and an ask queued for one role holds back no other role's.
// Each case's rows are run and finished before the next case, so no row of the implementer's that
// an earlier case queued stands in its way. The acceptance test of this change wrote it as the red
// of a filter placed in askRound: a sweep that skipped every claim holding a pending task there
// passed every other test of the sweep, and failed this one. The same filter placed anywhere from
// the tick to the outbox's publish fails it the same way, and so does a sweep that skips every
// claim while any ask of the round is queued.
//
// The negative control is that filter: this test then fails with each field of Pending set, as
// "the tick queued 0 rows and the lane delivered 0 asks". The test also fails when it tried no
// field, or saw no owed or no refused case.
func TestPodUpdateTheSweepsAskReachesEveryOwedReadyClaimWhateverElseItHolds(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
	opened := podUpdateStart
	base := rig.claimOf(claim.RoleImplementer)
	base.UpdateRound, base.UpdateOpenedAt, base.UpdateReasons = podUpdateRound, &opened, podUpdateDrift
	path := newSweepPath(rig)
	leaves, unset := claimLeaves()
	if len(unset) > 0 {
		t.Fatalf("the fields %v cannot be set, so this test cannot try them", unset)
	}
	fields, owedSeen, refusedSeen := 0, 0, 0
	try := func(label string, c supervise.Claim) {
		t.Helper()
		owed, why := c.OwesUpdateAsk(podUpdateRound)
		want := owed && supervise.SaidReady(c.State)
		heard := rig.deliveredTo(claim.RoleImplementer)
		other := record.UpdateAsk{Role: claim.RoleTester, Round: podUpdateRound, Pod: podUpdatePod,
			Text: supervise.UpdateTask(podUpdateRound, podUpdateDrift, podUpdateStart.Add(podUpdateGrace))}
		if err := rig.s.enqueue(rig.ctx, podUpdateIssue, other); err != nil {
			t.Fatalf("%s: queue the tester's ask of the round: %v", label, err)
		}
		group := []supervise.Claim{c}
		path.hold(group)
		path.sweep(group, podUpdatePod)
		if wrong := path.posts(group, podUpdateRound); len(wrong) > 0 {
			t.Errorf("%s: %s", label, strings.Join(wrong, "; "))
		}
		queued, others := 0, 0
		for _, row := range rig.rows(record.OutboxKindUpdateAsk) {
			var ask record.UpdateAsk
			if err := json.Unmarshal(row.Payload, &ask); err != nil {
				t.Fatalf("%s: decode update ask %d: %v", label, row.ID, err)
			}
			switch {
			case ask == other:
				others++
			case ask.Role == claim.RoleImplementer && ask.Round == podUpdateRound:
				queued++
			default:
				t.Fatalf("%s: an ask of round %s went to %s, want only the implementer's of round %s beside the tester's queued one", label, ask.Round, ask.Role, podUpdateRound)
			}
		}
		if others != 1 {
			t.Fatalf("%s: the outbox holds %d rows of the tester's ask, want the one this case queued", label, others)
		}
		rig.runLane()
		got := rig.deliveredTo(claim.RoleImplementer) - heard
		if (queued == 1) != want || queued > 1 || (got == 1) != want || got > 1 {
			t.Errorf("%s: the tick queued %d rows and the lane delivered %d asks to the ready implementer, state %s; the rule says owed=%t (%v); want one of each exactly where the rule owes one and the agent said it is ready",
				label, queued, got, c.State, owed, why)
		}
		if want {
			owedSeen++
		} else {
			refusedSeen++
		}
	}
	try("the control, no field set", base)
	for _, leaf := range leaves {
		if leaf.top == "UpdateRound" || !leaf.empty(base) {
			continue
		}
		c := base
		leaf.set(&c, opened)
		fields++
		try("with "+leaf.name+" set", c)
	}
	if fields == 0 || owedSeen == 0 || refusedSeen == 0 {
		t.Fatalf("tried %d fields, %d owed and %d refused: a test with no field tried, or no owed or no refused case, shows nothing", fields, owedSeen, refusedSeen)
	}
	t.Logf("tried the control and %d fields of a claim at every depth from the tick to the publish: %d owed an ask, %d refused one", fields, owedSeen, refusedSeen)
}
