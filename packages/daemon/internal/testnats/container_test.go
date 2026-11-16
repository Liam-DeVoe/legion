package testnats

import (
	"errors"
	"strings"
	"testing"
)

// The fixture reaches the Docker daemon the docker CLI reaches, and dials a published port on that
// daemon's machine, in the CLI's order: the context DOCKER_CONTEXT names when it is set, over any
// DOCKER_HOST; else DOCKER_HOST, with no read of a context; else the current context's endpoint. A
// daemon on this machine publishes on 127.0.0.1 alone and is dialled there; one on another machine
// publishes on every interface and is dialled at its host. The rows where both variables are set
// pick a local daemon for one and a remote one for the other, both ways round, so a helper that
// read them in the wrong order would publish on one machine and dial another.
func TestTheDaemonHostIsTheOneTheDockerCLIReaches(t *testing.T) {
	local := dockerDaemon{dial: "127.0.0.1", bind: "127.0.0.1"}
	contexts := map[string]string{
		"":       "unix:///var/run/docker.sock",
		"remote": "tcp://10.9.8.7:2376",
		"local":  "unix:///run/user/1000/docker.sock",
	}
	for _, tc := range []struct {
		name, dockerContext, dockerHost, current string
		want                                     dockerDaemon
		address                                  string
	}{
		{"the default context's socket", "", "", contexts[""], local, "127.0.0.1:32768"},
		{"a rootless socket in DOCKER_HOST", "", "unix:///run/user/1000/docker.sock", "", local, "127.0.0.1:32768"},
		{"a current context on another machine", "", "", "tcp://10.9.8.7:2376", dockerDaemon{dial: "10.9.8.7"}, "10.9.8.7:32768"},
		{"DOCKER_HOST on another machine", "", "tcp://10.1.2.3:2376", "", dockerDaemon{dial: "10.1.2.3"}, "10.1.2.3:32768"},
		{"DOCKER_HOST over ssh", "", "ssh://builder@docker.example:22", "", dockerDaemon{dial: "docker.example"}, "docker.example:32768"},
		{"DOCKER_HOST at an IPv6 host", "", "tcp://[fd00::1]:2376", "", dockerDaemon{dial: "fd00::1"}, "[fd00::1]:32768"},
		{"DOCKER_HOST at localhost over tcp", "", "tcp://localhost:2375", "", local, "127.0.0.1:32768"},
		{"DOCKER_HOST at a loopback address", "", "tcp://127.0.0.1:2375", "", local, "127.0.0.1:32768"},
		{"a remote DOCKER_CONTEXT over a local DOCKER_HOST", "remote", "unix:///var/run/docker.sock", "", dockerDaemon{dial: "10.9.8.7"}, "10.9.8.7:32768"},
		{"a local DOCKER_CONTEXT over a remote DOCKER_HOST", "local", "tcp://10.1.2.3:2376", "", local, "127.0.0.1:32768"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := daemonHost(tc.dockerContext, tc.dockerHost, func(name string) (string, error) {
				switch {
				case tc.dockerContext != "" && name != tc.dockerContext:
					t.Errorf("the context %q was read although DOCKER_CONTEXT is %q, which the docker CLI reads first", name, tc.dockerContext)
				case tc.dockerContext == "" && tc.dockerHost != "":
					t.Errorf("the context %q was read although DOCKER_HOST is %q and DOCKER_CONTEXT is unset", name, tc.dockerHost)
				case tc.dockerContext == "" && name != "":
					t.Errorf("the context %q was read where the current context is the one", name)
				}
				if name == "" {
					return tc.current, nil
				}
				return contexts[name], nil
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
		name, dockerContext, dockerHost, current string
		contextErr                               error
		want                                     string
	}{
		{"no current context to read", "", "", "", errors.New("docker: not found"), "read the docker CLI's current context"},
		{"a DOCKER_CONTEXT that does not exist", "gone", "", "", errors.New(`context "gone" does not exist`), "read the docker context gone that DOCKER_CONTEXT names"},
		{"an endpoint with no host", "", "tcp://:2375", "", nil, "names no host"},
		{"an endpoint of no scheme the CLI takes", "", "fd://", "", nil, "is not unix://"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := daemonHost(tc.dockerContext, tc.dockerHost, func(string) (string, error) { return tc.current, tc.contextErr })
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
