package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/startup"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// seedDatabaseEnv names a database restored from a deployment's dump, at any earlier migration,
// which TestTheIssueTreesStepKeepsEveryEdgeOverASeed reads a copy of. A cluster Job sets it; no
// other run does.
const seedDatabaseEnv = "DISPATCH_SEED_DATABASE_URL"

// seedWaitEnv bounds how long the test waits for the copy's boot to migrate it and for its
// backfills to owe nothing, as a Go duration; empty is 90 minutes.
const seedWaitEnv = "DISPATCH_SEED_WAIT"

// The issue tree's step keeps every edge over a deployment's data (dispatch://INFRA-1098's T7b,
// c-view-one-select): over a copy of the database seedDatabaseEnv names, made with CREATE DATABASE
// … TEMPLATE, the root's build boots, migrating it to this head; the test waits until its
// backfills owe nothing - the boot's own run, then graphcensus.Backfill until every pass is
// complete, and the block index owed by no document - and reads graph_edges. It then announces a
// build that lists every relation kind the children moved, as their release's fleet would, boots
// the root's build again, whose step deletes dispatch://INFRA-1150's and dispatch://INFRA-1253's
// arms and the tuple arm's reads of a document's artifacts and refs rows, and reads graph_edges
// again: each read EXCEPT ALL the other is empty, both ways. It prints both counts and every line
// the step logged, and fails where the issue tree's step kept its arms, naming what held them. It
// drops its own copy and nothing else.
//
// Without seedDatabaseEnv it does not run. With it, anything missing fails it.
func TestTheIssueTreesStepKeepsEveryEdgeOverASeed(t *testing.T) {
	seedURL := os.Getenv(seedDatabaseEnv)
	if seedURL == "" {
		t.Skipf("%s names no seed database; a cluster Job that restores one runs this test", seedDatabaseEnv)
	}
	wait := 90 * time.Minute
	if raw := os.Getenv(seedWaitEnv); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("%s=%q is no duration: %v", seedWaitEnv, raw, err)
		}
		wait = parsed
	}
	ctx := context.Background()
	copyURL := copySeedDatabase(t, seedURL)
	rootBuild := cmdtest.BuildStamped(t, "envoy-dispatch-seed-root", oneSelectRootBuild)

	first := startDispatchProcessWith(t, copyURL, dispatchOptions{Binary: rootBuild, SigningKey: multiprocessSigningKey})
	if err := first.healthyWithin(wait); err != nil {
		t.Fatal(err)
	}
	database := openSeedStore(t, copyURL)
	waitForTheBackfills(t, ctx, database, first, wait)
	stopDeployedProcess(t, first)
	scanSeed := func(what, statement string) int64 {
		t.Helper()
		var n int64
		if err := database.Pool.QueryRow(ctx, statement).Scan(&n); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		t.Logf("seed proof: %s = %d", what, n)
		return n
	}
	if _, err := database.Pool.Exec(ctx, `create table seed_proof_graph_edges as select * from graph_edges`); err != nil {
		t.Fatalf("read graph_edges before the step: %v", err)
	}
	scanSeed("graph_edges rows before the step", `select count(*) from seed_proof_graph_edges`)
	scanSeed("refs rows", `select count(*) from refs`)
	scanSeed("refs rows past the store's bound", `select count(*) from refs where octet_length(to_id) > 1024`)
	scanSeed("document mentions before the step", `select count(*) from seed_proof_graph_edges where from_kind = 'artifact' and kind = 'mentions'`)
	scanSeed("attached_to before the step", `select count(*) from seed_proof_graph_edges where from_kind = 'artifact' and kind = 'attached_to'`)

	if _, err := database.Pool.Exec(ctx, `insert into server_build_announcements (commit, tuple_kinds) values ($1, $2)`,
		oneSelectChildrenRelease, refs.TupleKinds()); err != nil {
		t.Fatalf("announce the children's release: %v", err)
	}
	second := startDispatchProcessWith(t, copyURL, dispatchOptions{Binary: rootBuild, SigningKey: multiprocessSigningKey})
	if err := second.healthyWithin(wait); err != nil {
		t.Fatal(err)
	}
	var logged []string
	for _, line := range strings.Split(second.Output.String(), "\n") {
		if strings.Contains(line, "retirement") {
			logged = append(logged, line)
		}
	}
	t.Logf("seed proof: the step logged:\n%s", strings.Join(logged, "\n"))
	if !strings.Contains(second.Output.String(), deletedLine(startup.IssueTreeArmsRetirement)) {
		t.Errorf("the issue tree's step kept its arms over the seed; its lines above name what held them")
	}
	gone := scanSeed("before EXCEPT ALL after", `select count(*) from (select * from seed_proof_graph_edges except all select * from graph_edges) d`)
	came := scanSeed("after EXCEPT ALL before", `select count(*) from (select * from graph_edges except all select * from seed_proof_graph_edges) d`)
	scanSeed("graph_edges rows after the step", `select count(*) from graph_edges`)
	if gone != 0 || came != 0 {
		t.Errorf("graph_edges lost %d rows and gained %d over the step, want none either way", gone, came)
	}
	stopDeployedProcess(t, second)
}

// copySeedDatabase makes a copy of the database seedURL names under a name of its own, with
// CREATE DATABASE … TEMPLATE, and drops that copy, by its name, when the test ends.
func copySeedDatabase(t *testing.T, seedURL string) string {
	t.Helper()
	ctx := context.Background()
	config, err := pgx.ParseConfig(seedURL)
	if err != nil {
		t.Fatalf("parse %s: %v", seedDatabaseEnv, err)
	}
	if config.Database == "" {
		t.Fatalf("%s names no database", seedDatabaseEnv)
	}
	seed := config.Database
	copied := fmt.Sprintf("seed_proof_%d", time.Now().UnixNano())
	config.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to the seed's server: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `create database `+pgx.Identifier{copied}.Sanitize()+` template `+pgx.Identifier{seed}.Sanitize()); err != nil {
		t.Fatalf("copy the seed database %s: %v", seed, err)
	}
	t.Cleanup(func() {
		conn, err := pgx.ConnectConfig(context.Background(), config)
		if err != nil {
			t.Errorf("connect to drop the copy %s: %v", copied, err)
			return
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(context.Background(), `drop database if exists `+pgx.Identifier{copied}.Sanitize()+` with (force)`); err != nil {
			t.Errorf("drop the copy %s: %v", copied, err)
		}
	})
	config.Database = copied
	return config.ConnString()
}

// openSeedStore opens a store on the copy, closed when the test ends.
func openSeedStore(t *testing.T, url string) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("open the copy: %v", err)
	}
	t.Cleanup(database.Pool.Close)
	return database
}

// waitForTheBackfills waits up to within for process's boot to have run the graph store's
// backfill, then runs graphcensus.Backfill until every pass is complete, and waits until no
// document owes its block index, failing on what is still owed when the time is up.
func waitForTheBackfills(t *testing.T, ctx context.Context, database *store.Store, process *dispatchProcess, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !strings.Contains(process.Output.String(), bootBackfillDoneLine) {
		if time.Now().After(deadline) {
			t.Fatalf("the boot never logged %q in %s:\n%s", bootBackfillDoneLine, within, process.Output.String())
		}
		time.Sleep(time.Second)
	}
	for {
		report, err := graphcensus.Backfill(ctx, database, io.Discard)
		if err != nil {
			if _, only := graphcensus.OnlyProjectMoved(err); !only {
				t.Fatalf("backfill the copy: %v", err)
			}
		}
		var owed []string
		for _, kind := range report.Kinds {
			if !kind.Complete {
				owed = append(owed, fmt.Sprintf("%s remaining=%d", kind.Kind, kind.Remaining))
			}
		}
		var unindexed int64
		if err := database.Pool.QueryRow(ctx, `
			select count(*) from document_block_versions where version is null or store_owed_at is not null
		`).Scan(&unindexed); err != nil {
			t.Fatalf("count the documents owed their block index: %v", err)
		}
		if len(owed) == 0 && unindexed == 0 {
			t.Logf("seed proof: the backfills owe nothing")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after %s the backfills still owe %v and %d documents their block index", within, owed, unindexed)
		}
		time.Sleep(10 * time.Second)
	}
}
