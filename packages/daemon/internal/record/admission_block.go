package record

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AdmissionBlock is a project's admission block: the cap and the five lists GET /legion/v1/state
// serves under `admission` (api.Admission, which is built from this type) and a state_admission
// event carries (StateAdmission). One judgement of one read of the project computes it
// (blockers.JudgeAdmission), which the state route and the admission pass both call, so the block
// the route serves and the block the event carries are the same value.
//
// It is declared here, below both readers, because the admission pass that writes the event cannot
// import api. Its JSON is the route's own: the keys api.Admission writes, in its order, and every
// list an array, never null (MarshalJSON). No field holds a time, so a block judged twice over the
// same records encodes the same. One word of the held list does read the clock: HeldTreeClosed
// asks whether a root's linger still runs at the pass's moment (LineFacts.Now), so a member of a
// parked tree moves from tree_closed to tree_parked when that linger runs out, with no write, and
// the next pass writes that change.
type AdmissionBlock struct {
	Cap     int            `json:"cap"`
	Active  []string       `json:"active"`
	Waiting []string       `json:"waiting"`
	Trees   []string       `json:"trees"`
	Blocked []BlockedIssue `json:"blocked"`
	Held    []HeldIssue    `json:"held"`
}

// MarshalJSON keeps every list an array on the wire, empty rather than `null`, as api.Admission
// writes them for the caller the route serves the whole block to.
func (b AdmissionBlock) MarshalJSON() ([]byte, error) {
	type wire AdmissionBlock
	out := wire(b)
	if out.Active == nil {
		out.Active = []string{}
	}
	if out.Waiting == nil {
		out.Waiting = []string{}
	}
	if out.Trees == nil {
		out.Trees = []string{}
	}
	if out.Blocked == nil {
		out.Blocked = []BlockedIssue{}
	}
	if out.Held == nil {
		out.Held = []HeldIssue{}
	}
	return json.Marshal(out)
}

// QueueAdmissionBlock writes block as project's admission block (state_admission_blocks) where it
// differs from the block stored there, and queues the state event of that change (StateAdmission,
// carrying the run the state events were turned on with, PublishState) in the same statement,
// from the upsert's returning: a block equal to the stored one writes nothing and queues nothing,
// and neither does any block while the state events are off. The two are compared as jsonb, so no
// key order or spacing can make two equal blocks differ. It answers whether it queued the event,
// and tells the state events it did as every write that queues one does.
//
// The block is sent once, and the event's payload is built in the statement from the row the
// upsert wrote, with the project and the run checked here as NewOutboxRow checks every state row's
// (validateStateRow): almost every pass writes nothing, so it encodes nothing else. The row's issue
// column is project, the bare Dispatch project key, so it runs on the project's one state lane
// (LaneState) as a StateChange does (queueStateChange), and the commit trigger of migration
// 20261118164019_state_events gives it its sequence and its time when tx commits. It is due at the
// transaction's own time, now().
func (s *Postgres) QueueAdmissionBlock(ctx context.Context, tx pgx.Tx, project string, block AdmissionBlock) (bool, error) {
	state := s.state.Load()
	if state == nil {
		return false, nil
	}
	if err := validateStateRow(OutboxKindStateAdmission, project, state.run); err != nil {
		return false, fmt.Errorf("queue the admission block of %s: %w", project, err)
	}
	encoded, err := json.Marshal(block)
	if err != nil {
		return false, fmt.Errorf("queue the admission block of %s: encode it: %w", project, err)
	}
	tag, err := tx.Exec(ctx, `with written as (
			insert into state_admission_blocks as stored (project, block) values ($1, $2::jsonb)
			on conflict (project) do update set block = excluded.block
				where stored.block is distinct from excluded.block
			returning project, block)
		insert into outbox (kind, issue, payload, attempts, next_at, last_error, lease_token, lease_until)
		select $3::text, written.project, jsonb_build_object('project', written.project, 'run', $4::text, 'admission', written.block),
			0, now(), '', '', null from written`,
		project, string(encoded), string(OutboxKindStateAdmission), state.run)
	if err != nil {
		return false, fmt.Errorf("queue the admission block of %s: %w", project, err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	state.queued(ctx)
	return true, nil
}
