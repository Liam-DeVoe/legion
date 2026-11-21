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
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
)

// durableNamesTimeout bounds the selection steps of a durable call whose caller gives no deadline:
// the start's check and bind, as the JetStream handle's own wait bounds each of today's reads.
const durableNamesTimeout = 10 * time.Second

// durableServer is the listener's durables on the stream as consumernames.Server, through the
// client's JetStream handle, so a read the grant refuses is bus.ErrPublishDenied as today's is.
type durableServer struct{ client *bus.Client }

func (s durableServer) Info(ctx context.Context, name string) (*jetstream.ConsumerInfo, error) {
	info, err := s.client.JS().ConsumerInfo(bus.Stream, name, nats.Context(ctx))
	if errors.Is(err, nats.ErrConsumerNotFound) {
		return nil, fmt.Errorf("%w: %w", consumernames.ErrNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	return protocolInfo(info), nil
}

func (s durableServer) Delete(ctx context.Context, name string) error {
	err := s.client.JS().DeleteConsumer(bus.Stream, name, nats.Context(ctx))
	if errors.Is(err, nats.ErrConsumerNotFound) {
		return nil
	}
	return err
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

// durableNames is the protocol over the listener's durable named durable, on client.
func durableNames(client *bus.Client, durable string) *consumernames.Names {
	host, _ := os.Hostname()
	return consumernames.New(consumernames.Config{
		Store:  client.SelectionStore(),
		Server: durableServer{client},
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
		Metadata: func(string) map[string]string { return map[string]string{consumernames.LogicalMetadataKey: durable} },
		Owner:    fmt.Sprintf("%s/%s/%d", durable, host, os.Getpid()),
		// Every setting the durable's create gives that no correction reaches, which the server's
		// own answer shows (bus.Client.ConsumerAdoptable): a push consumer delivering to a reply
		// inbox, explicit acks, instant replay, whole messages and no idle heartbeat among them. Its
		// start Names checks, and its filters and the settings an update changes are corrected in
		// place before the bind (ensureFreshDurable). A refusal is the listener's refusal of the
		// durable (errListenerDurableRefused), since no retry gets past a candidate only an
		// operator can repair.
		Adopt: func(ctx context.Context, _ string, adopted *jetstream.ConsumerInfo) error {
			err := client.ConsumerAdoptable(ctx, adopted.Name, true)
			if errors.Is(err, consumernames.ErrUnexpectedConsumer) {
				return fmt.Errorf("%w: %w", errListenerDurableRefused, err)
			}
			return err
		},
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
	if client.ConsumerNames() == consumernames.ModeFresh {
		names := durableNames(client, durable)
		key := consumernames.DurableKey(durable)
		sel, err := names.Resolve(ctx, key)
		if err != nil {
			return nil, err
		}
		if sel.Record.Current == "" {
			return nil, fmt.Errorf("%w: the selection of %s names no current", nats.ErrConsumerNotFound, durable)
		}
		info, err := client.JS().ConsumerInfo(bus.Stream, sel.Record.Current, nats.Context(ctx))
		if err != nil {
			return nil, err
		}
		if err := names.Owns(key, protocolInfo(info)); err != nil {
			return nil, err
		}
		return info, nil
	}
	return client.JS().ConsumerInfo(bus.Stream, durable, nats.Context(ctx))
}

// ensureFreshDurable is startListenerSubscription's create in the fresh mode: the durable's current
// through its record, its policy corrected in place as today's is, or a fresh <durable>~<nuid> at the
// record's start, and for a durable with no record at deliver policy all, today's first create. A
// current the server no longer holds is created again at the start its record proves. A current
// Names adopted rather than this call created (Step 0, a create whose outcome was unknown) is
// corrected in place like any other before it is bound. It answers the physical name to bind.
func ensureFreshDurable(ctx context.Context, client *bus.Client, durable string, subjects []string) (string, error) {
	ctx, cancel := boundedContext(ctx)
	defer cancel()
	names := durableNames(client, durable)
	key := consumernames.DurableKey(durable)
	var createdHere string
	sel, created, err := names.Ensure(ctx, key, consumernames.CreateRequest{
		Create: func(ctx context.Context, name string, start consumernames.Start) (*jetstream.ConsumerInfo, error) {
			config := listenerDurableConfig(name, subjects)
			config.Metadata = map[string]string{consumernames.LogicalMetadataKey: durable}
			switch start.Native {
			case consumernames.NativeAll:
				config.DeliverPolicy = nats.DeliverAllPolicy
			case consumernames.NativeNew:
				config.DeliverPolicy = nats.DeliverNewPolicy
			default:
				config.DeliverPolicy, config.OptStartSeq = nats.DeliverByStartSequencePolicy, start.Seq
			}
			info, err := client.JS().AddConsumer(bus.Stream, &config, nats.Context(ctx))
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
		return "", err
	}
	current := sel.Record.Current
	if created && current == createdHere {
		return current, nil
	}
	info, err := client.JS().ConsumerInfo(bus.Stream, current, nats.Context(ctx))
	if err != nil {
		return "", err
	}
	if err := names.Owns(key, protocolInfo(info)); err != nil {
		return "", err
	}
	if err := correctListenerDurable(client, info, subjects, nats.Context(ctx)); err != nil {
		return "", err
	}
	return current, nil
}
