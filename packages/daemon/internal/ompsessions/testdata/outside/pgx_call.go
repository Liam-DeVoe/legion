// A fixture of TestNoStatementRunsOutsideTheHelper: a statement on a pgx connection outside the
// helper. The check must name each line that carries a want comment, and no other.
package fixture

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func inTransaction(ctx context.Context, conn *pgx.Conn, access pgx.TxAccessMode, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: access}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path = pg_catalog, pg_temp"); err != nil {
			return err
		}
		return fn(tx)
	})
}

func inside(ctx context.Context, conn *pgx.Conn) (n int, err error) {
	err = inTransaction(ctx, conn, pgx.ReadOnly, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT 1").Scan(&n)
	})
	return n, err
}

func outside(ctx context.Context, conn *pgx.Conn) (n int, err error) {
	err = conn.QueryRow(ctx, "SELECT 1").Scan(&n) // want "conn.QueryRow (a method of *pgx.Conn)"
	return n, err
}
