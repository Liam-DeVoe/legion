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

// The five tables of the daemon's writes of legion-verdict (migration 20261120185521_verdicts) keep
// what the store writes in them. Each row written through the store's write reads back through
// its read with every field as it was written, the id the write answered included. A read keyed on
// a pull request, a pull request's head or a repository's head answers that key's rows and no
// other's, whatever case the repository is spelled in, since GitHub spells a repository as its
// owner does. The reads of the newest row answer the later of two rows of their key, and every
// other read answers each row of its key, oldest first.
func TestTheVerdictTablesRoundTrip(t *testing.T) {
	st := migratedStore(t)
	ctx := context.Background()
	widgets := ghrepo.MustParse("acme/widgets")
	gadgets := ghrepo.MustParse("acme/gadgets")
	spelledOtherwise := ghrepo.MustParse("ACME/Widgets")
	at := time.Date(2026, 11, 20, 18, 55, 21, 123456000, time.UTC)
	minute := func(n int) time.Time { return at.Add(time.Duration(n) * time.Minute) }

	firstHead := OutsidePullRequest{Repository: widgets, Number: 42, Head: "h1", RecordedAt: minute(0)}
	movedHead := OutsidePullRequest{Repository: widgets, Number: 42, Head: "h2", RecordedAt: minute(1)}
	approval := OutsideReview{Repository: widgets, Number: 42, GitHubID: 5001, Head: "h2", Author: "a-writer",
		AuthorCanWrite: true, State: "approved", SubmittedAt: minute(2), RecordedAt: minute(3)}
	ciRefusal := OutsideReview{Repository: widgets, Number: 42, GitHubID: 5002, Head: "h2", Author: "example-ci-review[bot]",
		State: "changes_requested", RecordedAt: minute(4)}
	firstNamed := SideReviewer{Repository: widgets, Number: 42, Head: "h2", Session: "ses-first", RecordedAt: minute(5)}
	namedAgain := SideReviewer{Repository: widgets, Number: 42, Head: "h2", Session: "ses-second", RecordedAt: minute(6)}
	legionVerdict := Verdict{Repository: widgets, Number: 7, Head: "h5", Issue: "LEGION-7", Verdict: VerdictApprove,
		TestedCommit: "h4", Session: "ses-reviewer", RecordedAt: minute(7)}
	sideRefusal := Verdict{Repository: widgets, Number: 42, Head: "h2", Verdict: VerdictChanges, Session: "ses-second",
		RecordedAt: minute(8)}
	sideApproval := Verdict{Repository: widgets, Number: 42, Head: "h2", Verdict: VerdictApprove, Session: "ses-second",
		RecordedAt: minute(9)}
	ownRun := VerdictWrite{Repository: widgets, Head: "h2", Kind: VerdictCheckRun, GitHubID: 900001,
		Conclusion: VerdictSuccess, WrittenAt: minute(10)}
	overForeignStatus := VerdictWrite{Repository: widgets, Head: "h2", Kind: VerdictStatus, GitHubID: 900002,
		Conclusion: VerdictFailure, ForeignID: 700001, WrittenAt: minute(11)}

	if err := st.Tx(ctx, func(tx pgx.Tx) error {
		for _, step := range []struct {
			record func() (int64, error)
			id     *int64
		}{
			{func() (int64, error) { return RecordOutsidePullRequestTx(ctx, tx, firstHead) }, &firstHead.ID},
			{func() (int64, error) { return RecordOutsidePullRequestTx(ctx, tx, movedHead) }, &movedHead.ID},
			{func() (int64, error) { return RecordOutsideReviewTx(ctx, tx, approval) }, &approval.ID},
			{func() (int64, error) { return RecordOutsideReviewTx(ctx, tx, ciRefusal) }, &ciRefusal.ID},
			{func() (int64, error) { return RecordSideReviewerTx(ctx, tx, firstNamed) }, &firstNamed.ID},
			{func() (int64, error) { return RecordSideReviewerTx(ctx, tx, namedAgain) }, &namedAgain.ID},
			{func() (int64, error) { return RecordVerdictTx(ctx, tx, legionVerdict) }, &legionVerdict.ID},
			{func() (int64, error) { return RecordVerdictTx(ctx, tx, sideRefusal) }, &sideRefusal.ID},
			{func() (int64, error) { return RecordVerdictTx(ctx, tx, sideApproval) }, &sideApproval.ID},
			{func() (int64, error) { return RecordVerdictWriteTx(ctx, tx, ownRun) }, &ownRun.ID},
			{func() (int64, error) { return RecordVerdictWriteTx(ctx, tx, overForeignStatus) }, &overForeignStatus.ID},
		} {
			id, err := step.record()
			if err != nil {
				return err
			}
			*step.id = id
		}
		// Rows of another key, which no read below may answer.
		for _, record := range []func() (int64, error){
			func() (int64, error) {
				return RecordOutsidePullRequestTx(ctx, tx, OutsidePullRequest{Repository: gadgets, Number: 42, Head: "g1", RecordedAt: minute(12)})
			},
			func() (int64, error) {
				return RecordOutsidePullRequestTx(ctx, tx, OutsidePullRequest{Repository: widgets, Number: 43, Head: "w1", RecordedAt: minute(12)})
			},
			func() (int64, error) {
				return RecordOutsideReviewTx(ctx, tx, OutsideReview{Repository: widgets, Number: 43, GitHubID: 5003, Head: "w1",
					Author: "a-writer", State: "approved", RecordedAt: minute(12)})
			},
			func() (int64, error) {
				return RecordSideReviewerTx(ctx, tx, SideReviewer{Repository: widgets, Number: 42, Head: "h1", Session: "ses-old", RecordedAt: minute(12)})
			},
			func() (int64, error) {
				return RecordVerdictTx(ctx, tx, Verdict{Repository: widgets, Number: 42, Head: "h1", Verdict: VerdictChanges,
					Session: "ses-old", RecordedAt: minute(12)})
			},
			func() (int64, error) {
				return RecordVerdictWriteTx(ctx, tx, VerdictWrite{Repository: widgets, Head: "h1", Kind: VerdictCheckRun, GitHubID: 900003,
					Conclusion: VerdictFailure, WrittenAt: minute(12)})
			},
			func() (int64, error) {
				return RecordVerdictWriteTx(ctx, tx, VerdictWrite{Repository: gadgets, Head: "h2", Kind: VerdictCheckRun, GitHubID: 900004,
					Conclusion: VerdictSuccess, WrittenAt: minute(12)})
			},
		} {
			if _, err := record(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("write the rows of the verdict tables: %v", err)
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

		reviews, err := OutsideReviewsTx(ctx, tx, spelledOtherwise, 42)
		if err != nil {
			return err
		}
		for i := range reviews {
			reviews[i].SubmittedAt = reviews[i].SubmittedAt.UTC()
			reviews[i].RecordedAt = reviews[i].RecordedAt.UTC()
		}
		if want := []OutsideReview{approval, ciRefusal}; !reflect.DeepEqual(reviews, want) {
			t.Errorf("the outside reviews of %s#42 read %+v, want its two rows, oldest first: %+v", spelledOtherwise, reviews, want)
		}

		named, ok, err := SideReviewerTx(ctx, tx, spelledOtherwise, 42, "h2")
		if err != nil {
			return err
		}
		named.RecordedAt = named.RecordedAt.UTC()
		if !ok || !reflect.DeepEqual(named, namedAgain) {
			t.Errorf("the side reviewer of %s#42 at h2 reads %+v (found %v), want the later of its two namings, %+v", spelledOtherwise, named, ok, namedAgain)
		}

		for _, want := range []Verdict{legionVerdict, sideApproval} {
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
		return nil
	}); err != nil {
		t.Fatalf("read the rows of the verdict tables: %v", err)
	}
}

// A write of the verdict tables refuses a row that leaves out what the row is about, or that
// carries a word the table's reader does not know: the tables hold no constraint of their own
// (20261120185521_verdicts says why), so the write's own refusal is what keeps such a row out. A
// write that let one through records it, and the test names the row it wrote.
func TestTheVerdictTableWritesRefuseAnIncompleteRow(t *testing.T) {
	st := migratedStore(t)
	ctx := context.Background()
	widgets := ghrepo.MustParse("acme/widgets")
	at := time.Date(2026, 11, 20, 18, 55, 21, 0, time.UTC)
	pr := OutsidePullRequest{Repository: widgets, Number: 42, Head: "h1", RecordedAt: at}
	review := OutsideReview{Repository: widgets, Number: 42, Head: "h1", State: "approved", RecordedAt: at}
	named := SideReviewer{Repository: widgets, Number: 42, Head: "h1", Session: "ses-side", RecordedAt: at}
	verdict := Verdict{Repository: widgets, Number: 42, Head: "h1", Verdict: VerdictApprove, Session: "ses-side", RecordedAt: at}
	written := VerdictWrite{Repository: widgets, Head: "h1", Kind: VerdictCheckRun, GitHubID: 1, Conclusion: VerdictSuccess, WrittenAt: at}
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
		{"an outside review of no state", func(tx pgx.Tx) (int64, error) {
			r := review
			r.State = ""
			return RecordOutsideReviewTx(ctx, tx, r)
		}, "state"},
		{"an outside review of no head", func(tx pgx.Tx) (int64, error) {
			r := review
			r.Head = ""
			return RecordOutsideReviewTx(ctx, tx, r)
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
		}, "id"},
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
