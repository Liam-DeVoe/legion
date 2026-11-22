// The listener's own durable in the fresh consumer naming mode: a fourth family of the selection
// record. Its logical name is the durable's, bus.ListenerDurable(machine id); each creation gets the
// physical name <durable>~<nuid> (consumernames.FreshName); and its record is kept in the session
// consumers' selection bucket under consumernames.DurableKey(durable), a key no session's is. In the
// fixed mode, the default, the durable is named as before and nothing here runs.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
)

// durableNamesTimeout bounds the selection steps of a durable call whose caller gives no deadline:
// the start's check and bind, as the JetStream handle's own wait bounds each of today's reads.
const durableNamesTimeout = 10 * time.Second

// durableServer is the listener's durables on the stream as consumernames.Server, each call on a
// connection its generation is granted on (durableConns): a generation's own intake connection
// where the listener holds one, else a cleanup connection opened for the call. A read the grant
// refuses is bus.ErrPublishDenied as today's is.
type durableServer struct {
	conns *durableConns
}

func (s *durableServer) Info(ctx context.Context, name string) (*jetstream.ConsumerInfo, error) {
	conn, done, err := s.conn(ctx, name)
	if err != nil {
		return nil, err
	}
	defer done()
	info, err := conn.JS().ConsumerInfo(bus.Stream, name, nats.Context(ctx))
	if errors.Is(err, nats.ErrConsumerNotFound) {
		return nil, fmt.Errorf("%w: %w", consumernames.ErrNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	return protocolInfo(info), nil
}

func (s *durableServer) Delete(ctx context.Context, name string) error {
	conn, err := s.conns.cleanup(ctx, name)
	if err != nil {
		return err
	}
	defer conn.Close()
	err = conn.JS().DeleteConsumer(bus.Stream, name, nats.Context(ctx))
	if errors.Is(err, nats.ErrConsumerNotFound) {
		return nil
	}
	return err
}

// conn is a connection granted name's info, and what to call once the call is done: the intake
// connection the listener holds for name, or a cleanup connection, closed when done.
func (s *durableServer) conn(ctx context.Context, name string) (*bus.Client, func(), error) {
	if held := s.conns.held(name); held != nil {
		return held, func() {}, nil
	}
	conn, err := s.conns.cleanup(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	return conn, conn.Close, nil
}

// protocolInfo is the part of info the protocol reads (consumernames.Names): the name, the start
// policy and sequence, the metadata and the ack floor.
func protocolInfo(info *nats.ConsumerInfo) *jetstream.ConsumerInfo {
	out := &jetstream.ConsumerInfo{Name: info.Name, Created: info.Created, NumAckPending: info.NumAckPending}
	out.Config.Durable = info.Config.Durable
	// nats.DeliverPolicy and jetstream.DeliverPolicy number the policies alike.
	out.Config.DeliverPolicy = jetstream.DeliverPolicy(info.Config.DeliverPolicy)
	out.Config.OptStartSeq = info.Config.OptStartSeq
	out.Config.Metadata = info.Config.Metadata
	out.AckFloor.Stream = info.AckFloor.Stream
	return out
}

// durableNames is the protocol over the listener's durable named durable, on client: its record in
// the client's selection store, each generation's calls on its own connection (durableConns), and
// the adapter that serves it. Its naming and metadata are the family table's for a durable's key
// (consumernames.DurableNaming), the ones an operator's reader judges the record and the consumer
// by (consumernames.CheckFamily, consumernames.OwnsFamily): Prefix(DurableKey(durable)) is durable.
func durableNames(client *bus.Client, durable string) (*consumernames.Names, *durableServer) {
	host, _ := os.Hostname()
	conns := connsOf(client, durable)
	server := &durableServer{conns: conns}
	naming := consumernames.DurableNaming()
	return consumernames.New(consumernames.Config{
		Store:  client.SelectionStore(),
		Server: server,
		Prefix: naming.Prefix,
		Fixed:  naming.Fixed,
		Floor:  func(info *jetstream.ConsumerInfo) uint64 { return info.AckFloor.Stream },
		StreamID: func(ctx context.Context) (string, error) {
			info, err := client.JS().StreamInfo(bus.Stream, nats.Context(ctx))
			if err != nil {
				return "", err
			}
			return info.Config.Metadata[bus.StreamIDMetadataKey], nil
		},
		CheckKey: consumernames.CheckDurableKey,
		Metadata: naming.Metadata,
		Owner:    fmt.Sprintf("%s/%s/%d", durable, host, os.Getpid()),
		Reserved: conns.reserved,
		// The candidate as the server answers it, against the whole configuration the durable's
		// create gives (durableCreateConfig, bus.Client.ConsumerAdoptable): one that differs in a
		// setting no update changes, a push consumer delivering to a reply inbox, explicit acks,
		// instant replay and no idle heartbeat among them, is refused. Its start Names checks, and
		// every setting an update changes is corrected in place before the bind
		// (ensureFreshDurable). A refusal is the listener's refusal of the durable
		// (errListenerDurableRefused), since no retry gets past a candidate only an operator can
		// repair. It reads on the candidate's own connection, the one its generation is granted on.
		Adopt: func(ctx context.Context, _ string, adopted *jetstream.ConsumerInfo) error {
			conn, err := conns.intake(ctx, adopted.Name)
			if err != nil {
				return err
			}
			create, err := jetstreamConfig(durableCreateConfig(adopted.Name, durable, nil))
			if err != nil {
				return err
			}
			err = conn.ConsumerAdoptable(ctx, adopted.Name, create)
			if errors.Is(err, consumernames.ErrUnexpectedConsumer) {
				return fmt.Errorf("%w: %w", errListenerDurableRefused, err)
			}
			return err
		},
	}), server
}

// durableMetadata is the metadata every fresh-mode consumer of the listener's durable named durable
// carries, which its create writes (durableCreateConfig): the family table's for its key
// (consumernames.DurableNaming), the one the owner check requires, so a key added to one is in the
// other.
func durableMetadata(durable string) map[string]string {
	return consumernames.DurableNaming().Metadata(consumernames.DurableKey(durable))
}

// durableCreateConfig is the whole configuration the fresh mode creates consumer name of the
// listener's durable named durable with, on subjects, but its start: listenerDurableConfig's,
// carrying the durable's metadata. The create sends it with its start (ensureFreshDurable), the
// adoption check compares a candidate with it, and the correction writes it (jetstreamConfig,
// bus.CorrectedConfig).
func durableCreateConfig(name, durable string, subjects []string) nats.ConsumerConfig {
	config := listenerDurableConfig(name, subjects)
	config.Metadata = durableMetadata(durable)
	return config
}

// jetstreamConfig is config as nats.go's jetstream package types it, which the adoption check and
// the correction read (bus.Client.ConsumerAdoptable, bus.CorrectedConfig): both types are the
// server's JSON of a consumer's configuration, so config's JSON decodes as it, every setting
// config holds kept.
func jetstreamConfig(config nats.ConsumerConfig) (jetstream.ConsumerConfig, error) {
	var out jetstream.ConsumerConfig
	data, err := json.Marshal(config)
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	if err != nil {
		return jetstream.ConsumerConfig{}, fmt.Errorf("listener durable %s's configuration as the jetstream package types it: %w", config.Durable, err)
	}
	return out, nil
}

// boundedContext is ctx with durableNamesTimeout when it carries no deadline.
func boundedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, durableNamesTimeout)
}

// durableInfo reads the listener's durable named durable: in the fixed mode the consumer of that
// name, in the fresh mode the current its record names, read through Step 0 (an operation left
// open is settled first) and never created here. A durable there is none of, a record that selects
// none included, is nats.ErrConsumerNotFound, so every reader of today's sees what it saw for a
// missing durable; a refused or missing selection state is its own error. Every reader of the
// durable reads it here: the start's check (listenerDurable), the monitor's probe (durableProbe),
// the bound wait (awaitDurableBound) and /healthz.
func durableInfo(ctx context.Context, client *bus.Client, durable string) (*nats.ConsumerInfo, error) {
	ctx, cancel := boundedContext(ctx)
	defer cancel()
	if client.ConsumerNames() != consumernames.ModeFresh {
		return client.JS().ConsumerInfo(bus.Stream, durable, nats.Context(ctx))
	}
	names, server := durableNames(client, durable)
	key := consumernames.DurableKey(durable)
	sel, err := names.Resolve(ctx, key)
	if err != nil {
		return nil, err
	}
	if sel.Record.Current == "" {
		return nil, fmt.Errorf("%w: the selection of %s names no current", nats.ErrConsumerNotFound, durable)
	}
	info, err := ownedDurable(ctx, names, server, key, sel.Record.Current)
	if err != nil {
		return nil, err
	}
	return natsInfo(info)
}

// ownedDurable reads the listener's durable consumer name, which key's record names, as the server
// answers it (bus.Client.ConsumerInfo: every setting nats.go's jetstream package reads, the pause
// among them), and answers it only when the consumer's own metadata names the durable
// (Names.Owns): a read of the durable that a reader then acts on, to report, correct or bind it, is
// never another owner's consumer. Both of the durable's readers through its record use it
// (durableInfo, ensureFreshDurable); Names reads the protocol's part of it itself (protocolInfo).
// The read goes on the connection the listener holds for name, or one opened for this read alone,
// so none outlives a rebind that replaced its generation. A consumer the server does not hold is
// nats.ErrConsumerNotFound, as today's read answers it.
func ownedDurable(ctx context.Context, names *consumernames.Names, server *durableServer, key, name string) (*jetstream.ConsumerInfo, error) {
	conn, done, err := server.conn(ctx, name)
	if err != nil {
		return nil, err
	}
	defer done()
	info, err := conn.ConsumerInfo(ctx, name)
	var refused *jetstream.APIError
	if errors.As(err, &refused) && refused.ErrorCode == jetstream.JSErrCodeConsumerNotFound {
		return nil, fmt.Errorf("%w: %w", nats.ErrConsumerNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	if err := names.Owns(key, info); err != nil {
		return nil, err
	}
	return info, nil
}

// natsInfo is info as nats.go's nats package types it, which the durable's readers read: both
// types are the server's JSON of a consumer's info, so info's JSON decodes as it.
func natsInfo(info *jetstream.ConsumerInfo) (*nats.ConsumerInfo, error) {
	var out nats.ConsumerInfo
	data, err := json.Marshal(info)
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	if err != nil {
		return nil, fmt.Errorf("listener durable %s's info as the nats package types it: %w", info.Name, err)
	}
	return &out, nil
}

// ensureFreshDurable is startListenerSubscription's create in the fresh mode: the durable's current
// through its record, brought in place to the whole configuration the durable's create gives, or a
// fresh <durable>~<nuid> at the record's start, and for a durable with no record at deliver policy
// all, today's first create. A current the server no longer holds is created again at the start its
// record proves. A current Names adopted rather than this call created (Step 0, a create whose
// outcome was unknown) is corrected in place like any other before it is bound
// (bus.CorrectedConfig): so one adopted with headers only, or updated so since, delivers only as
// the listener creates it. It answers the physical name to bind and the connection its generation
// is granted on, on which every call on it is made.
func ensureFreshDurable(ctx context.Context, client *bus.Client, durable string, subjects []string) (string, *bus.Client, error) {
	ctx, cancel := boundedContext(ctx)
	defer cancel()
	names, server := durableNames(client, durable)
	conns := server.conns
	key := consumernames.DurableKey(durable)
	var createdHere string
	sel, created, err := names.Ensure(ctx, key, consumernames.CreateRequest{
		Create: func(ctx context.Context, name string, start consumernames.Start) (*jetstream.ConsumerInfo, error) {
			config := durableCreateConfig(name, durable, subjects)
			switch start.Native {
			case consumernames.NativeAll:
				config.DeliverPolicy = nats.DeliverAllPolicy
			case consumernames.NativeNew:
				config.DeliverPolicy = nats.DeliverNewPolicy
			default:
				config.DeliverPolicy, config.OptStartSeq = nats.DeliverByStartSequencePolicy, start.Seq
			}
			conn := conns.held(name)
			if conn == nil {
				return nil, fmt.Errorf("consumer names: no connection is reserved for %s", name)
			}
			info, err := conn.JS().AddConsumer(bus.Stream, &config, nats.Context(ctx))
			var api *nats.APIError
			switch {
			case err == nil:
				createdHere = name
				return protocolInfo(info), nil
			case errors.As(err, &api) && api.ErrorCode == nats.ErrorCode(jetstream.JSErrCodeMaximumConsumersLimit):
				return nil, fmt.Errorf("%w: %w", consumernames.ErrConsumerLimit, err)
			case errors.As(err, &api):
				// The server's answer, not a create whose outcome is unknown.
				return nil, &jetstream.APIError{Code: api.Code, ErrorCode: jetstream.ErrorCode(api.ErrorCode), Description: api.Description}
			}
			return nil, err
		},
		First: func(ctx context.Context) (consumernames.Start, error) {
			info, err := client.JS().StreamInfo(bus.Stream, nats.Context(ctx))
			if err != nil {
				return consumernames.Start{}, err
			}
			return consumernames.Start{Seq: max(info.State.FirstSeq, 1), Native: consumernames.NativeAll}, nil
		},
	})
	if err != nil {
		return "", nil, err
	}
	current := sel.Record.Current
	conn, err := conns.intake(ctx, current)
	if err != nil {
		return "", nil, err
	}
	if created && current == createdHere {
		return current, conn, nil
	}
	info, err := ownedDurable(ctx, names, server, key, current)
	if err != nil {
		return "", nil, err
	}
	create, err := jetstreamConfig(durableCreateConfig(current, durable, subjects))
	if err != nil {
		return "", nil, err
	}
	if corrected, changed := bus.CorrectedConfig(info.Config, create); changed {
		if err := conn.UpdateConsumer(ctx, corrected); err != nil {
			return "", nil, err
		}
	}
	return current, conn, nil
}
