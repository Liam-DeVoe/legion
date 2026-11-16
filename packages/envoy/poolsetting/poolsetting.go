// Package poolsetting reads whether a Postgres connection string names pgxpool's pool size,
// pool_max_conns. A store that sizes its own pool keeps a size the connection string names and
// sets its own default otherwise, so it must know whether one is named. pgxpool.ParseConfig takes
// the key out of the config it returns, so that config cannot tell a named size from pgxpool's
// default, max(4, runtime.NumCPU()), which follows the machine (dispatch://INFRA-3904).
//
// The broker's store (internal/broker/store) and the daemon's (packages/daemon/internal/store)
// both read it, each with a default of its own. It is a public package, as migrationid is, because
// Go's internal rule keeps the daemon's module out of this module's internal tree.
package poolsetting

import "github.com/jackc/pgx/v5/pgconn"

// Key is the connection setting pgxpool.ParseConfig reads a pool's MaxConns from.
const Key = "pool_max_conns"

// Named reports whether connString's effective settings name Key, as pgx itself reads them:
// pgconn.ParseConfig resolves a URL's query, a keyword/value string with its quoting, and the keys
// of the service file entry a `service=` setting names, and keeps every key it does not know itself
// among RuntimeParams. A connection string pgx cannot parse names nothing here; pgxpool.ParseConfig
// then refuses it to its caller.
func Named(connString string) bool {
	settings, err := pgconn.ParseConfig(connString)
	if err != nil {
		return false
	}
	_, named := settings.RuntimeParams[Key]
	return named
}
