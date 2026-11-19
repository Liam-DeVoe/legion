package testnats_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// forward relays every connection listener accepts to target, a host:port, until the listener
// closes, as the relays, proxies and taps of this repository's tests do.
func forward(listener net.Listener, target string) {
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer client.Close()
			server, err := net.Dial("tcp", target)
			if err != nil {
				return
			}
			defer server.Close()
			go func() { _, _ = io.Copy(server, client) }()
			_, _ = io.Copy(client, server)
		}()
	}
}

// connected reports whether a client named name is connected to server, by its /connz.
func connected(t *testing.T, server *testnats.Server, name string) bool {
	t.Helper()
	response, err := http.Get(server.MonitorURL() + "/connz")
	require.NoError(t, err)
	defer response.Body.Close()
	var connz struct {
		Connections []struct {
			Name string `json:"name"`
		} `json:"connections"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&connz))
	return slices.ContainsFunc(connz.Connections, func(c struct {
		Name string `json:"name"`
	}) bool {
		return c.Name == name
	})
}

// hostPort is the host:port a client URL names.
func hostPort(t *testing.T, url string) string {
	t.Helper()
	parsed, err := neturl.Parse(url)
	require.NoError(t, err)
	return parsed.Host
}

// A relay in front of a server listens on the server's own address (testnats.Listen), so a client
// of a relay an earlier test closed, which keeps dialing that relay's address, never reaches a
// later server, even when the kernel gives the later relay the same port. Both relays take the
// host their row's listen gives them; the later one is put by hand on the earlier one's port, to
// make the reuse certain. The control is the two relays as this repository's relays listened
// before Listen, on 127.0.0.1: there the stray client reaches the later server, which is what
// shows this test can see the failure it rules out.
func TestAClientOfAClosedRelayNeverReachesALaterServer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("off Linux every server and relay listens on 127.0.0.1 (NewHost)")
	}
	loopback := func(t *testing.T, _ string) net.Listener {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = listener.Close() })
		return listener
	}
	for _, row := range []struct {
		name string
		// listen is the row's listener for a relay in front of the server at target.
		listen  func(t *testing.T, target string) net.Listener
		reaches bool
	}{
		{"testnats.Listen", func(t *testing.T, target string) net.Listener { return testnats.Listen(t, target) }, false},
		{"control: 127.0.0.1", loopback, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			first, _ := testnats.Start(t)
			earlier := row.listen(t, first.URL())
			go forward(earlier, hostPort(t, first.URL()))
			stray, err := natsgo.Connect("nats://"+earlier.Addr().String(), natsgo.Name("stray"),
				natsgo.MaxReconnects(-1), natsgo.ReconnectWait(100*time.Millisecond), natsgo.RetryOnFailedConnect(true))
			require.NoError(t, err)
			t.Cleanup(stray.Close)
			testwait.Eventually(t, "the stray client connects through the earlier relay", stray.IsConnected)
			port := strconv.Itoa(earlier.Addr().(*net.TCPAddr).Port)

			_ = earlier.Close()
			first.Stop(t)
			later, _ := testnats.Start(t)
			// The host the row's listen gives a relay in front of the later server, on the earlier
			// relay's port.
			placed := row.listen(t, later.URL())
			laterHost := placed.Addr().(*net.TCPAddr).IP.String()
			_ = placed.Close()
			relay, err := net.Listen("tcp", net.JoinHostPort(laterHost, port))
			require.NoError(t, err, "the later relay takes the earlier relay's port")
			t.Cleanup(func() { _ = relay.Close() })
			go forward(relay, hostPort(t, later.URL()))

			if row.reaches {
				testwait.Eventually(t, "the stray client reaches the later server through the later relay", func() bool {
					return connected(t, later, "stray")
				})
				return
			}
			// The stray client dials again every 100 ms; 3 s is 30 tries at the earlier address.
			time.Sleep(3 * time.Second)
			require.False(t, connected(t, later, "stray"), "a client of the closed relay reached the later server")
		})
	}
}
