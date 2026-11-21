// A fixture of TestNoStatementRunsOutsideTheHelper: the other ways a statement runs outside the
// helper. A transaction it did not open, begun on the connection or handed in by a caller;
// pgx.BeginFunc; a statement method taken as a value; and pgx's own PgConn under a connection.
// The check must name each line that carries a want comment, and no other.
package fixture

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func inTransaction(ctx context.Context, conn *pgx.Conn, access pgx.TxAccessMode, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: access}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path = pg_catalog, pg_temp"); err != nil {
			return err
		}
		return fn(tx)
	})
}

func inside(ctx context.Context, conn *pgx.Conn) error {
	return inTransaction(ctx, conn, pgx.ReadWrite, func(tx pgx.Tx) error {
		run := func(sql string) error {
			_, err := tx.Exec(ctx, sql)
			return err
		}
		return run("SELECT 1")
	})
}

func begun(ctx context.Context, conn *pgx.Conn) error {
	tx, err := conn.Begin(ctx) // want "conn.Begin (a method of *pgx.Conn)"
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SELECT 1") // want "tx.Exec (a method of pgx.Tx)"
	return err
}

func beginFunc(ctx context.Context, conn *pgx.Conn) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error { // want "pgx.BeginFunc (a function of pgx)"
		_, err := tx.Exec(ctx, "SELECT 1") // want "tx.Exec (a method of pgx.Tx)"
		return err
	})
}

// Handed runs a statement in a transaction its caller hands it, which the helper did not open.
func Handed(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT 1") // want "tx.Exec (a method of pgx.Tx)"
	return err
}

func value(conn *pgx.Conn) func(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return conn.Exec // want "conn.Exec (a method of *pgx.Conn)"
}

func underneath(ctx context.Context, conn *pgx.Conn) error {
	return conn.PgConn().Exec(ctx, "SELECT 1").Close() // want "conn.PgConn().Exec (a method of *pgconn.PgConn)"
}
