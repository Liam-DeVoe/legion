package githubproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// ProxyUser is the user name of the Proxy-Authorization credential the forwarder opens a broker
// connection with. The broker's proxy reads the password alone and leaves the user unread, so it
// names the program that built the credential, as the `agent-secrets` client's "agent-secrets"
// does, rather than anything of the grant's.
const ProxyUser = "legion-forwarder"

// Config is one forwarder: the role it serves, the loopback address it listens on, the role's
// secret names behind the proxy and the reason its grants are asked under, the gh bundle it
// writes the placeholders into, the account directory GH_CONFIG_DIR names, the configured renames
// the bundle routes a moved repository by, and how it reaches the broker. RenewBefore is
// githubproxy.RenewBefore unless a test shortens it; Open is LoadSession unless a test hands a
// fake.
type Config struct {
	Role         string
	Listen       string
	Secrets      []string
	Reason       string
	GHDir        string
	DefaultOwner string
	Renames      []ghrepo.Rename
	RenewBefore  time.Duration
	Open         func() (Broker, error)
	Log          *slog.Logger
}

// recoveryBound is how long a connection the broker's proxy answered 407 waits for the new grant
// before the forwarder answers it as failed: the 60 seconds within which a role's calls work again
// after the broker ends a grant early.
const recoveryBound = 60 * time.Second

// dialBound bounds each dial the forwarder makes, to the broker's proxy and to a host it opens
// itself.
const dialBound = 10 * time.Second

// Forwarder is a running forwarder: its listener and its grant keeper.
type Forwarder struct {
	cfg      Config
	listener net.Listener
	keeper   *keeper
	log      *slog.Logger
}

// Listen checks cfg and binds its address, which must be a loopback one: the role's own processes
// reach the forwarder, and nothing outside the pod may.
func Listen(cfg Config) (*Forwarder, error) {
	if cfg.Role == "" {
		return nil, errors.New("no role")
	}
	if len(cfg.Secrets) == 0 {
		return nil, errors.New("no secret names")
	}
	if cfg.GHDir == "" {
		return nil, errors.New("no gh directory")
	}
	if cfg.Open == nil {
		return nil, errors.New("no broker")
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen address %q: %w", cfg.Listen, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("listen address %q is no loopback address", cfg.Listen)
	}
	if cfg.RenewBefore == 0 {
		cfg.RenewBefore = RenewBefore
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	log = log.With("role", cfg.Role)
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	return &Forwarder{
		cfg: cfg, listener: listener, log: log,
		keeper: newKeeper(cfg.Open, cfg.Secrets, cfg.Reason, cfg.GHDir, cfg.DefaultOwner, cfg.Renames, cfg.RenewBefore, log),
	}, nil
}

// Addr is the address the forwarder listens on.
func (f *Forwarder) Addr() net.Addr { return f.listener.Addr() }

// Current is the grant the forwarder holds now, nil before the first.
func (f *Forwarder) Current() *Grant { return f.keeper.Current() }

// Serve keeps the role's grant and serves connections until ctx ends, then closes the listener
// and revokes the grants it holds, within stopBound.
func (f *Forwarder) Serve(ctx context.Context) error {
	f.log.Info("forwarder: listening", "addr", f.listener.Addr().String(), "names", f.cfg.Secrets, "gh_dir", f.cfg.GHDir)
	var wg sync.WaitGroup
	wg.Go(func() { f.keeper.keep(ctx) })
	go func() {
		<-ctx.Done()
		_ = f.listener.Close()
	}()
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var temporary interface{ Temporary() bool }
			if errors.As(err, &temporary) && temporary.Temporary() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		go f.serveConn(ctx, conn)
	}
	wg.Wait()
	stop, cancel := context.WithTimeout(context.Background(), stopBound)
	defer cancel()
	f.keeper.revokeHeld(stop)
	return nil
}

// stopBound bounds the revocation of the forwarder's grants at its stop: inside the four seconds
// its shim waits for it (internal/shim's forwarderStopWait), within the role's stop grace. A
// revocation cut short is no grant left live for long: the daemon revokes the generation's
// enrollment once the role has stopped, which ends every grant of it.
const stopBound = 3 * time.Second

// serveConn answers one client connection: a CONNECT is tunnelled, through the broker's proxy for
// a host the grant binds and directly otherwise; any other request is refused 405, since the role
// is told the forwarder for HTTPS alone.
func (f *Forwarder) serveConn(ctx context.Context, client net.Conn) {
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(client)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	_ = client.SetReadDeadline(time.Time{})
	if req.Method != http.MethodConnect {
		reply(client, http.StatusMethodNotAllowed, "the forwarder carries CONNECT alone")
		return
	}
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		reply(client, http.StatusBadRequest, "a CONNECT names host:port")
		return
	}
	grant := f.keeper.Current()
	if grant == nil {
		// Before the first grant the forwarder cannot tell a bound host from any other, so it waits
		// for one, within the recovery bound.
		waitCtx, cancel := context.WithTimeout(ctx, recoveryBound)
		grant, err = f.keeper.awaitFirst(waitCtx)
		cancel()
		if err != nil {
			f.log.Warn("forwarder: a connection came before any grant", "host", host, "err", err)
			reply(client, http.StatusServiceUnavailable, "the forwarder holds no grant of the role's GitHub secrets yet")
			return
		}
	}
	if !grant.Binds(host, port) {
		f.direct(client, reader, req.Host)
		return
	}
	f.throughBroker(ctx, client, reader, req.Host, grant)
}

// direct opens target itself and tunnels the client to it.
func (f *Forwarder) direct(client net.Conn, reader *bufio.Reader, target string) {
	upstream, err := net.DialTimeout("tcp", target, dialBound)
	if err != nil {
		reply(client, http.StatusBadGateway, "the forwarder could not reach the host")
		return
	}
	defer upstream.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	splice(client, reader, upstream, nil)
}

// throughBroker tunnels the client to target through the broker's proxy, opening that connection
// with grant's password. A 407 is the broker saying the grant no longer opens a connection — it
// ended it early — so the forwarder asks for a new grant at once and tries once more with that,
// within recoveryBound. Every other answer of the broker's is relayed to the client as it came,
// with its Cipher-Proxy-Code.
func (f *Forwarder) throughBroker(ctx context.Context, client net.Conn, reader *bufio.Reader, target string, grant *Grant) {
	upstream, resp, upstreamReader, err := connectThrough(grant, target)
	if err == nil && resp.StatusCode == http.StatusProxyAuthRequired {
		upstream.Close()
		f.log.Warn("forwarder: the broker's proxy refused the grant's password; asking for a new grant", "grant", grant.ID, "host", target, "code", resp.Header.Get("Cipher-Proxy-Code"))
		recovery, cancel := context.WithTimeout(ctx, recoveryBound)
		replaced, replaceErr := f.keeper.Replace(recovery, grant)
		cancel()
		if replaceErr != nil || replaced.ID == grant.ID {
			reply(client, http.StatusBadGateway, "the broker ended the role's grant and gave no new one")
			return
		}
		upstream, resp, upstreamReader, err = connectThrough(replaced, target)
	}
	if err != nil {
		f.log.Warn("forwarder: could not reach the broker's proxy", "host", target, "err", err)
		reply(client, http.StatusBadGateway, "the forwarder could not reach the broker's proxy")
		return
	}
	defer upstream.Close()
	if resp.StatusCode != http.StatusOK {
		code := resp.Header.Get("Cipher-Proxy-Code")
		f.log.Warn("forwarder: the broker's proxy refused a connection", "host", target, "status", resp.StatusCode, "code", code)
		header := ""
		if code != "" {
			header = "Cipher-Proxy-Code: " + code + "\r\n"
		}
		fmt.Fprintf(client, "HTTP/1.1 %d %s\r\n%sContent-Length: 0\r\nConnection: close\r\n\r\n", resp.StatusCode, http.StatusText(resp.StatusCode), header)
		return
	}
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	splice(client, reader, upstream, upstreamReader)
}

// connectThrough opens a connection to grant's proxy, over TLS where its URL is https (trusting
// the system's roots and the release's own certificate authority), and sends a CONNECT to target
// with the grant's password, answering the connection, the proxy's answer and the reader that
// holds any bytes past it.
func connectThrough(grant *Grant, target string) (net.Conn, *http.Response, *bufio.Reader, error) {
	address := grant.ProxyURL.Host
	if grant.ProxyURL.Port() == "" {
		port := "80"
		if grant.ProxyURL.Scheme == "https" {
			port = "443"
		}
		address = net.JoinHostPort(grant.ProxyURL.Hostname(), port)
	}
	conn, err := net.DialTimeout("tcp", address, dialBound)
	if err != nil {
		return nil, nil, nil, err
	}
	if grant.ProxyURL.Scheme == "https" {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		roots.AppendCertsFromPEM([]byte(grant.CA))
		secured := tls.Client(conn, &tls.Config{ServerName: grant.ProxyURL.Hostname(), RootCAs: roots, MinVersion: tls.VersionTLS12})
		if err := secured.Handshake(); err != nil {
			conn.Close()
			return nil, nil, nil, err
		}
		conn = secured
	}
	credential := base64.StdEncoding.EncodeToString([]byte(ProxyUser + ":" + grant.Password))
	_ = conn.SetDeadline(time.Now().Add(dialBound))
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, credential); err != nil {
		conn.Close()
		return nil, nil, nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, nil, nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, resp, reader, nil
}

// splice copies both ways between the client and upstream until either side ends, sending what
// each buffered reader already holds first.
func splice(client net.Conn, clientReader *bufio.Reader, upstream net.Conn, upstreamReader *bufio.Reader) {
	var fromUpstream io.Reader = upstream
	if upstreamReader != nil {
		fromUpstream = upstreamReader
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, clientReader)
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, fromUpstream)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
}

// closeWrite half-closes conn where it can, so the far side reads the end of what was sent.
func closeWrite(conn net.Conn) {
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
		return
	}
	_ = conn.Close()
}

// reply answers a client's CONNECT with status and text and closes nothing: the caller does.
func reply(client net.Conn, status int, text string) {
	body := strings.TrimSpace(text) + "\n"
	fmt.Fprintf(client, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, http.StatusText(status), len(body), body)
}
