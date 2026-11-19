package docs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// BackgroundQuiet is what AwaitBackgroundWrites answers of a Service's background writes: whether
// it waited for any, and the earliest moment a write it left armed for later may begin, with what
// that write is. At is zero when nothing is armed.
type BackgroundQuiet struct {
	Waited bool
	At     time.Time
	Writer string
}

// backgroundPoll is how often AwaitBackgroundWrites looks again at a write it waits for.
const backgroundPoll = 10 * time.Millisecond

// AwaitBackgroundWrites waits, within ctx, for every write the service runs in the background that
// is running now or armed to begin by soon, and answers when the first one it left armed for later
// may begin. Nothing in production calls it: a test that reads the whole database before and after
// a request (the API tests' databaseFingerprint) calls it so that no write of this service lands
// between its two reads unseen.
//
// It waits for a settlement whose timer is due by soon or has fired and whose callback still runs
// (settleTimer, settleCallbacks), and one a caller runs in a timer's place (roomState.settling);
// for every forced eviction (evicting); for every room's updates its persistence has not taken or
// has not stored (pendingUpdates, durableAppends), which a settlement's sweep of unrecorded marks
// leaves behind it; and for the compaction step in flight. One of them can arm another - a
// settlement retries, a stored update arms a settlement - so it looks again until one look finds
// none.
//
// What it leaves armed it answers rather than waits for: a settlement armed for after soon, and a
// compaction, which folds only rows older than compactTail (compactRule): a room's close folds any
// two such rows, and the queue's step folds a room only once it is due (compactionCheck), or on one
// such row when the step is a retry (tryCompaction). At is the earliest moment one of them may begin
// to write. The service's other background work writes no row: the awareness relay's sends and a
// room query's answer are notifications (pg_notify), and warming the appends pool takes and gives
// back one connection. The loops cmd/dispatch starts (RunEditNotices, RunSettlementResumption,
// RunCompactionSweep and the others) run only where their caller starts them. A service whose
// process registered room sync (RegisterRoomSync) is refused: another process's notification can
// make it write at any moment.
func (s *Service) AwaitBackgroundWrites(ctx context.Context, soon time.Time) (BackgroundQuiet, error) {
	if s.roomSyncRegistered.Load() {
		return BackgroundQuiet{}, errors.New("this document service runs room sync (RegisterRoomSync): another process's notification can make it write at any moment, which no wait of this process sees")
	}
	var quiet BackgroundQuiet
	look := time.NewTimer(0)
	defer look.Stop()
	for {
		writer := s.runningBackgroundWrite(soon)
		if writer == "" {
			break
		}
		quiet.Waited = true
		look.Reset(backgroundPoll)
		select {
		case <-ctx.Done():
			return quiet, fmt.Errorf("%s did not end: %w", writer, ctx.Err())
		case <-look.C:
		}
	}
	at, writer, err := s.nextBackgroundWrite(ctx)
	quiet.At, quiet.Writer = at, writer
	return quiet, err
}

// runningBackgroundWrite names a background write of the service that runs now or is due by soon,
// and answers "" when there is none.
func (s *Service) runningBackgroundWrite(soon time.Time) string {
	if s.settleCallbacks.Load() > 0 {
		return "a document settlement whose timer fired"
	}
	s.timerMu.Lock()
	for _, armed := range s.timers {
		if !armed.due.After(soon) {
			s.timerMu.Unlock()
			return fmt.Sprintf("the settlement of document %s armed for %s", armed.room, armed.due.Format(time.RFC3339Nano))
		}
	}
	s.timerMu.Unlock()
	if s.evicting.Load() > 0 {
		return "a forced eviction of a document room"
	}
	if _, stepping := s.compactions.held(); stepping {
		return "a document compaction"
	}
	var busy string
	s.rooms.Range(func(key, value any) bool {
		room, state := key.(string), value.(*roomState)
		state.mu.Lock()
		switch {
		case state.settling > 0:
			busy = "the settlement of document " + room
		case state.pendingLocked() || state.appendingLocked():
			busy = "the store of document " + room + "'s room updates"
		}
		state.mu.Unlock()
		return busy == ""
	})
	return busy
}

// nextBackgroundWriteSQL answers, for each room $1 names, how many seconds from the statement's
// now() its second doc_updates row, and its ($2 + 1)th, leave compactTail behind them: the first
// moment a fold may take two rows (a close's, or a retry's), and the first moment the room holds
// compactMinRows rows past its first that are older than compactTail (the queue's due). Each is null
// when the room holds no such row. The second row is also what compactMaxTailAge ages.
const nextBackgroundWriteSQL = `
	select r.room,
		extract(epoch from (
			select u.created_at from doc_updates u where u.artifact_id::text = r.room
			order by u.version offset 1 limit 1) - now()),
		extract(epoch from (
			select u.created_at from doc_updates u where u.artifact_id::text = r.room
			order by u.version offset $2 limit 1) - now())
	from unnest($1::text[]) as r(room)`

// nextBackgroundWrite answers the earliest moment a write the service holds armed may begin, and
// what it is: the settlement timer due first, and the first compaction that may fold a resident
// room's rows (a close's, which ygo's idle sweeper may run at any moment) or a queued room's (the
// queue's step, at its time and only on a room due). It answers the zero time when nothing is armed.
func (s *Service) nextBackgroundWrite(ctx context.Context) (time.Time, string, error) {
	var at time.Time
	var writer string
	take := func(when time.Time, what string) {
		if at.IsZero() || when.Before(at) {
			at, writer = when, what
		}
	}
	s.timerMu.Lock()
	for _, armed := range s.timers {
		take(armed.due, fmt.Sprintf("the settlement of document %s armed for %s", armed.room, armed.due.Format(time.RFC3339Nano)))
	}
	s.timerMu.Unlock()

	resident := map[string]bool{}
	s.rooms.Range(func(key, _ any) bool {
		if room := key.(string); s.srv.GetDoc(room) != nil {
			resident[room] = true
		}
		return true
	})
	queued, _ := s.compactions.held()
	rooms := make([]string, 0, len(resident)+len(queued))
	for room := range resident {
		rooms = append(rooms, room)
	}
	for _, held := range queued {
		if !resident[held.room] {
			rooms = append(rooms, held.room)
		}
	}
	if len(rooms) == 0 {
		return at, writer, nil
	}
	asked := time.Now()
	rows, err := s.store.Pool.Query(ctx, nextBackgroundWriteSQL, rooms, compactMinRows)
	if err != nil {
		return at, writer, fmt.Errorf("read when a document compaction may fold rows: %w", err)
	}
	defer rows.Close()
	type ages struct{ second, due *float64 }
	aged := map[string]ages{}
	for rows.Next() {
		var room string
		var age ages
		if err := rows.Scan(&room, &age.second, &age.due); err != nil {
			return at, writer, fmt.Errorf("read when a document compaction may fold rows: %w", err)
		}
		aged[room] = age
	}
	if err := rows.Err(); err != nil {
		return at, writer, fmt.Errorf("read when a document compaction may fold rows: %w", err)
	}
	// From asked, which comes before the statement's now(), so each moment reads no later than it is.
	past := func(seconds *float64, beyond time.Duration) (time.Time, bool) {
		if seconds == nil {
			return time.Time{}, false
		}
		return asked.Add(time.Duration(*seconds*float64(time.Second)) + beyond), true
	}
	fold := func(room string, when time.Time, how string) {
		take(when, fmt.Sprintf("a compaction of document %s (%s), which may fold its rows older than %s", room, how, compactTail))
	}
	for room := range resident {
		if when, ok := past(aged[room].second, compactTail); ok {
			fold(room, when, "its room's close")
		}
	}
	for _, held := range queued {
		age := aged[held.room]
		var due time.Time
		if held.retrying {
			when, ok := past(age.second, compactTail)
			if !ok {
				continue
			}
			due = when
		} else {
			many, manyOK := past(age.due, compactTail)
			old, oldOK := past(age.second, compactMaxTailAge)
			switch {
			case manyOK && oldOK:
				if many.Before(old) {
					due = many
				} else {
					due = old
				}
			case manyOK:
				due = many
			case oldOK:
				due = old
			default:
				continue
			}
		}
		if due.Before(held.at) {
			due = held.at
		}
		fold(held.room, due, "the compaction queue's step")
	}
	return at, writer, nil
}
