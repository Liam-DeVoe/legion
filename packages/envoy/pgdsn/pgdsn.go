// Package pgdsn is a Postgres DSN as a refusal or an error prints it: the setting that holds it,
// the address it points at and the driver's detail, never its credential. The Legion daemon (its
// record store and Oh My Pi's session database), the secrets broker and Dispatch each read their
// DSN through it: Refusal at boot, before any connection, and ConnectError for every failure to
// parse, open or reach the database.
package pgdsn

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"
)

// Refusal is why dsn, the value of the setting named setting, is refused before Postgres is asked,
// or nil. It refuses three shapes, each of which Postgres or pgx would print with part of its
// credential, and names setting alone, never the value, and says what to change:
//   - a postgres:// or postgresql:// URL holding an unencoded "@" after pgx's user part
//     (atPastUserinfo). Such an "@" is where a password holding an "@" or a "/" leaves part of
//     itself, read as the host, the port, the database or a query, which every connect error
//     prints;
//   - a value that names the scheme but is no such URL (schemeMistyped): pgx reads only a
//     lower-case postgres:// or postgresql:// at its first byte as a URL, so it reads
//     `POSTGRES://u:<pw>@h/x?sslmode=require`, ` postgres://…` and `postgres:u:<pw>@h` as one
//     keyword setting up to the "=", which it sends the server as a parameter's name, and a server
//     that admits the client prints that name whole;
//   - a keyword DSN with a postgres:// URL in a value (urlInKeywordValue): pgx expands no URL in
//     `dbname=`, and its connect error names the database, URL and password with it.
//
// A keyword DSN with no such URL (`host=… password=…`) is never refused: its password is a value
// of its own.
func Refusal(setting, dsn string) error {
	switch refusalOf(dsn) {
	case atPast:
		return fmt.Errorf("%s cannot be read: it holds an \"@\" after its user name and password, where Postgres reads"+
			" part of its user name, password or database name as its host, port, database or query and prints it;"+
			" write each \"@\" in a password, user name, database name or query value as %%40, and a \"/\" in a password as %%2F", setting)
	case schemeMistyped:
		return fmt.Errorf("%s cannot be read: it names the postgres scheme but does not begin with postgres:// or postgresql://"+
			" in lower case, with nothing before it, so Postgres would read it as one keyword setting and can print it whole;"+
			" write the scheme as postgres:// or postgresql://", setting)
	case urlInKeywordValue:
		return fmt.Errorf("%s cannot be read: it is a keyword DSN, and a value of it holds a postgres:// URL, which Postgres"+
			" does not read as a URL and prints whole, its password included; write the DSN as that URL, or name the database by its name", setting)
	}
	return nil
}

// refusal is the shape Refusal refuses a DSN for, or kept.
type refusal int

const (
	kept refusal = iota
	atPast
	schemeMistyped
	urlInKeywordValue
)

// refusalOf is the shape Refusal refuses dsn for, or kept. A URL is a value that begins, at its
// first byte, with a lower-case postgres:// or postgresql://, as pgx reads one (parseURLSettings);
// any other value that begins, after leading space, with "postgres:" or "postgresql:" in any case
// is a mistyped scheme; and any other value is a keyword DSN, refused when it holds postgres:// or
// postgresql:// in any case.
func refusalOf(dsn string) refusal {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		if atPastUserinfo(dsn) {
			return atPast
		}
		return kept
	}
	lower := strings.ToLower(dsn)
	trimmed := strings.TrimLeftFunc(lower, unicode.IsSpace)
	switch {
	case strings.HasPrefix(trimmed, "postgres:") || strings.HasPrefix(trimmed, "postgresql:"):
		return schemeMistyped
	case strings.Contains(lower, "postgres://") || strings.Contains(lower, "postgresql://"):
		return urlInKeywordValue
	}
	return kept
}

// atPastUserinfo reports whether dsn is a postgres:// or postgresql:// URL holding an unencoded "@"
// after pgx's user part. pgx, like libpq, ends the userinfo at the first "@" before the first "/"
// (parseURLSettings), and only a "/" stops that search. So a password holding an "@" leaves the
// rest of itself after that first "@": in the host list (`postgres://u:<head>@<tail>@db/x`), in the
// database (`postgres://u:<head>@<tail>/<more>@db/x`) or in the query (`…@<tail>?k=<more>@db/x`).
// A password holding a "/" ends the search before its own "@", so pgx reads no userinfo at all,
// the user name and the password's head as the host and the port, and the rest as the database or
// the query (`postgres://u:4821/<tail>@db/x`). Either way an unencoded "@" stands after pgx's user
// part, which is what this reads: every "@" after the userinfo pgx cuts, or every "@" when it cuts
// none. A query value holding an "@" (`?user=sa@project.iam`) and a database name holding one are
// refused too: the rule cannot tell them from a password's tail, and %40 serves both, since pgx
// decodes it. A "?" or "#" in a password stands before its "@", which pgx reads as the password's.
func atPastUserinfo(dsn string) bool {
	rest, ok := strings.CutPrefix(dsn, "postgresql://")
	if !ok {
		if rest, ok = strings.CutPrefix(dsn, "postgres://"); !ok {
			return false
		}
	}
	if i := strings.IndexAny(rest, "@/"); i >= 0 && rest[i] == '@' {
		rest = rest[i+1:]
	}
	return strings.Contains(rest, "@")
}

// ConnectError is err, a failure to parse, open or reach the Postgres dsn names, as an operator
// reads it, setting naming where dsn came from:
//
//   - A dsn Refusal refuses is named "the configured Postgres", with no driver detail: the
//     driver's text, or the server's, names the host, the database or a parameter's name, and part
//     of the credential stands there.
//   - A dsn pgx cannot parse (*pgconn.ParseConfigError) is named by setting alone, with no driver
//     detail: pgx quotes the DSN, and its own masking of a keyword DSN's password is best-effort
//     (`password = <secret>`, with spaces around the "=", prints whole).
//   - Any other failure names the address dsn points at and the driver's detail, with the password
//     taken out where a driver prints one (redactPassword).
//
// The error unwraps to err, so a caller still reads the driver's error by its type.
func ConnectError(setting, dsn string, err error) error {
	var unparsed *pgconn.ParseConfigError
	switch {
	case refusalOf(dsn) != kept:
		return &connectError{text: "connect to " + spilledAddress + ": the driver's error is not printed: it names the host, the database or a parameter's name, and part of the DSN's credential stands there", err: err}
	case errors.As(err, &unparsed):
		return &connectError{text: "connect to Postgres: " + setting + " cannot be parsed as a Postgres DSN; the driver's error is not printed, since it quotes the DSN", err: err}
	}
	address, password := target(dsn)
	return &connectError{text: fmt.Sprintf("connect to Postgres at %s: %s", address, redactPassword(err.Error(), password)), err: err}
}

// spilledAddress is how ConnectError names the Postgres of a DSN it cannot show without a
// credential.
const spilledAddress = "the configured Postgres"

// connectError is ConnectError's error: its text, and the driver's error it stands for.
type connectError struct {
	text string
	err  error
}

func (e *connectError) Error() string { return e.text }
func (e *connectError) Unwrap() error { return e.err }

// redactPassword hides a password where a driver would print one: after the password= of a keyword
// DSN, and between the : and @ of a URL. Only in those positions: a password equal to the user or
// the database name is the ordinary devbox DSN (user, password and database all `legion`), and
// blanking every occurrence would hide the fields an operator reads to diagnose a refusal.
func redactPassword(detail, password string) string {
	if password == "" {
		return detail
	}
	detail = strings.ReplaceAll(detail, "password="+password, "password=xxxxx")
	return strings.ReplaceAll(detail, ":"+password+"@", ":xxxxx@")
}

// target reads the address a DSN points at and the password it carries. pgx understands both the
// URL and the keyword/value forms; url.Parse answers when pgx's parse fails here though the failure
// ConnectError reports is another (a pool or a ping), so even then the operator learns the host.
// Neither reads part of a password as the host: they differ only for a DSN Refusal refuses, which
// ConnectError names before it asks target.
func target(dsn string) (string, string) {
	if config, err := pgconn.ParseConfig(dsn); err == nil && config.Host != "" {
		return net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))), config.Password
	}
	if parsed, err := url.Parse(dsn); err == nil && parsed.Host != "" {
		password, _ := parsed.User.Password()
		return parsed.Host, password
	}
	return spilledAddress, ""
}
