package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	brokerstore "github.com/sjawhar/envoy/internal/broker/store"
	brokerstoretest "github.com/sjawhar/envoy/internal/broker/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// runbookPath is Dispatch's runbook, whose recovery after a Dispatch database restored or made again
// alone ("Signing people in") the tests here run block by block.
const runbookPath = "../../../../../docs/site/src/content/docs/dispatch/running-dispatch.md"

// The leads of the runbook's paragraphs whose first ```sh block, or whose branch, the recovery
// runs.
const (
	sweepLead            = "The counter finds only an address that owes a revocation."
	brokerURLLead        = "Every block below that reads the broker's database reaches it with `psql`"
	madeAgainLead        = "A broker database **made again**, rather than restored, has no such moment"
	brokerGenerationLead = "Then take each address it lists, all of the address's revocations in one pass."
	brokerRefusedLead    = "and every account it refuses there, by `sub`:"
	brokerLiveLead       = "and every account that still holds a live credential there and is not one it refuses, by `sub`:"
	dispatchHolderLead   = "On Dispatch's database, read the account the address records, and list every revocation it owes:"
	raiseLead            = "Raise the address's session generation on Dispatch's database past every generation the lost"
	directoryLead        = "Then decide which Google account holds the address now."
	holderHoldsItLead    = "**The account the address records holds it**"
	staleRowLead         = "**Another account holds it**"
	writeBackLead        = "The broker refuses an account it was told no longer holds an address"
	brokerRestoredLead   = "The other way round, when it is **the broker's database that was restored alone**"
)

// bearerOnACommandLine is a curl whose arguments hold a bearer token: the header given on the
// command line rather than read from a file with -H @file.
var bearerOnACommandLine = regexp.MustCompile(`\bcurl\b[^\n]*-H\s+["']Authorization: Bearer`)

// TestTheRunbookPassesNoBearerTokenOnACommandLine: a token in curl's arguments sits in
// /proc/<pid>/cmdline while the call runs, which every user of the box can read. Every curl of
// Dispatch's runbook that sends a token reads its header from a file the step writes first
// (`-H @"$header"`), the directory read of the restore recovery among them.
//
// Control, in the test: the line the runbook's directory read had before this change is caught.
func TestTheRunbookPassesNoBearerTokenOnACommandLine(t *testing.T) {
	if before := `curl -sS --fail-with-body -H "Authorization: Bearer $DIRECTORY_TOKEN" "https://admin.googleapis.com/admin/directory/v1/users/<address>"`; !bearerOnACommandLine.MatchString(before) {
		t.Fatalf("control: the pattern does not catch %q, so the test shows nothing", before)
	}
	for number, line := range runbookLines(t) {
		if bearerOnACommandLine.MatchString(line) {
			t.Errorf("running-dispatch.md:%d passes a bearer token on curl's command line, where every user of the box reads it: %s", number+1, strings.TrimSpace(line))
		}
	}
	directory := strings.Join(runbookBlockAfter(t, directoryLead).lines, "\n")
	if !strings.Contains(directory, `-H @"$header"`) || !strings.Contains(directory, `printf 'Authorization: Bearer %s\n' "$DIRECTORY_TOKEN" >"$header"`) {
		t.Errorf("the runbook's directory read is %q; want its token written to a file and read with -H @\"$header\"", directory)
	}
}

// runbookBlock is one ```sh block of the runbook: the SQL statements of its psql heredocs, in
// order, each without its closing semicolon and with its psql variables (:'name') turned into
// named placeholders (@name); its other lines, the commands, as written; and every line of the
// block as it stands, for a test that runs the block itself.
type runbookBlock struct {
	statements, commands, lines []string
}

// psqlVariable is a psql variable interpolated as a literal, :'name'.
var psqlVariable = regexp.MustCompile(`:'([a-z_]+)'`)

// runbookBlockAfter is the first ```sh block the runbook gives after the line starting with lead,
// its lines trimmed.
func runbookBlockAfter(t *testing.T, lead string) runbookBlock {
	t.Helper()
	lines := runbookLines(t)
	start := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), lead) })
	if start < 0 {
		t.Fatalf("the runbook has no line starting %q", lead)
	}
	var block runbookBlock
	var statement []string
	fenced, heredoc := false, false
	for _, line := range lines[start:] {
		line = strings.TrimSpace(line)
		switch {
		case !fenced:
			fenced = line == "```sh"
		case heredoc && line == "SQL":
			if len(statement) > 0 {
				t.Fatalf("the runbook's block after %q ends a heredoc inside a statement: %q", lead, statement)
			}
			heredoc = false
			block.lines = append(block.lines, line)
		case heredoc && strings.HasPrefix(line, `\`):
			// A psql meta-command inside the heredoc (\copy), which is no SQL statement.
			block.commands = append(block.commands, line)
			block.lines = append(block.lines, line)
		case heredoc:
			statement = append(statement, line)
			block.lines = append(block.lines, line)
			if strings.HasSuffix(line, ";") {
				block.statements = append(block.statements,
					psqlVariable.ReplaceAllString(strings.TrimSuffix(strings.Join(statement, "\n"), ";"), "@$1"))
				statement = nil
			}
		case line == "```":
			return block
		default:
			block.commands = append(block.commands, line)
			block.lines = append(block.lines, line)
			heredoc = strings.HasSuffix(line, "<<'SQL'") || strings.HasSuffix(line, "<<SQL")
		}
	}
	t.Fatalf("the runbook gives no closed ```sh block after %q", lead)
	return block
}

// runbookLines is the runbook, line by line.
func runbookLines(t *testing.T) []string {
	t.Helper()
	text, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("read the runbook: %v", err)
	}
	return strings.Split(string(text), "\n")
}

// runbookParagraph is the paragraph of the runbook that starts with lead, as one line of text: the
// branches of the recovery that are instructions rather than blocks are read from it, so a test
// follows what the page says rather than what it remembers.
func runbookParagraph(t *testing.T, lead string) string {
	t.Helper()
	lines := runbookLines(t)
	start := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), lead) })
	if start < 0 {
		t.Fatalf("the runbook has no paragraph starting %q", lead)
	}
	var paragraph []string
	for _, line := range lines[start:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		paragraph = append(paragraph, strings.TrimSpace(line))
	}
	return strings.Join(paragraph, " ")
}

// runbookDatabase is a pool a block's statements run on: Dispatch's (store.Pool) or the broker's
// (pgxpool.Pool).
type runbookDatabase interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// runStatements runs statements on database with args, as psql runs a heredoc: one between begin
// and commit in one transaction, otherwise each on its own.
func runStatements(t *testing.T, database runbookDatabase, statements []string, args pgx.NamedArgs) {
	t.Helper()
	ctx := context.Background()
	if len(statements) >= 2 && statements[0] == "begin" && statements[len(statements)-1] == "commit" {
		tx, err := database.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		for _, statement := range statements[1 : len(statements)-1] {
			if _, err := tx.Exec(ctx, statement, args); err != nil {
				t.Fatalf("the runbook's %q: %v", statement, err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("the runbook's commit: %v", err)
		}
		return
	}
	for _, statement := range statements {
		if _, err := database.Exec(ctx, statement, args); err != nil {
			t.Fatalf("the runbook's %q: %v", statement, err)
		}
	}
}

// queryRunbook runs the one statement of block on database with args and answers the text of each
// row's first column, as psql -At prints it.
func queryRunbook(t *testing.T, database runbookDatabase, block runbookBlock, args pgx.NamedArgs) []string {
	t.Helper()
	if len(block.statements) != 1 {
		t.Fatalf("the runbook's block %q holds %d statements, want the one read", block.commands, len(block.statements))
	}
	rows, err := database.Query(context.Background(), block.statements[0], args)
	if err != nil {
		t.Fatalf("the runbook's %q: %v", block.statements[0], err)
	}
	values, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		columns, err := row.Values()
		if err != nil || len(columns) == 0 || columns[0] == nil {
			return "", err
		}
		return fmt.Sprint(columns[0]), nil
	})
	if err != nil {
		t.Fatalf("the runbook's %q: %v", block.statements[0], err)
	}
	return values
}

// sweep is the runbook's sweep (sweepLead): brokerHalf is the query it sends the broker's
// database (psql --csv -c, written to a file), createTemporary and copyIn are the table it makes
// on Dispatch's database and the copy that fills it from that file rather than from an argument,
// and dispatchHalf is the select that joins them.
type sweep struct {
	brokerHalf, createTemporary, copyIn, dispatchHalf string
	// lines is the block as the runbook writes it, for the run through psql itself.
	lines []string
	// urlStep is the block that makes the libpq URL every broker block names, run before the
	// sweep when the sweep is run through psql itself.
	urlStep []string
}

// runbookSweep reads the sweep's parts from the runbook.
func runbookSweep(t *testing.T) sweep {
	t.Helper()
	block := runbookBlockAfter(t, sweepLead)
	var s sweep
	s.lines = block.lines
	s.urlStep = runbookBlockAfter(t, brokerURLLead).lines
	brokerHalf := regexp.MustCompile(`^psql "\$BROKER_PSQL_URL" .*-c "([^"]+)"`)
	for _, command := range block.commands {
		if match := brokerHalf.FindStringSubmatch(command); match != nil {
			s.brokerHalf = match[1]
		}
		if strings.HasPrefix(command, `\copy `) {
			s.copyIn = command
		}
	}
	for _, statement := range block.statements {
		switch {
		case strings.HasPrefix(statement, "create temp table "):
			s.createTemporary = statement
		case strings.HasPrefix(statement, "select "):
			s.dispatchHalf = statement
		}
	}
	if s.brokerHalf == "" || s.createTemporary == "" || s.copyIn == "" || s.dispatchHalf == "" {
		t.Fatalf("the runbook's sweep %q, %q gives broker half %q, temporary table %q, copy %q and select %q, want one of each, the broker's read taking $BROKER_PSQL_URL",
			block.commands, block.statements, s.brokerHalf, s.createTemporary, s.copyIn, s.dispatchHalf)
	}
	if !strings.Contains(s.copyIn, "$rows") {
		t.Fatalf("the runbook's sweep fills the temporary table with %q, want it copied from the file the broker half wrote: an argument above 128 KiB is refused E2BIG", s.copyIn)
	}
	if !strings.Contains(strings.Join(s.urlStep, "\n"), "BROKER_PSQL_URL=") {
		t.Fatalf("the runbook's block after %q is %q, want the one that makes $BROKER_PSQL_URL from the broker's own URL", brokerURLLead, s.urlStep)
	}
	return s
}

// listedAddress is a row of the sweep: an address and its generation at the broker and at Dispatch.
type listedAddress struct {
	person               string
	brokerAt, dispatchAt int64
}

// brokerRow is one row of the broker half's answer: an address and the generation the broker
// recorded for it.
type brokerRow struct {
	person     string
	generation int64
}

// run runs the sweep on the two databases, as psql runs it: the broker half's rows, then the
// temporary table, the rows copied into it and the select. It answers how many rows the broker
// half gave, whether the guard reports UNKNOWN for that (no row at all), and the addresses the
// Dispatch half lists.
func (s sweep) run(t *testing.T, broker *brokerstore.Store, dispatch *store.Store) (int, bool, []listedAddress) {
	t.Helper()
	ctx := context.Background()
	rows, err := broker.Pool.Query(ctx, s.brokerHalf)
	if err != nil {
		t.Fatalf("the sweep's broker half %q: %v", s.brokerHalf, err)
	}
	generations, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (brokerRow, error) {
		var r brokerRow
		return r, row.Scan(&r.person, &r.generation)
	})
	if err != nil {
		t.Fatalf("the sweep's broker half: %v", err)
	}
	if len(generations) == 0 {
		return 0, true, nil
	}
	tx, err := dispatch.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, s.createTemporary); err != nil {
		t.Fatalf("the sweep's %q: %v", s.createTemporary, err)
	}
	for _, row := range generations {
		if _, err := tx.Exec(ctx, `insert into broker_generations (person, session_generation) values ($1, $2)`, row.person, row.generation); err != nil {
			t.Fatalf("copy the broker's rows in, as %q does: %v", s.copyIn, err)
		}
	}
	listedRows, err := tx.Query(ctx, s.dispatchHalf)
	if err != nil {
		t.Fatalf("the sweep's Dispatch half: %v", err)
	}
	listed, err := pgx.CollectRows(listedRows, func(row pgx.CollectableRow) (listedAddress, error) {
		var l listedAddress
		return l, row.Scan(&l.person, &l.brokerAt, &l.dispatchAt)
	})
	if err != nil {
		t.Fatalf("the sweep's Dispatch half: %v", err)
	}
	return len(generations), false, listed
}

// psqlURL is the URL psql reaches pool's database by: the pool's own connection string, with a
// search_path parameter (the broker's harness gives each test a schema of its own) moved into the
// options libpq takes, since libpq refuses a parameter it does not know.
func psqlURL(t *testing.T, pool interface{ Config() *pgxpool.Config }) string {
	t.Helper()
	parsed, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatalf("parse the pool's connection string: %v", err)
	}
	query := parsed.Query()
	if schema := query.Get("search_path"); schema != "" {
		query.Del("search_path")
		query.Set("options", "-csearch_path="+schema)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// runWithPsql runs the sweep as the runbook writes it, through bash and psql, and answers its
// output and exit status. It skips the test when psql is not installed, since the block is a
// shell block and nothing but a shell shows what it does — the argument limit it exists to stay
// under among it.
func (s sweep) runWithPsql(t *testing.T, broker *brokerstore.Store, dispatch *store.Store) (string, int) {
	t.Helper()
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skipf("psql is not installed, so the sweep's own shell block cannot be run here: %v", err)
	}
	script := filepath.Join(t.TempDir(), "sweep.sh")
	// The operator's own shell: the block that makes the libpq URL, then the sweep that names it.
	text := strings.Join(s.urlStep, "\n") + "\n" + strings.Join(s.lines, "\n") + "\n"
	if err := os.WriteFile(script, []byte(text), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", script)
	// The broker's own URL can carry a pgx-only parameter a deployment sets to size its pool,
	// which libpq refuses: the block's first step takes it out, and without that step the sweep
	// reads nothing.
	brokerURL := psqlURL(t, broker.Pool)
	if strings.Contains(brokerURL, "?") {
		brokerURL += "&pool_max_conns=4"
	} else {
		brokerURL += "?pool_max_conns=4"
	}
	command.Env = append(os.Environ(),
		"DATABASE_URL="+psqlURL(t, dispatch.Pool),
		"BROKER_DATABASE_URL="+brokerURL)
	output, err := command.CombinedOutput()
	status := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		status = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run the sweep's block: %v (%s)", err, output)
	}
	return string(output), status
}

// TestTheSweepListsEveryAddressBelowTheBrokers runs the runbook's sweep, every part read from the
// runbook, on a broker database and a Dispatch database: with no row at the broker the guard
// reports UNKNOWN, so the sweep never reads as a clean result; seeded through the broker's own
// writer (brokerstore.RecordReplacedSubjectTx), it lists every address whose generation at
// Dispatch is below the broker's, one with no Dispatch row at 0 among them, and no address at or
// above the broker's generation, none with no broker row, and none whose floor
// (session_generation_floors, which the recovery writes for an address with no session row) is at
// or above the broker's.
//
// It then runs the block itself, through bash and psql, against the two databases, with the broker
// holding more rows than fit in one argument (above 128 KiB): the rows reach psql on its standard
// input, so the block answers rather than failing E2BIG.
//
// Controls: compare with <= in the runbook's Dispatch half, and the address at the broker's
// generation is listed; read the row alone, not the greater of the row and the floor, and the
// floored address is listed again.
func TestTheSweepListsEveryAddressBelowTheBrokers(t *testing.T) {
	ctx := context.Background()
	s := runbookSweep(t)
	broker := brokerstoretest.Open(t)
	dispatch := storetest.Open(t)
	if rows, unknown, _ := s.run(t, broker, dispatch); !unknown {
		t.Fatalf("on an empty broker table the broker half gave %d rows, which the guard does not report UNKNOWN for", rows)
	}

	people := store.NewPgPeopleStore(dispatch.Pool, testSigningKey, nil)
	sessions := store.NewPgSessionStore(dispatch.Pool)
	at := map[string]int64{}
	for _, signIns := range []struct {
		email    string
		subjects []string
	}{
		{"below@example.com", []string{"b1"}},
		{"level@example.com", []string{"l1"}},
		{"above@example.com", []string{"a1", "a2"}},
		{"dispatchonly@example.com", []string{"d1"}},
	} {
		for _, subject := range signIns.subjects {
			if err := people.SignInSubject(ctx, signIns.email, subject, "refresh-"+subject, model.MemberFull); err != nil {
				t.Fatalf("the sign-in of %s at %s: %v", subject, signIns.email, err)
			}
		}
		generation, found, err := sessions.CurrentSessionGeneration(ctx, signIns.email)
		if err != nil || !found {
			t.Fatalf("read %s's generation: %d, %t, %v", signIns.email, generation, found, err)
		}
		at[signIns.email] = generation
	}
	// floored@ has no session row and no people row: the recovery wrote its floor at the broker's
	// generation, which is what the sweep must read for it.
	if _, err := dispatch.Pool.Exec(ctx, `insert into session_generation_floors (login, generation) values ('floored@example.com', 4)`); err != nil {
		t.Fatalf("the floor of an address with no session row: %v", err)
	}
	for _, seed := range []struct {
		person     string
		generation int64
	}{
		{"below@example.com", at["below@example.com"] + 3},
		{"level@example.com", at["level@example.com"]},
		{"above@example.com", at["above@example.com"] - 1},
		{"unseen@example.com", 2},
		{"floored@example.com", 4},
	} {
		tx, err := broker.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := brokerstore.RecordReplacedSubjectTx(ctx, tx, seed.person, "https://accounts.google.com", "old", "new", seed.generation); err != nil {
			t.Fatalf("seed %s at %d: %v", seed.person, seed.generation, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rows, unknown, listed := s.run(t, broker, dispatch)
	if unknown {
		t.Fatalf("on a seeded broker table the sweep reported UNKNOWN for %d rows", rows)
	}
	want := []listedAddress{{"below@example.com", at["below@example.com"] + 3, at["below@example.com"]}, {"unseen@example.com", 2, 0}}
	if !slices.Equal(listed, want) {
		t.Errorf("the sweep listed %+v, want %+v: every address below the broker's, owed or not, and none at or above it, missing at the broker, or already floored", listed, want)
	}

	t.Run("through psql, with more rows than one argument holds", func(t *testing.T) {
		// 2,200 addresses of this length are above 128 KiB, the longest single argument the kernel
		// takes (MAX_ARG_STRLEN); the sweep sends them on psql's standard input instead.
		bulk, err := broker.Pool.Exec(ctx, `insert into person_replacement_generations (person, session_generation)
			select 'bulk-' || lpad(n::text, 6, '0') || '-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa@example.com', 9 from generate_series(1, 2200) as n`)
		if err != nil {
			t.Fatalf("seed the broker with more rows than one argument holds: %v", err)
		}
		output, status := s.runWithPsql(t, broker, dispatch)
		if status != 0 {
			t.Fatalf("the sweep's own block over %d rows exited %d: %s", bulk.RowsAffected(), status, output)
		}
		for _, want := range []string{"bulk-000001-", "bulk-002200-", "unseen@example.com"} {
			if !strings.Contains(output, want) {
				t.Errorf("the sweep's own block over %d broker rows did not list %s:\n%s", bulk.RowsAffected(), want, output)
			}
		}
		if strings.Contains(output, "level@example.com") || strings.Contains(output, "floored@example.com") {
			t.Errorf("the sweep's own block listed an address at or above the broker's generation:\n%s", output)
		}
	})
}

// TestTheSweepReportsUnknownWhenItReadNoGeneration runs the sweep's own shell block against a
// broker database with no recorded generation at all: the guard must say UNKNOWN and exit
// non-zero, since an empty read and a clean sweep are otherwise the same output.
//
// Control, in the test: the same block over a seeded broker table exits 0 and lists the address.
func TestTheSweepReportsUnknownWhenItReadNoGeneration(t *testing.T) {
	ctx := context.Background()
	s := runbookSweep(t)
	broker := brokerstoretest.Open(t)
	dispatch := storetest.Open(t)
	output, status := s.runWithPsql(t, broker, dispatch)
	if status == 0 || !strings.Contains(output, "UNKNOWN") {
		t.Errorf("over a broker database holding no recorded generation the sweep exited %d saying %q, want a non-zero exit saying UNKNOWN", status, output)
	}
	if _, err := broker.Pool.Exec(ctx, `insert into person_replacement_generations (person, session_generation) values ('seeded@example.com', 3)`); err != nil {
		t.Fatal(err)
	}
	output, status = s.runWithPsql(t, broker, dispatch)
	if status != 0 || !strings.Contains(output, "seeded@example.com") {
		t.Errorf("control: over a seeded broker table the sweep exited %d saying %q, want 0 and the address listed", status, output)
	}
}
