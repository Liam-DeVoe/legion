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
// Where the Docker daemon is this machine (imageHost), Docker publishes each of the container's
// ports on a loopback address of the container's own (NewHost), as every server of this package
// listens on one; elsewhere on every interface of the daemon's machine, as Docker does by default.
// Either way at a port Docker chooses, and the URL names where it is (ImageEndpoint). The start
// waits for the server's own line saying its client listener is bound, not the module's
// ForListeningPort: that one dials the port at the daemon's host, where a container of its own
// address publishes nothing, and its in-container check runs /bin/sh, which nats:2.10's image does
// not have, so on that image it passed without checking anything. Every caller then waits for the
// server to answer a client (awaitAuth).
func startImage(t testing.TB, image, config string, serverArgs []string) (*tcnats.NATSContainer, string) {
	t.Helper()
	ctx := context.Background()
	host, err := imageHost(ctx)
	if err != nil {
		t.Fatalf("find where Docker publishes a container's ports: %v", err)
	}
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
			if host != "" {
				hostConfig.PortBindings = nat.PortMap{}
				for _, port := range imagePorts {
					hostConfig.PortBindings[port] = []nat.PortBinding{{HostIP: host}}
				}
			}
		}),
		testcontainers.WithWaitStrategy(wait.ForLog(`Listening for client connections on \S+:4222`).AsRegexp()),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start NATS on %s: %v", image, err)
	}
	return ctr, ImageEndpoint(t, ctr, "4222/tcp", "nats")
}

// imageHost is the address startImage has Docker publish a new container's ports on: a loopback
// address of the container's own (NewHost) when the Docker daemon is this machine, so a client
// another test left dialing 127.0.0.1 never reaches it, and "" otherwise, Docker's default of
// every interface. The daemon is this machine when testcontainers places it at "localhost" (a
// unix socket, outside a container) or at a loopback address; a daemon DOCKER_HOST names on
// another machine, or one a container reaches through its gateway, publishes on a machine whose
// loopback this one does not reach.
func imageHost(ctx context.Context) (string, error) {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return "", err
	}
	defer provider.Close()
	daemon, err := provider.DaemonHost(ctx)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(daemon); daemon == "localhost" || (ip != nil && ip.IsLoopback()) {
		return NewHost(), nil
	}
	return "", nil
}

// Inspected is a container ImageEndpoint can read the published ports and daemon host of: every
// container StartImageConfig returns, and a test's narrower view of one.
type Inspected interface {
	Inspect(ctx context.Context) (*container.InspectResponse, error)
	Host(ctx context.Context) (string, error)
}

// ImageEndpoint is scheme://host:port of port, one of the container's ports (imagePorts), as Docker
// published it at the container's last start: on the container's own loopback address where
// startImage gave it one, and otherwise at the daemon's host. A restart can give it another port,
// so a test reads this again after one, in place of the module's ConnectionString and
// PortEndpoint, which name the daemon's host whatever address the port is published on.
func ImageEndpoint(t testing.TB, ctr Inspected, port nat.Port, scheme string) string {
	t.Helper()
	ctx := context.Background()
	inspect, err := ctr.Inspect(ctx)
	if err != nil {
		t.Fatalf("inspect the NATS container: %v", err)
	}
	bindings := inspect.NetworkSettings.Ports[port]
	if len(bindings) == 0 {
		t.Fatalf("the NATS container publishes no %s", port)
	}
	host := bindings[0].HostIP
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		if host, err = ctr.Host(ctx); err != nil {
			t.Fatalf("read the Docker daemon's host: %v", err)
		}
	}
	return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, bindings[0].HostPort))
}
