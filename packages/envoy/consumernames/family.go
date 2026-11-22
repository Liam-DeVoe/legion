package consumernames

import (
	"strconv"
	"strings"

	"github.com/nats-io/nats.go/jetstream"
)

// Naming is how a family names the consumers of a logical name: Prefix(logical) begins each of its
// fresh names (Prefix(logical)~<nuid>), Fixed(logical) is the name the family gave logical before
// this package, which the cutover's convert replaces, and Metadata(logical) is the metadata every
// consumer of logical carries, which its create writes and the owner check requires. Every Naming
// the table gives sets all three. Each creator builds its Config's Prefix, Fixed and Metadata from
// its family's Naming (FamilyNaming), and CheckFamily and OwnsFamily judge a record and a consumer
// by the same, so the creators and an operator's reader, which may not import the creators'
// packages, hold one table of the families' names.
type Naming struct {
	Prefix   func(logical string) string
	Fixed    func(logical string) string
	Metadata func(logical string) map[string]string
}

const (
	// SessionPrefix begins every fresh session consumer's name: session~<nuid>, for every session.
	SessionPrefix = "session"
	// SessionFixedPrefix begins every fixed session consumer's name: s-<session id>.
	SessionFixedPrefix = "s-"
	// SessionIDMetadataKey is the metadata every session consumer of the fresh mode carries, naming
	// its session, beside LogicalMetadataKey.
	SessionIDMetadataKey = "envoy.session_id"
)

// SessionNaming is the session family's naming, for a session's key (its id): its fresh names are
// session~<nuid> (SessionPrefix), its fixed name s-<session id> (SessionFixedPrefix), and each of
// its consumers carries the session id under LogicalMetadataKey and SessionIDMetadataKey.
func SessionNaming() Naming {
	return Naming{
		Prefix: func(string) string { return SessionPrefix },
		Fixed:  func(sessionID string) string { return SessionFixedPrefix + sessionID },
		Metadata: func(sessionID string) map[string]string {
			return map[string]string{LogicalMetadataKey: sessionID, SessionIDMetadataKey: sessionID}
		},
	}
}

// DurableNaming is the naming of a durable kept in the session store under DurableKey(durable),
// the listener's own: its fresh names are <durable>~<nuid>, its fixed name the durable's name, and
// each of its consumers carries the durable's name under LogicalMetadataKey. A key DurableKey did
// not make has no durable (CheckDurableKey refuses it first).
func DurableNaming() Naming {
	durable := func(key string) string {
		durable, _ := durableOfKey(key)
		return durable
	}
	return Naming{Prefix: durable, Fixed: durable, Metadata: func(key string) map[string]string {
		return map[string]string{LogicalMetadataKey: durable(key)}
	}}
}

// LogicalNaming is the naming of a durable whose logical name is its own, Dispatch's and the
// daemons': its fresh names are <logical>~<nuid>, its fixed name the logical name, and each of its
// consumers carries the logical name under LogicalMetadataKey.
func LogicalNaming() Naming {
	logical := func(logical string) string { return logical }
	return Naming{Prefix: logical, Fixed: logical, Metadata: func(logical string) map[string]string {
		return map[string]string{LogicalMetadataKey: logical}
	}}
}

// FamilyNaming is the naming of logical's consumers in the store of family: in the session family's
// store, DurableNaming for a key DurableKey made and SessionNaming for any other; in Dispatch's and
// a daemon's (DaemonFamily), LogicalNaming. A family no store keeps is ErrInvalidFamily, and a
// durable's key DurableKey did not make ErrInvalidKey, each naming it.
func FamilyNaming(family, logical string) (Naming, error) {
	switch {
	case family == SessionFamily && strings.HasPrefix(logical, durableKeyPrefix):
		if err := CheckDurableKey(logical); err != nil {
			return Naming{}, err
		}
		return DurableNaming(), nil
	case family == SessionFamily:
		return SessionNaming(), nil
	}
	if err := checkFamily(family); err != nil {
		return Naming{}, err
	}
	return LogicalNaming(), nil
}

// CheckFamily is the family fence (Names.check, the one each creator applies to every record it
// reads) of logical's record in the store of family, by the family's own naming (FamilyNaming), for
// a reader with no creator's Config: an operator's status, census or cleanup. It answers what the
// family's creator answers of the record.
//
// Its nil says only that the record's names are of logical's family. It says nothing of the record
// itself, which must come from a store read or DecodeKVValue (its own key, well-formed fields), nor
// of the consumers: every session's prefix is "session", so a record of one session passes with
// another session's consumer as its current. A name the record gives must pass the owner check
// before anything reads, binds, updates or deletes it, as the creator does.
func CheckFamily(family, logical string, record Record) error {
	naming, err := FamilyNaming(family, logical)
	if err != nil {
		return err
	}
	return naming.names().check(logical, record)
}

// OwnsFamily is the owner check (Names.Owns, the one each creator applies before it reads, binds,
// updates, adopts or deletes a consumer a record names) of info, a consumer a record of logical
// names in the store of family, by the family's own naming and metadata (FamilyNaming), for a
// reader with no creator's Config: nil when the consumer is logical's own (its metadata holds each
// pair the family's consumers carry) or logical's fixed name, else ErrMalformedRecord naming the
// consumer and the pair it lacks. A family no store keeps is ErrInvalidFamily, and a durable's key
// DurableKey did not make ErrInvalidKey. A reader that deletes a consumer a record names asks here
// first, as the creator does.
func OwnsFamily(family, logical string, info *jetstream.ConsumerInfo) error {
	naming, err := FamilyNaming(family, logical)
	if err != nil {
		return err
	}
	return naming.names().Owns(logical, info)
}

// names is the Names a reader with no creator's Config judges a family's records and consumers by:
// the family's naming and metadata, and nothing else.
func (naming Naming) names() *Names {
	return &Names{cfg: Config{Prefix: naming.Prefix, Fixed: naming.Fixed, Metadata: naming.Metadata}}
}

// Naming is n's own naming (Config.Prefix, Config.Fixed and Config.Metadata), which a test of a
// family's creator holds equal to the family's (FamilyNaming).
func (n *Names) Naming() Naming {
	return Naming{Prefix: n.cfg.Prefix, Fixed: n.cfg.Fixed, Metadata: n.cfg.Metadata}
}

// durableOfKey is the durable whose key DurableKey made key, and false for a key it did not.
func durableOfKey(key string) (string, bool) {
	escaped, ok := strings.CutPrefix(key, durableKeyPrefix)
	if !ok || escaped == "" {
		return "", false
	}
	var durable strings.Builder
	for i := 0; i < len(escaped); i++ {
		if escaped[i] != '=' {
			durable.WriteByte(escaped[i])
			continue
		}
		if i+2 >= len(escaped) {
			return "", false
		}
		b, err := strconv.ParseUint(escaped[i+1:i+3], 16, 8)
		if err != nil {
			return "", false
		}
		durable.WriteByte(byte(b))
		i += 2
	}
	return durable.String(), true
}
