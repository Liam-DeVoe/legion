//go:build smoke

package smoke

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// envoyInfraInImage is where the Envoy image ships envoy-infra (docker/Dockerfile), the path a Job
// names as its command in place of the image's ENTRYPOINT, envoy-listener.
const envoyInfraInImage = "/usr/local/bin/envoy-infra"

// TestEnvoyInfraInImage checks that the image ships envoy-infra at envoyInfraInImage as an
// executable, and that it runs there as the image's user: `envoy-infra buckets --help`, through
// PATH as a Job's `command: [envoy-infra, ...]` runs it, exits 0 and prints the buckets usage with
// create's flags. The control is the same exec of a path the image does not hold, which fails, so
// a pass is the binary's.
func TestEnvoyInfraInImage(t *testing.T) {
	image := os.Getenv("ENVOY_SMOKE_IMAGE")
	if image == "" {
		t.Fatal("ENVOY_SMOKE_IMAGE is unset, and this test never builds the image itself; see TestSmoke for the build command.")
	}
	ctx := context.Background()
	container, err := testcontainers.Run(ctx, image,
		testcontainers.WithEntrypoint("sleep", "300"),
		// The image only sleeps while the command prints its usage.
		capped(1, 256),
	)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start the image: %v", err)
	}
	run := func(command ...string) (int, string) {
		t.Helper()
		code, output, err := container.Exec(ctx, command, tcexec.Multiplexed())
		if err != nil {
			t.Fatalf("exec %v in the image: %v", command, err)
		}
		printed, err := io.ReadAll(output)
		if err != nil {
			t.Fatalf("read the output of %v: %v", command, err)
		}
		return code, string(printed)
	}

	if code, printed := run("/usr/local/bin/envoy-infra-missing", "buckets", "--help"); code == 0 {
		t.Fatalf("control: exec of a path the image does not hold = 0, want it to fail: %s", printed)
	}
	if code, printed := run("test", "-x", envoyInfraInImage); code != 0 {
		t.Fatalf("%s is not an executable file in the image (test -x = %d): %s", envoyInfraInImage, code, printed)
	}
	code, printed := run("envoy-infra", "buckets", "--help")
	if code != 0 {
		t.Fatalf("envoy-infra buckets --help in the image = %d, want 0:\n%s", code, printed)
	}
	for _, want := range []string{"usage: envoy-infra buckets create|recreate|check --nats URL", "--inbox-prefix", "--replicas"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("envoy-infra buckets --help in the image does not print %q:\n%s", want, printed)
		}
	}
	t.Logf("envoy-infra buckets --help in the image = 0, %d bytes of usage", len(printed))
}
