package daemon

// The sweep's contract with the one rule of whom an update round asks (supervise's
// Claim.OwesUpdateAsk): from openPodUpdate to the ask it enqueues, the sweep asks a claim exactly
// where that rule owes it an ask, it has a session, its agent said it is ready and a machine of
// this daemon holds it, over every state, role and fact of a claim, in groups of every size an
// issue's claims make, and over every field of a claim at every depth. The delivery of an ask
// once the sweep enqueued it is podupdate_undelivered_ask_test.go's.

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
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
// state and role, in that order. Each claim carries token(role) and records the round's opening,
// as the one write of a round on a claim does (supervise's openPodUpdate), so the sweep's ask has
// a bound to state (roundBound). A state or a role added to those lists joins the tests without an
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

// roundOpener drives the sweep's whole path to the ask from its top, openPodUpdate, over claims a
// test builds. openPodUpdate hands the group it is given to askRound, which reads the sweep's list
// (unaskedClaims), keeps each claim of it whose agent said it is ready and that a machine of this
// daemon holds, reads the round's asks still queued, and enqueues one ask for each claim it keeps;
// and it posts PodUpdateOpen to each claim with a session (blockingClaims). So a statement of the
// sweep's own placed anywhere on that path — in openPodUpdate, in askRound or in the list — changes
// what a test reads here. The ask-path test and the sweep's rig below both open their rounds
// through it.
//
// The rig's machines are fed nothing once the opener is built: each one's inbox is swapped for one
// no goroutine takes from, and the inbox it had is closed, which ends the goroutine that fed it. So
// each post openPodUpdate makes is kept there for open to read rather than decided: the claims a
// test builds are the only claims the sweep reads, and no machine changes under a test that puts
// hundreds of thousands of groups to the sweep.
type roundOpener struct {
	rig    *podUpdateRig
	posted map[claim.Token]*inbox
}

func newRoundOpener(rig *podUpdateRig) *roundOpener {
	o := &roundOpener{rig: rig, posted: map[claim.Token]*inbox{}}
	rig.sup.mu.Lock()
	defer rig.sup.mu.Unlock()
	for token, m := range rig.sup.machines {
		fed := m.inbox
		m.inbox = newInbox()
		fed.close()
		o.posted[token] = m.inbox
	}
	return o
}

// open puts group to openPodUpdate as the round over pod, with the rig's drift as its reasons, and
// reports each claim of group a machine of this daemon holds that was not posted exactly one
// PodUpdateOpen of that round where it has a session and nothing where it has none, and each post
// to a claim of no claim of the group. The posts are cleared after.
func (o *roundOpener) open(group []supervise.Claim, pod string) (wrong []string) {
	round := supervise.PodRound(pod)
	o.rig.s.openPodUpdate(o.rig.ctx, podUpdateIssue, group, pod, podUpdateDrift)
	for token, box := range o.posted {
		box.mu.Lock()
		events := box.events
		box.events = nil
		box.mu.Unlock()
		i := slices.IndexFunc(group, func(c supervise.Claim) bool { return c.Token == token })
		want := i >= 0 && group[i].Session != ""
		got := 0
		for _, ev := range events {
			if ev == (supervise.PodUpdateOpen{Claim: token, Round: round, Reasons: podUpdateDrift}) {
				got++
				continue
			}
			wrong = append(wrong, fmt.Sprintf("openPodUpdate posted %#v to %s, want only PodUpdateOpen of round %s", ev, token, round))
		}
		if (got == 1) != want || got > 1 {
			wrong = append(wrong, fmt.Sprintf("openPodUpdate posted %s the round %d times, want it posted once exactly where the group holds its claim with a session (in the group: %t)",
				token, got, i >= 0))
		}
	}
	return wrong
}

// sweepRig is the pod-update rig with a ready worker of every workflow role, so a machine holds
// the claim of each role's token, and the controller's token, like any token of no issue, is held
// by none, driven from openPodUpdate (roundOpener). The round's enqueue is recorded rather than
// written: a test puts groups to the sweep hundreds of thousands of times, and the outbox's row is
// the ask-path test's below, and its publish podupdate_undelivered_ask_test.go's. With nothing
// written, no ask of the round is ever queued, so each group is asked afresh.
type sweepRig struct {
	rig    *podUpdateRig
	opener *roundOpener
	asked  []record.UpdateAsk
}

func newSweepRig(t *testing.T) *sweepRig {
	t.Helper()
	workers := make([]podUpdateRole, 0, len(claim.Roles))
	for _, role := range claim.Roles {
		workers = append(workers, readyWorker(role))
	}
	r := &sweepRig{rig: newPodUpdateRig(t, workers...)}
	r.rig.s.log = quietLogger()
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
	r.opener = newRoundOpener(r.rig)
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

// hold puts group, which holds one claim of a role at most, to the sweep's whole path from
// openPodUpdate (roundOpener), and reports each claim of it the sweep treats otherwise than the
// rule. The sweep's list (unaskedClaims) must hold a claim exactly where it has a session and its
// own record owes it an ask (supervise's Claim.OwesUpdateAsk), the sweep must enqueue one ask of
// the round for a claim exactly where the list holds it, its agent said it is ready
// (supervise.SaidReady) and a machine of this daemon holds it, and openPodUpdate must post the
// round to each claim with a session (roundOpener.open). asked is how many claims of the group are
// owed an ask that way.
func (r *sweepRig) hold(group []supervise.Claim) (asked int, wrong []string) {
	listed := unaskedClaims(group, podUpdateRound)
	r.asked = r.asked[:0]
	wrong = r.opener.open(group, podUpdatePod)
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
		if wantAsked {
			asked++
		}
		if (gotListed == 1) != wantListed || gotListed > 1 || (gotAsked == 1) != wantAsked || gotAsked > 1 {
			wrong = append(wrong, fmt.Sprintf("%s: the sweep's list holds it %d times and the sweep asks it %d times, where the rule says owed=%t (%v), so want it listed %t and asked %t",
				describeSwept(c), gotListed, gotAsked, owed, why, wantListed, wantAsked))
		}
	}
	for _, a := range r.asked {
		if a.Round != podUpdateRound || !slices.ContainsFunc(group, func(c supervise.Claim) bool { return c.Role == a.Role }) {
			wrong = append(wrong, fmt.Sprintf("the sweep asked %s in round %q, which is no claim of the group's round", a.Role, a.Round))
		}
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

// sweepPropertyShapes is each group the property test puts every claim to the sweep in: alone; at
// each place of a group of two and of three, beside claims of every mix of the two kinds, owed an
// ask and refused one; and in a group of each size from four to one claim of every role, beside
// claims all owed and beside claims all refused. So every size of group an issue's claims make is
// tried, and every group of two or three of the kinds.
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
		for _, kind := range []sweepKind{sweepOwedOne, sweepRefused} {
			others := slices.Repeat([]sweepKind{kind}, size-1)
			shapes = append(shapes, sweepShape{name: fmt.Sprintf("in a group of %d, beside claims all %s", size, kind), others: others, place: -1})
		}
	}
	return shapes
}

// The sweep's whole path to the ask, from openPodUpdate (sweepRig.hold), asks a claim exactly
// where the claim has a session, its own record owes it an ask (supervise's Claim.OwesUpdateAsk),
// its agent said it is ready and a machine of this daemon holds it, over every mix of the facts of
// a claim sweepFacts names, in groups of every size an issue's claims make (sweepPropertyShapes):
// alone; at each place of a group of two and of three, beside claims of every mix of the two
// kinds, one the rule owes an ask and one it refuses; and in a group of each size from four to one
// claim of every role, beside claims all owed and beside claims all refused. The claims beside it
// are taken in turn (sweepPools), and each of them holds one more field of the claim, a field at
// any depth taken in turn that keeps it of its kind (claimLeaves). Every claim of every group is
// held to the rule, the claims beside it included, so a group that holds two or more claims the
// sweep owes an ask fails the test where any one of them is not asked.
//
// With the test above it holds the one rule of whom a round asks, which is neither a failed claim
// nor one that has left the round's pod: that test fails when the rule loses a case, and this one
// fails when the sweep refuses or owes a claim by a statement of its own that the rule does not
// make. That statement can stand in openPodUpdate, which hands the group to askRound, in askRound,
// which filters the sweep's list again before it enqueues the ask, or in the list, and it can read
// the claim or the claims beside it: a filter of its own on one state, on one role or on a locator
// present or absent, its own failed filter put back while the rule's failed case is taken out, its
// left-pod check taken out, a filter on a pending task that holds only in a group of one size, a
// claim refused while another claim of its group is owed an ask too, or only the first owed claim
// of a group asked. The test after it holds the same over every field of a claim. None of them
// fails for a statement of the sweep's own that agrees with the rule in every group, such as that
// failed filter put back with the rule whole: it changes no claim's answer, and it shows here the
// first time the rule changes and the two disagree.
//
// The negative control is the sweep's own failed filter put back with the rule's failed case taken
// out: this test then fails on each failed claim with a session that the rule owes an ask, and the
// test above passes. The test also fails when it saw no claim owed an ask or no claim refused one,
// no group in which the sweep asks two claims, or a size of group in which it asks no claim, since
// such a set shows nothing.
func TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoes(t *testing.T) {
	r := newSweepRig(t)
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
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
	for _, s := range shapes {
		wrong, seen, example := 0, 0, ""
		for i := range facts {
			group := pools.group(buf, i, s.at(i), s.others, vary)
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

// The sweep's whole path to the ask, from openPodUpdate, asks a claim exactly where the claim has
// a session, its own record owes it an ask (which it owes neither a failed claim nor one that has
// left the round's pod), its agent said it is ready and a machine of this daemon holds it
// (sweepRig.hold), whatever else the claim holds. Each exported field of supervise.Claim at every
// depth below it — through structs, pointers, and each slice at its first element and again at
// its second, so each of the four budgets, each field of a pending task, of the handshake and of
// a turn's outcome, with one turn outcome and with two — is set in turn (claimLeaves), one field at
// a time: every claim of sweepFacts that leaves that field empty is put to the sweep again with
// the field set, alone, beside a claim of another role and beside a claim of every other role
// (sweepFieldShapes), each of which holds the field too and is the next claim of its role in turn
// (sweepPools). So a filter of the sweep's own on a field the test above does not vary — a pending
// task, a task's text or the moment a turn confirmed it, the round's told mark, the claim's deaths
// or prompt failures, the claim's issue, a claim with more than one turn outcome — fails here, in
// openPodUpdate, in askRound and in the sweep's list alike, on the claims with a session the rule
// owes an ask: a sweep that skipped every claim holding a task would leave a working role unasked
// until the round's bound, and every test of the sweep before these passed under it. A field added
// to the claim joins the test without an edit, and a field of a type this test cannot set fails
// it.
//
// The test leaves out the fields every claim of sweepFacts sets (sweepFieldsEveryClaimSets: the
// token, the role, the state, the round and the round's opening), each named with why, and it
// fails when one of them is no field of the claim or is empty on some claim, and when a field it
// does not name is tried on no claim.
//
// Each field is set to one value, and one field at a time. So this test does not fail for a
// filter of the sweep's own on one value of a field other than the one it sets: a filter on a
// claim an operator suspended (supervise.SuspendedByOperator) passes it, since SuspendedBy is set
// to "x" here. Nor does it fail for a filter on two fields set together, such as a pending task on
// a claim whose round was told, since no claim here holds both; nor for a filter that agrees with
// the rule in every group, as the test above says.
//
// The negative control is a filter of the sweep's own on a pending task (Claim.Pending), in
// openPodUpdate, in askRound or in the sweep's list: this test then fails with each field of
// Pending set, and the test above fails too, on the groups beside whose claims a pending task is
// set. The test also fails when it tried no field, or saw no owed or no refused claim, since such
// a set shows nothing.
func TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoesWhateverElseTheClaimHolds(t *testing.T) {
	r := newSweepRig(t)
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
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
	for _, leaf := range leaves {
		for j, c := range facts {
			if others[j] = c; leaf.empty(c) {
				leaf.set(&others[j], at)
			}
		}
		pools := newSweepPools(others)
		tried, wrong, example := 0, 0, ""
		for i, c := range facts {
			if !leaf.empty(c) {
				continue
			}
			tried++
			for _, s := range sweepFieldShapes {
				group := pools.group(buf, i, s.at(i), s.others, nil)
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
			t.Errorf("with %s set, the sweep disagrees with the rule on %d claims of %d groups, for example %s; want the sweep to ask a claim exactly where the rule owes it an ask, it has a session, its agent said it is ready and a machine holds it",
				leaf.name, wrong, tried*len(sweepFieldShapes), example)
		}
	}
	if fields == 0 || owed == 0 || refused == 0 {
		t.Fatalf("checked %d claims over %d fields, %d owed and %d refused: a set with no field tried, or no owed or no refused claim, shows nothing", checked, fields, owed, refused)
	}
	t.Logf("checked %d claims over %d fields of a claim at every depth: %d owed an ask, %d refused one", checked, fields, owed, refused)
}

// claimLeaf is one exported field of supervise.Claim at any depth below it, through structs,
// pointers and a slice's first or second element, that holds a value of its own: a string, a
// bool, a number or a time. name is its path ("Pending.ConfirmedAt", "TurnOutcomes[0].Generation",
// "TurnOutcomes[1].Generation"), and top is the claim's own field it is under.
type claimLeaf struct {
	name, top string
	path      []leafStep
}

// leafStep is one step of a leaf's path: a struct's field (kind reflect.Struct, field its index),
// the value a pointer points to (reflect.Pointer), or a slice's element (reflect.Slice, field its
// index: 0, or 1 for a slice of two elements).
type leafStep struct {
	kind  reflect.Kind
	field int
}

// claimLeaves is every leaf of supervise.Claim, and the name of each field at any depth that holds
// a value this test cannot set — an interface, a map, a function, a channel, a struct with no
// exported field, or a type that holds itself — which the tests fail on. A slice is walked twice:
// at its first element, which gives the slice one element, and at its second, which gives it two,
// so that a statement that reads how many elements a slice holds (a claim's turn outcomes) is
// tried with one and with more than one.
func claimLeaves() (leaves []claimLeaf, unset []string) {
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
			for i := range 2 {
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
// the rig's real outbox: for the rig's ready implementer, the control with nothing more set and
// then each exported field of the claim at every depth that the claim leaves empty, set in turn
// (claimLeaves), the sweep's whole path from openPodUpdate (roundOpener) queues one ask of a round
// of the case's own exactly where the claim's own record owes it one (supervise's
// Claim.OwesUpdateAsk, which owes a failed claim and one that has left the round's pod nothing)
// and its agent said it is ready, and posts the round to the claim once. Each case is its own
// round, over a pod of its own, so no ask an earlier case queued stands in its way. The acceptance
// test of this change wrote it as the red of a filter placed in askRound: a sweep that skipped
// every claim holding a pending task there passed every other test of the sweep, and failed this
// one. The same filter placed one call higher, in openPodUpdate, fails it the same way.
//
// The negative control is that filter: this test then fails with each field of Pending set, as
// "the sweep queued 0 asks of the ready implementer". The test also fails when it tried no field,
// or saw no owed or no refused case.
func TestPodUpdateTheSweepsAskReachesEveryOwedReadyClaimWhateverElseItHolds(t *testing.T) {
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
	opened := podUpdateStart
	base := rig.claimOf(claim.RoleImplementer)
	base.UpdateOpenedAt, base.UpdateReasons = &opened, podUpdateDrift
	opener := newRoundOpener(rig)
	leaves, unset := claimLeaves()
	if len(unset) > 0 {
		t.Fatalf("the fields %v cannot be set, so this test cannot try them", unset)
	}
	n, fields, owedSeen, refusedSeen := 0, 0, 0, 0
	try := func(label string, c supervise.Claim) {
		t.Helper()
		n++
		pod := fmt.Sprintf("case-%04d", n)
		round := supervise.PodRound(pod)
		c.UpdateRound = round
		owed, why := c.OwesUpdateAsk(round)
		want := owed && supervise.SaidReady(c.State)
		if wrong := opener.open([]supervise.Claim{c}, pod); len(wrong) > 0 {
			t.Errorf("%s: %s", label, strings.Join(wrong, "; "))
		}
		got := 0
		for _, ask := range rig.asks() {
			if ask.Round == round {
				if ask.Role != claim.RoleImplementer {
					t.Fatalf("%s: an ask of round %s went to %s, want only the implementer", label, round, ask.Role)
				}
				got++
			}
		}
		if (got == 1) != want || got > 1 {
			t.Errorf("%s: the sweep queued %d asks of the ready implementer, state %s; the rule says owed=%t (%v); want one exactly where the rule owes one and the agent said it is ready",
				label, got, c.State, owed, why)
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
	t.Logf("tried the control and %d fields of a claim at every depth through openPodUpdate: %d owed an ask, %d refused one", fields, owedSeen, refusedSeen)
}
