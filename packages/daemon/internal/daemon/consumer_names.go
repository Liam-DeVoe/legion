package daemon

import (
	"log/slog"

	"github.com/sjawhar/envoy/consumernames"
)

// consumerNamesLine is the boot's line naming how the daemon names its durable consumers
// (config.Config.ConsumerNames, legion.yaml's consumer_names), in its consumer_names field: fixed
// or fresh. The operator's checks read it from the daemon's log while the hub's hold refuses the
// daemon's NATS connect, so run writes it before anything else, and so before any connect.
const consumerNamesLine = "daemon: the durable consumers' naming"

// logConsumerNames writes consumerNamesLine for mode, fixed when the configuration names none.
func logConsumerNames(log *slog.Logger, mode consumernames.Mode) {
	if mode == "" {
		mode = consumernames.ModeFixed
	}
	log.Info(consumerNamesLine, "consumer_names", string(mode))
}
