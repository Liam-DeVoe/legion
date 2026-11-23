package store

import (
	"context"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/pgmigrate"
)

// The helpers this package's own tests migrate a database with, for its external tests
// (package store_test), which import packages that import this one - graphcensus, whose census
// reads what a migration wrote - and so cannot be tests of this package.
var (
	OpenEmptyTestStore = openEmptyTestStore
	MigrateThrough     = migrateThrough
)

// CensusOf is the pre-deploy census over the migrations fsys holds, on the census's own
// connection, for TestEveryConnectionOfThePoolCarriesTheStoresMovedWriterMark, which imports
// processes and so is an external test: a census file of its own reads what that connection
// carries.
func CensusOf(ctx context.Context, databaseURL string, fsys fs.FS) (*pgmigrate.Report, error) {
	return census(ctx, databaseURL, fsys, pgmigrate.CensusOptions{})
}

// HealthPool is the health probe's pool (healthPool), which no caller is handed, for the same test.
func (p *Pool) HealthPool() (*pgxpool.Pool, error) { return p.healthPool() }
