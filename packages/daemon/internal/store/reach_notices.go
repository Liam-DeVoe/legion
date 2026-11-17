package store

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// ReachProjectNotice is the project's own record of one of the reach keeper's notices that speak
// for no claim's reach record (R3, R5): the notice that Envoy lists no live session, one for the
// project, and the notice of a claim's newer process locked out by an older one, one for each claim
// (the table reach_project_notices). A per-claim notice's told mark is the claim's reach record
// (PutClaimReachTx); these have no such record, so they have this one.
//
// Told is whether the controller's notice was queued, written in that notice's own transaction
// (PutReachProjectNoticeTx), so a daemon that ends after it never tells it again. A lock-out
// notice the controller's lane did not queue is written with Told false: it is kept, for the next
// daemon to tell while its worker stream still refuses the same newer process. Kind is the
// notice's kind, Claim the claim a lock-out is of ("" for the no-live notice), Newer and Older
// the two generations a lock-out names, Issue the issue the notice is queued on, and Notice the
// notice as the controller is told it, encoded.
type ReachProjectNotice struct {
	Project string
	Kind    string
	Claim   claim.Token
	Newer   uint64
	Older   uint64
	Issue   string
	Notice  []byte
	Told    bool
}

// ReachProjectNotices is every record of project's own reach notices.
func (s *Store) ReachProjectNotices(ctx context.Context, project string) ([]ReachProjectNotice, error) {
	rows, err := s.pool.Query(ctx, `select kind, claim_token, newer, older, issue, notice, told_at is not null
		from reach_project_notices where project = $1 order by kind, claim_token`, project)
	if err != nil {
		return nil, fmt.Errorf("read the reach notices of %s: %w", project, err)
	}
	defer rows.Close()
	var notices []ReachProjectNotice
	for rows.Next() {
		n := ReachProjectNotice{Project: project}
		var token string
		var newer, older int64
		if err := rows.Scan(&n.Kind, &token, &newer, &older, &n.Issue, &n.Notice, &n.Told); err != nil {
			return nil, fmt.Errorf("read the reach notices of %s: %w", project, err)
		}
		n.Claim, n.Newer, n.Older = claim.Token(token), uint64(newer), uint64(older)
		notices = append(notices, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the reach notices of %s: %w", project, err)
	}
	return notices, nil
}

// PutReachProjectNotice writes n in a statement of its own: a lock-out notice kept, untold.
func (s *Store) PutReachProjectNotice(ctx context.Context, n ReachProjectNotice) error {
	return putReachProjectNotice(ctx, s.pool, n)
}

// PutReachProjectNoticeTx writes n through the caller's transaction: the transaction of the
// notice it marks told, so the notice and its record commit together or not at all.
func PutReachProjectNoticeTx(ctx context.Context, tx pgx.Tx, n ReachProjectNotice) error {
	return putReachProjectNotice(ctx, tx, n)
}

// DeleteReachProjectNotice deletes the record of project's notice of kind, of claim token ("" for
// the no-live notice): a read that found a live session ends the no-live run it was told of, and
// a lock-out that ended untold leaves nothing to keep.
func (s *Store) DeleteReachProjectNotice(ctx context.Context, project, kind string, token claim.Token) error {
	if _, err := s.pool.Exec(ctx, `delete from reach_project_notices where project = $1 and kind = $2 and claim_token = $3`,
		project, kind, string(token)); err != nil {
		return fmt.Errorf("delete the %s notice record of %s: %w", kind, project, err)
	}
	return nil
}

func putReachProjectNotice(ctx context.Context, db execer, n ReachProjectNotice) error {
	if n.Newer > math.MaxInt64 || n.Older > math.MaxInt64 {
		return fmt.Errorf("put the %s notice record of %s: a generation does not fit a bigint", n.Kind, n.Project)
	}
	if _, err := db.Exec(ctx, `insert into reach_project_notices (project, kind, claim_token, newer, older, issue, notice, told_at)
		values ($1, $2, $3, $4, $5, $6, $7, case when $8::boolean then now() end)
		on conflict (project, kind, claim_token) do update set newer = excluded.newer, older = excluded.older,
			issue = excluded.issue, notice = excluded.notice, told_at = excluded.told_at`,
		n.Project, n.Kind, string(n.Claim), int64(n.Newer), int64(n.Older), n.Issue, n.Notice, n.Told); err != nil {
		return fmt.Errorf("put the %s notice record of %s: %w", n.Kind, n.Project, err)
	}
	return nil
}
