// A fixture of TestNoStatementRunsOutsideTheHelper: a driver handle that leaves the package's own
// functions, held as another package's interface or handed to another package's function, which
// runs statements on it the check reads no line of. The check must name each line that carries a
// want comment, and no other.
package fixture

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/legion/daemon/internal/record"
)

type pinned struct{ tx pgx.Tx }

func (p pinned) exec(ctx context.Context, sql string) (pgconn.CommandTag, error) {
	return p.tx.Exec(ctx, sql)
}

func inTransaction(ctx context.Context, conn *pgx.Conn, readOnly bool, fn func(pinned) error) error {
	access := pgx.ReadWrite
	if readOnly {
		access = pgx.ReadOnly
	}
	return pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: access}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path = pg_catalog, pg_temp"); err != nil {
			return err
		}
		return fn(pinned{tx})
	})
}

func inside(ctx context.Context, conn *pgx.Conn) error {
	return inTransaction(ctx, conn, false, func(p pinned) error {
		_, err := p.exec(ctx, "SELECT 1")
		return err
	})
}

func heldAsRowQuerier(ctx context.Context, conn *pgx.Conn) (n int, err error) {
	var q record.RowQuerier = conn // want "conn (a *pgx.Conn held as record.RowQuerier)"
	err = q.QueryRow(ctx, "SELECT 1").Scan(&n)
	return n, err
}

func handedToRecord(ctx context.Context, conn *pgx.Conn) (bool, error) {
	return record.PullRequestRecorded(ctx, conn, "owner/repo", 1) // want "conn (a *pgx.Conn handed to record.PullRequestRecorded)"
}

func returnedAsAny(conn *pgx.Conn) any {
	return conn // want "conn (a *pgx.Conn held as any)"
}

func printed(conn *pgx.Conn) string {
	return fmt.Sprint(conn) // want "conn (a *pgx.Conn handed to fmt.Sprint)"
}

// Errors are no handles: an error of the driver's goes where any error goes.
func wrapped(err *pgconn.PgError) error {
	return fmt.Errorf("the store refused: %w", err)
}
