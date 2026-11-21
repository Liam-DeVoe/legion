package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Connect opens one connection of its own to the database databaseURL names, outside any pool,
// named applicationName in pg_stat_activity, so an operator reading the activity of a database
// sees which pass holds it. It migrates nothing: an operator pass that holds one connection - the
// store census (graphcensus) and the erase (grapherase) - works on the schema the deployment's
// own server migrated. The connection carries this build's marker writer setting
// (StoreMarkerWriterSetting), as every connection of its pool does: the erase rewrites this
// build's marked rows (grapherase's rewriteRepliedRows), which keep their mark only under it, and
// the census writes nothing.
func Connect(ctx context.Context, databaseURL, applicationName string) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse Postgres URL: %w", err)
	}
	config.RuntimeParams["application_name"] = applicationName
	config.RuntimeParams[StoreMarkerWriterSetting] = "on"
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return conn, nil
}
