package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/dispatch/duty"
	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/natsgrant"
)

// The fresh mode (ENVOY_CONSUMER_NAMES=fresh, IntakeNaming) names each of the intakes' durables by
// a logical name, delivery-events or delivery-jobs, and gives every consumer created for it a
// physical name of its own, <logical>~<generation>, created once and never again: nats-server can
// leave a deleted consumer's late state write in a consumer created next under the same name, which
// then skips messages after a restart (internal/dispatch/delivery's take-over deletes and creates
// its durable on every change of holder). The logical name's selection record, Dispatch's
// family's row dispatch/<logical> of the intake_consumers table (consumernames.PGStore of
// DispatchFamily), says which physical name the intake delivers from (its current), and the
// protocol that keeps it is consumernames.Names: Step 0 settles an operation another process left
// open, a take-over replaces the current with a fresh generation at the predecessor's ack floor +
// 1, saved before anything is deleted, and the check writes the current's last ack floor and
// deletes the retired generations.
//
// Every call is refused until Dispatch's sentinel row, envoy.sentinel/dispatch, says the cutover
// is done (consumernames.Admit), and a missing table, sentinel or row is a refusal too, never a
// create: the intake then delivers nothing and says so at ERROR, naming the durable. The fixed
// mode, the default, runs the take-over of the fixed names unchanged, writes nothing to the table,
// and reads its sentinel row alone: at the start (CheckNamingMode), before each take-over
// (fixedNamesAllowed) and after each reconnect (WatchFixedMarker).
//
// Each generation's calls go on a connection of its own, named for it, which the auth service
// grants exactly that generation's calls (natsgrant.ParseDispatchConnection): a take-over opens
// dispatch-intake/<logical>/<generation>/<old generation> once its operation is reserved
// (consumernames.Config.Reserved), before it reads or deletes anything, and delivers on it; a
// retired generation is read and deleted on dispatch-cleanup/<logical>/<generation>. A connect that
// fails -- the auth service down -- ends the take-over with nothing deleted, and the next attempt
// settles the operation it left. The intake's own connection, named as in the fixed mode, reads
// only the stream.

// IntakeNaming is how the intakes name their durables' consumers: the process's
// ENVOY_CONSUMER_NAMES, read once at its start. A process changes mode only by a restart.
type IntakeNaming struct {
	Mode consumernames.Mode
	// Steps counts each step of the fresh mode's protocol by durable, operation and outcome
	// (envoy_dispatch_durable_names_total); nil counts nothing.
	Steps *metrics.Counter
	// Converted is told, in the fixed mode, when an intake's take-over reads the store converted
	// (fixedNamesAllowed), with the refusal naming the setting: the process must exit. It is called
	// on the intake's own goroutine, under its consumer lease, so it must not block; nil refuses
	// and tells nothing.
	Converted func(error)
}

// CheckNamingMode is what a process in mode checks of the selection table at its start, before
// any intake runs. In the fixed mode it refuses a store the cutover converted: with the table's
// sentinel reading done, every durable is a fresh generation behind its record, and the fixed
// names must not run over it. A missing table or sentinel, a cutover not yet run or one rolled back
// is a store the fixed names own, and the fixed mode starts. A database it cannot reach, one that
// does not answer within markerReadWithin, a read Postgres refused (42501) or a row it cannot read
// it reads again every namingMarkerRetry, saying so at WARN, until it can or ctx ends: a fixed
// process never runs over a marker it has not read. The fresh mode refuses nothing here; its
// take-over refuses every call until the cutover is done (consumernames.Admit).
func CheckNamingMode(ctx context.Context, db consumernames.PGQuerier, mode consumernames.Mode) error {
	if mode == consumernames.ModeFresh {
		return nil
	}
	for {
		converted, err := readFixedMarker(ctx, db)
		switch {
		case err == nil && converted:
			return errConvertedStore()
		case err == nil:
			return nil
		}
		slog.Warn("dispatch delivery: the cutover row could not be read; the fixed consumer names wait to read it before they run",
			"setting", consumernames.ModeEnvVar, "retry_in", namingMarkerRetry, "error", err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s=%s: the %s cutover row was never read: %w (last: %w)", consumernames.ModeEnvVar, consumernames.ModeFixed, consumernames.IntakeConsumersTable, ctx.Err(), err)
		case <-time.After(namingMarkerRetry):
		}
	}
}

// WatchFixedMarker reads a fixed process's cutover marker again each time reconnected fires -- its
// database or NATS connection came back -- until ctx ends, and calls converted once, with the
// refusal CheckNamingMode answers, when a read finds the cutover done: a converted store must not
// keep running the fixed names, whichever process converted it while this one ran. A read that
// fails is read again every namingMarkerRetry until one answers, as at the start (CheckNamingMode):
// a reconnect is when a database is slowest, and the next one may never come. It is the backstop of
// the read each fixed take-over makes before it changes anything (heldDurable.fixedNamesAllowed),
// which catches the store converted whichever connection comes back first.
func WatchFixedMarker(ctx context.Context, db consumernames.PGQuerier, reconnected <-chan struct{}, converted func(error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconnected:
		}
		for {
			done, err := readFixedMarker(ctx, db)
			if err == nil && done {
				converted(errConvertedStore())
				return
			}
			if err == nil {
				break
			}
			slog.Warn("dispatch delivery: read the cutover row after a reconnect", "retry_in", namingMarkerRetry, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(namingMarkerRetry):
			}
		}
	}
}

// fixedGuard is a fixed-mode intake's read of the cutover marker before each take-over
// (guardFixedNames): the database it reads, and what it tells when the store is converted.
type fixedGuard struct {
	db        consumernames.PGQuerier
	converted func(error)
}

// guardFixedNames has each fixed take-over of the durable read the cutover marker in db, under the
// durable's consumer lease, before it deletes or creates anything (fixedNamesAllowed), and call
// converted, which must not block, when it reads the store converted. StartIntakes guards every
// fixed intake; nil converted only refuses.
func (d *heldDurable) guardFixedNames(db consumernames.PGQuerier, converted func(error)) {
	d.guard = &fixedGuard{db: db, converted: converted}
}

// fixedNamesAllowed reports whether a fixed take-over may go on, read under its consumer lease: the
// marker read, and not done. A store the cutover converted is refused, at ERROR, and told to the
// process, which exits (IntakeNaming.Converted): its fixed durable is gone, and creating it again
// would replay every retained message of delivery-events into the intake under a name a late
// write can reach. A marker that could not be read is refused too, logged at most once a
// standbyLogEvery per error, and the next attempt reads again: a fixed take-over never runs over a
// marker it has not read. An unguarded durable is always allowed.
func (d *heldDurable) fixedNamesAllowed(ctx context.Context) bool {
	if d.guard == nil {
		return true
	}
	converted, err := readFixedMarker(ctx, d.guard.db)
	switch {
	case err != nil:
		d.throttle.reportStandbyAt(slog.LevelWarn, d.name, "dispatch delivery: the cutover row could not be read, so the fixed take-over changes nothing until it is: "+err.Error())
		return false
	case converted:
		err := errConvertedStore()
		slog.Error("dispatch delivery: the fixed take-over stopped", "name", d.name, "error", err)
		if d.guard.converted != nil {
			d.guard.converted(err)
		}
		return false
	}
	return true
}

// namingMarkerRetry is how long a fixed process waits between reads of a cutover row it could not
// read, at its start and after a reconnect, and markerReadWithin bounds each read.
var namingMarkerRetry = 2 * time.Second

const markerReadWithin = 5 * time.Second

// readFixedMarker reads whether the cutover converted the store: true for Dispatch's sentinel
// (consumernames.PGSentinelKey of DispatchFamily) reading done; false for no marker -- no table
// (ErrNoStore), no sentinel row (ErrNoSentinel) -- and for any other marker; an error for a read
// that did not answer what the row says: a read Postgres refused (42501), so a role that may not
// read the row has not read the marker, and a row at Dispatch's key that is no sentinel of
// Dispatch's (ErrMalformedSentinel: one naming another family or none, under another key, or not
// decoding), which is not a missing row.
func readFixedMarker(ctx context.Context, db consumernames.PGQuerier) (bool, error) {
	read, cancel := context.WithTimeout(ctx, markerReadWithin)
	defer cancel()
	records, err := consumernames.NewPGStore(consumernames.DispatchFamily, db)
	if err != nil {
		return false, err
	}
	state, err := records.State(read)
	switch {
	case errors.Is(err, consumernames.ErrNoStore), errors.Is(err, consumernames.ErrNoSentinel):
		return false, nil
	case err != nil:
		return false, err
	}
	return state.Cutover == consumernames.CutoverDone, nil
}

// errConvertedStore is the refusal of a fixed process over a converted store.
func errConvertedStore() error {
	return fmt.Errorf("%s=%s, but the %s cutover row reads %q: the cutover converted the intakes' durables to fresh names, and the fixed names must not run over them; set %s=%s",
		consumernames.ModeEnvVar, consumernames.ModeFixed, consumernames.IntakeConsumersTable, consumernames.CutoverDone, consumernames.ModeEnvVar, consumernames.ModeFresh)
}

// freshNaming is one intake's durable in the fresh mode: its logical name's protocol, the
// connection of each generation the take-over under way reserved, and what it read of each.
type freshNaming struct {
	logical string
	store   consumernames.Store
	names   *consumernames.Names
	connect func(name string) (*bus.Client, error)
	// reader is the intake's own connection, which reads the stream and nothing of a consumer.
	reader *bus.Client
	// candidates are the connections the take-over under way opened, by the generation each
	// creates. The run loop alone reads and writes them, as it does the take-over.
	candidates map[string]*candidateConn
	// read is each consumer's info as this intake last read it, by physical name: what a replace
	// creates its candidate from when the predecessor carries a higher configuration generation.
	read map[string]*natsgo.ConsumerInfo
	// current is the physical name this intake delivers from while it delivers, else "".
	current string
	// refusals throttles the ERROR line of a refused call, per message, as standbys are.
	refusals bindThrottle
	// unadmitted is set when a generation's connection was not admitted, and cleared once a probe
	// connection is (admitted): until then no take-over reserves another candidate.
	unadmitted bool
}

// admissionProbeConnection names the connection a take-over opens, after a generation's connection
// was not admitted, to learn whether the auth service admits connections again before it reserves
// another candidate. The name asks for no generation (natsgrant.ParseDispatchConnection), so its
// grant is the fixed one.
const admissionProbeConnection = "dispatch-admission"

// admitted reports whether a take-over may reserve a candidate: always, unless the last
// generation's connection was not admitted, and then only once a probe connection is. A candidate
// reserved while the auth service admits nothing would be retired unborn at the next attempt, and
// one each attempt would grow the record's retired list, and its cleanup connections, for as long
// as the outage lasts. A probe that is not admitted is logged as a refusal.
func (d *heldDurable) admitted(ctx context.Context) bool {
	f := d.fresh
	if !f.unadmitted {
		return true
	}
	probe, err := f.open(ctx, admissionProbeConnection)
	if err != nil {
		d.refused(fmt.Errorf("the auth service admits no connection, so no candidate is reserved: %w", err))
		return false
	}
	probe.Close()
	f.unadmitted = false
	return true
}

// candidateConn is the connection a take-over opened for one candidate generation: granted its
// create, info and acknowledgements, and the info and delete of old.
type candidateConn struct {
	client *bus.Client
	old    string
	// delivery is the subscription the candidate's create bound, nil before it.
	delivery *intakeDelivery
}

// generationConnectWithin bounds the wait for a generation's connection to be admitted. The auth
// service answers within its two-second budget; a connect not admitted by then is a callout that
// does not answer, and the take-over stops before anything is deleted.
const generationConnectWithin = 5 * time.Second

// nameFreshly puts the intake's durable in the fresh mode: its selection records in store, each
// generation's connection opened by connect, and each step counted on steps. The intake's own
// client becomes the stream's reader (freshNaming.reader), and d.nats is nil until the first
// take-over sets it to the connection of the generation it delivers from. Its naming is the family
// table's for Dispatch's store (consumernames.LogicalNaming, FamilyNaming's for DispatchFamily),
// the one an operator's reader judges the record by (consumernames.CheckFamily).
func (d *heldDurable) nameFreshly(store consumernames.Store, connect func(string) (*bus.Client, error), steps *metrics.Counter) {
	fresh := &freshNaming{
		logical: d.name, store: store, connect: connect, reader: d.nats,
		candidates: map[string]*candidateConn{}, read: map[string]*natsgo.ConsumerInfo{},
	}
	naming := consumernames.LogicalNaming()
	fresh.names = consumernames.New(consumernames.Config{
		Store:    store,
		Server:   freshServer{d},
		Prefix:   naming.Prefix,
		Fixed:    naming.Fixed,
		Floor:    func(info *jetstream.ConsumerInfo) uint64 { return info.AckFloor.Stream },
		StreamID: fresh.streamID,
		MissingRecord: func(_ context.Context, logical string) error {
			return fmt.Errorf("%w: Dispatch's durable %s has no selection row", consumernames.ErrNoRecord, logical)
		},
		Owner:    d.holder.String(),
		Reserved: d.reserved,
		Adopt:    d.adopt,
		Observe: func(op consumernames.Operation, outcome consumernames.Outcome) {
			steps.Inc([2]string{"durable", d.name}, [2]string{"op", string(op)}, [2]string{"outcome", string(outcome)})
		},
	})
	d.fresh, d.nats = fresh, nil
}

// streamID is the stream's identity (bus.StreamIDMetadataKey), read on the intake's own connection.
func (f *freshNaming) streamID(ctx context.Context) (string, error) {
	info, err := f.reader.JS().StreamInfo(bus.Stream, natsgo.Context(ctx))
	if err != nil {
		return "", err
	}
	if id := info.Config.Metadata[bus.StreamIDMetadataKey]; id != "" {
		return id, nil
	}
	return "", bus.ErrNoStreamID
}

// generationOf is the generation of a physical name of this durable, <logical>~<generation>, and
// false for any other name: a fixed name or another durable's, which no grant of this intake's
// covers.
func (f *freshNaming) generationOf(physical string) (string, bool) {
	logical, generation, found := strings.Cut(physical, "~")
	if !found || logical != f.logical || generation == "" || strings.Contains(generation, "~") {
		return "", false
	}
	return generation, true
}

// open opens the connection called name and waits, up to generationConnectWithin or ctx's end, for
// the hub to admit it; a connection not admitted by then is closed and its failure answered.
func (f *freshNaming) open(ctx context.Context, name string) (*bus.Client, error) {
	client, err := f.connect(name)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", name, err)
	}
	if err := client.AwaitAdmitted(ctx, name, generationConnectWithin); err != nil {
		return nil, err
	}
	return client, nil
}

// reserved is consumernames.Config.Reserved: a candidate is reserved, so the connection its
// generations are granted on opens now, before the take-over reads or deletes anything. Every
// candidate connection of an earlier reservation in this take-over is closed first: the protocol
// retires a candidate before it reserves the next, so no earlier one is selected again.
func (d *heldDurable) reserved(ctx context.Context, logical string, op consumernames.Op) error {
	f := d.fresh
	d.dropCandidates("")
	if err := d.freshStep(stepReserved, op.Candidate); err != nil {
		return err
	}
	generation, ok := f.generationOf(op.Candidate)
	if !ok {
		return fmt.Errorf("consumer names: candidate %s of %s is no generation of it", op.Candidate, logical)
	}
	old := ""
	if op.Old != "" {
		if old, ok = f.generationOf(op.Old); !ok {
			return fmt.Errorf("consumer names: %s's current %s is no fresh generation, which no intake connection is granted; the cutover converts it", logical, op.Old)
		}
	}
	client, err := f.open(ctx, natsgrant.DispatchIntakeConnection(logical, generation, old))
	if err != nil {
		f.unadmitted = true
		return err
	}
	f.candidates[op.Candidate] = &candidateConn{client: client, old: op.Old}
	return nil
}

// dropCandidates stops and closes every candidate connection but keep's.
func (d *heldDurable) dropCandidates(keep string) {
	for name, candidate := range d.fresh.candidates {
		if name == keep {
			continue
		}
		if candidate.delivery != nil {
			stopping, cancel := context.WithTimeout(context.Background(), intakeHoldRenew)
			candidate.delivery.stop(stopping)
			cancel()
		}
		candidate.client.Close()
		delete(d.fresh.candidates, name)
	}
}

// freshServer is consumernames.Server over the connections each generation is granted on.
type freshServer struct{ d *heldDurable }

// Info implements consumernames.Server: it reads name on a connection granted its info.
func (s freshServer) Info(ctx context.Context, name string) (*jetstream.ConsumerInfo, error) {
	client, release, err := s.d.grantedFor(ctx, name, false)
	if err != nil {
		return nil, err
	}
	defer release()
	info, err := client.JS().ConsumerInfo(bus.Stream, name, natsgo.Context(ctx))
	if errors.Is(err, natsgo.ErrConsumerNotFound) {
		delete(s.d.fresh.read, name)
		return nil, fmt.Errorf("%w: %s", consumernames.ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	s.d.fresh.read[name] = info
	return toJetStreamInfo(info)
}

// Delete implements consumernames.Server: it deletes name on a connection granted its delete. A
// consumer the server does not hold is no error.
func (s freshServer) Delete(ctx context.Context, name string) error {
	if err := s.d.freshStep(stepDelete, name); err != nil {
		return err
	}
	client, release, err := s.d.grantedFor(ctx, name, true)
	if err != nil {
		return err
	}
	defer release()
	if err := client.JS().DeleteConsumer(bus.Stream, name, natsgo.Context(ctx)); err != nil && !errors.Is(err, natsgo.ErrConsumerNotFound) {
		return err
	}
	delete(s.d.fresh.read, name)
	return nil
}

// grantedFor answers a connection granted name's info, or its delete when del is set, and what to
// call once the call is done: a candidate's own connection (its info) or the one that replaces it
// (its info and delete), the delivering connection (its current's info), and otherwise a cleanup
// connection of name's generation, closed when the call is done.
func (d *heldDurable) grantedFor(ctx context.Context, name string, del bool) (*bus.Client, func(), error) {
	f := d.fresh
	keep := func() {}
	if candidate, ok := f.candidates[name]; ok && !del {
		return candidate.client, keep, nil
	}
	for _, candidate := range f.candidates {
		if candidate.old == name {
			return candidate.client, keep, nil
		}
	}
	if !del && name == f.current && d.nats != nil {
		return d.nats, keep, nil
	}
	generation, ok := f.generationOf(name)
	if !ok {
		return nil, nil, fmt.Errorf("consumer names: %s is no generation of %s, which no connection of this intake is granted", name, f.logical)
	}
	client, err := f.open(ctx, natsgrant.DispatchCleanupConnection(f.logical, generation))
	if err != nil {
		return nil, nil, err
	}
	return client, client.Close, nil
}

// toJetStreamInfo is info as nats.go's jetstream package reads the same answer, which
// consumernames takes: both decode the server's one JSON shape.
func toJetStreamInfo(info *natsgo.ConsumerInfo) (*jetstream.ConsumerInfo, error) {
	return converted[jetstream.ConsumerInfo](info)
}

// converted is from as To decodes its JSON: nats.go's nats and jetstream packages both read the
// server's one shape of a consumer's info and of its config.
func converted[To any](from any) (*To, error) {
	raw, err := json.Marshal(from)
	if err != nil {
		return nil, err
	}
	var to To
	if err := json.Unmarshal(raw, &to); err != nil {
		return nil, err
	}
	return &to, nil
}

// createConfig is the config createFresh sends candidate name, its start aside: this build's, or
// deployed's own where that consumer carries a higher configuration generation (newestConfig, as
// takeOverConfig keeps a newer build's), named name and marked with the durable's logical name.
func (d *heldDurable) createConfig(name string, deployed *natsgo.ConsumerInfo) natsgo.ConsumerConfig {
	config := d.config()
	if deployed != nil {
		config = newestConfig(deployed, config)
	}
	config.Durable = name
	config.Metadata = maps.Clone(config.Metadata)
	if config.Metadata == nil {
		config.Metadata = map[string]string{}
	}
	config.Metadata[consumernames.LogicalMetadataKey] = d.fresh.logical
	return config
}

// adoptionCreate is createConfig for held, a consumer of this durable as the server answers it,
// read as the listener's adoption check and its correction read a family's create: this build's
// config, or held's own where a newer build made it.
func (d *heldDurable) adoptionCreate(held *jetstream.ConsumerInfo) (jetstream.ConsumerConfig, error) {
	deployed, err := converted[natsgo.ConsumerInfo](held)
	if err != nil {
		return jetstream.ConsumerConfig{}, fmt.Errorf("read %s as the nats package types it: %w", held.Name, err)
	}
	create, err := converted[jetstream.ConsumerConfig](d.createConfig(held.Name, deployed))
	if err != nil {
		return jetstream.ConsumerConfig{}, fmt.Errorf("%s's create as the jetstream package types it: %w", held.Name, err)
	}
	return *create, nil
}

// adopt is consumernames.Config.Adopt, the listener's check (bus.Client.ConsumerAdoptable): a
// candidate this take-over did not see created -- one an interrupted take-over left, of this
// process or another -- is published only when every setting no update changes is as createFresh
// would have created it, its explicit acks and its deliver subject's kind among them, and it holds
// no setting this build does not know. A refusal leaves the operation open, so the intake delivers
// nothing until an operator acts. The settings an update changes, its filter among them, are
// corrected in place before the intake delivers from it (correctAdopted).
func (d *heldDurable) adopt(ctx context.Context, _ string, candidate *jetstream.ConsumerInfo) error {
	create, err := d.adoptionCreate(candidate)
	if err != nil {
		return err
	}
	client, release, err := d.grantedFor(ctx, candidate.Name, false)
	if err != nil {
		return err
	}
	defer release()
	return client.ConsumerAdoptable(ctx, candidate.Name, create)
}

// correctAdopted brings current, a candidate Step 0 adopted, to its durable's create in every
// setting an update changes (bus.CorrectedConfig) -- its filter, ack wait and pending bound among
// them -- on client, the connection its generation is granted its create on, before the intake
// binds it. The update goes out as the create does, with the filter after the name, which the
// generation's grant holds.
func (d *heldDurable) correctAdopted(ctx context.Context, client *bus.Client, current string) error {
	held, err := client.ConsumerInfo(ctx, current)
	if err != nil {
		return err
	}
	create, err := d.adoptionCreate(held)
	if err != nil {
		return err
	}
	next, changed := bus.CorrectedConfig(held.Config, create)
	if !changed {
		return nil
	}
	update, err := converted[natsgo.ConsumerConfig](next)
	if err != nil {
		return fmt.Errorf("%s's correction as the nats package types it: %w", current, err)
	}
	if _, err := client.JS().UpdateConsumer(bus.Stream, update, natsgo.Context(ctx)); err != nil {
		return fmt.Errorf("correct %s: %w", current, err)
	}
	return nil
}

// errCreateRefused marks a create the connection's grant refused, which the server answered: the
// candidate was never created.
var errCreateRefused = &jetstream.APIError{Code: 403, Description: "the connection's grant refused the create"}

// createFresh is the take-over's consumernames.CreateFunc: it creates candidate name at start on
// the connection reserved for it, subscribing the candidate's deliver subject first, as the fixed
// mode's take-over does, so the candidate is push-bound from the moment it exists. Its config is
// this build's, or the predecessor's when that carries a higher configuration generation
// (takeOverConfig), named name and marked with its logical name. A create the server answered with
// an error is a *jetstream.APIError, one at the stream's max_consumers wraps
// consumernames.ErrConsumerLimit, and any other failure is a create whose outcome is unknown, which
// the protocol settles with an info.
func (d *heldDurable) createFresh(ctx context.Context, name string, start consumernames.Start) (*jetstream.ConsumerInfo, error) {
	candidate, ok := d.fresh.candidates[name]
	if !ok {
		return nil, notSent(fmt.Errorf("no connection is reserved for candidate %s", name))
	}
	if !d.holds() {
		return nil, notSent(errHoldNotHeld)
	}
	config := d.createConfig(name, d.fresh.read[candidate.old])
	switch start.Native {
	case consumernames.NativeNew:
		config.DeliverPolicy, config.OptStartSeq = natsgo.DeliverNewPolicy, 0
	case consumernames.NativeAll:
		config.DeliverPolicy, config.OptStartSeq = natsgo.DeliverAllPolicy, 0
	default:
		config = startingAt(config, start.Seq)
	}
	if candidate.delivery == nil {
		delivery, err := d.subscribeOn(candidate.client, name, config.DeliverSubject)
		if err != nil {
			return nil, notSent(fmt.Errorf("bind %s before its create: %w", name, err))
		}
		candidate.delivery = delivery
	}
	if err := d.freshStep(stepSubscribed, name); err != nil {
		return nil, notSent(err)
	}
	if !d.holds() {
		return nil, notSent(errHoldNotHeld)
	}
	info, err := createDurable(candidate.client.JS(), &config, natsgo.Context(ctx))
	if err == nil {
		d.fresh.read[name] = info
		if err := d.freshStep(stepCreated, name); err != nil {
			return nil, err
		}
		return toJetStreamInfo(info)
	}
	var api *natsgo.APIError
	switch {
	case errors.As(err, &api) && api.ErrorCode == natsgo.ErrorCode(jetstream.JSErrCodeMaximumConsumersLimit):
		return nil, fmt.Errorf("%w: %w", consumernames.ErrConsumerLimit, err)
	case errors.As(err, &api):
		return nil, &jetstream.APIError{Code: api.Code, ErrorCode: jetstream.ErrorCode(api.ErrorCode), Description: api.Description}
	case errors.Is(err, bus.ErrPublishDenied):
		return nil, fmt.Errorf("%w: %w", errCreateRefused, err)
	}
	return nil, err
}

// errNotSent marks a create this intake did not send -- its hold lost, its candidate not bound --
// which the protocol takes as answered: nothing was created, and the operation stays open for the
// next attempt's Step 0.
var errNotSent = &jetstream.APIError{Description: "the create was not sent"}

// notSent is err marked errNotSent.
func notSent(err error) error { return fmt.Errorf("%w: %w", errNotSent, err) }

// The points of the fresh take-over a test can stop a process at (heldDurable.freshHook).
const (
	// stepReserved is a candidate reserved, before its generation's connection opens.
	stepReserved = "reserved"
	// stepSubscribed is a candidate's deliver subject subscribed, before its create is sent.
	stepSubscribed = "subscribed"
	// stepCreated is a candidate's create answered, before the protocol publishes it.
	stepCreated = "created"
	// stepDelete is a delete of a predecessor or a retired generation, before it is sent.
	stepDelete = "delete"
)

// freshStep runs the test's hook at point for the physical name, nil outside tests.
func (d *heldDurable) freshStep(point, name string) error {
	if d.freshHook == nil {
		return nil
	}
	return d.freshHook(point, name)
}

// firstCreate is the durable's first-create rule, for a record that selects nothing and saved no
// start: its native policy (DeliverAll for the events durable, DeliverNew for the jobs durable),
// with the numeric boundary a recovery creates at instead -- the stream's first sequence for
// DeliverAll, the sequence after its last for DeliverNew -- read before the reservation, so it can
// only redeliver.
func (d *heldDurable) firstCreate(ctx context.Context) (consumernames.Start, error) {
	info, err := d.fresh.reader.JS().StreamInfo(bus.Stream, natsgo.Context(ctx))
	if err != nil {
		return consumernames.Start{}, fmt.Errorf("read the stream for %s's first create: %w", d.name, err)
	}
	if d.config().DeliverPolicy == natsgo.DeliverNewPolicy {
		return consumernames.Start{Seq: info.State.LastSeq + 1, Native: consumernames.NativeNew}, nil
	}
	return consumernames.Start{Seq: max(info.State.FirstSeq, 1), Native: consumernames.NativeAll}, nil
}

// takeOverFresh is the fresh mode's take-over, under the hold and the durable's consumer lease: it
// settles the record (Step 0, consumernames.Names.Ensure), creating the durable's first generation
// when the record selects none, and replaces a current that was there before this take-over with a
// fresh generation at the current's ack floor + 1, or at the hold token's start where that is
// lower (Replace): a take-over fences off the last holder's subscription by a deliver subject of
// its own, as the fixed mode's delete and create do. A current this take-over made the current --
// a generation it created, or a candidate Step 0 adopted, whose create's answer was lost or whose
// creator stopped before its publish -- is kept, bound at its own deliver subject on a connection
// granted its generation. Then the hold records the deliver subject, as the fixed mode's does, and
// the intake delivers on that connection; the connection of the generation it delivered from
// before is closed. A current bound to a subject the hold does not record is a process that holds
// no hold, left alone (boundElsewhere).
//
// It runs on a context of its own, ending at takeOverFinishWithin or the hold's loss deadline, so
// a stop of the process does not cut a step in half and no step acts once another process can take
// the hold. A take-over that fails leaves what the record says -- an operation open, its start
// saved before anything was deleted -- for the next attempt's Step 0, and drops every connection
// it opened.
func (d *heldDurable) takeOverFresh(context.Context) {
	if !d.holds() {
		return
	}
	finish, cancel := context.WithTimeout(context.Background(), takeOverFinishWithin)
	defer cancel()
	ctx, cancelHeld := context.WithDeadline(finish, d.hold.Deadline())
	defer cancelHeld()
	f := d.fresh
	if !d.admitted(ctx) {
		return
	}
	sel, _, err := f.names.Ensure(ctx, f.logical, consumernames.CreateRequest{Create: d.createFresh, First: d.firstCreate})
	if err == nil && !sel.Created {
		if sel.Info != nil && d.boundElsewhere(natsInfo(sel.Info)) {
			d.dropCandidates("")
			return
		}
		start := uint64(0)
		if token := d.recorded.start; token != 0 && sel.Info != nil && token < sel.Info.AckFloor.Stream+1 {
			start = token
		}
		sel, err = f.names.Replace(ctx, sel, start, d.createFresh)
	}
	if err != nil {
		d.refused(err)
		d.dropCandidates("")
		return
	}
	current := sel.Record.Current
	subject := ""
	if sel.Info != nil {
		subject = sel.Info.Config.DeliverSubject
	}
	if subject == "" {
		slog.Error("dispatch delivery: the take-over's current has no deliver subject", "name", d.name, "physical", current)
		d.dropCandidates("")
		return
	}
	d.dropCandidates(current)
	candidate := f.candidates[current]
	if candidate == nil {
		// A candidate another process reserved and created, which Step 0 adopted: its own
		// generation's connection is granted its acknowledgements, and its create, on which what an
		// update changes is brought to this durable's create before it delivers anything.
		generation, ok := f.generationOf(current)
		if !ok {
			slog.Error("dispatch delivery: the take-over's current is no generation of the durable", "name", d.name, "physical", current)
			return
		}
		client, err := f.open(ctx, natsgrant.DispatchIntakeConnection(f.logical, generation, ""))
		if err != nil {
			slog.Error("dispatch delivery: connect the adopted generation", "name", d.name, "physical", current, "error", err)
			return
		}
		candidate = &candidateConn{client: client}
		f.candidates[current] = candidate
		if err := d.correctAdopted(ctx, client, current); err != nil {
			slog.Error("dispatch delivery: correct the adopted generation", "name", d.name, "physical", current, "error", err)
			d.dropCandidates("")
			return
		}
	}
	if candidate.delivery == nil || candidate.delivery.subject != subject {
		// An adopted candidate delivers at its own deliver subject: bind that one.
		if candidate.delivery != nil {
			candidate.delivery.stop(ctx)
		}
		delivery, err := d.subscribeOn(candidate.client, current, subject)
		if err != nil {
			slog.Error("dispatch delivery: bind NATS consumer", "name", d.name, "physical", current, "subject", subject, "error", err)
			d.dropCandidates("")
			return
		}
		candidate.delivery = delivery
	}
	bound := intakeHoldToken{subject: subject}
	if d.retryHeld(finish, "record the intake's deliver subject", func(call context.Context) error {
		return d.hold.SetToken(call, bound.String())
	}, "physical", current) != nil {
		// The durable delivers here, but the hold does not say so; the next attempt takes it over
		// again.
		d.dropCandidates("")
		return
	}
	delete(f.candidates, current)
	if previous := d.nats; previous != nil && previous != candidate.client {
		previous.Close()
	}
	d.nats, d.recorded, d.subject, f.current = candidate.client, bound, subject, current
	slog.Info("dispatch delivery: NATS consumer bound", "name", d.name, "physical", current, "subject", d.config().FilterSubject,
		"start_seq", sel.Record.Start, "generation", bus.MetadataGeneration(sel.Info.Config.Metadata, consumerGenerationKey))
	if d.boundHook != nil {
		d.boundHook()
	}
}

// natsInfo is the part of a consumernames info boundElsewhere reads.
func natsInfo(info *jetstream.ConsumerInfo) *natsgo.ConsumerInfo {
	return &natsgo.ConsumerInfo{Name: info.Name, PushBound: info.PushBound,
		Config: natsgo.ConsumerConfig{Durable: info.Config.Durable, DeliverSubject: info.Config.DeliverSubject}}
}

// refused logs a take-over the protocol stopped, at ERROR, at most once a standbyLogEvery for the
// same message: a refusal (the cutover not run, lost selection state, no safe start) repeats at
// every attempt until an operator acts.
func (d *heldDurable) refused(err error) {
	d.fresh.refusals.reportStandbyAt(slog.LevelError, d.name, "dispatch delivery: the fresh-name take-over stopped: "+err.Error())
}

// checkFresh is check in the fresh mode: it stops delivering when the current no longer delivers
// to this intake's subject, writes the current's ack floor as the record's last floor, and deletes
// the retired generations under the durable's consumer lease.
func (d *heldDurable) checkFresh(ctx context.Context) {
	if d.subject == "" {
		return
	}
	f := d.fresh
	lookup, cancel := context.WithTimeout(ctx, intakeHoldRenew)
	defer cancel()
	info, err := d.nats.JS().ConsumerInfo(bus.Stream, f.current, natsgo.Context(lookup))
	switch {
	case errors.Is(err, natsgo.ErrConsumerNotFound):
	case err != nil:
		return
	case info.Config.DeliverSubject == d.subject:
		d.recordLastFloor(lookup, info)
		d.cleanRetired(ctx)
		return
	}
	slog.Warn(durableDeliversElsewhere, "name", d.name, "physical", f.current)
	d.stopDeliveringWithin(intakeHoldRenew)
	d.subject, f.current = "", ""
}

// recordLastFloor writes info's ack floor as the record's last floor of the current, with the
// stream's identity. A failure is logged and changes nothing: the saved start still governs.
func (d *heldDurable) recordLastFloor(ctx context.Context, info *natsgo.ConsumerInfo) {
	f := d.fresh
	streamID, err := f.streamID(ctx)
	if err == nil {
		err = f.names.RecordLastFloor(ctx, f.logical, f.current, info.AckFloor.Stream, streamID)
	}
	if err != nil && ctx.Err() == nil {
		slog.Warn("dispatch delivery: record the current's last floor", "name", d.name, "physical", f.current, "error", err)
	}
}

// cleanRetired deletes the record's retired generations the server still holds, under the
// durable's consumer lease, when the record names any.
func (d *heldDurable) cleanRetired(ctx context.Context) {
	f := d.fresh
	record, _, err := f.store.Read(ctx, f.logical)
	if err != nil || len(record.Retired) == 0 {
		return
	}
	duty.Exclusive(ctx, d.leases, consumerLease(d.name), time.Time{}, 0, func(ctx context.Context) {
		if _, err := f.names.Cleanup(ctx, f.logical); err != nil && ctx.Err() == nil {
			slog.Warn("dispatch delivery: delete the retired generations", "name", d.name, "error", err)
		}
	})
}
