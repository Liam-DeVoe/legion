package consumernames

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/infra/core"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// replaceMaxName is NATS's limit on a consumer's name (nats-server JSMaxNameLen), and
// replaceFreshSuffix the bytes a fresh name adds to its prefix: a '~' and a 22-character NUID
// (selection.FreshName).
const (
	replaceMaxName     = 255
	replaceFreshSuffix = 1 + 22
)

// replaceListener is `consumer-names replace --listener <machine id> --hold <id>`: it replaces one
// listener's durable by a fresh name at its floor, through the durable's selection record in the
// session consumers' bucket (selection.DurableKey of bus.ListenerDurable), as the selection
// protocol's Replace does (selection.Names.Replace). It runs only while the hold row is held under
// --hold, so no listener is connected and the durable's floor cannot move under it.
//
// It first settles an operation a crashed run, or a listener before the hold, left open on the
// record (Step 0, selection.Names.Select): a candidate the server holds is published, and one it
// does not is retired. Then it reserves the replace with start = the current's ack floor + 1, and
// a start past the sequence after the stream's last at the stream's first sequence, as every
// session consumer create clamps one (internal/bus/session_consumers.go, create); creates the
// candidate from the current's own configuration, corrected as the listener's policy needs it (no
// idle heartbeat, no flow control, ack explicit, a new reply inbox) and carrying the durable's
// logical name; publishes it as the current; retires the old name and deletes it. At the stream's
// max_consumers the protocol deletes the retired names the server holds and tries again, and then
// deletes the old name, whose start is saved, before the create. Last, it deletes every retired
// name of the record the server still holds, so a rerun after a crash between the publish and the
// delete finishes the delete. A rerun replaces again from the new current's floor: under the hold
// nothing acknowledges a message, so the new current's floor is its start minus one, and the rerun
// starts at the same sequence.
//
// No workflow's table names the command (internal/holdrecord/table.go, tableFor), so it writes no
// journal entry, and a hold that ran it is released by its own workflow's rule.
func (r *run) replaceListener(ctx context.Context) error {
	if err := core.CheckMachineID("--listener", r.o.listener); err != nil {
		return err
	}
	durable := bus.ListenerDurable(r.o.listener)
	if len(durable)+replaceFreshSuffix > replaceMaxName {
		return fmt.Errorf("--listener: the durable's fresh name, %s~<nuid>, is %d bytes, over NATS's %d-byte consumer-name limit", durable, len(durable)+replaceFreshSuffix, replaceMaxName)
	}
	m, ok := classify(durable)
	if !ok || m.Family != familySession {
		return fmt.Errorf("--listener: %s is no listener durable's name", durable)
	}
	if _, err := r.requirePhase(ctx, natsgrant.OperatorPhaseHeld); err != nil {
		return err
	}
	nc, js, err := r.conn(ctx)
	if err != nil {
		return err
	}
	if err := holdrecord.Barrier(ctx, js, selection.SessionBucket, r.o.holdID); err != nil {
		if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, jetstream.ErrNoStreamResponse) {
			return fmt.Errorf("the selection bucket %s is missing, so %s has no record to replace by: %w", selection.SessionBucket, durable, err)
		}
		return unknownf(err, "the barrier on %s", selection.SessionBucket)
	}
	store, err := r.store(ctx, familySession)
	if err != nil {
		return err
	}
	names := replaceNames(nc, store, m, r.o.holdID)
	sel, err := names.Select(ctx, m.Logical, nil)
	switch {
	case errors.Is(err, selection.ErrCutoverNotRun):
		return fmt.Errorf("%s: the selection bucket's sentinel does not read cutover done, so the listener's durable is its fixed name, which replace does not take: %w", durable, err)
	case err != nil:
		return fmt.Errorf("%s's selection record (%s): %w", durable, m.Logical, err)
	case sel.Record.Current == "":
		return fmt.Errorf("%s's selection record (%s) selects no current: the listener's start creates its durable at the record's start", durable, m.Logical)
	case sel.Missing:
		return fmt.Errorf("%s's selection record (%s) selects %s, which %s does not hold: the listener's start creates it again at the start the record proves", durable, m.Logical, sel.Record.Current, notifications)
	}
	old, info := sel.Record.Current, sel.Info
	stream, err := streamInfo(ctx, nc, notifications)
	if err != nil {
		return unknownf(err, "the info of %s", notifications)
	}
	start := replaceStart(info.AckFloor.Stream, stream.State)
	replaced, err := names.Replace(ctx, sel, start, replaceCreate(nc, info.Config, m))
	if err != nil {
		return fmt.Errorf("replace %s (%s) at %d: %w", old, durable, start, err)
	}
	deleted, err := replaceDeleteRetired(ctx, nc, replaced.Record)
	if err != nil {
		return fmt.Errorf("%s is %s's current at %d, and %w", replaced.Record.Current, durable, replaced.Record.Start, err)
	}
	fmt.Fprintf(r.stdout, "replace: %s (record %s): %s replaced by %s at stream sequence %s; %s retired; deleted %d retired name(s) the server held\n",
		durable, m.Logical, old, replaced.Record.Current, formatSeq(replaced.Record.Start), old, deleted)
	return nil
}

// replaceNames is the selection protocol over the listener durable m's record in store, on the
// operator's connection nc, as the listener runs it for its own durable
// (cmd/listener/listener_durable_names.go, durableNames): the durable's own name as each fresh
// name's prefix, its floor the ack floor, and its key DurableKey's.
func replaceNames(nc *nats.Conn, store selection.Store, m member, holdID string) *selection.Names {
	return selection.New(selection.Config{
		Store:  store,
		Server: replaceServer{nc: nc},
		Prefix: func(string) string { return m.Prefix },
		Floor:  func(info *jetstream.ConsumerInfo) uint64 { return info.AckFloor.Stream },
		StreamID: func(ctx context.Context) (string, error) {
			info, err := streamInfo(ctx, nc, notifications)
			if err != nil {
				return "", err
			}
			return selection.StreamIdentity(info), nil
		},
		CheckKey: selection.CheckDurableKey,
		Owner:    "envoy-infra consumer-names replace, hold " + holdID,
	})
}

// replaceServer is the notification stream's consumers on the operator's connection, as the
// selection protocol reads and deletes them (selection.Server).
type replaceServer struct{ nc *nats.Conn }

func (s replaceServer) Info(ctx context.Context, name string) (*jetstream.ConsumerInfo, error) {
	info, err := consumerInfo(ctx, s.nc, notifications, name)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("%w: %s on %s", selection.ErrNotFound, name, notifications)
	}
	return info, nil
}

func (s replaceServer) Delete(ctx context.Context, name string) error {
	return deleteConsumer(ctx, s.nc, notifications, name)
}

// replaceStart is the start of a replace of a consumer whose ack floor is floor, on a stream in
// state: the sequence after the floor, and a start past the sequence after the stream's last at the
// stream's first sequence (at least 1, the sequence an empty stream stores next), the clamp every
// session consumer create applies (internal/bus/session_consumers.go, create and firstStart).
func replaceStart(floor uint64, state jetstream.StreamState) uint64 {
	start := floor + 1
	if start > state.LastSeq+1 {
		start = max(state.FirstSeq, 1)
	}
	return start
}

// replaceCreate is the selection.CreateFunc of the replace's candidate: the current's own
// configuration, base, under the candidate's name, carrying m's metadata, at the start the protocol
// gives by start sequence, corrected as packages/envoy/deploy/README.md's recreation corrected it
// (no idle heartbeat, no flow control, ack explicit), and delivering to a new reply inbox, as the
// listener's own create gives one (cmd/listener/durable.go, ensureFixedDurable), so the candidate
// and the name it replaces never deliver to one subject. A refusal at the stream's max_consumers is
// selection.ErrConsumerLimit; the server's other answers stay *jetstream.APIError, which the
// protocol reads as answered, and any other error is a create whose outcome it settles with an
// info.
func replaceCreate(nc *nats.Conn, base jetstream.ConsumerConfig, m member) selection.CreateFunc {
	return func(ctx context.Context, name string, start selection.Start) (*jetstream.ConsumerInfo, error) {
		config := base
		config.Name, config.Durable = name, name
		config.Metadata = m.Metadata()
		config.DeliverSubject = nats.NewInbox()
		config.IdleHeartbeat, config.FlowControl = 0, false
		config.AckPolicy = jetstream.AckExplicitPolicy
		config.DeliverPolicy, config.OptStartSeq, config.OptStartTime = jetstream.DeliverByStartSequencePolicy, start.Seq, nil
		info, err := writeConsumer(ctx, nc, notifications, config, actionCreateConsumer)
		var api *jetstream.APIError
		if errors.As(err, &api) && api.ErrorCode == jetstream.JSErrCodeMaximumConsumersLimit {
			return nil, fmt.Errorf("%w: %w", selection.ErrConsumerLimit, err)
		}
		return info, err
	}
}

// replaceDeleteRetired deletes each retired name of record, but its current, that the server still
// holds, and answers how many it deleted. The names stay in the record's retired list, which the
// cleanup command reads.
func replaceDeleteRetired(ctx context.Context, nc *nats.Conn, record selection.Record) (int, error) {
	deleted := 0
	for _, name := range record.Retired {
		if name == record.Current {
			continue
		}
		info, err := consumerInfo(ctx, nc, notifications, name)
		if err != nil {
			return deleted, unknownf(err, "the info of retired %s", name)
		}
		if info == nil {
			continue
		}
		if err := deleteConsumer(ctx, nc, notifications, name); err != nil {
			return deleted, fmt.Errorf("the delete of retired %s failed: %w", name, err)
		}
		deleted++
	}
	return deleted, nil
}
