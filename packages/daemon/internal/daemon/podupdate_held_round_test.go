package daemon

// Who is told of an update round a hold keeps past its bound (tellUpdateStalled): the architect
// that owns the held claim for a hold an architect placed, and the controller for every other
// hold, and for an architect's hold no architect can be told of. Each test drives the rig's real
// sweep to the round's bound, then runs the rows it wrote through the outbox's own executors, so
// what each test reads is the topic each notice was published on.

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// heldRoundRig is a round over the rig's issue, a tree root, whose implementer suspendedBy holds
// suspended, beside a tester that answers and the issue's own architect in state architect. The
// round is carried to its bound and swept there once.
func heldRoundRig(t *testing.T, suspendedBy supervise.SuspendSource, architect supervise.ClaimState) *podUpdateRig {
	t.Helper()
	rig := newPodUpdateRig(t,
		podUpdateRole{role: claim.RoleArchitect, state: architect, session: true, inPod: architect != supervise.StateFailed,
			contract: api.DaemonAPIVersion},
		podUpdateRole{role: claim.RoleImplementer, state: supervise.StateSuspended, session: true,
			contract: api.DaemonAPIVersion, suspendedBy: suspendedBy},
		readyWorker(claim.RoleTester),
	)
	rig.drifts(podUpdateDrift)
	rig.tick()
	rig.awaitRound(claim.RoleImplementer, podUpdateRound)
	rig.awaitRound(claim.RoleTester, podUpdateRound)
	if architect != supervise.StateFailed {
		rig.awaitRound(claim.RoleArchitect, podUpdateRound)
	}
	rig.signal(claim.RoleTester, "a1b2c3d")
	rig.clockAt(podUpdateBound)
	rig.tick()
	if calls := rig.replacements(); len(calls) != 0 {
		t.Fatalf("at the bound %s ReplacePod was called %d times under a %s suspension", podUpdateBound, len(calls), suspendedBy)
	}
	wantNoRefusedNotice(t, rig)
	return rig
}

// stallDelivery runs every notice and controller-notice row the sweep wrote through the outbox's
// own executors, each row leased as the outbox leases it: a notice is routed by the owner walk
// over the rig's machines (deliverNotice), and a controller notice is published to the project's
// controller topic (controllerNotice). It answers the update-stalled notices published, by topic.
func (r *podUpdateRig) stallDelivery() map[string][]record.Notice {
	r.t.Helper()
	publisher := &holderPublisher{}
	runner := &outbox{log: quietLogger(), pool: r.pool, records: r.records, notices: publisher, supervisor: r.sup,
		project: podUpdateProject, dispatchProject: "LEGION", now: time.Now}
	for _, kind := range []record.OutboxKind{record.OutboxKindNotice, record.OutboxKindControllerNotice} {
		for _, row := range r.rows(kind) {
			row.LeaseToken = leaseOutboxRow(r.t, r.pool, row.ID)
			if err := runner.execute(r.ctx, row); err != nil {
				r.t.Fatalf("run the %s row %d: %v", kind, row.ID, err)
			}
		}
	}
	_, delivered := publisher.snapshot()
	byTopic := map[string][]record.Notice{}
	for _, published := range delivered {
		if notice, ok := published.payload.(record.Notice); ok && notice.Kind == record.UpdateStalledNotice {
			byTopic[published.topic] = append(byTopic[published.topic], notice)
		}
	}
	return byTopic
}

// toldOn holds told to one update-stalled notice, published on topic and on no other, and answers
// it.
func toldOn(t *testing.T, told map[string][]record.Notice, topic string) record.Notice {
	t.Helper()
	if len(told) != 1 || len(told[topic]) != 1 {
		t.Fatalf("the update-stalled notices were published on %v, want one, on %s alone", slices.Sorted(maps.Keys(told)), topic)
	}
	return told[topic][0]
}

// A round held past its bound by an architect's suspension alone is told to the architect that
// owns the held claim, on that architect's role topic, and not to the controller: the hold is that
// architect's to resume, and the controller has nothing of its own to do about it. The notice
// carries the round, its pod and every claim the round asks, and names the held claim and its
// holder. It is told once: a sweep an hour later tells nobody again.
//
// The negative control is the routing undone, every hold told to the controller, which writes a
// controller notice and no architect's.
func TestPodUpdateTellsAnArchitectsHoldToTheArchitectThatOwnsTheHeldClaim(t *testing.T) {
	rig := heldRoundRig(t, supervise.SuspendedByArchitect, supervise.StateReady)

	if notices := rig.notices(); len(notices) != 0 {
		t.Fatalf("the controller was told %+v, want nothing: the round's one hold is an architect's, and that architect can be told", notices)
	}
	notice := toldOn(t, rig.stallDelivery(), rig.roleTopic(claim.RoleArchitect))
	if notice.Update == nil || notice.Update.Round != podUpdateRound || notice.Update.Pod != podUpdatePod {
		t.Fatalf("the architect's notice carries %+v, want round %q of pod %q", notice.Update, podUpdateRound, podUpdatePod)
	}
	for _, want := range []string{
		"An architect suspended implementer (suspended). An architect that owns the issue, or the controller, can resume it.",
		"The daemon does not replace the pod until a resume lifts each of these suspensions.",
	} {
		if !strings.Contains(notice.Reason, want) {
			t.Errorf("the architect's notice says %q, want it to say %q", notice.Reason, want)
		}
	}
	wantWaiting(t, record.ControllerNotice(notice),
		map[claim.Role]bool{claim.RoleArchitect: false, claim.RoleImplementer: false, claim.RoleTester: true})

	rig.clockAt(podUpdateBound.Add(time.Hour))
	rig.tick()
	if rows, notices := rig.rows(record.OutboxKindNotice), rig.notices(); len(rows) != 1 || len(notices) != 0 {
		t.Fatalf("an hour past the bound the round has %d architect notices and %d controller notices, want the one architect notice it was told at the bound", len(rows), len(notices))
	}
}

// A round held past its bound by the operator's or the controller's suspension is told to the
// controller alone, even where the issue's architect could be told: the operator's hold is the
// controller's to raise with the operator, and its own is the controller's to resume.
//
// The negative control is every hold told to the owning architect, whatever its holder, which
// writes an architect's notice and no controller notice.
func TestPodUpdateTellsTheControllerAloneOfItsOwnOrTheOperatorsHold(t *testing.T) {
	for _, tc := range []struct {
		by    supervise.SuspendSource
		words string
	}{
		{by: supervise.SuspendedByOperator, words: "An operator suspended implementer (suspended). Only the operator can resume it."},
		{by: supervise.SuspendedByController, words: "The controller suspended implementer (suspended). The controller can resume it."},
	} {
		t.Run(string(tc.by), func(t *testing.T) {
			rig := heldRoundRig(t, tc.by, supervise.StateReady)

			if rows := rig.rows(record.OutboxKindNotice); len(rows) != 0 {
				t.Fatalf("the sweep wrote %d architect notices, want none: the round's one hold is the %s's", len(rows), tc.by)
			}
			notice := toldOn(t, rig.stallDelivery(), notify.ControllerTopic(podUpdateProject))
			if !strings.Contains(notice.Reason, tc.words) {
				t.Errorf("the controller's notice says %q, want it to say %q", notice.Reason, tc.words)
			}
			wantWaiting(t, record.ControllerNotice(notice),
				map[claim.Role]bool{claim.RoleArchitect: false, claim.RoleImplementer: false, claim.RoleTester: true})
		})
	}
}

// A round held past its bound by an architect's suspension, where the architect the owner walk
// names has failed, is told to the controller: no session holds that architect's role to read
// the notice, so it would finish undelivered (noticeAnswered's architectEnded), and the
// controller can resume an architect's suspension too. The notice names the architect's hold.
//
// The negative control is an architect's hold told to the walk's architect whatever its claim's
// state, which writes an architect's notice and no controller notice.
func TestPodUpdateTellsTheControllerOfAnArchitectsHoldWhenTheOwningArchitectFailed(t *testing.T) {
	rig := heldRoundRig(t, supervise.SuspendedByArchitect, supervise.StateFailed)

	if rows := rig.rows(record.OutboxKindNotice); len(rows) != 0 {
		t.Fatalf("the sweep wrote %d architect notices, want none: the architect that owns the held claim has failed", len(rows))
	}
	notice := toldOn(t, rig.stallDelivery(), notify.ControllerTopic(podUpdateProject))
	if want := "An architect suspended implementer (suspended). An architect that owns the issue, or the controller, can resume it."; !strings.Contains(notice.Reason, want) {
		t.Errorf("the controller's notice says %q, want it to say %q", notice.Reason, want)
	}
	wantWaiting(t, record.ControllerNotice(notice), map[claim.Role]bool{claim.RoleImplementer: false, claim.RoleTester: true})
}
