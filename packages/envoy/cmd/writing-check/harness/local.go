package harness

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// LocalInstance is local.json: where the local Dispatch that `harness local` started listens, and
// its database. `harness load` and later runs read it.
type LocalInstance struct {
	BaseURL     string `json:"base_url"`
	FakeEnvoy   string `json:"fake_envoy_url,omitempty"`
	Database    string `json:"database"`
	DatabaseURL string `json:"database_url"`
	// AgentToken is the bearer the local instance admits for every agent (e2e/run-server.sh).
	AgentToken string `json:"agent_token"`
	// Settings are the DISPATCH_* settings passed through to the server, by name.
	Settings []string `json:"settings"`
	Started  string   `json:"started"`
}

// localAgentToken is the agent bearer e2e/run-server.sh pins.
const localAgentToken = "e2e-token"

// The lines `bun run dispatch:local` prints once the instance is up.
var (
	localReadyLine  = "Press Ctrl-C to stop."
	localSignInLine = regexp.MustCompile(`Open \(signed in as alice\): (http://127\.0\.0\.1:\d+)/auth/_dev/signin`)
	localEnvoyLine  = regexp.MustCompile(`Fake Envoy: live sessions GET (http://127\.0\.0\.1:\d+)/v1/sessions`)
	settingName     = regexp.MustCompile(`^DISPATCH_[A-Z0-9_]+$`)
)

// repoRoot is the nearest directory at or above dir that holds packages/dispatch/e2e/local-instance.ts.
func repoRoot(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "packages", "dispatch", "e2e", "local-instance.ts")); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("no directory at or above %s holds packages/dispatch/e2e/local-instance.ts: give --repo", dir)
		}
	}
}

// databaseURL is dsn with its database replaced by name.
func databaseURL(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("the Postgres DSN is not a postgres:// URL")
	}
	u.Path = "/" + name
	return u.String(), nil
}

// createDatabase runs createdb for a database name on dsn's server.
func createDatabase(ctx context.Context, dsn, name string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return err
	}
	args := []string{"-h", u.Hostname(), "-U", u.User.Username()}
	if port := u.Port(); port != "" {
		args = append(args, "-p", port)
	}
	cmd := exec.CommandContext(ctx, "createdb", append(args, name)...)
	cmd.Env = os.Environ()
	if pw, ok := u.User.Password(); ok {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+pw)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("createdb %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// settingsFlag is --setting NAME=VALUE, given once for each setting.
type settingsFlag []string

func (s *settingsFlag) String() string { return strings.Join(*s, ",") }

func (s *settingsFlag) Set(v string) error {
	name, _, ok := strings.Cut(v, "=")
	if !ok || !settingName.MatchString(name) || strings.HasPrefix(name, "DISPATCH_E2E_") {
		return fmt.Errorf("%q is not NAME=VALUE with a DISPATCH_ name that is not a DISPATCH_E2E_ switch", v)
	}
	*s = append(*s, v)
	return nil
}

func runLocal(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("writing-check harness local", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "the directory for local.json and the instance's log (required; keep it under $TMPDIR)")
	dsn := fs.String("pg-dsn", os.Getenv("LEGION_TEST_PG_DSN"), "the Postgres server the instance's database is made on (default $LEGION_TEST_PG_DSN)")
	database := fs.String("database", "", "a database made before on that server, to start on again; empty makes a new one, dispatch_harness_<unix time>")
	keep := fs.Bool("keep", false, "keep the database as it is: no reset and no seed, for a database whose cases are loaded (needs --database)")
	passThrough := fs.String("pass-through", "", "the DISPATCH_* settings of this environment the server gets, comma-separated")
	var settings settingsFlag
	fs.Var(&settings, "setting", "a DISPATCH_* setting the server gets, NAME=VALUE (repeatable)")
	repo := fs.String("repo", "", "the repository root (default: the nearest above the working directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "writing-check harness local: %s\n", msg)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "writing-check harness local: %v\n", err)
		return 1
	}
	switch {
	case *out == "" || *dsn == "" || fs.NArg() > 0:
		return usage("--out and a Postgres DSN (--pg-dsn or $LEGION_TEST_PG_DSN) are required, and nothing else")
	case *keep && *database == "":
		return usage("--keep starts again on a database made before: give --database")
	}
	if err := outsideRepository("--out", *out); err != nil {
		return usage(err.Error())
	}
	env := map[string]string{}
	for _, kv := range settings {
		name, value, _ := strings.Cut(kv, "=")
		env[name] = value
	}
	for _, name := range strings.Split(*passThrough, ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if !settingName.MatchString(name) || strings.HasPrefix(name, "DISPATCH_E2E_") {
			return usage(fmt.Sprintf("--pass-through %q is not a DISPATCH_ setting", name))
		}
		value, ok := os.LookupEnv(name)
		if !ok {
			return usage(fmt.Sprintf("--pass-through names %s, which this environment does not set", name))
		}
		env[name] = value
	}
	root := *repo
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fail(err)
		}
		if root, err = repoRoot(wd); err != nil {
			return usage(err.Error())
		}
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return fail(err)
	}
	name := *database
	if name == "" {
		name = fmt.Sprintf("dispatch_harness_%d", time.Now().Unix())
		if err := createDatabase(ctx, *dsn, name); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stderr, "writing-check harness local: made the database %s\n", name)
	}
	dbURL, err := databaseURL(*dsn, name)
	if err != nil {
		return usage(err.Error())
	}
	inst := LocalInstance{Database: name, DatabaseURL: dbURL, AgentToken: localAgentToken, Settings: slices.Sorted(maps.Keys(env)), Started: time.Now().UTC().Format(time.RFC3339)}

	cmd := exec.CommandContext(ctx, "bun", "run", "dispatch:local")
	cmd.Dir = root
	cmd.Env = localEnv(os.Environ(), dbURL, env, *keep)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	logFile, err := os.OpenFile(filepath.Join(*out, "local.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fail(err)
	}
	defer logFile.Close()
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	up := false
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(logFile, line)
		if m := localSignInLine.FindStringSubmatch(line); m != nil {
			inst.BaseURL = m[1]
		}
		if m := localEnvoyLine.FindStringSubmatch(line); m != nil {
			inst.FakeEnvoy = m[1]
		}
		// The instance prints its addresses after the line that says it is up, and this line
		// last.
		if !up && inst.BaseURL != "" && strings.Contains(line, localReadyLine) {
			up = true
			if err := writeJSON(filepath.Join(*out, "local.json"), inst); err != nil {
				return fail(err)
			}
			fmt.Fprintf(stdout, "local Dispatch at %s on database %s; %s holds it. Stop it with Ctrl-C or SIGTERM.\n", inst.BaseURL, name, filepath.Join(*out, "local.json"))
		}
	}
	err = cmd.Wait()
	os.Remove(filepath.Join(*out, "local.json"))
	if ctx.Err() != nil {
		return 0
	}
	if err != nil {
		return fail(fmt.Errorf("bun run dispatch:local: %w (its output is in %s)", err, filepath.Join(*out, "local.log")))
	}
	if !up {
		return fail(fmt.Errorf("bun run dispatch:local stopped before the instance was up (its output is in %s)", filepath.Join(*out, "local.log")))
	}
	return 0
}

// localEnv is the environment `bun run dispatch:local` gets: this process's, with the database,
// each setting, and the list of the settings e2e/run-server.sh passes through to the server
// (DISPATCH_E2E_PASS_THROUGH). With keep, the instance starts on the database as it is
// (DISPATCH_E2E_KEEP_DATABASE=1).
func localEnv(base []string, dbURL string, settings map[string]string, keep bool) []string {
	drop := []string{"DATABASE_URL", "DISPATCH_E2E_PASS_THROUGH", "DISPATCH_E2E_KEEP_DATABASE", "PLAYWRIGHT_BASE_URL", "DISPATCH_E2E_PROCESSES"}
	var env []string
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(drop, name) {
			continue
		}
		if _, set := settings[name]; set {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "DATABASE_URL="+dbURL)
	var names []string
	for name, value := range settings {
		env = append(env, name+"="+value)
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) > 0 {
		env = append(env, "DISPATCH_E2E_PASS_THROUGH="+strings.Join(names, " "))
	}
	if keep {
		env = append(env, "DISPATCH_E2E_KEEP_DATABASE=1")
	}
	return env
}
