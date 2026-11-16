// Package testpool holds the size of every Postgres pool a test of this module builds
// (PoolConns), and NewPool, which is pgxpool.New at that size. pgxpool's own default follows the
// machine's CPU count, so a test that took it would run with a pool of a different size on every
// machine.
//
// It is a package of its own, beside internal/testpg rather than in it, because it imports
// pgxpool and the standard library and nothing else. internal/testpg imports testcontainers to
// start a Postgres container, and a test binary that took the pool size from it would link
// testcontainers for one constant. This package imports nothing of this module either, so every
// package's tests can take it, internal/dispatch/store's and internal/broker/store's own among
// them.
package testpool

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConns is the size, as MaxConns, of every Postgres pool a test of this module builds.
// pgxpool's default is max(4, runtime.NumCPU()), so an unsized pool holds 4 connections on CI's
// 4-vCPU runners and hundreds on a large devbox, where a test's concurrent work on one pool can
// open more connections than the server's max_connections and fail with SQLSTATE 53300
// (dispatch://INFRA-3904). Four is what every unsized pool already got on the CI runners that run
// these suites, so every test is already green at it, and it is pgx's own floor. It is never
// derived from runtime.NumCPU or GOMAXPROCS: a test's pool is the same size on every machine.
//
// internal/broker/store/storetest sizes the broker store's pool with it. A pool a test copies from
// a Dispatch store's configuration keeps that store's own size, which store.Open fixes in code. A
// test that needs another size sets MaxConns itself.
const PoolConns = 4

// NewPool is pgxpool.New with MaxConns fixed at PoolConns: a pool on dsn that dials nothing until
// it is used, as pgxpool.New's does, and that answers pgxpool's own errors unchanged.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	config.MaxConns = PoolConns
	return pgxpool.NewWithConfig(ctx, config)
}
