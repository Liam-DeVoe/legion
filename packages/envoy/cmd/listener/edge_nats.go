package main

// edge_nats.go is what an edge listener does differently on NATS, so that it runs under the edge's
// grant (natsgrant.Edge) and asks the server for nothing that grant leaves out: every connection
// names its inboxes under the edge's own prefix, the main client reads the stream and never
// creates or changes it, and the start opens no role lane and publishes no Legion wake report.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/config"
	"github.com/sjawhar/envoy/natsgrant"
)

// edgeMode reports whether cfg is an edge listener's: one that serves the projects
// ENVOY_EDGE_PROJECTS names (config.Service.EdgeProjects).
func edgeMode(cfg config.Service) bool { return len(cfg.EdgeProjects) > 0 }

// listenerConnectOptions are the options every NATS connection of the listener takes, the main
// client's and the session consumers' alike: in edge mode the edge's inbox prefix
// (natsgrant.EdgeInboxPrefix), which is the one inbox family its grant subscribes, and none
// otherwise.
func listenerConnectOptions(cfg config.Service) []bus.ConnectOption {
	if !edgeMode(cfg) {
		return nil
	}
	return []bus.ConnectOption{bus.WithInboxPrefix(natsgrant.EdgeInboxPrefix)}
}

// connectListenerBus opens the listener's main client. Every listener but an edge one owns the
// stream (bus.ConnectOwningStream, listenerStreamOptions): it creates it when the server has none
// and keeps its configuration. An edge listener's grant holds no create or update of the stream,
// so its client owns none (bus.Connect) and its start reads the stream and refuses, naming it,
// when the server has none (edgeStreamPresent); its buckets are raised as every listener's are,
// at their open, and no keeper raises them after.
func connectListenerBus(cfg config.Service) (*bus.Client, error) {
	if !edgeMode(cfg) {
		return bus.ConnectOwningStream(cfg.NATSURLs, listenerStreamOptions(cfg)...)
	}
	client, err := bus.Connect(cfg.NATSURLs, append(listenerConnectOptions(cfg), bus.WithSilentCutDetection())...)
	if err != nil {
		return nil, err
	}
	if err := edgeStreamPresent(client, edgeStreamRead); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// edgeStreamRead bounds an edge listener's read of the stream at its start.
const edgeStreamRead = 10 * time.Second

// edgeStreamPresent reads the stream's info within timeout and answers nil when the server holds
// it. A stream the server does not hold is refused naming it: an edge listener may not create one
// (natsgrant.Edge), and a shared listener or Dispatch, whose grants may, creates it. A read that
// failed otherwise is refused naming the stream and the connection's last error, which is the
// server's refusal when the grant did not let the listener read the answer: a subscription to its
// own inboxes refused names the inbox prefix there.
func edgeStreamPresent(client *bus.Client, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := client.JS().StreamInfo(bus.Stream, nats.Context(ctx))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, nats.ErrStreamNotFound):
		return fmt.Errorf("edge mode: the server holds no %s stream, and an edge listener may not create it: a shared listener or Dispatch creates it", bus.Stream)
	}
	if last := client.Connection().LastError(); last != nil {
		return fmt.Errorf("edge mode: read the %s stream: %w (the connection's last error: %v)", bus.Stream, err, last)
	}
	return fmt.Errorf("edge mode: read the %s stream: %w", bus.Stream, err)
}
