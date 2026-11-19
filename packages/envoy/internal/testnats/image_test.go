//go:build natslive

package testnats

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// StartImageConfig gives the server config as its whole configuration and serverArgs after it, on
// either image: the server refuses a client with no credential, as config's one nkey user makes it,
// admits that user by its seed, and answers JetStream, which the -js among serverArgs turns on.
func TestStartImageConfigServesTheConfigWithTheArguments(t *testing.T) {
	for _, image := range Images {
		t.Run(image.Name, func(t *testing.T) {
			seed, public := User(t)
			config := fmt.Sprintf("authorization {\n  users: [ { nkey: %q } ]\n}\n", public)
			_, uri := StartImageConfig(t, image.Ref, config, []string{"-js"}, false)
			conn, err := ConnectSeed(t, uri, seed)
			if err != nil {
				t.Fatalf("connect with the seed: %v", err)
			}
			defer conn.Close()
			js, err := conn.JetStream()
			if err != nil {
				t.Fatalf("JetStream context: %v", err)
			}
			waitForJetStream(t, func() error {
				_, err := js.AccountInfo()
				return err
			})
		})
	}
}

// With no serverArgs the server runs without JetStream: StartImageConfig replaces the NATS
// module's own default arguments (-DV -js) rather than adding to them. nats-server enables
// JetStream before it accepts a client (server/server.go Start), so the first answer is final.
func TestStartImageConfigRunsNoArgumentItWasNotGiven(t *testing.T) {
	for _, image := range Images {
		t.Run(image.Name, func(t *testing.T) {
			seed, public := User(t)
			config := fmt.Sprintf("authorization {\n  users: [ { nkey: %q } ]\n}\n", public)
			_, uri := StartImageConfig(t, image.Ref, config, nil, false)
			conn, err := ConnectSeed(t, uri, seed)
			if err != nil {
				t.Fatalf("connect with the seed: %v", err)
			}
			defer conn.Close()
			js, err := conn.JetStream()
			if err != nil {
				t.Fatalf("JetStream context: %v", err)
			}
			if _, err := js.AccountInfo(); !errors.Is(err, natsgo.ErrJetStreamNotEnabled) {
				t.Fatalf("JetStream account info on a server given no -js: %v, want %v", err, natsgo.ErrJetStreamNotEnabled)
			}
		})
	}
}

// With anonymous set, StartImageConfig returns once the server admits a client with no
// credential, as a configuration naming a no_auth_user makes it, and that client stays admitted.
func TestStartImageConfigWaitsForAnAnonymousServerToAdmitAClient(t *testing.T) {
	for _, image := range Images {
		t.Run(image.Name, func(t *testing.T) {
			_, public := User(t)
			config := fmt.Sprintf("authorization {\n  users: [ { nkey: %q } ]\n}\nno_auth_user: %q\n", public, public)
			_, uri := StartImageConfig(t, image.Ref, config, nil, true)
			conn, err := ConnectSeed(t, uri, "")
			if err != nil {
				t.Fatalf("a client with no credential was refused after the wait: %v", err)
			}
			conn.Close()
		})
	}
}

// Where the Docker daemon is this machine, a container's ports are published on a loopback address
// of its own, as every server this package starts listens on one (NewHost): its client and
// monitoring endpoints name that address, never 127.0.0.1, and a dial to 127.0.0.1 at the
// published client port never reaches the container, judged by the server_id its INFO names. Two
// containers have two addresses. Control: the published address answers with an INFO naming a
// server_id, so the same read would see the container at 127.0.0.1 if Docker published it there.
func TestAContainersPortsArePublishedOnAnAddressOfItsOwn(t *testing.T) {
	if host, err := imageHost(t.Context()); err != nil || host == "" {
		t.Skipf("the Docker daemon is not this machine (%v), so Docker publishes on its own default", err)
	}
	// serverID is the server_id of the INFO a dial to address reads first, or the dial's error.
	serverID := func(address string) (string, error) {
		conn, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err != nil {
			return "", err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return "", err
		}
		info, found := strings.CutPrefix(line, "INFO ")
		if !found {
			return "", fmt.Errorf("%s answered %q, not an INFO", address, line)
		}
		var fields struct {
			ID string `json:"server_id"`
		}
		if err := json.Unmarshal([]byte(info), &fields); err != nil || fields.ID == "" {
			return "", fmt.Errorf("%s's INFO names no server_id: %q, %v", address, info, err)
		}
		return fields.ID, nil
	}
	hosts := map[string]bool{}
	for i := range 2 {
		ctr, uri := StartImageConfig(t, HubImage, "", nil, true)
		client, err := url.Parse(uri)
		if err != nil {
			t.Fatalf("parse %s: %v", uri, err)
		}
		monitor, err := url.Parse(ImageEndpoint(t, ctr, "8222/tcp", "http"))
		if err != nil {
			t.Fatalf("parse the monitoring endpoint: %v", err)
		}
		if host := client.Hostname(); host == "127.0.0.1" || host != monitor.Hostname() || hosts[host] {
			t.Fatalf("container %d publishes its client port on %s and its monitoring port on %s, want one address of its own (others: %v)", i, host, monitor.Hostname(), hosts)
		}
		hosts[client.Hostname()] = true
		own, err := serverID(client.Host)
		if err != nil {
			t.Fatalf("control: %v", err)
		}
		if stray, err := serverID(net.JoinHostPort("127.0.0.1", client.Port())); err == nil && stray == own {
			t.Fatalf("a dial to 127.0.0.1:%s reached container %d (server_id %s)", client.Port(), i, own)
		}
	}
}
