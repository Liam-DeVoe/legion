package kindtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/tlssites"
)

// NPMHost is the npm registry, which the rig stands in for too: a Claude Code role's install of its
// pinned release (internal/runtime/sandbox's claudeCodeBlock, `npm ci` by the lock's resolved
// URLs) and a stale-plugin install of a published version (`bun add`) fetch from it.
const NPMHost = "registry.npmjs.org"

// TLSSites is the rig's stand-in for the public hosts the cluster's pods reach over TLS beside
// GitHub: the sites the daemon's image probe reaches (internal/tlssites) and the npm registry
// (NPMHost), so no pod of a kind test leaves the runner. The cluster's pods reach it under the
// hosts' own names (kind_sandbox_route_stand_ins), and Image is built to read it as the probe reads
// the real sites: the emSign and AAA stand-ins serve chains that end at roots of the rig's own,
// which Image carries at the paths of the real pinned roots (tlssites.Site.Root) and holds in no
// trust store, so every client refuses them, as the probe requires, and the positive controls
// verify them with those files; the control, the isolation endpoint and the registry serve chains
// of the rig's sites authority (AuthorityPEM), which Image trusts as it trusts the fake GitHub's, so
// every client accepts the control and no pinned root verifies the isolation endpoint. Measured on
// 2026-11-21 in an INFRA issue pod, with the stand-ins on 127.0.0.1:443 and Image's changes made
// in namespaces of their own: the probe's positive controls and every client that pod's image
// carries passed (az and mise were not on its PATH).
//
// The answers follow what the probe was measured against: each compromised site serves a 200
// text/plain page at every path, the control a 200 HTML page at / and 404 elsewhere, and the
// isolation endpoint a 200 HTML page. The registry serves the files a test gives it (ServeNPMLock,
// PublishNPM) and 404 for any other path.
type TLSSites struct {
	server *http.Server
	addr   string
	// roots is each compromised site's stand-in root, as PEM, by the path Image carries it at.
	roots map[string][]byte
	// authority is the sites authority, as PEM.
	authority []byte

	mu sync.Mutex
	// npm is the registry's files by path.
	npm map[string][]byte
	// requests is every request the stand-ins answered: "<method> <host><path> -> <status>".
	requests []string
}

// StartTLSSites serves the stand-ins on listen (host:port) until t ends.
func StartTLSSites(t *testing.T, listen string) *TLSSites {
	t.Helper()
	authority, authorityKey := newRoot(t, "legion kind rig sites authority")
	s := &TLSSites{roots: map[string][]byte{}, authority: pemOf(authority), npm: map[string][]byte{}}
	certificates := map[string]tls.Certificate{}
	for _, site := range []tlssites.Site{tlssites.EmSign, tlssites.AAA} {
		root, key := newRoot(t, "stand-in for the root of "+site.Name)
		s.roots[site.Root] = pemOf(root)
		certificates[host(site)] = leafOf(t, host(site), root, key)
	}
	for _, name := range []string{host(tlssites.Control), host(tlssites.Isolation), NPMHost} {
		certificates[name] = leafOf(t, name, authority, authorityKey)
	}
	listener, err := tls.Listen("tcp", listen, &tls.Config{GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if certificate, ok := certificates[hello.ServerName]; ok {
			return &certificate, nil
		}
		return nil, fmt.Errorf("the rig's TLS sites serve no %q", hello.ServerName)
	}})
	if err != nil {
		t.Fatalf("listen for the rig's TLS sites on %s: %v", listen, err)
	}
	s.addr = listener.Addr().String()
	s.server = &http.Server{Handler: http.HandlerFunc(s.answer), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.server.Serve(listener) }()
	t.Cleanup(func() { _ = s.server.Close() })
	return s
}

// Addr is the address the stand-ins listen on.
func (s *TLSSites) Addr() string { return s.addr }

// Hosts is the name of every host the stand-ins serve.
func (s *TLSSites) Hosts() []string {
	return []string{host(tlssites.EmSign), host(tlssites.AAA), host(tlssites.Control), host(tlssites.Isolation), NPMHost}
}

// AuthorityPEM is the sites authority, which Image trusts.
func (s *TLSSites) AuthorityPEM() []byte { return s.authority }

// RootsPEM is each compromised site's stand-in root by the path Image carries it at.
func (s *TLSSites) RootsPEM() map[string][]byte { return s.roots }

// Requests is every request the stand-ins answered, in order.
func (s *TLSSites) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// answer answers a request as the host it names answered the probe, or as the registry serves
// the files it was given, and records it.
func (s *TLSSites) answer(w http.ResponseWriter, r *http.Request) {
	name, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		name = r.Host
	}
	status := http.StatusOK
	switch name {
	case host(tlssites.EmSign), host(tlssites.AAA):
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "the kind rig's stand-in for %s\n", name)
	case host(tlssites.Control):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path != "/" {
			status = http.StatusNotFound
			w.WriteHeader(status)
		}
		fmt.Fprintln(w, "<!doctype html><title>Example Domain</title><p>the kind rig's stand-in</p>")
	case NPMHost:
		s.mu.Lock()
		body, ok := s.npm[r.URL.Path]
		s.mu.Unlock()
		switch {
		case !ok:
			status = http.StatusNotFound
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprintln(w, `{"error":"Not found"}`)
		case strings.HasSuffix(r.URL.Path, ".tgz"):
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintln(w, "<!doctype html><title>the kind rig's stand-in</title>")
	}
	s.mu.Lock()
	s.requests = append(s.requests, fmt.Sprintf("%s %s%s -> %d", r.Method, name, r.URL.Path, status))
	s.mu.Unlock()
}

// ServeNPMLock serves, at the paths of their resolved URLs, the registry tarballs of lock (an npm
// lockfile, version 2 or 3) that dir holds under their file names, each held to the lock's
// integrity first. It fails t on a tarball whose bytes the lock does not hold, and when dir holds
// none of the lock's tarballs.
func (s *TLSSites) ServeNPMLock(t *testing.T, lock, dir string) {
	t.Helper()
	body, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Packages map[string]struct {
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("read the npm lock %s: %v", lock, err)
	}
	served := 0
	for _, key := range slices.Sorted(maps.Keys(parsed.Packages)) {
		entry := parsed.Packages[key]
		resolved, err := url.Parse(entry.Resolved)
		if entry.Resolved == "" || err != nil || resolved.Host != NPMHost {
			continue
		}
		tarball, err := os.ReadFile(filepath.Join(dir, path.Base(resolved.Path)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := npmIntegrity(tarball); got != entry.Integrity {
			t.Fatalf("%s in %s is not %s's tarball: its integrity is %s, and %s holds %s", path.Base(resolved.Path), dir, key, got, lock, entry.Integrity)
		}
		s.mu.Lock()
		s.npm[resolved.Path] = tarball
		s.mu.Unlock()
		served++
		t.Logf("the rig's npm registry serves %s (%d bytes), %s's tarball", resolved.Path, len(tarball), key)
	}
	if served == 0 {
		t.Fatalf("%s holds no tarball of %s", dir, lock)
	}
}

// PublishNPM serves a package of the registry whose one file is manifest, its package.json, which
// names the package and its version: its packument at /<name> and its tarball at
// /<name>/-/<base name>-<version>.tgz, as the registry lays them out.
func (s *TLSSites) PublishNPM(t *testing.T, manifest map[string]any) {
	t.Helper()
	name, _ := manifest["name"].(string)
	version, _ := manifest["version"].(string)
	if name == "" || version == "" {
		t.Fatalf("an npm manifest names its package and version: %v", manifest)
	}
	packageJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var tarball bytes.Buffer
	zipped := gzip.NewWriter(&tarball)
	archive := tar.NewWriter(zipped)
	if err := archive.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(packageJSON)), ModTime: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write(packageJSON); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	tarballPath := "/" + name + "/-/" + path.Base(name) + "-" + version + ".tgz"
	shasum := sha1.Sum(tarball.Bytes())
	packument, err := json.Marshal(map[string]any{
		"name":      name,
		"dist-tags": map[string]string{"latest": version},
		"modified":  time.Now().UTC().Format(time.RFC3339),
		"versions": map[string]any{version: map[string]any{
			"name": name, "version": version,
			"dist": map[string]string{
				"tarball":   "https://" + NPMHost + tarballPath,
				"integrity": npmIntegrity(tarball.Bytes()),
				"shasum":    hex.EncodeToString(shasum[:]),
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.npm["/"+name] = packument
	s.npm[tarballPath] = tarball.Bytes()
	s.mu.Unlock()
}

// npmIntegrity is the sha512 integrity npm records for body.
func npmIntegrity(body []byte) string {
	sum := sha512.Sum512(body)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func host(site tlssites.Site) string {
	parsed, err := url.Parse(site.URL)
	if err != nil {
		panic(fmt.Sprintf("tlssites.%s's URL %q: %v", site.Name, site.URL, err))
	}
	return parsed.Hostname()
}

// newRoot is a fresh self-signed authority named name, valid from an hour before now for a day.
func newRoot(t *testing.T, name string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key := newKey(t)
	template := &x509.Certificate{SerialNumber: serial(t), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return root, key
}

// leafOf is a server certificate for name that root's key signed, served alone, as the probe's
// sites serve no root.
func leafOf(t *testing.T, name string, root *x509.Certificate, rootKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	key := newKey(t)
	template := &x509.Certificate{SerialNumber: serial(t), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func serial(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func pemOf(certificate *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
}
