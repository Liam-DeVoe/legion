//go:build e2e

// The live proof's driver for the commit a later review round is told: the task the daemon prompts
// the reviewer with in its second review round holds ` Last reviewed commit: <sha>.`, the head the
// reviewer's own deciding review of the first round read (record.PhaseRow.ReviewedHead, which
// workflow's review writes and workflow's reviewedPart names), and the task of the first round names
// no commit. scripts/e2e/review-range.sh builds this driver with `-tags e2e` and owns the
// nats-server and the PostgreSQL it runs against.
//
// The rig is the handoff records proof's (startOnDispatch, in handoff_records_live_test.go): the
// real Dispatch server of the checkout on a database of its own, the whole daemon booted with
// dispatch_url at it, the real `dispatch` CLI writing each role's handoff under the session its
// claim registered, and each claim driven over the daemon's worker stream as its pane's shim drives
// it (handoffClaimant), the task it is prompted with read as the shim reads it. The reviewer's
// review is the review App's own, published on the ENVOY_NOTIFICATIONS stream as Envoy relays
// GitHub's webhook (publishReviewAs). It decides the round (workflow's RefusesRound) because its
// Legion footer names the session the reviewer's claim registered and the pull request's head, a
// commit id of 40 lowercase hexadecimal characters. The implementer's push and GitHub's synchronize
// move that head before the second round (publishPushOf, publishSynchronizeOf), so the commit the
// second round is told is not the head it reviews. GitHub's REST API is reviewRangeGitHub; no agent
// runs. The two issues and their pull requests are seeded in testing through the store
// (seedIssues), each with its tester's launch for the boot to relaunch.
//
// Each check reads the reviewer's task of its round, and first holds that it is that task: it says
// `Phase: reviewing.` and names the tester's handoff of that round by its version. Without that, a
// check of an absence would pass on a task of another round.
package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// lastReviewed is the part of a task that names the commit the reviewer's last deciding review read
// (workflow's reviewedPart), without the commit.
const lastReviewed = "Last reviewed commit"

// reviewRangeLive is one run of the proof: the handoff records rig booted over the real Dispatch,
// the GitHub stand-in, the two issues the steps walk through two review rounds, and the counters
// that keep each review its own fact.
type reviewRangeLive struct {
	*handoffRecordsLive
	pulls *reviewRangeGitHub
	// walked is the issue the first two checks walk; control the issue the third walks, with the
	// commit of its first review cleared from the record between its rounds.
	walked, control *reviewRangeIssue
	// seq numbers the review envelopes, so no two carry one event id, and reviewID numbers the
	// reviews, so each is newer than the one before it (record.ReviewOrder).
	seq, reviewID int64
}

// reviewRangeIssue is one issue the proof walks: its key, the name its sessions carry, its pull
// request, and its tester's and reviewer's claims once they register, which both of its rounds use.
type reviewRangeIssue struct {
	key, name string
	number    int
	// first is the head the pull request is opened at, which the first review reads; second is
	// the head the implementer pushes in the second round.
	first, second    string
	tester, reviewer *handoffClaimant
}

// TestReviewRangeLive drives the proof's three checks against one booted daemon and one Dispatch.
func TestReviewRangeLive(t *testing.T) {
	fixtureenv.Live(t)
	r := newReviewRangeLive(t)
	r.step("round-1-names-no-commit", r.roundOneNamesNoCommit)
	r.step("round-2-names-the-commit-of-round-1", r.roundTwoNamesTheCommitOfRoundOne)
	r.step("control-no-record-no-commit", r.controlNoRecordNoCommit)
}

// newReviewRangeLive starts Dispatch, creates the two issues on it, seeds them in testing on open
// pull requests, and boots the daemon at Dispatch with reviewRangeGitHub as GitHub.
func newReviewRangeLive(t *testing.T) *reviewRangeLive {
	t.Helper()
	r := &reviewRangeLive{handoffRecordsLive: startOnDispatch(t, "scripts/e2e/review-range.sh", "RR", "Review range live proof")}
	number, err := strconv.ParseInt(randomSuffix(t)[:6], 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	// Dispatch refuses a second issue whose title shares most of a first one's words as a possible
	// duplicate, so the two are titled apart.
	r.walked = r.newIssue("walked", "a later review round names the commit the round before it read", int(number)+1)
	r.control = r.newIssue("control", "the control whose record loses that commit between its rounds", int(number)+2)
	r.pulls = newReviewRangeGitHub(t, r.walked, r.control)
	r.github = r.pulls.url
	r.seedIssues([]dispatchSeed{
		{key: r.walked.key, phase: phase.Testing, role: claim.RoleTester, pr: r.walked.number, head: r.walked.first},
		{key: r.control.key, phase: phase.Testing, role: claim.RoleTester, pr: r.control.number, head: r.control.first},
	})
	r.boot()
	return r
}

// newIssue creates an issue titled title on Dispatch, in progress with the legion label, as a
// running daemon leaves one, with its pull request numbered number and the two heads of its rounds.
func (r *reviewRangeLive) newIssue(name, title string, number int) *reviewRangeIssue {
	key := r.createIssue(title, "in_progress", []string{"legion"})
	return &reviewRangeIssue{key: key, name: name, number: number,
		first: headCommit(key, "review round 1"), second: headCommit(key, "review round 2")}
}

// Check round-1-names-no-commit. The walked issue reaches reviewing, and the task the daemon prompts
// the reviewer with names no reviewed commit.
func (r *reviewRangeLive) roundOneNamesNoCommit() {
	task := r.roundOne(r.walked)
	if strings.Contains(task, lastReviewed) {
		r.t.Fatalf("the reviewer's task of round 1 = %q, want no %q in it: no review of this generation has read a commit yet", task, lastReviewed)
	}
	r.note("the task names no %q", lastReviewed)
}

// Check round-2-names-the-commit-of-round-1. The reviewer requests changes at the walked issue's
// first head; the implementer pushes the second head and completes, and the tester passes; the task
// the daemon prompts the reviewer with names the first head as the last reviewed commit.
func (r *reviewRangeLive) roundTwoNamesTheCommitOfRoundOne() {
	issue := r.walked
	r.requestChanges(issue)
	task := r.roundTwo(issue)
	want := " " + lastReviewed + ": " + issue.first + "."
	if !strings.Contains(task, want) {
		r.t.Fatalf("the reviewer's task of round 2 = %q, want it to hold %q, the head its review of round 1 read", task, want)
	}
	r.note("the task holds %q, the head round 1's review read, while the pull request is at %s", want, issue.second)
}

// Check control-no-record-no-commit. The control issue walks the same first round and review, and
// its reviewer's phases row holds the commit that review read; then the commit is cleared from that
// row in the daemon's database, and the task of round 2 names no reviewed commit. The check before
// it is this run with the clear left out. The read before the clear is what lets the control fail
// on a first review that recorded nothing: PostgreSQL counts a row the clear matches even when it
// already holds the empty string, so the clear's one row says nothing of what the row held.
func (r *reviewRangeLive) controlNoRecordNoCommit() {
	issue := r.control
	task := r.roundOne(issue)
	if strings.Contains(task, lastReviewed) {
		r.t.Fatalf("the control's reviewer task of round 1 = %q, want no %q in it", task, lastReviewed)
	}
	r.requestChanges(issue)
	if got := r.reviewedHead(issue.key); got != issue.first {
		r.t.Fatalf("phases.reviewed_head of %s's reviewer before the clear = %q, want %s, the head its review of round 1 read", issue.key, got, issue.first)
	}
	tag, err := r.st.Pool().Exec(context.Background(), "update phases set reviewed_head = '' where issue = $1 and role = 'reviewer'", issue.key)
	if err != nil || tag.RowsAffected() != 1 {
		r.t.Fatalf("clear phases.reviewed_head of %s's reviewer = %v rows, %v; want 1 row and no error", issue.key, tag.RowsAffected(), err)
	}
	if got := r.reviewedHead(issue.key); got != "" {
		r.t.Fatalf("phases.reviewed_head of %s's reviewer after the clear = %q, want \"\"", issue.key, got)
	}
	r.note("cleared phases.reviewed_head of %s's reviewer; it now reads \"\"", issue.key)
	task = r.roundTwo(issue)
	if strings.Contains(task, lastReviewed) {
		r.t.Fatalf("the control's reviewer task of round 2 = %q, want no %q in it: its record holds no reviewed commit", task, lastReviewed)
	}
	r.note("the task names no %q", lastReviewed)
}

// roundOne walks issue's first review round: the seeded tester, which the boot relaunched, is
// prompted to resume its phase, writes its handoff and passes; the daemon moves the issue to
// reviewing, and the reviewer it starts registers and is prompted. It returns that task, held to be
// the reviewing task naming the tester's handoff of round 1.
func (r *reviewRangeLive) roundOne(issue *reviewRangeIssue) string {
	issue.tester = r.claimant(claim.RoleTester, issue.key, "ses_"+issue.name+"_tester")
	r.awaitTask(issue.tester, "Phase: testing.", "Resume the existing phase work.")
	test := r.pass(issue, 1)
	issue.reviewer = r.claimant(claim.RoleReviewer, issue.key, "ses_"+issue.name+"_reviewer")
	task := r.awaitTask(issue.reviewer, "Phase: reviewing.", handoffLine(issue.key, "test", test.version))
	r.note("round 1: the reviewer's shim was prompted: %q", task)
	return task
}

// requestChanges publishes the reviewer's request for changes at issue's first head, its footer
// naming the session the reviewer's claim registered, and waits for the issue to move back to
// implementing; the reviewer's turn then ends, as a worker's does after its review.
func (r *reviewRangeLive) requestChanges(issue *reviewRangeIssue) {
	r.seq++
	r.reviewID++
	r.publishReviewAs(issue.reviewer.session, issue.number, "changes_requested", issue.first, r.seq, r.reviewID)
	r.note("published the review App's REQUEST_CHANGES of #%d, its footer naming session %s and head %s", issue.number, issue.reviewer.session, issue.first)
	r.waitPhase(issue.key, phase.Implementing)
	r.note("read back: %s is back in implementing; phases.reviewed_head of its reviewer is %q", issue.key, r.reviewedHead(issue.key))
	issue.reviewer.endTurn()
}

// roundTwo walks issue's second round from implementing: the implementer pushes the second head,
// which GitHub's synchronize moves the pull request to, writes its handoff and completes at that
// head; the tester passes; and the reviewer is prompted again. It returns that task, held to be the
// reviewing task naming the tester's handoff of round 2.
func (r *reviewRangeLive) roundTwo(issue *reviewRangeIssue) string {
	t := r.t
	implementer := r.claimant(claim.RoleImplementer, issue.key, "ses_"+issue.name+"_implementer")
	r.awaitTask(implementer, "Phase: implementing.")
	r.pulls.head(issue.key, issue.second)
	r.publishPushOf(issue.key, issue.first, issue.second)
	r.publishSynchronizeOf(issue.number, issue.key, issue.second, "main", "the pull request of "+issue.key, time.Now())
	if got := r.pullRequestHead(issue.key); got != issue.second {
		t.Fatalf("the head the record holds for %s after the push = %q, want %s", issue.key, got, issue.second)
	}
	r.note("published the implementer's push of %s onto %s and GitHub's synchronize; the record holds the pull request at %s", issue.second, issue.first, issue.second)
	implement := r.write(implementer.session, issue.key, "implement", r.smallBody(issue.key, "implement", "round 2"))
	status, failure := implementer.complete(api.HandoffCompleteRequest{Summary: "implemented round 2", Commit: issue.second, HandoffID: implement.id, HandoffVersion: implement.version})
	if status != http.StatusOK {
		t.Fatalf("the implementer's completion of round 2 = %d %+v, want 200", status, failure)
	}
	implementer.endTurn()
	r.waitPhase(issue.key, phase.Testing)
	r.note("the implementer completed at %s naming %s; read back: %s is in testing", issue.second, implement.line, issue.key)
	r.awaitTask(issue.tester, "Phase: testing.", handoffLine(issue.key, "implement", implement.version))
	test := r.pass(issue, 2)
	task := r.awaitTask(issue.reviewer, "Phase: reviewing.", handoffLine(issue.key, "test", test.version))
	r.note("round 2: the reviewer's shim was prompted: %q", task)
	return task
}

// pass has issue's tester write its handoff of round and complete with a pass, and waits for the
// issue to reach reviewing. Its turn ends with its completion, as a worker's does: the reviewer's
// task is held while another role of the issue is in a turn (supervise's sendPending).
func (r *reviewRangeLive) pass(issue *reviewRangeIssue, round int) handoffWritten {
	t := r.t
	t.Helper()
	test := r.write(issue.tester.session, issue.key, "test", r.smallBody(issue.key, "test", fmt.Sprintf("round %d passes", round)))
	status, failure := issue.tester.complete(api.HandoffCompleteRequest{Summary: "tested", Verdict: "pass", HandoffID: test.id, HandoffVersion: test.version})
	if status != http.StatusOK {
		t.Fatalf("the tester's passing completion of round %d = %d %+v, want 200", round, status, failure)
	}
	issue.tester.endTurn()
	r.waitPhase(issue.key, phase.Reviewing)
	r.note("round %d: the tester passed naming %s; read back: %s is in reviewing", round, test.line, issue.key)
	return test
}

// awaitTask waits for the prompt of c whose message holds each of want, which is the task of the
// round a step reads, and returns it. A prompt before it that does not is noted and passed over,
// so a check reads the task of its own round and no other message the daemon sent the claim.
func (r *reviewRangeLive) awaitTask(c *handoffClaimant, want ...string) string {
	r.t.Helper()
	deadline := time.After(2 * time.Minute)
	for {
		select {
		case prompt := <-c.prompts:
			if holdsAll(prompt.Message, want) {
				return prompt.Message
			}
			r.note("%s was first prompted with another task: %q", c.token, prompt.Message)
		case <-deadline:
			r.t.Fatalf("%s was prompted with no task holding %q within 2m", c.token, want)
			return ""
		}
	}
}

// holdsAll says whether text holds each of parts.
func holdsAll(text string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

// handoffLine is the part of a task that names the handoff of word on key at version (workflow's
// task).
func handoffLine(key, word string, version int) string {
	return fmt.Sprintf("Handoff: %s version %d (dispatch handoff-read --issue %s --phase %s --version %d).", word, version, key, word, version)
}

// reviewedHead is phases.reviewed_head of key's reviewer, read from the daemon's own database.
func (r *reviewRangeLive) reviewedHead(key string) string {
	r.t.Helper()
	var head string
	if err := r.st.Pool().QueryRow(context.Background(), "select reviewed_head from phases where issue = $1 and role = 'reviewer'", key).Scan(&head); err != nil {
		r.t.Fatalf("read phases.reviewed_head of %s's reviewer: %v", key, err)
	}
	return head
}

// pullRequestHead is the head the daemon's record holds for key's pull request.
func (r *reviewRangeLive) pullRequestHead(key string) string {
	r.t.Helper()
	var head string
	if err := r.st.Pool().QueryRow(context.Background(), "select head_sha from pull_requests where issue = $1", key).Scan(&head); err != nil {
		r.t.Fatalf("read the head of %s's pull request: %v", key, err)
	}
	return head
}

// reviewRangeGitHub is GitHub's REST API as the daemon reads it through this proof: every
// repository has default branch main, and the open pull requests of an issue's branch are that
// issue's one, at the head the step last set. The implementer's completion reads that list before
// it moves its issue on. Every other route is a 404, as in handoffRecordsDaemonGitHub, so the
// daemon's own read of a pull request (readIssuePullRequest) fails and the record stands as the
// envelopes left it.
type reviewRangeGitHub struct {
	url   string
	mu    sync.Mutex
	pulls map[string]*reviewRangePull
}

// reviewRangePull is one issue's open pull request, by the issue's branch.
type reviewRangePull struct {
	number int
	head   string
}

func newReviewRangeGitHub(t *testing.T, issues ...*reviewRangeIssue) *reviewRangeGitHub {
	t.Helper()
	g := &reviewRangeGitHub{pulls: map[string]*reviewRangePull{}}
	for _, issue := range issues {
		g.pulls[claim.IssueBranch(issue.key)] = &reviewRangePull{number: issue.number, head: issue.first}
	}
	g.url = githubtest.Server(t, githubtest.Spec{Handle: g.ServeHTTP})
	return g
}

// head moves key's pull request to sha, as a push does on GitHub.
func (g *reviewRangeGitHub) head(key, sha string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pulls[claim.IssueBranch(key)].head = sha
}

func (g *reviewRangeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rest, isRepository := strings.CutPrefix(r.URL.Path, "/repos/")
	switch parts := strings.Split(rest, "/"); {
	case r.Method == http.MethodGet && isRepository && len(parts) == 2:
		_, _ = io.WriteString(w, `{"full_name":"`+parts[0]+"/"+parts[1]+`","default_branch":"main"}`)
	case r.Method == http.MethodGet && isRepository && len(parts) == 3 && parts[2] == "pulls":
		// The query names the branch as `<owner>:<branch>` (githubrest's OpenPullRequestOn).
		_, branch, _ := strings.Cut(r.URL.Query().Get("head"), ":")
		pull, open := g.pulls[branch]
		if !open {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		_, _ = fmt.Fprintf(w, `[{"number":%d,"state":"open","head":{"ref":%q,"sha":%q},"base":{"ref":"main"},"updated_at":"2026-10-28T00:00:00Z"}]`, pull.number, branch, pull.head)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}
}
