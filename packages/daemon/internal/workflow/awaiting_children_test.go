package workflow

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The parent and the open child most of these tests stand up (dispatch://INFRA-3320 R9 to R12):
// LEGION-208, a root of its own at work on its open pull request #42, and LEGION-209, a Dispatch
// child of it that its architect filed and has not released, which is open (record.ChildOpen).
const (
	cwParent = "LEGION-208"
	cwChild  = "LEGION-209"
)

// cwAt is the test engine's clock (testEngine), which stamps every move these tests make.
var cwAt = time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)

// cwSeedParent records key as a root of its own standing in at, at generation 1, on its open pull
// request number at head, and answers the record. A parent held from a phase shows the status of
// the phase it was held from, as a hold leaves it.
func cwSeedParent(t *testing.T, pool *pgxpool.Pool, key string, at phase.Phase, number int, change ...func(*record.Issue)) record.Issue {
	t.Helper()
	status := PhaseStatus(at)
	if status == "" {
		status = "todo"
	}
	issue := record.Issue{Key: key, Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: key,
		Project: "LEGION", Title: "parent", Phase: at, Generation: 1, Status: status, Rank: "U", LastDispatchSeq: 1}
	for _, apply := range change {
		apply(&issue)
	}
	seedIssue(t, pool, issue)
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: key, Repo: "sjawhar/legion", Number: number,
		Branch: "legion/" + key, HeadSHA: "head", Failing: []string{}})
	return issue
}

// cwSeedChild writes what Dispatch shows of child: a child of parent at status, waiting on
// blockers, which the daemon has never recorded.
func cwSeedChild(t *testing.T, pool *pgxpool.Pool, child, parent, status string, blockers ...string) {
	t.Helper()
	seedRecord(t, pool, func(tx pgx.Tx) error {
		return record.NewStore().PutDispatchIssue(t.Context(), tx, record.DispatchIssueRow{
			Key: child, Project: "LEGION", Status: status, Parent: parent, Blockers: blockers, Seq: 1})
	})
}

// cwSeedWaiting records key already in awaiting_children, holding the start of start, with the
// forward merge owed, as the move leaves a parent.
func cwSeedWaiting(t *testing.T, pool *pgxpool.Pool, key string, start phase.Phase) record.Issue {
	t.Helper()
	waited := cwAt.Add(-time.Hour)
	return cwSeedParent(t, pool, key, phase.AwaitingChildren, 42, func(issue *record.Issue) {
		issue.Status = "in_progress"
		issue.ChildrenWait = &record.ChildrenWait{Start: start, Task: "Continue parent. Issue: " + key + ". Phase: " + string(start) + ".",
			Reason: "the tester completed testing, while these Dispatch children of " + key + " were open: " + cwChild + " (todo)", Since: waited}
		issue.ForwardMergeOwed, issue.ChildrenWaitedAt = true, &waited
	})
}

// cwTransition applies one transition of key on trigger toward target, with handoff the row the
// move carries and reason its words, as the handler that raises it would, inside a fact's own
// commit scope.
func cwTransition(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, trigger TriggerKind, target phase.Phase, handoff record.PhaseRow, reason string) {
	t.Helper()
	ctx, committed := intake.WithCommitHooks(t.Context())
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		issue, err := engine.store.Issue(ctx, tx, key)
		if err != nil || issue == nil {
			return fmt.Errorf("read %s: %v", key, err)
		}
		pr, err := engine.store.PullRequest(ctx, tx, key)
		if err != nil {
			return err
		}
		return engine.transition(ctx, tx, *issue, trigger, target, handoff, pr, reason)
	}); err != nil {
		t.Fatalf("the %s transition of %s: %v", trigger, key, err)
	}
	committed()
}

// cwNotices is every notice of kind queued for key, oldest first.
func cwNotices(t *testing.T, pool *pgxpool.Pool, key string, kind record.NoticeKind) []record.Notice {
	t.Helper()
	var notices []record.Notice
	for _, row := range issueOutboxOf(t, pool, key, string(record.OutboxKindNotice)) {
		var notice record.Notice
		if err := json.Unmarshal(row.payload, &notice); err != nil {
			t.Fatalf("decode notice row %d: %v", row.id, err)
		}
		if notice.Kind == kind {
			notices = append(notices, notice)
		}
	}
	return notices
}

// cwPhaseEnded is every phase-ended message queued for key, oldest first.
func cwPhaseEnded(t *testing.T, pool *pgxpool.Pool, key string) []record.PhaseEnded {
	t.Helper()
	var ended []record.PhaseEnded
	for _, row := range issueOutboxOf(t, pool, key, string(record.OutboxKindPhaseEnded)) {
		var message record.PhaseEnded
		if err := json.Unmarshal(row.payload, &message); err != nil {
			t.Fatalf("decode phase-ended row %d: %v", row.id, err)
		}
		ended = append(ended, message)
	}
	return ended
}

// cwVerdict is the verdict a tester's completion carries for trigger, and "" for every other.
func cwVerdict(trigger TriggerKind) string {
	switch trigger {
	case TriggerTesterPassed:
		return "pass"
	case TriggerTesterFailed:
		return "fail"
	}
	return ""
}

// Every start of planning, implementing, testing, reviewing or accepting of a parent standing in
// one of those phases is held while a Dispatch child of it is open, whatever the move is — a
// completion, a review, a worker's or the architect's move back, a conflicting head — and the
// parent waits in awaiting_children holding that start, with no worker started and the one
// awaiting-children notice in place of the phase-finished or checks-red the start would have sent.
func TestAnOpenChildHoldsEveryPhaseStartFromPlanningThroughAccepting(t *testing.T) {
	pool := migratedPool(t)
	engine := testEngine(config.DesignGateRootIssues, nil)
	held := map[phase.Phase]bool{}
	n := 0
	for _, row := range Table {
		if !holdsForChildren(row.From) || !holdsForChildren(row.To) {
			continue
		}
		n++
		key, child, number := fmt.Sprintf("LEGION-%d", 400+n), fmt.Sprintf("LEGION-%d", 700+n), 400+n
		held[row.From] = true
		t.Run(fmt.Sprintf("%s on %s to %s", row.From, row.Trigger, row.To), func(t *testing.T) {
			cwSeedParent(t, pool, key, row.From, number)
			cwSeedChild(t, pool, child, key, "todo")
			handoff := record.PhaseRow{Issue: key, Role: phase.RoleFor(row.From), Summary: "the work of " + string(row.From), Verdict: cwVerdict(row.Trigger)}

			cwTransition(t, pool, engine, key, row.Trigger, row.To, handoff, "the words of the move")

			got := storedIssue(t, pool, key)
			if got.Phase != phase.AwaitingChildren || got.ChildrenWait == nil || got.ChildrenWait.Start != row.To {
				t.Fatalf("%s after the move = phase %s, held start %+v; want awaiting_children holding the start of %s", key, got.Phase, got.ChildrenWait, row.To)
			}
			if !strings.Contains(got.ChildrenWait.Reason, child+" (todo)") {
				t.Fatalf("the held start's reason %q names no open child %s", got.ChildrenWait.Reason, child)
			}
			if starts := superviseRowsOf(t, pool, key, "start"); len(starts) != 0 {
				t.Fatalf("start rows = %+v, want none: no worker of a waiting parent starts", starts)
			}
			if kinds := noticeKinds(t, pool, key); len(kinds) != 1 || kinds[0] != AwaitingChildrenNoticeKind {
				t.Fatalf("notices = %v, want the one awaiting-children notice in place of the start's", kinds)
			}
		})
	}
	for _, at := range []phase.Phase{phase.Planning, phase.Implementing, phase.Testing, phase.Reviewing, phase.Accepting} {
		if !held[at] {
			t.Errorf("no start out of %s was tried, want every phase the rule covers", at)
		}
	}
}

// The rule reaches no start outside it, open child or not: the issue's first phase, which the
// admission line holds instead; a conflicting head's move back from retro or merging, past the
// daemon's acceptance; the architect's sign-off into retro; and any start of an issue whose pull
// request has merged.
func TestAnOpenChildHoldsNoStartOutsideTheRule(t *testing.T) {
	pool := migratedPool(t)
	engine := testEngine(config.DesignGateRootIssues, nil)
	for i, tc := range []struct {
		name    string
		from    phase.Phase
		trigger TriggerKind
		want    phase.Phase
		merged  bool
	}{
		{name: "the first phase", from: phase.Admitted, trigger: TriggerGateOpened, want: phase.Planning},
		{name: "a conflicting head in retro", from: phase.Retro, trigger: TriggerConflict, want: phase.Implementing},
		{name: "a conflicting head in merging", from: phase.Merging, trigger: TriggerConflict, want: phase.Implementing},
		{name: "the architect's sign-off", from: phase.Accepting, trigger: TriggerSignOff, want: phase.Retro},
		{name: "a merged pull request", from: phase.Testing, trigger: TriggerTesterPassed, want: phase.Reviewing, merged: true},
	} {
		key, child, number := fmt.Sprintf("LEGION-%d", 500+i), fmt.Sprintf("LEGION-%d", 800+i), 500+i
		t.Run(tc.name, func(t *testing.T) {
			cwSeedParent(t, pool, key, tc.from, number)
			if tc.merged {
				seedPR(t, pool, record.PullRequest{State: record.PullRequestMerged, Issue: key, Repo: "sjawhar/legion", Number: number,
					Branch: "legion/" + key, HeadSHA: "head", MergeSHA: "merge", Failing: []string{}})
			}
			cwSeedChild(t, pool, child, key, "todo")

			cwTransition(t, pool, engine, key, tc.trigger, tc.want, record.PhaseRow{Issue: key, Role: phase.RoleFor(tc.from), Verdict: cwVerdict(tc.trigger)}, "the head conflicts with main")

			if got := storedIssue(t, pool, key); got.Phase != tc.want || got.ChildrenWait != nil {
				t.Fatalf("%s after the move = phase %s, held start %+v; want %s and nothing held", key, got.Phase, got.ChildrenWait, tc.want)
			}
			if told := cwNotices(t, pool, key, AwaitingChildrenNoticeKind); len(told) != 0 {
				t.Fatalf("awaiting-children notices = %+v, want none outside the rule", told)
			}
		})
	}
}

// The notice that replaces the start's own names the parent, the start it holds, each open child
// with its Dispatch status, a child whose own blocked_by link names the parent above it, and what
// ends the wait, and carries what the notice it replaces carried: the worker's summary and the
// tester's verdict, the words of a move back, or the base a conflicting head merges forward.
func TestTheAwaitingChildrenNoticeNamesTheWaitAndCarriesWhatItReplaces(t *testing.T) {
	const conflict = "the head conflicts with main: GitHub runs no checks on it; merge main forward"
	for _, tc := range []struct {
		name    string
		from    phase.Phase
		trigger TriggerKind
		to      phase.Phase
		handoff record.PhaseRow
		reason  string
		carries []string
	}{
		{name: "a tester's pass", from: phase.Testing, trigger: TriggerTesterPassed, to: phase.Reviewing,
			handoff: record.PhaseRow{Role: claim.RoleTester, Summary: "every check passed", Verdict: "pass"},
			carries: []string{"holding the start of reviewing: the tester completed testing."}},
		{name: "a worker's move back", from: phase.Testing, trigger: TriggerBackward, to: phase.Implementing,
			handoff: record.PhaseRow{Role: claim.RoleTester}, reason: "the build is red",
			carries: []string{"holding the start of implementing: moved back to implementing by the tester: the build is red."}},
		{name: "a conflicting head", from: phase.Reviewing, trigger: TriggerConflict, to: phase.Implementing,
			reason: conflict, carries: []string{"holding the start of implementing: " + conflict + "."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			cwSeedParent(t, pool, cwParent, tc.from, 42)
			cwSeedChild(t, pool, cwChild, cwParent, "todo", cwParent)
			cwSeedChild(t, pool, "LEGION-210", cwParent, "backlog")
			tc.handoff.Issue = cwParent

			cwTransition(t, pool, testEngine(config.DesignGateRootIssues, nil), cwParent, tc.trigger, tc.to, tc.handoff, tc.reason)

			told := cwNotices(t, pool, cwParent, AwaitingChildrenNoticeKind)
			if len(told) != 1 {
				t.Fatalf("awaiting-children notices = %+v, want one", told)
			}
			notice := told[0]
			if notice.Role != claim.RoleArchitect || notice.Phase != tc.from || notice.Summary != tc.handoff.Summary || notice.Verdict != tc.handoff.Verdict {
				t.Fatalf("the notice = %+v, want it to the architect, of %s, carrying summary %q and verdict %q", notice, tc.from, tc.handoff.Summary, tc.handoff.Verdict)
			}
			for _, want := range append([]string{
				cwParent + " is in awaiting_children",
				"open child under it: " + cwChild + " (todo), LEGION-210 (backlog).",
				ChildCycleReason(cwChild, cwParent) + ".",
				"The wait ends when no child under " + cwParent + " is open",
				"goes back to the start it held by itself",
				"(" + waitingOnChildren + ")",
				parkRule,
			}, tc.carries...) {
				if !strings.Contains(notice.Reason, want) {
					t.Fatalf("the notice's reason %q does not say %q", notice.Reason, want)
				}
			}
			for _, replaced := range []record.NoticeKind{"phase-finished", "checks-red"} {
				if got := cwNotices(t, pool, cwParent, replaced); len(got) != 0 {
					t.Fatalf("%s notices = %+v, want none: the awaiting-children notice replaces it", replaced, got)
				}
			}
		})
	}
}

// A conflicting head held for the children tells the role whose phase it ended, on that role's own
// lane, that the issue waits on its children and no task follows (record.PhaseEnded into
// awaiting_children), its reason naming the open child; accepting's role is the architect, which
// the notice tells, so no message goes there.
func TestAConflictHeldForTheChildrenTellsTheEndedRoleNoTaskFollows(t *testing.T) {
	const conflict = "the head conflicts with main: GitHub runs no checks on it; merge main forward"
	for _, tc := range []struct {
		from  phase.Phase
		ended claim.Role
	}{
		{from: phase.Testing, ended: claim.RoleTester},
		{from: phase.Reviewing, ended: claim.RoleReviewer},
		{from: phase.Accepting},
	} {
		t.Run(string(tc.from), func(t *testing.T) {
			pool := migratedPool(t)
			cwSeedParent(t, pool, cwParent, tc.from, 42)
			cwSeedChild(t, pool, cwChild, cwParent, "todo")

			cwTransition(t, pool, testEngine(config.DesignGateRootIssues, nil), cwParent, TriggerConflict, phase.Implementing, record.PhaseRow{Issue: cwParent}, conflict)

			ended := cwPhaseEnded(t, pool, cwParent)
			if tc.ended == "" {
				if len(ended) != 0 {
					t.Fatalf("phase-ended messages = %+v, want none: the architect is told by its notice", ended)
				}
				return
			}
			if len(ended) != 1 || ended[0].Role != tc.ended || ended[0].From != tc.from || ended[0].To != phase.AwaitingChildren || ended[0].TaskFollows ||
				!strings.Contains(ended[0].Reason, conflict) || !strings.Contains(ended[0].Reason, cwChild+" (todo)") {
				t.Fatalf("phase-ended messages = %+v, want one to the %s out of %s into awaiting_children, no task following, naming the base and %s", ended, tc.ended, tc.from, cwChild)
			}
		})
	}
}

// The move writes what the return reads: the held start with the task the start would have given
// its worker, carrying no forward-merge request of its own, its reason and the moment the wait
// began; the forward merge owed; the moment of the move, which outlives the phase; and the status
// in_progress, written to Dispatch from the status the record held.
func TestTheMoveOwesAForwardMergeAndKeepsTheHeldStart(t *testing.T) {
	pool := migratedPool(t)
	// The parent still owed a forward merge from an earlier wait, which the held task must not
	// carry: the return adds the request to the implementing task it starts.
	cwSeedParent(t, pool, cwParent, phase.Implementing, 42, func(issue *record.Issue) { issue.ForwardMergeOwed = true })
	cwSeedChild(t, pool, cwChild, cwParent, "in_progress")

	cwTransition(t, pool, testEngine(config.DesignGateRootIssues, nil), cwParent, TriggerImplementationReady, phase.Testing,
		record.PhaseRow{Issue: cwParent, Role: claim.RoleImplementer, HandoffCommit: "head", Summary: "implemented"}, "")

	got := storedIssue(t, pool, cwParent)
	wait := got.ChildrenWait
	if got.Phase != phase.AwaitingChildren || wait == nil || wait.Start != phase.Testing || !wait.Since.Equal(cwAt) {
		t.Fatalf("the parent = phase %s, held start %+v; want awaiting_children holding testing since %s", got.Phase, wait, cwAt)
	}
	if !strings.Contains(wait.Task, "Phase: testing.") || !strings.Contains(wait.Task, "Handoff commit: head.") || strings.Contains(wait.Task, childrenMergedLine) {
		t.Fatalf("the held task = %q, want the testing task the start would have given, with no forward-merge request", wait.Task)
	}
	if want := "the implementer completed implementing, while these Dispatch children of " + cwParent + " were open: " + cwChild + " (in_progress)"; wait.Reason != want {
		t.Fatalf("the held reason = %q, want %q", wait.Reason, want)
	}
	if !got.ForwardMergeOwed || got.ChildrenWaitedAt == nil || !got.ChildrenWaitedAt.Equal(cwAt) || got.Status != "in_progress" {
		t.Fatalf("the parent = forward merge owed %t, waited at %v, status %s; want owed, %s and in_progress", got.ForwardMergeOwed, got.ChildrenWaitedAt, got.Status, cwAt)
	}
	// Implementing already shows in_progress, which the record holds, so no status is written.
	if writes := statusWrites(t, pool, cwParent); len(writes) != 0 {
		t.Fatalf("status writes = %+v, want none: the record already shows in_progress", writes)
	}
}

// A parent that leaves a phase showing another status is written in_progress (R11).
func TestTheMoveWritesInProgressOverAnotherStatus(t *testing.T) {
	pool := migratedPool(t)
	cwSeedParent(t, pool, cwParent, phase.Testing, 42)
	cwSeedChild(t, pool, cwChild, cwParent, "todo")

	cwTransition(t, pool, testEngine(config.DesignGateRootIssues, nil), cwParent, TriggerTesterPassed, phase.Reviewing,
		record.PhaseRow{Issue: cwParent, Role: claim.RoleTester, Verdict: "pass"}, "")

	if want := []record.StatusWrite{record.OrdinaryStatusWrite("in_progress", "testing", "")}; !slices.Equal(statusWrites(t, pool, cwParent), want) {
		t.Fatalf("status writes = %+v, want %+v", statusWrites(t, pool, cwParent), want)
	}
}

// sameChildrenWait says whether a and b are one held start, its moment compared as an instant.
func sameChildrenWait(a, b *record.ChildrenWait) bool {
	return a != nil && b != nil && a.Start == b.Start && a.Task == b.Task && a.Reason == b.Reason && a.Since.Equal(b.Since)
}

// A second move of a parent already waiting writes nothing and sends nothing again: the admission
// pass reads every parent on every pass, and the architect was told once.
func TestASecondMoveOfAWaitingParentSendsNothingAgain(t *testing.T) {
	pool := migratedPool(t)
	engine := testEngine(config.DesignGateRootIssues, nil)
	cwSeedParent(t, pool, cwParent, phase.Testing, 42)
	cwSeedChild(t, pool, cwChild, cwParent, "todo")
	cwTransition(t, pool, engine, cwParent, TriggerTesterPassed, phase.Reviewing, record.PhaseRow{Issue: cwParent, Role: claim.RoleTester, Verdict: "pass"}, "")
	first := storedIssue(t, pool, cwParent)
	rows := outboxRowCount(t, pool)

	ctx, committed := intake.WithCommitHooks(t.Context())
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return engine.AwaitChildren(ctx, tx, first, ChildrenHold{Start: phase.Implementing, Task: "another task", Cause: "a later pass",
			Open: []record.OpenChild{{Key: cwChild, Status: "todo"}}, Ended: claim.RoleTester})
	}); err != nil {
		t.Fatalf("the second move: %v", err)
	}
	committed()

	if got := outboxRowCount(t, pool); got != rows {
		t.Fatalf("outbox rows after the second move = %d, want the %d the first left", got, rows)
	}
	if told := cwNotices(t, pool, cwParent, AwaitingChildrenNoticeKind); len(told) != 1 {
		t.Fatalf("awaiting-children notices = %+v, want still one", told)
	}
	if again := storedIssue(t, pool, cwParent); !sameChildrenWait(again.ChildrenWait, first.ChildrenWait) {
		t.Fatalf("the held start after the second move = %+v, want the first's %+v", again.ChildrenWait, first.ChildrenWait)
	}
}

// outboxRowCount is how many rows the outbox holds.
func outboxRowCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var rows int
	if err := pool.QueryRow(t.Context(), "select count(*) from outbox").Scan(&rows); err != nil {
		t.Fatalf("count the outbox: %v", err)
	}
	return rows
}

// The counts of a held move run as they run today (R10): a move back counts the implementer's pass
// and its pair, while no hook of the deployment fires, since no role of the parent has a phase to
// hear it; the same move with no child open fires the hook. A request for changes counts its
// round, and the cap's message names the parent as waiting on its children.
func TestAHeldMoveCountsAsTodayAndFiresNoHook(t *testing.T) {
	hooked := New(record.NewStore(), Config{
		Project: "LEGION", Steps: config.DesignGateRootIssues.Steps(), ReviewRoundCap: 3, Linger: time.Hour, ProductionCheckRole: claim.RoleTester,
		ReviewAppLogin: testReviewApp, Repositories: testRepositories, Clock: func() time.Time { return cwAt },
		Hooks: []Hook{{Name: "back", On: HookEventWorkBack, Every: 1, Roles: []claim.Role{claim.RoleImplementer}, Prompt: "Read the failing check first."}},
	}, nil)
	for _, tc := range []struct {
		name  string
		child bool
		hooks int
	}{
		{name: "an open child", child: true},
		{name: "no open child", hooks: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			cwSeedParent(t, pool, cwParent, phase.Testing, 42)
			if tc.child {
				cwSeedChild(t, pool, cwChild, cwParent, "todo")
			}

			cwTransition(t, pool, hooked, cwParent, TriggerTesterFailed, phase.Implementing, record.PhaseRow{Issue: cwParent, Role: claim.RoleTester, Verdict: "fail"}, "")

			implementer, err := testPhaseRow(t, pool, claim.RoleImplementer)
			if err != nil {
				t.Fatal(err)
			}
			if implementer.Pass != 1 || implementer.BackwardMoves["testing-implementing"] != 1 {
				t.Fatalf("the implementer's counts = pass %d, moves %v; want one pass and one testing-implementing", implementer.Pass, implementer.BackwardMoves)
			}
			if prompts := issueOutboxOf(t, pool, cwParent, string(record.OutboxKindHookPrompt)); len(prompts) != tc.hooks {
				t.Fatalf("hook prompts = %d, want %d", len(prompts), tc.hooks)
			}
		})
	}

	t.Run("a request for changes at the cap", func(t *testing.T) {
		pool := migratedPool(t)
		cwSeedParent(t, pool, cwParent, phase.Reviewing, 42)
		cwSeedChild(t, pool, cwChild, cwParent, "todo")
		seedPhase(t, pool, record.PhaseRow{Issue: cwParent, Role: claim.RoleImplementer, Rounds: 2})

		cwTransition(t, pool, testEngine(config.DesignGateRootIssues, nil), cwParent, TriggerReviewRejected, "", record.PhaseRow{Issue: cwParent, Role: claim.RoleReviewer}, "rename the widget")

		if got := storedIssue(t, pool, cwParent); got.Phase != phase.AwaitingChildren {
			t.Fatalf("the parent = %s, want awaiting_children", got.Phase)
		}
		implementer, err := testPhaseRow(t, pool, claim.RoleImplementer)
		if err != nil {
			t.Fatal(err)
		}
		if implementer.Rounds != 3 {
			t.Fatalf("rounds = %d, want the request for changes counted", implementer.Rounds)
		}
		want := "Issue reached review_round_cap=3: 3 rounds counted, 0 conflict rounds not counted. " + cwParent +
			" waits on its open Dispatch children in awaiting_children, so no implementing round started: " + cwChild + " (todo)."
		if bodies := messageBodies(t, pool); len(bodies) != 1 || bodies[0] != want {
			t.Fatalf("messages = %q, want the cap's %q", bodies, want)
		}
		if told := cwNotices(t, pool, cwParent, "pr-blocked"); len(told) != 1 || told[0].Reason != want {
			t.Fatalf("pr-blocked notices = %+v, want one carrying %q", told, want)
		}
		if held := storedIssue(t, pool, cwParent).ChildrenWait; held == nil || !strings.Contains(held.Reason, "a review asked for changes: rename the widget") {
			t.Fatalf("the held start = %+v, want its reason to carry the review's words", held)
		}
	})
}

// testPhaseRow is LEGION-208's phases row of role as the record holds it.
func testPhaseRow(t *testing.T, pool *pgxpool.Pool, role claim.Role) (record.PhaseRow, error) {
	t.Helper()
	var row record.PhaseRow
	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		rows, err := record.NewStore().Phases(t.Context(), tx, cwParent)
		row = record.PhaseRowOf(rows, role)
		return err
	})
	return row, err
}

// The move cancels every open backstop of the parent's pod in its own transaction, through the
// engine's pod tables; an engine built without them moves the parent all the same and cancels
// nothing.
func TestTheMoveCancelsThePodsOpenBackstops(t *testing.T) {
	openBackstops := func(t *testing.T, pool *pgxpool.Pool) int {
		t.Helper()
		var open int
		if err := pool.QueryRow(t.Context(), "select count(*) from pod_wake_backstops where issue = $1 and not fired", cwParent).Scan(&open); err != nil {
			t.Fatalf("count the open backstops: %v", err)
		}
		return open
	}
	back := intake.BackwardMove{Issue: cwParent, Requester: claim.RoleImplementer, To: phase.Planning, Reason: "the plan missed a case"}

	t.Run("with the pod tables", func(t *testing.T) {
		rig := newWakeRig(t)
		cwSeedChild(t, rig.pool, cwChild, cwParent, "todo")
		armBackstop(t, rig.pool, claim.RoleImplementer, wakeAt.Add(time.Hour))
		armBackstop(t, rig.pool, claim.RoleTester, wakeAt.Add(2*time.Hour))

		rig.apply("back", back)

		if got := storedIssue(t, rig.pool, cwParent).Phase; got != phase.AwaitingChildren {
			t.Fatalf("the parent = %s, want awaiting_children", got)
		}
		if open := openBackstops(t, rig.pool); open != 0 {
			t.Fatalf("open backstops after the move = %d, want none", open)
		}
	})

	t.Run("without them", func(t *testing.T) {
		pool := migratedPool(t)
		cwSeedParent(t, pool, cwParent, phase.Implementing, 42)
		cwSeedChild(t, pool, cwChild, cwParent, "todo")
		armBackstop(t, pool, claim.RoleImplementer, wakeAt.Add(time.Hour))

		applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))("back", back)

		if got := storedIssue(t, pool, cwParent).Phase; got != phase.AwaitingChildren {
			t.Fatalf("the parent = %s, want awaiting_children", got)
		}
		if open := openBackstops(t, pool); open != 1 {
			t.Fatalf("open backstops after the move = %d, want the one armed: an engine with no pod tables cancels nothing", open)
		}
	})
}

// An implementing task asks for the forward merge while the parent owes it and its pull request
// has not merged, and no other task does.
func TestAnImplementingTaskAsksForTheForwardMergeWhileItIsOwed(t *testing.T) {
	open := &record.PullRequest{State: record.PullRequestOpen, Number: 42}
	merged := &record.PullRequest{State: record.PullRequestMerged, Number: 42}
	owed := record.Issue{Key: cwParent, Title: "parent", DefaultBranch: "main", Phase: phase.Implementing, ForwardMergeOwed: true}
	request := cwParent + " waited on its children: before anything else, merge the default branch main into the branch, push, and complete with a new handoff record, however the children left; end your forward-merge comment with the line `children merged: full test round`."
	for _, tc := range []struct {
		name  string
		issue func(record.Issue) record.Issue
		pr    *record.PullRequest
		asks  bool
	}{
		{name: "implementing, owed, open", issue: func(i record.Issue) record.Issue { return i }, pr: open, asks: true},
		{name: "implementing, owed, no pull request yet", issue: func(i record.Issue) record.Issue { return i }, asks: true},
		{name: "implementing, owed, merged", issue: func(i record.Issue) record.Issue { return i }, pr: merged},
		{name: "implementing, not owed", issue: func(i record.Issue) record.Issue { i.ForwardMergeOwed = false; return i }, pr: open},
		{name: "planning, owed", issue: func(i record.Issue) record.Issue { i.Phase = phase.Planning; return i }, pr: open},
		{name: "testing, owed", issue: func(i record.Issue) record.Issue { i.Phase = phase.Testing; return i }, pr: open},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := tc.issue(owed)
			got := task(issue, record.PhaseRow{}, tc.pr, "")
			if asks := strings.HasSuffix(got, " "+request); asks != tc.asks {
				t.Fatalf("the task %q asks for the forward merge = %t, want %t", got, asks, tc.asks)
			}
		})
	}
}

// The transition that writes an implementing task carrying the forward merge clears the debt in
// the same write; a transition into another phase leaves it owed for the implementing round to
// come.
func TestTheTransitionThatAsksForTheForwardMergeClearsIt(t *testing.T) {
	owed := func(issue *record.Issue) { issue.ForwardMergeOwed = true }

	t.Run("a move into implementing", func(t *testing.T) {
		pool := migratedPool(t)
		cwSeedParent(t, pool, cwParent, phase.Testing, 42, owed)

		cwTransition(t, pool, testEngine(config.DesignGateRootIssues, nil), cwParent, TriggerTesterFailed, phase.Implementing,
			record.PhaseRow{Issue: cwParent, Role: claim.RoleTester, Verdict: "fail"}, "")

		if got := storedIssue(t, pool, cwParent); got.Phase != phase.Implementing || got.ForwardMergeOwed {
			t.Fatalf("the parent = phase %s, forward merge owed %t; want implementing with the debt cleared", got.Phase, got.ForwardMergeOwed)
		}
		if task := startTask(t, pool, claim.RoleImplementer); !strings.Contains(task, "`"+childrenMergedLine+"`") {
			t.Fatalf("the implementer's task = %q, want the forward-merge request", task)
		}
	})

	t.Run("a move into testing", func(t *testing.T) {
		pool := migratedPool(t)
		cwSeedParent(t, pool, cwParent, phase.Implementing, 42, owed)

		cwTransition(t, pool, testEngine(config.DesignGateRootIssues, nil), cwParent, TriggerImplementationReady, phase.Testing,
			record.PhaseRow{Issue: cwParent, Role: claim.RoleImplementer, HandoffCommit: "head"}, "")

		if got := storedIssue(t, pool, cwParent); got.Phase != phase.Testing || !got.ForwardMergeOwed {
			t.Fatalf("the parent = phase %s, forward merge owed %t; want testing with the merge still owed", got.Phase, got.ForwardMergeOwed)
		}
		if task := startTask(t, pool, claim.RoleTester); strings.Contains(task, childrenMergedLine) {
			t.Fatalf("the tester's task = %q, want no forward-merge request", task)
		}
	})

	t.Run("a retry of implementing", func(t *testing.T) {
		pool := migratedPool(t)
		cwSeedParent(t, pool, cwParent, phase.Held, 42, owed, func(issue *record.Issue) {
			issue.Hold, issue.Status = &record.Hold{From: phase.Implementing}, "in_progress"
		})

		applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))("retry", intake.RetryOrEscalate{Issue: cwParent, Decision: intake.RetryDecision})

		if got := storedIssue(t, pool, cwParent); got.Phase != phase.Implementing || got.ForwardMergeOwed {
			t.Fatalf("the parent = phase %s, forward merge owed %t; want implementing with the debt cleared", got.Phase, got.ForwardMergeOwed)
		}
		if task := startTask(t, pool, claim.RoleImplementer); !strings.Contains(task, "`"+childrenMergedLine+"`") {
			t.Fatalf("the retried implementer's task = %q, want the forward-merge request", task)
		}
	})
}

// Facts that would move a phase start nothing while the parent waits (no row of the table leaves
// awaiting_children): a conflicting head moves nothing, and a merge is recorded on the pull
// request, ends the forward merge the parent owed, and leaves the held start for the return.
func TestFactsDuringTheWaitStartNothing(t *testing.T) {
	pool := migratedPool(t)
	waiting := cwSeedWaiting(t, pool, cwParent, phase.Testing)
	cwSeedChild(t, pool, cwChild, cwParent, "todo")
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))

	apply("conflict", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: "head",
		Mergeable: record.MergeabilityConflicting, MergeState: record.MergeStateDirty})
	if got := storedIssue(t, pool, cwParent); got.Phase != phase.AwaitingChildren || !got.ForwardMergeOwed {
		t.Fatalf("the parent after a conflict = phase %s, forward merge owed %t; want it waiting still, the merge owed", got.Phase, got.ForwardMergeOwed)
	}

	apply("merged", intake.PullRequestMerged{Repo: "sjawhar/legion", Number: 42, MergeSHA: "merge"})
	got := storedIssue(t, pool, cwParent)
	if got.Phase != phase.AwaitingChildren || got.ForwardMergeOwed || !sameChildrenWait(got.ChildrenWait, waiting.ChildrenWait) {
		t.Fatalf("the parent after the merge = phase %s, forward merge owed %t, held start %+v; want it waiting still, the merge no longer owed and the held start as it was",
			got.Phase, got.ForwardMergeOwed, got.ChildrenWait)
	}
	if starts := superviseRowsOf(t, pool, cwParent, "start"); len(starts) != 0 {
		t.Fatalf("start rows = %+v, want none", starts)
	}
	if ended := cwPhaseEnded(t, pool, cwParent); len(ended) != 0 {
		t.Fatalf("phase-ended messages = %+v, want none", ended)
	}
}

// Each release of a hold is a phase start (R9): the architect's retry, the operator's restart and
// a todo on a held root each move a parent with an open child to awaiting_children, holding the
// retried phase with the retry's reason, and start nobody; the todo's release writes in_progress
// over the todo Dispatch shows. An escalation stays as it is today.
func TestAReleaseOfAHoldWithAnOpenChildWaitsOnTheChildren(t *testing.T) {
	heldFrom := func(from phase.Phase) func(*record.Issue) {
		return func(issue *record.Issue) {
			issue.Hold, issue.Status, issue.DispatchStatus, issue.LastDispatchSeq = &record.Hold{From: from}, PhaseStatus(from), PhaseStatus(from), 4
			issue.HandedOver = true
		}
	}
	for _, tc := range []struct {
		name   string
		at     phase.Phase
		change func(*record.Issue)
		fact   intake.Fact
		start  phase.Phase
		reason string
		status []record.StatusWrite
	}{
		{name: "the architect's retry", at: phase.Held, change: heldFrom(phase.Testing),
			fact:  intake.RetryOrEscalate{Issue: cwParent, Decision: intake.RetryDecision, Reason: "the runner is back"},
			start: phase.Testing, reason: "the runner is back",
			status: []record.StatusWrite{record.OrdinaryStatusWrite("in_progress", "testing", "")}},
		{name: "the operator's restart", at: phase.Reviewing, change: func(*record.Issue) {},
			fact:  intake.RestartPhase{Issue: cwParent, Role: claim.RoleReviewer},
			start: phase.Reviewing, reason: "the operator retried this phase's worker",
			status: []record.StatusWrite{record.OrdinaryStatusWrite("in_progress", "needs_review", "")}},
		{name: "a todo on a held root", at: phase.Held, change: heldFrom(phase.Implementing),
			fact: intake.DispatchIssue{Key: cwParent, ResolvedRepo: "sjawhar/legion", Seq: 5, Type: "issue.updated", Status: "todo",
				Title: "parent", Rank: "U", HandedOver: true},
			start: phase.Implementing, reason: releaseReason,
			status: []record.StatusWrite{record.OrdinaryStatusWrite("in_progress", "todo", "")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			cwSeedParent(t, pool, cwParent, tc.at, 42, tc.change)
			cwSeedChild(t, pool, cwChild, cwParent, "todo")

			if result := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))("release", tc.fact); result.Refusal != nil {
				t.Fatalf("the release was refused: %+v", result.Refusal)
			}

			got := storedIssue(t, pool, cwParent)
			if got.Phase != phase.AwaitingChildren || got.Hold != nil || got.ChildrenWait == nil || got.ChildrenWait.Start != tc.start ||
				!strings.HasPrefix(got.ChildrenWait.Reason, tc.reason+", while these Dispatch children of "+cwParent+" were open: "+cwChild+" (todo)") {
				t.Fatalf("the parent = phase %s, hold %+v, held start %+v; want awaiting_children holding %s for %q", got.Phase, got.Hold, got.ChildrenWait, tc.start, tc.reason)
			}
			if !strings.Contains(got.ChildrenWait.Task, "Phase: "+string(tc.start)+".") || !strings.Contains(got.ChildrenWait.Task, "Reason: "+tc.reason+".") {
				t.Fatalf("the held task = %q, want the retry's task of %s", got.ChildrenWait.Task, tc.start)
			}
			if starts := superviseRowsOf(t, pool, cwParent, "start"); len(starts) != 0 {
				t.Fatalf("start rows = %+v, want none", starts)
			}
			if told := cwNotices(t, pool, cwParent, AwaitingChildrenNoticeKind); len(told) != 1 {
				t.Fatalf("awaiting-children notices = %+v, want one", told)
			}
			if got := statusWrites(t, pool, cwParent); !slices.Equal(got, tc.status) {
				t.Fatalf("status writes = %+v, want %+v", got, tc.status)
			}
		})
	}

	t.Run("an escalation", func(t *testing.T) {
		pool := migratedPool(t)
		cwSeedParent(t, pool, cwParent, phase.Held, 42, heldFrom(phase.Testing))
		cwSeedChild(t, pool, cwChild, cwParent, "todo")

		applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))("escalate", intake.RetryOrEscalate{Issue: cwParent, Decision: intake.EscalateDecision, Reason: "a person must look"})

		if got := storedIssue(t, pool, cwParent); got.Phase != phase.Held || got.Hold == nil || got.Hold.Reason != record.HoldEscalated || got.ChildrenWait != nil {
			t.Fatalf("the parent = phase %s, hold %+v, held start %+v; want it held and escalated, as today", got.Phase, got.Hold, got.ChildrenWait)
		}
		if told := cwNotices(t, pool, cwParent, AwaitingChildrenNoticeKind); len(told) != 0 {
			t.Fatalf("awaiting-children notices = %+v, want none", told)
		}
	})
}

// request_backward_move — the architect's and a worker's — and sign_off on a parent in
// awaiting_children are refused WAITING_ON_CHILDREN, naming the open children and the start the
// parent holds, and change nothing. A parent whose last open child has gone since the last pass is
// told that the next pass brings it back.
func TestABackwardMoveAndASignOffOfAWaitingParentAreRefused(t *testing.T) {
	pool := migratedPool(t)
	cwSeedWaiting(t, pool, cwParent, phase.Testing)
	cwSeedChild(t, pool, cwChild, cwParent, "in_progress")
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))

	for _, tc := range []struct {
		name string
		fact intake.Fact
		act  string
	}{
		{name: "the architect's move back", fact: intake.BackwardMove{Issue: cwParent, Requester: claim.RoleArchitect, To: phase.Implementing, Reason: "redo it"}, act: "request_backward_move"},
		{name: "a worker's move back", fact: intake.BackwardMove{Issue: cwParent, Requester: claim.RoleTester, To: phase.Implementing, Reason: "redo it"}, act: "request_backward_move"},
		{name: "the sign-off", fact: intake.SignOff{Issue: cwParent}, act: "sign_off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := refusedWith(t, apply(tc.name, tc.fact), waitingOnChildren, cwChild+" (in_progress)", "holds, testing,")
			if want := cwParent + " waits in awaiting_children on these open Dispatch children of it: " + cwChild + " (in_progress). " +
				"It stands in no phase that " + tc.act + " acts on, so this " + tc.act + " changed nothing; " + cwParent + " goes back to the start it holds, testing, by itself once no child under it is open. " +
				"Act on the children: release one that should run, or move one out from under " + cwParent + " to an issue outside its tree or to no parent; close, icebox or park one " + parkRule + ", and cite that reason in a comment on the child"; message != want {
				t.Fatalf("the refusal = %q, want %q", message, want)
			}
		})
	}
	if got := storedIssue(t, pool, cwParent).Phase; got != phase.AwaitingChildren {
		t.Fatalf("the parent = %s, want it waiting still", got)
	}
	assertOutboxKinds(t, pool, []string{})

	mirrorChild(t, pool, cwChild, cwParent, "done", 2)
	refusedWith(t, apply("sign-off-after", intake.SignOff{Issue: cwParent}), waitingOnChildren,
		"no Dispatch child of it is open now: the next admission pass takes it back to that start by itself")
}

// A completion of a phase the wait stopped is refused while the parent waits and after it comes
// back: a role whose phase entry was raised before the parent's last move into awaiting_children
// reports work the wait ended, which the return starts again on an entry of its own; a completion
// of an entry raised after that move is recorded as a left phase's always is.
func TestACompletionOfAPhaseTheWaitStoppedIsRefused(t *testing.T) {
	waited := cwAt.Add(-time.Hour)
	completion := intake.HandoffComplete{Generation: 1, Issue: cwParent, Role: claim.RoleTester, Claim: "test-claim", Verdict: "pass",
		Summary: "tested", HandoffRecord: "test-1", HandoffVersion: 1}
	for _, tc := range []struct {
		name    string
		at      phase.Phase
		raised  time.Time
		refused bool
	}{
		{name: "during the wait", at: phase.AwaitingChildren, raised: waited.Add(-time.Hour), refused: true},
		{name: "after the return, an entry from before the wait", at: phase.Implementing, raised: waited.Add(-time.Hour), refused: true},
		{name: "after the return, an entry from after the wait", at: phase.Implementing, raised: waited.Add(time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			cwSeedParent(t, pool, cwParent, tc.at, 42, func(issue *record.Issue) {
				issue.Status, issue.ForwardMergeOwed, issue.ChildrenWaitedAt = "in_progress", true, &waited
				if tc.at == phase.AwaitingChildren {
					issue.ChildrenWait = &record.ChildrenWait{Start: phase.Testing, Task: "test it", Reason: "a child is open", Since: waited}
				}
			})
			seedPhase(t, pool, record.PhaseRow{Issue: cwParent, Role: claim.RoleTester, Claim: "test-claim", Entry: 1, EntryTask: "test it", EntryRaisedAt: tc.raised})
			if _, err := pool.Exec(t.Context(), `insert into claims (token, project, tree, issue, role, generation, session, session_file,
				state, launch_failures, prompt_failures, prompt_retires, uncertain_streak)
				values ('test-claim', 'LEGION', $1, $1, 'tester', 1, '', '', 'working', 0, 0, 0, 0)`, cwParent); err != nil {
				t.Fatalf("seed the tester's claim: %v", err)
			}

			result := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))("late", completion)

			if refused := refusalCode(result) == "HANDOFF_NOT_CURRENT_PHASE"; refused != tc.refused {
				t.Fatalf("the late completion = %+v, want refused %t", result.Refusal, tc.refused)
			}
			row, err := testPhaseRow(t, pool, claim.RoleTester)
			if err != nil {
				t.Fatal(err)
			}
			if recorded := row.HandoffRecord == "test-1"; recorded == tc.refused {
				t.Fatalf("the tester's row = %+v, want its completion recorded %t", row, !tc.refused)
			}
		})
	}
}
