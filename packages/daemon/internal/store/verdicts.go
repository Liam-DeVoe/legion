package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/verdict"
)

// The records the daemon's writer of the required check legion-verdict keeps, in the five tables of
// migration 20261123213816_verdicts: the pull requests of a verdict repository that no Legion issue
// owns, with their heads; the reviews of a verdict repository's pull requests, Legion heads and
// outside ones alike; the side reviewer a controller named for such a pull request and head; the
// verdicts given on a head; and each check run and commit status the writer
// wrote. Each table is a record of what was seen or done, one row each time: a row is never
// changed, so the tables need no unique key, and a read of the newest row takes the one with the
// highest id, of an outside pull request the newest by GitHub's clock first (OutsidePullRequestTx).
// The tables hold no constraint (the migration says why), so each write refuses, before
// it reaches the database, a row that leaves out its repository, its head, its pull request's
// number where the table has one, or its time, and a verdict, a write's kind or a conclusion its
// reader does not know. Each read keys on the repository without case, as GitHub compares a
// repository's names, and parses it back with ghrepo.Parse, so a row reads back as the Repository
// it was written with.
//
// Every read and write runs in the caller's transaction, so a record commits with the decision
// the caller makes on it, or not at all. A caller with no transaction opens one with Store.Tx.

// OutsidePullRequest is one sighting of a pull request of a verdict repository that no Legion issue
// owns, as a webhook event or GitHub's list showed it: the head it stood at, and GitHub's clock of
// the pull request's last change then. The newest sighting by that clock is its current head
// (OutsidePullRequestTx).
type OutsidePullRequest struct {
	// ID is the row's own id, which the store fills.
	ID         int64
	Repository ghrepo.Repository
	Number     int
	Head       string
	// UpdatedAt is GitHub's updated_at of the pull request as the sighting carried it, zero (a null)
	// where it carried none.
	UpdatedAt time.Time
	// RecordedAt is when the daemon recorded the sighting.
	RecordedAt time.Time
}

// VerdictReview is one review of a verdict repository's pull request at a head, delivered by
// webhook, of a Legion head or an outside one, as intake recorded it: the writer reads a person's
// last review at a head, and the CI reviewer's, from these rows.
type VerdictReview struct {
	// ID is the row's own id, which the store fills.
	ID         int64
	Repository ghrepo.Repository
	Number     int
	// GitHubID is GitHub's id of the review, zero when the event did not carry it.
	GitHubID int64
	// Head is the commit the review read.
	Head   string
	Author string
	// AuthorCanWrite is whether GitHub gives Author write access or higher to the repository, as
	// intake read it (intake.PullRequestReview.AuthorCanWrite).
	AuthorCanWrite bool
	// State is the review's state as intake spells it (approved, changes_requested, commented).
	State string
	// SubmittedAt is when GitHub says the review was submitted, zero when the event did not carry
	// it.
	SubmittedAt time.Time
	// RecordedAt is when the daemon recorded the review.
	RecordedAt time.Time
}

// SideReviewer is one naming, by a controller, of the reviewer session whose verdict on a pull
// request at a head the daemon takes. The newest naming of a pull request and head stands
// (SideReviewerTx).
type SideReviewer struct {
	// ID is the row's own id, which the store fills.
	ID         int64
	Repository ghrepo.Repository
	Number     int
	Head       string
	Session    string
	// RecordedAt is when the daemon recorded the naming.
	RecordedAt time.Time
}

// VerdictWord is a reviewer's verdict on a head, as verdict.Decide reads it.
type VerdictWord = verdict.Word

const (
	VerdictApprove = verdict.ReviewerApprove
	VerdictChanges = verdict.ReviewerChanges
)

// Verdict is one verdict a session gave on a pull request at a head: the agent of a review step of
// a Legion issue's task on its issue's head, or the side reviewer a controller named on a head no
// Legion issue owns. The newest verdict on a pull request and head stands (VerdictTx), and of a
// Legion issue's step the newest verdict of that step (StepVerdictTx).
type Verdict struct {
	// ID is the row's own id, which the store fills.
	ID         int64
	Repository ghrepo.Repository
	Number     int
	Head       string
	// Issue is the Legion issue whose head it is, empty for a pull request no Legion issue owns.
	Issue string
	// Task and Step are the review step of Issue's task whose agent gave the verdict: the task's id
	// and the step's place in it, from 1 (workflow's StepID keys a step's record by the same two).
	// A Legion issue's verdict names both, and a verdict on a head no Legion issue owns names
	// neither: "" and 0.
	Task    string
	Step    int
	Verdict VerdictWord
	// Session is the claim of the step's agent, or the side reviewer's session, that gave the
	// verdict.
	Session string
	// RecordedAt is when the daemon recorded the verdict.
	RecordedAt time.Time
}

// VerdictWriteKind is what a write of legion-verdict made on GitHub.
type VerdictWriteKind string

const (
	VerdictWriteCheckRun VerdictWriteKind = "check_run"
	VerdictWriteStatus   VerdictWriteKind = "status"
)

// VerdictConclusion is the conclusion a write gave legion-verdict, as verdict.Decide answers it: a
// check run's conclusion, or a commit status's state. A write never records verdict.None.
type VerdictConclusion = verdict.Conclusion

const (
	VerdictConclusionSuccess = verdict.Success
	VerdictConclusionFailure = verdict.Failure
)

// VerdictWrite is one check run or commit status named legion-verdict that the daemon wrote on a
// head. A head's rows are what the daemon's record holds of that head, which the writer's sweep
// tells a run or status another writer made apart by.
type VerdictWrite struct {
	// ID is the row's own id, which the store fills.
	ID         int64
	Repository ghrepo.Repository
	Head       string
	Kind       VerdictWriteKind
	// GitHubID is GitHub's id of the check run or status the write made or updated.
	GitHubID   int64
	Conclusion VerdictConclusion
	// ForeignID is GitHub's id of the run or status of another writer that this write went over,
	// zero when it went over none.
	ForeignID int64
	// WrittenAt is when the daemon wrote it.
	WrittenAt time.Time
}

// validPullRequestRow refuses a row whose pull request is not named whole, or that carries no
// time: what every table but verdict_writes keys on. what names the row in the refusal.
func validPullRequestRow(what string, repository ghrepo.Repository, number int, head string, at time.Time) error {
	switch {
	case repository.IsZero():
		return fmt.Errorf("%s needs its repository", what)
	case number <= 0:
		return fmt.Errorf("%s needs its pull request's number, not %d", what, number)
	case head == "":
		return fmt.Errorf("%s needs its head", what)
	case at.IsZero():
		return fmt.Errorf("%s needs the time it was recorded", what)
	}
	return nil
}

// insertID runs an insert that returns the new row's id, and names what in its failure.
func insertID(ctx context.Context, tx pgx.Tx, what, sql string, args ...any) (int64, error) {
	var id int64
	if err := tx.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		return 0, fmt.Errorf("record %s: %w", what, err)
	}
	return id, nil
}

// parseRepository is a stored repository read back as the Repository it was written with.
func parseRepository(table string, id int64, stored string) (ghrepo.Repository, error) {
	return ghrepo.Parse(fmt.Sprintf("the repository of %s row %d", table, id), stored)
}

// RecordOutsidePullRequestTx records one sighting of an outside pull request at its head and
// answers the row's id. A sighting that carries no clock of GitHub's is stored with none (null).
func RecordOutsidePullRequestTx(ctx context.Context, tx pgx.Tx, pr OutsidePullRequest) (int64, error) {
	what := fmt.Sprintf("the outside pull request %s#%d", pr.Repository, pr.Number)
	if err := validPullRequestRow(what, pr.Repository, pr.Number, pr.Head, pr.RecordedAt); err != nil {
		return 0, err
	}
	return insertID(ctx, tx, what, `insert into outside_pull_requests (repository, number, head, updated_at, recorded_at)
		values ($1, $2, $3, $4, $5) returning id`, pr.Repository.String(), pr.Number, pr.Head, instant(pr.UpdatedAt), pr.RecordedAt)
}

// OutsidePullRequestTx is the newest sighting of an outside pull request, the head it stands at
// now, and whether any row records it. Newest is by GitHub's clock of the sighting (UpdatedAt), so
// an event delivered late that is older than one recorded before it takes no head back; a sighting
// with no clock sorts as the oldest, and of two of one clock the later recorded is newer.
func OutsidePullRequestTx(ctx context.Context, tx pgx.Tx, repository ghrepo.Repository, number int) (OutsidePullRequest, bool, error) {
	var pr OutsidePullRequest
	var stored string
	var updated *time.Time
	err := tx.QueryRow(ctx, `select id, repository, number, head, updated_at, recorded_at from outside_pull_requests
		where lower(repository) = lower($1) and number = $2 order by updated_at desc nulls last, id desc limit 1`, repository.String(), number).
		Scan(&pr.ID, &stored, &pr.Number, &pr.Head, &updated, &pr.RecordedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OutsidePullRequest{}, false, nil
	}
	if err != nil {
		return OutsidePullRequest{}, false, fmt.Errorf("read the outside pull request %s#%d: %w", repository, number, err)
	}
	pr.UpdatedAt = orZero(updated)
	if pr.Repository, err = parseRepository("outside_pull_requests", pr.ID, stored); err != nil {
		return OutsidePullRequest{}, false, err
	}
	return pr, true, nil
}

// RecordVerdictReviewTx records one review of a verdict repository's pull request and answers the
// row's id. A review GitHub gave no submission time is stored with none (null).
func RecordVerdictReviewTx(ctx context.Context, tx pgx.Tx, r VerdictReview) (int64, error) {
	what := fmt.Sprintf("the review %d of the pull request %s#%d", r.GitHubID, r.Repository, r.Number)
	if err := validPullRequestRow(what, r.Repository, r.Number, r.Head, r.RecordedAt); err != nil {
		return 0, err
	}
	if r.State == "" {
		return 0, fmt.Errorf("%s needs its state", what)
	}
	return insertID(ctx, tx, what, `insert into verdict_reviews
		(repository, number, github_id, head, author, author_can_write, state, submitted_at, recorded_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning id`,
		r.Repository.String(), r.Number, r.GitHubID, r.Head, r.Author, r.AuthorCanWrite, r.State, instant(r.SubmittedAt), r.RecordedAt)
}

// VerdictReviewsTx is every review recorded of a verdict repository's pull request, oldest record
// first.
func VerdictReviewsTx(ctx context.Context, tx pgx.Tx, repository ghrepo.Repository, number int) ([]VerdictReview, error) {
	what := fmt.Sprintf("read the reviews of the pull request %s#%d", repository, number)
	rows, err := tx.Query(ctx, `select id, repository, number, github_id, head, author, author_can_write, state,
		submitted_at, recorded_at from verdict_reviews where lower(repository) = lower($1) and number = $2 order by id`,
		repository.String(), number)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	reviews, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (r VerdictReview, err error) {
		var stored string
		var submitted *time.Time
		if err = row.Scan(&r.ID, &stored, &r.Number, &r.GitHubID, &r.Head, &r.Author, &r.AuthorCanWrite, &r.State,
			&submitted, &r.RecordedAt); err != nil {
			return VerdictReview{}, err
		}
		r.SubmittedAt = orZero(submitted)
		r.Repository, err = parseRepository("verdict_reviews", r.ID, stored)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return reviews, nil
}

// RecordSideReviewerTx records a controller's naming of the reviewer session of a pull request at
// a head and answers the row's id.
func RecordSideReviewerTx(ctx context.Context, tx pgx.Tx, r SideReviewer) (int64, error) {
	what := fmt.Sprintf("the side reviewer of %s#%d at %s", r.Repository, r.Number, r.Head)
	if err := validPullRequestRow(what, r.Repository, r.Number, r.Head, r.RecordedAt); err != nil {
		return 0, err
	}
	if r.Session == "" {
		return 0, fmt.Errorf("%s needs the reviewer's session", what)
	}
	return insertID(ctx, tx, what, `insert into side_reviewers (repository, number, head, session, recorded_at)
		values ($1, $2, $3, $4, $5) returning id`, r.Repository.String(), r.Number, r.Head, r.Session, r.RecordedAt)
}

// SideReviewerTx is the newest naming of the reviewer of a pull request at a head, and whether any
// row names one.
func SideReviewerTx(ctx context.Context, tx pgx.Tx, repository ghrepo.Repository, number int, head string) (SideReviewer, bool, error) {
	var r SideReviewer
	var stored string
	err := tx.QueryRow(ctx, `select id, repository, number, head, session, recorded_at from side_reviewers
		where lower(repository) = lower($1) and number = $2 and head = $3 order by id desc limit 1`,
		repository.String(), number, head).Scan(&r.ID, &stored, &r.Number, &r.Head, &r.Session, &r.RecordedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SideReviewer{}, false, nil
	}
	if err != nil {
		return SideReviewer{}, false, fmt.Errorf("read the side reviewer of %s#%d at %s: %w", repository, number, head, err)
	}
	if r.Repository, err = parseRepository("side_reviewers", r.ID, stored); err != nil {
		return SideReviewer{}, false, err
	}
	return r, true, nil
}

// RecordVerdictTx records a session's verdict on a pull request at a head and answers the row's
// id. A Legion issue's verdict names the step it is of, and a verdict no Legion issue owns names
// none, or the write refuses it.
func RecordVerdictTx(ctx context.Context, tx pgx.Tx, v Verdict) (int64, error) {
	what := fmt.Sprintf("the verdict on %s#%d at %s", v.Repository, v.Number, v.Head)
	if err := validPullRequestRow(what, v.Repository, v.Number, v.Head, v.RecordedAt); err != nil {
		return 0, err
	}
	if v.Verdict != VerdictApprove && v.Verdict != VerdictChanges {
		return 0, fmt.Errorf("%s is %q, not %s or %s", what, v.Verdict, VerdictApprove, VerdictChanges)
	}
	if v.Session == "" {
		return 0, fmt.Errorf("%s needs the session that gave it", what)
	}
	if named := v.Task != "" || v.Step != 0; v.Issue != "" && (v.Task == "" || v.Step < 1) || v.Issue == "" && named {
		return 0, fmt.Errorf("%s names the step %q/%d for the issue %q: a Legion issue's verdict names the task and the step, from 1, of its review step, and a verdict no Legion issue owns names no step",
			what, v.Task, v.Step, v.Issue)
	}
	return insertID(ctx, tx, what, `insert into verdicts
		(repository, number, head, issue, task, step, verdict, session, recorded_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning id`,
		v.Repository.String(), v.Number, v.Head, v.Issue, v.Task, v.Step, string(v.Verdict), v.Session, v.RecordedAt)
}

// verdictColumns are the columns scanVerdict reads, in its order.
const verdictColumns = `id, repository, number, head, issue, task, step, verdict, session, recorded_at`

// scanVerdict reads the newest row the query answers (verdictColumns), and whether it answers one;
// what names the verdict in a failure.
func scanVerdict(row pgx.Row, what string) (Verdict, bool, error) {
	var v Verdict
	var stored, word string
	err := row.Scan(&v.ID, &stored, &v.Number, &v.Head, &v.Issue, &v.Task, &v.Step, &word, &v.Session, &v.RecordedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Verdict{}, false, nil
	}
	if err != nil {
		return Verdict{}, false, fmt.Errorf("read %s: %w", what, err)
	}
	v.Verdict = VerdictWord(word)
	if v.Repository, err = parseRepository("verdicts", v.ID, stored); err != nil {
		return Verdict{}, false, err
	}
	return v, true, nil
}

// VerdictTx is the newest verdict on a pull request at a head, of any step, and whether any row
// records one.
func VerdictTx(ctx context.Context, tx pgx.Tx, repository ghrepo.Repository, number int, head string) (Verdict, bool, error) {
	return scanVerdict(tx.QueryRow(ctx, `select `+verdictColumns+`
		from verdicts where lower(repository) = lower($1) and number = $2 and head = $3 order by id desc limit 1`,
		repository.String(), number, head), fmt.Sprintf("the verdict on %s#%d at %s", repository, number, head))
}

// StepVerdictTx is the newest verdict of the step at place step of task on a pull request at a
// head, and whether any row records one.
func StepVerdictTx(ctx context.Context, tx pgx.Tx, repository ghrepo.Repository, number int, head, task string, step int) (Verdict, bool, error) {
	return scanVerdict(tx.QueryRow(ctx, `select `+verdictColumns+`
		from verdicts where lower(repository) = lower($1) and number = $2 and head = $3 and task = $4 and step = $5 order by id desc limit 1`,
		repository.String(), number, head, task, step), fmt.Sprintf("the verdict of step %d of task %s on %s#%d at %s", step, task, repository, number, head))
}

// LastStepVerdictTx is the newest verdict of the step at place step of task on a pull request, at
// whichever head it names, and whether any row records one.
func LastStepVerdictTx(ctx context.Context, tx pgx.Tx, repository ghrepo.Repository, number int, task string, step int) (Verdict, bool, error) {
	return scanVerdict(tx.QueryRow(ctx, `select `+verdictColumns+`
		from verdicts where lower(repository) = lower($1) and number = $2 and task = $3 and step = $4 order by id desc limit 1`,
		repository.String(), number, task, step), fmt.Sprintf("the last verdict of step %d of task %s on %s#%d", step, task, repository, number))
}

// RecordVerdictWriteTx records one check run or commit status the daemon wrote and answers the
// row's id.
func RecordVerdictWriteTx(ctx context.Context, tx pgx.Tx, w VerdictWrite) (int64, error) {
	what := fmt.Sprintf("the write of legion-verdict on %s at %s", w.Repository, w.Head)
	switch {
	case w.Repository.IsZero():
		return 0, fmt.Errorf("%s needs its repository", what)
	case w.Head == "":
		return 0, fmt.Errorf("%s needs its head", what)
	case w.Kind != VerdictWriteCheckRun && w.Kind != VerdictWriteStatus:
		return 0, fmt.Errorf("%s is of kind %q, not %s or %s", what, w.Kind, VerdictWriteCheckRun, VerdictWriteStatus)
	case w.GitHubID <= 0:
		return 0, fmt.Errorf("%s needs GitHub's id of its %s, not %d", what, w.Kind, w.GitHubID)
	case w.Conclusion != VerdictConclusionSuccess && w.Conclusion != VerdictConclusionFailure:
		return 0, fmt.Errorf("%s concludes %q, not %s or %s", what, w.Conclusion, VerdictConclusionSuccess, VerdictConclusionFailure)
	case w.ForeignID < 0:
		return 0, fmt.Errorf("%s names the foreign id %d, which no run or status has", what, w.ForeignID)
	case w.WrittenAt.IsZero():
		return 0, fmt.Errorf("%s needs the time it was written", what)
	}
	return insertID(ctx, tx, what, `insert into verdict_writes
		(repository, head, kind, github_id, conclusion, foreign_id, written_at)
		values ($1, $2, $3, $4, $5, $6, $7) returning id`,
		w.Repository.String(), w.Head, string(w.Kind), w.GitHubID, string(w.Conclusion), w.ForeignID, w.WrittenAt)
}

// VerdictWritesTx is every write of legion-verdict the daemon recorded on a repository's head,
// oldest first.
func VerdictWritesTx(ctx context.Context, tx pgx.Tx, repository ghrepo.Repository, head string) ([]VerdictWrite, error) {
	what := fmt.Sprintf("read the writes of legion-verdict on %s at %s", repository, head)
	rows, err := tx.Query(ctx, `select id, repository, head, kind, github_id, conclusion, foreign_id, written_at
		from verdict_writes where lower(repository) = lower($1) and head = $2 order by id`, repository.String(), head)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	writes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (w VerdictWrite, err error) {
		var stored, kind, conclusion string
		if err = row.Scan(&w.ID, &stored, &w.Head, &kind, &w.GitHubID, &conclusion, &w.ForeignID, &w.WrittenAt); err != nil {
			return VerdictWrite{}, err
		}
		w.Kind, w.Conclusion = VerdictWriteKind(kind), VerdictConclusion(conclusion)
		w.Repository, err = parseRepository("verdict_writes", w.ID, stored)
		return w, err
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return writes, nil
}
