// Package pgsetting is how a program reads a Postgres connection string a person configured (a
// DATABASE_URL, the broker's BROKER_DATABASE_URL, the daemon's DSN, a tool's -pg) before it opens
// the database the string names. Such a string may carry a user and a password, and pgx prints
// parts of it: its parse error masks the password and quotes the rest, the user among it, and its
// connect error names the user and the database in front of what failed. And a user part written
// with a raw @ or / is read by pgx in part as the host or the database. So each opener reads its
// string here: one whose user part pgx reads apart is refused (Refuse), one pgx cannot parse is
// refused too, each named by its length and the cause the check found (redact.Refusal), and a
// failed connect names no user and no database (ConnectFailure). A reader that also parses the
// string with net/url refuses what net/url reads apart first (URLParserReadsApart).
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
	// AtInDatabaseName: an @ before the first / and another in the database name, after the first
	// / and before any ?. A password that holds a raw @ and then a raw / (u:p@S/x@host/db) gives
	// pgx the host S, a piece of the password, and an honest host with an @ in its database name
	// (u:p@host/dis@patch) reads the same: no rule of the string tells the two apart, and pgx names
	// the host it read in every error of a connect, a later query's included, so no line could be
	// kept from printing the piece. The honest form can always be written with that @ as %40.
	AtInDatabaseName redact.Misreading = "has an @ before its first / and another in its database name, so the host pgx reads may be a piece of a password that holds a raw @ and /; write the @ in its password, or in its database name, as %40 (and a / in its password as %2F)"
	// EndBeforeAt: a ?, # or \ before the @ that ends pgx's user part. pgx reads such a value
	// right, so the openers admit it (misread finds nothing in it), but net/url ends the authority
	// at that character and reads a piece of the user part as the host, or fails with an error that
	// quotes the value whole. A reader that parses the value with net/url refuses it with these
	// words first (URLParserReadsApart).
	EndBeforeAt redact.Misreading = "has a ?, # or \\ in its user part, where Go's URL parser ends its host; write each of them in the user part percent-encoded (? as %3F, # as %23, \\ as %5C)"
	// NotParsed: pgx cannot parse the value. pgx's parse error masks the password and quotes the
	// rest, the user among it, which can be a token written where the user stands.
	NotParsed redact.Misreading = "is not a Postgres connection string pgx can parse; check its port and the name and value of each of its options (sslmode=require, say)"
)

// Refuse is the refusal of raw, the value of the setting named, whose user part pgx reads apart
// (misread), named by its length and what the check found, or nil.
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

// misread is what makes raw a Postgres URL whose user part pgx itself reads apart, so that pgx
// takes a piece of the user part for a host or a database name, which its connect error and a
// refusal naming its host would print; the empty Misreading when nothing does. pgx reads the user
// part as libpq does, up to the FIRST @ before the first / (pgconn's parseURLSettings), and keeps a
// ?, # or \ before that @ in it, where net/url would end the authority (URLParserReadsApart). An @
// after the first / and before any ? is refused either way: with no @ before the / it may end a
// user part that held a / (AtAfterPath), and with one it may follow a piece of a password that held
// an @ and a / (AtInDatabaseName). An @ in the query, after a ?, is the query's, and is admitted
// when an @ comes before the / (a password would need a raw @, / and ? to put one there). The
// key=value form has no such part.
func misread(raw string) redact.Misreading {
	authority, afterPath, ok := splitURL(raw)
	if !ok {
		return ""
	}
	database, _, _ := strings.Cut(afterPath, "?")
	at := strings.Index(authority, "@")
	switch {
	case at < 0 && strings.Contains(afterPath, "@"):
		return AtAfterPath
	case at < 0:
		return ""
	case strings.Count(authority, "@") > 1:
		return AtsBeforePath
	case strings.Contains(database, "@"):
		return AtInDatabaseName
	}
	return ""
}

// URLParserReadsApart reports whether net/url reads raw's user part apart from where pgx reads it:
// a ?, # or \ before the @ that ends pgx's user part, where net/url ends the host (EndBeforeAt).
// pgx reads such a value right, so the openers admit it; a reader that parses the value with
// net/url first refuses it, since net/url would name a piece of the user part as the host or quote
// the value whole in its error.
func URLParserReadsApart(raw string) bool {
	authority, _, ok := splitURL(raw)
	if !ok {
		return false
	}
	at := strings.Index(authority, "@")
	return at >= 0 && strings.ContainsAny(authority[:at], "?#\\")
}

// splitURL is raw's authority, up to its first / after the scheme, and what follows that /, when
// raw is a Postgres URL; ok is false for the key=value form.
func splitURL(raw string) (authority, afterPath string, ok bool) {
	rest, ok := strings.CutPrefix(raw, "postgres://")
	if !ok {
		if rest, ok = strings.CutPrefix(raw, "postgresql://"); !ok {
			return "", "", false
		}
	}
	authority, afterPath, _ = strings.Cut(rest, "/")
	return authority, afterPath, true
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
