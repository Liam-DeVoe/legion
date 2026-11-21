package consumernames

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nuid"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/testpg/testdb"
	"github.com/sjawhar/envoy/natsgrant"
)

// stack is the commands' whole surrounding in a test, real but for the platform tokens: the
// writer hold's stack (holdtest.Stack: the broker's hold routes on a test Postgres, a three-server
// cluster every client of which the NATS auth service decides, and that auth service reading the
// hold row at every decision), and a Postgres database of each family store a test names, migrated
// by the stores' own migrations. Like holdtest, it skips the test when BROKER_TEST_DATABASE_URL is
// unset.
type stack struct {
	t *testing.T
	s *holdtest.Stack
	e env

	mu  sync.Mutex
	dbs map[string]*pgxpool.Pool
}

// newStack starts a stack, stopped when t ends.
func newStack(t *testing.T) *stack {
	t.Helper()
	s := holdtest.Start(t)
	h := &stack{t: t, s: s, dbs: map[string]*pgxpool.Pool{}}
	h.e = env{
		records: func(context.Context, options, bool) (*holdrecord.Client, error) { return s.Broker.Client(t), nil },
		connect: func(context.Context, options) (*nats.Conn, error) {
			return nats.Connect(s.Cluster.URL(), append(s.OperatorCredential(), nats.Name("envoy-infra consumer-names"),
				nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond), nats.Timeout(10*time.Second))...)
		},
		database: func(_ context.Context, _ options, family string) (Database, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			db, ok := h.dbs[family]
			if !ok {
				return nil, fmt.Errorf("the test made no database for %s", family)
			}
			return db, nil
		},
		now: time.Now,
	}
	return h
}

// commonArgs are the flags every command takes, which the stack's env stands in for.
var commonArgs = []string{"--broker-url", "https://broker.consumernames-test", "--token-file", "/dev/null",
	"--verify-keys", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "--nats", "nats://consumernames-test:4222"}

// writingArgs are the flags every command but status takes beside commonArgs.
var writingArgs = []string{"--signing-key-secret", "projects/consumernames-test/secrets/operator-only-envoy-hold-signing"}

// run runs `envoy-infra consumer-names <args>` in the stack, with the common flags, and returns its
// exit code and output.
func (h *stack) run(args ...string) (int, string, string) {
	h.t.Helper()
	full := append(append([]string{}, args...), commonArgs...)
	if args[0] != actionStatus {
		full = append(full, writingArgs...)
	}
	var stdout, stderr bytes.Buffer
	code := runCommand(context.Background(), full, &stdout, &stderr, h.e)
	h.t.Logf("envoy-infra consumer-names %s: exit %d\n%s%s", strings.Join(args, " "), code, stdout.String(), stderr.String())
	return code, stdout.String(), stderr.String()
}

// records is the operator's client of the stack's hold routes.
func (h *stack) records() *holdrecord.Client { return h.s.Records }

// row reads the hold row.
func (h *stack) row() holdrecord.Row {
	h.t.Helper()
	row, err := h.s.Records.Row(context.Background())
	require.NoError(h.t, err)
	return row
}

// prepared moves the row from released to prepare under a new hold id, with the hold's journal
// for workflow, as `envoy-infra hold prepare` writes them (its minter preflight aside), and
// returns the id.
func (h *stack) prepared(w holdrecord.Workflow) string {
	h.t.Helper()
	ctx := context.Background()
	row := h.row()
	require.Equal(h.t, natsgrant.OperatorPhaseReleased, row.Phase, "a hold is prepared only from released")
	id := nuid.Next()
	_, err := h.s.Records.MoveRow(ctx, row.Gen, natsgrant.OperatorPhasePrepare, id, "the test's hold")
	require.NoError(h.t, err)
	require.NoError(h.t, h.s.Records.StartJournal(ctx, id, w, nil))
	return id
}

// held moves a prepared hold to held the way `hold set` writes its journal (its intent with the
// row prepare, the move, its done entry), without the steps it runs against the hub: a row that
// tests what comes after a set hold rather than the steps themselves.
func (h *stack) held(id string) {
	h.t.Helper()
	ctx := context.Background()
	_, err := h.s.Records.Admit(ctx, id, holdrecord.Request{Command: holdrecord.CommandSet}, nil)
	require.NoError(h.t, err)
	row := h.row()
	_, err = h.s.Records.MoveRow(ctx, row.Gen, natsgrant.OperatorPhaseHeld, id, "the test's hold")
	require.NoError(h.t, err)
	require.NoError(h.t, h.s.Records.AppendDone(ctx, id, holdrecord.CommandSet, nil))
}

// heldHold is a hold of workflow, prepared and set (held).
func (h *stack) heldHold(w holdrecord.Workflow) string {
	h.t.Helper()
	id := h.prepared(w)
	h.held(id)
	return id
}

// release releases hold id the way `hold release` writes its journal and the row (its admission,
// the row's move to released, its done entry), and answers the admission's refusal: a hold whose
// journal is not complete is refused, and the row stays held.
func (h *stack) release(id string) error {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.s.Records.Admit(ctx, id, holdrecord.Request{Command: holdrecord.CommandRelease}, nil); err != nil {
		return err
	}
	row := h.row()
	if _, err := h.s.Records.MoveRow(ctx, row.Gen, natsgrant.OperatorPhaseReleased, id, "the test's release"); err != nil {
		return err
	}
	return h.s.Records.AppendDone(ctx, id, holdrecord.CommandRelease, nil)
}

// journal reads hold id's journal.
func (h *stack) journal(id string) holdrecord.Done {
	h.t.Helper()
	d, _, err := h.s.Records.Journal(context.Background(), id)
	require.NoError(h.t, err)
	return d
}

// operator connects to the hub as the operator, decided under the row's phase now: a test that
// changes the phase connects again for the new phase's grant.
func (h *stack) operator() (*nats.Conn, jetstream.JetStream) {
	h.t.Helper()
	nc, err := h.s.Connect(h.s.Cluster.URL(), h.s.OperatorCredential()...)
	require.NoError(h.t, err)
	js, err := jetstream.New(nc)
	require.NoError(h.t, err)
	return nc, js
}

// notificationStream creates ENVOY_NOTIFICATIONS on js with the build's configuration at one copy,
// as `envoy-infra streams update` would under a hold, and answers its info.
func (h *stack) notificationStream(js jetstream.JetStream) *jetstream.StreamInfo {
	h.t.Helper()
	ctx := context.Background()
	config := bus.NotificationStreamConfig(1, 0)
	raw, err := json.Marshal(config)
	require.NoError(h.t, err)
	var cfg jetstream.StreamConfig
	require.NoError(h.t, json.Unmarshal(raw, &cfg))
	stream, err := js.CreateStream(ctx, cfg)
	require.NoError(h.t, err)
	return stream.CachedInfo()
}

// The migrations of the families' stores, by the store's own files, so a test's table is the one
// each store's migration makes.
var (
	dispatchMigrations = []string{
		"../../internal/dispatch/store/migrations/20261101143201_duty_holds.up.sql",
		"../../internal/dispatch/store/migrations/20261121002206_intake_consumers.up.sql",
	}
	daemonMigrations = []string{
		"../../../daemon/internal/store/migrations/0063_consumer_positions.up.sql",
		"../../../daemon/internal/store/migrations/20261121001600_intake_consumers.up.sql",
	}
)

// database makes family's store a database of its own on the test's Postgres server, with each
// of migrations run in order (none: a store with no table), and answers its pool, which the
// stack's env hands the commands.
func (h *stack) database(family string, migrations ...string) *pgxpool.Pool {
	h.t.Helper()
	ctx := context.Background()
	url := testdb.DatabaseOn(h.t, os.Getenv("BROKER_TEST_DATABASE_URL"), "cnames")
	pool, err := pgxpool.New(ctx, url)
	require.NoError(h.t, err)
	h.t.Cleanup(pool.Close)
	for _, file := range migrations {
		sql, err := os.ReadFile(filepath.FromSlash(file))
		require.NoError(h.t, err)
		_, err = pool.Exec(ctx, string(sql))
		require.NoError(h.t, err, "migrate %s with %s", family, file)
	}
	h.mu.Lock()
	h.dbs[family] = pool
	h.mu.Unlock()
	return pool
}
