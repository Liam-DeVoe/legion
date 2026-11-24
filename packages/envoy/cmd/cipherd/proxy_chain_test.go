// packages/envoy/cmd/cipherd/proxy_chain_test.go
// The broker binary on each form of the proxy's chain: an intermediate from a pool (here the
// stand-in of Certificate Authority Service the local stack runs), which it is not ready
// without, and a mounted intermediate with its chain; and what it refuses to start on.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules/proxyrulestest"
	"github.com/sjawhar/envoy/internal/cipher/store/storetest"
	"github.com/sjawhar/envoy/internal/testwait"
)

const testProxyPool = "projects/example-project/locations/us-west1/caPools/proxy"

// localProxyEnv is what a broker with a proxy is started with on the local stack, less the
// authority: the database at databaseURL, a port of its own, a fake secret store, which makes it
// a local run, and a profiles file binding one host.
func localProxyEnv(t *testing.T, databaseURL string) []string {
	t.Helper()
	fakeSecretsFile := filepath.Join(t.TempDir(), "fake-secrets.json")
	if err := os.WriteFile(fakeSecretsFile, []byte(`{"secrets": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{
		"BROKER_DATABASE_URL=" + databaseURL,
		"BROKER_LISTEN_ADDR=127.0.0.1:0",
		"BROKER_PUBLIC_URL=http://127.0.0.1:0",
		"BROKER_FAKE_SECRETS_FILE=" + fakeSecretsFile,
		"BROKER_PROFILES_FILE=" + profilesFile(t, `{"profiles": [{"name": "DEEL_API_KEY", "proxy": {"hosts": ["api.deel.com"]}}]}`),
		"BROKER_AIRLOCK_PROXY_URL=http://127.0.0.1:13381",
		"BROKER_AIRLOCK_LISTEN_ADDR=127.0.0.1:0",
		"KUBERNETES_SERVICE_HOST=",
	}
}

// healthz answers GET /healthz at addr: its status and body.
func healthz(t *testing.T, addr string) (int, string) {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET http://%s/healthz: %v", addr, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// TestABrokerOnAPoolIsNotReadyUntilItHoldsAnIntermediate: a broker whose settings name a pool
// serves, and answers 503 NO_INTERMEDIATE while the service fails every request, logging each
// failure; once the service answers it logs its intermediate, with the names of its bound host,
// and is ready. The stand-in is sent no credential.
func TestABrokerOnAPoolIsNotReadyUntilItHoldsAnIntermediate(t *testing.T) {
	stand, err := proxyrulestest.NewCAStand(proxyrulestest.CAOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := proxyrulestest.NewService(proxyrulestest.ServiceOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	service.AnswerWith(stand.Answer)
	stand.Fail(true)
	broker := startBroker(t, brokerOptions{Env: append(localProxyEnv(t, storetest.URL(t)),
		"BROKER_PROXY_CA_POOL="+testProxyPool,
		"BROKER_PROXY_TEST_CA_URL="+service.URL(),
	)})
	broker.AwaitRecord(t, "proxy intermediate request failed", map[string]any{"pool": testProxyPool})
	if status, body := healthz(t, broker.Addr); status != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"NO_INTERMEDIATE"`) {
		t.Fatalf("GET /healthz while the pool fails = %d %s; want 503 NO_INTERMEDIATE", status, body)
	}
	stand.Fail(false)
	line := broker.AwaitRecord(t, "proxy intermediate", map[string]any{"names": "api.deel.com"})
	issued := stand.Issued()
	if len(issued) != 1 || line["serial"] != issued[0].SerialNumber.Text(16) {
		t.Fatalf("the broker logged %v; want the serial of the one certificate the stand issued", line)
	}
	testwait.Until(t, "GET /healthz to answer 200 once the broker holds its intermediate", func() (string, bool) {
		status, body := healthz(t, broker.Addr)
		return body, status == http.StatusOK
	})
	for _, seen := range service.Requests() {
		if got := seen.Header.Get("Authorization"); got != "" {
			t.Fatalf("the stand-in was sent the credential %q; want none", got[:min(len(got), 12)])
		}
	}
}

// TestABrokerStartsOnAMountedIntermediateAndItsChain is the local half of k21's control: a
// broker whose mounted pair is an intermediate and its chain to its root starts, ready, and logs
// that intermediate.
func TestABrokerStartsOnAMountedIntermediateAndItsChain(t *testing.T) {
	certFile, keyFile, intermediate := mountedIntermediate(t, []string{"api.deel.com"})
	broker := startBroker(t, brokerOptions{Env: append(localProxyEnv(t, storetest.URL(t)),
		"BROKER_AIRLOCK_CA_CERT_FILE="+certFile,
		"BROKER_AIRLOCK_CA_KEY_FILE="+keyFile,
	)})
	broker.AwaitRecord(t, "proxy intermediate", map[string]any{"serial": intermediate.SerialNumber.Text(16), "self_signed": false})
	requireHealthy(t, broker.Addr)
}

// TestMainRefusesAProxyChainItCannotStartOn: an intermediate whose name constraints leave out
// the bound host, and a clock file that holds no duration, each refuse the start naming what is
// wrong. The database is unreachable, so each refusal is made before the broker reaches it.
func TestMainRefusesAProxyChainItCannotStartOn(t *testing.T) {
	binary := buildBroker(t)
	const unreachable = "postgres://nonexistent-host-this-test-must-never-reach/db"
	elsewhereCert, elsewhereKey, _ := mountedIntermediate(t, []string{"elsewhere.example"})
	boundCert, boundKey, _ := mountedIntermediate(t, []string{"api.deel.com"})
	for _, row := range []struct {
		name string
		env  []string
		want []string
	}{
		{"a mounted chain that signs nothing for the bound host", []string{
			"BROKER_AIRLOCK_CA_CERT_FILE=" + elsewhereCert, "BROKER_AIRLOCK_CA_KEY_FILE=" + elsewhereKey,
		}, []string{"BROKER_AIRLOCK_CA_CERT_FILE", "api.deel.com"}},
		{"a clock file with no duration", []string{
			"BROKER_AIRLOCK_CA_CERT_FILE=" + boundCert, "BROKER_AIRLOCK_CA_KEY_FILE=" + boundKey,
			"BROKER_PROXY_TEST_CLOCK_OFFSET_FILE=" + writeTempFile(t, "clock-offset", []byte("two days\n")),
		}, []string{"BROKER_PROXY_TEST_CLOCK_OFFSET_FILE", "no duration"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			out := runRefusedBroker(t, binary, append(localProxyEnv(t, unreachable), row.env...)...)
			for _, want := range row.want {
				if !strings.Contains(out, want) {
					t.Fatalf("the broker refused, but its output does not name %q: %s", want, out)
				}
			}
		})
	}
}

// TestTheReleaseRootsFileHoldsRootsAlone: a file of two authorities is read in order; one that
// cannot be read, holds nothing, holds a key - which would otherwise ride in every release - or a
// certificate that is no authority is refused, naming the setting.
func TestTheReleaseRootsFileHoldsRootsAlone(t *testing.T) {
	first, second := testAuthority(t, "today's authority"), testAuthority(t, "the new root")
	roots, err := readProxyReleaseRoots(writeTempFile(t, "roots.pem", slices.Concat(first.CertificatePEM(), second.CertificatePEM())))
	if err != nil || len(roots) != 2 || roots[0].Subject.CommonName != "today's authority" || roots[1].Subject.CommonName != "the new root" {
		t.Fatalf("readProxyReleaseRoots of two roots = %d roots, %v; want both, in order", len(roots), err)
	}
	if roots, err := readProxyReleaseRoots(""); roots != nil || err != nil {
		t.Fatalf("readProxyReleaseRoots of no path = %v, %v; want none", roots, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	_, leafPEM, err := first.Leaf([]string{"leaf.example"})
	if err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"a path that cannot be read":         filepath.Join(t.TempDir(), "absent.pem"),
		"a file with no certificate":         writeTempFile(t, "empty.pem", []byte("no PEM here\n")),
		"a key beside a root":                writeTempFile(t, "key.pem", slices.Concat(first.CertificatePEM(), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))),
		"a certificate that is no authority": writeTempFile(t, "leaf.pem", leafPEM),
	} {
		if _, err := readProxyReleaseRoots(path); err == nil || !strings.Contains(err.Error(), "BROKER_PROXY_RELEASE_ROOTS_FILE") {
			t.Errorf("%s: readProxyReleaseRoots = %v; want a refusal naming the setting", name, err)
		}
	}
}

// testAuthority is a self-signed test authority whose common name is name.
func testAuthority(t *testing.T, name string) *proxyrulestest.Authority {
	t.Helper()
	authority, err := proxyrulestest.NewAuthority(name)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

// mountedIntermediate writes an intermediate of path length 0 for names, signed by a root of
// its own, as the pair a deployment mounts: the certificate file holds the intermediate and then
// the root, the key file the intermediate's key.
func mountedIntermediate(t *testing.T, names []string) (certFile, keyFile string, intermediate *x509.Certificate) {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mounted test root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour),
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(4242), Subject: pkix.Name{CommonName: "mounted test intermediate"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(7*24*time.Hour - time.Hour),
		BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign,
		ExtKeyUsage:         []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomains: names, PermittedDNSDomainsCritical: true,
		ExcludedIPRanges: []*net.IPNet{{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)}, {IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	if intermediate, err = x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})...)
	return writeTempFile(t, "chain.crt", chain), writeTempFile(t, "chain.key", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})), intermediate
}
