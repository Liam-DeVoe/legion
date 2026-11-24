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
	"io"
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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// NPMHost is the npm registry, which the rig stands in for too: a Claude Code role's install of its
// pinned release (internal/runtime/sandbox's claudeCodeBlock, `npm ci` by the lock's resolved
// URLs) and a stale-plugin install of a published version (`bun add`) fetch from it.
const NPMHost = "registry.npmjs.org"

// WebSearchHost is Parallel's keyless MCP endpoint, the search provider the pinned Oh My Pi's
// web_search tool tries first and always, with no key and no setting (the "web" role's defaults
// in its catalog, packages/catalog/src/compat/rules/runtime/behavior.kdl, and
// packages/coding-agent/src/web/search/providers/parallel.ts at the .omp-pin tag): each pod's
// capability report runs one search through it (packages/pi-legion/src/capability-report.ts,
// checkWebSearch), and web-search stays closed only when a provider answers.
const WebSearchHost = "search.parallel.ai"

// TLSSites is the rig's stand-in for the public hosts the cluster's pods reach over TLS beside
// GitHub: the npm registry (NPMHost) and the pods' web-search provider (WebSearchHost), so no pod
// of a kind test leaves the runner. The cluster's pods reach it under the hosts' own names
// (kind_sandbox_route_stand_ins), and each host serves a chain of the rig's sites authority
// (AuthorityPEM), which Image trusts as it trusts the fake GitHub's.
//
// The registry serves the files a test gives it (ServeNPMLock, PublishNPM) and 404 for any other
// path. The web-search provider answers a search with one result (answerWebSearch).
type TLSSites struct {
	server *http.Server
	addr   string
	// authority is the sites authority, as PEM.
	authority []byte

	mu sync.Mutex
	// npm is the registry's files by path.
	npm map[string][]byte
	// requests is every request the stand-ins answered: "<method> <host><path> -> <status>", and
	// its body after a space where it had one (recordedBody).
	requests []string
}

// StartTLSSites serves the stand-ins on listen (host:port) until t ends.
func StartTLSSites(t *testing.T, listen string) *TLSSites {
	t.Helper()
	authority, authorityKey := newRoot(t, "legion kind rig sites authority")
	s := &TLSSites{authority: pemOf(authority), npm: map[string][]byte{}}
	certificates := map[string]tls.Certificate{}
	for _, name := range []string{NPMHost, WebSearchHost} {
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
	return []string{NPMHost, WebSearchHost}
}

// AuthorityPEM is the sites authority, which Image trusts.
func (s *TLSSites) AuthorityPEM() []byte { return s.authority }

// Requests is every request the stand-ins answered, in order, each with its body.
func (s *TLSSites) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// maxRecordedBody is the most of a request's body the stand-ins read and record.
const maxRecordedBody = 1 << 20

// answer answers a request as the registry serves the files it was given, or as the web-search
// provider answers a search, and records it with its body.
func (s *TLSSites) answer(w http.ResponseWriter, r *http.Request) {
	name, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		name = r.Host
	}
	received, _ := io.ReadAll(io.LimitReader(r.Body, maxRecordedBody))
	status := http.StatusOK
	switch name {
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
	case WebSearchHost:
		status = answerWebSearch(w, r, received)
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintln(w, "<!doctype html><title>the kind rig's stand-in</title>")
	}
	line := fmt.Sprintf("%s %s%s -> %d", r.Method, name, r.URL.Path, status)
	if len(received) > 0 {
		line += " " + recordedBody(received)
	}
	s.mu.Lock()
	s.requests = append(s.requests, line)
	s.mu.Unlock()
}

// recordedBody is a request's body as one line of the requests the stand-ins keep: JSON compacted,
// as it was sent, and anything else quoted.
func recordedBody(body []byte) string {
	var compact bytes.Buffer
	if json.Compact(&compact, body) == nil {
		return compact.String()
	}
	return strconv.Quote(string(body))
}

// webSearchResult is the one result the web-search stand-in answers every search with: a page of
// Jujutsu's documentation, which a pod's capability check searches for ("jujutsu version
// control", capability-report.ts), with the title and excerpt Parallel's results carry.
var webSearchResult = map[string]any{
	"url":      "https://jj-vcs.github.io/jj/latest/",
	"title":    "Jujutsu, as the kind rig's web-search stand-in answers it",
	"excerpts": []string{"Jujutsu is a Git-compatible version control system."},
}

// answerWebSearch answers a request to WebSearchHost as Parallel's keyless MCP endpoint answers the
// pinned Oh My Pi's web_search (packages/coding-agent/src/web/search/providers/parallel.ts and
// src/mcp/json-rpc.ts at the .omp-pin tag), and returns the status it wrote. The provider POSTs one
// JSON-RPC 2.0 request to /mcp, a tools/call of web_search, with no initialize before it. The
// answer is a JSON-RPC result whose id is the request's own, of the same JSON type, since the
// provider compares the two with ===, and whose structuredContent, which the provider reads before
// any text content, holds one result with a url, all the provider requires of an answer; its
// content carries the same as text, as an MCP tool's result does. A request of another shape is
// refused, naming what it was, so a change of the exchange shows in the requests the rig keeps
// rather than as an answer the provider misreads.
func answerWebSearch(w http.ResponseWriter, r *http.Request, body []byte) int {
	const wants = "the kind rig's web-search stand-in answers a JSON-RPC 2.0 tools/call of web_search POSTed to /mcp"
	switch {
	case r.URL.Path != "/mcp":
		http.Error(w, wants+", and this is "+r.URL.Path, http.StatusNotFound)
		return http.StatusNotFound
	case r.Method != http.MethodPost:
		http.Error(w, wants+", and this is a "+r.Method, http.StatusMethodNotAllowed)
		return http.StatusMethodNotAllowed
	}
	var call struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &call); err != nil || call.JSONRPC != "2.0" || len(call.ID) == 0 {
		http.Error(w, wants+", and this is no JSON-RPC 2.0 request with an id", http.StatusBadRequest)
		return http.StatusBadRequest
	}
	answer := map[string]any{"jsonrpc": "2.0", "id": call.ID}
	if call.Method != "tools/call" || call.Params.Name != "web_search" {
		answer["error"] = map[string]any{"code": -32601, "message": fmt.Sprintf("%s, and this is %q of %q", wants, call.Method, call.Params.Name)}
	} else {
		results := map[string]any{"results": []any{webSearchResult}}
		text, err := json.Marshal(results)
		if err != nil {
			http.Error(w, "the kind rig's web-search stand-in cannot encode its result: "+err.Error(), http.StatusInternalServerError)
			return http.StatusInternalServerError
		}
		answer["result"] = map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": string(text)}},
			"structuredContent": results,
			"isError":           false,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
	return http.StatusOK
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

// leafOf is a server certificate for name that root's key signed, served alone, with no root.
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
