// Package pgsetting is how a program reads a Postgres connection string a person configured (a
// DATABASE_URL, the broker's BROKER_DATABASE_URL, the daemon's DSN, a tool's -pg) before it opens
// the database the string names. Such a string may carry a user and a password, and pgx prints
// parts of it: its parse error masks the password and quotes the rest, the user among it, and its
// connect error names the user and the database in front of what failed. And a user part written
// with a raw @, /, ?, # or \ is read in part as the host or the database. So each opener reads its
// string here: one whose user part pgx and net/url read apart is refused (Refuse), one pgx cannot
// parse is refused too, each named by its length and the cause the check found (redact.Refusal),
// and a failed connect names no user and no database (ConnectFailure).
package pgsetting

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/redact"
)

// The causes a refusal of a Postgres connection string names, in redact.Refusal's words.
const (
	// AtAfterPath: an @ after the first / and none before it. pgx and net/url both end the
	// authority at that /, so a user part that holds a / is read as a host, a port and a database
	// name, which pgx's connect error prints.
	AtAfterPath redact.Misreading = "has an @ after its first / and none before it, where a user part that holds a / is read as its host and database; write each / in its user part as %2F, and an @ in its database name or query as %40"
	// AtsBeforePath: more than one @ before the first /. pgx ends the user part at the first,
	// net/url at the last, so one of them reads a piece of the password as the host.
	AtsBeforePath redact.Misreading = "has more than one @ before its first /, and pgx ends its user part at the first while Go's URL parser ends it at the last; write each @ in its user part as %40, and one in its query as %40 or after a / before the query"
	// EndBeforeAt: a ?, # or \ before the @ that ends pgx's user part, where net/url ends the
	// authority, so net/url reads a piece of the user part as the host.
	EndBeforeAt redact.Misreading = "has a ?, # or \\ in its user part, where Go's URL parser ends its host; write each of them in the user part percent-encoded (? as %3F, # as %23, \\ as %5C)"
	// NotParsed: pgx cannot parse the value. pgx's parse error masks the password and quotes the
	// rest, the user among it, which can be a token written where the user stands.
	NotParsed redact.Misreading = "is not a Postgres connection string pgx can parse; check its port and the name and value of each of its options (sslmode=require, say)"
)

// Refuse is the refusal of raw, the value of the setting named, whose user part pgx may read apart
// from where net/url reads it (misread), named by its length and what the check found, or nil.
func Refuse(setting, raw string) error {
	if why := misread(raw); why != "" {
		return redact.Refusal(setting, raw, why)
	}
	return nil
}

// ParseConfig is raw, the value of the setting named, as pgx reads it: refused first when Refuse
// refuses it, and named by its length when pgx cannot parse it.
func ParseConfig(setting, raw string) (*pgx.ConnConfig, error) {
	if err := Refuse(setting, raw); err != nil {
		return nil, err
	}
	config, err := pgx.ParseConfig(raw)
	if err != nil {
		return nil, redact.Refusal(setting, raw, NotParsed)
	}
	return config, nil
}

// ParsePoolConfig is ParseConfig for a pool: raw as pgxpool reads it, refused the same way.
func ParsePoolConfig(setting, raw string) (*pgxpool.Config, error) {
	if err := Refuse(setting, raw); err != nil {
		return nil, err
	}
	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return nil, redact.Refusal(setting, raw, NotParsed)
	}
	return config, nil
}

// misread is what makes raw a Postgres URL whose user part pgx may read apart from where net/url
// reads it, so that pgx takes a piece of the user part for a host or a database name, which its
// connect error and a refusal naming its host would print; the empty Misreading when nothing does.
// pgx reads the user part as libpq does, up to the FIRST @ before the first / (pgconn's
// parseURLSettings), and net/url up to the LAST @ before the first /, ? or #. An @ after the first
// / belongs to the path or the query for both, unless no @ comes before it: then it may end a user
// part that held a /. The key=value form has no such part.
func misread(raw string) redact.Misreading {
	rest, ok := strings.CutPrefix(raw, "postgres://")
	if !ok {
		if rest, ok = strings.CutPrefix(raw, "postgresql://"); !ok {
			return ""
		}
	}
	authority, afterPath, _ := strings.Cut(rest, "/")
	at := strings.Index(authority, "@")
	switch {
	case at < 0 && strings.Contains(afterPath, "@"):
		return AtAfterPath
	case at < 0:
		return ""
	case strings.Count(authority, "@") > 1:
		return AtsBeforePath
	case strings.ContainsAny(authority[:at], "?#\\"):
		return EndBeforeAt
	}
	return ""
}

// connectFailure is a pgconn.ConnectError as a message may print it: what failed at each host the
// connection tried, without the user and the database pgx names in front of it, since a URL's user
// part can be a token written where the user stands. It unwraps to the ConnectError, so errors.As
// still finds it.
type connectFailure struct{ connect *pgconn.ConnectError }

func (c connectFailure) Error() string { return "failed to connect: " + c.connect.Unwrap().Error() }
func (c connectFailure) Unwrap() error { return c.connect }

// ConnectFailure is err with a pgconn.ConnectError in it written without the user and the database
// pgx names in front of it, and err itself otherwise.
func ConnectFailure(err error) error {
	var connect *pgconn.ConnectError
	if errors.As(err, &connect) {
		return connectFailure{connect}
	}
	return err
}
