package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/bus/signedrecord"
	"github.com/sjawhar/envoy/internal/cipher/machine"
	"github.com/sjawhar/envoy/internal/cipher/natshold"
	"github.com/sjawhar/envoy/internal/ciphertoken"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// NATSHold is what the NATS writer hold's routes need: the tables they write
// (internal/cipher/natshold, migration 0041_nats_hold), the operator's public keys every write
// must be signed by, and the one caller they admit, the operator entry of BROKER_NATS_SERVICES.
// The routes are the only writer of those tables a deployment runs: the operator's commands
// (envoy-infra hold, through internal/holdrecord) call them, and the auth service reads the hold
// row they write at every decision while BROKER_NATS_HOLD_READ is true.
type NATSHold struct {
	// Pool is the broker's Postgres pool, whose schema holds the hold's tables.
	Pool *pgxpool.Pool
	// Schema is the schema the hold's tables are in, which every statement names them by: the one
	// the broker's migrations ran in, public in a deployment, and a test's own in a test.
	Schema string
	// Keys are BROKER_NATS_HOLD_SIGNING_KEYS. A write whose signature none of them verifies is
	// 400 bad_signature, so with none every write is.
	Keys []ed25519.PublicKey
	// OperatorToken verifies a platform identity token for the NATS audiences, as the auth service
	// verifies a service's (cmd/cipherd passes natscallout.PlatformTokens.Verify), and answers the
	// account it names, whether that is a Google account, and when the token was issued and
	// expires. Nil, a broker with no NATS audience, admits no caller.
	OperatorToken func(ctx context.Context, raw string) (account string, google bool, issued, expiry time.Time, err error)
	// OperatorAccount and OperatorGoogle are the operator entry's account and whether it is a
	// Google account: the one caller the routes admit. An empty OperatorAccount, a list with no
	// operator entry, admits none.
	OperatorAccount string
	OperatorGoogle  bool
	// RowFloor is this process's one floor of the hold row (signedrecord.Floor): the routes put
	// every verified row they serve, write or restore through it, and the auth service's hold view
	// (natscallout.NewHoldView) puts every row it reads through the same one, so neither half of the
	// process trusts a row the other has moved past. cmd/cipherd makes one; nil gives the routes a
	// floor of their own, as a test that runs no view does.
	RowFloor *signedrecord.Floor
}

// longestOperatorToken is the longest life the operator's token may have from its issue, the bound
// the auth service holds every service's platform token to (natscallout's longestServiceToken): a
// token login's, beside the proof clock skew. The operator's commands mint ten minutes.
const longestOperatorToken = machine.MaxTokenLoginLifetime + ciphertoken.ProofClockSkew

// operatorCaller authenticates the operator's own platform token for authenticate, the one
// credential operatorAuth's routes take: verified as the auth service verifies a service's, no
// longer lived than an hour from its issue and not expired, naming the operator entry's account.
// Anything else is 403 not_operator, whatever was wrong with it, so a caller learns nothing of the
// operator's identity from the refusal, and none repeats the token.
func (s *server) operatorCaller(w http.ResponseWriter, r *http.Request) (caller, bool) {
	hold := s.deps.NATSHold
	if hold.OperatorToken == nil || hold.OperatorAccount == "" {
		writeHoldRefusal(w, http.StatusForbidden, "not_operator", "this broker admits no operator: BROKER_NATS_SERVICES lists no operator entry, or no NATS audience verifies its token", nil)
		return caller{}, false
	}
	account, google, issued, expiry, err := hold.OperatorToken(r.Context(), bearer(r))
	switch {
	case err != nil:
		writeHoldRefusal(w, http.StatusForbidden, "not_operator", "the caller's token does not verify as a platform token of the NATS audiences", nil)
		return caller{}, false
	case issued.IsZero() || expiry.Sub(issued) > longestOperatorToken:
		writeHoldRefusal(w, http.StatusForbidden, "not_operator", "the caller's token names no issue time or lives longer than an hour from it", nil)
		return caller{}, false
	case !expiry.After(time.Now()):
		writeHoldRefusal(w, http.StatusForbidden, "not_operator", "the caller's token has expired", nil)
		return caller{}, false
	case account != hold.OperatorAccount || google != hold.OperatorGoogle:
		writeHoldRefusal(w, http.StatusForbidden, "not_operator", "the caller's token names an account other than the operator entry's", nil)
		return caller{}, false
	}
	return caller{operator: account}, true
}

// holdStore is the store of the hold's tables the routes write.
func (s *server) holdStore() natshold.Store {
	return natshold.Store{Pool: s.deps.NATSHold.Pool, Schema: s.deps.NATSHold.Schema}
}

// holdRow is the writer hold's row as every route answers it, in the request and refusal bodies
// alike: every column the operator's commands verify the row by, with its signature.
type holdRow struct {
	// The row's gen, which only goes up: one above the gen a move named, and on a restore one
	// above every gen nats_hold_log holds.
	Gen int64 `json:"gen"`
	// released, prepare or held.
	Phase string `json:"phase"`
	// The hold's id: the one a prepare began, which held and the release after it keep.
	HoldID string `json:"hold_id"`
	// Why the operator wrote the row.
	Reason string `json:"reason"`
	// When the broker stored the row, RFC 3339 in UTC.
	UpdatedAt string `json:"updated_at"`
	// The account whose token wrote the row: the operator entry's.
	UpdatedBy string `json:"updated_by"`
	// When the operator signed the row, RFC 3339 with nanoseconds in UTC, the text its signature's
	// lines carry (signedrecord.HoldRow).
	SignedAt string `json:"signed_at"`
	// The operator's Ed25519 signature over the row's lines, base64url without padding.
	Sig string `json:"sig"`
}

// holdRowAnswer is row as the routes answer it.
func holdRowAnswer(row natshold.Row) holdRow {
	return holdRow{
		Gen: int64(row.Gen), Phase: row.Phase, HoldID: row.HoldID, Reason: row.Reason,
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano), UpdatedBy: row.UpdatedBy,
		SignedAt: row.SignedAt.UTC().Format(time.RFC3339Nano), Sig: row.Sig,
	}
}

// holdRefusal is every refusal of the hold's routes: its code, in error as RFC 6749 and the
// operator's client read it, and what was refused, with the hold row it was refused against.
type holdRefusal struct {
	// The refusal's code: bad_request, too_large, bad_signature, bad_transition, not_operator,
	// no_row, gen_mismatch, hold_mismatch, row_exists, below_floor or clock_skew.
	Error string `json:"error"`
	// What was refused.
	Message string `json:"message"`
	// The hold row as the refused write read it, as GET /v1/nats-hold answers it, on a 409 the row
	// decides; absent when there is no row.
	Current *holdRow `json:"current,omitempty"`
	// On POST /v1/nats-hold/restore's 409 gen_mismatch, the gen a restore must carry now, one above
	// every gen nats_hold_log holds, which the operator signs afresh; absent otherwise.
	RestoreGen *int64 `json:"restore_gen,omitempty"`
	// On a 409 hold_mismatch, the hold id the hold row or log names, which the refused write must
	// name instead (a restore after a prepare entry names that entry's hold); absent otherwise. The
	// operator's client reads it as RouteError.HoldID, since no route lists the log.
	HoldID string `json:"hold_id,omitempty"`
}

// writeHoldRefusal answers the hold routes' refusal of code with message, carrying the row and
// the gen a restore must carry when conflict names them. It is the routes' one refusal writer
// (one that carries a row goes through writeHoldConflict first): their bodies name the code in
// error, as the operator's client reads it, rather than the broker's {code, error}.
func writeHoldRefusal(w http.ResponseWriter, status int, code, message string, conflict *natshold.Conflict) {
	body := holdRefusal{Error: code, Message: message}
	if conflict != nil {
		if conflict.Current != nil {
			current := holdRowAnswer(*conflict.Current)
			body.Current = &current
		}
		if conflict.RestoreGen != 0 {
			gen := int64(conflict.RestoreGen)
			body.RestoreGen = &gen
		}
		if code == "hold_mismatch" {
			body.HoldID = conflict.HoldID
		}
	}
	writeJSON(w, status, body)
}

// writeHoldConflict is writeHoldRefusal for a refusal that carries the row a write read
// (conflict.Current). That row goes through this broker's floor of the row first (acceptRow), as
// GET's does, so no route hands out a row the floor reads as a replay or a fork: the answer is then
// 409 below_floor with no row, which the operator's client reads as unknown.
func (s *server) writeHoldConflict(w http.ResponseWriter, status int, code, message string, conflict *natshold.Conflict) {
	if conflict != nil && conflict.Current != nil {
		if err := s.acceptRow(*conflict.Current); err != nil {
			writeHoldRefusal(w, http.StatusConflict, "below_floor", fmt.Sprintf("refused %s against a hold row that reads as unknown: %v: an older row put back, or another row at the gen this broker served or wrote", code, err), nil)
			return
		}
	}
	writeHoldRefusal(w, status, code, message, conflict)
}

// readHoldBody decodes r's body into v as readJSON does, at most maxJSONBodyBytes (413
// too_large), no field v does not declare and nothing after the value (400 bad_request),
// answering in the hold routes' refusal, whose message names the field and the kind of failure
// (holdBodyFault), never a value the body holds.
func readHoldBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(v)
	if err == nil && decoder.More() {
		err = errors.New("body must contain one JSON value")
	}
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooLarge):
		writeHoldRefusal(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("request body exceeds %d bytes", maxJSONBodyBytes), nil)
	default:
		writeHoldRefusal(w, http.StatusBadRequest, "bad_request", "body must be one valid JSON object: "+holdBodyFault(err), nil)
	}
	return false
}

// holdBodyFault is err, a JSON decoder's, as the kind of failure alone: a syntax error at its
// byte, a field whose value does not decode to its Go type, an unknown field, a body that ends
// early or holds more than one value. json's own message holds a number's digits when it is out of
// range and a character of the input at a syntax error, and a hold body holds a record's value, so
// the message is never json's.
func holdBodyFault(err error) string {
	var syntax *json.SyntaxError
	var typed *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("a JSON syntax error at byte %d", syntax.Offset)
	case errors.As(err, &typed) && typed.Field != "":
		return fmt.Sprintf("the field %q does not decode to Go %s", typed.Field, typed.Type)
	case errors.As(err, &typed):
		return "the body is not a JSON object"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "the body ends before its JSON value does"
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return "it holds a field the route does not take"
	}
	return "the body holds more than one JSON value, or does not decode"
}

// holdSignedAt reads a body's signed_at: RFC 3339 at a whole microsecond, the precision a
// timestamptz stores, since a row stored at another time than the one signed never verifies.
func holdSignedAt(text string) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || !at.Equal(at.Truncate(time.Microsecond)) {
		return time.Time{}, false
	}
	return at.UTC(), true
}

// readNATSHold answers the hold row, 404 no_row while there is none, and 409 below_floor for a
// verified row this broker's floor of the row (holdRowFloor) reads as a replay or a fork, which the
// operator's client reads as unknown. A row that does not verify is answered as it is, and the
// client refuses it by its signature. A read that meets the floor is made once more first, since a
// move that committed while it read moves the floor past what it read.
func (s *server) readNATSHold(w http.ResponseWriter, r *http.Request, _ string) {
	for attempt := 1; ; attempt++ {
		row, found, err := s.holdStore().Row(r.Context())
		switch {
		case err != nil:
			writeUnavailable(w, "DATABASE_UNAVAILABLE", "read the hold row", err)
			return
		case !found:
			writeHoldRefusal(w, http.StatusNotFound, "no_row", "there is no hold row; the operator restores it through POST /v1/nats-hold/restore", nil)
			return
		}
		err = s.acceptRow(row)
		if err == nil {
			writeJSON(w, http.StatusOK, holdRowAnswer(row))
			return
		}
		if attempt == 2 {
			writeHoldRefusal(w, http.StatusConflict, "below_floor", fmt.Sprintf("the hold row at gen %d reads as unknown: %v: an older row put back, or another row at the gen this broker served or wrote", row.Gen, err), nil)
			return
		}
	}
}

// holdRowLines are row's signed lines (signedrecord.HoldRow).
func holdRowLines(row natshold.Row) []string {
	return signedrecord.HoldRow(row.Gen, row.Phase, row.HoldID, row.SignedAt)
}

// acceptRow puts row, read from the table, through this broker's floor of the row when a key of
// BROKER_NATS_HOLD_SIGNING_KEYS signed it, and answers the floor's ErrReplay, ErrFork or
// ErrBackdated; a row that does not verify moves nothing and is answered nil, for its reader to
// refuse.
func (s *server) acceptRow(row natshold.Row) error {
	if signedrecord.Verify(s.deps.NATSHold.Keys, row.Sig, holdRowLines(row)) != nil {
		return nil
	}
	return s.holdRowFloor.Accept(row.Gen, row.SignedAt, holdRowLines(row))
}

// errWrittenUnverified is a write whose row, as the store answered it (its RETURNING row), does
// not verify: a database trigger, say, changed it between the write and the answer.
var errWrittenUnverified = errors.New("the row the store answered for the write does not verify")

// acceptWritten puts row, the row a move or restore wrote as the store answered it, through this
// broker's floor of the row once it verifies, and answers errWrittenUnverified otherwise, which
// moves nothing: an unverified answer, a gen a trigger raised among them, would wedge the floor
// above every genuine row until the process starts again.
func (s *server) acceptWritten(row natshold.Row) error {
	if err := signedrecord.Verify(s.deps.NATSHold.Keys, row.Sig, holdRowLines(row)); err != nil {
		return fmt.Errorf("%w: gen %d: %w", errWrittenUnverified, row.Gen, err)
	}
	return s.holdRowFloor.Accept(row.Gen, row.SignedAt, holdRowLines(row))
}

// holdMoveBody is PUT /v1/nats-hold's body.
type holdMoveBody struct {
	// The gen the row holds, which the move stores one above.
	ExpectedGen int64 `json:"expected_gen"`
	// The phase to move to: released, prepare or held.
	Phase string `json:"phase"`
	// The hold's id: a new one to prepare, the row's own to hold or release.
	HoldID string `json:"hold_id"`
	// Why the operator moves the row.
	Reason string `json:"reason"`
	// When the operator signed the move, RFC 3339 at a whole microsecond.
	SignedAt string `json:"signed_at"`
	// The operator's signature over the row's lines at expected_gen + 1 (signedrecord.HoldRow).
	Sig string `json:"sig"`
}

// errRowSkewed is a row a move or restore would write whose signed_at the broker refuses: more than
// signedrecord.SignedAtSkew from the broker's own clock, or more than that before the latest row
// this broker's floor of the row accepted. It is answered 409 clock_skew, naming both times, and
// nothing is stored: a row signed by a clock far fast, once stored, would raise the floor past every
// genuine move until each replica restarts, and one signed far behind would be stored and then read
// as unknown.
var errRowSkewed = errors.New("the row's signed_at is outside the clock-skew allowance")

// rowSignedAtAdmitted judges the signed_at of a row a move or restore would write at gen, before
// the write: against the broker's own clock, and against this broker's floor of the row
// (signedrecord.Floor.Admits, which moves nothing). It answers errRowSkewed, naming both times.
func (s *server) rowSignedAtAdmitted(gen uint64, signedAt time.Time) error {
	if err := signedrecord.WithinSkewOf(signedAt, time.Now()); err != nil {
		return fmt.Errorf("%w: %w", errRowSkewed, err)
	}
	if err := s.holdRowFloor.Admits(gen, signedAt); err != nil {
		return fmt.Errorf("%w: %w", errRowSkewed, err)
	}
	return nil
}

// moveNATSHold moves the hold row, once its signature verifies over the gen it will store, and
// only from a row this broker's floor of the row (holdRowFloor) accepts, checked on the row the
// move's own transaction holds: a move from an older row put back (a replay) is 409 gen_mismatch
// with no row, and from another row at the gen the floor took (a fork) 409 below_floor with no row,
// which the operator's client reads as unknown. A move whose gen the log already holds, from a row
// put back below the log on a replica whose floor never passed it, is 409 below_floor with no row.
// The new row's signed_at is judged in the same transaction, before the write (rowSignedAtAdmitted):
// one outside the allowance is 409 clock_skew, and nothing is stored.
func (s *server) moveNATSHold(w http.ResponseWriter, r *http.Request, operator string) {
	var body holdMoveBody
	if !readHoldBody(w, r, &body) {
		return
	}
	signedAt, ok := holdSignedAt(body.SignedAt)
	if body.ExpectedGen < 0 || !ok {
		writeHoldRefusal(w, http.StatusBadRequest, "bad_request", "expected_gen is a gen, at least 0, and signed_at an RFC 3339 time at a whole microsecond", nil)
		return
	}
	gen := uint64(body.ExpectedGen) + 1
	if err := signedrecord.Verify(s.deps.NATSHold.Keys, body.Sig, signedrecord.HoldRow(gen, body.Phase, body.HoldID, signedAt)); err != nil {
		writeHoldRefusal(w, http.StatusBadRequest, "bad_signature", "no key of BROKER_NATS_HOLD_SIGNING_KEYS signed the row at the gen it would store", nil)
		return
	}
	accept := func(current natshold.Row) error {
		if err := s.acceptRow(current); err != nil {
			return err
		}
		return s.rowSignedAtAdmitted(gen, signedAt)
	}
	row, err := s.holdStore().Move(r.Context(), uint64(body.ExpectedGen), natshold.Row{
		Phase: body.Phase, HoldID: body.HoldID, Reason: body.Reason, UpdatedBy: operator, SignedAt: signedAt, Sig: body.Sig,
	}, accept)
	var conflict *natshold.Conflict
	switch {
	case err == nil:
	case errors.Is(err, errRowSkewed):
		writeHoldRefusal(w, http.StatusConflict, "clock_skew", "the move stores nothing: "+err.Error(), nil)
		return
	case errors.Is(err, signedrecord.ErrReplay):
		writeHoldRefusal(w, http.StatusConflict, "gen_mismatch", fmt.Sprintf("the hold row the move starts from reads as unknown: %v: an older row put back", err), nil)
		return
	case errors.Is(err, signedrecord.ErrFork), errors.Is(err, signedrecord.ErrBackdated):
		writeHoldRefusal(w, http.StatusConflict, "below_floor", fmt.Sprintf("the hold row the move starts from reads as unknown: %v: another row at the gen this broker served, read or wrote, or a row of an earlier history", err), nil)
		return
	case !errors.As(err, &conflict):
		writeUnavailable(w, "DATABASE_UNAVAILABLE", "move the hold row", err)
		return
	case errors.Is(err, natshold.ErrGenLogged):
		writeHoldRefusal(w, http.StatusConflict, "below_floor", err.Error(), nil)
		return
	case errors.Is(err, natshold.ErrNoRow):
		s.writeHoldConflict(w, http.StatusNotFound, "no_row", err.Error(), conflict)
		return
	case errors.Is(err, natshold.ErrGenMismatch):
		s.writeHoldConflict(w, http.StatusConflict, "gen_mismatch", err.Error(), conflict)
		return
	case errors.Is(err, natshold.ErrHoldMismatch):
		s.writeHoldConflict(w, http.StatusConflict, "hold_mismatch", err.Error(), conflict)
		return
	case errors.Is(err, natshold.ErrBadTransition):
		s.writeHoldConflict(w, http.StatusBadRequest, "bad_transition", err.Error(), conflict)
		return
	default:
		writeInternal(w, "move the hold row", err)
		return
	}
	// The stored row is above the row the move started from, which the floor accepted, so it raises
	// the floor once it verifies as the store answered it.
	if err := s.acceptWritten(row); err != nil {
		writeHoldRefusal(w, http.StatusConflict, "below_floor", "the move's row reads as unknown: "+err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, holdRowAnswer(row))
}

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
	admit := func(latest *natshold.LogEntry, journals []natshold.Record, next natshold.Row) error {
		if err := s.rowSignedAtAdmitted(next.Gen, next.SignedAt); err != nil {
			return err
		}
		return s.restoreAdmitted(latest, journals, next)
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
// no client, only with that entry's hold id (409 hold_mismatch otherwise, no hold id included): a
// released row under another id leaves the prepared hold's cohort and journal where no command
// reaches them, since Admit and the cohort delete need its id on the row and it cannot be prepared
// again; or a held one whose hold's journal, the done record, verifies and shows the hold ended (a
// done hold release or hold abort), as when the row was lost between that done entry and the row's
// move; after any other held entry it would free every client mid-hold. Whatever the log shows, a
// released row is refused while any hold's journal that verifies shows hold set done and no end
// (heldByRecord): that hold is held by its own record, and a log a database writer emptied or cut
// back, on a replica whose floor started again, would otherwise free every client mid-hold. A
// released row naming a hold whose done record does not verify is refused too, as unknown (409
// below_floor). Held follows only a prepare or held entry of its own hold id, so a hold the log
// shows ended, by a release or by abort from prepare, which leaves no journal entry, is not held
// again.
func (s *server) restoreAdmitted(latest *natshold.LogEntry, journals []natshold.Record, next natshold.Row) error {
	refuse := func(why string) error {
		return &natshold.Conflict{Err: fmt.Errorf("%w: %s", natshold.ErrBadTransition, why)}
	}
	if next.Phase == string(natsgrant.OperatorPhaseReleased) {
		if err := s.heldByRecord(latest, journals, next.HoldID); err != nil {
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

// heldByRecord refuses a released restore while a hold's own record holds it: any journal among
// journals that verifies and shows hold set done and no end (ErrBadTransition, naming the hold),
// and, for named, the hold the restore names, a done record that does not verify (unknown, 409
// below_floor). A hold whose row the log's latest entry shows released already (a release or abort
// cut between its row move and its done entry) is not held by its record: its row was released,
// and released is restored for it.
func (s *server) heldByRecord(latest *natshold.LogEntry, journals []natshold.Record, named string) error {
	released := func(holdID string) bool {
		return latest != nil && latest.Phase == string(natsgrant.OperatorPhaseReleased) && latest.HoldID == holdID
	}
	for i := range journals {
		holdID := journals[i].HoldID
		if released(holdID) {
			continue
		}
		d, ok := s.verifiedJournal(holdID, &journals[i])
		switch {
		case !ok && holdID == named:
			return fmt.Errorf("%w: hold %s's done record does not verify", errHoldLogUnknown, holdID)
		case !ok:
			continue
		}
		if _, setDone := d.Latest(holdrecord.CommandSet, holdrecord.StateDone); setDone && !journalEnded(d) {
			return &natshold.Conflict{Err: fmt.Errorf("%w: hold %s's journal shows hold set done and no end, so a released row restored now would free every client mid-hold; restore the row held with its hold id %s", natshold.ErrBadTransition, holdID, holdID)}
		}
	}
	return nil
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

// holdRecordBody is PUT /v1/nats-hold/records' body.
type holdRecordBody struct {
	// The hold the record is of, which must be the hold row's.
	HoldID string `json:"hold_id"`
	// cohort, convert, done or selection.
	Kind string `json:"kind"`
	// The record's key within its hold and kind.
	Key string `json:"key"`
	// The record's value.
	Value json.RawMessage `json:"value"`
	// The gen the record holds, which the write stores one above; 0 inserts a record that is not
	// there.
	ExpectedGen int64 `json:"expected_gen"`
	// The operator's signature over the record's lines at expected_gen + 1 (signedrecord.Record).
	Sig string `json:"sig"`
}

// holdRecord is a record as PUT /v1/nats-hold/records answers the one it stored.
type holdRecord struct {
	// The hold the record is of.
	HoldID string `json:"hold_id"`
	// cohort, convert, done or selection.
	Kind string `json:"kind"`
	// The record's key within its hold and kind.
	Key string `json:"key"`
	// The record's value, as Postgres holds it: its meaning is the value written, its keys
	// reordered and its white space dropped, which its signature's lines hash past.
	Value json.RawMessage `json:"value"`
	// The record's gen.
	Gen int64 `json:"gen"`
	// When the broker stored the record, RFC 3339 in UTC.
	WrittenAt string `json:"written_at"`
	// The operator's signature over the record's lines (signedrecord.Record).
	Sig string `json:"sig"`
}

// putNATSHoldRecord writes one record of the hold row's hold, once its signature verifies over
// the gen it will store. A record the routes deleted stays deleted while its signed delete stands
// and verifies: putting its key again is 409 gen_mismatch, on every replica.
func (s *server) putNATSHoldRecord(w http.ResponseWriter, r *http.Request, _ string) {
	var body holdRecordBody
	if !readHoldBody(w, r, &body) {
		return
	}
	if !natshold.RecordKind(body.Kind) || body.Key == "" || len(body.Value) == 0 || body.ExpectedGen < 0 {
		writeHoldRefusal(w, http.StatusBadRequest, "bad_request", "a record names its hold, its kind (cohort, convert, done or selection), its key and its value, at an expected_gen of at least 0", nil)
		return
	}
	gen := uint64(body.ExpectedGen) + 1
	if err := signedrecord.Verify(s.deps.NATSHold.Keys, body.Sig, signedrecord.Record(body.HoldID, body.Kind, body.Key, gen, body.Value)); err != nil {
		writeHoldRefusal(w, http.StatusBadRequest, "bad_signature", "no key of BROKER_NATS_HOLD_SIGNING_KEYS signed the record at the gen it would store", nil)
		return
	}
	floorKey := recordFloorKey{holdID: body.HoldID, kind: body.Kind, key: body.Key}
	if floor, above := s.holdFloors.above(floorKey, gen); !above {
		writeHoldRefusal(w, http.StatusConflict, "gen_mismatch", fmt.Sprintf("this broker has written or served the record at gen %d, so a write storing gen %d is a replay", floor, gen), nil)
		return
	}
	deletedByRoutes := func(d natshold.Deletion) bool {
		return signedrecord.Verify(s.deps.NATSHold.Keys, d.Sig, signedrecord.RecordDelete(body.HoldID, body.Kind, d.Key, d.Gen)) == nil
	}
	stored, err := s.holdStore().PutRecord(r.Context(), natshold.Record{
		HoldID: body.HoldID, Kind: body.Kind, Key: body.Key, Value: body.Value, Sig: body.Sig,
	}, uint64(body.ExpectedGen), deletedByRoutes)
	var conflict *natshold.Conflict
	switch {
	case err == nil:
	case !errors.As(err, &conflict):
		writeUnavailable(w, "DATABASE_UNAVAILABLE", "write the hold record", err)
		return
	case errors.Is(err, natshold.ErrHoldMismatch):
		s.writeHoldConflict(w, http.StatusConflict, "hold_mismatch", err.Error(), conflict)
		return
	case errors.Is(err, natshold.ErrGenMismatch):
		s.writeHoldConflict(w, http.StatusConflict, "gen_mismatch", err.Error(), conflict)
		return
	default:
		writeInternal(w, "write the hold record", err)
		return
	}
	// The record as the store answered it raises its floor only once it verifies: a trigger that
	// changed it between the write and the answer would otherwise wedge the floor.
	if err := signedrecord.Verify(s.deps.NATSHold.Keys, stored.Sig, signedrecord.Record(stored.HoldID, stored.Kind, stored.Key, stored.Gen, stored.Value)); err != nil {
		writeHoldRefusal(w, http.StatusConflict, "below_floor", fmt.Sprintf("the record the store answered for the write does not verify: gen %d: %v", stored.Gen, err), nil)
		return
	}
	s.holdFloors.raise(floorKey, stored.Gen)
	writeJSON(w, http.StatusOK, holdRecord{
		HoldID: stored.HoldID, Kind: stored.Kind, Key: stored.Key, Value: stored.Value, Gen: int64(stored.Gen),
		WrittenAt: stored.WrittenAt.UTC().Format(time.RFC3339Nano), Sig: stored.Sig,
	})
}

// holdRecordEntry is one record GET /v1/nats-hold/records lists, of the hold and kind it named.
type holdRecordEntry struct {
	// The record's key within its hold and kind.
	Key string `json:"key"`
	// The record's value, as Postgres holds it.
	Value json.RawMessage `json:"value"`
	// The record's gen.
	Gen int64 `json:"gen"`
	// When the broker stored the record, RFC 3339 in UTC.
	WrittenAt string `json:"written_at"`
	// The operator's signature over the record's lines (signedrecord.Record).
	Sig string `json:"sig"`
}

// holdRecords is GET /v1/nats-hold/records' answer.
type holdRecords struct {
	// Every record of the hold and kind the query named, by key; empty when there is none.
	Records []holdRecordEntry `json:"records"`
}

// readNATSHoldRecords lists one hold's records of one kind, none below its floor and none missing
// that its floor has seen (recordFloors): either is 409 below_floor, which the operator's client
// reads as unknown. A read that meets the floor is made once more first, since a write that
// committed while it read moves the floor past what it read; an older record put back, or a record
// deleted outside the routes, meets it again. Only a record whose signature verifies raises its
// floor; one that does not is answered as it is, and the client refuses it by its signature. The
// deletes the routes kept are read in the same snapshot as the records, and only one whose
// signature verifies is the routes' delete.
func (s *server) readNATSHoldRecords(w http.ResponseWriter, r *http.Request, _ string) {
	holdID, kind := r.URL.Query().Get("hold_id"), r.URL.Query().Get("kind")
	if holdID == "" || !natshold.RecordKind(kind) {
		writeHoldRefusal(w, http.StatusBadRequest, "bad_request", "the query names hold_id and a kind: cohort, convert, done or selection", nil)
		return
	}
	verified := func(rec natshold.Record) bool {
		return signedrecord.Verify(s.deps.NATSHold.Keys, rec.Sig, signedrecord.Record(holdID, kind, rec.Key, rec.Gen, rec.Value)) == nil
	}
	deletedByRoutes := func(d natshold.Deletion) bool {
		return signedrecord.Verify(s.deps.NATSHold.Keys, d.Sig, signedrecord.RecordDelete(holdID, kind, d.Key, d.Gen)) == nil
	}
	var records []natshold.Record
	for attempt := 1; ; attempt++ {
		var deletions []natshold.Deletion
		var err error
		records, deletions, err = s.holdStore().Records(r.Context(), holdID, kind)
		if err != nil {
			writeUnavailable(w, "DATABASE_UNAVAILABLE", "read the hold records", err)
			return
		}
		refusal := s.holdFloors.serve(holdID, kind, records, deletions, verified, deletedByRoutes)
		if refusal == "" {
			break
		}
		if attempt == 2 {
			writeHoldRefusal(w, http.StatusConflict, "below_floor", refusal, nil)
			return
		}
	}
	answer := holdRecords{Records: []holdRecordEntry{}}
	for _, rec := range records {
		answer.Records = append(answer.Records, holdRecordEntry{
			Key: rec.Key, Value: rec.Value, Gen: int64(rec.Gen), WrittenAt: rec.WrittenAt.UTC().Format(time.RFC3339Nano), Sig: rec.Sig,
		})
	}
	writeJSON(w, http.StatusOK, answer)
}

// recordFloors is a broker process's floor of each hold record: the highest gen of it the routes
// have written, deleted or served verified, and whether the routes deleted it. A database writer
// other than the operator cannot write a record that verifies, but can put back an older one the
// operator signed, which does, or delete one: the floor answers either as unknown (GET's 409
// below_floor) and refuses a write that would store a gen at or below it again (409
// gen_mismatch), so the operator's commands never act on a journal a command has moved past. A
// record the floor has seen is a tombstone once it is absent, since the absence is a database
// writer's, unless the routes deleted it. The DELETE route keeps the operator's signed delete (the
// cohort's, signed over the gen it deleted) in the delete's own statement, in
// nats_hold_record_deletes, and every replica reads it beside the records: on each, a record whose
// delete verifies at or above its floor is absent as it should be, and reads as unknown if it
// comes back. A delete that does not verify, or one signed over a gen below the floor, deletes
// nothing the floor has seen. Only a record whose signature verifies raises its floor, so a
// writer's unsigned record at a high gen moves nothing.
//
// The residuals, which the hold's design accepts:
//
//   - Each broker replica keeps its own floor, for as long as its process lives. A broker-database
//     writer can put back an older journal on a replica whose floor is below the put-back gen, at
//     any time, not only after a restart; production runs two replicas. A floor both replicas
//     share, kept outside the database (one Secret Manager secret the broker alone writes), is the
//     fix if that acceptance changes. A replica that starts again, or one that has not yet written
//     or served a record, has no floor for it at all. The auth service's floor of the hold row
//     (natscallout's holdView) is per replica too, but every connection decision reads the row, so
//     both replicas track its newest gen.
//   - After a legitimate restore of the broker's database to an earlier point, every broker
//     answers each record it has seen at a later gen 409 below_floor until it restarts.
//   - A kind with several keys put back as an older set that lacks a key is caught only by the
//     tombstone of the missing key; the keys put back at gens the floor has not passed read as
//     genuine.
//   - Cutover rows carry no gen of their own, so the routes keep no floor for them: an older signed
//     cutover row put back reads as genuine, on any replica.
//   - A token taken at CONNECT can read every hold route. The operator's platform token is the
//     NATS CONNECT password, and the routes take it as their bearer for its life, at most an hour:
//     every write needs the operator's signature as well, but no read does, so whoever holds the
//     token reads the hold row, the journal, the cohort and the cutover rows.
//
// Safe for concurrent use.
type recordFloors struct {
	mu   sync.Mutex
	gens map[recordFloorKey]recordFloor
}

// recordFloorKey names one hold record.
type recordFloorKey struct{ holdID, kind, key string }

// recordFloor is one record's floor: its gen, and whether the routes deleted the record at it.
type recordFloor struct {
	gen     uint64
	deleted bool
}

func newRecordFloors() *recordFloors { return &recordFloors{gens: map[recordFloorKey]recordFloor{}} }

// above answers the record's floor (0 for none), and whether gen, the gen a write would store, is
// above it.
func (f *recordFloors) above(k recordFloorKey, gen uint64) (uint64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gens[k].gen, gen > f.gens[k].gen
}

// raise moves the record's floor up to gen, a gen the routes stored, with the record present.
func (f *recordFloors) raise(k recordFloorKey, gen uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gens[k] = recordFloor{gen: max(f.gens[k].gen, gen)}
}

// deleted moves the record's floor up to gen, the gen the routes deleted it at, and marks it
// deleted, so its absence is no tombstone and its return is a put-back.
func (f *recordFloors) deleted(k recordFloorKey, gen uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gens[k] = recordFloor{gen: max(f.gens[k].gen, gen), deleted: true}
}

// gone reports whether the floor has seen the record k present and the routes did not delete it,
// so that its absence now is a database writer's, not a record that never was.
func (f *recordFloors) gone(k recordFloorKey) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	floor, seen := f.gens[k]
	return seen && !floor.deleted
}

// serve checks the records one read of holdID's kind answered against their floors, and answers
// why the read reads as unknown, or "" and raises the floor of every record verified accepts to
// its gen. A delete among deletions that deletedByRoutes verifies, at or above its record's floor,
// first marks that floor deleted at its gen, as the replica that served the delete marked its own.
// A read reads as unknown when a record is below its floor, when a record the routes deleted is
// back, and when a record the floor has seen, and the routes did not delete, is absent.
func (f *recordFloors) serve(holdID, kind string, records []natshold.Record, deletions []natshold.Deletion,
	verified func(natshold.Record) bool, deletedByRoutes func(natshold.Deletion) bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range deletions {
		k := recordFloorKey{holdID, kind, d.Key}
		if floor := f.gens[k]; d.Gen >= floor.gen && deletedByRoutes(d) {
			f.gens[k] = recordFloor{gen: d.Gen, deleted: true}
		}
	}
	present := map[string]bool{}
	for _, rec := range records {
		present[rec.Key] = true
		floor, seen := f.gens[recordFloorKey{holdID, kind, rec.Key}]
		switch {
		case seen && floor.deleted:
			return fmt.Sprintf("the %s record %s of hold %s, which the routes deleted at gen %d, is back at gen %d: a deleted record put back, which reads as unknown",
				kind, rec.Key, holdID, floor.gen, rec.Gen)
		case seen && rec.Gen < floor.gen:
			return fmt.Sprintf("the %s record %s of hold %s is at gen %d, below gen %d, which this broker has written or served: an older record put back, which reads as unknown",
				kind, rec.Key, holdID, rec.Gen, floor.gen)
		}
	}
	for k, floor := range f.gens {
		if k.holdID == holdID && k.kind == kind && !floor.deleted && !present[k.key] {
			return fmt.Sprintf("the %s record %s of hold %s, which this broker has written or served at gen %d, is absent: a record deleted outside the routes, which reads as unknown",
				kind, k.key, holdID, floor.gen)
		}
	}
	for _, rec := range records {
		if verified(rec) {
			f.gens[recordFloorKey{holdID, kind, rec.Key}] = recordFloor{gen: rec.Gen}
		}
	}
	return ""
}

// errJournalGone is a cohort delete on a released row whose hold has no done record, where this
// broker's floor of that record saw it: a journal a database writer deleted, not one never written.
var errJournalGone = errors.New("the hold's done record is gone, which this broker wrote or served")

// holdJournalKey is the done record's one key (natshold's journalKey, internal/holdrecord's
// journal).
const holdJournalKey = "hold"

// deleteNATSHoldCohort deletes the cohort record the query names (?hold_id=, ?kind=cohort, ?key=,
// ?gen=, ?sig=), for hold abort from prepare, and for hold abort on a row lost in prepare and
// restored released: once the operator's signature over the record's delete at that gen verifies
// (signedrecord.RecordDelete), only while the row is prepare with that hold id, or released with
// it while the table admits hold abort on the released row (holdrecord.Request.RowReleased): the
// hold's done record verifies and shows prepare, or the hold has no done record, which the table
// reads as the zero Done, and this broker's floor of that record never saw it (409 below_floor
// otherwise: a journal a writer deleted); and only the record at that gen, so neither the
// operator's token alone nor an old signature deletes a record.
func (s *server) deleteNATSHoldCohort(w http.ResponseWriter, r *http.Request, _ string) {
	query := r.URL.Query()
	holdID, kind, key := query.Get("hold_id"), query.Get("kind"), query.Get("key")
	gen, err := strconv.ParseUint(query.Get("gen"), 10, 63)
	if holdID == "" || kind != natshold.KindCohort || key == "" || err != nil || gen == 0 {
		writeHoldRefusal(w, http.StatusBadRequest, "bad_request", "the query names hold_id, kind=cohort, the one kind of record a delete takes, its key, the gen it deletes (at least 1) and the sig over them", nil)
		return
	}
	if err := signedrecord.Verify(s.deps.NATSHold.Keys, query.Get("sig"), signedrecord.RecordDelete(holdID, kind, key, gen)); err != nil {
		writeHoldRefusal(w, http.StatusBadRequest, "bad_signature", "no key of BROKER_NATS_HOLD_SIGNING_KEYS signed the record's delete at the gen it names", nil)
		return
	}
	released := func(journal *natshold.Record) error {
		var d holdrecord.Done // no done record: the zero Done, a hold with no journal
		if journal == nil && s.holdFloors.gone(recordFloorKey{holdID: holdID, kind: natshold.KindDone, key: holdJournalKey}) {
			return fmt.Errorf("%w: hold %s", errJournalGone, holdID)
		}
		if journal != nil {
			var ok bool
			if d, ok = s.verifiedJournal(holdID, journal); !ok {
				return &natshold.Conflict{Err: fmt.Errorf("%w: the row is released, and hold %s's done record does not verify", natshold.ErrHoldMismatch, holdID), HoldID: holdID}
			}
		}
		if err := holdrecord.Decide(d, holdrecord.Request{Command: holdrecord.CommandAbort, RowReleased: true}); err != nil {
			return &natshold.Conflict{Err: fmt.Errorf("%w: the row is released, and hold %s's journal does not show prepare", natshold.ErrHoldMismatch, holdID), HoldID: holdID}
		}
		return nil
	}
	err = s.holdStore().DeleteCohort(r.Context(), holdID, key, gen, query.Get("sig"), released)
	var conflict *natshold.Conflict
	switch {
	case err == nil:
	case errors.Is(err, errJournalGone):
		writeHoldRefusal(w, http.StatusConflict, "below_floor", "the cohort is not deleted: "+err.Error(), nil)
		return
	case !errors.As(err, &conflict):
		writeUnavailable(w, "DATABASE_UNAVAILABLE", "delete the cohort", err)
		return
	case errors.Is(err, natshold.ErrHoldMismatch):
		s.writeHoldConflict(w, http.StatusConflict, "hold_mismatch", err.Error()+": the cohort is deleted only while the row is prepare with its hold id, or released with it while that hold's journal shows prepare", conflict)
		return
	case errors.Is(err, natshold.ErrGenMismatch):
		s.writeHoldConflict(w, http.StatusConflict, "gen_mismatch", err.Error(), conflict)
		return
	default:
		writeInternal(w, "delete the cohort", err)
		return
	}
	// The floor stays at the deleted gen, marked deleted: its absence is the route's, and a write
	// or a database writer putting an older signed cohort back is refused. Every other replica
	// marks its own once it reads the delete the store kept.
	s.holdFloors.deleted(recordFloorKey{holdID: holdID, kind: kind, key: key}, gen)
	w.WriteHeader(http.StatusNoContent)
}

// holdCutoverBody is PUT /v1/nats-hold/cutover's body.
type holdCutoverBody struct {
	// The family: session, dispatch, or daemon/<project key>.
	Family string `json:"family"`
	// done or rolled_back.
	State string `json:"state"`
	// The hold the row is written under, which must be the held row's.
	HoldID string `json:"hold_id"`
	// The operator's signature over the row's lines with hold_gen the hold row's current gen
	// (signedrecord.Cutover).
	Sig string `json:"sig"`
}

// holdCutover is one family's cutover row, as PUT /v1/nats-hold/cutover answers the one it
// stored and GET lists each.
type holdCutover struct {
	// The family: session, dispatch, or daemon/<project key>.
	Family string `json:"family"`
	// done or rolled_back.
	State string `json:"state"`
	// The hold the row was written under.
	HoldID string `json:"hold_id"`
	// The hold row's gen when the row was written.
	HoldGen int64 `json:"hold_gen"`
	// When the broker stored the row, RFC 3339 in UTC.
	WrittenAt string `json:"written_at"`
	// The account whose token wrote the row: the operator entry's.
	WrittenBy string `json:"written_by"`
	// The operator's signature over the row's lines (signedrecord.Cutover).
	Sig string `json:"sig"`
}

// holdCutoverAnswer is c as the routes answer it.
func holdCutoverAnswer(c natshold.Cutover) holdCutover {
	return holdCutover{
		Family: c.Family, State: c.State, HoldID: c.HoldID, HoldGen: int64(c.HoldGen),
		WrittenAt: c.WrittenAt.UTC().Format(time.RFC3339Nano), WrittenBy: c.WrittenBy, Sig: c.Sig,
	}
}

// holdCutoverFamily reports whether family names a family a hold converts.
func holdCutoverFamily(family string) bool {
	switch family {
	case "session", "dispatch":
		return true
	}
	key, daemon := strings.CutPrefix(family, "daemon/")
	return daemon && key != ""
}

// putNATSHoldCutover writes one family's cutover row while the hold row is held with the body's
// hold id, once its signature verifies over that row's current gen.
func (s *server) putNATSHoldCutover(w http.ResponseWriter, r *http.Request, operator string) {
	var body holdCutoverBody
	if !readHoldBody(w, r, &body) {
		return
	}
	if !holdCutoverFamily(body.Family) || (body.State != natshold.CutoverDone && body.State != natshold.CutoverRolledBack) {
		writeHoldRefusal(w, http.StatusBadRequest, "bad_request", "a cutover row names its family (session, dispatch or daemon/<project key>) and its state (done or rolled_back)", nil)
		return
	}
	stored, err := s.holdStore().PutCutover(r.Context(), natshold.Cutover{
		Family: body.Family, State: body.State, HoldID: body.HoldID, WrittenBy: operator, Sig: body.Sig,
	}, func(holdGen uint64) error {
		return signedrecord.Verify(s.deps.NATSHold.Keys, body.Sig, signedrecord.Cutover(body.Family, body.State, body.HoldID, holdGen))
	})
	var conflict *natshold.Conflict
	switch {
	case err == nil:
	case isSignatureRefusal(err):
		writeHoldRefusal(w, http.StatusBadRequest, "bad_signature", "no key of BROKER_NATS_HOLD_SIGNING_KEYS signed the row at the hold row's current gen", nil)
		return
	case !errors.As(err, &conflict):
		writeUnavailable(w, "DATABASE_UNAVAILABLE", "write the cutover row", err)
		return
	case errors.Is(err, natshold.ErrHoldMismatch):
		s.writeHoldConflict(w, http.StatusConflict, "hold_mismatch", err.Error(), conflict)
		return
	default:
		writeInternal(w, "write the cutover row", err)
		return
	}
	writeJSON(w, http.StatusOK, holdCutoverAnswer(stored))
}

// isSignatureRefusal reports whether err is signedrecord's refusal of a signature or of the
// fields it covers.
func isSignatureRefusal(err error) bool {
	return errors.Is(err, signedrecord.ErrNoSignature) || errors.Is(err, signedrecord.ErrBadSignature) || errors.Is(err, signedrecord.ErrBadField)
}

// holdCutovers is GET /v1/nats-hold/cutover's answer.
type holdCutovers struct {
	// Every family's cutover row, by family; empty when there is none.
	Families []holdCutover `json:"families"`
}

// readNATSHoldCutovers lists every family's cutover row.
func (s *server) readNATSHoldCutovers(w http.ResponseWriter, r *http.Request, _ string) {
	cutovers, err := s.holdStore().Cutovers(r.Context())
	if err != nil {
		writeUnavailable(w, "DATABASE_UNAVAILABLE", "read the cutover rows", err)
		return
	}
	answer := holdCutovers{Families: []holdCutover{}}
	for _, c := range cutovers {
		answer.Families = append(answer.Families, holdCutoverAnswer(c))
	}
	writeJSON(w, http.StatusOK, answer)
}
