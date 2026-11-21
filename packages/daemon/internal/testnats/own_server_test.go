package testnats

import (
	"crypto/rand"
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
