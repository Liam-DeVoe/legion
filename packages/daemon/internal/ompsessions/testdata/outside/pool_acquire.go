// A fixture of TestNoStatementRunsOutsideTheHelper: a connection acquired from a pool outside the
// helper, and a statement on it. The check must name each line that carries a want comment, and
// no other.
package fixture

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func inTransaction(ctx context.Context, pool *pgxpool.Pool, access pgx.TxAccessMode, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: access}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path = pg_catalog, pg_temp"); err != nil {
			return err
		}
		return fn(tx)
	})
}

func inside(ctx context.Context, pool *pgxpool.Pool) error {
	return inTransaction(ctx, pool, pgx.ReadWrite, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT 1")
		return err
	})
}

func outside(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx) // want "pool.Acquire (a method of *pgxpool.Pool)"
	if err != nil {
		return err
	}
	defer conn.Release()
	_, err = conn.Exec(ctx, "SELECT 1") // want "conn.Exec (a method of *pgxpool.Conn)"
	return err
}
