// The container half of the rig. Every other helper of this package runs its server in the test
// process; StartImage and StartOpenImage run a published NATS image instead, for the tests whose
// subject is those images themselves: internal/daemon's TestDaemonPermissionsOnRealServers, which
// runs the daemon's grant on each image of testnatsimages.Images and reads the server's own log,
// and internal/intake's TestOpenConsumersRefusesAServerThatSkipsAFilter, which runs SkippingImage,
// a release no in-process server can be. Where no container runtime answers, those tests and the
// tests of this file's helpers skip, and fail on GitHub Actions, whose test job has one
// (skipWithoutContainerRuntime).

package testnats

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"

	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// SkippingImage is ghcr.io/cybermonklabs/legion/ci, the copy of nats:2.10 (.github/ci-images.yaml),
// whose v2.10.29 can skip a message on a consumer filtering on a wildcard and a literal at the same
// token. The daemon refuses such a server (envoy's natsversion.Refusal, intake.OpenConsumers), and
// the one test that shows it refusing one starts this image (StartOpenImage).
const SkippingImage = "ghcr.io/cybermonklabs/legion/ci@sha256:5498ba57b9471840be3d15b033a4eec554d1c02fa6c2cc0ca2d888637f6c6e2f"

// StartImage runs image as a NATS container with JetStream that admits users alone, each with its
// permissions (the configuration StartNkeyAuthorizedUsers gives an in-process server), removed when
// t ends, and returns the container, whose log a test reads, beside its client URL once the server
// refuses a client with no credential (answering).
func StartImage(t testing.TB, image string, users ...NkeyUser) (*tcnats.NATSContainer, string) {
	t.Helper()
	skipWithoutContainerRuntime(t)
	started, err := startContainer(testwait.Context(t), image, tcnats.WithConfigFile(strings.NewReader(nkeyConfig(users))))
	if err != nil {
		t.Fatalf("start nkey-authorized NATS on %s: %v", image, err)
	}
	testcontainers.CleanupContainer(t, started)
	url, err := started.ConnectionString(testwait.Context(t))
	if err != nil {
		t.Fatalf("NATS connection string: %v", err)
	}
	answering(t, url)
	return started, url
}

// StartOpenImage runs image as a NATS container with JetStream and no users (the NATS module's own
// -js), so it admits any client, removed when t ends, and returns its client URL once its JetStream
// API answers. It is for a test of the server itself, such as one the daemon refuses
// (SkippingImage); every other test takes a server of its own in this process through URL.
func StartOpenImage(t testing.TB, image string) string {
	t.Helper()
	skipWithoutContainerRuntime(t)
	started, err := startContainer(testwait.Context(t), image)
	if err != nil {
		t.Fatalf("start NATS on %s: %v", image, err)
	}
	testcontainers.CleanupContainer(t, started)
	url, err := started.ConnectionString(testwait.Context(t))
	if err != nil {
		t.Fatalf("NATS connection string: %v", err)
	}
	testwait.Until(t, fmt.Sprintf("the JetStream API of the NATS server at %s to answer", url), func() (error, bool) {
		conn, err := nats.Connect(url, nats.Name(t.Name()))
		if err != nil {
			return err, false
		}
		defer conn.Close()
		js, err := jetstream.New(conn)
		if err == nil {
			_, err = js.AccountInfo(testwait.Context(t))
		}
		return err, err == nil
	})
	return url
}

// limits caps a NATS test container at one CPU and 512 MiB, as every container the repository's
// tests and proofs start is capped on a shared host: a test server holds a few small streams.
var limits = testcontainers.WithHostConfigModifier(func(hostConfig *container.HostConfig) {
	hostConfig.NanoCPUs = 1_000_000_000
	hostConfig.Memory = 512 << 20
})

// startContainer runs a NATS container of image. A container Docker created and never saw ready (a
// readiness wait that failed) comes back beside the error, and is removed before startContainer
// returns, since the caller registers no removal of a container it was not handed.
func startContainer(ctx context.Context, image string, options ...testcontainers.ContainerCustomizer) (*tcnats.NATSContainer, error) {
	started, err := tcnats.Run(ctx, image, append([]testcontainers.ContainerCustomizer{limits}, options...)...)
	if err != nil {
		return started, errors.Join(err, testcontainers.TerminateContainer(started))
	}
	return started, nil
}

// containerRuntime is the probe's answer for the test binary's life: a machine with no container
// runtime when the first test asks has none for the rest of the run.
var (
	containerRuntimeOnce sync.Once
	containerRuntimeErr  error
)

// skipWithoutContainerRuntime skips t where no container runtime answers, naming the runtime it
// needs, and fails it instead on GitHub Actions, whose test job has Docker: there a skip would hide
// the one run of the test that needs it.
func skipWithoutContainerRuntime(t testing.TB) {
	t.Helper()
	containerRuntimeOnce.Do(func() { containerRuntimeErr = probeContainerRuntime(testwait.Context(t)) })
	if containerRuntimeErr == nil {
		return
	}
	const needs = "this test needs a container runtime: a Docker daemon testcontainers reaches through DOCKER_HOST, a Docker context or a socket it finds on disk"
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Fatalf("%s, and GitHub Actions' test job has one: %v", needs, containerRuntimeErr)
	}
	t.Skipf("%s: %v", needs, containerRuntimeErr)
}

// probeContainerRuntime reports why testcontainers cannot reach a container runtime, or nil when
// one answers. testcontainers has no answer to whether one is there that is not a panic where none
// is: every route to a client runs through its MustExtractDockerHost, which panics ("rootless
// Docker not found") when neither DOCKER_HOST, a Docker context nor a socket on disk names a host,
// and a panic on a test's goroutine ends the whole test binary rather than the test. So the probe
// recovers that panic and reports it as the reason.
func probeContainerRuntime(ctx context.Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("testcontainers found no Docker host: %v", recovered)
		}
	}()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return err
	}
	return provider.Health(ctx)
}
