package signedrecord_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus/signedrecord"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

func sign(t *testing.T, key ed25519.PrivateKey, lines []string) string {
	t.Helper()
	sig, err := signedrecord.Sign(key, lines)
	if err != nil {
		t.Fatalf("sign %q: %v", lines, err)
	}
	return sig
}

// Each forgery a writer of the broker's tables could make is refused, naming the reason: a record
// with no signature, one signed by another key, a genuine record copied onto another key, a
// genuine older record read after a newer gen was accepted, another genuine record at the gen
// already accepted, a genuine record at a higher gen signed more than SignedAtSkew before the one
// accepted (a record of an earlier history of the tables), and a value changed after signing. A
// genuine record verifies, the record accepted reads again at its gen, and a higher gen signed
// within the allowance before it is accepted, so the refusals are the verifier's and not a reader
// that refuses everything. The fork row fails on a floor that keeps the gen alone, as it did at
// bb2b623923, and the backdated row on one that keeps no signing time, as at 4a4ebd8787.
func TestASignedRecordRefusesForgeriesAndReplays(t *testing.T) {
	operator, operatorKey := newKey(t)
	_, otherKey := newKey(t)
	keys := []ed25519.PublicKey{operator}
	value := []byte(`{"workflow":"cutover","ops":[],"complete":false}`)
	genuine := signedrecord.Record("hold-1", "done", "hold", 2, value)
	genuineSig := sign(t, operatorKey, genuine)
	if err := signedrecord.Verify(keys, genuineSig, genuine); err != nil {
		t.Fatalf("the genuine record does not verify: %v", err)
	}
	signedAt := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	older := signedrecord.Record("hold-1", "done", "hold", 1, value)
	olderSig := sign(t, operatorKey, older)
	floor := &signedrecord.Floor{}
	if err := floor.Accept(2, signedAt, genuine); err != nil {
		t.Fatalf("the floor refuses the first gen it reads: %v", err)
	}
	if err := floor.Accept(2, signedAt, genuine); err != nil {
		t.Fatalf("the floor refuses the record it accepted, read again at its gen: %v", err)
	}
	skewed := &signedrecord.Floor{}
	if err := skewed.Accept(2, signedAt, genuine); err != nil {
		t.Fatal(err)
	}
	if err := skewed.Accept(3, signedAt.Add(-signedrecord.SignedAtSkew), signedrecord.Record("hold-1", "done", "hold", 3, value)); err != nil {
		t.Fatalf("the floor refuses a higher gen signed the whole allowance before the accepted record: %v", err)
	}

	for _, forgery := range []struct {
		name string
		read func() error
		want error
	}{
		{
			name: "no signature",
			read: func() error { return signedrecord.Verify(keys, "", genuine) },
			want: signedrecord.ErrNoSignature,
		},
		{
			name: "signed by another key",
			read: func() error { return signedrecord.Verify(keys, sign(t, otherKey, genuine), genuine) },
			want: signedrecord.ErrBadSignature,
		},
		{
			name: "a genuine record copied onto another key",
			read: func() error {
				return signedrecord.Verify(keys, genuineSig, signedrecord.Record("hold-1", "convert", "set", 2, value))
			},
			want: signedrecord.ErrBadSignature,
		},
		{
			name: "a genuine older record after a newer gen was accepted",
			read: func() error {
				if err := signedrecord.Verify(keys, olderSig, older); err != nil {
					return err
				}
				return floor.Accept(1, signedAt, older)
			},
			want: signedrecord.ErrReplay,
		},
		{
			name: "another genuine record at the gen already accepted",
			read: func() error {
				other := signedrecord.Record("hold-1", "done", "hold", 2, []byte(`{"workflow":"cutover","ops":[],"complete":true}`))
				if err := signedrecord.Verify(keys, sign(t, operatorKey, other), other); err != nil {
					return err
				}
				return floor.Accept(2, signedAt, other)
			},
			want: signedrecord.ErrFork,
		},
		{
			name: "a genuine record at a higher gen signed more than the allowance before the accepted one",
			read: func() error {
				earlier := signedrecord.Record("hold-1", "done", "hold", 4, value)
				if err := signedrecord.Verify(keys, sign(t, operatorKey, earlier), earlier); err != nil {
					return err
				}
				return floor.Accept(4, signedAt.Add(-signedrecord.SignedAtSkew-time.Second), earlier)
			},
			want: signedrecord.ErrBackdated,
		},
		{
			name: "a value changed after signing",
			read: func() error {
				return signedrecord.Verify(keys, genuineSig, signedrecord.Record("hold-1", "done", "hold", 2, []byte(`{"workflow":"cutover","ops":[],"complete":true}`)))
			},
			want: signedrecord.ErrBadSignature,
		},
	} {
		err := forgery.read()
		if !errors.Is(err, forgery.want) {
			t.Errorf("%s: read as %v, want it refused with %v", forgery.name, err, forgery.want)
		}
	}
}

// Admits judges a gen and signing time as Accept would, and moves nothing: the hold's routes ask it
// inside a move's transaction, before they write the row. A higher gen signed within the allowance
// is admitted and leaves the floor where it was; one signed past it is ErrBackdated, naming both
// times. And the floor judges by the latest signing time it has accepted, not the last: after gen 3
// signed 4 minutes before gen 2, gen 4 signed 6 minutes before gen 2 is refused, which a floor that
// kept gen 3's time (2 minutes before it) would accept, as at d08fb4a866.
func TestTheFloorAdmitsWithoutMovingAndKeepsTheLatestSigningTime(t *testing.T) {
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	lines := func(gen uint64) []string { return signedrecord.Record("hold-1", "done", "hold", gen, []byte(`{}`)) }
	floor := &signedrecord.Floor{}
	if err := floor.Admits(9, at.Add(-time.Hour)); err != nil {
		t.Fatalf("the zero floor does not admit a gen: %v", err)
	}
	if err := floor.Accept(2, at, lines(2)); err != nil {
		t.Fatal(err)
	}
	if err := floor.Admits(3, at.Add(-signedrecord.SignedAtSkew)); err != nil {
		t.Errorf("Admits refuses a higher gen signed the whole allowance before the accepted record: %v", err)
	}
	err := floor.Admits(3, at.Add(-signedrecord.SignedAtSkew-time.Second))
	if !errors.Is(err, signedrecord.ErrBackdated) || !strings.Contains(err.Error(), "11:54:59") || !strings.Contains(err.Error(), "12:00:00") {
		t.Errorf("Admits of a higher gen signed past the allowance answered %v, want ErrBackdated naming both times", err)
	}
	if !floor.Reached(2) || floor.Reached(3) {
		t.Errorf("Admits moved the floor: it reaches gen 3 %v, gen 2 %v; want only gen 2", floor.Reached(3), floor.Reached(2))
	}
	if err := floor.Accept(3, at.Add(-4*time.Minute), lines(3)); err != nil {
		t.Fatal(err)
	}
	if err := floor.Accept(4, at.Add(-6*time.Minute), lines(4)); !errors.Is(err, signedrecord.ErrBackdated) {
		t.Errorf("after gen 3 signed 4 minutes before gen 2, gen 4 signed 6 minutes before gen 2 was read as %v, want ErrBackdated", err)
	}
}

// A value is signed by its meaning, not its bytes: Postgres stores a record's value as jsonb, which
// reorders keys and drops whitespace, so the value read back verifies against the signature made
// over the value written.
func TestARecordValueVerifiesAfterJSONBRewritesIt(t *testing.T) {
	operator, operatorKey := newKey(t)
	written := signedrecord.Record("h", "cohort", "cohort", 1, []byte(`{ "b": [1, 2], "a": "x<y" }`))
	read := signedrecord.Record("h", "cohort", "cohort", 1, []byte(`{"a": "x<y", "b": [1, 2]}`))
	if err := signedrecord.Verify([]ed25519.PublicKey{operator}, sign(t, operatorKey, written), read); err != nil {
		t.Fatalf("the value as jsonb gives it back does not verify: %v", err)
	}
}

// A field holding a line break is refused when signed and when verified, so no two records can
// write the same lines.
func TestAFieldHoldingALineBreakIsRefused(t *testing.T) {
	operator, operatorKey := newKey(t)
	lines := signedrecord.HoldRow(3, "held", "hold\nreleased", time.Unix(0, 0))
	if _, err := signedrecord.Sign(operatorKey, lines); !errors.Is(err, signedrecord.ErrBadField) {
		t.Fatalf("Sign of a line break = %v, want ErrBadField", err)
	}
	if err := signedrecord.Verify([]ed25519.PublicKey{operator}, "c2ln", lines); !errors.Is(err, signedrecord.ErrBadField) {
		t.Fatalf("Verify of a line break = %v, want ErrBadField", err)
	}
}

// A record's digest line must be a SHA-256 digest in lowercase hex, which Record writes for a JSON
// value: lines whose digest is anything else, built by hand, are refused by Sign and Verify
// (ErrBadField), as a value that is not JSON is. Fails if the check reads a sentinel string at the
// digest's position: a hand-built digest of "abc" signs.
func TestARecordWhoseDigestIsNoSHA256IsRefused(t *testing.T) {
	operator, operatorKey := newKey(t)
	good := signedrecord.Record("h", "done", "hold", 1, []byte(`{}`))
	sig := sign(t, operatorKey, good)
	for name, digest := range map[string]string{
		"three letters":  "abc",
		"uppercase hex":  strings.ToUpper(good[5]),
		"one digit more": good[5] + "0",
		"empty":          "",
	} {
		lines := append([]string(nil), good...)
		lines[5] = digest
		if _, err := signedrecord.Sign(operatorKey, lines); !errors.Is(err, signedrecord.ErrBadField) {
			t.Errorf("Sign of a record whose digest is %s answered %v, want ErrBadField", name, err)
		}
		if err := signedrecord.Verify([]ed25519.PublicKey{operator}, sig, lines); !errors.Is(err, signedrecord.ErrBadField) {
			t.Errorf("Verify of a record whose digest is %s answered %v, want ErrBadField", name, err)
		}
	}
	if _, err := signedrecord.Sign(operatorKey, signedrecord.Record("h", "done", "hold", 1, []byte(`not json`))); !errors.Is(err, signedrecord.ErrBadField) {
		t.Errorf("Sign of a record whose value is not JSON answered %v, want ErrBadField", err)
	}
}

// Each kind's lines open with its own tag, so a signature over one kind's lines never verifies as
// another kind's.
func TestEachKindOpensWithItsTag(t *testing.T) {
	for want, lines := range map[string][]string{
		"nats-hold-v1":                 signedrecord.HoldRow(1, "released", "h", time.Unix(0, 0)),
		"nats-hold-record-v1":          signedrecord.Record("h", "done", "hold", 1, []byte(`{}`)),
		"nats-hold-record-delete-v1":   signedrecord.RecordDelete("h", "cohort", "cohort", 1),
		"nats-cutover-v1":              signedrecord.Cutover("session", "done", "h", 1),
		"envoy-kv-records-manifest-v1": signedrecord.RecordsManifest([]byte("{}\n")),
	} {
		if lines[0] != want {
			t.Errorf("lines open with %q, want %q", lines[0], want)
		}
	}
}

// A record's delete is signed over the record's hold id, kind, key and the gen it deletes, under
// its own tag: a delete signed over one gen does not verify over another, so an old signature
// cannot delete a newer record, and a record write's signature does not verify as a delete.
func TestARecordDeleteIsSignedOverTheGenItDeletes(t *testing.T) {
	operator, operatorKey := newKey(t)
	keys := []ed25519.PublicKey{operator}
	sig := sign(t, operatorKey, signedrecord.RecordDelete("h", "cohort", "cohort", 2))
	if err := signedrecord.Verify(keys, sig, signedrecord.RecordDelete("h", "cohort", "cohort", 2)); err != nil {
		t.Fatalf("the delete's own lines: %v", err)
	}
	for name, lines := range map[string][]string{
		"another gen":      signedrecord.RecordDelete("h", "cohort", "cohort", 3),
		"another key":      signedrecord.RecordDelete("h", "cohort", "other", 2),
		"another hold id":  signedrecord.RecordDelete("h2", "cohort", "cohort", 2),
		"a record's write": signedrecord.Record("h", "cohort", "cohort", 2, []byte(`{}`)),
	} {
		if err := signedrecord.Verify(keys, sig, lines); !errors.Is(err, signedrecord.ErrBadSignature) {
			t.Errorf("the delete's signature over %s answered %v, want ErrBadSignature", name, err)
		}
	}
	write := sign(t, operatorKey, signedrecord.Record("h", "cohort", "cohort", 2, []byte(`{}`)))
	if err := signedrecord.Verify(keys, write, signedrecord.RecordDelete("h", "cohort", "cohort", 2)); !errors.Is(err, signedrecord.ErrBadSignature) {
		t.Errorf("a record write's signature verified as a delete: %v", err)
	}
}

// A key list is read from its setting's text: entries parted by whitespace or commas, each the
// unpadded base64url of a 32-byte Ed25519 public key. An entry that is not is refused naming the
// setting and its place in the list and never its text, since text pasted there in error may be a
// private half.
func TestParsePublicKeysReadsTheSettingsList(t *testing.T) {
	first, _ := newKey(t)
	second, _ := newKey(t)
	encode := func(key ed25519.PublicKey) string { return base64.RawURLEncoding.EncodeToString(key) }
	keys, err := signedrecord.ParsePublicKeys("EXAMPLE_KEYS", " "+encode(first)+",\n"+encode(second)+" ")
	if err != nil || len(keys) != 2 || !keys[0].Equal(first) || !keys[1].Equal(second) {
		t.Fatalf("ParsePublicKeys of two keys = %v, %v", keys, err)
	}
	if keys, err := signedrecord.ParsePublicKeys("EXAMPLE_KEYS", "  "); err != nil || len(keys) != 0 {
		t.Fatalf("ParsePublicKeys of nothing = %v, %v; want no key and no error", keys, err)
	}
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.PrivateKeySize))
	for raw, place := range map[string]string{
		encode(first) + " " + secret:                  "entry 2",
		base64.StdEncoding.EncodeToString(first):      "entry 1",
		encode(first) + "," + encode(second) + ",!!!": "entry 3",
	} {
		_, err := signedrecord.ParsePublicKeys("EXAMPLE_KEYS", raw)
		if err == nil || !strings.Contains(err.Error(), "EXAMPLE_KEYS "+place) || strings.Contains(err.Error(), secret) {
			t.Errorf("ParsePublicKeys(%q) = %v, want a refusal naming EXAMPLE_KEYS %s and not the entry", raw, err, place)
		}
	}
}

// A record value that is not JSON is refused ErrBadField, naming the byte where it stops being
// JSON and no character of it: the value is a record's, and json's message for a syntax error
// quotes the character it stopped at. Run by hand, the control is Canonical at bb2b623923, whose
// error ends `invalid character 'Q' looking for beginning of value`.
func TestAValueThatIsNotJSONIsRefusedNamingNoPartOfIt(t *testing.T) {
	_, err := signedrecord.Canonical([]byte(`{"a":Q7canary}`))
	if !errors.Is(err, signedrecord.ErrBadField) {
		t.Fatalf("Canonical of a value that is not JSON = %v, want ErrBadField", err)
	}
	if text := err.Error(); strings.Contains(text, "Q") || strings.Contains(text, "canary") || !strings.Contains(text, "byte 6") {
		t.Errorf("Canonical's error is %q: it holds part of the value, or names no byte", text)
	}
}
