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

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/infra/seed"
	"github.com/sjawhar/envoy/infra/writerhold"
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
	// hub opens the operator's reach of the clustered hub for o (writerhold.Operator): its
	// Kubernetes objects and each declared server's monitoring port, which convert's meta barrier
	// reads (writerhold.MetaBarrier) and restore --finish restarts.
	hub func(ctx context.Context, o options) (*writerhold.Operator, error)
	// reread, when set, replaces the held re-read (heldReread): nil, as realEnv leaves it, is the
	// re-read. Only the grant rows, whose hubs of static users have no callout stack, set it.
	reread func(ctx context.Context, r *run) error
	now    func() time.Time
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
	operator  *writerhold.Operator
	// heldStreams are the protected streams the latest held re-read read, which a command's
	// postcheck compares against (writerhold.Postcheck's step 1).
	heldStreams []writerhold.StreamReading
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

// reconnectWithin bounds how long reconnect connects again, while the hub's auth service, itself
// reconnecting after a server's restart, may refuse the operator.
const reconnectWithin = time.Minute

// reconnect replaces the run's operator connection with one made now, for a held command after it
// restarted the hub's servers (writerhold's restart, proof and barrier): nats.go closes a
// connection whose reconnects the hub refused twice in a row, and a restart refuses every
// reconnect until the auth service is back. It connects every 250 ms until one connect is
// admitted, and is UNKNOWN once reconnectWithin passes. What the caller made on the old connection
// (a store over it, a bucket barrier) it makes again.
func (r *run) reconnect(ctx context.Context) (*nats.Conn, jetstream.JetStream, error) {
	if r.nc != nil {
		r.nc.Close()
	}
	r.nc, r.js = nil, nil
	deadline := time.Now().Add(reconnectWithin)
	for {
		nc, js, err := r.conn(ctx)
		if err == nil {
			return nc, js, nil
		}
		if time.Now().After(deadline) {
			return nil, nil, unknownf(err, "connect to the hub as the operator again, for %s", reconnectWithin)
		}
		select {
		case <-ctx.Done():
			return nil, nil, unknownf(ctx.Err(), "connect to the hub as the operator again")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// operatorHub is the run's reach of the clustered hub (env.hub), opened once.
func (r *run) operatorHub(ctx context.Context) (*writerhold.Operator, error) {
	if r.operator == nil {
		if r.e.hub == nil {
			return nil, errors.New("this run has no reach of the hub's Kubernetes objects")
		}
		op, err := r.e.hub(ctx, r.o)
		if err != nil {
			return nil, fmt.Errorf("the operator's reach of the hub: %w", err)
		}
		r.operator = op
	}
	return r.operator, nil
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
// phase it needs, so a deploy during a hold changes nothing. For held, it then reads the hold's
// journal and runs the held re-read (heldReread) before the command does anything else.
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
	if phase == natsgrant.OperatorPhaseHeld {
		if err := r.heldReread(ctx); err != nil {
			return row, err
		}
	}
	return row, nil
}

// The held re-read's writer census: read again while its answer is not whole, at most
// heldCensusReadings times, heldCensusPause apart (writerhold's hold set, wholeCensus).
const (
	heldCensusReadings = 10
	heldCensusPause    = 3 * time.Second
)

// heldReread is the rule every held command keeps when it starts and before its mutation steps
// (plan §3a, "Every held command"): it reads again step 1's server and stream reads, step 2's
// writer census and step 6's report, as writerhold's hold set reads them (hold.go Set), on the
// command's own connection:
//
//   - the render's precondition, which names the auth service's user (writerhold.Precondition),
//     and every declared server, its identity and its routes, leafnodes and gateways
//     (writerhold.Discover);
//   - the restart: every declared pod must be one hold set restarted in this hold, by the server
//     ids its done entry records (server_id.<pod>). A server's id is new at each start, so an id
//     other than the recorded one is a start since, still inside the hold; a pod hold set did not
//     restart is UNKNOWN;
//   - every protected stream by name, absent only where the hold's workflow declares it
//     (writerhold.ReadProtectedStreams), and the writer census over the ones present
//     (writerhold.TakeCensus), read again while it is not whole;
//   - the report: every connection of every declared server is the command's own or the auth
//     service's (writerhold.Report). Its restart check compares step 1's ids, which the journal
//     does not keep; the restart is the check above, so the report is given ids no server
//     answers, and checks the connections, the routes, the leafnodes and the gateways.
//
// A refusal or an UNKNOWN of any of them stops the command before it writes anything.
func (r *run) heldReread(ctx context.Context) error {
	if r.e.reread != nil {
		return r.e.reread(ctx, r)
	}
	d, _, err := r.records.Journal(ctx, r.o.holdID)
	if err != nil {
		return err
	}
	op, err := r.operatorHub(ctx)
	if err != nil {
		return err
	}
	nc, _, err := r.conn(ctx)
	if err != nil {
		return unknownf(err, "the operator's connection")
	}
	render, err := writerhold.Precondition(ctx, op.Hub)
	if err != nil {
		return heldError(err, "the hub's render")
	}
	declared, err := writerhold.Discover(ctx, op.Hub)
	if err != nil {
		return heldError(err, "the declared servers")
	}
	restarted := heldRestarted(d)
	report := make(writerhold.Declared, len(declared))
	for i, server := range declared {
		if _, ok := restarted[server.Pod]; !ok {
			return unknownf(nil, "declared server %s (%s) is not one hold set restarted in hold %s: its done entry records no server_id.%s",
				server.Pod, server.Varz.ServerID, r.o.holdID, server.Pod)
		}
		report[i] = server
		report[i].Varz.ServerID = "the process " + server.Pod + " ran before hold set's restart"
	}
	streams, err := writerhold.ReadProtectedStreams(ctx, nc, heldExpectedAbsent(d.Workflow))
	if err != nil {
		return heldError(err, "the protected streams")
	}
	r.heldStreams = streams
	var present []string
	for _, stream := range streams {
		if !stream.Absent {
			present = append(present, stream.Name)
		}
	}
	for reading := 1; ; reading++ {
		_, err := writerhold.TakeCensus(ctx, nc, present)
		var unknown *writerhold.Unknown
		if err == nil {
			break
		}
		if !errors.As(err, &unknown) || reading == heldCensusReadings {
			return heldError(err, "the writer census")
		}
		timer := time.NewTimer(heldCensusPause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return unknownf(ctx.Err(), "the writer census")
		case <-timer.C:
		}
	}
	if _, err := (writerhold.Report{Hub: op.Hub, Declared: report, AuthUser: render.AuthUser, Wait: op.Wait}).Run(ctx, nc); err != nil {
		return heldError(err, "the report")
	}
	return nil
}

// heldRestarted is each pod's server id after hold set's restart, from its latest done entry's
// outputs (server_id.<pod>).
func heldRestarted(d holdrecord.Done) map[string]string {
	ids := map[string]string{}
	for _, entry := range d.Ops {
		if entry.Command != holdrecord.CommandSet || entry.State != "done" {
			continue
		}
		ids = map[string]string{}
		for key, id := range entry.Outputs {
			if pod, ok := strings.CutPrefix(key, "server_id."); ok {
				ids[pod] = id
			}
		}
	}
	return ids
}

// heldExpectedAbsent are the protected streams w's commands may find absent, as hold set's
// preflight declares them (writerhold hold.go expectedAbsent): the selection bucket in the
// cutover, until create-bucket makes it, and in a selection recovery, until create-bucket
// --after-loss does; every protected stream in streams-update.
func heldExpectedAbsent(w holdrecord.Workflow) []string {
	switch w {
	case holdrecord.WorkflowCutover, holdrecord.WorkflowSelectionRecovery:
		return []string{"KV_" + selection.SessionBucket}
	case holdrecord.WorkflowStreamsUpdate:
		return writerhold.ProtectedStreams()
	}
	return nil
}

// heldError is a reading of writerhold's that failed: its UNKNOWN stays UNKNOWN here (exit 2),
// and its refusal a refusal, each naming what was read.
func heldError(err error, what string) error {
	var unknown *writerhold.Unknown
	if errors.As(err, &unknown) {
		return unknownf(err, "the held re-read, %s", what)
	}
	return fmt.Errorf("the held re-read, %s: %w", what, err)
}

// admission is a held command admitted to its hold: its intent entry is written, and it ends
// with its done entry or, when its own check refuses, its stop entry.
type admission struct {
	r        *run
	command  string
	workflow holdrecord.Workflow
}

// admit reads the row, which must be held under the run's hold id, runs the held re-read
// (requirePhase, heldReread), and writes command's intent entry with inputs
// (holdrecord.Client.Admit): the decision and the entry are one write.
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

// reread is the held re-read again (heldReread), which an admitted command runs before each of
// its mutation steps. Its refusal leaves the command's intent pending: the way out is the rerun,
// once whatever it found is gone.
func (a *admission) reread(ctx context.Context) error { return a.r.heldReread(ctx) }

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
		// The hub's Kubernetes flags come with writerhold's HubFlags, which this command takes once
		// that export merges; until then a command that needs the hub refuses, naming it.
		hub: func(context.Context, options) (*writerhold.Operator, error) {
			return nil, errors.New("this build takes no flags for the hub's Kubernetes objects yet (writerhold.HubFlags)")
		},
		now: time.Now,
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
