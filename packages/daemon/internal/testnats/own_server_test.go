package testnats

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	neturl "net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/sjawhar/legion/daemon/internal/testnkeys"
)

// nkeyClient connects to url as the nkey user whose seed is seed, named name, never reconnecting:
// a client a row of this file leaves open must not outlive the row by dialing a removed server.
func nkeyClient(t *testing.T, url, seed, name string) *nats.Conn {
	t.Helper()
	user, err := nkeys.FromSeed([]byte(seed))
	if err != nil {
		t.Fatalf("read the nkey seed: %v", err)
	}
	public, err := user.PublicKey()
	if err != nil {
		t.Fatalf("read the nkey's public key: %v", err)
	}
	conn, err := nats.Connect(url, nats.Name(name), nats.NoReconnect(), nats.Timeout(5*time.Second),
		nats.Nkey(public, user.Sign))
	if err != nil {
		t.Fatalf("connect to %s as the nkey user: %v", url, err)
	}
	return conn
}

// The leak check holds a server a test starts for itself too (armServerLeakCheck): a client the
// test leaves open on it fails the test, named by id and name, though the server is removed when
// the test ends. The control is the same row closing its client: nothing is reported.
func TestAClientLeftOnATestsOwnServerFailsTheCheck(t *testing.T) {
	for _, row := range []struct {
		name   string
		closes bool
	}{{"leaves its client open", false}, {"control: closes its client", true}} {
		t.Run(row.name, func(t *testing.T) {
			seed, public := testnkeys.User(t)
			clientName := "a-client-of-a-tests-own-server-" + rand.Text()
			var conn *nats.Conn
			var recorder *leakRecorder
			t.Run("the test that starts the server", func(t *testing.T) {
				recorder = &leakRecorder{TB: t}
				url := StartNkeyAuthorized(recorder, public)
				conn = nkeyClient(t, url, seed, clientName)
				if row.closes {
					conn.Close()
				}
			})
			if conn != nil {
				t.Cleanup(conn.Close)
			}
			if row.closes {
				if len(recorder.messages) != 0 {
					t.Errorf("the leak check reported %q for a test that closed its client", recorder.messages)
				}
				return
			}
			report := recorder.only(t)
			if !strings.Contains(report, clientName) || !strings.Contains(report, "own NATS server") {
				t.Errorf("the leak check reported %q, which does not name the client %s left open on its own server", report, clientName)
			}
		})
	}
}

// serverID is the server_id of the INFO a dial to address reads first, which a NATS server sends
// before any credential, or why there is none.
func serverID(address string) (string, error) {
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

// Where the Docker daemon is this machine, a server a test starts for itself, and the shared
// server, are published on a loopback address of their own (newHost), never 127.0.0.1: a client of
// an earlier test dialing 127.0.0.1 at a server's port never reaches it, judged by the server_id of
// the INFO a dial reads. The control is the same read at the server's own address, which answers
// with that server_id.
func TestAServersPortsArePublishedOnAnAddressOfItsOwn(t *testing.T) {
	if _, ok := os.LookupEnv(externalURLEnv); ok {
		t.Skipf("%s names a running NATS server, so the package starts no container", externalURLEnv)
	}
	if _, host, err := publishHost(t.Context()); err != nil {
		t.Fatalf("find where Docker publishes a container's ports: %v", err)
	} else if host == "" {
		t.Skip("the Docker daemon is not this machine, so Docker publishes on its own default")
	}
	_, public := testnkeys.User(t)
	for i, url := range []string{StartNkeyAuthorized(t, public), URL(t)} {
		parsed, err := neturl.Parse(url)
		if err != nil {
			t.Fatalf("parse %s: %v", url, err)
		}
		if parsed.Hostname() == "127.0.0.1" {
			t.Fatalf("server %d is published on 127.0.0.1 (%s), want an address of its own", i, url)
		}
		own, err := serverID(parsed.Host)
		if err != nil {
			t.Fatalf("control: server %d's own address: %v", i, err)
		}
		if stray, err := serverID(net.JoinHostPort("127.0.0.1", parsed.Port())); err == nil && stray == own {
			t.Fatalf("a dial to 127.0.0.1:%s reached server %d (server_id %s)", parsed.Port(), i, own)
		}
	}
}
