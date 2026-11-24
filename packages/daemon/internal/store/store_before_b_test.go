package store

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store/migrations"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// This file is the second half of a pair. store_b_rows_test.go's TestRowsForTheBuildBeforeB, at the
// head of the change that brings one GitHub App and the daemon's legion-verdict check (B), makes
// the pair's database (beforeBDatabase) on the server LEGION_TEST_PG_DSN names, migrates it, writes
// there through B's code the rows B writes, and leaves them. This file is then copied, alone, into
// the internal/store directory of a checkout of a build before B, and run there with the same
// LEGION_TEST_PG_DSN against the same database: a daemon of that build is what a revert of B's
// rollout puts back on that database.
//
// So this file calls only code that every build before B has, and that 52b3cf5c, the build the
// daemons ran on 2026-11-17, already had: Open, Store.Migrate, Store.Pool, Pool.BeginTx,
// Store.SchemaVersion, Store.Claims and Store.PutClaim, migrations.All, testdb.DatabaseURL, pgx's
// pgconn.ParseConfig, supervise.StateIdle and the fields of supervise.Claim it sets, and in record
// NewStore, Issues, Phases, PullRequest, PutIssue, PutPullRequest, PutPhase, Enqueue,
// NewOutboxRow, DecodeOutboxPayload and LaneOf, and the fields of Issue, PullRequest, PhaseRow,
// ReviewDecision and OutboxRow that it sets. Its SQL names B's two claims columns, which the
// database B migrated holds and no Go code of the build reads. It names none of this package's
// test helpers, since a build before B may lack them or spell them otherwise, and every name it
// declares but the test's own starts with beforeB, which no build declares.

// beforeBDatabaseName is the shape of the pair's database name: one Postgres identifier that needs
// no quotes, within Postgres's 63 bytes.
var beforeBDatabaseName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// beforeBDatabase is the pair's database and its URL: on the server dsn names, the database dsn
// names with "_b_rows" after its name. TestRowsForTheBuildBeforeB makes it anew, writes it and
// leaves it; this test reads it, in a checkout of the build before B. It is not the named database
// itself: CI's daemon-go job boots its Stage 1 daemon on the database LEGION_TEST_PG_DSN names
// after the package tests, and that proof requires a state that lists no issue, while the state
// lists the issues of every project the database holds.
func beforeBDatabase(t *testing.T, dsn string) (database, url string) {
	t.Helper()
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatal("LEGION_TEST_PG_DSN does not parse as a PostgreSQL connection string")
	}
	database = config.Database + "_b_rows"
	if !beforeBDatabaseName.MatchString(database) {
		t.Fatalf("the pair's database would be named %q, from the database LEGION_TEST_PG_DSN names: name a database of lowercase letters, digits and underscores, of at most 56 bytes", database)
	}
	url, err = testdb.DatabaseURL(dsn, database)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return database, url
}

// beforeBTables are the five tables B's migration 20261123213816_verdicts adds, which no build
// before B names in any statement. The test reads them only to know that the database is the one
// TestRowsForTheBuildBeforeB wrote.
var beforeBTables = []string{"outside_pull_requests", "verdict_reviews", "side_reviewers", "verdicts", "verdict_writes"}

// A build before B boots on the database B migrated and wrote, and reads and writes it (spec R15).
// The database must be one TestRowsForTheBuildBeforeB wrote: each of B's five tables holds a
// row, or the test fails rather than passing over a store that holds nothing of B's. Then:
//
//   - Store.Migrate applies nothing and returns no error: the database records every migration
//     this build embeds, and the migrations of B that this build does not embed stop nothing.
//   - record.DecodeOutboxPayload, the strict decode (DisallowUnknownFields) that the outbox runs
//     before each row, decodes every outbox row, and a lane of this build runs each row's kind.
//     Among them are rows of the issues whose verdicts B recorded: TestRowsForTheBuildBeforeB
//     writes one row of each kind B runs, in the transaction that writes those verdicts. A kind
//     this build runs and B does not, such as one B retired, is no row here and is asked for by
//     nothing.
//   - The record's reads read every row of issues, phases and pull_requests, the tables this
//     build knows that B writes rows of: Issues lists every issue, and Phases and PullRequest
//     of each of them read as many rows as the tables hold. The reviewer's decision B writes is a
//     state and a head in the keys this build reads, and this build reads each decision's state
//     and head as the column holds them.
//   - The record's writes write an issue, its pull request, the reviewer's verdict (the round's
//     decision, as this build records a review's) and an outbox row with no error, and each
//     reads back.
//   - The claim store reads every claims row, the reviewer's claim B wrote with the head its
//     launch gave it among them, and writes claims: B's claim written back keeps the two columns
//     B's migration adds as they were, since a build before B names neither in any statement,
//     and a claim this build writes records no launch head: a build before B leaves both null,
//     which the nullable columns with no default take, and B writes no head at generation 0.
//
// The rows this test writes are committed and left in the database, under a fresh ACME issue key
// each run, as TestRowsForTheBuildBeforeB leaves its own.
func TestTheBuildBeforeBReadsBsStore(t *testing.T) {
	dsn := os.Getenv("LEGION_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("LEGION_TEST_PG_DSN is unset, so there is no Postgres to test against: start one and set it as internal/testpg's own skip message says, which holds the whole recipe and the image to run.")
	}
	ctx := context.Background()
	database, url := beforeBDatabase(t, dsn)
	st, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("open %s, the database TestRowsForTheBuildBeforeB makes on the server LEGION_TEST_PG_DSN names (run it at B's head with the same LEGION_TEST_PG_DSN first): %v", database, err)
	}
	t.Cleanup(st.Close)
	records := record.NewStore()

	for _, table := range beforeBTables {
		var present bool
		var rows int64
		if err := beforeBTx(ctx, st, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, "select to_regclass($1) is not null", table).Scan(&present); err != nil || !present {
				return err
			}
			return tx.QueryRow(ctx, "select count(*) from "+table).Scan(&rows)
		}); err != nil {
			t.Fatalf("read whether the database holds rows of B's table %s: %v", table, err)
		}
		if !present || rows == 0 {
			t.Fatalf("%s holds no row of B's table %s (table there: %t), so it is not a store B migrated and wrote, and nothing here would be read: run TestRowsForTheBuildBeforeB at B's head with the same LEGION_TEST_PG_DSN first", database, table, present)
		}
		t.Logf("B's table %s holds %d rows", table, rows)
	}

	t.Run("Migrate applies nothing and returns no error", func(t *testing.T) {
		applied, err := st.Migrate(ctx)
		if err != nil {
			t.Fatalf("Migrate on the database B migrated: %v", err)
		}
		if applied != 0 {
			t.Errorf("Migrate applied %d migrations on the database B migrated; want none, since B's migrations include every one of this build's", applied)
		}
		embedded, err := migrations.All()
		if err != nil {
			t.Fatalf("list this build's migrations: %v", err)
		}
		recorded, err := st.SchemaVersion(ctx)
		if err != nil {
			t.Fatalf("read the database's schema version: %v", err)
		}
		t.Logf("Migrate applied %d; the database records schema version %d, and this build embeds %d migrations, up to version %d",
			applied, recorded, len(embedded), embedded[len(embedded)-1].Version)
	})

	t.Run("DecodeOutboxPayload decodes every outbox row", func(t *testing.T) {
		decoded := map[record.OutboxKind]int{}
		read := 0
		if err := beforeBTx(ctx, st, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, "select id, kind, issue, payload from outbox order by id")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var row record.OutboxRow
				var kind string
				if err := rows.Scan(&row.ID, &kind, &row.Issue, &row.Payload); err != nil {
					return err
				}
				row.Kind = record.OutboxKind(kind)
				read++
				if _, err := record.DecodeOutboxPayload(row); err != nil {
					t.Errorf("this build cannot decode outbox row %d (kind %s, issue %s): %v; its payload is %s", row.ID, kind, row.Issue, err, row.Payload)
					continue
				}
				if record.LaneOf(row.Kind) == "" {
					t.Errorf("outbox row %d decodes, but no lane of this build runs its kind %s", row.ID, kind)
					continue
				}
				decoded[row.Kind]++
			}
			return rows.Err()
		}); err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		if read == 0 {
			t.Fatal("the outbox holds no row, so no decode ran: TestRowsForTheBuildBeforeB writes one of each kind B writes")
		}
		var ofB int
		if err := beforeBTx(ctx, st, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "select count(*) from outbox where issue in (select issue from verdicts where issue <> '')").Scan(&ofB)
		}); err != nil {
			t.Fatalf("count the outbox rows of the issues whose verdicts B recorded: %v", err)
		}
		if ofB == 0 {
			t.Error("the outbox holds no row of an issue whose verdict B recorded, so no row read here is one B wrote: TestRowsForTheBuildBeforeB writes them")
		}
		t.Logf("decoded %d of %d outbox rows, %d of them of the issues whose verdicts B recorded, by kind: %v", beforeBSum(decoded), read, ofB, decoded)
	})

	t.Run("the record reads read every row of issues, phases and pull_requests", func(t *testing.T) {
		if err := beforeBSnapshot(ctx, st, func(tx pgx.Tx) error {
			var issueRows, phaseRows, pullRequestRows int
			if err := tx.QueryRow(ctx, "select (select count(*) from issues), (select count(*) from phases), (select count(*) from pull_requests)").
				Scan(&issueRows, &phaseRows, &pullRequestRows); err != nil {
				return err
			}
			issues, err := records.Issues(ctx, tx)
			if err != nil {
				t.Errorf("Issues: %v", err)
				return nil
			}
			decisions := map[string]*record.ReviewDecision{}
			phasesRead, pullRequestsRead := 0, 0
			for _, issue := range issues {
				rows, err := records.Phases(ctx, tx, issue.Key)
				if err != nil {
					t.Errorf("Phases of %s: %v", issue.Key, err)
					continue
				}
				phasesRead += len(rows)
				for _, row := range rows {
					decisions[issue.Key+"/"+string(row.Role)] = row.Decision
				}
				pr, err := records.PullRequest(ctx, tx, issue.Key)
				if err != nil {
					t.Errorf("PullRequest of %s: %v", issue.Key, err)
					continue
				}
				if pr != nil {
					pullRequestsRead++
				}
			}
			if len(issues) != issueRows || phasesRead != phaseRows || pullRequestsRead != pullRequestRows {
				t.Errorf("the reads read %d issues, %d phases rows and %d pull requests; the tables hold %d, %d and %d",
					len(issues), phasesRead, pullRequestsRead, issueRows, phaseRows, pullRequestRows)
			}
			if issueRows == 0 || phaseRows == 0 || pullRequestRows == 0 {
				t.Errorf("issues, phases or pull_requests holds no row (%d, %d, %d), so a read of it read nothing: TestRowsForTheBuildBeforeB writes each", issueRows, phaseRows, pullRequestRows)
			}

			// B's reviewer's verdict: each decision row, and what it holds of the keys this build
			// reads.
			rows, err := tx.Query(ctx, `select issue, role, coalesce(decision->>'state', ''), coalesce(decision->>'head', '') from phases
				where decision is not null and decision <> 'null'::jsonb order by issue, role`)
			if err != nil {
				return err
			}
			defer rows.Close()
			decided := 0
			for rows.Next() {
				var issue, role, state, head string
				if err := rows.Scan(&issue, &role, &state, &head); err != nil {
					return err
				}
				decided++
				got, ok := decisions[issue+"/"+role]
				switch {
				case !ok:
					t.Errorf("Phases read no %s row of %s, whose decision the column holds", role, issue)
				case got == nil || got.State != state || got.Head != head:
					t.Errorf("the %s row of %s decided %s at %s; this build read %+v", role, issue, state, head, got)
				}
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if decided == 0 {
				t.Error("no phases row holds a decision, so no read here read the reviewer's verdict B writes: TestRowsForTheBuildBeforeB writes one")
			}
			t.Logf("read %d issues, %d phases rows, %d pull requests and %d decisions", len(issues), phasesRead, pullRequestsRead, decided)
			return nil
		}); err != nil {
			t.Fatalf("read the record: %v", err)
		}
	})

	t.Run("the record writes write a verdict, a pull request and an outbox row", func(t *testing.T) {
		number := beforeBNumber(t)
		key := fmt.Sprintf("ACME-%d", number)
		head := beforeBSHA(t)
		at := time.Now().UTC().Truncate(time.Microsecond)
		issue := record.Issue{Key: key, Tree: key, Project: "ACME", Title: "A change a build before B records", Phase: phase.Reviewing,
			Generation: 1, Status: "in_progress", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets"}
		pr := record.PullRequest{Issue: key, Repo: "acme/widgets", Number: number, Branch: "legion/" + key, Base: "main",
			HeadSHA: head, State: record.PullRequestOpen, Generation: 1}
		reviewer := record.PhaseRow{Issue: key, Role: claim.RoleReviewer, Claim: claim.Token("legion-acme-" + strings.ToLower(key) + "-reviewer"),
			HandoffCommit: head, Summary: "The change does what its plan says.",
			Decision: &record.ReviewDecision{State: "approved", Body: "The change does what its plan says.", Head: head}}
		row, err := record.NewOutboxRow(key, record.MessagePost{Body: "Pull request #" + fmt.Sprint(number) + " is approved at " + head + "."}, at)
		if err != nil {
			t.Fatalf("NewOutboxRow: %v", err)
		}
		if err := beforeBTx(ctx, st, func(tx pgx.Tx) error {
			if err := records.PutIssue(ctx, tx, issue); err != nil {
				return err
			}
			if err := records.PutPullRequest(ctx, tx, pr); err != nil {
				return err
			}
			if err := records.PutPhase(ctx, tx, reviewer); err != nil {
				return err
			}
			return records.Enqueue(ctx, tx, row)
		}); err != nil {
			t.Fatalf("write an issue, its pull request, the reviewer's verdict and an outbox row on the database B migrated: %v", err)
		}

		if err := beforeBTx(ctx, st, func(tx pgx.Tx) error {
			readPR, err := records.PullRequest(ctx, tx, key)
			if err != nil {
				return err
			}
			if readPR == nil || readPR.Number != number || readPR.HeadSHA != head {
				t.Errorf("the pull request of %s reads back as %+v; want #%d at %s", key, readPR, number, head)
			}
			phases, err := records.Phases(ctx, tx, key)
			if err != nil {
				return err
			}
			if len(phases) != 1 || phases[0].Decision == nil || phases[0].Decision.State != "approved" || phases[0].Decision.Head != head {
				t.Errorf("the phases of %s read back as %+v; want the reviewer's row, approved at %s", key, phases, head)
			}
			var queued record.OutboxRow
			var kind string
			if err := tx.QueryRow(ctx, "select id, kind, issue, payload from outbox where issue = $1", key).
				Scan(&queued.ID, &kind, &queued.Issue, &queued.Payload); err != nil {
				return err
			}
			queued.Kind = record.OutboxKind(kind)
			if _, err := record.DecodeOutboxPayload(queued); err != nil {
				t.Errorf("the outbox row this build wrote does not decode: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatalf("read back what this build wrote: %v", err)
		}
		t.Logf("wrote and read back issue %s, pull request #%d at %s, the reviewer's approval and an outbox row", key, number, head)
	})

	t.Run("the claim store reads every claims row and writes one", func(t *testing.T) {
		var rows int
		var launched string
		if err := beforeBTx(ctx, st, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, "select count(*) from claims").Scan(&rows); err != nil {
				return err
			}
			return tx.QueryRow(ctx, "select coalesce(min(token), '') from claims where review_launch_head <> ''").Scan(&launched)
		}); err != nil {
			t.Fatalf("read the claims rows: %v", err)
		}
		if launched == "" {
			t.Fatal("no claims row holds a launch head, so no row read here is one B's reviewer launch wrote: TestRowsForTheBuildBeforeB writes one")
		}
		claims, err := st.Claims(ctx)
		if err != nil {
			t.Fatalf("Claims on the database B migrated and wrote: %v", err)
		}
		if len(claims) != rows {
			t.Fatalf("Claims read %d claims; the table holds %d rows", len(claims), rows)
		}
		var bs *supervise.Claim
		for i := range claims {
			if string(claims[i].Token) == launched {
				bs = &claims[i]
			}
		}
		if bs == nil {
			t.Fatalf("Claims read no claim %s, whose row holds B's launch head", launched)
		}

		// B's claim, written back by this build: B's two columns keep what B wrote.
		var headBefore, headAfter string
		var generationBefore, generationAfter int64
		readLaunch := func(head *string, generation *int64) error {
			return beforeBTx(ctx, st, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, "select review_launch_head, review_launch_generation from claims where token = $1", launched).
					Scan(head, generation)
			})
		}
		if err := readLaunch(&headBefore, &generationBefore); err != nil {
			t.Fatalf("read B's launch head of %s: %v", launched, err)
		}
		if err := st.PutClaim(ctx, *bs); err != nil {
			t.Fatalf("PutClaim of B's claim %s: %v", launched, err)
		}
		if err := readLaunch(&headAfter, &generationAfter); err != nil {
			t.Fatalf("read B's launch head of %s again: %v", launched, err)
		}
		if headAfter != headBefore || generationAfter != generationBefore {
			t.Errorf("this build's write of %s moved B's launch head from %s at %d to %s at %d; want it left as B wrote it",
				launched, headBefore, generationBefore, headAfter, generationAfter)
		}

		// A claim this build writes: its row holds neither of B's columns.
		key := fmt.Sprintf("ACME-%d", beforeBNumber(t))
		mine := supervise.Claim{Token: claim.Token("legion-acme-" + strings.ToLower(key) + "-reviewer"), Project: "acme", Tree: key, Issue: key,
			Role: claim.RoleReviewer, Generation: 1, State: supervise.StateIdle}
		if err := st.PutClaim(ctx, mine); err != nil {
			t.Fatalf("PutClaim of a claim this build makes, on the database B migrated: %v", err)
		}
		var none bool
		if err := beforeBTx(ctx, st, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "select coalesce(review_launch_head, '') = '' and coalesce(review_launch_generation, 0) = 0 from claims where token = $1",
				string(mine.Token)).Scan(&none)
		}); err != nil {
			t.Fatalf("read B's columns of the claim this build wrote: %v", err)
		}
		if !none {
			t.Errorf("the claim %s this build wrote records a launch head in B's columns; want none", mine.Token)
		}
		again, err := st.Claims(ctx)
		if err != nil {
			t.Fatalf("Claims after this build's write: %v", err)
		}
		if len(again) != rows+1 {
			t.Errorf("Claims read %d claims after this build wrote one; want %d", len(again), rows+1)
		}
		t.Logf("read %d claims, %s among them with B's launch head %s at %d, wrote it back and left that head, and wrote %s", rows, launched, headAfter, generationAfter, mine.Token)
	})
}

// beforeBNumber is a fresh issue and pull request number, so that each run's rows take keys of
// their own: a second run of this test on the pair's database writes keys the first did not.
func beforeBNumber(t *testing.T) int {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random number: %v", err)
	}
	return int(binary.BigEndian.Uint32(b[:])%900_000_000) + 100_000_000
}

// beforeBSHA is a fresh commit SHA, forty hex digits.
func beforeBSHA(t *testing.T) string {
	t.Helper()
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random commit: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// beforeBTx runs fn in one transaction on st's pool and commits it, or rolls it back when fn or
// the commit fails. It is built of Store.Pool and Pool.BeginTx alone, which every build before B
// and B itself have: Store.Tx is gone from B (store.Transact took its place), and store.Transact is
// in no build before B, so the file names neither. Nothing fn runs here registers a commit hook.
func beforeBTx(ctx context.Context, st *Store, fn func(tx pgx.Tx) error) error {
	tx, err := st.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// beforeBSum is the sum of counts.
func beforeBSum(counts map[record.OutboxKind]int) int {
	sum := 0
	for _, n := range counts {
		sum += n
	}
	return sum
}

// beforeBSnapshot runs read in one repeatable-read, read-only transaction, so that every
// statement of it reads one snapshot: a count and the read compared with it agree whatever
// another session writes between them.
func beforeBSnapshot(ctx context.Context, st *Store, read func(pgx.Tx) error) error {
	tx, err := st.Pool().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return read(tx)
}
