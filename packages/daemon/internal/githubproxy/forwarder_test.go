package githubproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/cipher"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// echoServer accepts connections and echoes every line back, prefixed with name, until closed: the
// host a tunnel reaches.
func echoServer(t *testing.T, name string) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					fmt.Fprintf(conn, "%s:%s\n", name, scanner.Text())
				}
			}()
		}
	}()
	return l
}

// brokerProxy stands in for the broker's proxy port: it reads a CONNECT and its Basic credential,
// records the target and the password, answers 407 for a password no live grant holds, and
// otherwise tunnels to upstream, whatever host the CONNECT named.
type brokerProxy struct {
	listener net.Listener
	upstream string

	mu    sync.Mutex
	live  map[string]bool
	seen  []proxyCall
	users []string
}

type proxyCall struct{ target, password string }

func newBrokerProxy(t *testing.T, upstream string) *brokerProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	p := &brokerProxy{listener: l, upstream: upstream, live: map[string]bool{}}
	go p.serve()
	return p
}

func (p *brokerProxy) serve() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			reader := bufio.NewReader(conn)
			req, err := http.ReadRequest(reader)
			if err != nil {
				return
			}
			user, password := "", ""
			if raw, ok := strings.CutPrefix(req.Header.Get("Proxy-Authorization"), "Basic "); ok {
				if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
					user, password, _ = strings.Cut(string(decoded), ":")
				}
			}
			p.mu.Lock()
			p.seen = append(p.seen, proxyCall{target: req.Host, password: password})
			p.users = append(p.users, user)
			live := p.live[password]
			p.mu.Unlock()
			if !live {
				io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nCipher-Proxy-Code: GRANT_NOT_LIVE\r\nContent-Length: 0\r\n\r\n")
				return
			}
			upstream, err := net.Dial("tcp", p.upstream)
			if err != nil {
				return
			}
			defer upstream.Close()
			io.WriteString(conn, "HTTP/1.1 200 OK\r\n\r\n")
			splice(conn, reader, upstream, nil)
		}()
	}
}

func (p *brokerProxy) setLive(password string, live bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live[password] = live
}

func (p *brokerProxy) calls() []proxyCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.seen)
}

// fakeBroker is the role's session at a broker that grants the role's names at once: each ask is
// a new grant g<n>, whose proxy password and two account placeholders name it, alive at the
// proxy until the test ends it or the forwarder revokes it.
type fakeBroker struct {
	proxy    *brokerProxy
	lifetime time.Duration
	hosts    []string
	asValue  bool // release the name as its value, which the forwarder must refuse

	mu      sync.Mutex
	asked   int
	revoked []string
}

func (b *fakeBroker) Request(_ context.Context, names []string, reason string, lifetime int) (cipher.RequestAnswer, error) {
	if lifetime != 3600 || reason == "" || !slices.Equal(names, []string{"ROLE_APP"}) {
		return cipher.RequestAnswer{}, fmt.Errorf("asked for %v for %d s under %q", names, lifetime, reason)
	}
	b.mu.Lock()
	b.asked++
	id := fmt.Sprintf("g%d", b.asked)
	b.mu.Unlock()
	b.proxy.setLive("pw-"+id, true)
	return cipher.RequestAnswer{RequestID: "r-" + id, State: "granted", GrantID: &id, LifetimeSeconds: &lifetime}, nil
}

func (b *fakeBroker) WaitRequest(context.Context, string, time.Duration) (cipher.RequestAnswer, error) {
	return cipher.RequestAnswer{}, errors.New("no request waits")
}

func (b *fakeBroker) Values(_ context.Context, id string) (cipher.Release, error) {
	expires := time.Now().Add(b.lifetime)
	release := cipher.Release{ExpiresAt: &expires, Proxy: &cipher.ProxyRelease{
		ProxyURL: "http://" + b.proxy.listener.Addr().String(), ProxyPassword: "pw-" + id, CACertificate: "unused",
		Names: map[string]cipher.ProxyName{"ROLE_APP": {
			Value: "{}", Hosts: b.hosts,
			Slots: map[string]string{"github:Acct-A": "ph-a-" + id, "github:acct-b": "ph-b-" + id},
		}},
	}}
	if b.asValue {
		release.Values = map[string]string{"ROLE_APP": "ghs_value"}
	}
	return release, nil
}

func (b *fakeBroker) Revoke(_ context.Context, id string) error {
	b.mu.Lock()
	b.revoked = append(b.revoked, id)
	b.mu.Unlock()
	b.proxy.setLive("pw-"+id, false)
	return nil
}

func (b *fakeBroker) revokedIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.revoked)
}

// rig is one forwarder over a fake broker, its broker proxy and the host behind it.
type rig struct {
	t       *testing.T
	broker  *fakeBroker
	proxy   *brokerProxy
	fwd     *Forwarder
	ghDir   string
	stop    context.CancelFunc
	stopped chan struct{}
}

func newRig(t *testing.T, change func(*Config, *fakeBroker)) *rig {
	t.Helper()
	bound := echoServer(t, "bound")
	proxy := newBrokerProxy(t, bound.Addr().String())
	broker := &fakeBroker{proxy: proxy, lifetime: time.Hour, hosts: []string{"api.github.com", "github.com", "*.githubusercontent.com"}}
	ghDir := filepath.Join(t.TempDir(), "gh")
	cfg := Config{
		Role: "implementer", Listen: "127.0.0.1:0", Secrets: []string{"ROLE_APP"}, Reason: "the implementer of ACME-1",
		GHDir: ghDir, DefaultOwner: "acct-a", Renames: nil,
		Open: func() (Broker, error) { return broker, nil },
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if change != nil {
		change(&cfg, broker)
	}
	fwd, err := Listen(cfg)
	if err != nil {
		t.Fatalf("Listen = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{t: t, broker: broker, proxy: proxy, fwd: fwd, ghDir: ghDir, stop: cancel, stopped: make(chan struct{})}
	go func() {
		defer close(r.stopped)
		if err := fwd.Serve(ctx); err != nil {
			t.Errorf("Serve = %v", err)
		}
	}()
	t.Cleanup(r.shutdown)
	return r
}

func (r *rig) shutdown() {
	r.stop()
	select {
	case <-r.stopped:
	case <-time.After(10 * time.Second):
		r.t.Error("the forwarder did not stop within 10 s")
	}
}

// connect sends a CONNECT for target through the forwarder and answers the status line and, for
// a 200, what the tunnel echoes of one line.
func (r *rig) connect(target string) (status string, echoed string) {
	r.t.Helper()
	conn, err := net.Dial("tcp", r.fwd.Addr().String())
	if err != nil {
		r.t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		r.t.Fatalf("read the forwarder's answer: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.Status, ""
	}
	fmt.Fprintf(conn, "ping\n")
	line, err := reader.ReadString('\n')
	if err != nil {
		r.t.Fatalf("read the tunnel's echo: %v", err)
	}
	return resp.Status, strings.TrimSpace(line)
}

// awaitGrant waits for the forwarder's first grant.
func (r *rig) awaitGrant() *Grant {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g, err := r.fwd.keeper.awaitFirst(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	return g
}

// TestABoundHostGoesThroughTheBrokerWithTheGrantsPassword: a CONNECT to a host the grant binds
// reaches the broker's proxy, which is told the same target and the grant's proxy password as the
// password of a Basic credential whose user is ProxyUser, and the tunnel carries the client's
// bytes to the host behind it.
//
// The named revert: send the CONNECT to the host itself for every target, and the broker's proxy
// records nothing.
func TestABoundHostGoesThroughTheBrokerWithTheGrantsPassword(t *testing.T) {
	r := newRig(t, nil)
	grant := r.awaitGrant()
	status, echoed := r.connect("api.github.com:443")
	if !strings.HasPrefix(status, "200") || echoed != "bound:ping" {
		t.Fatalf("CONNECT api.github.com:443 = %q, echo %q; want 200 and the bound host's echo", status, echoed)
	}
	calls := r.proxy.calls()
	if len(calls) != 1 || calls[0] != (proxyCall{target: "api.github.com:443", password: "pw-" + grant.ID}) {
		t.Fatalf("the broker's proxy saw %+v; want one CONNECT api.github.com:443 with grant %s's password", calls, grant.ID)
	}
	r.proxy.mu.Lock()
	users := slices.Clone(r.proxy.users)
	r.proxy.mu.Unlock()
	if len(users) != 1 || users[0] != ProxyUser {
		t.Fatalf("the credential's user was %q, want %q", users, ProxyUser)
	}
	if status, _ := r.connect("raw.githubusercontent.com:443"); !strings.HasPrefix(status, "200") {
		t.Fatalf("CONNECT under the wildcard entry = %q, want 200 through the broker", status)
	}
	if got := len(r.proxy.calls()); got != 2 {
		t.Fatalf("the broker's proxy saw %d CONNECTs; want 2, the wildcard entry's among them", got)
	}
}

// TestAnotherHostIsOpenedDirectly: a CONNECT to a host the grant does not bind, or to a bound
// host's other port, is opened by the forwarder itself and never reaches the broker's proxy.
func TestAnotherHostIsOpenedDirectly(t *testing.T) {
	r := newRig(t, nil)
	r.awaitGrant()
	public := echoServer(t, "public")
	status, echoed := r.connect(public.Addr().String())
	if !strings.HasPrefix(status, "200") || echoed != "public:ping" {
		t.Fatalf("CONNECT %s = %q, echo %q; want 200 and the public host's own echo", public.Addr(), status, echoed)
	}
	if calls := r.proxy.calls(); len(calls) != 0 {
		t.Fatalf("the broker's proxy saw %+v; want nothing of a host the grant does not bind", calls)
	}
}

// TestA407GetsANewGrantAndFailsOnlyThatConnection: once the broker ends the grant early, its
// proxy answers 407; the forwarder asks for a new grant at once and rewrites the bundle with its
// placeholders, and answers that one connection 503 rather than carry it under the new grant: the
// client sent it with a placeholder of the ended grant, which the new grant never swaps, so a
// retry would take the old placeholder to GitHub. The client's next call reads the new bundle and
// goes through with the new grant's password.
//
// The named revert: carry the connection under the new grant, and the broker's proxy sees a
// second CONNECT for it and the client gets 200.
func TestA407GetsANewGrantAndFailsOnlyThatConnection(t *testing.T) {
	r := newRig(t, nil)
	first := r.awaitGrant()
	r.proxy.setLive("pw-"+first.ID, false)

	if status, _ := r.connect("github.com:443"); !strings.HasPrefix(status, "503") {
		t.Fatalf("CONNECT after the grant ended = %q; want 503, that connection failed", status)
	}
	calls := r.proxy.calls()
	if len(calls) != 1 || calls[0].password != "pw-"+first.ID {
		t.Fatalf("the broker's proxy saw %+v; want the ended grant's password once and no retry under a new one", calls)
	}
	current := r.fwd.Current()
	if current == nil || current.ID == first.ID {
		t.Fatalf("the forwarder holds %+v; want a new grant in place of %s", current, first.ID)
	}
	if token := hostsToken(t, r.ghDir, "acct-a"); token != "ph-a-"+current.ID {
		t.Fatalf("acct-a's hosts.yml holds %q; want the new grant's placeholder", token)
	}
	if status, echoed := r.connect("github.com:443"); !strings.HasPrefix(status, "200") || echoed != "bound:ping" {
		t.Fatalf("the next CONNECT = %q, echo %q; want 200 under the new grant", status, echoed)
	}
	if calls := r.proxy.calls(); len(calls) != 2 || calls[1].password != "pw-"+current.ID {
		t.Fatalf("the broker's proxy saw %+v; want the next CONNECT with the new grant's password", calls)
	}
}

// TestTheBundleHoldsEachAccountsPlaceholder: the forwarder writes one gh directory per account of
// the grant, lowercased, whose hosts.yml holds that account's placeholder and no other string,
// beside the configured renames.
func TestTheBundleHoldsEachAccountsPlaceholder(t *testing.T) {
	rename := ghrepo.MustRename("acct-b/old", "acct-a/new")
	r := newRig(t, func(cfg *Config, _ *fakeBroker) { cfg.Renames = []ghrepo.Rename{rename} })
	grant := r.awaitGrant()
	for account, want := range map[string]string{"acct-a": "ph-a-" + grant.ID, "acct-b": "ph-b-" + grant.ID} {
		if got := hostsToken(t, r.ghDir, account); got != want {
			t.Errorf("%s's hosts.yml holds %q; want %q", account, got, want)
		}
	}
	pairs, err := ghconfig.ReadPairs(r.ghDir)
	if err != nil || len(pairs) != 1 || pairs[0] != rename {
		t.Fatalf("the bundle's renames = %v, %v; want %v", pairs, err, rename)
	}
}

// hostsToken is the token account's hosts.yml under dir holds.
func hostsToken(t *testing.T, dir, account string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, account, ghconfig.HostsFile))
	if err != nil {
		t.Fatalf("read %s's hosts.yml: %v", account, err)
	}
	token, err := ghconfig.TokenFromHosts(data)
	if err != nil {
		t.Fatalf("%s's hosts.yml: %v", account, err)
	}
	return token
}

// TestTheGrantIsRenewedBeforeItExpires: with RenewBefore of a grant's life left, the forwarder
// asks for the next grant, rewrites the bundle with it, and leaves the old grant to its own
// expiry rather than revoking it under calls made with it.
func TestTheGrantIsRenewedBeforeItExpires(t *testing.T) {
	r := newRig(t, func(cfg *Config, b *fakeBroker) {
		b.lifetime = 3 * time.Second
		cfg.RenewBefore = 2 * time.Second
	})
	first := r.awaitGrant()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && r.fwd.Current().ID == first.ID {
		time.Sleep(50 * time.Millisecond)
	}
	renewed := r.fwd.Current()
	if renewed.ID == first.ID {
		t.Fatalf("the forwarder still holds %s after its renewal was due", first.ID)
	}
	if token := hostsToken(t, r.ghDir, "acct-b"); token != "ph-b-"+renewed.ID {
		t.Fatalf("acct-b's hosts.yml holds %q; want the renewed grant's placeholder", token)
	}
	if revoked := r.broker.revokedIDs(); slices.Contains(revoked, first.ID) {
		t.Fatalf("the renewal revoked %s; want it left to its own expiry", first.ID)
	}
}

// TestStopRevokesTheHeldGrants: when the forwarder stops, at the end of the role's generation, it
// revokes the grant it holds.
func TestStopRevokesTheHeldGrants(t *testing.T) {
	r := newRig(t, nil)
	grant := r.awaitGrant()
	r.shutdown()
	if revoked := r.broker.revokedIDs(); !slices.Contains(revoked, grant.ID) {
		t.Fatalf("the broker saw revocations %v; want %s revoked at the stop", revoked, grant.ID)
	}
}

// TestANameReleasedAsAValueIsRefused: a release that hands one of the role's names over as its
// value would put a credential in the role's files, so the forwarder writes no bundle and revokes
// the grant.
func TestANameReleasedAsAValueIsRefused(t *testing.T) {
	r := newRig(t, func(_ *Config, b *fakeBroker) { b.asValue = true })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(r.broker.revokedIDs()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if revoked := r.broker.revokedIDs(); len(revoked) == 0 {
		t.Fatal("the forwarder revoked nothing; want the grant that released a value revoked")
	}
	if r.fwd.Current() != nil {
		t.Fatalf("the forwarder holds %+v; want no grant", r.fwd.Current())
	}
	if _, err := os.Stat(r.ghDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the gh directory exists (%v); want no bundle written", err)
	}
}

// TestAnythingButCONNECTIsRefused: the role is told the forwarder for HTTPS alone, so a plain
// proxy request is answered 405 and goes nowhere.
func TestAnythingButCONNECTIsRefused(t *testing.T) {
	r := newRig(t, nil)
	r.awaitGrant()
	conn, err := net.Dial("tcp", r.fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("a GET through the forwarder = %v, %v; want 405", resp, err)
	}
}

// TestTheForwarderListensOnLoopbackAlone: an address that is no loopback one is refused before
// anything listens.
func TestTheForwarderListensOnLoopbackAlone(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", ":0", "example.com:80"} {
		_, err := Listen(Config{Role: "implementer", Listen: address, Secrets: []string{"ROLE_APP"}, GHDir: t.TempDir(),
			Open: func() (Broker, error) { return nil, errors.New("unused") }})
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("Listen(%q) = %v; want a refusal naming loopback", address, err)
		}
	}
}

// TestBindsReadsTheGrantsHostsAsTheProxyDoes: an exact entry binds its own host on port 443, a
// `*.` entry binds names under its domain and not the domain itself, and another port binds
// nothing.
func TestBindsReadsTheGrantsHostsAsTheProxyDoes(t *testing.T) {
	g := &Grant{Hosts: []string{"api.github.com", "*.githubusercontent.com"}}
	for _, tc := range []struct {
		host, port string
		want       bool
	}{
		{"api.github.com", "443", true},
		{"API.GitHub.com.", "443", true},
		{"api.github.com", "80", false},
		{"github.com", "443", false},
		{"raw.githubusercontent.com", "443", true},
		{"githubusercontent.com", "443", false},
		{"evilgithubusercontent.com", "443", false},
	} {
		if got := g.Binds(tc.host, tc.port); got != tc.want {
			t.Errorf("Binds(%s, %s) = %v, want %v", tc.host, tc.port, got, tc.want)
		}
	}
}
