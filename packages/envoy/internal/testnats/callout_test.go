package testnats_test

import (
	"bufio"
	"encoding/json"
	"fmt"
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

// Every server this package starts listens on a loopback address of its own, never on 127.0.0.1,
// and so does every listener in front of it: a client another test left behind keeps dialing the
// address its own server had, and once the kernel gives that port to a later server the client
// reaches it and sends what the later test never sent (NewHost). Each kind of server is a
// row: a plain one, a callout hub, the three members of a cluster, and a gate and a proxy in front
// of a plain one. For each server its /varz names the host of its relay as its client and
// monitoring host, and a cluster member's route listener too; a dial to 127.0.0.1 at the server's
// client port is refused or answered by some other listener, never by this server; and no two
// servers, gates or proxies share a host. Control: the address /varz names answers with the
// server's own INFO, so the same read would see the server at 127.0.0.1 if it listened there.
func TestEveryServerAndWhatStandsBeforeItListensOnAnAddressOfItsOwn(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("off Linux every server listens on 127.0.0.1 (NewHost)")
	}
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
	hostOf := func(url string) string {
		u, err := neturl.Parse(url)
		require.NoError(t, err)
		return u.Hostname()
	}
	hosts := map[string]string{}
	owned := func(name, host string) {
		require.NotEqual(t, "127.0.0.1", host, "%s listens on 127.0.0.1", name)
		if other, taken := hosts[host]; taken {
			t.Errorf("%s and %s share the address %s", name, other, host)
		}
		hosts[host] = name
	}
	check := func(name string, server *testnats.Server, cluster bool) {
		response, err := http.Get(server.MonitorURL() + "/varz")
		require.NoError(t, err)
		defer response.Body.Close()
		var varz struct {
			ID      string `json:"server_id"`
			Host    string `json:"host"`
			Port    int    `json:"port"`
			HTTP    string `json:"http_host"`
			Cluster struct {
				Addr string `json:"addr"`
			} `json:"cluster"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&varz))
		relay := hostOf(server.URL())
		owned(name, relay)
		require.Equal(t, relay, varz.Host, "%s's server listens for clients on another host than its relay", name)
		require.Equal(t, relay, varz.HTTP, "%s's server serves monitoring on another host than its relay", name)
		if cluster {
			require.Equal(t, relay, varz.Cluster.Addr, "%s's route listener is on another host than its relay", name)
		}
		own, err := info(net.JoinHostPort(varz.Host, strconv.Itoa(varz.Port)))
		require.NoError(t, err)
		require.Contains(t, own, varz.ID, "control: %s's own address did not answer with its INFO", name)
		if stray, err := info(net.JoinHostPort("127.0.0.1", strconv.Itoa(varz.Port))); err == nil {
			require.NotContains(t, stray, varz.ID, "a client dialing 127.0.0.1 at %s's port reached it", name)
		}
	}

	plain, _ := testnats.Start(t)
	check("a plain server", plain, false)
	callout, _ := testnats.StartWithCalloutServer(t, testnats.NewCalloutKeys(t))
	check("a callout hub", callout, false)
	for i, member := range testnats.StartCluster(t, 3).Servers {
		check(fmt.Sprintf("cluster member %d", i+1), member, true)
	}
	gated, _ := testnats.Start(t)
	check("the gated server", gated, false)
	require.Equal(t, hostOf(gated.URL()), hostOf(testnats.StartGate(t, gated).URL()), "a gate is on another host than its server")
	proxied, _ := testnats.Start(t)
	check("the proxied server", proxied, false)
	require.Equal(t, hostOf(proxied.URL()), hostOf(testnats.StartProxy(t, proxied.URL()).URL()), "a proxy is on another host than its server")
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
