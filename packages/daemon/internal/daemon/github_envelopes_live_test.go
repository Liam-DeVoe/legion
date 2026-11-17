//go:build e2e

// The GitHub envelopes two whole-daemon live proofs publish alike, merge_wait_readable_live_test.go's
// and review_range_live_test.go's: the review App's review of a pull request, the implementer's push
// and GitHub's synchronize, each published on the ENVOY_NOTIFICATIONS stream as Envoy relays the
// webhook (liveProof's publish) and waited on until the daemon's intake has applied it
// (waitProcessed). It carries `//go:build e2e` because no untagged test calls it.
package daemon

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// publishReviewAs publishes a review of the pull request numbered number at head, as Envoy relays
// a GitHub pull-request review (intake's decodeReview reads it). It is the review App's own, with
// the Legion footer naming session and head, which is what lets it decide the round at all
// (workflow's refusesRound): the head is the pull request's current head. The session is held to
// the one the claims record holds for the claim the daemon starts the reviewer under, in the first
// round as in every later one (workflow's byReviewer): only a review naming that session decides
// the round and moves the commit the reviewer read, and the login decides by itself only while
// that claim records no session. seq numbers the envelope and reviewID the review: each review a
// run publishes carries an id and a time after the one before it, so none is ordered behind a
// review already applied (record.ReviewOrder). The time is the next whole second after the
// publish: GitHub's submitted_at is to the second, and only a review submitted after its round's
// entry moves the commit the reviewer read (workflow's review), so a time truncated to the second
// could stand before an entry raised in that same second. A real review comes minutes after its
// round's entry.
func (p *liveProof) publishReviewAs(session string, number int, state, head string, seq, reviewID int64) {
	p.t.Helper()
	payload, err := json.Marshal(map[string]string{
		"kind": "review", "repo": liveRepo, "number": strconv.Itoa(number), "action": "submitted",
		"state": state, "commit_id": head, "head_sha": head, "author": reviewAppLogin,
		"body":           "the reviewer's " + state + " of " + head + legionFooter(session, head),
		"body_truncated": "false", "review_id": strconv.FormatInt(1000+reviewID, 10),
		"submitted_at": time.Now().UTC().Truncate(time.Second).Add(time.Second).Format(time.RFC3339),
	})
	if err != nil {
		p.t.Fatalf("encode the review payload: %v", err)
	}
	subject := fmt.Sprintf("notifications.github.%s.pr.%d.review", p.subjectRepo(), number)
	p.waitProcessed(p.publish(subject, "github", fmt.Sprintf("review-%d-%d", number, seq), string(payload)))
}

// publishPushOf publishes the implementer's push of to onto from on issue's branch, changing code,
// as Envoy relays a GitHub push (intake's decodePush reads it). It is published before the
// synchronize that moves the head, which is the order that leaves the move judged by the chain of
// its pushes (classify.AdvancePullRequestHead).
func (p *liveProof) publishPushOf(issue, from, to string) {
	p.t.Helper()
	payload, err := json.Marshal(map[string]string{
		"kind": "push", "repo": liveRepo, "ref": "refs/heads/" + claim.IssueBranch(issue), "before": from, "after": to,
		"changed_paths": "src/widget.go", "changed_paths_truncated": "false", "forced": "false",
		"pusher": "legion-implementer[bot]", "commit_count": "1", "head_subject": "earn what the base requires",
	})
	if err != nil {
		p.t.Fatalf("encode the push payload: %v", err)
	}
	subject := fmt.Sprintf("notifications.github.%s.push.branch.%s", p.subjectRepo(), claim.IssueBranch(issue))
	p.waitProcessed(p.publish(subject, "github", fmt.Sprintf("push-%s-%s", issue, to[:8]), string(payload)))
}

// publishSynchronizeOf publishes GitHub's synchronize of the pull request numbered number, issue's,
// on base and titled title, to head at updated, as Envoy relays a GitHub pull-request event
// (intake's decodePullRequest reads it). updated is GitHub's own clock, which the lifecycle fence
// orders the observation by and the record keeps as HeadUpdatedAt.
func (p *liveProof) publishSynchronizeOf(number int, issue, head, base, title string, updated time.Time) {
	p.t.Helper()
	payload, err := json.Marshal(map[string]string{
		"action": "synchronize", "kind": "pr", "repo": liveRepo, "number": strconv.Itoa(number),
		"head_sha": head, "base_ref": base, "head_ref": claim.IssueBranch(issue),
		"title":      title,
		"url":        "https://github.com/" + liveRepo + "/pull/" + strconv.Itoa(number),
		"updated_at": updated.Format(time.RFC3339),
	})
	if err != nil {
		p.t.Fatalf("encode the synchronize payload: %v", err)
	}
	subject := fmt.Sprintf("notifications.github.%s.pr.%d", p.subjectRepo(), number)
	p.waitProcessed(p.publish(subject, "github", fmt.Sprintf("pr-%d-synchronize-%s", number, head[:8]), string(payload)))
}
