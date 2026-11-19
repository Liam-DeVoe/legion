package daemon

// The sweep's contract with the one rule of whom an update round asks (supervise's
// Claim.OwesUpdateAsk): from the daemon's tick (supervision.reconcilePodUpdates) to the publish of
// each ask it enqueued (the outbox's updateAsk), the sweep asks a claim, and the ask reaches the
// claim's role, exactly where that rule owes it an ask, it has a session, its agent said it is
// ready and a machine of this daemon holds it, over every state, role and fact of a claim, in groups
// of every size and mix an issue's claims make, and over every field of a claim at every depth,
// alone, in pairs and all at once; and the tick that opens a round posts it to every claim with a
// session. The rig these tests drive the path with is podupdate_sweep_rig_test.go's, and the fields
// of a claim they set, the sets of fields and the groups they set them in are
// podupdate_sweep_fields_test.go's. An ask that waited in the outbox while its claim moved on is
// podupdate_undelivered_ask_test.go's.

import (
	"encoding/json"
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
// failed claim as it drops every claim that is not ready, so the sweep asks no failed claim
// whether the rule refuses it or not. With the rule's failed case taken out, this test shows it in
// the list itself, and the property and field tests show it only where the tick decides a group's
// round before openPodUpdate, since the round waits for no failed claim (waitingClaims), and so
// posts the round to no claim while the rule owes a failed claim an ask (sweepPath.posts). The
// control is the ready claim with none of these, which the sweep owes its ask.
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

// sweepFact is one fact of a claim that sweepFacts sets or leaves out, and the facts a claim holds
// are the bits of the ones it holds, read from its own fields (factsOf).
type sweepFact int

const (
	// factSession is a session (Claim.Session).
	factSession sweepFact = 1 << iota
	// factAsked is an ask of the round that reached its session (Claim.UpdateTaskedAt).
	factAsked
	// factAnswered is its role's answer to the round (Claim.UpdateSignalledAt).
	factAnswered
	// factLeft is a leave of the round's pod (Claim.UpdateLeftRound).
	factLeft
	// factOtherRound is another round than the one swept (Claim.UpdateRound).
	factOtherRound
	// factLocator is a process the claim records (Claim.Locator).
	factLocator
	// sweepFactSets is how many sets of those facts there are, each of which sweepFacts builds a
	// claim of for every state and role.
	sweepFactSets
)

var sweepFactNames = []struct {
	fact sweepFact
	name string
}{
	{factSession, "session"}, {factAsked, "asked"}, {factAnswered, "answered"}, {factLeft, "left"},
	{factOtherRound, "other round"}, {factLocator, "locator"},
}

func (f sweepFact) String() string {
	var names []string
	for _, n := range sweepFactNames {
		if f&n.fact != 0 {
			names = append(names, n.name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, "|")
}

// factsOf is the facts c holds, read from its own fields: what the tests select claims of sweepFacts
// by (sweepKindsOf), and what sweepFacts holds each claim it builds to.
func factsOf(c supervise.Claim) sweepFact {
	var f sweepFact
	if c.Session != "" {
		f |= factSession
	}
	if c.UpdateTaskedAt != nil {
		f |= factAsked
	}
	if c.UpdateSignalledAt != nil {
		f |= factAnswered
	}
	if c.UpdateLeftRound == podUpdateRound {
		f |= factLeft
	}
	if c.UpdateRound != podUpdateRound {
		f |= factOtherRound
	}
	if c.Locator != nil {
		f |= factLocator
	}
	return f
}

// sweepFacts is the claims the sweep tests below hold the sweep to the rule over: every state a
// claim can be in (supervise.States), every role a claim can be on (claim.Roles, and the
// controller's, which is none of them), and each set of the facts a claim holds or not (sweepFact):
// a session, a delivered ask, an answer, a leave of the round's pod, another round and a process
// locator, which is 64 claims for each state and role. Each claim carries token(role) and records
// the round's opening at at, as the one write of a round on a claim does (supervise's
// openPodUpdate), so the sweep's ask has a bound to state (roundBound); the tests give at as the
// rig's held clock, so no round they build has reached its bound. A state or a role added to those
// lists joins the tests without an edit. A claim that does not hold the facts it was built of, read
// from its fields (factsOf), fails the test, naming the facts set on the wrong fields.
func sweepFacts(t *testing.T, at time.Time, token func(claim.Role) claim.Token) []supervise.Claim {
	t.Helper()
	var claims []supervise.Claim
	for _, state := range supervise.States() {
		for _, role := range sweepRoleList {
			for facts := range sweepFactSets {
				opened := at
				c := supervise.Claim{Token: token(role), Role: role, State: state, UpdateRound: podUpdateRound,
					UpdateOpenedAt: &opened}
				if facts&factSession != 0 {
					c.Session = "ses_i"
				}
				if facts&factAsked != 0 {
					c.UpdateTaskedAt = &at
				}
				if facts&factAnswered != 0 {
					c.UpdateSignalledAt = &at
				}
				if facts&factLeft != 0 {
					c.UpdateLeftRound = podUpdateRound
				}
				if facts&factOtherRound != 0 {
					c.UpdateRound = "pod:" + podUpdateReplacementPod
				}
				if facts&factLocator != 0 {
					c.Locator = &runtime.Locator{Claim: c.Token, Incarnation: "inc-1"}
				}
				if got := factsOf(c); got != facts {
					t.Fatalf("sweepFacts built the %s %s claim of the facts %s, and its fields hold %s: the facts %s are set on the wrong fields",
						state, role, facts, got, facts^got)
				}
				claims = append(claims, c)
			}
		}
	}
	return claims
}

// sweepRoleList is every role a claim can be on: claim.Roles, and the controller's.
var sweepRoleList = append(slices.Clone(claim.Roles), claim.RoleController)

// sweepRoleCount is how many roles sweepFacts tries, which is also the most claims a group of one
// issue holds: one of each role, the controller's included.
var sweepRoleCount = len(sweepRoleList)

// sweepRoleIndex is the index of role in sweepRoleList.
func sweepRoleIndex(role claim.Role) int { return slices.Index(sweepRoleList, role) }

// sweepKinds is the facts of each kind of claim (sweepKindsOf): the claim with a session, in the
// round, that no ask of the round reached, that has not answered it and has not left its pod —
// which the rule owes an ask unless it failed — without a process locator and with one; that claim
// refused by one fact alone: an ask of the round reached it, its role answered the round, it left
// the round's pod, or it is in another round; and that claim with no session.
var sweepKinds = []sweepFact{
	factSession, factSession | factLocator,
	factSession | factAsked, factSession | factAnswered, factSession | factLeft, factSession | factOtherRound,
	0,
}

// sweepKindsOf is the claims of sweepFacts, by index, that hold the facts of a kind (sweepKinds),
// read from each claim's own fields: the claims the property test puts in groups of four or more
// and the field test sets fields on, seven for every state and role. A claim refused by two of the
// facts at once is tried alone and in groups of two and three, as every claim of sweepFacts is, and
// not in the larger groups or with a field set: the seven kinds a state and role give are what a
// statement that reads one fact beside the field or the group can tell apart, at a ninth of the
// claims.
func sweepKindsOf(t *testing.T, facts []supervise.Claim) []int {
	t.Helper()
	var kinds []int
	for i, c := range facts {
		if slices.Contains(sweepKinds, factsOf(c)) {
			kinds = append(kinds, i)
		}
	}
	if want := len(facts) / int(sweepFactSets) * len(sweepKinds); len(kinds) != want {
		t.Fatalf("sweepFacts holds %d claims of the kinds %v, want %d: one of each kind for every state and role", len(kinds), sweepKinds, want)
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
// ask (sweepOwed), one the sweep asks (sweepRig.asks), or either of the first two.
type sweepKind int

const (
	sweepRefused sweepKind = iota
	sweepOwedOne
	sweepAsked
	sweepEither
)

func (k sweepKind) String() string { return [...]string{"refused", "owed", "asked", "either"}[k] }

// sweepPools is a set of claims — sweepFacts, or sweepFacts with one more field set — by kind and
// by role, from which groups are built. The roles of a group's other claims, and the claims of
// each role and kind, are each taken in turn, so over a test every claim of a pool stands in
// groups alike, and no claim's place in the set decides what stands beside it.
type sweepPools struct {
	claims []supervise.Claim
	pool   [4][][]int // by kind, then role: indexes into claims
	taken  [4][]int   // by kind, then role: how many claims were taken
	roles  int        // how many roles were taken
}

// newSweepPools sorts claims into their pools by kind and role. asks is whether the sweep asks a
// claim (sweepRig.asks); a claim it asks is in the pool of owed claims too.
func newSweepPools(claims []supervise.Claim, asks func(supervise.Claim) bool) *sweepPools {
	p := &sweepPools{claims: claims}
	for k := range p.pool {
		p.pool[k], p.taken[k] = make([][]int, sweepRoleCount), make([]int, sweepRoleCount)
	}
	for i, c := range claims {
		r, k := sweepRoleIndex(c.Role), sweepRefused
		if sweepOwed(c) {
			k = sweepOwedOne
			if asks(c) {
				p.pool[sweepAsked][r] = append(p.pool[sweepAsked][r], i)
			}
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
	roles := append(held[:0], sweepRoleIndex(p.claims[i].Role))
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
// askedOnly is whether the claims put in it are the kinds the sweep asks alone: a group of four or
// more beside no claim owed an ask holds a claim the sweep asks that way.
type sweepShape struct {
	name      string
	others    []sweepKind
	place     int
	askedOnly bool
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
// claims of which every count from none to all is owed and the rest refused, each group holding a
// claim the sweep asks: the first owed claim beside the one tried is one, and beside no owed claim
// the claim tried is one. So every size of group an issue's claims make is tried, every count of
// owed claims at each size, and in every group of four or more the sweep asks a claim, so a
// statement of its own there that reads the claims beside a claim changes what an ask reaches.
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
		shapes = append(shapes, sweepShape{name: fmt.Sprintf("in a group of %d, a claim the sweep asks beside %d claims refused one", size, size-1),
			others: slices.Repeat([]sweepKind{sweepRefused}, size-1), place: -1, askedOnly: true})
		for owed := 1; owed < size; owed++ {
			others := append(append([]sweepKind{sweepAsked}, slices.Repeat([]sweepKind{sweepOwedOne}, owed-1)...),
				slices.Repeat([]sweepKind{sweepRefused}, size-1-owed)...)
			shapes = append(shapes, sweepShape{name: fmt.Sprintf("in a group of %d, beside %d claims owed an ask, one the sweep asks among them, and %d refused one", size, owed, size-1-owed),
				others: others, place: -1})
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
// refused, with a claim the sweep asks in every such group. The claims beside it are taken in turn
// (sweepPools), and each of them holds one more field of the claim, a field at any depth taken in
// turn that keeps it of its kind (claimLeaves). Every claim of every group is held to the rule, the
// claims beside it included, so a group that holds two or more claims the sweep owes an ask fails
// the test where any one of them is not asked or not reached.
//
// With the test above it holds the one rule of whom a round asks, which is neither a failed claim
// nor one that has left the round's pod: that test fails when the rule loses a case, and this one
// fails when the sweep refuses or owes a claim by a statement of its own that the rule does not
// make. That statement can stand anywhere from the tick to the publish (sweepPath), and it can read
// the claim or the claims beside it: a filter of its own on one state, on one role or on a locator
// present or absent, its own failed filter put back while the rule's failed case is taken out, its
// left-pod check taken out, a filter on a pending task that holds only in a group of one size or
// only beside a given count of owed claims, a claim refused while another claim of its group is
// owed an ask too, only the first owed claim of a group asked, or a claim refused in askRound
// beside a claim of one state and role in a group of one size. The test after it holds the same
// over every field of a claim. None of them fails for a statement of the sweep's own that agrees
// with the rule in every group, such as that failed filter put back with the rule whole: it changes
// no claim's answer, and it shows here the first time the rule changes and the two disagree.
//
// The negative control is the sweep's own failed filter put back with the rule's failed case taken
// out: this test then fails on each failed claim with a session that the rule owes an ask, and the
// test above passes. The test also fails when it saw no claim owed an ask or no claim refused one,
// no group in which the sweep asks two claims, a size of group in which it asks no claim, or a
// group of four or more in which it asks none, since such a set shows nothing.
func TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoes(t *testing.T) {
	r := newSweepRig(t)
	at := podUpdateStart
	facts := sweepFacts(t, at, r.token)
	leaves, unset := claimLeaves(sweepSliceLength(t))
	if len(unset) > 0 || len(leaves) == 0 {
		t.Fatalf("the claim holds fields this test cannot set (%v), or none it can (%d), so it cannot vary the claims beside each claim", unset, len(leaves))
	}
	pools := newSweepPools(facts, r.asks)
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
	checked, owed, refused, groups, twoAsked, unasked := 0, 0, 0, 0, 0, 0
	askedBySize := make([]int, sweepRoleCount+1)
	buf := make([]supervise.Claim, 0, sweepRoleCount)
	every, kinds := make([]int, len(facts)), sweepKindsOf(t, facts)
	for i := range every {
		every[i] = i
	}
	askedKinds := slices.DeleteFunc(slices.Clone(kinds), func(i int) bool { return !r.asks(facts[i]) })
	for _, s := range shapes {
		wrong, seen, example := 0, 0, ""
		tried := every
		if len(s.others) >= 3 {
			tried = kinds
		}
		if s.askedOnly {
			tried = askedKinds
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
			if asked == 0 && len(group) >= 4 {
				unasked++
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
	if owed == 0 || refused == 0 || twoAsked == 0 || len(noAsk) > 0 || unasked > 0 {
		t.Fatalf("checked %d claims of %d groups, %d owed an ask and %d refused one, %d groups in which the sweep asks two claims or more, groups of the sizes %v in which it asks none, and %d groups of four or more in which it asks none: a set with no owed or no refused claim, no group of two asked claims, a size of group with no ask, or a larger group with no ask shows nothing",
			checked, groups, owed, refused, twoAsked, noAsk, unasked)
	}
	t.Logf("checked %d claims of %d groups in %d shapes, of every size from 1 to %d: %d owed an ask, %d refused one; in %d groups the sweep asks two claims or more, and in every group of four or more it asks one",
		checked, groups, len(shapes), sweepRoleCount, owed, refused, twoAsked)
}

// sweepFieldsEveryClaimSets is the claim's fields that every claim of sweepFacts sets, so the test
// below can set them on no claim, each with why the sweep's tests still hold the sweep over them.
var sweepFieldsEveryClaimSets = map[string]string{
	"Token":          "a claim is known by it; each role's claim has its own, so the test above varies it with the role",
	"Role":           "the test above tries every role",
	"State":          "the test above tries every state",
	"UpdateRound":    "the test above tries the round and another round, and the opening rows the claim in no round",
	"UpdateOpenedAt": "every claim of a round records the round's opening, which the ask's bound is read from",
}

// sweepFieldShapes is each group the field test puts a claim to the sweep in with one field set:
// alone, beside one claim of another role, and beside one claim of every other role, which is an
// issue pod with all its roles, each claim beside it of either kind.
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
// turn (sweepPools). A field the machine's view reads from the machine's own state rather than the
// claim's record — a compaction in flight, a process the runtime reported gone — is set by putting
// the claim's machine in that state (sweepMachineStates), on each claim whose machine takes it.
//
// Then fields are set together, in sets that put every pair of fields together in one set
// (sweepFieldBlocks), and in one set every field at once, on each kind of claim of every state and
// role, each claim alone and in a group of one claim of every role, each of a state and a kind of
// its own, that holds the same set (sweepKindGroups). So a filter of the sweep's own on a field
// the test above does not vary, on two of them together on any kind of claim, on two of them
// together beside a claim of another state, or on any number of them set together — a pending
// task, a task's text or the moment a turn confirmed it, the round's told mark, the claim's deaths
// or prompt failures, the claim's issue, a claim with more than one or two turn outcomes, a claim
// being compacted or whose process is gone, a pending task on a claim whose round was told — fails
// here, wherever it stands from the tick to the publish, on the claims with a session the rule
// owes an ask: a sweep that skipped every claim holding a task would leave a working role unasked
// until the round's bound. Last, each field is set on each claim of every state and role with a
// session at the tick that opens the round, alone and in groups of every role: the tick reads the
// group live there and must post the round to each of them (sweepRig.open). A field added to the
// claim joins the test without an edit, and a field of a type this test cannot set fails it.
//
// The test leaves out the fields every claim of sweepFacts sets (sweepFieldsEveryClaimSets: the
// token, the role, the state, the round and the round's opening), each named with why, and it
// fails when one of them is no field of the claim or is empty on some claim, and when a field it
// does not name is tried on no claim, or held by no machine of a claim it was set on.
//
// Each field is set to one value. So this test does not fail for a filter of the sweep's own on one
// value of a field other than the one it sets: a filter on a claim an operator suspended
// (supervise.SuspendedByOperator) passes it, since SuspendedBy is set to "x" here. Nor does it fail
// for a filter that agrees with the rule in every group, as the test above says.
//
// The negative control is a filter of the sweep's own on a pending task (Claim.Pending), anywhere
// from the tick to the publish: this test then fails with each field of Pending set, and the test
// above fails too, on the groups beside whose claims a pending task is set. A filter on a pending
// task together with the round's told mark fails the sets of fields alone. The test also fails
// when it tried no field or no set of fields, left a pair of fields set together on no kind of
// claim that leaves both empty, or saw no owed or no refused claim, since such a set shows nothing.
func TestPodUpdateSweepOwesAnAskExactlyWhereTheRuleDoesWhateverElseTheClaimHolds(t *testing.T) {
	r := newSweepRig(t)
	at := podUpdateStart
	facts := sweepFacts(t, at, r.token)
	kinds := sweepKindsOf(t, facts)
	leaves, unset := claimLeaves(sweepSliceLength(t))
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
	base := facts[slices.IndexFunc(facts, func(c supervise.Claim) bool {
		return c.State == supervise.StateReady && c.Role == claim.RoleImplementer && factsOf(c) == factSession
	})]
	if wrong := r.path.machineStatesRead(base, leaves, at); len(wrong) > 0 {
		t.Fatalf("the machine's view reads fields this test does not set as a machine holds them: %s", strings.Join(wrong, "; "))
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
		pools := newSweepPools(others, r.asks)
		tried, held, wrong, example := 0, 0, 0, ""
		for n, i := range kinds {
			c := facts[i]
			if !leaf.empty(c) {
				continue
			}
			tried++
			for _, s := range sweepFieldShapes {
				group := pools.group(buf, i, s.at(n), s.others, nil)
				asked, misses := r.hold(group)
				if !leaf.empty(group[s.at(n)]) {
					held++
				}
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
		case tried > 0 && held == 0:
			t.Errorf("no machine of the %d claims %s was set on held it, so this test tried it on no claim", tried, leaf.name)
		case tried > 0:
			fields++
		}
		if wrong > 0 {
			t.Errorf("with %s set, the sweep disagrees with the rule on %d claims of %d groups, for example %s; want the sweep to ask a claim, and the ask to reach it, exactly where the rule owes it an ask, it has a session, its agent said it is ready and a machine holds it",
				leaf.name, wrong, tried*len(sweepFieldShapes), example)
		}
	}

	blocks := sweepFieldBlocks(sweepFlips(facts, kinds, leaves, at))
	if missing := sweepUncoveredPairs(facts, kinds, leaves, blocks); len(missing) > 0 {
		t.Fatalf("%d pairs of fields that a kind of claim leaves empty are set together on it in no set of fields, for example %s", len(missing), missing[0])
	}
	groups := sweepKindGroups(facts, kinds, sweepKinds)
	sets, setsWrong := 0, 0
	for _, block := range blocks {
		with := func(c supervise.Claim) supervise.Claim {
			for _, l := range block {
				if leaves[l].empty(c) {
					leaves[l].set(&c, at)
				}
			}
			return c
		}
		wrong, seen, example := 0, 0, ""
		try := func(shape string, group []supervise.Claim) {
			asked, misses := r.hold(group)
			checked += len(group)
			seen += len(group)
			owed += asked
			refused += len(group) - asked
			wrong += len(misses)
			if example == "" && len(misses) > 0 {
				example = shape + ", " + misses[0]
			}
		}
		for _, i := range kinds {
			try("alone", append(buf[:0], with(facts[i])))
		}
		for _, g := range groups {
			group := buf[:0]
			for _, i := range g {
				group = append(group, with(facts[i]))
			}
			try("in a group of every role", group)
		}
		sets++
		if wrong > 0 {
			if setsWrong++; setsWrong <= 10 {
				t.Errorf("with %s set together, the sweep disagrees with the rule on %d of %d claims, for example %s",
					sweepLeafNames(leaves, block), wrong, seen, example)
			}
		}
	}
	if setsWrong > 10 {
		t.Errorf("and with %d more sets of fields", setsWrong-10)
	}

	opening := sweepKindsOfFacts(facts, factSession, factSession|factLocator)
	openingGroups := sweepKindGroups(facts, opening, []sweepFact{factSession, factSession | factLocator})
	opened := 0
	openAt := func(label string, set func(*supervise.Claim)) {
		build := func(i int) supervise.Claim {
			c := facts[i]
			c.UpdateRound, c.UpdateOpenedAt = "", nil
			set(&c)
			return c
		}
		wrong, seen, example := 0, 0, ""
		try := func(shape string, group []supervise.Claim) {
			asked, misses := r.open(group)
			checked += len(group)
			seen += len(group)
			refused += len(group) - asked
			wrong += len(misses)
			if example == "" && len(misses) > 0 {
				example = shape + ", " + misses[0]
			}
		}
		for _, i := range opening {
			try("alone", append(buf[:0], build(i)))
		}
		for _, g := range openingGroups {
			group := buf[:0]
			for _, i := range g {
				group = append(group, build(i))
			}
			try("in a group of every role", group)
		}
		opened++
		if wrong > 0 {
			t.Errorf("at the tick that opens the round, %s, the sweep disagrees with the rule on %d of %d claims, for example %s; want the round posted to each claim with a session that has not retired, and no ask, since no claim records the round yet",
				label, wrong, seen, example)
		}
	}
	openAt("with no field set", func(*supervise.Claim) {})
	for _, leaf := range leaves {
		if _, everyClaimSets := sweepFieldsEveryClaimSets[leaf.top]; everyClaimSets {
			continue
		}
		openAt("with "+leaf.name+" set", func(c *supervise.Claim) {
			if leaf.empty(*c) {
				leaf.set(c, at)
			}
		})
	}
	if fields == 0 || sets == 0 || opened < 2 || owed == 0 || refused == 0 {
		t.Fatalf("checked %d claims over %d fields, %d sets of fields and %d openings, %d owed and %d refused: a set with no field, no set of fields or no opening tried, or no owed or no refused claim, shows nothing",
			checked, fields, sets, opened, owed, refused)
	}
	t.Logf("checked %d claims over %d fields of a claim at every depth, %d sets of them that put every pair together and all of them at once, and the round's opening with each field: %d owed an ask, %d refused one",
		checked, fields, sets, owed, refused)
}

// The sweep's ask reaches a ready claim the rule owes an ask whatever else the claim holds, through
// the rig's real outbox: for the rig's ready implementer in the round, the control with nothing
// more set and then each exported field of the claim at every depth that the claim leaves empty,
// set in turn (claimLeaves), the daemon's tick (sweepPath) queues one row of the round's ask, and
// the role-message lane's run of that row delivers one ask to the implementer's topic, exactly
// where the claim's own record owes it one (supervise's Claim.OwesUpdateAsk, which owes a failed
// claim and one that has left the round's pod nothing) and its agent said it is ready; and the
// tick posts the round to the claim once. A field the machine's view reads from the machine's own
// state is set by putting the machine in that state (sweepMachineStates). Through every case the
// round's ask to another role, the tester's, waits in the outbox as a row the lane has not run yet,
// so the tick asks each case beside an ask of the round still queued: askRound skips a claim whose
// own ask of the round is queued (the store's QueuedUpdateAsks), and an ask queued for one role
// holds back no other role's. Each case's rows are run and finished before the next case, so no
// row of the implementer's that an earlier case queued stands in its way. The acceptance test of
// this change wrote it as the red of a filter placed in askRound: a sweep that skipped every claim
// holding a pending task there passed every other test of the sweep, and failed this one. The same
// filter placed anywhere from the tick to the outbox's publish fails it the same way, and so does
// a sweep that skips every claim while any ask of the round is queued.
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
	leaves, unset := claimLeaves(sweepSliceLength(t))
	if len(unset) > 0 {
		t.Fatalf("the fields %v cannot be set, so this test cannot try them", unset)
	}
	fields, owedSeen, refusedSeen := 0, 0, 0
	try := func(label string, c supervise.Claim) {
		t.Helper()
		heard := rig.deliveredTo(claim.RoleImplementer)
		other := record.UpdateAsk{Role: claim.RoleTester, Round: podUpdateRound, Pod: podUpdatePod,
			Text: supervise.UpdateTask(podUpdateRound, podUpdateDrift, podUpdateStart.Add(podUpdateGrace))}
		if err := rig.s.enqueue(rig.ctx, podUpdateIssue, other); err != nil {
			t.Fatalf("%s: queue the tester's ask of the round: %v", label, err)
		}
		group := []supervise.Claim{c}
		path.hold(group)
		c = group[0]
		owed, why := c.OwesUpdateAsk(podUpdateRound)
		want := owed && supervise.SaidReady(c.State)
		path.sweep(group, podUpdatePod, podUpdateRound)
		if wrong := path.posts(group, podUpdateRound, false); len(wrong) > 0 {
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
