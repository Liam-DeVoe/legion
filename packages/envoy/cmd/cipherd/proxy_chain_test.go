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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules/proxyrulestest"
	"github.com/sjawhar/envoy/internal/cipher/store/storetest"
	"github.com/sjawhar/envoy/internal/testwait"
)

const testProxyPool = "projects/example-project/locations/us-west1/caPools/proxy"

// localProxyEnv is what a broker with a proxy is started with on the local stack, less the
// authority: its database, a port of its own, a fake secret store, which makes it a local run,
// and a profiles file binding one host.
func localProxyEnv(t *testing.T) []string {
	t.Helper()
	fakeSecretsFile := filepath.Join(t.TempDir(), "fake-secrets.json")
	if err := os.WriteFile(fakeSecretsFile, []byte(`{"secrets": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{
		"BROKER_DATABASE_URL=" + storetest.URL(t),
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
	broker := startBroker(t, brokerOptions{Env: append(localProxyEnv(t),
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
	broker := startBroker(t, brokerOptions{Env: append(localProxyEnv(t),
		"BROKER_AIRLOCK_CA_CERT_FILE="+certFile,
		"BROKER_AIRLOCK_CA_KEY_FILE="+keyFile,
	)})
	broker.AwaitRecord(t, "proxy intermediate", map[string]any{"serial": intermediate.SerialNumber.Text(16), "self_signed": false})
	requireHealthy(t, broker.Addr)
}

// TestMainRefusesAMountedChainItCannotSignIn: an intermediate whose name constraints leave out
// the bound host refuses the start, naming the host and the two settings.
func TestMainRefusesAMountedChainItCannotSignIn(t *testing.T) {
	certFile, keyFile, _ := mountedIntermediate(t, []string{"elsewhere.example"})
	out := runRefusedBroker(t, buildBroker(t), append(localProxyEnv(t),
		"BROKER_AIRLOCK_CA_CERT_FILE="+certFile,
		"BROKER_AIRLOCK_CA_KEY_FILE="+keyFile,
	)...)
	for _, want := range []string{"BROKER_AIRLOCK_CA_CERT_FILE", "api.deel.com"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the broker refused, but its output does not name %q: %s", want, out)
		}
	}
}

// TestMainRefusesAClockFileItCannotRead: the local stack's clock file is read at start, so one
// that holds no duration refuses the start naming the setting rather than running on the
// system's clock.
func TestMainRefusesAClockFileItCannotRead(t *testing.T) {
	certFile, keyFile, _ := mountedIntermediate(t, []string{"api.deel.com"})
	out := runRefusedBroker(t, buildBroker(t), append(localProxyEnv(t),
		"BROKER_AIRLOCK_CA_CERT_FILE="+certFile,
		"BROKER_AIRLOCK_CA_KEY_FILE="+keyFile,
		"BROKER_PROXY_TEST_CLOCK_OFFSET_FILE="+writeTempFile(t, "clock-offset", []byte("two days\n")),
	)...)
	if !strings.Contains(out, "BROKER_PROXY_TEST_CLOCK_OFFSET_FILE") || !strings.Contains(out, "no duration") {
		t.Fatalf("the broker refused, but its output does not name the clock file and its fault: %s", out)
	}
}

// TestTheReleaseRootsFileHoldsRootsAlone: a file of two authorities is read in order; one that
// cannot be read, holds nothing, holds a key - which would otherwise ride in every release - or a
// certificate that is no authority is refused, naming the setting.
func TestTheReleaseRootsFileHoldsRootsAlone(t *testing.T) {
	first, second := testRootPEM(t, "today's authority", true), testRootPEM(t, "the new root", true)
	roots, err := readProxyReleaseRoots(writeTempFile(t, "roots.pem", append(append([]byte{}, first...), second...)))
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
	for name, path := range map[string]string{
		"a path that cannot be read":         filepath.Join(t.TempDir(), "absent.pem"),
		"a file with no certificate":         writeTempFile(t, "empty.pem", []byte("no PEM here\n")),
		"a key beside a root":                writeTempFile(t, "key.pem", append(append([]byte{}, first...), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...)),
		"a certificate that is no authority": writeTempFile(t, "leaf.pem", testRootPEM(t, "a leaf", false)),
	} {
		if _, err := readProxyReleaseRoots(path); err == nil || !strings.Contains(err.Error(), "BROKER_PROXY_RELEASE_ROOTS_FILE") {
			t.Errorf("%s: readProxyReleaseRoots = %v; want a refusal naming the setting", name, err)
		}
	}
}

// testRootPEM is one self-signed certificate, an authority when ca says so, as PEM.
func testRootPEM(t *testing.T, name string, ca bool) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		BasicConstraintsValid: true, IsCA: ca, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
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
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(7 * 24 * time.Hour),
		BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign,
		PermittedDNSDomains: names, PermittedDNSDomainsCritical: true,
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
