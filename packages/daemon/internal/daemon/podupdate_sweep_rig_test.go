package daemon

// The rig the sweep's contract tests (podupdate_sweep_contract_test.go) drive the sweep's whole
// path with, from the daemon's tick to the publish of each ask, and the substitutes its machines
// are built over.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

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
// the claim's token. A claim of a token no machine of the rig holds stays unheld, and holds none of
// a machine's own state (sweepMachineStates). A claim that sets a field of that state is put to the
// path as its machine holds it once the machine was driven into that state: group's claim becomes
// the machine's view of it, so where the machine does not take the state the claim is tried
// without the field, as a running daemon would hold it.
func (p *sweepPath) hold(group []supervise.Claim) {
	p.rig.t.Helper()
	p.rig.sup.mu.Lock()
	defer p.rig.sup.mu.Unlock()
	for i := range group {
		c := &group[i]
		member, ok := p.rig.sup.machines[c.Token]
		var states []sweepMachineState
		for _, s := range sweepMachineStates {
			if !s.holds(*c) {
				continue
			}
			if !ok {
				s.clear(c)
				continue
			}
			if s.prepare != nil {
				s.prepare(c)
			}
			states = append(states, s)
		}
		if !ok {
			continue
		}
		m, err := supervise.NewMachine(p.rig.sup.ctx, p.deps, *c)
		if err != nil {
			p.rig.t.Fatalf("build the machine of %s: %v", describeSwept(*c), err)
		}
		for _, s := range states {
			s.drive(p, m, *c)
		}
		if len(states) > 0 {
			*c = m.View().Claim
		}
		member.machine = m
	}
}

// sweepMachineState is one field of the claim that the machine's view puts over the claim's record
// from the machine's own memory (supervise's Machine.View), with how a test puts a machine in the
// state it reads: set on the claim alone, the field would read false in the live read whatever the
// claim says.
type sweepMachineState struct {
	field string
	holds func(supervise.Claim) bool
	clear func(*supervise.Claim)
	// prepare makes the claim one a machine can hold the state over, before its machine is built.
	prepare func(*supervise.Claim)
	// drive puts the machine in the state through the machine's own request or read. Where the
	// machine does not take it, its view reads the field false.
	drive func(p *sweepPath, m *supervise.Machine, c supervise.Claim)
}

// sweepMachineStates is every field of the claim the machine's view reads from the machine's own
// state (supervise's Machine.current): a compaction in flight (Claim.Compacting), which the
// machine's own request takes the claim for as the first step of Machine.Compact, so the claim is
// working until the compaction ends, and none ends here; and the runtime's last report of the
// process the claim records (Claim.ProcessGone), which a read of that process the runtime answers
// gone records (Machine.ReadProcess), on a claim given a process in the pod where it records none.
// A field the view reads from the machine that this list does not name fails the field test
// (sweepPath.machineStatesRead).
var sweepMachineStates = []sweepMachineState{
	{
		field: "Compacting",
		holds: func(c supervise.Claim) bool { return c.Compacting },
		clear: func(c *supervise.Claim) { c.Compacting = false },
		drive: func(p *sweepPath, m *supervise.Machine, c supervise.Claim) {
			// A claim in a state that takes no compaction is refused or ignored, and its view
			// reads no compaction.
			_ = m.Handle(p.rig.ctx, supervise.RequestCompact{Claim: c.Token})
		},
	},
	{
		field: "ProcessGone",
		holds: func(c supervise.Claim) bool { return c.ProcessGone },
		clear: func(c *supervise.Claim) { c.ProcessGone = false },
		prepare: func(c *supervise.Claim) {
			if c.Locator == nil {
				c.Locator = podUpdateLocator(c.Token, podUpdateIssue, podUpdatePod)
			}
		},
		drive: func(p *sweepPath, m *supervise.Machine, c supervise.Claim) {
			p.rig.rt.ScriptProbe(fake.ProbeResult{Kind: runtime.Gone})
			if read := m.ReadProcess(p.rig.ctx); read != supervise.ProcessGone {
				p.rig.t.Fatalf("the read of the process of %s answered %q, want %q: the runtime was scripted to answer it gone", describeSwept(c), read, supervise.ProcessGone)
			}
		},
	},
}

// machineStatesRead reports each field the machine's view puts over the claim's record from the
// machine's own memory that sweepMachineStates does not drive, and each one it drives that the
// machine of base does not take: base with each leaf it leaves empty set, put into a machine of
// its own (hold), is read back through the machine's view.
func (p *sweepPath) machineStatesRead(base supervise.Claim, leaves []claimLeaf, at time.Time) (wrong []string) {
	p.rig.t.Helper()
	for _, leaf := range leaves {
		if !leaf.empty(base) {
			continue
		}
		c := base
		leaf.set(&c, at)
		p.hold([]supervise.Claim{c})
		m, ok := p.rig.sup.Machine(c.Token)
		if !ok {
			p.rig.t.Fatalf("no machine holds %s", describeSwept(c))
		}
		if !leaf.empty(m.View().Claim) {
			continue
		}
		if slices.ContainsFunc(sweepMachineStates, func(s sweepMachineState) bool { return s.field == leaf.top }) {
			wrong = append(wrong, fmt.Sprintf("the machine of %s did not take the state its view reads %s from", describeSwept(c), leaf.name))
			continue
		}
		wrong = append(wrong, fmt.Sprintf("the machine's view reads %s empty on a claim whose record sets it, so it is the machine's own state: name it in sweepMachineStates with how a machine is put in that state", leaf.name))
	}
	return wrong
}

// sweep runs the daemon's tick over group, each claim of it read by the tick in the issue and in a
// process in pod where a machine holds it, and, where round is not empty, in round with the rig's
// drift as the reasons it records. An empty round is the tick that opens the round: no claim
// records a round yet, so the tick reads the group live and posts the round's opening.
func (p *sweepPath) sweep(group []supervise.Claim, pod, round string) {
	p.read = append(p.read[:0], group...)
	for i := range p.read {
		c := &p.read[i]
		c.Issue, c.UpdateRound, c.UpdateReasons, c.Locator = podUpdateIssue, round, "", nil
		if round != "" {
			c.UpdateReasons = podUpdateDrift
		}
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
// (reconcilePodUpdates, roundGroup). At a tick that opens the round (opening) it always does. At a
// tick that finds the round open it may post no opening at all instead, where it decided the round
// before openPodUpdate because no claim of the group waits for it (completePodUpdate), which it may
// do only where the rule owes no claim of the group with a session an ask; a round so decided may
// be replaced at that sweep, which posts the round's end to its members (endPodUpdates). So it never
// posts the opening to some of those claims and not to others. A claim of a token no machine holds
// has no inbox to read.
func (p *sweepPath) posts(group []supervise.Claim, round string, opening bool) (wrong []string) {
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
		want := (posted || opening) && i >= 0 && group[i].Session != "" && group[i].State != supervise.StateRetired
		if n := got[token]; (n == 1) != want || n > 1 {
			where := "where it posts to any claim"
			if opening {
				where = "at the round's opening"
			}
			wrong = append(wrong, fmt.Sprintf("the sweep posted %s the round %d times, want it posted once exactly where the group holds its claim with a session that has not retired, %s (in the group: %t)",
				token, n, where, i >= 0))
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
// the outbox's own rows are the ask-path test's (podupdate_sweep_contract_test.go). Each recorded
// ask is then run as the outbox runs its row (outbox.execute), through the rig's supervisor, so it
// is published under the machine of the claim it names; what reaches each role's topic is read from
// the listener (heard).
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
// daemon's tick to the publish (sweepPath), at a tick that finds the round open over the group's
// pod, and reports each claim of it the sweep treats otherwise than the rule. The sweep's list
// (unaskedClaims) must hold a claim exactly where it has a session and its own record owes it an
// ask (supervise's Claim.OwesUpdateAsk); the sweep must enqueue one ask of the round for a claim,
// and that ask's row must reach the claim's role topic once, exactly where the list holds the
// claim, its agent said it is ready (supervise.SaidReady) and a machine of this daemon holds it
// (asks); every row must run without an error; and the tick must post the round to each claim with
// a session that has not retired (sweepPath.posts). Each claim is read as its machine holds it
// (sweepPath.hold). asked is how many claims of the group are owed an ask that way.
func (r *sweepRig) hold(group []supervise.Claim) (asked int, wrong []string) {
	return r.drive(group, podUpdateRound)
}

// open is hold at the tick that opens the round: no claim of group records a round yet, so the tick
// reads the group live and posts the round's opening to each claim of it with a session that has
// not retired, and the rule owes no claim an ask until its own record holds the round.
func (r *sweepRig) open(group []supervise.Claim) (asked int, wrong []string) {
	return r.drive(group, "")
}

// asks is whether the sweep asks c: the rule owes c an ask of the round and c has a session
// (sweepOwed), its agent said it is ready (supervise.SaidReady), and a machine of this daemon holds
// it.
func (r *sweepRig) asks(c supervise.Claim) bool {
	_, held := r.rig.sup.Machine(c.Token)
	return sweepOwed(c) && supervise.SaidReady(c.State) && held
}

// drive is hold and open: the tick reads group in round, or at the round's opening where round is
// empty.
func (r *sweepRig) drive(group []supervise.Claim, round string) (asked int, wrong []string) {
	r.asked = r.asked[:0]
	r.path.hold(group)
	listed := unaskedClaims(group, podUpdateRound)
	r.path.sweep(group, podUpdatePod, round)
	wrong = r.path.posts(group, podUpdateRound, round == "")
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
		wantAsked := r.asks(c)
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
