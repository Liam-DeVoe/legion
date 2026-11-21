package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// What a root architect's start task names of the decisions a person made while nobody in its
// tree could hear them (record.KeptDecisions, carried as supervise.Delivery.Kept and persisted as
// pending_task_deliveries.kept_decisions) is held by the delivery that task was queued as, and
// told at that delivery's confirmation. Those rows stand in parked_decisions throughout
// (20261101163106_parked_decisions): the claim's write of the delivery takes each from the start row that
// named it and marks it held by this claim and delivery id, the write recording the confirmation
// tells it, and the write that retires the delivery leaves it KEPT again. Each is the delivery's
// own write, so no ordering of queues, confirmations, retirements and restarts can leave a row
// held by a delivery that is not there or told by one whose turn never started.
//
// The telling follows the one validity rule both sides of this machinery follow: a row is told
// only while its own issue is still owed to the claim's issue, its gate root on the Dispatch chain
// (record.KeptMembers, R18 of dispatch://INFRA-3320), never by the tree the claim or the issue
// stores. An issue that has moved under another gate root — a change of parent, a park, a return
// — is owed its answer by that root now, so its row's claim and hold go instead and that root's
// next start or catch-up names it. The old architect having been prompted with it as well is the
// lesser fault: a person's decision is never lost.

// KeptMembersReader answers the issues whose kept rows are owed to the architect of issue now, on
// the Dispatch chain tx reads (the record's KeptMembers, R18 of dispatch://INFRA-3320). The store
// cannot read the chain itself, since the record package reads its own store through this one, so
// the daemon hands the reader over when it opens the store (ReadKeptMembers).
type KeptMembersReader func(ctx context.Context, tx pgx.Tx, issue string) ([]string, error)

// ReadKeptMembers hands the store the reader every delivery write that holds or tells a kept row
// reads its members with (heldMembers).
func (s *Store) ReadKeptMembers(read KeptMembersReader) {
	s.keptMembers.Store(&read)
}

// heldMembers is the issues whose kept rows are owed to the architect of token's issue now
// (KeptMembersReader): the members a delivery of that claim holds and tells rows of. A claim the
// store holds no row of, or one on no issue, holds none. A store no reader was handed refuses the
// write, since it cannot say what the delivery owns.
func (s *Store) heldMembers(ctx context.Context, tx pgx.Tx, token claim.Token) ([]string, error) {
	var issue string
	switch err := tx.QueryRow(ctx, `select issue from claims where token = $1`, string(token)).Scan(&issue); {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read the issue of %s: %w", token, err)
	}
	if issue == "" {
		return nil, nil
	}
	read := s.keptMembers.Load()
	if read == nil {
		return nil, fmt.Errorf("the store was handed no reader of kept members (ReadKeptMembers), so the decisions the delivery of %s names cannot be held or told", token)
	}
	return (*read)(ctx, tx, issue)
}

// putBackARetakenConfirmation puts back what this claim's own confirmation told, and does so on
// one unconfirmed write alone: the one that takes that confirmation back. The confirmation's
// write told the rows the delivery held and deleted them — a prompt refused busy after a turn of
// the agent's own confirmed it, a late refusal, a turn its process died in — so the architect
// never read them and the resend names them again; a second telling is the worst case, never a
// loss. It inserts, held by this delivery, each decision the delivery names that has no
// (issue, ref) row and whose issue stands in the claim's tree.
//
// Which write that is, is read from the row the write is about to replace: a delivery the claim
// holds now with a confirmation on it. So this runs before the upsert, since afterwards nothing
// says what the write undid, and the read takes that row's own lock, so no write of another
// transaction lands between the two. A claim whose row holds no delivery at all — retired, or
// never written — has no confirmation to take back, and a task that names nothing has nothing to
// put back.
//
// Every other unconfirmed write — the queue, the mark as sent, a write-back, a send a refusal
// took back — puts nothing back, because a row of theirs that is missing was told by somebody
// else: a catch-up of the tree named it while no delivery held it and its telling forgot it
// (record.Store.ForgetKeptDecisions). Putting such a row back makes it KEPT again, the tree's
// next start or catch-up names it, and a person's decision reaches one architect twice
// (dispatch://INFRA-2339). The send-time narrowing drops such a mark from the task (the daemon's
// rootTask), but only at a send, and a task the root cannot hear yet is written many times
// before one. An issue that left the claim's tree is never put back.
func (s *Store) putBackARetakenConfirmation(ctx context.Context, tx pgx.Tx, token claim.Token, delivery string, kept []byte) error {
	if len(kept) == 0 {
		return nil
	}
	var confirmed bool
	switch err := tx.QueryRow(ctx, `select confirmed_at is not null from pending_task_deliveries
		where claim_token = $1 for update`, string(token)).Scan(&confirmed); {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("read whether the delivery %s holds was confirmed: %w", token, err)
	}
	if !confirmed {
		return nil
	}
	members, err := s.heldMembers(ctx, tx, token)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into parked_decisions (issue, ref, decision, actor, at, held_by_claim, held_by_delivery)
		select named.issue, named.ref, named.decision, named.actor, named.at, $1, $2
		from jsonb_to_recordset(coalesce($3::jsonb -> 'decisions', '[]'::jsonb))
			as named (ref text, at timestamptz, issue text, decision text, actor text)
		where named.decision is not null and named.actor is not null and named.issue = any($4)
		on conflict (issue, ref) do nothing`,
		string(token), delivery, kept, members); err != nil {
		return fmt.Errorf("put back what the task queued on %s as delivery %s named and its confirmation told: %w", token, delivery, err)
	}
	return nil
}

// holdKeptDecisions marks every row the delivery names held by this claim and delivery id, and
// moves to this delivery id whatever the claim's previous delivery held — a task a death took back
// comes back under a new id, and what it names is the same (supervise's interrupted,
// takeBackPending). It takes only rows this delivery owns: one claimed by a start row whose task
// this claim queued (claims.delivered_rows, which the claim's write puts down first), one this
// claim already holds, or one nobody claims or holds whose issue is owed to this claim's issue
// (heldMembers: what this delivery names of its own root that nothing else has taken). A row
// another start, catch-up or delivery owns now — the gate root a moved issue stands under has
// claimed or held it — stays theirs.
//
// It puts no row back: a row the task names and the table does not hold is
// putBackARetakenConfirmation's, which runs before the write of the delivery this holds for and
// only where that write takes this claim's own confirmation back. A delivery that names nothing
// holds nothing.
func (s *Store) holdKeptDecisions(ctx context.Context, tx pgx.Tx, token claim.Token, delivery string, kept []byte) error {
	if len(kept) == 0 {
		return nil
	}
	members, err := s.heldMembers(ctx, tx, token)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update parked_decisions set held_by_delivery = $2
		where held_by_claim = $1 and held_by_delivery is not null and held_by_delivery <> $2`,
		string(token), delivery); err != nil {
		return fmt.Errorf("move what the task of %s held to delivery %s: %w", token, delivery, err)
	}
	if _, err := tx.Exec(ctx, `update readmitted_approvals set held_by_delivery = $2
		where held_by_claim = $1 and held_by_delivery is not null and held_by_delivery <> $2`,
		string(token), delivery); err != nil {
		return fmt.Errorf("move the approval the task of %s held to delivery %s: %w", token, delivery, err)
	}
	if _, err := tx.Exec(ctx, `update parked_decisions set claimed_by = null, held_by_claim = $1, held_by_delivery = $2
		from jsonb_to_recordset(coalesce($3::jsonb -> 'decisions', '[]'::jsonb)) as named (ref text, at timestamptz),
			claims holder
		where holder.token = $1
		and parked_decisions.ref = named.ref and parked_decisions.at = named.at
		and (parked_decisions.held_by_claim = $1 or parked_decisions.claimed_by = any(holder.delivered_rows)
			or (parked_decisions.claimed_by is null and parked_decisions.held_by_claim is null and parked_decisions.issue = any($4)))`,
		string(token), delivery, kept, members); err != nil {
		return fmt.Errorf("hold what the task queued on %s as delivery %s names: %w", token, delivery, err)
	}
	if _, err := tx.Exec(ctx, `update readmitted_approvals set claimed_by = null, held_by_claim = $1, held_by_delivery = $2
		from claims holder, (select ($3::jsonb ->> 'approved')::int as approved, ($3::jsonb ->> 'latest')::int as latest) as named
		where holder.token = $1 and readmitted_approvals.issue = holder.issue
		and readmitted_approvals.approved_version = named.approved and readmitted_approvals.latest_version = named.latest
		and (readmitted_approvals.held_by_claim = $1 or readmitted_approvals.claimed_by = any(holder.delivered_rows)
			or (readmitted_approvals.claimed_by is null and readmitted_approvals.held_by_claim is null))`,
		string(token), delivery, kept); err != nil {
		return fmt.Errorf("hold the approval the task queued on %s as delivery %s names: %w", token, delivery, err)
	}
	return nil
}

// tellKeptDecisions is the confirmation's own write: the turn carrying this delivery has started,
// so every row it holds whose issue is still owed to the claim's issue (heldMembers) has been told
// and is deleted, and every row whose issue has moved is KEPT again for the gate root it stands
// under now to name. A delivery that holds no row reads no chain.
func (s *Store) tellKeptDecisions(ctx context.Context, tx pgx.Tx, token claim.Token, delivery string) error {
	var holds bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from parked_decisions where held_by_claim = $1 and held_by_delivery = $2)
		or exists (select 1 from readmitted_approvals where held_by_claim = $1 and held_by_delivery = $2)`,
		string(token), delivery).Scan(&holds); err != nil {
		return fmt.Errorf("read what the delivery %s on %s holds: %w", delivery, token, err)
	}
	if !holds {
		return nil
	}
	members, err := s.heldMembers(ctx, tx, token)
	if err != nil {
		return err
	}
	for _, table := range []string{"parked_decisions", "readmitted_approvals"} {
		if _, err := tx.Exec(ctx, `delete from `+table+` where held_by_claim = $1 and held_by_delivery = $2 and issue = any($3)`,
			string(token), delivery, members); err != nil {
			return fmt.Errorf("tell what the delivery %s on %s held: %w", delivery, token, err)
		}
	}
	return releaseKeptDecisions(ctx, tx, token, delivery)
}

// releaseKeptDecisions clears the hold the delivery holds on every row, which is what a retired
// delivery and a told one's leftovers both leave behind: the row is KEPT, for the next start or
// catch-up of its own issue's tree to name. It clears the hold alone: a row is claimed or held,
// never both, and a claim is settled by the start or catch-up that owns it, never by a delivery's
// release.
func releaseKeptDecisions(ctx context.Context, db execer, token claim.Token, delivery string) error {
	for _, table := range []string{"parked_decisions", "readmitted_approvals"} {
		if _, err := db.Exec(ctx, `update `+table+` set held_by_claim = null, held_by_delivery = null
			where held_by_claim = $1 and held_by_delivery = $2`, string(token), delivery); err != nil {
			return fmt.Errorf("leave what the delivery %s on %s held kept: %w", delivery, token, err)
		}
	}
	return nil
}
