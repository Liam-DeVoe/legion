package consumernames

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/infra/writerhold"
	"github.com/sjawhar/envoy/internal/holdrecord"
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
	// database opens the Postgres store of family: "dispatch", or "daemon/<project key>", and
	// answers with it the close of what the open made, which the run calls when it ends: realEnv's
	// pool (openDatabase). nil is a store the caller owns and closes, as a test's.
	database func(ctx context.Context, o options, family string) (Database, func(), error)
	// hub opens the operator's reach of the clustered hub for o (writerhold.Operator): its
	// Kubernetes objects and each declared server's monitoring port, which the held re-read
	// (heldReread), convert's meta barrier (writerhold.MetaBarrier) and restore --finish's restart
	// read.
	hub func(ctx context.Context, o options) (*writerhold.Operator, error)
	// reread, when set, replaces the held re-read (heldReread): nil, as realEnv leaves it, is the
	// re-read. Only the grant rows, whose hubs of static users have no callout stack, set it.
	reread func(ctx context.Context, r *run) error
	now    func() time.Time
}

// run is one command's reach: its options, its env, and what it opened, each opened once, when
// first needed, and closed when the command ends (close).
type run struct {
	o              options
	e              env
	stdout, stderr io.Writer

	records  *holdrecord.Client
	nc       *nats.Conn
	js       jetstream.JetStream
	operator *writerhold.Operator
	// heldStreams are the protected streams the latest held re-read read, which a command's
	// postcheck compares against (writerhold.Postcheck's step 1).
	heldStreams []writerhold.StreamReading
	databases   map[string]Database
	// closeDatabases are the closes env.database answered for the stores it opened.
	closeDatabases []func()
}

// close closes the run's operator connection, the latest one reconnect made, and every store it
// opened (env.database).
func (r *run) close() {
	for _, closeDatabase := range r.closeDatabases {
		closeDatabase()
	}
	if r.nc != nil {
		r.nc.Close()
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

// readHoldRecord reads hold holdID's record of kind and key (holdrecord.Client.Records, which
// verifies each record) into a T, with its gen, which a write of it is conditional on, and false
// when the hold holds none. A value that does not decode is UNKNOWN, naming what.
func readHoldRecord[T any](ctx context.Context, records *holdrecord.Client, holdID, kind, key, what string) (T, uint64, bool, error) {
	var zero T
	list, err := records.Records(ctx, holdID, kind)
	if err != nil {
		return zero, 0, false, err
	}
	for _, record := range list {
		if record.Key != key {
			continue
		}
		var value T
		if err := json.Unmarshal(record.Value, &value); err != nil {
			return zero, 0, false, unknownf(err, "%s", what)
		}
		return value, record.Gen, true, nil
	}
	return zero, 0, false, nil
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
		if sleep(ctx, 250*time.Millisecond) != nil {
			return nil, nil, unknownf(ctx.Err(), "connect to the hub as the operator again")
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

// database is family's Postgres store, opened once, and closed when the run ends (close).
func (r *run) database(ctx context.Context, family string) (Database, error) {
	if db, ok := r.databases[family]; ok {
		return db, nil
	}
	db, closeDatabase, err := r.e.database(ctx, r.o, family)
	if err != nil {
		return nil, err
	}
	if closeDatabase != nil {
		r.closeDatabases = append(r.closeDatabases, closeDatabase)
	}
	if r.databases == nil {
		r.databases = map[string]Database{}
	}
	r.databases[family] = db
	return db, nil
}

// requireRow reads the hold row and refuses, naming the phase it found, unless it is phase, and,
// for prepare and held, under the run's hold id: each operator command runs only in the one phase
// it needs, so a deploy during a hold changes nothing. A held command that ran the held re-read
// when it started and has only read since checks the row with it alone before its intent
// (admitRead).
func (r *run) requireRow(ctx context.Context, phase natsgrant.OperatorPhase) (holdrecord.Row, error) {
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

// requirePhase is requireRow, and for held then the held re-read (heldReread), which reads the
// hold's journal, before the command does anything else.
func (r *run) requirePhase(ctx context.Context, phase natsgrant.OperatorPhase) (holdrecord.Row, error) {
	row, err := r.requireRow(ctx, phase)
	if err != nil || phase != natsgrant.OperatorPhaseHeld {
		return row, err
	}
	return row, r.heldReread(ctx)
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
		return hubError(err, "the held re-read, the hub's render")
	}
	declared, err := writerhold.Discover(ctx, op.Hub)
	if err != nil {
		return hubError(err, "the held re-read, the declared servers")
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
		return hubError(err, "the held re-read, the protected streams")
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
			return hubError(err, "the held re-read, the writer census")
		}
		if sleep(ctx, heldCensusPause) != nil {
			return unknownf(ctx.Err(), "the writer census")
		}
	}
	if _, err := (writerhold.Report{Hub: op.Hub, Declared: report, AuthUser: render.AuthUser, Wait: op.Wait}).Run(ctx, nc); err != nil {
		return hubError(err, "the held re-read, the report")
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
		return []string{selectionStreamName}
	case holdrecord.WorkflowStreamsUpdate:
		return writerhold.ProtectedStreams()
	}
	return nil
}

// hubError is err, a writerhold reading or step that failed, as this package answers it: its
// UNKNOWN (writerhold.Unknown) stays UNKNOWN here (exit 2), and its refusal a refusal, each naming
// what.
func hubError(err error, what string) error {
	if errors.As(err, new(*writerhold.Unknown)) {
		return unknownf(err, "%s", what)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// hubUnknown is hubError with a refusal answered as it is: a caller that names its refusal
// itself, or answers it to its operator in the hub's own words (restore, behindBarrier).
func hubUnknown(err error, what string) error {
	if errors.As(err, new(*writerhold.Unknown)) {
		return unknownf(err, "%s", what)
	}
	return err
}

// sleep waits d, and answers ctx's error when ctx ends first.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// admission is a held command admitted to its hold: its intent entry is written, and it ends
// with its done entry or, when its own check refuses, its stop entry.
type admission struct {
	r        *run
	command  string
	workflow holdrecord.Workflow
}

// admit reads the row, which must be held under the run's hold id, runs the held re-read
// (requirePhase, heldReread), and writes command's intent entry (intent).
func (r *run) admit(ctx context.Context, command string, inputs map[string]string) (*admission, error) {
	if _, err := r.requirePhase(ctx, natsgrant.OperatorPhaseHeld); err != nil {
		return nil, err
	}
	return r.intent(ctx, command, inputs)
}

// admitRead is admit for a command that ran the held re-read when it started (requirePhase) and
// has only read since what can refuse it before its intent (create-bucket, replay,
// repair-sentinel): it reads the row again, which must still be held under the run's hold id
// (requireRow), and writes the intent with no second re-read, which would read again what the
// first read with nothing changed between. The re-read still comes before the command's first
// change, the intent.
func (r *run) admitRead(ctx context.Context, command string, inputs map[string]string) (*admission, error) {
	if _, err := r.requireRow(ctx, natsgrant.OperatorPhaseHeld); err != nil {
		return nil, err
	}
	return r.intent(ctx, command, inputs)
}

// intent writes command's intent entry with inputs (holdrecord.Client.Admit): the decision and
// the entry are one write.
func (r *run) intent(ctx context.Context, command string, inputs map[string]string) (*admission, error) {
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

// stop writes the command's stop entry with reason and left, what every run of the command in this
// hold changed outside the journal: holdrecord.LeftUnchanged when none changed anything, which is
// the only stop hold release is admitted over; anything else counts as changed, and the way out is
// the command's rerun or hold abort (holdrecord.Client.AppendStopLeft). It writes the entry when
// the command is Stoppable in its workflow, and answers reason as the command's refusal; where it
// is not Stoppable, the intent stays pending, so only its rerun is admitted (holdrecord.ErrBusy for
// any other).
func (a *admission) stop(ctx context.Context, reason, left string) error {
	refusal := errors.New(reason)
	if !holdrecord.Stoppable(a.workflow, a.command) {
		return refusal
	}
	if err := a.r.records.AppendStopLeft(ctx, a.r.o.holdID, a.command, reason, left); err != nil {
		return fmt.Errorf("%s; and its stop entry: %w", reason, err)
	}
	return refusal
}

// realEnv is the env of a real run: the broker's routes with the operator's token and the hold
// key from Secret Manager, the hub as the operator, and each family's database from the file its
// flag names, all from the hold's flags as writerhold reads them (writerhold.HubFlags).
func realEnv() env { return envFromFlags(realHubOpeners) }

// hubOpeners are how envFromFlags reaches the broker's hold routes, the hub's NATS and the hub's
// Kubernetes objects from a run's writerhold.HubFlags.
type hubOpeners struct {
	records func(h writerhold.HubFlags, ctx context.Context, signing bool) (*holdrecord.Client, error)
	connect func(h writerhold.HubFlags) (*nats.Conn, error)
	hub     func(h writerhold.HubFlags, ctx context.Context) (*writerhold.Operator, error)
}

// realHubOpeners are writerhold's own: HubFlags.Records (Secret Manager's signing key when
// signing), ConnectOperator and HubFlags.Operator (the in-cluster configuration or KUBECONFIG's, and
// the Compute API for the hub's disks).
var realHubOpeners = hubOpeners{
	records: writerhold.HubFlags.Records,
	connect: func(h writerhold.HubFlags) (*nats.Conn, error) {
		return writerhold.ConnectOperator(h.NATS, h.TokenFile, "envoy-infra consumer-names")
	},
	hub: writerhold.HubFlags.Operator,
}

// envFromFlags is the env that reaches everything a run's flags name through open, and each
// family's database from the file its flag names (openDatabase), closed when the run ends.
func envFromFlags(open hubOpeners) env {
	return env{
		records: func(ctx context.Context, o options, signing bool) (*holdrecord.Client, error) {
			return open.records(o.HubFlags, ctx, signing)
		},
		connect: func(_ context.Context, o options) (*nats.Conn, error) { return open.connect(o.HubFlags) },
		database: func(ctx context.Context, o options, family string) (Database, func(), error) {
			pool, err := openDatabase(ctx, o, family)
			if err != nil {
				return nil, nil, err
			}
			return pool, pool.Close, nil
		},
		hub: func(ctx context.Context, o options) (*writerhold.Operator, error) {
			return open.hub(o.HubFlags, ctx)
		},
		now: time.Now,
	}
}

// openDatabase opens a pool on family's database from the URL its file holds
// (familyDatabaseFile), which its caller closes.
func openDatabase(ctx context.Context, o options, family string) (*pgxpool.Pool, error) {
	f, err := familyDatabaseFile(o, family)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, f.url)
	if err != nil {
		return nil, fmt.Errorf("open %s's database (%s; the URL is not echoed): %w", family, f.flag, err)
	}
	return pool, nil
}

// databaseFile is a family's database file as its flag names it. Its first line is the database's
// URL with the operator's credential, which every command opens; a daemon's file has a second, the
// connection name of the Cloud SQL instance the operator's own proxy reaches with that URL, which
// the census compares with the daemon's Deployment (censusReadDaemonStores). A file of one line
// serves every command but that comparison.
type databaseFile struct {
	// flag names the flag and value that named the file, as an error names it.
	flag string
	// url is the first line; instance the second, "" when the file has none.
	url, instance string
}

// familyDatabaseFile reads family's database file from the flag that names it:
// --dispatch-database-file for Dispatch, --daemon-database <project>=<file> for a daemon. A file
// whose first line holds no URL is refused: a pool opened on an empty URL would reach whatever
// server the environment names.
func familyDatabaseFile(o options, family string) (databaseFile, error) {
	file, flagName := o.storeFile(family)
	if file == "" {
		return databaseFile{}, fmt.Errorf("%s's store needs %s", family, flagName)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return databaseFile{}, fmt.Errorf("read %s: %w", flagName, err)
	}
	first, rest, _ := strings.Cut(string(raw), "\n")
	second, _, _ := strings.Cut(rest, "\n")
	f := databaseFile{flag: flagName, url: strings.TrimSpace(first), instance: strings.TrimSpace(second)}
	if f.url == "" {
		return databaseFile{}, fmt.Errorf("%s: its file's first line holds no database URL", flagName)
	}
	return f, nil
}
