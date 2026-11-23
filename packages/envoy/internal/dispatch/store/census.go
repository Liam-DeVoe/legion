package store

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/pgmigrate"
)

// censusApplicationName is how the census's one connection appears in pg_stat_activity, and
// versionsApplicationName how the version check's does.
const (
	censusApplicationName   = "envoy-dispatch census"
	versionsApplicationName = "envoy-dispatch versions"
)

// Census takes the pre-deploy census (pgmigrate.Census) of the database databaseURL names against
// the migrations this binary embeds, on one connection of its own: a deployment runs it before it
// rolls the service, so it uses no pool and migrates nothing.
func Census(ctx context.Context, databaseURL string) (*pgmigrate.Report, error) {
	return census(ctx, databaseURL, migrationFiles, pgmigrate.CensusOptions{})
}

func census(ctx context.Context, databaseURL string, fsys fs.FS, options pgmigrate.CensusOptions) (*pgmigrate.Report, error) {
	migrations, err := pgmigrate.Load(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migrations refused: %w", err)
	}
	config, err := connConfig(databaseURL, censusApplicationName)
	if err != nil {
		return nil, err
	}
	// The census's one author-written statement is pinned to the extended protocol on its own
	// call (pgmigrate.Census); this pins the connection's default too, so a connection string
	// asking for the simple protocol changes nothing the census runs.
	config.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)
	return pgmigrate.Census(ctx, conn, migrations, "schema_migrations", options)
}

// CheckVersions reads what schema_migrations, on the database databaseURL names, records of each
// version against the migrations this binary embeds, and what the runner's next boot would do
// with each row (pgmigrate.CheckVersions). It holds one connection of its own, in a read-only
// transaction, and writes nothing.
func CheckVersions(ctx context.Context, databaseURL string) (*pgmigrate.VersionCheck, error) {
	migrations, err := pgmigrate.Load(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migrations refused: %w", err)
	}
	conn, err := Connect(ctx, databaseURL, versionsApplicationName)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	return pgmigrate.CheckVersions(ctx, conn, migrations, versionTable)
}

// UnappliedMigrations answers the version of every migration this binary embeds that
// schema_migrations, read in tx, records no row of, in the order the runner applies them: every
// one of them where the table does not exist yet (pgmigrate.RecordedVersions). It writes nothing,
// so a reader that needs a database migrated to this build - the store census - asks it in its own
// read-only snapshot rather than probing a table or a column of one migration.
func UnappliedMigrations(ctx context.Context, tx pgx.Tx) ([]int, error) {
	pending, err := PendingMigrations(ctx, tx)
	if err != nil {
		return nil, err
	}
	versions := make([]int, len(pending))
	for index, migration := range pending {
		versions[index] = migration.Version
	}
	return versions, nil
}

// PendingMigrations answers every migration this binary embeds that schema_migrations, read in tx,
// records no row of, whole, in the order the runner applies them (UnappliedMigrations): what the
// pre-deploy census's rehearsal of the stores' move reads to know which it may run
// (storesmove.Rehearse).
func PendingMigrations(ctx context.Context, tx pgx.Tx) ([]pgmigrate.Migration, error) {
	migrations, err := Migrations()
	if err != nil {
		return nil, err
	}
	recorded, err := pgmigrate.RecordedVersions(ctx, tx, versionTable)
	if err != nil {
		return nil, err
	}
	var pending []pgmigrate.Migration
	for _, migration := range migrations {
		if !recorded[migration.Version] {
			pending = append(pending, migration)
		}
	}
	return pending, nil
}

// Migrations is every migration this binary embeds, in the order the runner applies them
// (pgmigrate.Load).
func Migrations() ([]pgmigrate.Migration, error) {
	migrations, err := pgmigrate.Load(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migrations refused: %w", err)
	}
	return migrations, nil
}
