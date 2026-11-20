package daemon

import (
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/pause"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/texts"
)

// R5 of dispatch://INFRA-3686: "A project's resume still launches each reader it held a `low`
// text for, as dispatch://INFRA-3759's R4 says", and the Design: "A `low` text calls no
// `wakeForDelivery`, apart from a row that a project's pause held, whose resume launches its
// reader as R5 says". A `low` notice the pause held for an architect that an idle release took
// before the pause launches that architect at the resume
// (TestALowNoticeAPauseHeldLaunchesItsReaderAtTheResume). A `low` role message the pause held for
// a role that an idle release took before the pause is the same case for a role message: the
// pause held a `low` text for that reader, so the resume launches it.
//
// The message is a CI settlement of an issue in testing to its implementer, `low` to it, as
// tellChecks queues one for every settlement while the project is paused. At this head the row
// finishes undelivered once the resume lets it run (outbox.roleMessage: "a low message is not worth
// a wake"), and the implementer is never launched.
func TestALowRoleMessageAPauseHeldForARoleAReleaseTookLaunchesThatRoleAtTheResume(t *testing.T) {
	r := newPauseDaemonRig(t, nil)
	issue := r.issue(1)
	worker := r.token(issue, claim.RoleImplementer)
	r.seed(r.root(issue, phase.Testing), r.running(issue, claim.RoleTester, supervise.StateIdle),
		r.released(issue, claim.RoleImplementer))
	if _, err := r.pool.Exec(r.ctx, `insert into pod_releases (project, issue, generation, roles, at)
		values ($1, $2, 1, 'implementer', $3)`, r.project, issue, r.clock.Now().UTC().Add(-2*time.Hour)); err != nil {
		t.Fatalf("record a release of %s's pod: %v", issue, err)
	}
	r.envoy.gone(worker)
	r.boot()

	pauseView := r.pauseProject()
	r.awaitState(r.token(issue, claim.RoleTester), supervise.StateSuspended, "the pause stops the idle tester")
	urgency := texts.UrgencyOf(texts.CISettled, claim.RoleImplementer, texts.Variant{Phase: phase.Testing})
	if urgency != texts.Low {
		t.Fatalf("a CI settlement of an issue in testing goes to the implementer at %q, want low: this case proves nothing", urgency)
	}
	settled, err := record.NewRoleMessage(string(worker), texts.CISettledRecord{RoleHead: texts.RoleHead{Kind: texts.CISettled, Issue: issue},
		PullRequest: texts.PullRequestRef(liveRepo, 42), Head: "c0de", Phase: phase.Testing, Passed: 3, ListPassed: texts.ListPassed(liveRepo, 42)}, urgency)
	if err != nil {
		t.Fatalf("build the CI settlement: %v", err)
	}
	message := r.enqueue(issue, settled)
	time.Sleep(time.Second)
	if n := r.count(`select count(*) from outbox where id = $1`, message); n != 1 {
		t.Fatalf("the role message's row is gone while the project is paused, want it held")
	}
	r.resumeProject()
	released := false
	for _, line := range r.lines(pause.HeldRowReleasedMessage) {
		if id, ok := line["row"].(float64); ok && int64(id) == message && int64(line["pause"].(float64)) == pauseView.ID {
			released = true
		}
	}
	if !released {
		t.Fatalf("the resume logged no release of held row %d: the pause did not hold it, and this case proves nothing", message)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if r.count(`select count(*) from pod_resumes where issue = $1 and role = 'implementer'`, issue) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the resume launched no reader of the low role message the pause held for %s (row %d: %d left in the outbox, %d publishes to the role, %d wakes of the implementer); R5: the resume launches each reader it held a low text for",
		worker, message, r.count(`select count(*) from outbox where id = $1`, message), len(r.envoy.publishedTo(worker)),
		r.count(`select count(*) from pod_resumes where issue = $1 and role = 'implementer'`, issue))
}
