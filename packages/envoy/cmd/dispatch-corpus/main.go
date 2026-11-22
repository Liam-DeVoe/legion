// Command dispatch-corpus writes the seeded corpus every test lane of Dispatch's graph model
// shares into the database DATABASE_URL names, and reads it back: the one entry point the shell
// drivers and the browser file run, beside the Go tests that call internal/dispatch/seedcorpus
// directly. It is test support, and no deployment runs it.
//
// Usage:
//
//	DATABASE_URL=<url> go run ./cmd/dispatch-corpus seed [--project CORE] [--fixtures <dir>] [--test-copy] [--prevented-by-out <file>]
//	DATABASE_URL=<url> go run ./cmd/dispatch-corpus check [--project CORE] [--fixtures <dir>]
//	DATABASE_URL=<url> go run ./cmd/dispatch-corpus end-claim --role <role>
//	DATABASE_URL=<url> go run ./cmd/dispatch-corpus restore-claim --role <role> --session <session>
//
// seed migrates the database, writes the corpus, prints each name the drivers read as one
// `key=value` line, and then runs the self-check over what it wrote. It refuses a database that
// already holds events, naming how many, unless --test-copy says the database is a test copy, such
// as the one `bun run dispatch:local` seeds; it publishes no event and marks none published. With
// --prevented-by-out it also writes, to that file, the map a second conversion of the captured
// pre-mortem reads (`envoy-dispatch premortem-convert --prevented-by`, seedcorpus.Corpus.PreventedBy),
// which names checks of the issue it seeded and so exists only once the seed has run. check runs
// the self-check alone, over a database a seed wrote. Each prints the self-check's lines
// (seedcorpus.Check) and exits 1 when a seed is refused or one bullet or row is missing or holds a
// count other than the one written, and 2 on a refused argument.
//
// end-claim ends the claim the store holds on a role, and restore-claim puts a session's claim on
// it back, each through the store's writer (seedcorpus.EndClaim, seedcorpus.RestoreClaim), printing
// one line for each held_by it retracted or asserted: the outbox states a role's claim in a
// deployment, and `bun run dispatch:local` runs no bus and so no outbox, so agent-page.sh ends the
// seeded role's claim and puts it back with these, and writes the store no other way. Each exits 1
// on a refused write and 2 on a refused argument. The claim commands take --project and --fixtures
// too, which they read nothing of, so a driver runs every command with the same flags.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc/schemanodes"
	"github.com/sjawhar/envoy/internal/dispatch/seedcorpus"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// commands are the subcommands dispatch-corpus takes.
var commands = []string{"seed", "check", "end-claim", "restore-claim"}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The handlers the loader calls log a line for each request at info, which would bury the
	// corpus's own lines; a warning or an error still reaches stderr.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	os.Exit(run(ctx, os.Args[1:], os.Getenv("DATABASE_URL"), os.Stdout, os.Stderr))
}

// run parses the subcommand and its flags, opens the database and runs it. Every refusal is
// written to stderr with a non-zero status, so a driver reads the status and a person the reason.
func run(ctx context.Context, args []string, databaseURL string, stdout, stderr io.Writer) int {
	if len(args) == 0 || !slices.Contains(commands, args[0]) {
		fmt.Fprintln(stderr, "dispatch-corpus: the first argument is seed, check, end-claim or restore-claim")
		return 2
	}
	command := args[0]
	flags := flag.NewFlagSet("dispatch-corpus "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", seedcorpus.DefaultProject, "the key of the project the corpus is written into")
	fixtures := flags.String("fixtures", "", "the directory holding the three captured fixtures; empty finds packages/dispatch/e2e/fixtures in the working directory or above it")
	testCopy := false
	preventedByOut := ""
	role, session := "", ""
	switch command {
	case "seed":
		flags.BoolVar(&testCopy, "test-copy", false, "the database is a test copy: seed it though it holds events the loader did not write")
		flags.StringVar(&preventedByOut, "prevented-by-out", "", "a file to write the map of a second pre-mortem conversion to, from the names the seed wrote")
	case "end-claim":
		flags.StringVar(&role, "role", "", "the role whose claim is ended")
	case "restore-claim":
		flags.StringVar(&role, "role", "", "the role whose claim is put back")
		flags.StringVar(&session, "session", "", "the session whose claim on the role is put back")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "dispatch-corpus: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if (command == "end-claim" || command == "restore-claim") && role == "" {
		fmt.Fprintf(stderr, "dispatch-corpus %s: --role is required\n", command)
		return 2
	}
	if command == "restore-claim" && session == "" {
		fmt.Fprintln(stderr, "dispatch-corpus restore-claim: --session is required")
		return 2
	}
	if strings.TrimSpace(databaseURL) == "" {
		fmt.Fprintln(stderr, "dispatch-corpus: DATABASE_URL is required")
		return 2
	}
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(stderr, "dispatch-corpus: open the database: %v\n", err)
		return 1
	}
	defer database.Pool.Close()
	switch command {
	case "end-claim":
		ended, err := seedcorpus.EndClaim(ctx, database.Pool, role)
		if err != nil {
			fmt.Fprintf(stderr, "dispatch-corpus end-claim: %v\n", err)
			return 1
		}
		for _, holder := range ended {
			fmt.Fprintf(stdout, "end-claim: retracted held_by role:%s -> session:%s\n", role, holder)
		}
		return 0
	case "restore-claim":
		if err := seedcorpus.RestoreClaim(ctx, database.Pool, role, session); err != nil {
			fmt.Fprintf(stderr, "dispatch-corpus restore-claim: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "restore-claim: asserted held_by role:%s -> session:%s\n", role, session)
		return 0
	}
	if command == "seed" {
		if err := database.Migrate(ctx); err != nil {
			fmt.Fprintf(stderr, "dispatch-corpus: migrate the database: %v\n", err)
			return 1
		}
	}
	// The block schema the store holds is the one every document is read under, as the server's
	// own boot and its database subcommands install it before they parse one.
	if err := schemanodes.InstallFromStore(ctx, database.Pool); err != nil {
		fmt.Fprintf(stderr, "dispatch-corpus: install the block schema from the store: %v\n", err)
		return 1
	}
	if *fixtures == "" {
		if *fixtures, err = seedcorpus.FindFixtures("."); err != nil {
			fmt.Fprintf(stderr, "dispatch-corpus: %v\n", err)
			return 2
		}
	}
	if command == "seed" {
		started := time.Now()
		corpus, err := seedcorpus.Seed(ctx, database, seedcorpus.Options{Project: *project, Fixtures: *fixtures, TestCopy: testCopy})
		if err != nil {
			fmt.Fprintf(stderr, "dispatch-corpus: seed: %v\n", err)
			return 1
		}
		corpus.Print(stdout)
		fmt.Fprintf(stdout, "seeded in %s\n", time.Since(started).Round(time.Millisecond))
		if preventedByOut != "" {
			preventedBy, err := corpus.PreventedBy()
			if err == nil {
				err = os.WriteFile(preventedByOut, []byte(preventedBy), 0o644)
			}
			if err != nil {
				fmt.Fprintf(stderr, "dispatch-corpus: write the map of a second pre-mortem conversion to %s: %v\n", preventedByOut, err)
				return 1
			}
		}
	}
	report, err := seedcorpus.Check(ctx, database.Pool, seedcorpus.CheckOptions{Project: *project, Fixtures: *fixtures})
	report.Print(stdout)
	if err != nil {
		var failed *seedcorpus.CheckFailed
		if errors.As(err, &failed) {
			fmt.Fprintf(stderr, "dispatch-corpus: check failed: %v\n", err)
		} else {
			fmt.Fprintf(stderr, "dispatch-corpus: check: %v\n", err)
		}
		return 1
	}
	return 0
}
