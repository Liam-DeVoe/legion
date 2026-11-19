package store

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// A server process's identity is one uuid, minted when Open opens its pool, which every
// connection the process opens to Postgres carries at the end of its application_name, after one
// space: the shared pool's, every separate pool's (Pool.separate), the registry's (it copies the
// shared pool's configuration), and the dedicated connections (Pool.Dedicated), whose names end
// in the process id the document service takes from this identity (docs.Service.ProcessID). The
// events outbox holds the publisher hold under the same uuid (outbox.New), so the hold's row names
// the process whose sessions a standby that takes the hold over ends (EndSessionsOf).

// defaultApplicationName is the shared pool's application_name, before the identity, when the
// connection string names none.
const defaultApplicationName = "dispatch"

// maxApplicationName is the longest application_name Postgres keeps: it truncates a longer one
// to NAMEDATALEN - 1 bytes, which would cut the identity off its end.
const maxApplicationName = 63

// applicationName is base followed by one space and identity, with base cut, on a rune boundary,
// so the whole fits in maxApplicationName and the identity stays whole at its end. A nil identity
// (a pool NewPool wrapped) leaves base as it is.
func applicationName(base string, identity uuid.UUID) string {
	if identity == uuid.Nil {
		return base
	}
	suffix := " " + identity.String()
	if room := maxApplicationName - len(suffix); len(base) > room {
		base = base[:room]
		for !utf8.ValidString(base) {
			base = base[:len(base)-1]
		}
	}
	return base + suffix
}

// Identity is the identity every connection of this pool names at the end of its
// application_name: the one Open minted for it, shared by every separate pool opened from it
// (Outbox, TextTrees and the rest). A pool NewPool wrapped has none (uuid.Nil): its connections
// carry whatever name their own configuration gives them.
func (p *Pool) Identity() uuid.UUID { return p.identity }

// EndedSession is one database session EndSessionsOf ended: the backend's process id, its
// application_name, and whether pg_terminate_backend signalled it (false when the backend had
// already gone).
type EndedSession struct {
	PID             int32
	ApplicationName string
	Signalled       bool
}

// endSessionsOf ends every other session of this database whose application_name ends with one
// space and the identity $1. The identity is a whole uuid at the very end of the name, so neither
// another process's identity nor a name that merely begins with this one's matches. Every
// Dispatch process connects as one database role, which may signal its own backends.
const endSessionsOf = `
	select pid, coalesce(application_name, ''), pg_terminate_backend(pid)
	from pg_stat_activity
	where datname = current_database() and pid <> pg_backend_pid()
	  and right(application_name, 37) = ' ' || $1::text
	order by pid`

// EndSessionsOf ends (pg_terminate_backend) every session of this database that names identity at
// the end of its application_name, and returns each it ended. The backend of each ends at once,
// whatever its client is doing: a transaction it held open is rolled back and its locks are
// released, and its client's next read answers SQLSTATE 57P01 (SessionEnded). It refuses the nil
// identity, which names no process.
func (p *Pool) EndSessionsOf(ctx context.Context, identity uuid.UUID) ([]EndedSession, error) {
	if identity == uuid.Nil {
		return nil, errors.New("end the sessions of the nil identity: it names no process")
	}
	rows, err := p.Query(ctx, endSessionsOf, identity.String())
	if err != nil {
		return nil, fmt.Errorf("end the sessions of %s: %w", identity, err)
	}
	ended, err := pgx.CollectRows(rows, pgx.RowToStructByPos[EndedSession])
	if err != nil {
		return nil, fmt.Errorf("end the sessions of %s: %w", identity, err)
	}
	return ended, nil
}

// sessionEndedCode is SQLSTATE 57P01, admin_shutdown: what a backend answers its client when
// pg_terminate_backend, or a server shutdown, ended its session.
const sessionEndedCode = "57P01"

// SessionEnded reports whether err is a statement whose database session was ended under it
// (SQLSTATE 57P01): by a standby that took the publisher hold over from this process
// (EndSessionsOf), by an operator, or by the server shutting down. The request it served did
// nothing that commits; the pool dials a fresh connection for the next one.
func SessionEnded(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == sessionEndedCode
}
