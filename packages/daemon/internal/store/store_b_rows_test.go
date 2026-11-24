package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// This release, one GitHub App and the daemon's legion-verdict check (B), writes rows a build
// before it must read: a revert of its rollout puts each daemon back on its build before B, on the
// database B migrated and wrote (spec R15). This test is the first half of the pair that shows the
// build before B can. It makes the pair's database anew on the server LEGION_TEST_PG_DSN names
// (beforeBDatabase: the named database's name with "_b_rows" after it), migrates it, writes there
// through B's own code each kind of row B writes, and leaves them, committed.
// store_before_b_test.go's TestTheBuildBeforeBReadsBsStore is the second half: it is copied into
// a checkout of a build before B and run there with the same LEGION_TEST_PG_DSN, against the same
// database.
//
// That is why this test, unlike the rest of this package's, leaves its database behind: the second
// half runs in another checkout, after this process has ended, and finds the database by its name.
// The name is fixed, so the test drops a database of that name that an earlier run left before it
// makes its own, and the second half reads one run's rows. Two runs of this test at once on one
// server would drop each other's database; nothing here guards against that.
//
// The rows, as B's code writes them:
//
//   - an issue standing in merging with its pull request, whose recorded push names its pusher and
//     no role, as one App's push does (record.ClassifiedPush);
//   - the reviewer's verdict, as the workflow records a completion of reviewing (workflow's
//     handoff and decisionOf): on the reviewer's phases row, the round's decision approved at the
//     head and the commit it read; and the same verdict in verdicts, as the verdict of the review
//     step it ran, step 4 of the task default (workflow's recordVerdict); and the owner's
//     acceptance at that head, the approve of step 5 that the architect's sign-off records;
//   - a pull request with no Legion issue, at a head (outside_pull_requests), a review of it from
//     the CI reviewer, a review of the Legion head from a person with no submission time
//     (verdict_reviews), the side reviewer a controller named for it (side_reviewers) and that
//     reviewer's verdict, with no issue (verdicts);
//   - the writes of legion-verdict: the daemon's check run on the Legion head, its commit status
//     over another writer's on the outside head, and the merge-group write, a check run on the
//     commit of the merge queue's group (verdict_writes);
//   - the reviewer's claim, as its machine writes it once its launch gave it the head it reviews:
//     the two claims columns B's migration adds hold that head and the launch's generation
//     (supervise.Claim.ReviewLaunchHead and ReviewLaunchGeneration);
//   - one outbox row of each kind B runs, through record.NewOutboxRow, among them the ask for the
//     reviewer's verdict past review that the workflow queues for a push (askVerdict): the
//     merge-forward review with no reason; and the same ask of the architect that ran the owner's
//     acceptance, a role message to its claim. The test fails when a kind of B's lanes has no row
//     here, so a kind added to B is a kind this pair reads too.
func TestRowsForTheBuildBeforeB(t *testing.T) {
	ctx := context.Background()
	dsn := testDSN(t)
	database, url := beforeBDatabase(t, dsn)
	admin, err := testdb.DatabaseURL(dsn, "postgres")
	if err != nil {
		t.Fatalf("%v", err)
	}
	server, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to the admin database of the server LEGION_TEST_PG_DSN names: %v", err)
	}
	var left bool
	if err := server.QueryRow(ctx, "select exists (select 1 from pg_database where datname = $1)", database).Scan(&left); err != nil {
		t.Fatalf("read whether an earlier run left %s: %v", database, err)
	}
	if left {
		if err := testdb.Drop(ctx, server, database); err != nil {
			t.Fatalf("drop the %s an earlier run left: %v", database, err)
		}
	}
	// A file copy of template1 (docs/testing.md, "A database a Go test creates"), as emptyStore's.
	if _, err := server.Exec(ctx, "create database "+database+" strategy file_copy"); err != nil {
		t.Fatalf("create %s: %v", database, err)
	}
	if err := server.Close(ctx); err != nil {
		t.Fatalf("close the admin connection: %v", err)
	}
	st, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("open %s: %v", database, err)
	}
	t.Cleanup(st.Close)
	applied, err := st.Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate %s: %v", database, err)
	}
	version, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("read the schema version: %v", err)
	}
	t.Logf("made %s anew (an earlier run's dropped: %t) and migrated it: %d migrations applied, schema version %d", database, left, applied, version)

	records := record.NewStore()
	number, outside := beforeBNumber(t), beforeBNumber(t)
	key := fmt.Sprintf("ACME-%d", number)
	const repo = "acme/widgets"
	widgets := ghrepo.MustParse(repo)
	// tested is the commit of the tester's pass, pushedFrom the head a push past review replaced,
	// head the head it left, which the reviewer and the owner approve, group the merge queue's group
	// commit for the pull request, and outsideHead the head of the pull request no Legion issue owns.
	tested, pushedFrom, head, group, outsideHead := beforeBSHA(t), beforeBSHA(t), beforeBSHA(t), beforeBSHA(t), beforeBSHA(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	reviewerClaim := claim.Token("legion-acme-" + strings.ToLower(key) + "-reviewer")
	architectClaim := claim.Token("legion-acme-" + strings.ToLower(key) + "-architect")
	const summary = "The change does what its plan says, and the tester's pass covers this head."

	payloads := []struct {
		issue   string
		payload record.OutboxPayload
	}{
		{key, record.OrdinaryStatusWrite("in_progress", "testing", "")},
		{key, record.MessagePost{Body: fmt.Sprintf("Pull request %s#%d: legion-verdict is success at %s.", repo, number, head)}},
		{key, record.Notice{Kind: "phase-finished", Role: claim.RoleReviewer, Phase: phase.Reviewing, Summary: summary, Version: 1}},
		{key, record.ControllerNotice{Kind: "held", Role: claim.RoleImplementer, Phase: phase.Implementing, Reason: "escalated"}},
		{key, record.SuperviseRequest{Op: "start", Tree: key, Role: claim.RoleReviewer, Generation: 1, Task: "Review pull request #" + fmt.Sprint(number) + "."}},
		{key, record.GateSeed{ArtifactID: "artifact-spec", Version: 1}},
		{key, record.LingerClose{Generation: 1}},
		{key, record.IssueSuspend{Tree: key, Generation: 1, TreeGeneration: 1}},
		{key, record.WorkspaceRemove{Linger: 1}},
		{key, record.IssueBranch{Generation: 1}},
		{key, record.CheckWakeDue{Look: 1, OpenedAt: at, Reason: "the tester's own wake"}},
		{key, record.CheckLook{Look: 1, OpenedAt: at}},
		{key, record.CheckBound{}},
		{key, record.RoleMessage{Role: "legion.acme." + key + ".reviewer", Body: fmt.Sprintf("Pull request #%d: CI settled at %s.", number, head)}},
		{key, record.RoleMessage{Role: string(architectClaim), Body: fmt.Sprintf("%s: a push moved the head of %s#%d past review. Send your verdict on %s with review_verdict.", key, repo, number, head)}},
		{key, record.HookPrompt{Hook: "back-to-implementing", Role: claim.RoleImplementer, From: phase.Reviewing, To: phase.Implementing,
			Count: 1, Counts: map[string]int{"reviewing-implementing": 1}, PullRequest: fmt.Sprintf("%s#%d", repo, number),
			Prompt: "Read the reviewer's findings before you write anything."}},
		{key, record.UpdateAsk{Role: claim.RoleTester, Round: "pod:9d3f0c7a", Pod: "9d3f0c7a", Text: "The daemon must replace the pod that runs this issue's roles."}},
		{key, record.IssueBundle{Tree: key, Generation: 1, TreeGeneration: 1, Role: claim.RoleImplementer}},
		{key, record.IssueRelease{Tree: key, Generation: 1, TreeGeneration: 1}},
		{"ACME", record.GatedStepsPublish{Project: "ACME", Steps: []record.DesignStep{record.DesignStepCriteria, record.DesignStepSpec}}},
		{key, record.ClaimRelease{}},
		{key, record.DesignGatesPublish{Issue: key, Gates: []record.DesignGateDocument{{Step: record.DesignStepSpec, ArtifactID: "artifact-spec"}}}},
		{key, record.DesignNewsReconcile{}},
		{key, record.MergeQueueRead{Number: number, Head: head}},
		{key, record.MergeQueueRemoval{Number: number, Head: head, Unread: true}},
		{key, record.RoleHoldersPublish{Issue: key}},
		{key, record.StepRecord{ID: uuid.NewString(), Task: "default", Step: 2, Kind: "step", Role: "implementer", Type: "implementation",
			Agent: "legion-acme-" + strings.ToLower(key) + "-implementer", Generation: 1, StartedAt: &at}},
		{key, record.ConflictConfirm{Number: number, Head: head, FirstReadAt: at.Add(-time.Minute)}},
		// The ask for the reviewer's verdict at the head a push left past review (workflow's
		// askVerdict), and a merge forward's ask to the implementer, which names its reason.
		{key, record.MergeForwardMessage{Role: claim.RoleReviewer, Step: record.MergeForwardReview, Head: pushedFrom, Landed: head,
			Base: "main", Phase: phase.Merging, PullRequest: number}},
		{key, record.MergeForwardMessage{Role: claim.RoleImplementer, Step: record.MergeForwardAsk, Head: head, Base: "main",
			Phase: phase.Merging, PullRequest: number, Why: record.ConflictCheckNotGreen}},
		{key, record.PlanRecord{ID: uuid.NewString(), Generation: 1, Source: "default",
			Tasks: []record.PlanRecordTask{{ID: "default", Steps: []string{"planner", "implementer", "tester", "reviewer", "acceptance:owner"}}}}},
	}
	written := map[record.OutboxKind]bool{}
	for _, p := range payloads {
		written[p.payload.OutboxKind()] = true
	}
	for _, lane := range record.OutboxLanes() {
		for _, kind := range record.LaneKinds(lane) {
			if !written[kind] {
				t.Errorf("B runs outbox kind %s on its %s lane, and this test writes no row of it: write one, so the build before B is shown to read it", kind, lane)
			}
		}
	}

	// queued is the id of each outbox row this run writes.
	var queued []int64
	if err := Transact(ctx, st.Pool(), func(ctx context.Context, tx pgx.Tx) error {
		if err := records.PutIssue(ctx, tx, record.Issue{Key: key, Tree: key, Project: "ACME", Title: "A change the build before B reads",
			Phase: phase.Merging, Generation: 1, Status: "in_progress", Repo: repo, DefaultBranch: "main", GitHubRepo: repo}); err != nil {
			return err
		}
		if err := records.PutPullRequest(ctx, tx, record.PullRequest{Issue: key, Repo: repo, Number: number, Branch: "legion/" + key,
			Base: "main", HeadSHA: head, HeadUpdatedAt: at, State: record.PullRequestOpen, Generation: 1,
			Pushes: []record.ClassifiedPush{{SHA: head, Before: pushedFrom, Sender: "example-legion[bot]"}}}); err != nil {
			return err
		}
		if err := records.PutPhase(ctx, tx, record.PhaseRow{Issue: key, Role: claim.RoleTester, Claim: claim.Token("legion-acme-" + strings.ToLower(key) + "-tester"),
			HandoffCommit: tested, Summary: "Every check passes.", CompletedAt: at}); err != nil {
			return err
		}
		if err := records.PutPhase(ctx, tx, record.PhaseRow{Issue: key, Role: claim.RoleReviewer, Claim: reviewerClaim,
			HandoffCommit: head, Summary: summary, CompletedAt: at, ReviewedHead: head,
			Entry: 1, EntryTask: "Review pull request #" + fmt.Sprint(number) + ".",
			Decision: &record.ReviewDecision{State: "approved", Body: summary, Head: head}}); err != nil {
			return err
		}

		for _, given := range []Verdict{
			{Repository: widgets, Number: number, Head: head, Issue: key, Task: "default", Step: 4, Verdict: VerdictApprove, Session: string(reviewerClaim), RecordedAt: at},
			{Repository: widgets, Number: number, Head: head, Issue: key, Task: "default", Step: 5, Verdict: VerdictApprove, Session: string(architectClaim), RecordedAt: at},
		} {
			if _, err := RecordVerdictTx(ctx, tx, given); err != nil {
				return err
			}
		}
		if _, err := RecordOutsidePullRequestTx(ctx, tx, OutsidePullRequest{Repository: widgets, Number: outside, Head: outsideHead, RecordedAt: at}); err != nil {
			return err
		}
		if _, err := RecordVerdictReviewTx(ctx, tx, VerdictReview{Repository: widgets, Number: outside, GitHubID: int64(beforeBNumber(t)), Head: outsideHead,
			Author: "example-ci-review[bot]", State: "approved", SubmittedAt: at, RecordedAt: at}); err != nil {
			return err
		}
		if _, err := RecordVerdictReviewTx(ctx, tx, VerdictReview{Repository: widgets, Number: number, GitHubID: int64(beforeBNumber(t)), Head: head,
			Author: "a-writer", AuthorCanWrite: true, State: "changes_requested", RecordedAt: at}); err != nil {
			return err
		}
		if _, err := RecordSideReviewerTx(ctx, tx, SideReviewer{Repository: widgets, Number: outside, Head: outsideHead, Session: "ses-side", RecordedAt: at}); err != nil {
			return err
		}
		if _, err := RecordVerdictTx(ctx, tx, Verdict{Repository: widgets, Number: outside, Head: outsideHead, Verdict: VerdictApprove,
			Session: "ses-side", RecordedAt: at}); err != nil {
			return err
		}
		for _, write := range []VerdictWrite{
			{Repository: widgets, Head: head, Kind: VerdictWriteCheckRun, GitHubID: int64(beforeBNumber(t)), Conclusion: VerdictConclusionSuccess, WrittenAt: at},
			{Repository: widgets, Head: outsideHead, Kind: VerdictWriteStatus, GitHubID: int64(beforeBNumber(t)), Conclusion: VerdictConclusionSuccess,
				ForeignID: int64(beforeBNumber(t)), WrittenAt: at},
			// The merge-group write: the check run on the commit of the queue's group.
			{Repository: widgets, Head: group, Kind: VerdictWriteCheckRun, GitHubID: int64(beforeBNumber(t)), Conclusion: VerdictConclusionSuccess, WrittenAt: at},
		} {
			if _, err := RecordVerdictWriteTx(ctx, tx, write); err != nil {
				return err
			}
		}

		for _, p := range payloads {
			row, err := record.NewOutboxRow(p.issue, p.payload, at)
			if err != nil {
				return fmt.Errorf("NewOutboxRow(%s): %w", p.payload.OutboxKind(), err)
			}
			id, err := records.EnqueueID(ctx, tx, row)
			if err != nil {
				return err
			}
			queued = append(queued, id)
		}
		return nil
	}); err != nil {
		t.Fatalf("write B's rows: %v", err)
	}
	reviewer := supervise.Claim{Token: reviewerClaim, Project: "acme", Tree: key, Issue: key, Role: claim.RoleReviewer,
		Generation: 2, State: supervise.StateIdle, ReviewLaunchHead: head, ReviewLaunchGeneration: 2}
	if err := st.PutClaim(ctx, reviewer); err != nil {
		t.Fatalf("write the reviewer's claim with the head its launch gave it: %v", err)
	}

	// What was committed reads back through B's own reads: each review step's verdict at the head,
	// and the reviewer's decision, a state and a head in the keys every build reads.
	if err := Transact(ctx, st.Pool(), func(ctx context.Context, tx pgx.Tx) error {
		for step, by := range map[int]claim.Token{4: reviewerClaim, 5: architectClaim} {
			verdict, ok, err := StepVerdictTx(ctx, tx, widgets, number, head, "default", step)
			if err != nil {
				return err
			}
			if !ok || verdict.Session != string(by) || verdict.Issue != key {
				t.Errorf("the verdict of step %d on %s#%d at %s reads back as %+v (found %t), want %s's", step, repo, number, head, verdict, ok, by)
			}
		}
		if pr, ok, err := OutsidePullRequestTx(ctx, tx, widgets, outside); err != nil {
			return err
		} else if !ok || pr.Head != outsideHead {
			t.Errorf("the outside pull request %s#%d reads back as %+v (found %t)", repo, outside, pr, ok)
		}
		if writes, err := VerdictWritesTx(ctx, tx, widgets, group); err != nil {
			return err
		} else if len(writes) != 1 || writes[0].Kind != VerdictWriteCheckRun {
			t.Errorf("the merge-group write on %s reads back as %+v", group, writes)
		}
		phases, err := records.Phases(ctx, tx, key)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(phases, func(row record.PhaseRow) bool { return row.Role == claim.RoleReviewer })
		if i < 0 || phases[i].Decision == nil || phases[i].Decision.State != "approved" || phases[i].Decision.Head != head || phases[i].ReviewedHead != head {
			t.Errorf("the phases of %s read back as %+v; want the reviewer's decision approved at %s", key, phases, head)
		}
		var kinds int
		if err := tx.QueryRow(ctx, "select count(distinct kind) from outbox where id = any($1)", queued).Scan(&kinds); err != nil {
			return err
		}
		if kinds != len(written) {
			t.Errorf("the %d outbox rows this run queued hold %d kinds; want %d", len(queued), kinds, len(written))
		}
		return nil
	}); err != nil {
		t.Fatalf("read back B's rows: %v", err)
	}
	claims, err := st.Claims(ctx)
	if err != nil {
		t.Fatalf("read back the claims: %v", err)
	}
	if i := slices.IndexFunc(claims, func(c supervise.Claim) bool { return c.Token == reviewerClaim }); i < 0 ||
		claims[i].ReviewLaunchHead != head || claims[i].ReviewLaunchGeneration != 2 {
		t.Errorf("the reviewer's claim %s reads back from %+v; want its launch head %s at generation 2", reviewerClaim, claims, head)
	}
	t.Logf("wrote and left issue %s with pull request %s#%d at %s, the reviewer's and the owner's approvals there, outside pull request #%d at %s, its side reviewer and verdict, three writes of legion-verdict (the merge group's on %s), the reviewer's claim launched at %s, and %d outbox rows of %d kinds",
		key, repo, number, head, outside, outsideHead, group, head, len(payloads), len(written))
}
