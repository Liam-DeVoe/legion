package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// A claim's state is one of the fields Dispatch observes of an issue (record.StateFieldClaim, the
// state route's claim of each role of the issue), and every write of it converges in putClaim, as
// the role-holder publication's does. Once the daemon has turned the state events on
// (PublishState), the claim write that moves the state the route serves queues one state_change
// row in that write's own transaction, so the change and its event commit together or not at all,
// and the row takes its sequence and its time when the transaction commits (the commit trigger of
// migration 20261118164019_state_events). internal/record queues the rows of the issue and slot
// writes the same way (record.Postgres.PublishState).

// stateChangeKind is record.OutboxKindStateChange. This package cannot import record, whose tests
// import this package, so the kind and its payload's shape are written here once, and
// state_events_test.go reads every row this package queues back through record's decoder, which
// holds the two to one.
const stateChangeKind = "state_change"

// stateChange is the payload of that row, record.StateChange's shape: the Dispatch project key and
// the run that wrote the row, the issue the change is of, and each field the write moved.
type stateChange struct {
	Project string             `json:"project"`
	Run     string             `json:"run"`
	Issue   string             `json:"issue"`
	Changes []stateFieldChange `json:"changes"`
}

// stateFieldChange is record.StateFieldChange's shape: one field the write moved, its role where
// the field is a claim, and its old and new value as the state route serves them, null included.
type stateFieldChange struct {
	Field string          `json:"field"`
	Role  claim.Role      `json:"role,omitempty"`
	Old   json.RawMessage `json:"old"`
	New   json.RawMessage `json:"new"`
}

// The two fields a claim write can move: record.StateFieldPhase and record.StateFieldClaim.
const (
	stateFieldPhase = "phase"
	stateFieldClaim = "claim"
)

// unrecordedPhase is api.Unrecorded, as a state event carries it: the phase the state route
// serves an issue it lists for its claims alone, having no record of it. This package cannot
// import api, which imports this package.
var unrecordedPhase = json.RawMessage(`"unrecorded"`)

// jsonNull is the JSON null, the value of a field the route serves none of.
var jsonNull = json.RawMessage("null")

// statePublication is what PublishState turns the state events on with: the Dispatch project key
// every row is queued under, the run that writes them, and the wake told once a row commits.
type statePublication struct {
	project string
	run     string
	woken   func()
}

// PublishState turns the state events of the claim writes on: from this call on, each claim write
// that moves the state the state route serves at its issue's role queues one state_change row
// naming the claim with its role, in the write's own transaction, and woken is told once that
// transaction commits, where the daemon wakes the project's state lane so the row waits for no
// sweep. A transaction that rolls back tells nothing, since it queued nothing. project is the
// Dispatch project key, which the row's issue column holds so that every state row of the project
// runs on its one state lane (record.LaneState), and which the payload names; run is the id of
// the daemon run that writes the rows, the run the record store's events carry
// (record.Postgres.PublishState).
//
// Until it is called the store queues no state row, for the reason it queues no role-holder
// publication before PublishRoleHolders: only a daemon with a workflow runs the outbox that sends
// and deletes the rows, so only that daemon turns the events on.
func (s *Store) PublishState(project, run string, woken func()) {
	if woken == nil {
		woken = func() {}
	}
	s.stateQueued.Store(&statePublication{project: project, run: run, woken: woken})
}

// queuesState is the state events' switch as a write of claim c reads it: nil when the write can
// queue no state row, because the daemon has not turned the events on (PublishState) or because
// c is no claim a state event can name (statesClaim), and the switch otherwise. Neither depends on
// the stored row, so each is read before a transaction opens, and a write for which it is nil
// needs none for its state (PutClaim).
func (s *Store) queuesState(c supervise.Claim) *statePublication {
	if !statesClaim(c) {
		return nil
	}
	return s.stateQueued.Load()
}

// statesClaim is whether the state route shows claim c as the claim of a role of an issue, in the
// shape a state event names one: an issue key, and one of a claim's six roles (record's check of
// a state change). That holds of every claim the daemon makes (claim.NewToken), and of no claim
// that holds no role of an issue (holdsIssueRole): the controller's, which the route shows on no
// issue, writes none.
func statesClaim(c supervise.Claim) bool {
	return holdsIssueRole(c) && claim.IsIssueKey(c.Issue) && claim.IsRole(c.Role)
}

// queueClaimState queues, in tx, the state event of the write of claim c over stored, the row the
// write replaced (lockedClaim), and answers whether it queued one. The event names the claim with
// its role when the write moved its state, from the stored state, null where the store held no
// row yet, to c's; and it names the phase, from null to unrecorded, when the write is the first to
// list an issue the daemon holds no record of (storedClaim.unlisted), which the route serves at
// unrecorded from then on. A write that moved neither queues nothing; so a later claim write of
// that issue names the claim alone, since the route served its phase at unrecorded already.
func queueClaimState(ctx context.Context, tx pgx.Tx, state *statePublication, c supervise.Claim, stored storedClaim) (bool, error) {
	var changes []stateFieldChange
	if stored.unlisted {
		changes = append(changes, stateFieldChange{Field: stateFieldPhase, Old: jsonNull, New: unrecordedPhase})
	}
	if c.State != stored.state {
		changes = append(changes, stateFieldChange{Field: stateFieldClaim, Role: c.Role, Old: claimStateValue(stored.state), New: claimStateValue(c.State)})
	}
	if len(changes) == 0 {
		return false, nil
	}
	payload, err := json.Marshal(stateChange{Project: state.project, Run: state.run, Issue: c.Issue, Changes: changes})
	if err != nil {
		return false, fmt.Errorf("encode the state change of %s: %w", c.Issue, err)
	}
	// Due at the transaction's own time, as the record store's state rows are, with the columns
	// every outbox insert writes (record.Postgres.Enqueue).
	if _, err := tx.Exec(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error, lease_token, lease_until)
		values ($1, $2, $3::jsonb, 0, now(), '', '', null)`,
		stateChangeKind, state.project, string(payload)); err != nil {
		return false, fmt.Errorf("enqueue %s for %s: %w", stateChangeKind, c.Issue, err)
	}
	return true, nil
}

// claimStateValue is a claim's state as a state event carries it: the state string the route
// serves on the claim's view (api.ClaimViewOf), or null for the empty state, which is the old
// state of a claim the store held no row of (storedClaim), and which Dispatch reads as no value.
func claimStateValue(state supervise.ClaimState) json.RawMessage {
	if state == "" {
		return jsonNull
	}
	// A string always encodes.
	encoded, _ := json.Marshal(string(state))
	return encoded
}
