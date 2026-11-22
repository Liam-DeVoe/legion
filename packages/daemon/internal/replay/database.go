package replay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/legion/daemon/internal/store"
)

// replayDatabasePrefix begins the name of every database a replay makes (legion_replay_<run id>),
// so a database a run kept (RunConfig.KeepDB) is told from every other on its server.
const replayDatabasePrefix = "legion_replay_"

// replayTables is the tables a rule replays read that no daemon migration makes, created by the
// replay in its own database after the daemon's migrations, and in no daemon's database, since the
// daemon runs none of these rules: the merger's ready mark inside merging of the release at
// 9542c6fb (ReadyMark, assumption 20), which the rule legion-v19.57.3 reads, kept for the issue's
// generation and the merger's phase entry it was made in, so a later visit to merging reads none
// of it; the sign-offs the workflow held behind an issue beneath, which the rule cover reads as a
// wait (HeldSignOff), kept the same way for the architect's entry into accepting; and the covers
// that stand under the rule cover, the holder of each covered waiter. The admission pass reads
// them and keeps the covers through ruleTables. A rule added with a table of its own adds it here.
var replayTables = []string{
	`create table replay_ready_marks (
		issue text not null,
		generation bigint not null,
		entry bigint not null,
		marked_at timestamptz not null,
		primary key (issue, generation, entry)
	)`,
	`create table replay_held_signoffs (
		issue text not null,
		generation bigint not null,
		entry bigint not null,
		held_at timestamptz not null,
		primary key (issue, generation, entry)
	)`,
	`create table replay_covers (
		waiter text primary key,
		holder text not null unique
	)`,
}

// replayDatabase is the database one replay made on its DSN's server: its name, the URL it is
// reached at, and the URL of the DSN's own database, which the replay connects to only to make and
// drop its own.
type replayDatabase struct {
	name, url, admin string
}

// openReplayDatabase makes a database of the replay's own on the server dsn names, named
// legion_replay_<run id>, as a file copy of template1 (every database made from it carries the
// server's extensions), migrates it with the daemon's migrations and creates the replay's own
// tables in it (replayTables), and answers it with the store opened on it. It never writes the
// database dsn names: that connection only makes the replay's. A DSN on which no database can be
// made is refused with the server's reason.
func openReplayDatabase(ctx context.Context, dsn string) (replayDatabase, *store.Store, error) {
	if dsn == "" {
		return replayDatabase{}, nil, errors.New("replay: no Postgres DSN to make the replay's database on (--pg-dsn)")
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return replayDatabase{}, nil, fmt.Errorf("replay: name the replay's database: %w", err)
	}
	name := replayDatabasePrefix + hex.EncodeToString(suffix[:])
	target, err := databaseURL(dsn, name)
	if err != nil {
		return replayDatabase{}, nil, err
	}
	db := replayDatabase{name: name, url: target, admin: dsn}
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return replayDatabase{}, nil, fmt.Errorf("replay: connect to %s to make the replay's database: %w", redacted(dsn), err)
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()
	if _, err := admin.Exec(ctx, "create database "+pgx.Identifier{name}.Sanitize()+" template template1 strategy file_copy"); err != nil {
		return replayDatabase{}, nil, fmt.Errorf("replay: the server of %s refuses to make the replay's database %s: %w", redacted(dsn), name, err)
	}
	st, err := store.Open(ctx, target)
	if err != nil {
		return db, nil, errors.Join(fmt.Errorf("replay: open the replay's database %s: %w", name, err), db.drop(ctx))
	}
	if _, err := st.Migrate(ctx); err != nil {
		st.Close()
		return db, nil, errors.Join(fmt.Errorf("replay: migrate the replay's database %s: %w", name, err), db.drop(ctx))
	}
	if err := st.Tx(ctx, func(tx pgx.Tx) error {
		// Every fact commits, and a replay's facts are many: a commit that does not wait for the
		// write-ahead log to reach the disk changes no decision (review Q4, item 5).
		if _, err := tx.Exec(ctx, "alter database "+pgx.Identifier{name}.Sanitize()+" set synchronous_commit = off"); err != nil {
			return err
		}
		for _, table := range slices.Concat(replayTables, scopeTriggers()) {
			if _, err := tx.Exec(ctx, table); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		st.Close()
		return db, nil, errors.Join(fmt.Errorf("replay: create the replay's own tables in %s: %w", name, err), db.drop(ctx))
	}
	// The setting reaches the connections opened after it, so the pool starts again on them.
	st.Close()
	if st, err = store.Open(ctx, target); err != nil {
		return db, nil, errors.Join(fmt.Errorf("replay: open the replay's database %s: %w", name, err), db.drop(ctx))
	}
	return db, st, nil
}

// drop drops the replay's database, from a connection to the DSN's own, which it writes nothing
// to. A drop that finds a session of the database still closing is tried again for a while.
func (db replayDatabase) drop(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	admin, err := pgx.Connect(ctx, db.admin)
	if err != nil {
		return fmt.Errorf("replay: connect to drop the replay's database %s: %w", db.name, err)
	}
	defer func() { _ = admin.Close(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := admin.Exec(ctx, "drop database if exists "+pgx.Identifier{db.name}.Sanitize()+" with (force)")
		var refused *pgconn.PgError
		if err == nil || !errors.As(err, &refused) || refused.Code != "55006" || time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("replay: drop the replay's database %s: %w", db.name, err)
			}
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// databaseURL is dsn naming the database name in place of its own: the URL form's path, or a
// keyword form's dbname, whichever dsn is written in. It refuses a dsn pgx cannot read, and a
// rewrite pgx reads another server or user from than dsn, so the replay's database is made on the
// server the caller named and on no other.
func databaseURL(dsn, name string) (string, error) {
	given, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return "", fmt.Errorf("replay: read the Postgres DSN: %w", err)
	}
	var rewritten string
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("replay: read the Postgres DSN: %w", err)
		}
		parsed.Path, parsed.RawPath = "/"+name, ""
		rewritten = parsed.String()
	} else {
		rewritten = dsn + " dbname=" + name
	}
	read, err := pgconn.ParseConfig(rewritten)
	if err != nil {
		return "", fmt.Errorf("replay: name the replay's database on the Postgres DSN: %w", err)
	}
	if read.Host != given.Host || read.Port != given.Port || read.User != given.User || read.Database != name {
		return "", errors.New("replay: naming the replay's database on the Postgres DSN would reach another server or user than the DSN names")
	}
	return rewritten, nil
}

// redacted is dsn as an error names it: its server and database, never its password.
func redacted(dsn string) string {
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return "the Postgres DSN"
	}
	return fmt.Sprintf("postgres://%s@%s:%d/%s", config.User, config.Host, config.Port, config.Database)
}
