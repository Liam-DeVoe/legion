//go:build natslive

// The container-backed half of the rig, built only under the natslive tag. Envoy's tests run a
// nats-server in the test process (nats.go), which needs no container runtime; what is here runs
// a published NATS image instead, which the natslive lane uses to check the rig's helpers against
// the server an operator deploys. Keeping it behind the tag keeps testcontainers out of every
// other build of this package, and the guard in nats_test.go keeps it out of the rest of the
// module.

package testnats

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/wait"
)

// imageConfigPath is where StartImageConfig puts a server's configuration: the path the alpine
// images' default command reads its configuration from.
const imageConfigPath = "/etc/nats/nats-server.conf"

// StartImageConfig runs image as a NATS test container whose whole configuration is config, at
// /etc/nats/nats-server.conf, with the arguments -c /etc/nats/nats-server.conf and then serverArgs
// (so -js for JetStream), removed when the test ends (even when its start fails). It returns the
// container with its client URL once the server answers a client with no credential (awaitAuth):
// with anonymous false, by refusing it; with anonymous true, by admitting it, as a configuration
// naming a no_auth_user makes it. Both official image variants take arguments that start with a
// flag as nats-server's own.
func StartImageConfig(t testing.TB, image, config string, serverArgs []string, anonymous bool) (*tcnats.NATSContainer, string) {
	t.Helper()
	ctr, uri := startImage(t, image, config, serverArgs)
	awaitAuth(t, uri, anonymous)
	return ctr, uri
}

// imagePorts are the ports the NATS module exposes: client, route and monitoring.
var imagePorts = []nat.Port{"4222/tcp", "6222/tcp", "8222/tcp"}

// startImage runs the container StartImageConfig runs and returns it with its client URL, before
// any wait for the server to answer a client: each caller waits the way its configuration admits.
// Docker publishes each of the container's ports on a loopback address of the container's own
// (NewHost), as every server of this package listens on one, at a port Docker chooses; the URL
// names that address (ImageEndpoint). The start waits for the client port inside the container
// only: the module's wait dials the Docker host's own name, "localhost", where nothing is
// published.
func startImage(t testing.TB, image, config string, serverArgs []string) (*tcnats.NATSContainer, string) {
	t.Helper()
	ctx := context.Background()
	host := NewHost()
	ctr, err := tcnats.Run(ctx, image,
		testcontainers.WithCmd(append([]string{"-c", imageConfigPath}, serverArgs...)...),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader:            strings.NewReader(config),
			ContainerFilePath: imageConfigPath,
			FileMode:          0o644,
		}),
		testcontainers.WithHostConfigModifier(func(hostConfig *container.HostConfig) {
			// One CPU and 512 MiB, as every container the repository's tests and proofs start is
			// capped on a shared host: a test server holds a few small streams.
			hostConfig.NanoCPUs = 1_000_000_000
			hostConfig.Memory = 512 << 20
			hostConfig.PortBindings = nat.PortMap{}
			for _, port := range imagePorts {
				hostConfig.PortBindings[port] = []nat.PortBinding{{HostIP: host}}
			}
		}),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("4222/tcp").SkipExternalCheck()),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start NATS on %s: %v", image, err)
	}
	return ctr, ImageEndpoint(t, ctr, "4222/tcp", "nats")
}

// Inspected is a container ImageEndpoint can read the published ports of: every container
// StartImageConfig returns, and a test's narrower view of one.
type Inspected interface {
	Inspect(ctx context.Context) (*container.InspectResponse, error)
}

// ImageEndpoint is scheme://host:port of port, one of the container's ports (imagePorts), as Docker
// published it: on the container's own loopback address (startImage), at the port Docker gave it
// at the container's last start. A restart can give it another, so a test reads this again after
// one, in place of the module's ConnectionString and PortEndpoint, which name "localhost".
func ImageEndpoint(t testing.TB, ctr Inspected, port nat.Port, scheme string) string {
	t.Helper()
	inspect, err := ctr.Inspect(context.Background())
	if err != nil {
		t.Fatalf("inspect the NATS container: %v", err)
	}
	bindings := inspect.NetworkSettings.Ports[port]
	if len(bindings) == 0 {
		t.Fatalf("the NATS container publishes no %s", port)
	}
	return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(bindings[0].HostIP, bindings[0].HostPort))
}
