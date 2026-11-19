package testnats

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"runtime"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
)

// newHost is the loopback address a container this package starts has Docker publish its ports
// on: on Linux, where every address of 127.0.0.0/8 is the loopback, one at random from 127.1.0.1
// through 127.254.255.254 that no earlier container of the process had (hosts); 127.0.0.1
// elsewhere, the only loopback address such a system brings up by default. It is envoy's
// testnats.NewHost, kept here because the daemon's module does not import envoy's test packages.
//
// A client of an earlier test keeps dialing the address its own server had - one that connects
// with ReconnectForever (internal/daemon's workflow) and outlives its test, as a failed assertion
// before its close leaves it - and the kernel can give that server's port to a later container.
// Published on every interface (Docker's default), the later container answers that client, and
// whatever it counts or refuses, the stray's requests are among it. On an address no earlier server
// of the process had it is out of the stray's reach, whatever port it gets.
func newHost() string {
	if runtime.GOOS != "linux" {
		return "127.0.0.1"
	}
	hostsMu.Lock()
	defer hostsMu.Unlock()
	for {
		host := fmt.Sprintf("127.%d.%d.%d", 1+rand.IntN(254), rand.IntN(256), 1+rand.IntN(254))
		if !hosts[host] {
			hosts[host] = true
			return host
		}
	}
}

// hosts is every address newHost has given in this process, kept for the process's life: an
// address is never given twice, so a client of an ended test never reaches a later container.
var (
	hostsMu sync.Mutex
	hosts   = map[string]bool{}
)

// containerPorts are the ports the NATS module exposes: client, route and monitoring.
var containerPorts = []nat.Port{"4222/tcp", "6222/tcp", "8222/tcp"}

// publishHost is the customizer that has Docker publish a new container's ports on a loopback
// address of its own (newHost), and that address, when the Docker daemon is this machine; it
// leaves Docker's default of every interface otherwise, with no address: a daemon DOCKER_HOST
// names on another machine publishes on a machine whose loopback this one does not reach. The
// daemon is this machine when testcontainers places it at "localhost" (a unix socket, outside a
// container) or at a loopback address.
func publishHost(ctx context.Context) (testcontainers.ContainerCustomizer, string, error) {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return nil, "", err
	}
	defer provider.Close()
	daemon, err := provider.DaemonHost(ctx)
	if err != nil {
		return nil, "", err
	}
	ip := net.ParseIP(daemon)
	if daemon != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return testcontainers.CustomizeRequestOption(func(*testcontainers.GenericContainerRequest) error { return nil }), "", nil
	}
	host := newHost()
	return testcontainers.WithHostConfigModifier(func(hostConfig *container.HostConfig) {
		hostConfig.PortBindings = nat.PortMap{}
		for _, port := range containerPorts {
			hostConfig.PortBindings[port] = []nat.PortBinding{{HostIP: host}}
		}
	}), host, nil
}

// endpoint is scheme://host:port of port, one of a container's ports (containerPorts), as Docker
// published it: on the container's own loopback address where publishHost gave it one, and
// otherwise at the daemon's host. It replaces the module's ConnectionString and MappedPort, which
// name the daemon's host whatever address the port is published on.
func endpoint(ctx context.Context, ctr testcontainers.Container, port nat.Port, scheme string) (string, error) {
	inspect, err := ctr.Inspect(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect the NATS container: %w", err)
	}
	bindings := inspect.NetworkSettings.Ports[port]
	if len(bindings) == 0 {
		return "", fmt.Errorf("the NATS container publishes no %s", port)
	}
	host := bindings[0].HostIP
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		if host, err = ctr.Host(ctx); err != nil {
			return "", fmt.Errorf("read the Docker daemon's host: %w", err)
		}
	}
	return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, bindings[0].HostPort)), nil
}
