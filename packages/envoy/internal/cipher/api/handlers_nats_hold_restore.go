package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/sjawhar/envoy/internal/bus/signedrecord"
	"github.com/sjawhar/envoy/internal/cipher/natshold"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// holdRestoreBody is POST /v1/nats-hold/restore's body.
type holdRestoreBody struct {
	// The gen to restore at: one above every gen nats_hold_log holds, 1 for an empty log.
	Gen int64 `json:"gen"`
	// held, which only after a verified prepare or held log entry of its hold id, or released,
	// which only after a verified released entry, a prepare entry of the hold id it names, or a held
	// one whose hold's journal shows it ended (restoreAdmitted).
	Phase string `json:"phase"`
	// The hold's id, which a held row names.
	HoldID string `json:"hold_id"`
	// Why the operator restores the row.
	Reason string `json:"reason"`
	// When the operator signed the row, RFC 3339 at a whole microsecond.
	SignedAt string `json:"signed_at"`
	// The operator's signature over the row's lines at gen (signedrecord.HoldRow).
	Sig string `json:"sig"`
}

// errHoldLogUnknown is a restore refused because the log's latest entry cannot be judged by: it
// does not verify, or the log is empty under a row this broker served, read or wrote.
var errHoldLogUnknown = errors.New("the hold log's latest entry reads as unknown")

// restoreNATSHold writes the hold row while there is none, once its signature verifies, judging
// from the log's latest entry, which must verify (restoreTrusted), whether the row may be restored
// after it (restoreAdmitted). A latest entry that does not verify, or that this broker's floor of
// the row reads as a replay or a fork (a restore at or below the floor), is 409 below_floor and
// stores nothing. The row's signed_at is judged before the write (rowSignedAtAdmitted): one outside
// the allowance is 409 clock_skew, and nothing is stored.
func (s *server) restoreNATSHold(w http.ResponseWriter, r *http.Request, operator string) {
	var body holdRestoreBody
	if !readHoldBody(w, r, &body) {
		return
	}
	signedAt, ok := holdSignedAt(body.SignedAt)
	if body.Gen < 1 || !ok {
		writeHoldRefusal(w, http.StatusBadRequest, "bad_request", "gen is a gen, at least 1, and signed_at an RFC 3339 time at a whole microsecond", nil)
		return
	}
	if err := signedrecord.Verify(s.deps.NATSHold.Keys, body.Sig, signedrecord.HoldRow(uint64(body.Gen), body.Phase, body.HoldID, signedAt)); err != nil {
		writeHoldRefusal(w, http.StatusBadRequest, "bad_signature", "no key of BROKER_NATS_HOLD_SIGNING_KEYS signed the row at its gen", nil)
		return
	}
	admit := func(log []natshold.LogEntry, journals []natshold.Record, next natshold.Row) error {
		if err := s.rowSignedAtAdmitted(next.Gen, next.SignedAt, log); err != nil {
			return err
		}
		return s.restoreAdmitted(log, journals, next)
	}
	row, err := s.holdStore().Restore(r.Context(), natshold.Row{
		Gen: uint64(body.Gen), Phase: body.Phase, HoldID: body.HoldID, Reason: body.Reason, UpdatedBy: operator, SignedAt: signedAt, Sig: body.Sig,
	}, natshold.RestoreJudge{Trust: s.restoreTrusted, Admit: admit})
	var conflict *natshold.Conflict
	switch {
	case err == nil:
	case errors.Is(err, errRowSkewed):
		writeHoldRefusal(w, http.StatusConflict, "clock_skew", "the restore stores nothing: "+err.Error(), nil)
		return
	case errors.Is(err, errHoldLogUnknown), errors.Is(err, signedrecord.ErrReplay), errors.Is(err, signedrecord.ErrFork), errors.Is(err, signedrecord.ErrBackdated), errors.Is(err, natshold.ErrGenLogged):
		writeHoldRefusal(w, http.StatusConflict, "below_floor", "the restore stores nothing: "+err.Error(), nil)
		return
	case !errors.As(err, &conflict):
		writeUnavailable(w, "DATABASE_UNAVAILABLE", "restore the hold row", err)
		return
	case errors.Is(err, natshold.ErrBadTransition):
		s.writeHoldConflict(w, http.StatusBadRequest, "bad_transition", err.Error(), conflict)
		return
	case errors.Is(err, natshold.ErrHoldMismatch):
		s.writeHoldConflict(w, http.StatusConflict, "hold_mismatch", err.Error(), conflict)
		return
	case errors.Is(err, natshold.ErrRowExists):
		s.writeHoldConflict(w, http.StatusConflict, "row_exists", err.Error(), conflict)
		return
	case errors.Is(err, natshold.ErrGenMismatch):
		s.writeHoldConflict(w, http.StatusConflict, "gen_mismatch", err.Error(), conflict)
		return
	default:
		writeInternal(w, "restore the hold row", err)
		return
	}
	// The restored row is one above the log's latest entry, which the floor accepted, so it raises
	// the floor once it verifies as the store answered it.
	if err := s.acceptWritten(row); err != nil {
		writeHoldRefusal(w, http.StatusConflict, "below_floor", "the restored row reads as unknown: "+err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, holdRowAnswer(row))
}

// restoreTrusted is a restore's trust in the log's latest entry, before any gen is compared: the
// entry must verify against BROKER_NATS_HOLD_SIGNING_KEYS, since a database writer can add or
// change a log row but not one that verifies, and this broker's floor of the row must take it,
// which refuses an entry below a row the floor has accepted (so a restore at or below the floor)
// and another entry at the floor's gen. An empty log is trusted only by a floor that has accepted
// nothing: under a row this broker served, read or wrote, it is a log a writer emptied.
func (s *server) restoreTrusted(latest *natshold.LogEntry) error {
	if latest == nil {
		if s.holdRowFloor.Reached(1) {
			return fmt.Errorf("%w: the log is empty, under a hold row this broker has served, read or written", errHoldLogUnknown)
		}
		return nil
	}
	lines := signedrecord.HoldRow(latest.Gen, latest.Phase, latest.HoldID, latest.SignedAt)
	if err := signedrecord.Verify(s.deps.NATSHold.Keys, latest.Sig, lines); err != nil {
		return fmt.Errorf("%w: the entry at gen %d: %w", errHoldLogUnknown, latest.Gen, err)
	}
	return s.holdRowFloor.Accept(latest.Gen, latest.SignedAt, lines)
}

// restoreAdmitted is whether next may be restored after the log's latest entry, which verified
// (restoreTrusted), given journals, every hold's done record. An empty log takes only the first
// row's released. Released follows a released entry; a prepare entry, since a prepare row refuses
// no client, only with that entry's hold id (409 hold_mismatch otherwise, naming that hold): a
// released row under another id leaves the prepared hold's cohort and journal where no command
// reaches them, since Admit and the cohort delete need its id on the row and it cannot be prepared
// again; or a held one whose hold's journal, the done record, verifies and shows the hold ended (a
// done hold release or hold abort), as when the row was lost between that done entry and the row's
// move; after any other held entry it would free every client mid-hold. Whatever the latest entry,
// a released row is refused while a hold's own record holds it (heldByRecord): that hold is held
// by its own record, and a log a database writer emptied or cut back, on a replica whose floor
// started again, would otherwise free every client mid-hold. Held follows only a prepare or held
// entry of its own hold id, so a hold the log shows ended, by a release or by abort from prepare,
// which leaves no journal entry, is not held again.
func (s *server) restoreAdmitted(log []natshold.LogEntry, journals []natshold.Record, next natshold.Row) error {
	refuse := func(why string) error {
		return &natshold.Conflict{Err: fmt.Errorf("%w: %s", natshold.ErrBadTransition, why)}
	}
	var latest *natshold.LogEntry
	if len(log) > 0 {
		latest = &log[len(log)-1]
	}
	if next.Phase == string(natsgrant.OperatorPhaseReleased) {
		if err := s.heldByRecord(log, journals); err != nil {
			return err
		}
	}
	if latest == nil {
		if next.Phase == string(natsgrant.OperatorPhaseHeld) {
			return refuse("the log is empty, and a held row is restored only after a prepare or held entry of its own hold")
		}
		return nil
	}
	if next.Phase == string(natsgrant.OperatorPhaseHeld) {
		if (latest.Phase == string(natsgrant.OperatorPhasePrepare) || latest.Phase == string(natsgrant.OperatorPhaseHeld)) && latest.HoldID == next.HoldID {
			return nil
		}
		return refuse(fmt.Sprintf("the log's latest entry is %s with hold id %q, and a held row is restored only after a prepare or held entry of its own hold", latest.Phase, latest.HoldID))
	}
	switch latest.Phase {
	case string(natsgrant.OperatorPhasePrepare):
		if next.HoldID != latest.HoldID {
			return &natshold.Conflict{Err: fmt.Errorf("%w: the log's latest entry is prepare with hold id %q, and a released row after it names that hold, or the prepared hold's cohort and journal are left where no command reaches them", natshold.ErrHoldMismatch, latest.HoldID), HoldID: latest.HoldID}
		}
		return nil
	case string(natsgrant.OperatorPhaseHeld):
	default:
		return nil
	}
	if s.holdEnded(latest.HoldID, journalOf(journals, latest.HoldID)) {
		return nil
	}
	return refuse("the log's latest entry is held, and its hold's journal shows no done hold release or hold abort, so a released row restored after it would free every client mid-hold; restore the row held with its hold id")
}

// heldByRecord refuses a released restore while a hold's own record holds it. It skips a hold the
// verified log shows ended (endedInLog): a released entry naming it, at any gen, or a later hold's
// prepare after its own entries. Every other hold's done record is judged: one below this
// replica's record floor (an older signed version put back) or one that does not verify is unknown
// (409 below_floor), and one that shows hold set done and no end refuses the restore
// (ErrBadTransition, naming the hold). A log cut back below a hold's own entries shows nothing of
// that hold, so its record still refuses.
func (s *server) heldByRecord(log []natshold.LogEntry, journals []natshold.Record) error {
	ended := s.endedInLog(log)
	for i := range journals {
		holdID := journals[i].HoldID
		if ended(holdID) {
			continue
		}
		if s.holdFloors.below(recordFloorKey{holdID: holdID, kind: natshold.KindDone, key: holdJournalKey}, journals[i].Gen) {
			return fmt.Errorf("%w: hold %s's done record is at gen %d, below a gen this broker wrote or served", errHoldLogUnknown, holdID, journals[i].Gen)
		}
		d, ok := s.verifiedJournal(holdID, &journals[i])
		if !ok {
			return fmt.Errorf("%w: hold %s's done record does not verify", errHoldLogUnknown, holdID)
		}
		if _, setDone := d.Latest(holdrecord.CommandSet, holdrecord.StateDone); setDone && !journalEnded(d) {
			return &natshold.Conflict{Err: fmt.Errorf("%w: hold %s's journal shows hold set done and no end, so a released row restored now would free every client mid-hold; restore the row held with its hold id %s", natshold.ErrBadTransition, holdID, holdID)}
		}
	}
	return nil
}

// endedInLog answers, for a hold id, whether the log's verified entries show that hold ended: a
// released entry naming it, at any gen (its release or abort moved the row, and a hold id is never
// held again), or a prepare entry of another hold at a gen above every entry naming it (a later
// hold began). A hold no verified entry names is shown nothing.
func (s *server) endedInLog(log []natshold.LogEntry) func(holdID string) bool {
	released := map[string]bool{}
	last := map[string]uint64{}
	var prepares []natshold.LogEntry
	for _, e := range log {
		if e.HoldID == "" || !s.verifiedLogEntry(e) {
			continue
		}
		last[e.HoldID] = max(last[e.HoldID], e.Gen)
		switch e.Phase {
		case string(natsgrant.OperatorPhaseReleased):
			released[e.HoldID] = true
		case string(natsgrant.OperatorPhasePrepare):
			prepares = append(prepares, e)
		}
	}
	return func(holdID string) bool {
		if released[holdID] {
			return true
		}
		lastGen, named := last[holdID]
		return named && slices.ContainsFunc(prepares, func(p natshold.LogEntry) bool { return p.HoldID != holdID && p.Gen > lastGen })
	}
}

// journalOf is holdID's done record among journals, nil when there is none.
func journalOf(journals []natshold.Record, holdID string) *natshold.Record {
	for i := range journals {
		if journals[i].HoldID == holdID {
			return &journals[i]
		}
	}
	return nil
}

// holdEnded reports whether journal, holdID's done record, verifies and shows the hold ended
// (journalEnded).
func (s *server) holdEnded(holdID string, journal *natshold.Record) bool {
	d, ok := s.verifiedJournal(holdID, journal)
	return ok && journalEnded(d)
}

// journalEnded reports whether d shows the hold ended. It asks the journal's own Decide: it answers
// hold release ErrAlreadyDone only once the hold has ended, its first refusal (EndIsFinal).
func journalEnded(d holdrecord.Done) bool {
	return errors.Is(holdrecord.Decide(d, holdrecord.Request{Command: holdrecord.CommandRelease}), holdrecord.ErrAlreadyDone)
}

// verifiedJournal is journal, holdID's done record, decoded, when it is there and its signature
// verifies against BROKER_NATS_HOLD_SIGNING_KEYS, and false otherwise.
func (s *server) verifiedJournal(holdID string, journal *natshold.Record) (holdrecord.Done, bool) {
	if journal == nil || signedrecord.Verify(s.deps.NATSHold.Keys, journal.Sig, signedrecord.Record(holdID, natshold.KindDone, journal.Key, journal.Gen, journal.Value)) != nil {
		return holdrecord.Done{}, false
	}
	var d holdrecord.Done
	if json.Unmarshal(journal.Value, &d) != nil {
		return holdrecord.Done{}, false
	}
	return d, true
}
