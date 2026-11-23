package signals

import (
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/prodevents"
)

// TestAFailedAfterMergeCheckMakesOneCheckFailRow: a fail of an after_merge check, at a commit a
// deploy row holds, is one counted check_fail row of the result issue's project, blaming that
// deploy, naming the issue's pull requests, keyed check:<block>@<deploy>, with the result's actor
// and time; written by the record route's handler (RecordCheckFailure) and by the checks feed from
// the result.recorded event, a repeat of either writes no second row.
func TestAFailedAfterMergeCheckMakesOneCheckFailRow(t *testing.T) {
	r := newRig(t)
	issue := r.issue(1)
	r.pullRequest(8, at(30), sha("8"), issue)
	r.deployRun(200, at(60), sha("a"), prodJob, success, success)
	r.passDeploys()
	deploy := r.deployOf(200)

	result := r.checkResult(issue, "c1", sha("8"), verdictFail, whenAfterMerge)
	created, err := RecordCheckFailure(r.ctx, r.pool, result)
	if err != nil || !created {
		t.Fatalf("RecordCheckFailure = %v, %v; want a row", created, err)
	}
	again, err := RecordCheckFailure(r.ctx, r.pool, result)
	if err != nil || again {
		t.Fatalf("a repeat of RecordCheckFailure = %v, %v; want no second row", again, err)
	}
	r.passEvents()

	rows := r.failures()
	if len(rows) != 1 {
		t.Fatalf("failure rows = %+v, want one", rows)
	}
	row := rows[0]
	if row.ProjectKey != alpha || row.Source != prodevents.SourceCheckResults || *row.Signal != prodevents.SignalCheckFail ||
		row.SourceRef != "check:c1@"+deploy.ID || *row.DeployID != deploy.ID || row.State != prodevents.StateCounted ||
		row.Actor != tester || !slices.Equal(row.PullRequests, []string{"acme/widgets#8"}) {
		t.Fatalf("check_fail row = %+v", row)
	}
	var recorded string
	if err := r.pool.QueryRow(r.ctx, `select created_at::text from check_results where id = $1`, result).Scan(&recorded); err != nil {
		t.Fatalf("read the result's time: %v", err)
	}
	var detected string
	if err := r.pool.QueryRow(r.ctx, `select detected_at::text from production_events where id = $1`, row.ID).Scan(&detected); err != nil || detected != recorded {
		t.Errorf("detected_at = %s (%v), want the result's own time %s", detected, err, recorded)
	}
}

// TestAPassABeforeMergeFailAndAnUnmatchedCommitMakeNoCheckFailRow: a passing result, a fail of a
// check run before the merge, and a fail of an after_merge check at a commit no deploy row holds
// make no row, through the handler's write and through the checks feed alike.
func TestAPassABeforeMergeFailAndAnUnmatchedCommitMakeNoCheckFailRow(t *testing.T) {
	r := newRig(t)
	issue := r.issue(2)
	r.deployRun(210, at(60), sha("b"), prodJob, success, success)
	r.passDeploys()

	for _, result := range []string{
		r.checkResult(issue, "c1", sha("b"), "pass", whenAfterMerge),
		r.checkResult(issue, "c2", sha("b"), verdictFail, "before_merge"),
		r.checkResult(issue, "c3", sha("e"), verdictFail, whenAfterMerge),
	} {
		if created, err := RecordCheckFailure(r.ctx, r.pool, result); err != nil || created {
			t.Errorf("RecordCheckFailure(%s) = %v, %v; want no row", result, created, err)
		}
	}
	r.passEvents()
	if rows := r.failures(); len(rows) != 0 {
		t.Fatalf("failure rows = %+v, want none", rows)
	}
}

// TestARetractionRejectsTheCheckFailRow: a retraction of the result that made a check_fail row
// rejects that row, with the retraction's reason and actor, from the result.retracted event the
// checks feed reads; the row stays, out of the numbers.
func TestARetractionRejectsTheCheckFailRow(t *testing.T) {
	r := newRig(t)
	issue := r.issue(3)
	r.deployRun(220, at(60), sha("c"), prodJob, success, success)
	r.passDeploys()
	result := r.checkResult(issue, "c1", sha("c"), verdictFail, whenAfterMerge)
	r.passEvents()
	if rows := r.failures(); len(rows) != 1 || rows[0].State != prodevents.StateCounted {
		t.Fatalf("before the retraction, failure rows = %+v, want one counted", rows)
	}

	const reason = "The check ran against the staging cluster, not production."
	r.exec("retract the result", `insert into check_result_retractions (result_id, reason, actor) values ($1, $2, $3)`,
		result, reason, `{"kind":"user","id":"alice"}`)
	r.event(issue, "result.retracted", person, model.CheckResultRetractedEventPayload{
		ResultID: result, Issue: issue, CheckIssue: issue, CheckBlockID: "c1", CommitSHA: sha("c"), Reason: reason,
	})
	r.passEvents()

	rows := r.failures()
	if len(rows) != 1 {
		t.Fatalf("after the retraction, failure rows = %+v, want the one row kept", rows)
	}
	if row := rows[0]; row.State != prodevents.StateRejected || row.StateReason == nil || *row.StateReason != reason ||
		row.StateBy == nil || *row.StateBy != person || row.StateAt == nil {
		t.Fatalf("the retracted result's row = state %q, reason %v, by %v; want rejected with the retraction's reason and actor",
			row.State, row.StateReason, row.StateBy)
	}
}

// TestAnEarlyCheckFailIsMatchedWhenItsDeployRowLands: a fail of an after_merge check whose commit no
// deploy row holds yet makes no row; the deploy row that holds the commit, once it lands, makes it,
// blaming that deploy.
func TestAnEarlyCheckFailIsMatchedWhenItsDeployRowLands(t *testing.T) {
	r := newRig(t)
	issue := r.issue(4)
	r.checkResult(issue, "c1", sha("d"), verdictFail, whenAfterMerge)
	r.passEvents()
	if rows := r.failures(); len(rows) != 0 {
		t.Fatalf("before any deploy holds the commit, failure rows = %+v, want none", rows)
	}

	r.deployRun(230, at(60), sha("d"), prodJob, success, success)
	r.passDeploys()
	deploy := r.deployOf(230)
	rows := r.failures()
	if len(rows) != 1 || *rows[0].DeployID != deploy.ID || rows[0].SourceRef != "check:c1@"+deploy.ID {
		t.Fatalf("once the deploy row landed, failure rows = %+v, want the check_fail blaming %s", rows, deploy.ID)
	}
}
