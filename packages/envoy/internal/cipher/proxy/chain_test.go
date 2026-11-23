// packages/envoy/internal/cipher/proxy/chain_test.go
// The chain a leaf is signed in: a mounted intermediate and its chain to a root, today's
// self-signed root, the roots a release carries, a leaf that ends no later than what signed it,
// and readiness, which is whether the authority holds a certificate it may still sign with.
package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testCertificate is one certificate of a test chain and its key.
type testCertificate struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// testRoot is a self-signed root valid for a month either side of now.
func testRoot(t *testing.T, name string) testCertificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-30 * 24 * time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour),
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCertificate{cert, key}
}

// testIntermediate is an authority of path length 0 that issuer signs, for names (none is no
// name constraint at all), from notBefore to notAfter.
func testIntermediate(t *testing.T, issuer testCertificate, names []string, notBefore, notAfter time.Time) testCertificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "test intermediate"},
		NotBefore: notBefore, NotAfter: notAfter,
		BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign,
		PermittedDNSDomains: names, PermittedDNSDomainsCritical: len(names) > 0,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, issuer.cert, &key.PublicKey, issuer.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCertificate{cert, key}
}

// writeChain writes signer's key and, in one file, signer's certificate and then each of
// chain's: the pair BROKER_AIRLOCK_CA_CERT_FILE and BROKER_AIRLOCK_CA_KEY_FILE name.
func writeChain(t *testing.T, signer testCertificate, chain ...*x509.Certificate) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	var certs []byte
	for _, cert := range append([]*x509.Certificate{signer.cert}, chain...) {
		certs = append(certs, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})...)
	}
	keyDER, err := x509.MarshalECPrivateKey(signer.key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "chain.crt"), filepath.Join(dir, "chain.key")
	writeFile(t, certFile, certs)
	writeFile(t, keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certFile, keyFile
}

// verifyLeaf verifies host's leaf as a command does: against root alone, with what the TLS
// answer carries after the leaf as its intermediates, at the moment at.
func verifyLeaf(t *testing.T, a *Authority, host string, root *x509.Certificate, at time.Time) *x509.Certificate {
	t.Helper()
	leaf, err := a.leaf(host)
	if err != nil {
		t.Fatalf("the leaf of %s: %v", host, err)
	}
	parsed, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(root)
	for _, der := range leaf.Certificate[1:] {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		intermediates.AddCert(cert)
	}
	if _, err := parsed.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: intermediates, CurrentTime: at}); err != nil {
		t.Fatalf("the leaf of %s does not verify against the root: %v", host, err)
	}
	return parsed
}

// TestAMountedIntermediateAndItsChainSign is k21's local half in this package: a mounted
// intermediate with its chain to its root signs every bound host, the TLS answer carries the
// leaf and the intermediate and not the root, a command that trusts the root alone verifies it,
// and the release carries that root.
func TestAMountedIntermediateAndItsChainSign(t *testing.T) {
	root := testRoot(t, "mounted root")
	intermediate := testIntermediate(t, root, []string{"s.airlock.test", "example.com"}, time.Now().Add(-time.Hour), time.Now().Add(7*24*time.Hour))
	certFile, keyFile := writeChain(t, intermediate, root.cert)
	authority, err := NewAuthority(certFile, keyFile, AuthorityOptions{Hosts: []string{"s.airlock.test", "*.example.com"}})
	if err != nil {
		t.Fatalf("NewAuthority on an intermediate and its chain: %v", err)
	}
	leaf, err := authority.leaf("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.Certificate) != 2 || !bytesEqual(leaf.Certificate[1], intermediate.cert.Raw) {
		t.Fatalf("the TLS answer carries %d certificate(s); want the leaf and the intermediate", len(leaf.Certificate))
	}
	verifyLeaf(t, authority, "api.example.com", root.cert, time.Now())
	verifyLeaf(t, authority, "s.airlock.test", root.cert, time.Now())
	if got := authority.ReleaseRoots(); got != string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.cert.Raw})) {
		t.Errorf("the release carries %q; want the root of the chain alone", got)
	}
	if err := authority.Ready(); err != nil {
		t.Errorf("Ready on a mounted intermediate within its life: %v", err)
	}
}

// TestTodaysSelfSignedRootStillSigns: until today's authority is retired, a mounted self-signed
// root signs as before, sends itself after the leaf, and is the root a release carries.
func TestTodaysSelfSignedRootStillSigns(t *testing.T) {
	root := testRoot(t, "today's authority")
	certFile, keyFile := writeChain(t, root)
	authority, err := NewAuthority(certFile, keyFile, AuthorityOptions{Hosts: []string{"s.airlock.test"}})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := authority.leaf("s.airlock.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.Certificate) != 2 || !bytesEqual(leaf.Certificate[1], root.cert.Raw) {
		t.Fatalf("the TLS answer carries %d certificate(s); want the leaf and the root, as today", len(leaf.Certificate))
	}
	verifyLeaf(t, authority, "s.airlock.test", root.cert, time.Now())
	if !strings.Contains(authority.ReleaseRoots(), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.cert.Raw}))) {
		t.Error("the release does not carry the self-signed root")
	}
}

// TestTheAuthorityRefusesAChainItCannotSignIn: an intermediate with no chain, a chain that
// ends at another root, a chain with no root at its end, an authority whose name constraints
// leave out a bound host, and one whose life is over. Each refusal names what is wrong.
func TestTheAuthorityRefusesAChainItCannotSignIn(t *testing.T) {
	root, other := testRoot(t, "root"), testRoot(t, "another root")
	now := time.Now()
	intermediate := testIntermediate(t, root, []string{"s.airlock.test"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	middle := testIntermediate(t, root, nil, now.Add(-time.Hour), now.Add(24*time.Hour))
	ended := testIntermediate(t, root, nil, now.Add(-48*time.Hour), now.Add(-time.Hour))
	for _, row := range []struct {
		name, want string
		certFile   func() (string, string)
	}{
		{"an intermediate with no chain to its root", "no chain to its root", func() (string, string) { return writeChain(t, intermediate) }},
		{"a chain that ends at another root", "does not chain", func() (string, string) { return writeChain(t, intermediate, other.cert) }},
		{"a chain whose last certificate is no root", "is not a self-signed root", func() (string, string) { return writeChain(t, intermediate, middle.cert) }},
		{"a name constraint that leaves out a bound host", "u.airlock.test", func() (string, string) { return writeChain(t, intermediate, root.cert) }},
		{"an authority whose life is over", "ended", func() (string, string) { return writeChain(t, ended, root.cert) }},
	} {
		t.Run(row.name, func(t *testing.T) {
			certFile, keyFile := row.certFile()
			authority, err := NewAuthority(certFile, keyFile, AuthorityOptions{Hosts: []string{"s.airlock.test", "u.airlock.test"}})
			if err == nil {
				t.Fatalf("NewAuthority built an authority; want a refusal naming %q", row.want)
			}
			if authority != nil {
				t.Error("NewAuthority answered an authority beside its refusal")
			}
			if !strings.Contains(err.Error(), row.want) || !strings.Contains(err.Error(), certFile) {
				t.Errorf("the refusal reads %q; want it to name %q and the file", err, row.want)
			}
		})
	}
}

// TestTheReleaseCarriesTheReleaseRootsBesideItsOwn: BROKER_PROXY_RELEASE_ROOTS_FILE's roots go
// in every release after the root of the authority's own chain, and a root named twice is
// carried once.
func TestTheReleaseCarriesTheReleaseRootsBesideItsOwn(t *testing.T) {
	today, next := testRoot(t, "today's authority"), testRoot(t, "the new root")
	certFile, keyFile := writeChain(t, today)
	authority, err := NewAuthority(certFile, keyFile, AuthorityOptions{ReleaseRoots: []*x509.Certificate{next.cert, today.cert}})
	if err != nil {
		t.Fatal(err)
	}
	want := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: today.cert.Raw})) +
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: next.cert.Raw}))
	if got := authority.ReleaseRoots(); got != want {
		t.Errorf("the release carries\n%s\nwant today's authority and then the new root, each once", got)
	}
	var nilAuthority *Authority
	if got := nilAuthority.ReleaseRoots(); got != "" {
		t.Errorf("a deployment with no authority releases %q; want nothing", got)
	}
}

// TestALeafEndsNoLaterThanItsIntermediate: a leaf lasts 25 hours at most from its start to its
// end, and one signed while its intermediate has less than that left ends with the
// intermediate. Once the intermediate has ended the authority signs nothing and is not ready.
func TestALeafEndsNoLaterThanItsIntermediate(t *testing.T) {
	root := testRoot(t, "root")
	now := time.Now().Truncate(time.Second)
	intermediate := testIntermediate(t, root, nil, now.Add(-time.Hour), now.Add(10*time.Hour))
	certFile, keyFile := writeChain(t, intermediate, root.cert)
	clock := &steppedClock{now: now}
	authority, err := NewAuthority(certFile, keyFile, AuthorityOptions{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	leaf := verifyLeaf(t, authority, "s.airlock.test", root.cert, now)
	if !leaf.NotAfter.Equal(intermediate.cert.NotAfter) {
		t.Errorf("the leaf ends %v; want the intermediate's end %v", leaf.NotAfter, intermediate.cert.NotAfter)
	}
	// Another authority with a long life: the leaf's whole validity is 25 hours at most.
	long := testIntermediate(t, root, nil, now.Add(-time.Hour), now.Add(7*24*time.Hour))
	certFile, keyFile = writeChain(t, long, root.cert)
	longAuthority, err := NewAuthority(certFile, keyFile, AuthorityOptions{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	leaf = verifyLeaf(t, longAuthority, "s.airlock.test", root.cert, now)
	if life := leaf.NotAfter.Sub(leaf.NotBefore); life > 25*time.Hour || life < 24*time.Hour {
		t.Errorf("the leaf runs %v from its start to its end; want 25 hours at most and a day at least", life)
	}

	clock.set(intermediate.cert.NotAfter)
	if _, err := authority.leaf("t.airlock.test"); err == nil || !strings.Contains(err.Error(), "ended") {
		t.Errorf("a leaf signed once the intermediate had ended: %v; want a refusal saying it ended", err)
	}
	if err := authority.Ready(); err == nil {
		t.Error("Ready once the intermediate had ended = nil; want not ready")
	}
}

// TestALeafNearItsIntermediatesEndIsNotSignedAgainForNothing: in the last hour of a leaf that
// already ends with its intermediate, a new one would end no later, so the one held is
// answered rather than a key made on every handshake.
func TestALeafNearItsIntermediatesEndIsNotSignedAgainForNothing(t *testing.T) {
	root := testRoot(t, "root")
	now := time.Now().Truncate(time.Second)
	intermediate := testIntermediate(t, root, nil, now.Add(-time.Hour), now.Add(3*time.Hour))
	certFile, keyFile := writeChain(t, intermediate, root.cert)
	clock := &steppedClock{now: now}
	authority, err := NewAuthority(certFile, keyFile, AuthorityOptions{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	first, err := authority.leaf("s.airlock.test")
	if err != nil {
		t.Fatal(err)
	}
	clock.set(intermediate.cert.NotAfter.Add(-leafRenewBefore / 2))
	again, err := authority.leaf("s.airlock.test")
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Error("the authority signed a leaf again that would end no later than the one it holds")
	}
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

// newTestAuthorityOn is newTestAuthority signing by clock.
func newTestAuthorityOn(t *testing.T, clock Clock) *Authority {
	t.Helper()
	certFile, keyFile := writeCAFiles(t)
	authority, err := NewAuthority(certFile, keyFile, AuthorityOptions{Clock: clock})
	if err != nil {
		t.Fatalf("the test certificate authority: %v", err)
	}
	return authority
}

// steppedClock is a clock a row moves by hand, which a waiter on it notices at once: each set
// wakes every SleepUntil, which looks at the clock again. It records each SleepUntil's target, so
// a row reads when a loop meant to wake without waiting for it.
type steppedClock struct {
	mu      sync.Mutex
	now     time.Time
	moved   chan struct{}
	targets []time.Time
}

func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppedClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
	if c.moved != nil {
		close(c.moved)
		c.moved = nil
	}
}

// sleeps is every target a SleepUntil has been called with, in order.
func (c *steppedClock) sleeps() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.targets...)
}

func (c *steppedClock) SleepUntil(ctx context.Context, t time.Time) bool {
	c.mu.Lock()
	c.targets = append(c.targets, t)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		if !c.now.Before(t) {
			c.mu.Unlock()
			return true
		}
		if c.moved == nil {
			c.moved = make(chan struct{})
		}
		moved := c.moved
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-moved:
		}
	}
}
