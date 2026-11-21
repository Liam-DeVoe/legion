package consumernames

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/infra/seed"
	"github.com/sjawhar/envoy/internal/bus/signedrecord"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natscred"
	"github.com/sjawhar/envoy/natsgrant"
)

// Database is what a command runs its statements on in a family's Postgres store: a
// *pgxpool.Pool, or a test's.
type Database interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// env is what a run reaches outside the process. Command gives the real one (realEnv); a test
// gives its own.
type env struct {
	// records opens the operator's client of the broker's hold routes for o, with the signing key
	// when signing.
	records func(ctx context.Context, o options, signing bool) (*holdrecord.Client, error)
	// connect opens the operator's NATS connection to o's hub, its replies under
	// natsgrant.OperatorInbox.
	connect func(ctx context.Context, o options) (*nats.Conn, error)
	// database opens the Postgres store of family: "dispatch", or "daemon/<project key>".
	database func(ctx context.Context, o options, family string) (Database, error)
	now      func() time.Time
}

// run is one command's reach: its options, its env, and what it opened, each opened once, when
// first needed, and closed when the command ends.
type run struct {
	o              options
	e              env
	stdout, stderr io.Writer

	records   *holdrecord.Client
	nc        *nats.Conn
	js        jetstream.JetStream
	databases map[string]Database
	closers   []func()
}

func (r *run) close() {
	for i := len(r.closers) - 1; i >= 0; i-- {
		r.closers[i]()
	}
}

func (r *run) now() time.Time {
	if r.e.now != nil {
		return r.e.now()
	}
	return time.Now()
}

// hold is the run's client of the broker's hold routes, opened with the signing key for every
// command but status.
func (r *run) hold(ctx context.Context) (*holdrecord.Client, error) {
	if r.records == nil {
		records, err := r.e.records(ctx, r.o, r.o.action != actionStatus)
		if err != nil {
			return nil, err
		}
		r.records = records
	}
	return r.records, nil
}

// conn is the run's operator connection to the hub, and its JetStream.
func (r *run) conn(ctx context.Context) (*nats.Conn, jetstream.JetStream, error) {
	if r.nc == nil {
		nc, err := r.e.connect(ctx, r.o)
		if err != nil {
			return nil, nil, fmt.Errorf("connect to the hub as the operator: %w", err)
		}
		js, err := jetstream.New(nc)
		if err != nil {
			nc.Close()
			return nil, nil, err
		}
		r.nc, r.js = nc, js
		r.closers = append(r.closers, nc.Close)
	}
	return r.nc, r.js, nil
}

// database is family's Postgres store, opened once.
func (r *run) database(ctx context.Context, family string) (Database, error) {
	if db, ok := r.databases[family]; ok {
		return db, nil
	}
	db, err := r.e.database(ctx, r.o, family)
	if err != nil {
		return nil, err
	}
	if r.databases == nil {
		r.databases = map[string]Database{}
	}
	r.databases[family] = db
	return db, nil
}

// requirePhase reads the hold row and refuses, naming the phase it found, unless it is phase,
// and, for prepare and held, under the run's hold id: each operator command runs only in the one
// phase it needs, so a deploy during a hold changes nothing.
func (r *run) requirePhase(ctx context.Context, phase natsgrant.OperatorPhase) (holdrecord.Row, error) {
	records, err := r.hold(ctx)
	if err != nil {
		return holdrecord.Row{}, err
	}
	row, err := records.Row(ctx)
	if err != nil {
		return holdrecord.Row{}, err
	}
	if row.Phase != phase {
		return row, fmt.Errorf("%s runs while the hold row is %s, and it is %s (hold %q)", r.o.action, phase, row.Phase, row.HoldID)
	}
	if phase != natsgrant.OperatorPhaseReleased && row.HoldID != r.o.holdID {
		return row, fmt.Errorf("the hold row is %s under hold %q, not under --hold %q", row.Phase, row.HoldID, r.o.holdID)
	}
	return row, nil
}

// admission is a held command admitted to its hold: its intent entry is written, and it ends
// with its done entry or, when its own check refuses, its stop entry.
type admission struct {
	r        *run
	command  string
	workflow holdrecord.Workflow
}

// admit reads the row, which must be held under the run's hold id, and writes command's intent
// entry with inputs (holdrecord.Client.Admit): the decision and the entry are one write.
func (r *run) admit(ctx context.Context, command string, inputs map[string]string) (*admission, error) {
	if _, err := r.requirePhase(ctx, natsgrant.OperatorPhaseHeld); err != nil {
		return nil, err
	}
	w, err := r.records.Admit(ctx, r.o.holdID, holdrecord.Request{Command: command}, inputs)
	if err != nil {
		return nil, err
	}
	return &admission{r: r, command: command, workflow: w}, nil
}

// done writes the command's done entry with outputs (holdrecord.Client.AppendDone).
func (a *admission) done(ctx context.Context, outputs map[string]string) error {
	return a.r.records.AppendDone(ctx, a.r.o.holdID, a.command, outputs)
}

// stop writes the command's stop entry with reason, when the command is Stoppable in its
// workflow, and answers reason as the command's refusal; where it is not Stoppable, the intent
// stays pending, so only its rerun is admitted (holdrecord.ErrBusy for any other).
func (a *admission) stop(ctx context.Context, reason string) error {
	refusal := errors.New(reason)
	if !holdrecord.Stoppable(a.workflow, a.command) {
		return refusal
	}
	if err := a.r.records.AppendStop(ctx, a.r.o.holdID, a.command, reason); err != nil {
		return fmt.Errorf("%s; and its stop entry: %w", reason, err)
	}
	return refusal
}

// realEnv is the env of a real run: the broker's routes with the operator's token and the hold
// key from Secret Manager, the hub as the operator, and each family's database from the file its
// flag names.
func realEnv() env {
	return env{
		records: func(ctx context.Context, o options, signing bool) (*holdrecord.Client, error) {
			return openRecords(ctx, o, signing)
		},
		connect: func(_ context.Context, o options) (*nats.Conn, error) {
			credential, _ := natscred.Service(natscred.ServiceFile(o.tokenFile))
			return nats.Connect(strings.Join(o.nats, ","), credential, nats.CustomInboxPrefix(natsgrant.OperatorInbox),
				nats.Name("envoy-infra consumer-names"), nats.Timeout(10*time.Second), nats.MaxReconnects(-1))
		},
		database: openDatabase,
		now:      time.Now,
	}
}

// openDatabase opens family's database from the file its flag names, which holds the database's
// URL with the operator's credential, closed when the run ends.
func openDatabase(ctx context.Context, o options, family string) (Database, error) {
	file, flagName := o.dispatchDatabaseFile, "--dispatch-database-file"
	if project, ok := strings.CutPrefix(family, familyDaemonPrefix); ok {
		var named bool
		file, named = o.daemonDatabase(project)
		flagName = "--daemon-database " + project + "=<file>"
		if !named {
			file = ""
		}
	}
	if file == "" {
		return nil, fmt.Errorf("%s's store needs %s", family, flagName)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", flagName, err)
	}
	pool, err := pgxpool.New(ctx, strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("open %s's database (%s; the URL is not echoed): %w", family, flagName, err)
	}
	return pool, nil
}

// openRecords opens the operator's client of o's broker, as `envoy-infra hold` does: the token
// read from o.tokenFile at each request, the verify keys parsed from o.verifyKeys, and, when
// signing, the private half from Secret Manager.
func openRecords(ctx context.Context, o options, signing bool) (*holdrecord.Client, error) {
	verify, err := signedrecord.ParsePublicKeys("--verify-keys", o.verifyKeys)
	if err != nil {
		return nil, err
	}
	cfg := holdrecord.Config{
		BrokerURL:  o.brokerURL,
		Token:      tokenFromFile(o.tokenFile),
		VerifyKeys: verify,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
	}
	if signing {
		store, err := seed.OpenGCP(ctx, "", "")
		if err != nil {
			return nil, err
		}
		_, value, err := store.Current(ctx, o.keySecret)
		if err != nil {
			return nil, fmt.Errorf("read the signing key from %s: %w", o.keySecret, err)
		}
		defer clear(value)
		seedBytes, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimSpace(string(value)))
		if err != nil || len(seedBytes) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s's served version is not the unpadded base64url of a %d-byte Ed25519 seed (not echoed)", o.keySecret, ed25519.SeedSize)
		}
		defer clear(seedBytes)
		cfg.SigningKey = ed25519.NewKeyFromSeed(seedBytes)
	}
	return holdrecord.New(cfg)
}

// tokenFromFile reads the token at path at each call: the kubelet rewrites a projected token in
// place.
func tokenFromFile(path string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read the token file %s: %w", path, err)
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("the token file %s is empty", path)
		}
		return token, nil
	}
}
