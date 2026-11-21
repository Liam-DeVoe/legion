// The listener's own durable in the fresh consumer naming mode: a fourth family of the selection
// record. Its logical name is the durable's, bus.ListenerDurable(machine id); each creation gets the
// physical name <durable>~<nuid> (consumernames.FreshName); and its record is kept in the session
// consumers' selection bucket under consumernames.DurableKey(durable), a key no session's is. In the
// fixed mode, the default, the durable is named as before and nothing here runs.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
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
type durableServer struct{ conns *durableConns }

func (s durableServer) Info(ctx context.Context, name string) (*jetstream.ConsumerInfo, error) {
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

func (s durableServer) Delete(ctx context.Context, name string) error {
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
func (s durableServer) conn(ctx context.Context, name string) (*bus.Client, func(), error) {
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
// the client's selection store, each generation's calls on its own connection (durableConns).
func durableNames(client *bus.Client, durable string) *consumernames.Names {
	host, _ := os.Hostname()
	conns := connsOf(client, durable)
	return consumernames.New(consumernames.Config{
		Store:  client.SelectionStore(),
		Server: durableServer{conns},
		Prefix: func(string) string { return durable },
		Floor:  func(info *jetstream.ConsumerInfo) uint64 { return info.AckFloor.Stream },
		StreamID: func(ctx context.Context) (string, error) {
			info, err := client.JS().StreamInfo(bus.Stream, nats.Context(ctx))
			if err != nil {
				return "", err
			}
			return info.Config.Metadata[bus.StreamIDMetadataKey], nil
		},
		CheckKey: consumernames.CheckDurableKey,
		Owner:    fmt.Sprintf("%s/%s/%d", durable, host, os.Getpid()),
		Reserved: conns.reserved,
	})
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
	sel, err := durableNames(client, durable).Resolve(ctx, consumernames.DurableKey(durable))
	if err != nil {
		return nil, err
	}
	if sel.Record.Current == "" {
		return nil, fmt.Errorf("%w: the selection of %s names no current", nats.ErrConsumerNotFound, durable)
	}
	conn, err := connsOf(client, durable).intake(ctx, sel.Record.Current)
	if err != nil {
		return nil, err
	}
	return conn.JS().ConsumerInfo(bus.Stream, sel.Record.Current, nats.Context(ctx))
}

// ensureFreshDurable is startListenerSubscription's create in the fresh mode: the durable's current
// through its record, its policy corrected in place as today's is, or a fresh <durable>~<nuid> at the
// record's start, and for a durable with no record at deliver policy all, today's first create. A
// current the server no longer holds is created again at the start its record proves. It answers
// the physical name to bind and the connection its generation is granted on.
func ensureFreshDurable(ctx context.Context, client *bus.Client, durable string, subjects []string) (string, *bus.Client, error) {
	ctx, cancel := boundedContext(ctx)
	defer cancel()
	conns := connsOf(client, durable)
	sel, created, err := durableNames(client, durable).Ensure(ctx, consumernames.DurableKey(durable), consumernames.CreateRequest{
		Create: func(ctx context.Context, name string, start consumernames.Start) (*jetstream.ConsumerInfo, error) {
			// The deliver subject is a random inbox, as today's create gives it (startListenerSubscription).
			config := nats.ConsumerConfig{
				Durable:        name,
				DeliverSubject: nats.NewInbox(),
				Metadata:       map[string]string{consumernames.LogicalMetadataKey: durable},
			}
			switch start.Native {
			case consumernames.NativeAll:
				config.DeliverPolicy = nats.DeliverAllPolicy
			case consumernames.NativeNew:
				config.DeliverPolicy = nats.DeliverNewPolicy
			default:
				config.DeliverPolicy, config.OptStartSeq = nats.DeliverByStartSequencePolicy, start.Seq
			}
			applyListenerConsumerPolicy(&config, subjects)
			conn := conns.held(name)
			if conn == nil {
				return nil, fmt.Errorf("consumer names: no connection is reserved for %s", name)
			}
			info, err := conn.JS().AddConsumer(bus.Stream, &config, nats.Context(ctx))
			var api *nats.APIError
			switch {
			case err == nil:
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
	if created {
		return current, conn, nil
	}
	info, err := conn.JS().ConsumerInfo(bus.Stream, current, nats.Context(ctx))
	if err != nil {
		return "", nil, err
	}
	if err := listenerDurableRefusal(current, info.Config); err != nil {
		return "", nil, err
	}
	corrected := info.Config
	applyListenerConsumerPolicy(&corrected, subjects)
	if !reflect.DeepEqual(corrected, info.Config) {
		if _, err := conn.JS().UpdateConsumer(bus.Stream, &corrected, nats.Context(ctx)); err != nil {
			return "", nil, err
		}
	}
	return current, conn, nil
}
