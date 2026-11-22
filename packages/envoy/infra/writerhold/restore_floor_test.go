package writerhold

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus/signedrecord"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// endedHold is a buckets-create hold set without its steps and released in the stack: a hold whose
// journal is complete and whose released entry is in the row's log. It answers the hold's id and
// the gen of its released entry.
func (h *heldStack) endedHold() (string, uint64) {
	h.t.Helper()
	id := h.s.Prepare(h.t, holdrecord.WorkflowBucketsCreate)
	h.s.HoldWithoutTheSteps(h.t, id)
	h.s.Release(h.t, id)
	return id, h.row().Gen
}

// signedBy re-signs, with key, hold id's done record and every entry of the row's log at or below
// upTo: what that hold's commands wrote under a signing key since retired from the broker's list.
func (h *heldStack) signedBy(key ed25519.PrivateKey, id string, upTo uint64) {
	h.t.Helper()
	ctx := context.Background()
	pool := h.s.Broker.Pool
	rows, err := pool.Query(ctx, `select gen, phase, hold_id, signed_at from nats_hold_log where gen <= $1`, int64(upTo))
	require.NoError(h.t, err)
	type entry struct {
		gen           int64
		phase, holdID string
		signedAt      time.Time
	}
	var entries []entry
	for rows.Next() {
		var e entry
		require.NoError(h.t, rows.Scan(&e.gen, &e.phase, &e.holdID, &e.signedAt))
		entries = append(entries, e)
	}
	rows.Close()
	require.NoError(h.t, rows.Err())
	require.NotEmpty(h.t, entries, "setup: no log entry to re-sign")
	for _, e := range entries {
		sig, err := signedrecord.Sign(key, signedrecord.HoldRow(uint64(e.gen), e.phase, e.holdID, e.signedAt))
		require.NoError(h.t, err)
		_, err = pool.Exec(ctx, `update nats_hold_log set sig = $2 where gen = $1`, e.gen, sig)
		require.NoError(h.t, err)
	}
	var value string
	var gen int64
	require.NoError(h.t, pool.QueryRow(ctx, `select value::text, gen from nats_hold_records where hold_id = $1 and kind = 'done'`, id).Scan(&value, &gen))
	sig, err := signedrecord.Sign(key, signedrecord.Record(id, "done", "hold", uint64(gen), []byte(value)))
	require.NoError(h.t, err)
	_, err = pool.Exec(ctx, `update nats_hold_records set sig = $2 where hold_id = $1 and kind = 'done'`, id, sig)
	require.NoError(h.t, err)
}

// A released restore judged against a hold's journal signed by a key the broker no longer lists
// (#4679 round 8: 409 below_floor naming the hold and the key's id) is UNKNOWN (exit 2) in `hold
// restore`, which prints the code and the broker's message, so the operator reads the key id and
// the repair: the retired key goes back in BROKER_NATS_HOLD_SIGNING_KEYS, verify-only. Hold A ran
// under the retired key and hold B after it under the listed one, so the log's latest entry
// verifies and the route judges A's journal. Nothing is written, and no held restore is sent. With
// the key listed again, the same restore stores the released row. The message never holds the key
// itself. Control: restore at 449251ffc3, which printed the code alone, so neither the hold nor the
// key id.
func TestHoldARestoreOverAJournalOfARetiredKeyIsUnknownNamingTheKey(t *testing.T) {
	h := newHeldStack(t)
	id, end := h.endedHold()
	h.endedHold()
	retired, retiredKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	h.signedBy(retiredKey, id, end)
	h.deleteRow()
	h.s.Broker.Restart()

	code, stdout, stderr := h.run(actionRestore, "--reason", "the row is gone")
	require.Equal(t, 2, code, "restore over a journal of a retired key: %s%s", stdout, stderr)
	require.Contains(t, stderr, "UNKNOWN")
	require.Contains(t, stderr, "below_floor")
	require.Contains(t, stderr, id, "the broker's message, which names the hold, is not printed")
	require.Contains(t, stderr, signedrecord.KeyID(retired), "the broker's message, which names the key id, is not printed")
	require.Contains(t, stderr, "verify-only")
	require.NotContains(t, stderr, base64.RawURLEncoding.EncodeToString(retired), "the refusal holds the retired key itself")
	_, err = h.s.Records.Row(context.Background())
	require.Error(t, err, "the refused restore wrote a row")

	h.s.Broker.RestartListing(retired)
	code, _, stderr = h.run(actionRestore, "--reason", "the retired key listed verify-only")
	require.Zero(t, code, "restore with the retired key listed verify-only: %s", stderr)
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
}

// A done record a database writer added unsigned, under a hold id no command used, refuses every
// released restore (#4679 round 8: 409 below_floor naming that record and the hand repair, since
// no route can tell it from a live hold's broken record). `hold restore` reads it as UNKNOWN (exit
// 2) and prints the code and the broker's message, which names the record's hold and the repair:
// a database admin deletes the rows it names. After that delete the restore stores the released
// row. Control: restore at 449251ffc3, which printed the code alone.
func TestHoldARestoreOverAnUnsignedDoneRecordIsUnknownNamingTheRepair(t *testing.T) {
	h := newHeldStack(t)
	h.endedHold()
	ctx := context.Background()
	_, err := h.s.Broker.Pool.Exec(ctx, `insert into nats_hold_records (hold_id, kind, key, value, gen, written_at, sig) values ('ghost', 'done', 'hold', '{}'::jsonb, 1, now(), 'garbage')`)
	require.NoError(t, err)
	h.deleteRow()

	code, stdout, stderr := h.run(actionRestore, "--reason", "the row is gone")
	require.Equal(t, 2, code, "restore over an unsigned done record: %s%s", stdout, stderr)
	require.Contains(t, stderr, "UNKNOWN")
	require.Contains(t, stderr, "below_floor")
	require.Contains(t, stderr, "ghost", "the broker's message, which names the record's hold, is not printed")
	require.Contains(t, stderr, "a database admin deletes or restores the rows named here", "the broker's message, which names the repair, is not printed")
	_, err = h.s.Records.Row(ctx)
	require.Error(t, err, "the refused restore wrote a row")

	_, err = h.s.Broker.Pool.Exec(ctx, `delete from nats_hold_records where hold_id = 'ghost' and kind = 'done'`)
	require.NoError(t, err)
	code, _, stderr = h.run(actionRestore, "--reason", "after the hand repair")
	require.Zero(t, code, "restore after the hand repair: %s", stderr)
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
}
