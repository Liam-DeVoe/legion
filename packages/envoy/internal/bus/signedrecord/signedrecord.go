// Package signedrecord signs and verifies the records the NATS writer hold trusts: the hold row,
// each hold record and each family's cutover row, which live in the secrets broker's Postgres and
// which only the operator's Ed25519 key writes. A writer of those tables other than the operator
// (a database admin, or the other service sharing the instance) can write a row, but not one that
// verifies, so every reader reads such a row as unknown, never as released.
//
// A record is signed as lines: a tag naming its kind and version, then each field in a fixed
// order, joined by line feeds. The tag keeps a signature over one kind from verifying as another,
// and a field holding a line feed is refused, so no two records write the same lines. A record's
// JSON value enters its lines as the hex SHA-256 of its canonical form (Canonical), because
// Postgres stores it as jsonb and gives it back with its keys reordered and its whitespace dropped.
package signedrecord

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The refusals, each wrapped with what it names.
var (
	// ErrNoSignature is a record read or written with no signature.
	ErrNoSignature = errors.New("signedrecord: no signature")
	// ErrBadSignature is a signature that no key of the list verifies over the record's lines: a
	// record signed by another key, copied onto another record's key, or changed after signing.
	ErrBadSignature = errors.New("signedrecord: the signature does not verify")
	// ErrReplay is a verified record whose gen is below the highest one already accepted (Floor).
	ErrReplay = errors.New("signedrecord: a gen below the one already accepted")
	// ErrFork is a verified record at the gen already accepted (Floor) whose lines differ from the
	// record accepted there: two records the operator signed at one gen, one of them put in the
	// other's place.
	ErrFork = errors.New("signedrecord: a record at the gen already accepted that is not the one accepted")
	// ErrBackdated is a verified record at a gen above the one accepted (Floor) that the operator
	// signed more than SignedAtSkew before the latest record accepted: a record of an earlier
	// history of the tables, put back after a reset, or one signed by a clock far behind.
	ErrBackdated = errors.New("signedrecord: a gen above the one accepted, signed before it by more than the clock-skew allowance")
	// ErrSkewed is a record signed more than SignedAtSkew before or after the reader's own clock
	// (WithinSkewOf).
	ErrSkewed = errors.New("signedrecord: signed further from the reader's clock than the clock-skew allowance")
	// ErrBadField is a field holding a line feed, or a value that is not JSON.
	ErrBadField = errors.New("signedrecord: a field that cannot be signed")
)

// The tags each kind's lines open with.
const (
	holdRowTag         = "nats-hold-v1"
	recordTag          = "nats-hold-record-v1"
	recordDeleteTag    = "nats-hold-record-delete-v1"
	cutoverTag         = "nats-cutover-v1"
	recordsManifestTag = "envoy-kv-records-manifest-v1"
)

// HoldRow is the hold row's lines: its gen, phase, hold id and the time it was signed, which the
// row stores beside the signature.
func HoldRow(gen uint64, phase, holdID string, signedAt time.Time) []string {
	return []string{holdRowTag, strconv.FormatUint(gen, 10), phase, holdID, signedAt.UTC().Format(time.RFC3339Nano)}
}

// Record is one hold record's lines: its hold id, kind, key, gen and the hex SHA-256 of its value's
// canonical form. A value that is not JSON has no canonical form, so its digest line is empty, and
// Sign and Verify refuse it (ErrBadField), as they refuse any record whose digest line is not a
// SHA-256 digest in hex.
func Record(holdID, kind, key string, gen uint64, value []byte) []string {
	digest := ""
	if canonical, err := Canonical(value); err == nil {
		sum := sha256.Sum256(canonical)
		digest = hex.EncodeToString(sum[:])
	}
	return []string{recordTag, holdID, kind, key, strconv.FormatUint(gen, 10), digest}
}

// RecordDelete is the lines of one hold record's delete: its hold id, kind, key and the gen the
// delete removes, the record's current one. Signed over that gen, an old delete's signature cannot
// delete the record once a newer write has moved it.
func RecordDelete(holdID, kind, key string, gen uint64) []string {
	return []string{recordDeleteTag, holdID, kind, key, strconv.FormatUint(gen, 10)}
}

// Cutover is one family's cutover row's lines: the family, its state, the hold id it was written
// under and the hold row's gen at that write.
func Cutover(family, state, holdID string, holdGen uint64) []string {
	return []string{cutoverTag, family, state, holdID, strconv.FormatUint(holdGen, 10)}
}

// RecordsManifest is the lines of a records-only copy's manifest (internal/bus/kvrecords): the hex
// SHA-256 of the manifest file's bytes as written. The manifest names the records file's own
// SHA-256, so one signature covers the whole copy, and a copy rewritten so that its three files
// agree with each other still does not verify.
func RecordsManifest(manifest []byte) []string {
	sum := sha256.Sum256(manifest)
	return []string{recordsManifestTag, hex.EncodeToString(sum[:])}
}

// Canonical is value's canonical JSON: decoded, then encoded with its object keys sorted, no
// whitespace, no HTML escaping, and each number as the text it was written in. jsonb keeps the
// meaning of a value and its number text for every integer, so a value written and the value
// Postgres gives back have one canonical form. Its error says where the value stops being JSON,
// never what it holds: json's message for a syntax error holds a character of the input, and the
// value is a record's.
func Canonical(value []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return nil, fmt.Errorf("%w: the value is not JSON: a syntax error at byte %d", ErrBadField, syntax.Offset)
		}
		return nil, fmt.Errorf("%w: the value is not JSON", ErrBadField)
	}
	if decoder.More() {
		return nil, fmt.Errorf("%w: the value holds more than one JSON value", ErrBadField)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(decoded); err != nil {
		return nil, fmt.Errorf("%w: the value does not encode again", ErrBadField)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// message is the bytes a record's lines are signed as, or ErrBadField for a field holding a line
// feed, or a record whose digest line is not a SHA-256 digest in lowercase hex: a value that is not
// JSON, which Record gives an empty digest, or lines built by hand.
func message(lines []string) ([]byte, error) {
	for index, line := range lines {
		if strings.ContainsAny(line, "\n\r") {
			return nil, fmt.Errorf("%w: field %d holds a line break", ErrBadField, index)
		}
	}
	if len(lines) > 0 && lines[0] == recordTag && (len(lines) != 6 || !sha256Hex(lines[5])) {
		return nil, fmt.Errorf("%w: the record's value is not JSON, or its digest is no SHA-256 digest in hex", ErrBadField)
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// sha256Hex reports whether s is a SHA-256 digest as Record writes it: 64 lowercase hex digits.
func sha256Hex(s string) bool {
	if len(s) != hex.EncodedLen(sha256.Size) {
		return false
	}
	for _, r := range s {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f') {
			return false
		}
	}
	return true
}

// Sign signs lines with key and returns the signature, base64url without padding.
func Sign(key ed25519.PrivateKey, lines []string) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", errors.New("signedrecord: no signing key")
	}
	msg, err := message(lines)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, msg)), nil
}

// Verify reports whether some key of keys signed lines with sig: nil when one did, ErrNoSignature
// for an empty sig, ErrBadField for lines Sign would refuse, and ErrBadSignature otherwise, for a
// sig that does not decode, an empty key list included.
func Verify(keys []ed25519.PublicKey, sig string, lines []string) error {
	if sig == "" {
		return ErrNoSignature
	}
	msg, err := message(lines)
	if err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("%w: the signature does not decode", ErrBadSignature)
	}
	for _, key := range keys {
		if len(key) == ed25519.PublicKeySize && ed25519.Verify(key, msg, raw) {
			return nil
		}
	}
	return ErrBadSignature
}

// ParsePublicKeys reads a list of the operator's public keys from the text of setting: entries
// parted by whitespace or commas, each the unpadded base64url of a 32-byte Ed25519 public key, as
// BROKER_NATS_HOLD_SIGNING_KEYS and every command that verifies the hold's rows write them. An entry
// that is not one is refused naming setting and the entry's place in the list, never its text,
// since text pasted there in error may be a private half. Blank text is no key and no error.
func ParsePublicKeys(setting, raw string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	entries := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	for index, entry := range entries {
		key, err := base64.RawURLEncoding.Strict().DecodeString(entry)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s entry %d is not the unpadded base64url of a %d-byte Ed25519 public key", setting, index+1, ed25519.PublicKeySize)
		}
		keys = append(keys, ed25519.PublicKey(key))
	}
	return keys, nil
}

// Floor is the highest gen a reader has accepted of one record, with the digest of the record it
// accepted there and the latest time the operator signed a record it accepted, so an older signed
// copy that a database writer puts back reads as a replay, a different signed record at the gen
// already accepted reads as a fork, and a higher gen signed well before the latest accepted record
// reads as a record from an earlier history of the tables: the operator can be led to sign a second
// record at a gen it signed before (a writer who puts an older row back has the operator's next
// move signed at the gen the older row names), and either one verifies; and after a reset of the
// tables (a database restore, a log emptied or cut back, and a restart) the new history's gens start
// again below the old history's, so an old row at a higher gen verifies too, but was signed before
// it. After a point-in-time restore the old history's later rows were signed after the restored
// row, so the check catches them only once the floor accepts a new move signed more than
// SignedAtSkew after the old history's latest row. It lives as long as its reader's process: a
// reader that starts again starts with no floor, which is the residual the hold's design states, and
// is also how a floor that took a wrong time (a clock far fast) is cleared. The zero Floor has
// accepted nothing, so it accepts any gen; it is safe for concurrent use.
type Floor struct {
	mu       sync.Mutex
	gen      uint64
	digest   [sha256.Size]byte
	signedAt time.Time
	accepted bool
}

// SignedAtSkew is the clock-skew allowance the hold grants: Floor.Accept and Floor.Admits refuse a
// higher gen signed more than this before the latest accepted record (ErrBackdated), and the hold's
// routes refuse a row signed more than this before or after the broker's own clock
// (WithinSkewOf). Every move of the hold row is signed after the move it follows was read, so in
// one history a later gen is never signed earlier than this, as long as the clocks of the operator
// processes that sign agree within it. Clocks that are each within this of true time can be twice
// this apart, so the operator's machines must agree with each other, and with the broker's, within
// it, not merely each with true time.
const SignedAtSkew = 5 * time.Minute

// WithinSkewOf answers ErrSkewed, naming both times, when signedAt is more than SignedAtSkew
// before or after now, the reader's own clock, and nil otherwise. The hold's routes ask it of
// every row they are asked to write, so a row signed by a clock far fast never reaches a floor.
func WithinSkewOf(signedAt, now time.Time) error {
	if signedAt.Before(now.Add(-SignedAtSkew)) || signedAt.After(now.Add(SignedAtSkew)) {
		return fmt.Errorf("%w: signed at %s, and the reader's clock reads %s", ErrSkewed,
			signedAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return nil
}

// Admits answers ErrBackdated, naming the gens and both times, when gen is above the floor and
// signedAt is more than SignedAtSkew before the latest accepted record's time, and nil otherwise,
// as Accept judges a gen's signing time. It moves nothing: the hold's routes ask it of the row a
// move or restore would write, inside that write's transaction and before it, so a row the floor
// would refuse is never stored.
func (f *Floor) Admits(gen uint64, signedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.backdated(gen, signedAt)
}

// backdated is Admits' judgement, with f.mu held.
func (f *Floor) backdated(gen uint64, signedAt time.Time) error {
	if f.accepted && gen > f.gen && signedAt.Before(f.signedAt.Add(-SignedAtSkew)) {
		return fmt.Errorf("%w: gen %d signed at %s, after gen %d signed at %s", ErrBackdated, gen,
			signedAt.UTC().Format(time.RFC3339), f.gen, f.signedAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// Accept records gen, the digest of lines, the record's signed lines, and signedAt, the time the
// operator signed it (keeping the latest signing time it has accepted), when gen is above the floor
// and signedAt is no more than SignedAtSkew before that latest time, or gen is at the floor with the
// same lines, and returns nil. It returns ErrReplay for a gen below the floor, ErrFork for other
// lines at the floor's gen, and ErrBackdated for a higher gen signed more than SignedAtSkew before
// the latest accepted record, naming the gens, and leaves the floor where it was.
func (f *Floor) Accept(gen uint64, signedAt time.Time, lines []string) error {
	digest := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.backdated(gen, signedAt); err != nil {
		return err
	}
	switch {
	case !f.accepted || gen > f.gen:
		if !f.accepted || signedAt.After(f.signedAt) {
			f.signedAt = signedAt
		}
		f.gen, f.digest, f.accepted = gen, digest, true
		return nil
	case gen < f.gen:
		return fmt.Errorf("%w: gen %d, after gen %d", ErrReplay, gen, f.gen)
	case digest != f.digest:
		return fmt.Errorf("%w: gen %d", ErrFork, gen)
	}
	return nil
}

// Reached reports whether the floor has accepted gen or a gen above it, so a record written at gen
// now would be at or below the floor. It moves nothing: the hold's restore route asks it before it
// writes a row at a gen of its own choosing.
func (f *Floor) Reached(gen uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accepted && gen <= f.gen
}
