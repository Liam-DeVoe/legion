package natscred

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/brokerclient"
)

// ServiceUser is the CONNECT user a service presents beside its platform identity token as the
// password, which is how the auth service tells a service's credential from a session's.
const ServiceUser = "service"

// ServiceRenewBefore is how long before the token it connected with lapses a service's connection
// moves to a later one: the auth service answers a service's user with its token's expiry, and a
// projected Kubernetes token is rewritten by the kubelet at 80% of its life, a Google identity
// token minted fresh on each read, so a later one is there to read well before then.
const ServiceRenewBefore = 10 * time.Minute

// servicePoll is how often a service's Keeper, once its token is due, asks its reader for a later
// one, and checks that its reconnect has presented it.
const servicePoll = time.Second

// TokenReader reads a service's platform identity token as it is now, for each CONNECT.
type TokenReader interface {
	Read() (string, error)
}

// Service is the credential of a service: user ServiceUser and, as the password, the platform
// identity token reader answers at each CONNECT, the auth service verifying it and answering the
// grant BROKER_NATS_SERVICES lists its account for. The option sets IgnoreAuthErrorAbort: a
// refused token is retried, not given up on, so a service whose token the hub refused while the
// broker was down connects again once it answers. A token reader cannot read is sent as no
// password, which the auth service refuses and the client retries.
//
// The Keeper reads the expiry of the token the connection presented (unverified, its 'exp') and,
// ServiceRenewBefore ahead of it, waits for reader to answer a token that lapses later, then
// reconnects once, and waits for that reconnect to present it before it reads the next expiry.
func Service(reader TokenReader) (nats.Option, Keeper) {
	credential := &serviceCredential{reader: reader}
	return allOf(nats.UserInfoHandler(credential.userInfo), nats.IgnoreAuthErrorAbort()), credential
}

// serviceCredential is Service's state: the reader and the token the last CONNECT presented.
type serviceCredential struct {
	reader TokenReader

	mu        sync.Mutex
	presented string
}

// userInfo is the CONNECT's user and password: ServiceUser and the token reader answers now.
func (c *serviceCredential) userInfo() (string, string) {
	token, err := c.reader.Read()
	if err != nil {
		token = ""
	}
	c.mu.Lock()
	c.presented = token
	c.mu.Unlock()
	return ServiceUser, token
}

func (c *serviceCredential) token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.presented
}

// Keep reconnects nc with a later token ServiceRenewBefore ahead of each one it presented, until
// ctx ends or nc closes.
func (c *serviceCredential) Keep(ctx context.Context, nc *nats.Conn) {
	for {
		presented := c.token()
		expiry, err := expiryOf(presented)
		wait := servicePoll
		if err == nil {
			wait = time.Until(expiry.Add(-ServiceRenewBefore))
		}
		if !sleep(ctx, wait) || nc.IsClosed() {
			return
		}
		if err != nil || c.token() != presented {
			// Nothing readable was presented yet, or a reconnect of nats.go's own presented
			// another token while this waited: read the expiry again.
			continue
		}
		if !c.awaitLaterToken(ctx, nc, expiry) {
			return
		}
		if nc.ForceReconnect() != nil {
			return
		}
		for c.token() == presented {
			if !sleep(ctx, servicePoll) || nc.IsClosed() {
				return
			}
		}
	}
}

// awaitLaterToken waits, asking the reader every servicePoll, for a token that lapses after
// expiry, and reports false when ctx ended or nc closed first.
func (c *serviceCredential) awaitLaterToken(ctx context.Context, nc *nats.Conn, expiry time.Time) bool {
	for {
		if token, err := c.reader.Read(); err == nil {
			if later, err := expiryOf(token); err == nil && later.After(expiry) {
				return true
			}
		}
		if !sleep(ctx, servicePoll) || nc.IsClosed() {
			return false
		}
	}
}

// sleep waits d, reporting false when ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ServiceFile is the token in the file at path, read afresh at every Read: a projected Kubernetes
// ServiceAccount token, which the kubelet rewrites in place.
func ServiceFile(path string) TokenReader { return serviceFile(path) }

type serviceFile string

func (f serviceFile) Read() (string, error) {
	contents, err := os.ReadFile(string(f))
	if err != nil {
		return "", fmt.Errorf("natscred: read the service token %s: %w", string(f), err)
	}
	token := strings.TrimSpace(string(contents))
	if token == "" {
		return "", fmt.Errorf("natscred: the service token %s is empty", string(f))
	}
	return token, nil
}

// googleReadTimeout bounds one read of the metadata server.
const googleReadTimeout = 10 * time.Second

// ServiceGoogle is this VM's Google identity token for audience, minted by its metadata server at
// every Read (brokerclient.MetadataIdentity).
func ServiceGoogle(audience string) TokenReader {
	return serviceGoogle{identity: brokerclient.NewMetadataIdentity(audience)}
}

type serviceGoogle struct {
	identity *brokerclient.MetadataIdentity
}

func (g serviceGoogle) Read() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), googleReadTimeout)
	defer cancel()
	token, _, err := g.identity.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("natscred: read %s: %w", g.identity.Describe(), err)
	}
	return token, nil
}
