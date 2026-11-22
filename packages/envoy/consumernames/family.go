package consumernames

import (
	"strconv"
	"strings"
)

// Naming is how a family names the consumers of a logical name: Prefix(logical) begins each of its
// fresh names (Prefix(logical)~<nuid>), and Fixed(logical) is the name the family gave logical
// before this package, which the cutover's convert replaces. Every Naming the table gives sets
// both. Each
// creator builds its Config's Prefix and Fixed from its family's Naming (FamilyNaming), and
// CheckFamily judges a record by the same, so the creators and an operator's reader, which may not
// import the creators' packages, hold one table of the families' names.
type Naming struct {
	Prefix func(logical string) string
	Fixed  func(logical string) string
}

const (
	// SessionPrefix begins every fresh session consumer's name: session~<nuid>, for every session.
	SessionPrefix = "session"
	// SessionFixedPrefix begins every fixed session consumer's name: s-<session id>.
	SessionFixedPrefix = "s-"
)

// SessionNaming is the session family's naming, for a session's key (its id): its fresh names are
// session~<nuid> (SessionPrefix), and its fixed name s-<session id> (SessionFixedPrefix).
func SessionNaming() Naming {
	return Naming{
		Prefix: func(string) string { return SessionPrefix },
		Fixed:  func(sessionID string) string { return SessionFixedPrefix + sessionID },
	}
}

// DurableNaming is the naming of a durable kept in the session store under DurableKey(durable),
// the listener's own: its fresh names are <durable>~<nuid>, and its fixed name the durable's name.
// A key DurableKey did not make has no durable (CheckDurableKey refuses it first).
func DurableNaming() Naming {
	durable := func(key string) string {
		durable, _ := durableOfKey(key)
		return durable
	}
	return Naming{Prefix: durable, Fixed: durable}
}

// LogicalNaming is the naming of a durable whose logical name is its own, Dispatch's and the
// daemons': its fresh names are <logical>~<nuid>, and its fixed name the logical name.
func LogicalNaming() Naming {
	logical := func(logical string) string { return logical }
	return Naming{Prefix: logical, Fixed: logical}
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

// CheckFamily is Names.Check of logical's record in the store of family, by the family's own
// naming (FamilyNaming), for a reader with no creator's Config: an operator's status, census or
// cleanup. It answers what the family's creator answers of the record, and its nil says no more
// than Check's: the record must come from a store read or DecodeKVValue, and a name it gives must
// pass Owns before anything acts on it.
func CheckFamily(family, logical string, record Record) error {
	naming, err := FamilyNaming(family, logical)
	if err != nil {
		return err
	}
	return (&Names{cfg: Config{Prefix: naming.Prefix, Fixed: naming.Fixed}}).Check(logical, record)
}

// Naming is n's own naming (Config.Prefix and Config.Fixed), which a test of a family's creator
// holds equal to the family's (FamilyNaming).
func (n *Names) Naming() Naming {
	return Naming{Prefix: n.cfg.Prefix, Fixed: n.cfg.Fixed}
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
