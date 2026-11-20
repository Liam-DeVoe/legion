// Command going-eval is the eval of the command bar's agent (R13 of dispatch://INFRA-1588/spec),
// over the tasks and the seeded Dispatch of going/eval. Its one subcommand today is seed, which
// the browser harness runs to serve a Dispatch on the eval's seed with the real models
// (DISPATCH_E2E_MODELS=real, packages/dispatch/e2e/harness-eval-seed.ts):
//
//	go run ./cmd/going-eval seed --database-url <url> [--envoy-fixture-out <file>]
//
// seed migrates the database the URL names and writes going/eval's seed.json into it
// (eval.WriteSeed), or writes nothing where the database already holds the seed. With
// --envoy-fixture-out it writes the seed's agent sessions to that file as JSON, the sessions a
// fake Envoy must answer for the seeded issues' claims, routes and role holders to name live,
// unresponsive or gone sessions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sjawhar/envoy/going/eval"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// usage is what the command prints for no subcommand or one it does not know.
const usage = "usage: going-eval seed --database-url <url> [--envoy-fixture-out <file>]"

// run answers the exit status of the subcommand args name: 0 done, 1 refused or failed, 2 a usage
// error. Every refusal goes to stderr.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "seed" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet("going-eval seed", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL := flags.String("database-url", "", "the Postgres URL of the Dispatch database to seed (required)")
	fixtureOut := flags.String("envoy-fixture-out", "", "a file to write the seed's agent sessions to, as JSON, for a fake Envoy")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() > 0 || strings.TrimSpace(*databaseURL) == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err := seed(ctx, *databaseURL, *fixtureOut, stdout); err != nil {
		fmt.Fprintf(stderr, "going-eval seed: %v\n", err)
		return 1
	}
	return 0
}

// seed migrates the database, writes the committed seed into it, and writes the seed's sessions
// to fixtureOut where it names a file.
func seed(ctx context.Context, databaseURL, fixtureOut string, stdout io.Writer) error {
	loaded, err := eval.LoadSeed(eval.Files)
	if err != nil {
		return err
	}
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open the database: %w", err)
	}
	defer database.Pool.Close()
	if err := database.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate the database: %w", err)
	}
	written, err := eval.WriteSeed(ctx, database, loaded)
	if err != nil {
		return err
	}
	if written {
		fmt.Fprintf(stdout, "going-eval seed: wrote %d projects, %d issues, %d documents and files, %d asks, %d comments and %d messages\n",
			len(loaded.Projects), len(loaded.Issues), len(loaded.Artifacts), len(loaded.Asks), len(loaded.Comments), len(loaded.Messages))
	} else {
		fmt.Fprintln(stdout, "going-eval seed: the database already holds the seed; nothing written")
	}
	if fixtureOut == "" {
		return nil
	}
	encoded, err := json.Marshal(loaded.Sessions)
	if err != nil {
		return err
	}
	if err := os.WriteFile(fixtureOut, encoded, 0o600); err != nil {
		return fmt.Errorf("write the Envoy fixture: %w", err)
	}
	return nil
}
