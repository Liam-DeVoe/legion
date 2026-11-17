package workflow

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// reviewEntryPartOf is the sentence a reviewer's task in reviewing names the entry of its round
// with, " Review entry: <n>.", written out here rather than through reviewEntry.part, so the
// tests read the sentence and not the code that writes it.
func reviewEntryPartOf(entry int64) string {
	return " " + ReviewEntryLabel + ": " + strconv.FormatInt(entry, 10) + "."
}

// entryNamedIn is the entry the reviewer's task names in its last sentence, read as an agent reads
// it: the number after ReviewEntryLabel, failing the test when the task does not end naming one.
func entryNamedIn(t *testing.T, task string) int64 {
	t.Helper()
	label := " " + ReviewEntryLabel + ": "
	at := strings.LastIndex(task, label)
	if at < 0 || !strings.HasSuffix(task, ".") {
		t.Fatalf("the task %q does not end naming its review entry", task)
	}
	entry, err := strconv.ParseInt(strings.TrimSuffix(task[at+len(label):], "."), 10, 64)
	if err != nil {
		t.Fatalf("the task %q does not end naming its review entry: %v", task, err)
	}
	return entry
}

// deliveredTask is the task of the one start of LEGION-208's reviewer the outbox holds, the task
// the reviewer is DELIVERED, failing the test unless that start hands the reviewer the entry the
// reviewer's row holds (or, for a resume level with its entry, no number at all).
func deliveredTask(t *testing.T, pool *pgxpool.Pool, path string) string {
	t.Helper()
	start, row := onlyReviewerStart(t, pool), reviewerRow(t, pool)
	if start.Entry != 0 && start.Entry != row.Entry {
		t.Fatalf("%s: the start hands the reviewer entry %d, want the row's %d", path, start.Entry, row.Entry)
	}
	return start.Task
}

// Each task the reviewer is given in reviewing ends naming the entry of its round
// (ReviewEntryLabel): the number the reviewer's row holds at that moment (record.PhaseRow.Entry),
// in each round, the first one too, while the row holds the task of an entry. The reviewer writes
// it into the footer of each review it submits for that task, and only a review that names the
// entry that stands moves the commit the reviewer read (review). One function writes the sentence
// (reviewEntry.part), and the seven paths that give the reviewer a task in reviewing reach it:
//
//   - the two that raise the entry store the text they send (enter): a transition's start and the
//     promotion's start;
//   - the delivery of an owed entry sends that stored text again, so it names the same entry;
//   - the four that give the reviewer a task at the entry its row already holds name that entry:
//     the retry of a held phase (restart), and the resume task of a ready worker's catch-up, the
//     silence repair and the boot's resume (phaseResumeTask).
//
// The eighth case is the resume task made because the stored text is empty (a clear emptied it,
// and no entry was raised since): it names no entry, since no review of that entry moves the
// commit. A task of any other role names no entry either.
func TestEachTaskOfTheReviewerInReviewingEndsNamingItsEntry(t *testing.T) {
	resume := "Continue root. Issue: LEGION-208. Phase: reviewing." + reviewedPartOf(headOne) + " Resume the existing phase work."
	for _, tc := range []struct {
		name string
		// walk takes LEGION-208 to the path's start, with only that start in the outbox, and returns
		// the pool and the start's task as stored and as delivered must read before the sentence.
		walk func(t *testing.T) (*pgxpool.Pool, string)
		// raises is whether the path raises the entry and stores the text it sends (enter), so the
		// row's stored task is the delivered one word for word.
		raises bool
		// none is the eighth case: the task names no entry.
		none bool
	}{
		{
			name: "a transition's start of the first round", raises: true,
			walk: func(t *testing.T) (*pgxpool.Pool, string) {
				pool, _, _ := reviewRoundOne(t)
				return pool, "Continue root. Issue: LEGION-208. Phase: reviewing. Handoff: test version 1 (dispatch handoff-read --issue LEGION-208 --phase test --version 1)." +
					" Tester's verdict: pass. Pull request: #42."
			},
		},
		{
			name: "a transition's start of a later round", raises: true,
			walk: func(t *testing.T) (*pgxpool.Pool, string) {
				pool, _, apply := reviewRoundOne(t)
				outboxRan(t, pool)
				reviewedTwice(t, apply, pool)
				for _, role := range []claim.Role{claim.RoleImplementer, claim.RoleTester} {
					if task := roleRow(t, pool, role).EntryTask; task == "" || strings.Contains(task, ReviewEntryLabel) {
						t.Fatalf("the %s's row holds the task %q, want round 2's task with no review entry: only the reviewer is told one", role, task)
					}
				}
				return pool, "Continue root. Issue: LEGION-208. Phase: reviewing. Handoff: test version 2 (dispatch handoff-read --issue LEGION-208 --phase test --version 2)." +
					" Tester's verdict: pass. Pull request: #42." + reviewedPartOf(headOne)
			},
		},
		{
			name: "the promotion's start", raises: true,
			walk: func(t *testing.T) (*pgxpool.Pool, string) {
				pool, engine, _ := reviewedChild(t)
				readmitsTheTree(t, pool, engine, engine.now().Add(time.Hour))
				outboxRan(t, pool)
				promotesTheReviewer(t, pool, engine, engine.now().Add(time.Hour))
				return pool, "Continue child. Issue: LEGION-208. Phase: reviewing. Resume the existing phase work."
			},
		},
		{
			name: "the delivery of an owed entry", raises: true,
			walk: func(t *testing.T) (*pgxpool.Pool, string) {
				pool, engine, _ := reviewRoundTwo(t)
				row, issue := reviewerRow(t, pool), issueRecord(t, pool, "LEGION-208")
				outboxRan(t, pool)
				applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
					return engine.SendOwedEntry(ctx, tx, *issue, claim.RoleReviewer, row)
				})
				return pool, strings.TrimSuffix(row.EntryTask, reviewEntryPartOf(row.Entry))
			},
		},
		{
			name: "the retry of a held reviewing phase",
			walk: func(t *testing.T) (*pgxpool.Pool, string) {
				pool, _, apply := reviewRoundTwo(t)
				held := issueRecord(t, pool, "LEGION-208")
				held.Phase, held.Hold = phase.Held, &record.Hold{From: phase.Reviewing}
				seedIssue(t, pool, *held)
				outboxRan(t, pool)
				apply("retry", intake.RetryOrEscalate{Issue: "LEGION-208", Decision: intake.RetryDecision, Reason: "the claim failed"})
				return pool, "Continue root. Issue: LEGION-208. Phase: reviewing. Pull request: #42." + reviewedPartOf(headOne) + " Reason: the claim failed."
			},
		},
		{
			name: "a ready worker's catch-up",
			walk: func(t *testing.T) (*pgxpool.Pool, string) {
				pool, engine, _ := reviewRoundTwo(t)
				row := reviewerRow(t, pool)
				outboxRan(t, pool)
				token := seedPhaseWorker(t, pool, "LEGION-208", claim.RoleReviewer, "")
				if _, err := pool.Exec(context.Background(), "update claims set given_entry = $1 where token = $2", row.Entry, string(token)); err != nil {
					t.Fatalf("give the reviewer's claim its entry: %v", err)
				}
				if _, err := intake.ApplyFact(context.Background(), pool, "supervise", "supervise:reviewer:2:ready",
					intake.ClaimReady{Issue: "LEGION-208", Role: claim.RoleReviewer, Launch: 2}, engine, admissionStub{}); err != nil {
					t.Fatalf("apply the ready: %v", err)
				}
				return pool, resume
			},
		},
		{
			name: "the silence repair",
			walk: func(t *testing.T) (*pgxpool.Pool, string) {
				pool, engine, _ := reviewRoundTwo(t)
				row, issue := reviewerRow(t, pool), issueRecord(t, pool, "LEGION-208")
				outboxRan(t, pool)
				applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
					return engine.repairSilence(ctx, tx, *issue, *issue, record.RoleRun{Claim: &record.RoleClaim{GivenEntry: row.Entry}}, row,
						intake.WorkerSilent{Issue: "LEGION-208", Role: claim.RoleReviewer, Generation: 1, LastStep: reviewTestedAt, Bound: time.Hour},
						reviewTestedAt.Add(2*time.Hour))
				})
				return pool, resume
			},
		},
		{
			name: "the boot's resume",
			walk: func(t *testing.T) (*pgxpool.Pool, string) {
				pool, engine, _ := reviewRoundTwo(t)
				bootResumesTheReviewer(t, pool, engine)
				return pool, resume
			},
		},
		{
			name: "the resume of an entry whose stored task is empty", none: true,
			walk: func(t *testing.T) (*pgxpool.Pool, string) {
				pool, engine, _ := reviewedChild(t)
				readmitsTheTree(t, pool, engine, engine.now().Add(time.Hour))
				row, issue := reviewerRow(t, pool), issueRecord(t, pool, "LEGION-208")
				if row.EntryTask != "" {
					t.Fatalf("after the re-admission the reviewer's row stores the task %q, want none", row.EntryTask)
				}
				outboxRan(t, pool)
				applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
					return engine.SendOwedEntry(ctx, tx, *issue, claim.RoleReviewer, row)
				})
				return pool, "Continue child. Issue: LEGION-208. Phase: reviewing. Resume the existing phase work."
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, before := tc.walk(t)
			row := reviewerRow(t, pool)
			stored := row.EntryTask
			delivered := deliveredTask(t, pool, tc.name)
			want := before + reviewEntryPartOf(row.Entry)
			if tc.none {
				want = before
				if strings.Contains(delivered, ReviewEntryLabel) {
					t.Fatalf("the reviewer is delivered the task %q, want no %q in it: its row holds no entry's task", delivered, ReviewEntryLabel)
				}
			}
			if delivered != want {
				t.Fatalf("the reviewer is delivered the task %q, want %q: it ends naming entry %d, the row's", delivered, want, row.Entry)
			}
			if tc.raises && stored != delivered {
				t.Fatalf("the reviewer's row stores the task %q, and the reviewer is delivered %q: the two must be one text", stored, delivered)
			}
			if !tc.raises && !tc.none && !strings.HasSuffix(stored, reviewEntryPartOf(row.Entry)) {
				t.Fatalf("the reviewer's row stores the task %q, want the task its entry %d was raised with, which names it", stored, row.Entry)
			}
		})
	}
}

// bootResumesTheReviewer is the boot's resume of LEGION-208's reviewer, a claim the daemon
// suspended level with the entry its row holds (startSuspendedOwner), with only that start in the
// outbox: the reviewer relaunched in the middle of its round.
func bootResumesTheReviewer(t *testing.T, pool *pgxpool.Pool, engine *Engine) {
	t.Helper()
	row, issue := reviewerRow(t, pool), issueRecord(t, pool, "LEGION-208")
	outboxRan(t, pool)
	applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := engine.startSuspendedOwner(ctx, tx, suspendedOwner{issue: *issue, root: *issue, role: claim.RoleReviewer,
			run: record.RoleRun{Claim: &record.RoleClaim{GivenEntry: row.Entry}}}, row)
		return err
	})
}

// A reviewer relaunched in the middle of its round is given the boot's resume task, which names
// the entry its row holds, and its review that names that entry records the head it read: the
// entry a resume names is the entry that stands. Round 2 here stands at headTwo, and round 1's
// review read headOne. The number is read from the task the reviewer was delivered, as the agent
// reads it.
func TestAReviewOfTheEntryTheBootsResumeNamesRecordsItsHead(t *testing.T) {
	pool, engine, apply := reviewRoundOne(t)
	apply("request-changes-1", reviewAt(t, pool, 1, "changes_requested", headOne, reviewTestedAt))
	mergesForward(t, pool, engine, headOne, headTwo, reviewTestedAt.Add(time.Hour))
	backToReviewing(t, apply, 2)
	if got := reviewerRow(t, pool).ReviewedHead; got != headOne {
		t.Fatalf("round 2's reviewer row reads the reviewed commit %q, want %s", got, headOne)
	}
	bootResumesTheReviewer(t, pool, engine)
	entry := entryNamedIn(t, deliveredTask(t, pool, "the boot's resume"))
	review := reviewAt(t, pool, 2, "changes_requested", headTwo, reviewTestedAt.Add(2*time.Hour))
	review.Body = reviewBodyOfEntry("the review", "ses-reviewer", headTwo, entry)
	apply("request-changes-2", review)
	if got := issuePhase(t, pool); got != phase.Implementing {
		t.Fatalf("after the resumed reviewer's request for changes the issue is in %s, want implementing", got)
	}
	if got := reviewerRow(t, pool).ReviewedHead; got != headTwo {
		t.Fatalf("after the resumed reviewer's review of entry %d the reviewer's row reads the reviewed commit %q, want %s, the head that review read", entry, got, headTwo)
	}
}
