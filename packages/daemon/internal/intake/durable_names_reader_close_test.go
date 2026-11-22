//go:build odatrace

package intake

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/testnatsinprocess"
	"github.com/sjawhar/legion/daemon/internal/testnats"
)

// readerCloseRig is a fresh daemon's creator on the hooked in-process server, with the record of
// its GitHub durable as a row plants it.
type readerCloseRig struct {
	server  *testnats.InProcessServer
	hooks   *testnatsinprocess.Hooks
	pool    *pgxpool.Pool
	d       *durableNames
	github  jetstream.ConsumerConfig
	logical string
}

func newReaderCloseRig(t *testing.T) readerCloseRig {
	t.Helper()
	server, url := testnats.StartInProcess(t)
	hooks := testnatsinprocess.Hook(t)
	js := inProcessJetStream(t, url)
	identifiedStream(t, js)
	pool := migratedPool(t)
	cutover(t, pool, consumernames.CutoverDone)
	spec := freshSpec(t, &lockedBuffer{}, js)
	d, err := newDurableNames(js, pool, spec)
	if err != nil {
		t.Fatalf("the daemon's creator: %v", err)
	}
	t.Cleanup(d.close)
	github := githubConsumerConfig(spec, spec.Repositories)
	return readerCloseRig{server: server, hooks: hooks, pool: pool, d: d, github: github, logical: github.Durable}
}

// requireOpenConnection fails t unless the daemon still holds candidate's connection, open: a
// reader's close must not land while an open of the same logical name may still read on it.
func (r readerCloseRig) requireOpenConnection(t *testing.T, candidate, after string) {
	t.Helper()
	r.d.mu.Lock()
	held, ok := r.d.generations[candidate]
	r.d.mu.Unlock()
	if !ok || held.Conn().IsClosed() {
		t.Fatalf("after %s the daemon holds %s's connection = %t, closed = %t; want it held and open, since a repositories change still reads on it", after, candidate, ok, ok && held.Conn().IsClosed())
	}
}

// requireSettled fails t unless the open ended with no error on a fresh name other than
// abandoned, which the record retired.
func (r readerCloseRig) requireSettled(t *testing.T, err error, abandoned string) {
	t.Helper()
	if err != nil {
		t.Fatalf("the repositories change's open of %s = %v, want it settled on a fresh name", r.logical, err)
	}
	rec := selectionOf(t, r.pool, r.logical)
	if rec.Current == abandoned || !isFresh(r.logical, rec.Current) || !slices.Contains(rec.Retired, abandoned) {
		t.Fatalf("the record of %s is %+v, want a fresh current other than %s, which is retired", r.logical, rec, abandoned)
	}
}

// One reader under a repositories change: the record of the GitHub durable holds a create another
// boot left open, and a repositories change's open (SetRepositories) reads its candidate on the
// candidate's connection, held at the server. A state read (Lag, info) beside it reads the same
// candidate on the same connection, finds it gone, abandons the create and retires the candidate,
// and must not close that connection: the change's read is still in flight on it, and a close
// would end it on "nats: connection closed", which Ensure does not retry, so SetRepositories
// would be refused. Once the hold is released, the change's read finds the candidate gone, its
// abandon conflicts with the reader's, and Ensure settles on a fresh name. The control is the code
// before this row, whose reader closed the connection under the change's read.
func TestForcedOrderARepositoriesChangeOutlivesAReaderThatAbandonsItsCandidate(t *testing.T) {
	r := newReaderCloseRig(t)
	abandoned := consumernames.FreshName(r.logical)
	putRecord(t, r.pool, r.logical, consumernames.Record{Op: &consumernames.Op{Kind: consumernames.OpCreate, Owner: "another boot", Candidate: abandoned, Native: consumernames.NativeNew, ReservedAt: time.Now().Add(-time.Hour)}})
	held := r.hooks.Hold(testnatsinprocess.APIWorker, testnatsinprocess.OnServer(r.server),
		testnatsinprocess.Subject("$JS.API.CONSUMER.INFO."+notificationStream+"."+abandoned))
	opened := make(chan error, 1)
	go func() {
		_, err := r.d.open(context.Background(), r.github)
		opened <- err
	}()
	held.Reached(t)
	if _, err := r.d.info(context.Background(), r.logical); err == nil {
		t.Fatalf("the reader's info of %s, whose record selects no consumer, answered", r.logical)
	}
	if retired := selectionOf(t, r.pool, r.logical).Retired; !slices.Contains(retired, abandoned) {
		t.Fatalf("the reader retired %v, want %s among them: the row abandoned nothing", retired, abandoned)
	}
	r.requireOpenConnection(t, abandoned, "the reader's abandon")
	held.Release()
	r.requireSettled(t, <-opened, abandoned)
}

// Two readers under a repositories change's own create: the change's create of its candidate is
// held at the server past the settle age (2 s). A first state read finds the candidate gone,
// abandons the create and retires the candidate, and a second reads the record after it. Neither
// may close the candidate's connection while the change's create, and launch's read of the
// candidate after it, may still be in flight on it: a close would end the create, launch would
// read the candidate again on a new connection, and the second reader would close that one under
// the read, so launch would end on "nats: connection closed" rather than retry. Once the hold is
// released, the change's publish conflicts with the abandon's write, and Ensure settles on a fresh
// name. The control is the code before this row, whose first reader closed the connection under
// the change's create.
func TestForcedOrderARepositoriesChangeOutlivesTwoReadersAfterItsCreateOutlivesTheSettleAge(t *testing.T) {
	r := newReaderCloseRig(t)
	createPrefix := "$JS.API.CONSUMER.CREATE." + notificationStream + "." + r.logical + "~"
	held := r.hooks.Hold(testnatsinprocess.APIWorker, testnatsinprocess.OnServer(r.server),
		testnatsinprocess.Where("the create of a fresh name of "+r.logical, func(e testnatsinprocess.Event) bool { return strings.HasPrefix(e.Subject, createPrefix) }))
	opened := make(chan error, 1)
	go func() {
		_, err := r.d.open(context.Background(), r.github)
		opened <- err
	}()
	parked := held.Reached(t)
	candidate := strings.Split(strings.TrimPrefix(parked.Subject, "$JS.API.CONSUMER.CREATE."+notificationStream+"."), ".")[0]
	time.Sleep(2500 * time.Millisecond)
	if _, err := r.d.info(context.Background(), r.logical); err == nil {
		t.Fatalf("the first reader's info of %s, whose record selects no consumer, answered", r.logical)
	}
	if retired := selectionOf(t, r.pool, r.logical).Retired; !slices.Contains(retired, candidate) {
		t.Fatalf("the first reader retired %v, want the change's candidate %s among them: the row abandoned nothing", retired, candidate)
	}
	r.requireOpenConnection(t, candidate, "the first reader's abandon")
	if _, err := r.d.info(context.Background(), r.logical); err == nil {
		t.Fatalf("the second reader's info of %s, whose record selects no consumer, answered", r.logical)
	}
	r.requireOpenConnection(t, candidate, "the second reader")
	held.Release()
	r.requireSettled(t, <-opened, candidate)
}
