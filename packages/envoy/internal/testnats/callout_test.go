package testnats_test

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	neturl "net/url"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// The callout fixture sends the authorization of a client it has no static user for to the auth
// service, encrypted to the service's key, and admits a listed static user without asking.
func TestTheCalloutFixtureSendsUnknownClientsToTheService(t *testing.T) {
	keys := testnats.NewCalloutKeys(t)
	staticSeed, staticKey := testnats.User(t)
	url := testnats.StartWithCallout(t, keys, testnats.StaticUser{
		NKey: staticKey, Publish: []string{"example.>"}, Subscribe: []string{"example.>"},
	})
	requireCalloutRequests(t, url, keys, staticSeed)
}

// A callout hub's server listens on its relay's own loopback address, never on 127.0.0.1: a client
// another test left behind keeps dialing 127.0.0.1 at its own server's old port, and once the
// kernel gives that port to this server the client would reach the hub and add requests its test
// never sent (calloutHost). The server's /varz names the relay's host, and a dial to 127.0.0.1 at
// the server's port is refused or answered by some other listener, never by this server. Control:
// the address /varz names answers with this server's INFO, so the same read would see it there.
func TestACalloutHubIsOutOfReachOfAClientDialing127001(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("off Linux every callout hub listens on 127.0.0.1 (calloutHost)")
	}
	server, url := testnats.StartWithCalloutServer(t, testnats.NewCalloutKeys(t))
	relay, err := neturl.Parse(url)
	require.NoError(t, err)
	response, err := http.Get(server.MonitorURL() + "/varz")
	require.NoError(t, err)
	defer response.Body.Close()
	var varz struct {
		ID   string `json:"server_id"`
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&varz))
	require.Equal(t, relay.Hostname(), varz.Host, "the hub's server listens on another host than its relay")

	// info is the first line a dial to address reads, a NATS server's INFO, or the dial's error.
	info := func(address string) (string, error) {
		conn, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err != nil {
			return "", err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		return bufio.NewReader(conn).ReadString('\n')
	}
	own, err := info(net.JoinHostPort(varz.Host, strconv.Itoa(varz.Port)))
	require.NoError(t, err)
	require.Contains(t, own, varz.ID, "control: the server's own address did not answer with its INFO")
	stray, err := info(net.JoinHostPort("127.0.0.1", strconv.Itoa(varz.Port)))
	if err == nil {
		require.NotContains(t, stray, varz.ID, "a client dialing 127.0.0.1 at the hub's port reached the hub")
	}
}

// requireCalloutRequests connects to url as the auth service (keys.ServiceSeed) and counts the
// authorization requests the server sends it, refusing each: a client with no credential is one
// request, and the static user whose seed is staticSeed is admitted with none.
func requireCalloutRequests(t *testing.T, url string, keys testnats.CalloutKeys, staticSeed string) {
	t.Helper()
	service, err := testnats.ConnectSeed(t, url, keys.ServiceSeed)
	require.NoError(t, err, "the service's own user is a static auth user, admitted without a callout")
	defer service.Close()
	var requests, encrypted atomic.Int32
	_, err = service.Subscribe(natsserver.AuthCalloutSubject, func(msg *natsgo.Msg) {
		requests.Add(1)
		if msg.Header.Get(natsserver.AuthRequestXKeyHeader) != "" {
			encrypted.Add(1)
		}
		// An empty answer is the server's "no reason supplied" refusal, so the client is refused
		// at once rather than at the authorization timeout, through the service's own publish
		// grant on the server's reply inbox.
		_ = msg.Respond(nil)
	})
	require.NoError(t, err)
	require.NoError(t, service.Flush())

	unknown, err := testnats.ConnectSeed(t, url, "")
	if err == nil {
		unknown.Close()
	}
	require.ErrorIs(t, err, natsgo.ErrAuthorization, "a client with no credential is admitted only by the service's answer, and it refused")
	testwait.Eventually(t, "the authorization request for the client with no credential", func() bool {
		return requests.Load() == 1
	})
	require.EqualValues(t, 1, encrypted.Load(), "the request carries the server's xkey: it is sealed to the service's")

	static, err := testnats.ConnectSeed(t, url, staticSeed)
	require.NoError(t, err, "a listed static user is admitted by the server alone")
	static.Close()
	require.NoError(t, service.Flush())
	require.EqualValues(t, 1, requests.Load(), "the static user's connection sent the service a request")
}
