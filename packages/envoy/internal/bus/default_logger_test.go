package bus

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/logtest"
)

// A client built without WithLogger wrote each line through slog.Default() as the line was
// written, before the client took a logger, so a program that set the default after it built a
// client found that client's lines where it set them. The logger such a client takes still does:
// the row builds the options first, sets the default after, and reads the line, attributes and
// group included, from the default set last. Serial: it sets the process's default logger.
func TestAClientBuiltWithoutALoggerWritesWhereTheDefaultPointsAtEachLine(t *testing.T) {
	opts := parseConnectOptions([]ConnectOption{})
	var logs logtest.Buffer
	logtest.SetDefault(t, slog.NewJSONHandler(&logs, nil))

	opts.logger.With("client", "the row's").WithGroup("nats").Info("envoy nats connected", "url", "nats://127.0.0.1:4222")
	got := logs.String()
	for _, want := range []string{`"msg":"envoy nats connected"`, `"client":"the row's"`, `"nats":{"url":"nats://127.0.0.1:4222"}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("the default set after the options were built got %q, want it to hold %s", got, want)
		}
	}
	if loggerOwning(nil) != followsDefault {
		t.Fatal("a bucket call on a connection no client owns does not write where the default points at each line")
	}
}
