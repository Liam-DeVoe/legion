package delivery

// A pull request a `pull_request` event named, whose read of GitHub the intake could not finish,
// is held in pull_request_reads_owed until a read of GitHub answers for it (dispatch://INFRA-3992,
// R1 and R2). R2 makes GitHub's answer the only source of a head, so an event the intake gives up
// on would otherwise leave its pull request with no head for good: the reconcile reads only rows
// that exist, a merged or closed row is never read again, and the read-through runs only for a
// result's commit. The row holds which pull request changed and when that was recorded, and
// nothing an envelope says: no head, no state, no issue.
//
// The intake records one in two places. Intake.deliver records one before it acknowledges an
// envelope at the delivery cap (headWriteMaxDeliveries), which a GitHub outage of about five
// minutes reaches, and a rate limit after thirty of its waits; and recordPullRequestHead records
// one for GitHub's refusal of a read, a pull request it does not know or a repository the App
// cannot read, since GitHub's answer to a read made a moment after its own webhook may not know
// the pull request yet. A record that cannot be written leaves the envelope with NATS, handed
// back, so no envelope whose read did not succeed is acknowledged with no record of it. Every
// other way an envelope leaves the intake unacknowledged keeps it with NATS: a stop hands it back
// (Intake.deliver), and a hold that has lapsed leaves it outstanding for the next holder.
//
// The reconcile's head pass reads each owed pull request from GitHub first, the longest owed
// first, before the stored rows (Reconcile.readOwedPullRequests): an answer writes its row,
// creating it where none existed, and settles the read; a failure leaves it for the next pass, so
// it is read every pass until GitHub answers. A refusal settles it only once it has been owed
// owedReadRefusalFinalAfter, which bounds what a caller's envelope naming a pull request GitHub
// never had costs: the intake's read and one or two of the reconcile's.
//
// No reader of a head reads this table. The results read, the CI-proved checks and the delivery
// timeline read pull_request_heads and delivery_pull_requests alone, so an owed read is never a
// head, a basis or a merge anywhere; and a release before it neither reads nor writes the table.

import (
	"context"
	"fmt"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// owedReadRefusalFinalAfter is how long a pull request must have been owed a read before
// GitHub's refusal of the reconcile's read (a pull request it does not have, a repository the App
// cannot read) settles it. A refusal earlier than that may be GitHub's answer catching up with its
// own webhook, and is read again by the next pass. It is the reconcile's own period
// (ReconcileInterval), so a pull request GitHub never had is read by the reconcile at most twice.
const owedReadRefusalFinalAfter = ReconcileInterval

// owedReadWriteTimeout bounds the write of an owed read the intake makes at the delivery cap,
// which runs on a context of its own: the message's own deadline may be the very failure that
// spent the cap.
const owedReadWriteTimeout = 5 * time.Second

// owePullRequestRead records that the pull request number of the repository given names changed
// and that no read of GitHub has answered for that change. given is canonicalised first
// (refs.CanonicalRepo), so one pull request is owed under one name. Recording it again moves its
// time to now: the latest event is the one a later read must answer for, and a read the reconcile
// began before it settles nothing (settleOwedPullRequestRead).
func owePullRequestRead(ctx context.Context, pool *store.Pool, given string, number int) error {
	repoFull, err := refs.CanonicalRepo(ctx, pool, given)
	if err != nil {
		return err
	}
	if _, _, err := splitRepo(repoFull); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		insert into pull_request_reads_owed (repo, number) values ($1, $2)
		on conflict (repo, number) do update set owed_at = excluded.owed_at
	`, repoFull, number); err != nil {
		return fmt.Errorf("record the read %s#%d is owed: %w", repoFull, number, err)
	}
	return nil
}

// owedRead is one pull request owed a read: its repository, its number and when the read was
// last owed.
type owedRead struct {
	Repo   string
	Number int
	OwedAt time.Time
}

// listOwedPullRequestReads is at most limit pull requests owed a read, the longest owed first.
// The table holds a row only while GitHub has not answered for an event, so it is empty but for
// an outage, and the read is a scan of those rows.
func listOwedPullRequestReads(ctx context.Context, pool *store.Pool, limit int) ([]owedRead, error) {
	rows, err := pool.Query(ctx, `
		select repo, number, owed_at from pull_request_reads_owed
		order by owed_at, repo, number
		limit $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list the pull requests owed a read: %w", err)
	}
	defer rows.Close()
	var owed []owedRead
	for rows.Next() {
		var read owedRead
		if err := rows.Scan(&read.Repo, &read.Number, &read.OwedAt); err != nil {
			return nil, fmt.Errorf("list the pull requests owed a read: %w", err)
		}
		owed = append(owed, read)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the pull requests owed a read: %w", err)
	}
	return owed, nil
}

// settleOwedPullRequestRead deletes read once the reconcile's read of it is its answer: at once
// where GitHub answered for the pull request, and for a refusal only once the pull request has
// been owed owedReadRefusalFinalAfter. The delete names the time it read, so an event owed again
// while the reconcile read GitHub keeps its record for the next pass. Both times are the
// database's own clock.
func settleOwedPullRequestRead(ctx context.Context, pool *store.Pool, read owedRead, refused bool) error {
	if _, err := pool.Exec(ctx, `
		delete from pull_request_reads_owed
		where repo = $1 and number = $2 and owed_at = $3
		  and (not $4::boolean or owed_at < now() - make_interval(secs => $5))
	`, read.Repo, read.Number, read.OwedAt, refused, owedReadRefusalFinalAfter.Seconds()); err != nil {
		return fmt.Errorf("settle the read %s#%d is owed: %w", read.Repo, read.Number, err)
	}
	return nil
}
