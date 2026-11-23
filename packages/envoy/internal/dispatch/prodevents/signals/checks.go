package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/prodevents"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The check_fail signal (dispatch://INFRA-978, R7 and T12): a check_results row with verdict fail
// on a check whose when is after_merge (the result.recorded event's When) is a failure of the
// deploy that first shipped its commit (prodevents.DeployForCommit), written under the project of
// the issue the result was recorded on, naming that issue's pull requests, keyed
// check:<check block id>@<deploy id>, its actor the result's and its detected time the result's
// own. A result whose commit no deploy row holds yet makes its row when a deploy row that holds it
// lands (matchEarlyChecks); a result no deploy ever holds makes none. A retraction of the result
// rejects the row it made, with the retraction's reason and actor.

// The words of a check result this signal reads (api's checkWhenAfterMerge and the verdict column).
const (
	whenAfterMerge = "after_merge"
	verdictFail    = "fail"
)

// checkResult is what the signal reads of one check_results row: its issue's project, its check,
// its commit, its verdict, the When its result.recorded event judged it by, its actor and time, and
// whether a retraction stands against it.
type checkResult struct {
	id         string
	issueKey   string
	project    string
	artifactID string
	blockID    string
	commitSHA  string
	verdict    string
	when       string
	actor      model.Actor
	createdAt  time.Time
	retracted  bool
}

// checkResultColumns is checkResult's read: the result, its issue's project, the When of the event
// it was recorded by (the event's own seq on the check's issue, check_results.event_seq), and
// whether a retraction stands against it.
const checkResultColumns = `
	select r.id::text, r.issue_key, i.project_key, r.check_artifact_id::text, r.check_block_id, r.commit_sha,
		r.verdict, coalesce(e.payload ->> 'when', ''), r.actor, r.created_at,
		exists (select from check_result_retractions x where x.result_id = r.id)
	from check_results r
	join issues i on i.key = r.issue_key
	left join events e on e.issue_key = r.check_issue_key and e.seq = r.event_seq`

func scanCheckResult(row pgx.Row) (checkResult, error) {
	var result checkResult
	var actor []byte
	if err := row.Scan(&result.id, &result.issueKey, &result.project, &result.artifactID, &result.blockID, &result.commitSHA,
		&result.verdict, &result.when, &actor, &result.createdAt, &result.retracted); err != nil {
		return checkResult{}, err
	}
	if err := json.Unmarshal(actor, &result.actor); err != nil {
		return checkResult{}, fmt.Errorf("decode the actor of result %s: %w", result.id, err)
	}
	return result, nil
}

// readCheckResult reads the result id, and false where no row holds it.
func readCheckResult(ctx context.Context, q delivery.Querier, id string) (checkResult, bool, error) {
	result, err := scanCheckResult(q.QueryRow(ctx, checkResultColumns+` where r.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return checkResult{}, false, nil
	}
	if err != nil {
		return checkResult{}, false, fmt.Errorf("read check result %s: %w", id, err)
	}
	return result, true, nil
}

// counts reports whether the result is a check_fail signal: a fail of an after_merge check that no
// retraction has taken back.
func (r checkResult) counts() bool {
	return r.verdict == verdictFail && r.when == whenAfterMerge && !r.retracted
}

// checkSourceRef is the source_ref of the check_fail row of block's check blaming deploy.
func checkSourceRef(blockID, deployID string) string {
	return "check:" + blockID + "@" + deployID
}

// RecordCheckFailure writes the check_fail row of result id where it is one (a fail of an
// after_merge check, not retracted) and a deploy row holds its commit, and reports whether it wrote
// a row. A repeat writes nothing: the row is keyed by the check and the deploy. A result no deploy
// holds yet is logged with its id and makes its row when one lands (matchEarlyChecks).
func RecordCheckFailure(ctx context.Context, pool *store.Pool, id string) (bool, error) {
	result, found, err := readCheckResult(ctx, pool, id)
	if err != nil || !found || !result.counts() {
		return false, err
	}
	deploy, held, err := prodevents.DeployForCommit(ctx, pool, result.commitSHA)
	if err != nil {
		return false, err
	}
	if !held {
		slog.Info("dispatch: a failed after-merge check names a commit no deploy row holds yet; its deploy row makes its failure",
			"result", result.id, "commit", result.commitSHA)
		return false, nil
	}
	pulls, err := issuePullRequests(ctx, pool, result.issueKey)
	if err != nil {
		return false, err
	}
	_, created, err := prodevents.Write(ctx, pool, prodevents.Event{
		ProjectKey: result.project, Kind: prodevents.KindFailure, Source: prodevents.SourceCheckResults,
		SourceRef: checkSourceRef(result.blockID, deploy.ID), PullRequests: pulls, Actor: result.actor,
		DeployID: new(deploy.ID), Signal: new(prodevents.SignalCheckFail), DetectedAt: new(result.createdAt.UTC()),
	})
	if err != nil {
		return false, fmt.Errorf("write the check_fail of result %s: %w", result.id, err)
	}
	return created, nil
}

// issuePullRequests is the pull requests of issue (delivery_pull_requests attributed to it), as the
// record names them, oldest merge first.
func issuePullRequests(ctx context.Context, q delivery.Querier, issue string) ([]string, error) {
	rows, err := q.Query(ctx, `select repo, number from delivery_pull_requests where issue_key = $1
		order by merged_at nulls last, repo, number`, issue)
	if err != nil {
		return nil, fmt.Errorf("read the pull requests of %s: %w", issue, err)
	}
	defer rows.Close()
	pulls := []string{}
	for rows.Next() {
		var repo string
		var number int
		if err := rows.Scan(&repo, &number); err != nil {
			return nil, fmt.Errorf("read the pull requests of %s: %w", issue, err)
		}
		pulls = append(pulls, prodevents.PullRequestName(repo, number))
	}
	return pulls, rows.Err()
}

// RejectRetractedCheck rejects the check_fail row result id made, with the retraction's reason,
// actor and time, and reports whether it rejected one. A row another fail of the same check still
// stands behind (a result of it, not retracted, whose commit the same deploy first shipped) stays
// counted: that result makes the same row. A row rejected already, or one no result made, writes
// nothing.
func RejectRetractedCheck(ctx context.Context, pool *store.Pool, id, reason string, actor model.Actor, at time.Time) (bool, error) {
	result, found, err := readCheckResult(ctx, pool, id)
	if err != nil || !found || result.verdict != verdictFail || result.when != whenAfterMerge {
		return false, err
	}
	deploy, held, err := prodevents.DeployForCommit(ctx, pool, result.commitSHA)
	if err != nil || !held {
		return false, err
	}
	row, made, err := prodevents.BySourceRef(ctx, pool, result.project, checkSourceRef(result.blockID, deploy.ID))
	if err != nil || !made || row.State == prodevents.StateRejected {
		return false, err
	}
	standing, err := standingFailsOf(ctx, pool, result)
	if err != nil {
		return false, err
	}
	for _, other := range standing {
		if other.project != result.project {
			continue
		}
		if blamed, held, err := prodevents.DeployForCommit(ctx, pool, other.commitSHA); err != nil {
			return false, err
		} else if held && blamed.ID == deploy.ID {
			return false, nil
		}
	}
	if _, err := prodevents.SetState(ctx, pool, result.project, row.ID, prodevents.StateChange{
		State: prodevents.StateRejected, Reason: reason, Actor: actor, At: at,
	}); err != nil {
		return false, fmt.Errorf("reject the check_fail of result %s: %w", result.id, err)
	}
	return true, nil
}

// standingFailsOf is every other result of result's check that is a check_fail signal still
// (checkResult.counts).
func standingFailsOf(ctx context.Context, q delivery.Querier, result checkResult) ([]checkResult, error) {
	rows, err := q.Query(ctx, checkResultColumns+`
		where r.check_artifact_id = $1::uuid and r.check_block_id = $2 and r.id <> $3::uuid and r.verdict = 'fail'`,
		result.artifactID, result.blockID, result.id)
	if err != nil {
		return nil, fmt.Errorf("read the results of check %s: %w", result.blockID, err)
	}
	defer rows.Close()
	var standing []checkResult
	for rows.Next() {
		other, err := scanCheckResult(rows)
		if err != nil {
			return nil, fmt.Errorf("read the results of check %s: %w", result.blockID, err)
		}
		if other.counts() {
			standing = append(standing, other)
		}
	}
	return standing, rows.Err()
}

// matchEarlyChecks writes the check_fail rows a deploy row that just landed completes: every fail
// of an after_merge check, not retracted, whose commit the row holds (RecordCheckFailure, which
// blames the deploy that first shipped the commit and writes nothing for a row already made).
func matchEarlyChecks(ctx context.Context, pool *store.Pool, deploy prodevents.Event) error {
	if len(deploy.Commits) == 0 {
		return nil
	}
	rows, err := pool.Query(ctx, `
		select r.id::text from check_results r
		join events e on e.issue_key = r.check_issue_key and e.seq = r.event_seq
		where r.verdict = 'fail' and r.commit_sha = any($1) and e.payload ->> 'when' = $2
		  and not exists (select from check_result_retractions x where x.result_id = r.id)
		order by r.created_at, r.id`, deploy.Commits, whenAfterMerge)
	if err != nil {
		return fmt.Errorf("read the failed checks of deploy %s: %w", deploy.ID, err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("read the failed checks of deploy %s: %w", deploy.ID, err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the failed checks of deploy %s: %w", deploy.ID, err)
	}
	for _, id := range ids {
		if _, err := RecordCheckFailure(ctx, pool, id); err != nil {
			return err
		}
	}
	return nil
}

// feedEvent is one event an event feed reads (readEventPage): its id, its type, the issue it is on
// ("" for an event of no issue), its payload, its actor and when it was written.
type feedEvent struct {
	id        int64
	kind      string
	issueKey  string
	payload   []byte
	actor     model.Actor
	createdAt time.Time
}

// passChecks is one pass of the checks feed: it reads the result events after its cursor up to
// safe, the event log's safe horizon, page after page, writes the check_fail of each fail of an
// after_merge check (RecordCheckFailure) and rejects the row of each retracted result
// (RejectRetractedCheck), then moves its cursor over the page. A page shorter than a whole one
// read every event to safe, so the cursor moves to safe.
func passChecks(ctx context.Context, pool *store.Pool, safe int64) error {
	stored, err := readCursor(ctx, pool, checksCursor)
	if err != nil {
		return err
	}
	position := stored.position
	for position < safe {
		events, err := readEventPage(ctx, pool, position, safe, []string{"result.recorded", "result.retracted"}, "")
		if err != nil {
			return err
		}
		for _, event := range events {
			if err := checkSignal(ctx, pool, event); err != nil {
				return err
			}
		}
		next := safe
		if len(events) == page {
			next = events[len(events)-1].id
		}
		if err := moveCursor(ctx, pool, checksCursor, next, nil, time.Now()); err != nil {
			return err
		}
		position = next
	}
	return nil
}

// checkSignal is the write one result event makes.
func checkSignal(ctx context.Context, pool *store.Pool, event feedEvent) error {
	switch event.kind {
	case "result.recorded":
		var payload model.CheckResultEventPayload
		if err := json.Unmarshal(event.payload, &payload); err != nil {
			return fmt.Errorf("decode result event %d: %w", event.id, err)
		}
		if payload.Verdict != verdictFail || payload.When != whenAfterMerge {
			return nil
		}
		_, err := RecordCheckFailure(ctx, pool, payload.ResultID)
		return err
	case "result.retracted":
		var payload model.CheckResultRetractedEventPayload
		if err := json.Unmarshal(event.payload, &payload); err != nil {
			return fmt.Errorf("decode retraction event %d: %w", event.id, err)
		}
		_, err := RejectRetractedCheck(ctx, pool, payload.ResultID, payload.Reason, event.actor, event.createdAt)
		return err
	}
	return nil
}

// readEventPage reads up to a page of the events of kinds after after and up to safe, in id order,
// those an extra condition on the alias event keeps where where is not empty.
func readEventPage(ctx context.Context, q delivery.Querier, after, safe int64, kinds []string, where string) ([]feedEvent, error) {
	condition := ""
	if where != "" {
		condition = " and (" + where + ")"
	}
	rows, err := q.Query(ctx, `
		select event.id, event.type, coalesce(event.issue_key, ''), event.payload, event.actor, event.created_at
		from events event
		where event.id > $1 and event.id <= $2 and event.type = any($3)`+condition+`
		order by event.id
		limit $4`, after, safe, kinds, page)
	if err != nil {
		return nil, fmt.Errorf("read the events after %d: %w", after, err)
	}
	defer rows.Close()
	var events []feedEvent
	for rows.Next() {
		var event feedEvent
		var actor []byte
		if err := rows.Scan(&event.id, &event.kind, &event.issueKey, &event.payload, &actor, &event.createdAt); err != nil {
			return nil, fmt.Errorf("read the events after %d: %w", after, err)
		}
		if err := json.Unmarshal(actor, &event.actor); err != nil {
			return nil, fmt.Errorf("decode the actor of event %d: %w", event.id, err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
