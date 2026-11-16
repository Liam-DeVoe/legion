package testnats

import (
	"errors"
	"strings"
	"testing"
)

// The fixture reaches the Docker daemon the docker CLI reaches, and dials a published port on that
// daemon's machine: DOCKER_HOST when it is set, with no read of the current context, and otherwise
// the current context's endpoint. A daemon on this machine publishes on 127.0.0.1 alone and is
// dialled there; one on another machine publishes on every interface and is dialled at its host.
func TestTheDaemonHostIsTheOneTheDockerCLIReaches(t *testing.T) {
	local := dockerDaemon{dial: "127.0.0.1", bind: "127.0.0.1"}
	for _, tc := range []struct {
		name, dockerHost, context string
		want                      dockerDaemon
		address                   string
	}{
		{"the default context's socket", "", "unix:///var/run/docker.sock", local, "127.0.0.1:32768"},
		{"a rootless socket in DOCKER_HOST", "unix:///run/user/1000/docker.sock", "", local, "127.0.0.1:32768"},
		{"a context on another machine", "", "tcp://10.9.8.7:2376", dockerDaemon{dial: "10.9.8.7"}, "10.9.8.7:32768"},
		{"DOCKER_HOST on another machine", "tcp://10.1.2.3:2376", "", dockerDaemon{dial: "10.1.2.3"}, "10.1.2.3:32768"},
		{"DOCKER_HOST over ssh", "ssh://builder@docker.example:22", "", dockerDaemon{dial: "docker.example"}, "docker.example:32768"},
		{"DOCKER_HOST at an IPv6 host", "tcp://[fd00::1]:2376", "", dockerDaemon{dial: "fd00::1"}, "[fd00::1]:32768"},
		{"DOCKER_HOST at localhost over tcp", "tcp://localhost:2375", "", local, "127.0.0.1:32768"},
		{"DOCKER_HOST at a loopback address", "tcp://127.0.0.1:2375", "", local, "127.0.0.1:32768"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := daemonHost(tc.dockerHost, func() (string, error) {
				if tc.dockerHost != "" {
					t.Errorf("the current context was read although DOCKER_HOST is %q, which the docker CLI reads first", tc.dockerHost)
				}
				return tc.context, nil
			})
			if err != nil {
				t.Fatalf("daemonHost = %v", err)
			}
			if got != tc.want {
				t.Errorf("daemonHost = %+v, want %+v", got, tc.want)
			}
			if address := got.address("32768"); address != tc.address {
				t.Errorf("address = %q, want %q", address, tc.address)
			}
		})
	}

	for _, tc := range []struct {
		name, dockerHost, context string
		contextErr                error
		want                      string
	}{
		{"no current context to read", "", "", errors.New("docker: not found"), "read the docker CLI's current context"},
		{"an endpoint with no host", "tcp://:2375", "", nil, "names no host"},
		{"an endpoint of no scheme the CLI takes", "fd://", "", nil, "is not unix://"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := daemonHost(tc.dockerHost, func() (string, error) { return tc.context, tc.contextErr })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("daemonHost = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// The publish flag binds the daemon's loopback address for a daemon on this machine, and no
// address, so every interface, for one on another machine, whose ports this process dials there.
func TestThePublishFlagFollowsTheDaemonsMachine(t *testing.T) {
	if got := (dockerDaemon{dial: "127.0.0.1", bind: "127.0.0.1"}).publish(clientPort); got != "127.0.0.1::4222/tcp" {
		t.Errorf("publish on this machine = %q, want 127.0.0.1::4222/tcp", got)
	}
	if got := (dockerDaemon{dial: "10.1.2.3"}).publish(clientPort); got != "4222/tcp" {
		t.Errorf("publish on another machine = %q, want 4222/tcp", got)
	}
}
