package intake

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
)

// fixedNaming is the daemon's durables on the fixed names (ConsumerSpec.Names "fixed", the default):
// each durable is its own name, legion-go-<project>-<kind>, opened as the daemon has always opened
// it (openConsumer), once the project's cutover marker says the operator's convert has not made the
// store's durables fresh names (refuseConvertedStore). The marker is read at the boot
// (OpenConsumers), again before each open after the daemon's connection reconnected (rereadMarker),
// and from a watch beside Run (watch).
type fixedNaming struct {
	js      jetstream.JetStream
	stream  jetstream.Stream
	pool    Pool
	project string
	logger  *slog.Logger

	// marker is what the fixed names know of the cutover marker (rereadMarker): the count of the
	// connection's reconnects at its last read, and the refusal once a read found the store
	// converted.
	marker struct {
		sync.Mutex
		reconnects uint64
		converted  error
	}
}

// newFixedNaming is the fixed names of spec's project on stream, whose marker the boot has just
// read at the connection's present count of reconnects.
func newFixedNaming(js jetstream.JetStream, stream jetstream.Stream, pool Pool, spec ConsumerSpec) *fixedNaming {
	f := &fixedNaming{js: js, stream: stream, pool: pool, project: spec.Project, logger: spec.Logger}
	f.marker.reconnects = js.Conn().Stats().Reconnects
	return f
}

// open opens config's durable, whose Durable is its fixed name, as main always has (openConsumer),
// once the cutover marker has been read since the connection's last reconnect (rereadMarker).
func (f *fixedNaming) open(ctx context.Context, config jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	if err := f.rereadMarker(ctx); err != nil {
		return nil, err
	}
	return openConsumer(ctx, f.js, f.stream, f.pool, f.logger, config)
}

// info is the info of the durable named name, read fresh through a handle of its own.
func (f *fixedNaming) info(ctx context.Context, name string) (*jetstream.ConsumerInfo, error) {
	consumer, err := f.stream.Consumer(ctx, name)
	if err != nil {
		return nil, err
	}
	return consumer.CachedInfo(), nil
}

// watch is Run's watch of the daemon's connection: every markerCheckEvery it reads the cutover
// marker again if the connection has reconnected since the last read (rereadMarker), and it returns
// the refusal of a store found converted, which ends Run. A read that does not answer within
// markerReadBound is made again at the next look.
func (f *fixedNaming) watch(ctx context.Context) error {
	ticker := time.NewTicker(markerCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		read, cancel := context.WithTimeout(ctx, markerReadBound)
		err := f.rereadMarker(read)
		cancel()
		if errors.Is(err, errStoreConverted) {
			return err
		}
	}
}

// close closes nothing: the fixed names open no connection of their own.
func (f *fixedNaming) close() {}

// markerCheckEvery is how often Run, on fixed names, looks for a reconnect of the daemon's
// connection whose marker it has not read (fixedNaming.watch), and markerReadBound bounds one such
// read.
const (
	markerCheckEvery = 250 * time.Millisecond
	markerReadBound  = 30 * time.Second
)

// rereadMarker reads the cutover marker again (refuseConvertedStore) when the daemon's connection
// has reconnected since the last read, at its boot or after a reconnect: a fixed daemon that only
// reconnects across the operator's hold stops at its first connect after the convert rather than
// create a fixed name again. A read that found the store converted is the answer from then on. A
// read that did not answer leaves the count, so the next call reads again.
func (f *fixedNaming) rereadMarker(ctx context.Context) error {
	f.marker.Lock()
	converted, seen := f.marker.converted, f.marker.reconnects
	f.marker.Unlock()
	if converted != nil {
		return converted
	}
	reconnects := f.js.Conn().Stats().Reconnects
	if reconnects == seen {
		return nil
	}
	err := refuseConvertedStore(ctx, f.pool, f.project)
	f.marker.Lock()
	defer f.marker.Unlock()
	switch {
	case errors.Is(err, errStoreConverted):
		f.marker.converted = err
	case err == nil:
		f.marker.reconnects = max(f.marker.reconnects, reconnects)
	}
	return err
}

// errStoreConverted is the refusal of the fixed names on a database the operator's convert has
// marked converted (refuseConvertedStore).
var errStoreConverted = errors.New("intake_consumers says this database's durable consumers were converted to fresh names (cutover done), and legion.yaml's consumer_names is fixed: set consumer_names: fresh, since a converted store must not run the fixed names")

// refuseConvertedStore refuses the fixed names on a database whose sentinel the operator's convert
// has marked converted (cutover "done"), at the boot and after each reconnect of the daemon's
// connection (fixedNaming.rereadMarker): its durables are fresh names now, and creating the fixed
// ones again would run the race the cutover removed. What says there is no marker is
// today's code, which opens the fixed names: no table, no sentinel, a sentinel before any cutover,
// and one a rollback wrote ("rolled_back", which restored the fixed names). A read that says
// nothing either way is made again, from a quarter second doubling to five, until it answers or
// ctx ends, and then its error is returned: no connection, a timeout, a sentinel that is not the
// project's own (consumernames.ErrMalformedSentinel), and a read the database refuses
// (insufficient_privilege, SQLSTATE 42501), since a login that may not read the table says nothing
// of what the table holds. The boot ends on that error, and the watch beside Run reads again at its
// next look (fixedNaming.watch). The fixed names never open on a marker the daemon did not read.
func refuseConvertedStore(ctx context.Context, pool Pool, project string) error {
	store, err := daemonStore(pool, project)
	if err != nil {
		return err
	}
	wait := markerReadWait
	for {
		state, err := store.State(ctx)
		switch {
		case err == nil && state.Cutover == consumernames.CutoverDone:
			return errStoreConverted
		case err == nil, errors.Is(err, consumernames.ErrNoStore), errors.Is(err, consumernames.ErrNoSentinel):
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("read the cutover marker of intake_consumers, which the fixed names never start without: %w", err)
		case <-time.After(wait):
		}
		wait = min(2*wait, markerReadWaitMost)
	}
}

// The waits between reads of the cutover marker that did not answer (refuseConvertedStore).
const (
	markerReadWait     = 250 * time.Millisecond
	markerReadWaitMost = 5 * time.Second
)
