package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// The five tables of the daemon's writes of legion-verdict (migration 20261122045825_verdicts) keep
// what the store writes in them. Each row written through the store's write reads back through
// its read with every field as it was written, the id the write answered and the repository's own
// spelling included. A read keyed on a pull request, a pull request's head or a repository's head
// answers that key's rows and no other's, whatever case the repository is spelled in, since GitHub
// spells a repository as its owner does: the rows written last, and so the newest of all, each
// differ from a key read below in one part of the key alone. The reads of the newest row answer
// the later of two rows of their key, a refusal over an approval among them, and every other read
// answers each row of its key in the order the rows were written: each later row of a key carries
// an earlier time, so the order is the rows' ids and never their times. A key no row holds reads
// as none. A review GitHub gave no submission time is stored with none, a null. A Legion issue's
// verdict is the verdict of one step of its task: the reads of a step's verdict, at a head and at
// any head, answer the newest row of that task and step alone, while the read of a head's newest
// verdict answers the newest row of any step.
func TestTheVerdictTablesRoundTrip(t *testing.T) {
	st := migratedStore(t)
	ctx := context.Background()
	widgets := ghrepo.MustParse("Acme/Widgets")
	gadgets := ghrepo.MustParse("acme/gadgets")
	spelledOtherwise := ghrepo.MustParse("acme/WIDGETS")
	at := time.Date(2026, 11, 20, 18, 55, 21, 123456000, time.UTC)
	minute := func(n int) time.Time { return at.Add(time.Duration(n) * time.Minute) }

	firstHead := OutsidePullRequest{Repository: widgets, Number: 42, Head: "h1", RecordedAt: minute(1)}
	movedHead := OutsidePullRequest{Repository: widgets, Number: 42, Head: "h2", RecordedAt: minute(0)}
	approval := VerdictReview{Repository: widgets, Number: 42, GitHubID: 5001, Head: "h2", Author: "a-writer",
		AuthorCanWrite: true, State: "approved", SubmittedAt: minute(3), RecordedAt: minute(4)}
	ciRefusal := VerdictReview{Repository: widgets, Number: 42, GitHubID: 5002, Head: "h2", Author: "example-ci-review[bot]",
		State: "changes_requested", RecordedAt: minute(2)}
	firstNamed := SideReviewer{Repository: widgets, Number: 42, Head: "h2", Session: "ses-first", RecordedAt: minute(6)}
	namedAgain := SideReviewer{Repository: widgets, Number: 42, Head: "h2", Session: "ses-second", RecordedAt: minute(5)}
	// LEGION-7's verdicts: its reviewer's (step 4 of the task default) at h4 and then at h5, the
	// owner's acceptance (step 5) at h5, and, newest of all at h5, a row of another task's step 5.
	reviewerEarlier := Verdict{Repository: widgets, Number: 7, Head: "h4", Issue: "LEGION-7", Task: "default", Step: 4, Verdict: VerdictApprove,
		Session: "ses-reviewer", RecordedAt: minute(13)}
	legionVerdict := Verdict{Repository: widgets, Number: 7, Head: "h5", Issue: "LEGION-7", Task: "default", Step: 4, Verdict: VerdictApprove,
		Session: "ses-reviewer", RecordedAt: minute(7)}
	ownerVerdict := Verdict{Repository: widgets, Number: 7, Head: "h5", Issue: "LEGION-7", Task: "default", Step: 5, Verdict: VerdictChanges,
		Session: "ses-architect", RecordedAt: minute(6)}
	otherTask := Verdict{Repository: widgets, Number: 7, Head: "h5", Issue: "LEGION-7", Task: "other", Step: 5, Verdict: VerdictApprove,
		Session: "ses-other", RecordedAt: minute(5)}
	sideApproval := Verdict{Repository: widgets, Number: 42, Head: "h2", Verdict: VerdictApprove, Session: "ses-second",
		RecordedAt: minute(9)}
	sideRefusal := Verdict{Repository: widgets, Number: 42, Head: "h2", Verdict: VerdictChanges, Session: "ses-second",
		RecordedAt: minute(8)}
	ownRun := VerdictWrite{Repository: widgets, Head: "h2", Kind: VerdictWriteCheckRun, GitHubID: 900001,
		Conclusion: VerdictConclusionSuccess, WrittenAt: minute(11)}
	overForeignStatus := VerdictWrite{Repository: widgets, Head: "h2", Kind: VerdictWriteStatus, GitHubID: 900002,
		Conclusion: VerdictConclusionFailure, ForeignID: 700001, WrittenAt: minute(10)}

	written := func(id int64, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatalf("write a row of the verdict tables: %v", err)
		}
		return id
	}
	if err := st.Tx(ctx, func(tx pgx.Tx) error {
		firstHead.ID = written(RecordOutsidePullRequestTx(ctx, tx, firstHead))
		movedHead.ID = written(RecordOutsidePullRequestTx(ctx, tx, movedHead))
		approval.ID = written(RecordVerdictReviewTx(ctx, tx, approval))
		ciRefusal.ID = written(RecordVerdictReviewTx(ctx, tx, ciRefusal))
		firstNamed.ID = written(RecordSideReviewerTx(ctx, tx, firstNamed))
		namedAgain.ID = written(RecordSideReviewerTx(ctx, tx, namedAgain))
		reviewerEarlier.ID = written(RecordVerdictTx(ctx, tx, reviewerEarlier))
		legionVerdict.ID = written(RecordVerdictTx(ctx, tx, legionVerdict))
		ownerVerdict.ID = written(RecordVerdictTx(ctx, tx, ownerVerdict))
		otherTask.ID = written(RecordVerdictTx(ctx, tx, otherTask))
		sideApproval.ID = written(RecordVerdictTx(ctx, tx, sideApproval))
		sideRefusal.ID = written(RecordVerdictTx(ctx, tx, sideRefusal))
		ownRun.ID = written(RecordVerdictWriteTx(ctx, tx, ownRun))
		overForeignStatus.ID = written(RecordVerdictWriteTx(ctx, tx, overForeignStatus))

		// Each differs from a key read below in its repository, its number or its head alone.
		for _, pr := range []OutsidePullRequest{
			{Repository: gadgets, Number: 42, Head: "h2", RecordedAt: minute(-1)},
			{Repository: widgets, Number: 43, Head: "h2", RecordedAt: minute(-1)},
		} {
			written(RecordOutsidePullRequestTx(ctx, tx, pr))
		}
		for _, r := range []VerdictReview{
			{Repository: gadgets, Number: 42, GitHubID: 5003, Head: "h2", State: "approved", RecordedAt: minute(-1)},
			{Repository: widgets, Number: 43, GitHubID: 5004, Head: "h2", State: "approved", RecordedAt: minute(-1)},
		} {
			written(RecordVerdictReviewTx(ctx, tx, r))
		}
		for _, n := range []SideReviewer{
			{Repository: gadgets, Number: 42, Head: "h2", Session: "ses-gadgets", RecordedAt: minute(-1)},
			{Repository: widgets, Number: 43, Head: "h2", Session: "ses-43", RecordedAt: minute(-1)},
			{Repository: widgets, Number: 42, Head: "h1", Session: "ses-h1", RecordedAt: minute(-1)},
		} {
			written(RecordSideReviewerTx(ctx, tx, n))
		}
		for _, v := range []Verdict{
			{Repository: gadgets, Number: 42, Head: "h2", Verdict: VerdictChanges, Session: "ses-gadgets", RecordedAt: minute(-1)},
			{Repository: widgets, Number: 43, Head: "h2", Verdict: VerdictChanges, Session: "ses-43", RecordedAt: minute(-1)},
			{Repository: widgets, Number: 42, Head: "h1", Verdict: VerdictChanges, Session: "ses-h1", RecordedAt: minute(-1)},
		} {
			written(RecordVerdictTx(ctx, tx, v))
		}
		for _, w := range []VerdictWrite{
			{Repository: gadgets, Head: "h2", Kind: VerdictWriteCheckRun, GitHubID: 900003, Conclusion: VerdictConclusionSuccess, WrittenAt: minute(-1)},
			{Repository: widgets, Head: "h1", Kind: VerdictWriteCheckRun, GitHubID: 900004, Conclusion: VerdictConclusionFailure, WrittenAt: minute(-1)},
		} {
			written(RecordVerdictWriteTx(ctx, tx, w))
		}
		return nil
	}); err != nil {
		t.Fatalf("write the rows of the verdict tables: %v", err)
	}
	// A later write of a key appends a row of its own: it never takes the earlier row's place.
	for _, ids := range [][2]int64{{firstHead.ID, movedHead.ID}, {firstNamed.ID, namedAgain.ID}, {sideApproval.ID, sideRefusal.ID}} {
		if ids[0] >= ids[1] {
			t.Fatalf("a later row of one key was written as row %d after row %d, want a row of its own with a higher id", ids[1], ids[0])
		}
	}

	// pgx reads a timestamptz back in the local zone, and one moment in two zones is two values to
	// reflect.DeepEqual, so each time read is put back in UTC, as the rows were written, before
	// the compare.
	if err := st.Tx(ctx, func(tx pgx.Tx) error {
		pr, ok, err := OutsidePullRequestTx(ctx, tx, spelledOtherwise, 42)
		if err != nil {
			return err
		}
		pr.RecordedAt = pr.RecordedAt.UTC()
		if !ok || !reflect.DeepEqual(pr, movedHead) {
			t.Errorf("the outside pull request %s#42 reads %+v (found %v), want the newer of its two rows, %+v", spelledOtherwise, pr, ok, movedHead)
		}
		if pr, ok, err := OutsidePullRequestTx(ctx, tx, widgets, 44); err != nil || ok {
			t.Errorf("the outside pull request %s#44, which no row records, reads %+v (found %v, %v), want none", widgets, pr, ok, err)
		}

		reviews, err := VerdictReviewsTx(ctx, tx, spelledOtherwise, 42)
		if err != nil {
			return err
		}
		for i := range reviews {
			reviews[i].SubmittedAt = reviews[i].SubmittedAt.UTC()
			reviews[i].RecordedAt = reviews[i].RecordedAt.UTC()
		}
		if want := []VerdictReview{approval, ciRefusal}; !reflect.DeepEqual(reviews, want) {
			t.Errorf("the reviews of %s#42 read %+v, want its two rows, oldest first: %+v", spelledOtherwise, reviews, want)
		}
		for review, wantNull := range map[int64]bool{approval.ID: false, ciRefusal.ID: true} {
			var null bool
			if err := tx.QueryRow(ctx, `select submitted_at is null from verdict_reviews where id = $1`, review).Scan(&null); err != nil {
				return err
			}
			if null != wantNull {
				t.Errorf("the review row %d holds a null submission time: %v, want %v", review, null, wantNull)
			}
		}

		named, ok, err := SideReviewerTx(ctx, tx, spelledOtherwise, 42, "h2")
		if err != nil {
			return err
		}
		named.RecordedAt = named.RecordedAt.UTC()
		if !ok || !reflect.DeepEqual(named, namedAgain) {
			t.Errorf("the side reviewer of %s#42 at h2 reads %+v (found %v), want the later of its two namings, %+v", spelledOtherwise, named, ok, namedAgain)
		}

		// The newest verdict at a head is the newest row of any step: another task's at h5.
		for _, want := range []Verdict{otherTask, sideRefusal} {
			got, ok, err := VerdictTx(ctx, tx, spelledOtherwise, want.Number, want.Head)
			if err != nil {
				return err
			}
			got.RecordedAt = got.RecordedAt.UTC()
			if !ok || !reflect.DeepEqual(got, want) {
				t.Errorf("the verdict on %s#%d at %s reads %+v (found %v), want the newest of its rows, %+v",
					spelledOtherwise, want.Number, want.Head, got, ok, want)
			}
		}
		// A step's verdict at h5 is that task's and that step's newest row there: the reviewer's
		// under the owner's later row, and the owner's under the other task's later row.
		for _, want := range []Verdict{legionVerdict, ownerVerdict} {
			got, ok, err := StepVerdictTx(ctx, tx, spelledOtherwise, 7, "h5", want.Task, want.Step)
			if err != nil {
				return err
			}
			got.RecordedAt = got.RecordedAt.UTC()
			if !ok || !reflect.DeepEqual(got, want) {
				t.Errorf("the verdict of step %d of task %s on %s#7 at h5 reads %+v (found %v), want %+v", want.Step, want.Task, spelledOtherwise, got, ok, want)
			}
		}
		// A step's last verdict at any head is the newest row of that task and step: the reviewer's
		// at h5 over its earlier one at h4, and the owner's under the other task's later row.
		for _, want := range []Verdict{legionVerdict, ownerVerdict} {
			got, ok, err := LastStepVerdictTx(ctx, tx, spelledOtherwise, 7, want.Task, want.Step)
			if err != nil {
				return err
			}
			got.RecordedAt = got.RecordedAt.UTC()
			if !ok || !reflect.DeepEqual(got, want) {
				t.Errorf("the last verdict of step %d of task %s on %s#7 reads %+v (found %v), want %+v", want.Step, want.Task, spelledOtherwise, got, ok, want)
			}
		}
		if v, ok, err := StepVerdictTx(ctx, tx, widgets, 7, "h4", "default", 5); err != nil || ok {
			t.Errorf("the verdict of step 5 on %s#7 at h4, which no row records, reads %+v (found %v, %v), want none", widgets, v, ok, err)
		}
		if v, ok, err := LastStepVerdictTx(ctx, tx, widgets, 7, "default", 3); err != nil || ok {
			t.Errorf("the last verdict of step 3 on %s#7, which no row records, reads %+v (found %v, %v), want none", widgets, v, ok, err)
		}

		writes, err := VerdictWritesTx(ctx, tx, spelledOtherwise, "h2")
		if err != nil {
			return err
		}
		for i := range writes {
			writes[i].WrittenAt = writes[i].WrittenAt.UTC()
		}
		if want := []VerdictWrite{ownRun, overForeignStatus}; !reflect.DeepEqual(writes, want) {
			t.Errorf("the writes of legion-verdict on %s at h2 read %+v, want its two rows, oldest first: %+v", spelledOtherwise, writes, want)
		}

		if named, ok, err := SideReviewerTx(ctx, tx, widgets, 42, "h9"); err != nil || ok {
			t.Errorf("the side reviewer of %s#42 at h9, which no row names, reads %+v (found %v, %v), want none", widgets, named, ok, err)
		}
		if v, ok, err := VerdictTx(ctx, tx, widgets, 42, "h9"); err != nil || ok {
			t.Errorf("the verdict on %s#42 at h9, which no row records, reads %+v (found %v, %v), want none", widgets, v, ok, err)
		}
		if reviews, err := VerdictReviewsTx(ctx, tx, widgets, 44); err != nil || len(reviews) != 0 {
			t.Errorf("the reviews of %s#44, which no row records, read %+v (%v), want none", widgets, reviews, err)
		}
		if writes, err := VerdictWritesTx(ctx, tx, widgets, "h9"); err != nil || len(writes) != 0 {
			t.Errorf("the writes of legion-verdict on %s at h9, which no row records, read %+v (%v), want none", widgets, writes, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("read the rows of the verdict tables: %v", err)
	}
}

// The verdict tables keep the shape their migration gives them (20261122045825_verdicts): no table
// holds a key, a check or another constraint, none has a unique index, and every column a row must
// fill has a default. A daemon build from before them then runs on them however it inserts. The
// five tables and their columns are counted first, so a catalog read that finds none of them fails
// rather than reading as a pass.
func TestTheVerdictTablesHoldNoConstraintAndNoUniqueIndex(t *testing.T) {
	st := migratedStore(t)
	ctx := context.Background()
	tables := []string{"outside_pull_requests", "verdict_reviews", "side_reviewers", "verdicts", "verdict_writes"}
	const relations = `select c.oid from pg_class c where c.relname = any($1) and c.relnamespace = current_schema()::regnamespace`
	var found, columns int
	if err := st.Pool().QueryRow(ctx, `select count(*), (select count(*) from pg_attribute
		where attrelid in (`+relations+`) and attnum > 0 and not attisdropped)
		from (`+relations+`) r`, tables).Scan(&found, &columns); err != nil {
		t.Fatalf("count the verdict tables: %v", err)
	}
	if found != len(tables) || columns == 0 {
		t.Fatalf("the catalog holds %d of the %d verdict tables, with %d columns: the reads below would see nothing", found, len(tables), columns)
	}
	for _, check := range []struct{ what, sql string }{
		// Postgres 18 records each column's not null as a constraint of kind n; a default fills it.
		{"constraint", `select conrelid::regclass || ' ' || conname from pg_constraint
			where conrelid in (` + relations + `) and contype <> 'n' order by 1`},
		{"unique index", `select indexrelid::regclass::text from pg_index
			where indrelid in (` + relations + `) and indisunique order by 1`},
		{"column a row must fill with no default", `select attrelid::regclass || '.' || attname from pg_attribute
			where attrelid in (` + relations + `) and attnum > 0 and not attisdropped and attnotnull and not atthasdef order by 1`},
	} {
		rows, err := st.Pool().Query(ctx, check.sql, tables)
		if err != nil {
			t.Fatalf("read each %s of the verdict tables: %v", check.what, err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("read each %s of the verdict tables: %v", check.what, err)
		}
		if len(names) > 0 {
			t.Errorf("the verdict tables hold a %s: %v, want none", check.what, names)
		}
	}
}

// A write of the verdict tables refuses a row that leaves out what the row is about, or that
// carries a word the table's reader does not know: the tables hold no constraint of their own
// (20261122045825_verdicts says why), so the write's own refusal is what keeps such a row out. A
// write that let one through records it, and the test names the row it wrote.
func TestTheVerdictTableWritesRefuseAnIncompleteRow(t *testing.T) {
	st := migratedStore(t)
	ctx := context.Background()
	widgets := ghrepo.MustParse("acme/widgets")
	at := time.Date(2026, 11, 20, 18, 55, 21, 0, time.UTC)
	pr := OutsidePullRequest{Repository: widgets, Number: 42, Head: "h1", RecordedAt: at}
	review := VerdictReview{Repository: widgets, Number: 42, Head: "h1", State: "approved", RecordedAt: at}
	named := SideReviewer{Repository: widgets, Number: 42, Head: "h1", Session: "ses-side", RecordedAt: at}
	verdict := Verdict{Repository: widgets, Number: 42, Head: "h1", Verdict: VerdictApprove, Session: "ses-side", RecordedAt: at}
	written := VerdictWrite{Repository: widgets, Head: "h1", Kind: VerdictWriteCheckRun, GitHubID: 1, Conclusion: VerdictConclusionSuccess, WrittenAt: at}
	for _, tc := range []struct {
		row   string
		write func(tx pgx.Tx) (int64, error)
		want  string
	}{
		{"an outside pull request of no repository", func(tx pgx.Tx) (int64, error) {
			p := pr
			p.Repository = ghrepo.Repository{}
			return RecordOutsidePullRequestTx(ctx, tx, p)
		}, "repository"},
		{"an outside pull request of no number", func(tx pgx.Tx) (int64, error) {
			p := pr
			p.Number = 0
			return RecordOutsidePullRequestTx(ctx, tx, p)
		}, "number"},
		{"an outside pull request of no head", func(tx pgx.Tx) (int64, error) {
			p := pr
			p.Head = ""
			return RecordOutsidePullRequestTx(ctx, tx, p)
		}, "head"},
		{"an outside pull request of no time", func(tx pgx.Tx) (int64, error) {
			p := pr
			p.RecordedAt = time.Time{}
			return RecordOutsidePullRequestTx(ctx, tx, p)
		}, "time"},
		{"a review of no state", func(tx pgx.Tx) (int64, error) {
			r := review
			r.State = ""
			return RecordVerdictReviewTx(ctx, tx, r)
		}, "state"},
		{"a review of no head", func(tx pgx.Tx) (int64, error) {
			r := review
			r.Head = ""
			return RecordVerdictReviewTx(ctx, tx, r)
		}, "head"},
		{"a side reviewer of no session", func(tx pgx.Tx) (int64, error) {
			n := named
			n.Session = ""
			return RecordSideReviewerTx(ctx, tx, n)
		}, "session"},
		{"a side reviewer of a number below one", func(tx pgx.Tx) (int64, error) {
			n := named
			n.Number = -1
			return RecordSideReviewerTx(ctx, tx, n)
		}, "number"},
		{"a Legion issue's verdict of no task", func(tx pgx.Tx) (int64, error) {
			v := verdict
			v.Issue, v.Step = "LEGION-7", 4
			return RecordVerdictTx(ctx, tx, v)
		}, "step"},
		{"a Legion issue's verdict of no step", func(tx pgx.Tx) (int64, error) {
			v := verdict
			v.Issue, v.Task = "LEGION-7", "default"
			return RecordVerdictTx(ctx, tx, v)
		}, "step"},
		{"a verdict of a step on no Legion issue", func(tx pgx.Tx) (int64, error) {
			v := verdict
			v.Task, v.Step = "default", 4
			return RecordVerdictTx(ctx, tx, v)
		}, "step"},
		{"a verdict of a word no reader knows", func(tx pgx.Tx) (int64, error) {
			v := verdict
			v.Verdict = "pass"
			return RecordVerdictTx(ctx, tx, v)
		}, `"pass"`},
		{"a verdict of no session", func(tx pgx.Tx) (int64, error) {
			v := verdict
			v.Session = ""
			return RecordVerdictTx(ctx, tx, v)
		}, "session"},
		{"a verdict of no head", func(tx pgx.Tx) (int64, error) {
			v := verdict
			v.Head = ""
			return RecordVerdictTx(ctx, tx, v)
		}, "head"},
		{"a write of no repository", func(tx pgx.Tx) (int64, error) {
			w := written
			w.Repository = ghrepo.Repository{}
			return RecordVerdictWriteTx(ctx, tx, w)
		}, "repository"},
		{"a write of no head", func(tx pgx.Tx) (int64, error) {
			w := written
			w.Head = ""
			return RecordVerdictWriteTx(ctx, tx, w)
		}, "head"},
		{"a write of a kind no reader knows", func(tx pgx.Tx) (int64, error) {
			w := written
			w.Kind = "comment"
			return RecordVerdictWriteTx(ctx, tx, w)
		}, `"comment"`},
		{"a write of a conclusion no reader knows", func(tx pgx.Tx) (int64, error) {
			w := written
			w.Conclusion = "neutral"
			return RecordVerdictWriteTx(ctx, tx, w)
		}, `"neutral"`},
		{"a write of no GitHub id", func(tx pgx.Tx) (int64, error) {
			w := written
			w.GitHubID = 0
			return RecordVerdictWriteTx(ctx, tx, w)
		}, "GitHub's id"},
		{"a write over a foreign id below zero", func(tx pgx.Tx) (int64, error) {
			w := written
			w.ForeignID = -1
			return RecordVerdictWriteTx(ctx, tx, w)
		}, "foreign"},
		{"a write of no time", func(tx pgx.Tx) (int64, error) {
			w := written
			w.WrittenAt = time.Time{}
			return RecordVerdictWriteTx(ctx, tx, w)
		}, "time"},
	} {
		if err := st.Tx(ctx, func(tx pgx.Tx) error {
			if id, err := tc.write(tx); err == nil {
				t.Errorf("%s was written as row %d, want it refused", tc.row, id)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s was refused with %q, want the refusal to name %s", tc.row, err, tc.want)
			}
			return nil
		}); err != nil {
			t.Fatalf("write %s: %v", tc.row, err)
		}
	}
}
