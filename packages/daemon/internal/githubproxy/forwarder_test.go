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
	"net/url"
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
// proxy until the test ends it or the forwarder revokes it. With gate set, each ask signals
// entered and then waits for gate to close before it answers: an ask the broker commits while the
// forwarder stops. With hangFrom set, the ask of that number and every later one signals entered
// and then hangs until its context ends, as a slow broker or a request it holds pending does.
// With denyFrom set, the ask of that number and every later one is denied, as a broker whose
// policy no longer allows the names answers. With valuesErr set, every release fails with it after
// the grant was made. Revoke fails on a context that has ended, as cipher.Session.Revoke's HTTP
// call does, answers revokeErr where it is set, and fails its first revokeFailures calls.
type fakeBroker struct {
	proxy          *brokerProxy
	lifetime       time.Duration
	hosts          []string
	asValue        bool // release the name as its value, which the forwarder must refuse
	gate           chan struct{}
	entered        chan struct{}
	hangFrom       int
	denyFrom       int
	valuesErr      error
	revokeErr      error
	revokeFailures int

	mu       sync.Mutex
	asked    int
	attempts int // Revoke calls, failed ones included
	revoked  []string
}

func (b *fakeBroker) Request(ctx context.Context, names []string, reason string, lifetime int) (cipher.RequestAnswer, error) {
	if lifetime != 3600 || reason == "" || !slices.Equal(names, []string{"ROLE_APP"}) {
		return cipher.RequestAnswer{}, fmt.Errorf("asked for %v for %d s under %q", names, lifetime, reason)
	}
	b.mu.Lock()
	b.asked++
	n := b.asked
	id := fmt.Sprintf("g%d", n)
	b.mu.Unlock()
	if b.gate != nil || (b.hangFrom > 0 && n >= b.hangFrom) {
		select {
		case b.entered <- struct{}{}:
		default:
		}
	}
	if b.gate != nil {
		<-b.gate
	}
	if b.hangFrom > 0 && n >= b.hangFrom {
		<-ctx.Done()
		return cipher.RequestAnswer{}, ctx.Err()
	}
	if b.denyFrom > 0 && n >= b.denyFrom {
		detail := "the policy refuses ROLE_APP"
		return cipher.RequestAnswer{RequestID: "r-" + id, State: "denied", Detail: &detail}, nil
	}
	b.proxy.setLive("pw-"+id, true)
	return cipher.RequestAnswer{RequestID: "r-" + id, State: "granted", GrantID: &id, LifetimeSeconds: &lifetime}, nil
}

// asks is how many asks the broker has answered, granted or denied.
func (b *fakeBroker) asks() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.asked
}

func (b *fakeBroker) WaitRequest(context.Context, string, time.Duration) (cipher.RequestAnswer, error) {
	return cipher.RequestAnswer{}, errors.New("no request waits")
}

func (b *fakeBroker) Values(_ context.Context, id string) (cipher.Release, error) {
	if b.valuesErr != nil {
		return cipher.Release{}, b.valuesErr
	}
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

func (b *fakeBroker) Revoke(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.revokeErr != nil {
		return b.revokeErr
	}
	b.mu.Lock()
	b.attempts++
	if b.revokeFailures > 0 {
		b.revokeFailures--
		b.mu.Unlock()
		return errors.New("the broker could not end the grant")
	}
	b.revoked = append(b.revoked, id)
	b.mu.Unlock()
	b.proxy.setLive("pw-"+id, false)
	return nil
}

// revokeAttempts is how many Revoke calls reached the broker, failed ones included.
func (b *fakeBroker) revokeAttempts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attempts
}

// requireEveryGrantRevoked fails t unless every grant the broker made, g1 to g<asks>, was revoked.
func requireEveryGrantRevoked(t *testing.T, b *fakeBroker) {
	t.Helper()
	revoked := b.revokedIDs()
	for n := 1; n <= b.asks(); n++ {
		if id := fmt.Sprintf("g%d", n); !slices.Contains(revoked, id) {
			t.Errorf("grant %s was never revoked; the broker saw revocations %v", id, revoked)
		}
	}
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
		GitHubHosts: []string{"api.github.com", "github.com", "uploads.github.com"},
		GHDir:       ghDir, DefaultOwner: "acct-a", Renames: nil,
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
// a 200, what the tunnel echoes of one line, failing the test on an error.
func (r *rig) connect(target string) (status string, echoed string) {
	r.t.Helper()
	status, echoed, err := r.tryConnect(target)
	if err != nil {
		r.t.Fatal(err)
	}
	return status, echoed
}

// tryConnect is connect answering its error rather than failing the test, for a goroutine other
// than the test's own, which may not call t.Fatal. A status read before the error is answered
// with it.
func (r *rig) tryConnect(target string) (status string, echoed string, err error) {
	conn, err := net.Dial("tcp", r.fwd.Addr().String())
	if err != nil {
		return "", "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return "", "", fmt.Errorf("read the forwarder's answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.Status, "", nil
	}
	fmt.Fprintf(conn, "ping\n")
	line, err := reader.ReadString('\n')
	if err != nil {
		return resp.Status, "", fmt.Errorf("read the tunnel's echo: %w", err)
	}
	return resp.Status, strings.TrimSpace(line), nil
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

// TestABrokerDownAtTheStartStopsOnlyGitHubCalls: with the broker unreachable from the start, a
// CONNECT to a host GitHub's kind does not bind is tunnelled at once, with nothing waiting on a
// grant, and a CONNECT to api.github.com is answered 503 at once, naming the failed ask, rather
// than held for the recovery bound.
//
// The named revert: have serveConn wait for the first grant before it reads the host, and the
// first CONNECT is held 60 s, past connect's 20 s deadline.
func TestABrokerDownAtTheStartStopsOnlyGitHubCalls(t *testing.T) {
	public := echoServer(t, "public")
	r := newRig(t, func(cfg *Config, _ *fakeBroker) {
		cfg.Open = func() (Broker, error) { return nil, errors.New("dial the broker: connection refused") }
	})
	start := time.Now()
	status, echoed := r.connect(public.Addr().String())
	if !strings.HasPrefix(status, "200") || echoed != "public:ping" || time.Since(start) > 3*time.Second {
		t.Fatalf("CONNECT %s with no grant = %q, echo %q, in %s; want 200 and the public host's echo at once", public.Addr(), status, echoed, time.Since(start))
	}
	start = time.Now()
	if status, _ := r.connect("api.github.com:443"); !strings.HasPrefix(status, "503") || time.Since(start) > 5*time.Second {
		t.Fatalf("CONNECT api.github.com:443 with the broker down = %q in %s; want 503 at once", status, time.Since(start))
	}
	if calls := r.proxy.calls(); len(calls) != 0 {
		t.Fatalf("the broker's proxy saw %+v; want nothing while the forwarder holds no grant", calls)
	}
}

// stallingWriter is a log sink whose writes of a line holding match block until free is called,
// as a role's stderr does when nothing drains it; stalled is closed when the first such write
// begins. Every other line is kept.
type stallingWriter struct {
	match   string
	stalled chan struct{}
	release chan struct{}
	once    sync.Once
	freed   sync.Once
	lockedBuffer
}

func newStallingWriter(match string) *stallingWriter {
	return &stallingWriter{match: match, stalled: make(chan struct{}), release: make(chan struct{})}
}

func (w *stallingWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.match) {
		w.once.Do(func() { close(w.stalled) })
		<-w.release
	}
	return w.lockedBuffer.Write(p)
}

// free lets every stalled write through, once.
func (w *stallingWriter) free() { w.freed.Do(func() { close(w.release) }) }

// TestAStalledLogDoesNotHoldBackTheRevokeAtTheStop: when the forwarder's log stalls on the line
// about a grant it just got (its stderr not drained), a stop still revokes that grant at once,
// well inside the stop bound: the grant is held before any line about it is written, and the
// revocation waits for the grant to be held, not for the line about it.
//
// The named reverts: put the grant in held only when the ask is published, after its log lines
// (as at d377b67), and the grant is never revoked while the log stalls; have the stop wait for
// the ask to end, and the revocation comes only when the stop bound runs out.
func TestAStalledLogDoesNotHoldBackTheRevokeAtTheStop(t *testing.T) {
	logs := newStallingWriter("holds a grant")
	r := newRig(t, func(cfg *Config, _ *fakeBroker) { cfg.Log = slog.New(slog.NewTextHandler(logs, nil)) })
	t.Cleanup(logs.free) // runs before the rig's own shutdown, which waits for the stop to end
	select {
	case <-logs.stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("the forwarder wrote no line about a grant")
	}
	r.stop()
	deadline := time.Now().Add(stopBound / 2)
	for time.Now().Before(deadline) && !slices.Contains(r.broker.revokedIDs(), "g1") {
		time.Sleep(20 * time.Millisecond)
	}
	if revoked := r.broker.revokedIDs(); !slices.Contains(revoked, "g1") {
		t.Fatalf("the broker saw revocations %v %s into a stop while the log stalled; want g1 revoked at once", revoked, stopBound/2)
	}
	logs.free()
	select {
	case <-r.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the forwarder did not stop once its log drained")
	}
}

// TestAnAskInFlightAtTheStopDoesNotKeepTheHeldGrantsLive: g1 lands and is live for an hour, then
// the renewal ask hangs, as a slow broker or a request it holds pending makes it, and the forwarder
// stops. g1 is revoked at once: the stop revokes the grants it holds before it waits for the ask in
// flight, each with a time limit of its own, so the wait cannot use up the time the revocation
// needs.
//
// The named revert: wait for the ask in flight first and revoke afterwards under the same limit
// (as at aa1a419f), and the revocation runs on a context that has ended and revokes nothing.
func TestAnAskInFlightAtTheStopDoesNotKeepTheHeldGrantsLive(t *testing.T) {
	r := newRig(t, func(cfg *Config, b *fakeBroker) {
		b.hangFrom, b.entered = 2, make(chan struct{}, 1)
		cfg.RenewBefore = time.Hour - 300*time.Millisecond
	})
	if g := r.awaitGrant(); g.ID != "g1" {
		t.Fatalf("the first grant is %s, want g1", g.ID)
	}
	select {
	case <-r.broker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the forwarder made no renewal ask")
	}
	r.stop()
	deadline := time.Now().Add(revokeBound)
	for time.Now().Before(deadline) && !slices.Contains(r.broker.revokedIDs(), "g1") {
		time.Sleep(20 * time.Millisecond)
	}
	if revoked := r.broker.revokedIDs(); !slices.Contains(revoked, "g1") {
		t.Fatalf("the broker saw revocations %v %s into a stop with the renewal ask hanging; want g1, held and live for an hour, revoked at once", revoked, revokeBound)
	}
	select {
	case <-r.stopped:
	case <-time.After(stopBound + 5*time.Second):
		t.Fatal("the forwarder did not stop")
	}
}

// TestAGrantWhoseReleaseFailsIsStillRevoked: the broker made the grant, then its release fails.
// The forwarder owns the grant from the moment the broker named it, before its release is asked
// for, so the grant is revoked, and every grant the broker made is revoked by the stop at the
// latest.
//
// The named revert: put the grant in held only once its release and bundle succeeded (as at
// aa1a419f), and a grant whose release fails is never revoked.
func TestAGrantWhoseReleaseFailsIsStillRevoked(t *testing.T) {
	r := newRig(t, func(_ *Config, b *fakeBroker) { b.valuesErr = errors.New("the broker's database is down") })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !slices.Contains(r.broker.revokedIDs(), "g1") {
		time.Sleep(20 * time.Millisecond)
	}
	r.shutdown()
	if r.broker.asks() == 0 {
		t.Fatal("the forwarder made no ask")
	}
	requireEveryGrantRevoked(t, r.broker)
}

// TestARefusedGrantWhoseRevokeFailsIsRevokedAtTheStop: the forwarder refuses a release (a name
// released as its value) and its revocation of that grant fails once. The grant stays in the one
// ledger every revocation reads, so the stop revokes it.
//
// The named revert: revoke a refused grant once and drop it whatever the broker answered (as at
// aa1a419f), and the stop never revokes it.
func TestARefusedGrantWhoseRevokeFailsIsRevokedAtTheStop(t *testing.T) {
	r := newRig(t, func(_ *Config, b *fakeBroker) { b.asValue, b.revokeFailures = true, 1 })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && r.broker.revokeAttempts() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if r.broker.revokeAttempts() == 0 {
		t.Fatal("the forwarder tried no revocation of the grant it refused")
	}
	r.shutdown()
	requireEveryGrantRevoked(t, r.broker)
}

// TestNoLineAboutAnAskIsWrittenBeforeItLands: every line an ask writes, whatever its outcome,
// comes after the ask has landed (its grant, if any, in held), so a log that stalls on such a line
// holds back no revocation at the stop. Three outcomes write a line: a grant it holds, a release it
// refuses and cannot revoke, and a grant that holds no account for GH_CONFIG_DIR's directory.
//
// The named revert: write the refused grant's line in ask, or the missing default's in write
// (as at aa1a419f), and that row's ask has not landed while its line stalls.
func TestNoLineAboutAnAskIsWrittenBeforeItLands(t *testing.T) {
	for _, tc := range []struct {
		name, stallOn, defaultOwner string
		asValue                     bool
		revokeErr                   error
	}{
		{name: "a grant it holds", stallOn: "holds a grant", defaultOwner: "acct-a"},
		{name: "a release it refuses and cannot revoke", stallOn: "the grant it refused", defaultOwner: "acct-a",
			asValue: true, revokeErr: errors.New("the broker is down")},
		{name: "a grant with no account for the default", stallOn: "GH_CONFIG_DIR's directory", defaultOwner: "acct-z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hosts := []string{"api.github.com", "github.com"}
			broker := &fakeBroker{proxy: newBrokerProxy(t, echoServer(t, "bound").Addr().String()), lifetime: time.Hour,
				hosts: hosts, asValue: tc.asValue, revokeErr: tc.revokeErr}
			logs := newStallingWriter(tc.stallOn)
			t.Cleanup(logs.free)
			k := newKeeper(Config{Secrets: []string{"ROLE_APP"}, Reason: "the implementer of ACME-1", GitHubHosts: hosts,
				GHDir: filepath.Join(t.TempDir(), "gh"), DefaultOwner: tc.defaultOwner, RenewBefore: RenewBefore,
				Open: func() (Broker, error) { return broker, nil }}, slog.New(slog.NewTextHandler(logs, nil)))
			a := &ask{landed: make(chan struct{}), done: make(chan struct{})}
			k.asking = a
			go k.run(a, "the test")
			select {
			case <-logs.stalled:
			case <-time.After(5 * time.Second):
				t.Fatalf("the ask wrote no line holding %q", tc.stallOn)
			}
			select {
			case <-a.landed:
			case <-time.After(time.Second):
				t.Fatalf("the ask has not landed while its line holding %q stalls", tc.stallOn)
			}
			logs.free()
			select {
			case <-a.done:
			case <-time.After(5 * time.Second):
				t.Fatal("the ask did not end once its log drained")
			}
		})
	}
}

// TestAGrantInFlightAtTheStopIsRevokedWhenItLands: an ask the broker answers only after the
// forwarder's stop began still lands a grant, and the stop waits for it, within its bound, and
// revokes it.
//
// The named revert: have revokeHeld read the grants it holds without waiting for the ask in
// flight, and the grant that lands after it is never revoked.
func TestAGrantInFlightAtTheStopIsRevokedWhenItLands(t *testing.T) {
	gate := make(chan struct{})
	r := newRig(t, func(_ *Config, b *fakeBroker) {
		b.gate, b.entered = gate, make(chan struct{}, 1)
	})
	select {
	case <-r.broker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the forwarder made no ask")
	}
	r.stop()
	time.Sleep(200 * time.Millisecond)
	close(gate)
	select {
	case <-r.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the forwarder did not stop")
	}
	if revoked := r.broker.revokedIDs(); !slices.Equal(revoked, []string{"g1"}) {
		t.Fatalf("the broker saw revocations %v; want g1, the grant that landed during the stop", revoked)
	}
}

// TestAListenerThatFailsStillRevokesTheGrants: when the listener fails for good, Serve answers the
// failure and still revokes the grants it holds first.
//
// The named revert: have Serve return on a failed Accept before revokeHeld, and the grant stays
// live.
func TestAListenerThatFailsStillRevokesTheGrants(t *testing.T) {
	broker := &fakeBroker{proxy: newBrokerProxy(t, echoServer(t, "bound").Addr().String()), lifetime: time.Hour, hosts: []string{"api.github.com"}}
	fwd, err := Listen(Config{Role: "implementer", Listen: "127.0.0.1:0", Secrets: []string{"ROLE_APP"}, Reason: "the implementer of ACME-1",
		GitHubHosts: []string{"api.github.com"},
		GHDir:       filepath.Join(t.TempDir(), "gh"), Open: func() (Broker, error) { return broker, nil },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- fwd.Serve(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && fwd.Current() == nil {
		time.Sleep(20 * time.Millisecond)
	}
	if fwd.Current() == nil {
		t.Fatal("the forwarder holds no grant")
	}
	_ = fwd.listener.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve answered nil for a listener that failed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after its listener failed")
	}
	if revoked := broker.revokedIDs(); !slices.Equal(revoked, []string{"g1"}) {
		t.Fatalf("the broker saw revocations %v; want g1", revoked)
	}
}

// lockedBuffer is a log sink the forwarder's goroutines and the test share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestAGrantBindingAHostTheConfigLacksIsWarnedOf: at each grant the forwarder compares the hosts
// the grant binds with the hosts it was told (Config.GitHubHosts, the deployment's github_hosts)
// and logs one warning naming each host the grant binds that the configured list does not cover,
// and the key to change: such a host goes out directly before a later forwarder's first grant. A
// configured `*.` entry covers every name under its domain, as it does when the forwarder routes
// (bindsHost), so a host it covers is not warned of, and a host it does not cover still is.
//
// The named reverts: drop the comparison from the keeper, and the grant with an extra host logs
// nothing; compare exact entries only, and the wildcard rows warn of api.github.com.
func TestAGrantBindingAHostTheConfigLacksIsWarnedOf(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured []string
		lacks      string // the warning's whole lacks field, "" for no warning
	}{
		{"the grant binds a host the config lacks", []string{"api.github.com", "github.com"}, "lacks=[*.githubusercontent.com] "},
		{"the config holds every host the grant binds", []string{"api.github.com", "github.com", "*.githubusercontent.com"}, ""},
		{"a wildcard entry covers a host, and an uncovered one is still warned of", []string{"*.github.com", "github.com"}, "lacks=[*.githubusercontent.com] "},
		{"wildcard entries cover every host the grant binds", []string{"*.github.com", "github.com", "*.githubusercontent.com"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &lockedBuffer{}
			r := newRig(t, func(cfg *Config, _ *fakeBroker) {
				cfg.GitHubHosts = tc.configured
				cfg.Log = slog.New(slog.NewTextHandler(logs, nil))
			})
			r.awaitGrant()
			r.shutdown()
			var warnings []string
			for _, line := range strings.Split(logs.String(), "\n") {
				if strings.Contains(line, "binds hosts runtime.kubernetes.agent_secrets.github_hosts lacks") {
					warnings = append(warnings, line)
				}
			}
			switch {
			case tc.lacks == "" && len(warnings) != 0:
				t.Fatalf("%d warnings of a host the config lacks; want none. Log:\n%s", len(warnings), logs.String())
			case tc.lacks != "" && (len(warnings) != 1 || !strings.Contains(warnings[0], tc.lacks)):
				t.Fatalf("warnings %q; want one holding %s. Log:\n%s", warnings, tc.lacks, logs.String())
			}
		})
	}
}

// TestAProxyThatNeverFinishesTheTLSHandshakeIsGivenUp: an https proxy that accepts the connection
// and never answers the TLS handshake is given up within connectThrough's bound, not waited on
// for ever while the role's call hangs.
//
// The named revert: set the connection's deadline only after the handshake, and connectThrough is
// still in the handshake when the test gives up on it.
func TestAProxyThatNeverFinishesTheTLSHandshakeIsGivenUp(t *testing.T) {
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held []net.Conn
	var mu sync.Mutex
	t.Cleanup(func() {
		silent.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			conn.Close()
		}
	})
	go func() {
		for {
			conn, err := silent.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	proxyURL, err := url.Parse("https://" + silent.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	grant := &Grant{ID: "g1", ProxyURL: proxyURL, Password: "pw-g1"}
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, _, _, err := connectThrough(grant, "api.github.com:443", 500*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("connectThrough answered no error for a proxy that never finished the TLS handshake")
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Fatalf("connectThrough gave up after %s; want about its 500 ms bound", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connectThrough is still in the TLS handshake 5 s after its 500 ms bound")
	}
}

// TestConcurrent407sMakeOneAsk: after the broker ends the grant early and then denies every ask,
// eight connections answered 407 at once make one ask between them, and eight more, one after
// another, make none: a failed ask for a new grant is not made again for each 407'd connection
// within replaceCooldown, so a refusing broker gets one denied request, not one per call.
//
// The named revert: drop the cooldown from Replace, and each 407 that comes after the denied ask
// makes an ask of its own.
func TestConcurrent407sMakeOneAsk(t *testing.T) {
	r := newRig(t, func(_ *Config, b *fakeBroker) { b.denyFrom = 2 })
	first := r.awaitGrant()
	r.proxy.setLive("pw-"+first.ID, false)

	const n = 8
	var wg sync.WaitGroup
	statuses := make([]string, n)
	for i := range n {
		wg.Go(func() { statuses[i], _ = r.connect("api.github.com:443") })
	}
	wg.Wait()
	for _, status := range statuses {
		if !strings.HasPrefix(status, "502") {
			t.Fatalf("concurrent CONNECTs after the grant ended = %v; want 502 for each, the new grant denied", statuses)
		}
	}
	if asks := r.broker.asks(); asks != 2 {
		t.Fatalf("the broker answered %d asks; want 2, the first grant and one ask for %d concurrent 407s", asks, n)
	}
	for range n {
		if status, _ := r.connect("api.github.com:443"); !strings.HasPrefix(status, "502") {
			t.Fatalf("a CONNECT after the denied ask = %q; want 502", status)
		}
	}
	if asks := r.broker.asks(); asks != 2 {
		t.Fatalf("the broker answered %d asks after %d more 407s one after another; want still 2", asks, n)
	}
}

// TestAHostTheKindBindsWaitsForTheFirstGrant: the hosts the forwarder is told the GitHub kind binds
// (Config.GitHubHosts) decide which CONNECTs wait for the first grant. A host the kind gained is
// held until the grant comes and then goes through the broker, and a host the kind does not bind
// goes out at once. The added host is a name that never resolves (.invalid), so a CONNECT that
// goes out directly fails at once and the test reaches no outside network.
//
// The named revert: have serveConn read a fixed list of GitHub's hosts in place of the hosts it
// is told, and the CONNECT to the added host goes out at once, before any grant.
func TestAHostTheKindBindsWaitsForTheFirstGrant(t *testing.T) {
	const host = "codeload.github.invalid"
	added := host + ":443"
	gate := make(chan struct{})
	r := newRig(t, func(cfg *Config, b *fakeBroker) {
		cfg.GitHubHosts = append(cfg.GitHubHosts, host)
		b.hosts = append(b.hosts, host)
		b.gate, b.entered = gate, make(chan struct{}, 1)
	})
	select {
	case <-r.broker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the forwarder made no ask")
	}
	public := echoServer(t, "public")
	if status, echoed := r.connect(public.Addr().String()); !strings.HasPrefix(status, "200") || echoed != "public:ping" {
		t.Fatalf("CONNECT %s before the first grant = %q, echo %q; want 200 at once", public.Addr(), status, echoed)
	}
	type answer struct {
		status, echoed string
		err            error
	}
	answered := make(chan answer, 1)
	go func() {
		status, echoed, err := r.tryConnect(added)
		answered <- answer{status, echoed, err}
	}()
	select {
	case got := <-answered:
		t.Fatalf("CONNECT %s before the first grant was answered at once (%q, %v); want it held for the grant", added, got.status, got.err)
	case <-time.After(500 * time.Millisecond):
	}
	close(gate)
	select {
	case got := <-answered:
		if got.err != nil || !strings.HasPrefix(got.status, "200") || got.echoed != "bound:ping" {
			t.Fatalf("CONNECT %s once the grant came = %q, echo %q, %v; want 200 through the broker", added, got.status, got.echoed, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("CONNECT %s was not answered once the grant came", added)
	}
	if calls := r.proxy.calls(); len(calls) != 1 || calls[0].target != added {
		t.Fatalf("the broker's proxy saw %+v; want the one CONNECT to %s", calls, added)
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
		_, err := Listen(Config{Role: "implementer", Listen: address, Secrets: []string{"ROLE_APP"}, GitHubHosts: []string{"api.github.com"},
			GHDir: t.TempDir(), Open: func() (Broker, error) { return nil, errors.New("unused") }})
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
