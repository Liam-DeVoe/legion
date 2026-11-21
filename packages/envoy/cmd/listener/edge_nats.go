package main

// edge_nats.go is what an edge listener does differently on NATS, so that it runs under the edge's
// grant (natsgrant.Edge) and asks the server for nothing that grant leaves out: every connection
// names its inboxes under the edge's own prefix, the main client reads the stream and never
// creates or changes it, and the start opens no role lane and publishes no Legion wake report. Its
// delivery handlers keep no role message and hand no kept lane over, and publish a delivery
// exception only on a lane the grant holds (edgeGrant).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/config"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/natsgrant"
)

// edgeMode reports whether cfg is an edge listener's: one that serves the projects
// ENVOY_EDGE_PROJECTS names (config.Service.EdgeProjects).
func edgeMode(cfg config.Service) bool { return len(cfg.EdgeProjects) > 0 }

// listenerConnectOptions are the options every NATS connection of the listener takes, the main
// client's and the session consumers' alike: in edge mode the edge's inbox prefix for its machine
// (natsgrant.EdgeInboxPrefix), which is the one inbox family its grant subscribes, and none
// otherwise.
func listenerConnectOptions(cfg config.Service) []bus.ConnectOption {
	if !edgeMode(cfg) {
		return nil
	}
	return []bus.ConnectOption{bus.WithInboxPrefix(natsgrant.EdgeInboxPrefix(cfg.MachineID))}
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

// edgeGrant is an edge listener's own NATS grant (natsgrant.Edge) as the listener holds itself to
// it, beside the server's refusal of every subject the grant leaves out: the subjects it publishes,
// and the count of the delivery exceptions it withheld because their lane is not among them
// (withholds). Only an edge listener holds one (listenerDeliveryHandlerConfig.edge).
type edgeGrant struct {
	// publish is natsgrant.Edge(machine id, ENVOY_EDGE_PROJECTS).Publish.
	publish []string
	// withheld is envoy_edge_exceptions_withheld_total{topic_prefix, reason}.
	withheld *metrics.Counter
}

// newEdgeGrant is the grant of the edge listener of machineID serving projects, counting what it
// withholds on met. It refuses what natsgrant.Edge refuses: a project list config.Load would not
// have read.
func newEdgeGrant(machineID string, projects []string, met *metrics.Registry) (*edgeGrant, error) {
	grant, err := natsgrant.Edge(machineID, projects)
	if err != nil {
		return nil, fmt.Errorf("edge mode: the edge listener's grant: %w", err)
	}
	return &edgeGrant{
		publish: grant.Publish,
		withheld: met.NewCounter("envoy_edge_exceptions_withheld_total",
			"Delivery exceptions the edge listener did not publish because its NATS grant holds no publish of their lane, by the topic prefix of the message each reports and its reason; the message is finalized as if the exception had gone out"),
	}, nil
}

// withholds reports whether the edge withholds item's delivery exception for reason: whether the
// exception's lane (bus.ExceptionTopicPrefix followed by item.Topic) is under none of its grant's
// publish entries. The grant publishes the exception lanes of the agent subjects and of its
// projects' role subjects, so what it withholds is the report of a fan-out refusal on any other
// topic a session follows - a GitHub or Dispatch event whose delivery mode the session does not
// advertise, or that no one can read (fanoutRefusalOutcome) - and the refusal of a role topic that
// names none of its projects. The server would refuse that publish, and the listener would log
// exceptionPublishFailedLine, which the deployed publish-failure alarm matches, for a lane the
// grant leaves out on purpose. A withheld exception is counted and logged here, and the delivery it
// reports is finalized as it would be had it gone out: the message is acknowledged, undelivered to
// that session. A nil grant, every other listener's, withholds nothing.
func (g *edgeGrant) withholds(logger *logging.Logger, item contracts.Envelope, reason string) bool {
	if g == nil {
		return false
	}
	lane := bus.ExceptionTopicPrefix + item.Topic
	if slices.ContainsFunc(g.publish, func(entry string) bool { return bus.SubjectContains(entry, lane) }) {
		return false
	}
	g.withheld.Inc([2]string{"topic_prefix", metrics.TopicPrefix(item.Topic)}, [2]string{"reason", reason})
	logger.Info("edge listener withheld a delivery exception its grant does not publish",
		slog.String("topic", item.Topic), slog.String("event_id", item.EventID), slog.String("reason", reason))
	return true
}
