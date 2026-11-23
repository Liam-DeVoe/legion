// packages/envoy/cmd/cipher/proxy_trust_test.go
//
// When a command needs no trust bundle: the machine's system trust bundle already trusts every
// root the proxy's release carries, and no trust variable or wgetrc the command has names a file
// that lacks one. Then nothing is written and no trust variable is set. Otherwise prepareAirlock
// writes the bundle and every trust variable names it. The roots here are made for each test and
// are public certificates, so these tests print what they compare.
package main

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules/proxyrulestest"
)

// proxyTestRoot is a self-signed certificate authority made for one test, in the proxy root's
// shape, as the PEM a release carries.
func proxyTestRoot(t *testing.T, name string) string {
	t.Helper()
	authority, err := proxyrulestest.NewAuthority(name)
	if err != nil {
		t.Fatalf("make %s: %v", name, err)
	}
	return string(authority.CertificatePEM())
}

// proxyTestPool is a certificate pool that holds the roots given, standing for a system store.
func proxyTestPool(t *testing.T, roots ...string) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	for _, root := range roots {
		if !pool.AppendCertsFromPEM([]byte(root)) {
			t.Fatalf("the test pool took no certificate from %q", root)
		}
	}
	return pool
}

// TestProxyRootsTrustedOnlyWhenTheStoreHoldsEveryRoot: the store must trust each root the release
// carries. While the proxy's root changes, a release carries two, the old root and the new, so a
// store that holds one of the two still gets the bundle. A release with no root that parses is no
// root the store trusts.
func TestProxyRootsTrustedOnlyWhenTheStoreHoldsEveryRoot(t *testing.T) {
	newRoot, oldRoot := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test old authority")
	for _, row := range []struct {
		name    string
		release string
		store   []string
		want    bool
	}{
		{name: "the store holds the one root", release: newRoot, store: []string{newRoot}, want: true},
		{name: "the store holds another root", release: newRoot, store: []string{oldRoot}},
		{name: "an empty store", release: newRoot},
		{name: "a two-root release and a store that holds one of them", release: oldRoot + newRoot, store: []string{newRoot}},
		{name: "a two-root release and a store that holds both", release: oldRoot + newRoot, store: []string{newRoot, oldRoot}, want: true},
		{name: "a release with no certificate", release: "", store: []string{newRoot}},
		{name: "a release whose one block does not parse", release: testAirlockCertificate, store: []string{newRoot}},
		{name: "a release with a root and a block that does not parse", release: newRoot + testAirlockCertificate, store: []string{newRoot}},
		{name: "a release with a block that does not parse and a root", release: testAirlockCertificate + newRoot, store: []string{newRoot}},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := proxyRootsTrusted(nil, row.release, proxyTestPool(t, row.store...)); got != row.want {
				t.Fatalf("proxyRootsTrusted = %v, want %v", got, row.want)
			}
		})
	}
}

// TestProxyRootsTrustedReadsTheTrustVariablesTheCommandHas: a trust variable the command already
// has replaces its client's roots, and with no bundle the command keeps it as it is. So a file it
// names that lacks a root takes the proxy away from that client, and the bundle is still written.
// NODE_EXTRA_CA_CERTS is the exception: Node adds its file to the roots it has, so a file without
// the root takes nothing away. A file that cannot be read cannot show that it holds the root.
func TestProxyRootsTrustedReadsTheTrustVariablesTheCommandHas(t *testing.T) {
	root, other := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test private authority")
	store := proxyTestPool(t, root)
	without := writeLoginFile(t, "private.pem", other)
	with := writeLoginFile(t, "both.pem", other+root)
	unreadable := filepath.Join(t.TempDir(), "not-there.pem")

	for _, key := range airlockBundleVars {
		want := key == "NODE_EXTRA_CA_CERTS"
		if got := proxyRootsTrusted([]string{key + "=" + without}, root, store); got != want {
			t.Errorf("%s naming a file without the root: proxyRootsTrusted = %v, want %v", key, got, want)
		}
		if got := proxyRootsTrusted([]string{key + "=" + with}, root, store); !got {
			t.Errorf("%s naming a file that holds the root: proxyRootsTrusted = false, want true", key)
		}
	}
	if proxyRootsTrusted([]string{"REQUESTS_CA_BUNDLE=" + unreadable}, root, store) {
		t.Errorf("REQUESTS_CA_BUNDLE naming a file that cannot be read: proxyRootsTrusted = true, want false")
	}
	if !proxyRootsTrusted([]string{"CURL_CA_BUNDLE="}, root, store) {
		t.Errorf("an empty CURL_CA_BUNDLE, which names no file: proxyRootsTrusted = false, want true")
	}
	// The last entry wins, as execve reads it.
	if proxyRootsTrusted([]string{"CURL_CA_BUNDLE=" + with, "CURL_CA_BUNDLE=" + without}, root, store) {
		t.Errorf("CURL_CA_BUNDLE set twice, the last naming a file without the root: proxyRootsTrusted = true, want false")
	}
}

// TestProxyRootsTrustedReadsTheWgetrcTheCommandHas: wget reads no trust variable, only its
// wgetrc, and with no bundle the command keeps the WGETRC it has. A ca_directory there replaces
// the system store, so the last one, the one wget reads, must hold every root; a ca_certificate
// adds to the store's trust and takes nothing away; and a wgetrc or a directory this process
// cannot read is one it cannot vouch for. wget ignores case, '_' and '-' in a command's name, and
// skips a line that opens with '#'.
func TestProxyRootsTrustedReadsTheWgetrcTheCommandHas(t *testing.T) {
	root, other := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test private authority")
	store := proxyTestPool(t, root)
	without := writeLoginFile(t, "private.pem", other)
	dirWithout := filepath.Dir(writeLoginFile(t, "private.pem", other))
	dirWith := filepath.Dir(writeLoginFile(t, "root.pem", root))
	if err := os.WriteFile(filepath.Join(dirWith, "private.pem"), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "not-there")
	for _, row := range []struct {
		name   string
		wgetrc string
		want   bool
	}{
		{name: "a wgetrc with no trust setting", wgetrc: "tries = 3\n", want: true},
		{name: "a ca_certificate without the root", wgetrc: "ca_certificate = " + without + "\n", want: true},
		{name: "a ca_directory that holds the root", wgetrc: "tries = 3\nca_directory = " + dirWith + "\n", want: true},
		{name: "a ca_directory without the root", wgetrc: "tries = 3\nca_directory = " + dirWithout + "\n"},
		{name: "the same setting spelt with dashes and capitals", wgetrc: "CA-Directory=" + dirWithout + "\n"},
		{name: "two ca_directory lines, the last without the root", wgetrc: "ca_directory = " + dirWith + "\nca_directory = " + dirWithout + "\n"},
		{name: "two ca_directory lines, the last with the root", wgetrc: "ca_directory = " + dirWithout + "\nca_directory = " + dirWith + "\n", want: true},
		{name: "a commented-out ca_directory", wgetrc: "# ca_directory = " + dirWithout + "\n", want: true},
		{name: "a ca_directory that cannot be read", wgetrc: "ca_directory = " + missing + "\n"},
	} {
		t.Run(row.name, func(t *testing.T) {
			wgetrc := writeLoginFile(t, "wgetrc", row.wgetrc)
			if got := proxyRootsTrusted([]string{"WGETRC=" + wgetrc}, root, store); got != row.want {
				t.Fatalf("proxyRootsTrusted with the wgetrc %q = %v, want %v", row.wgetrc, got, row.want)
			}
		})
	}
	if proxyRootsTrusted([]string{"WGETRC=" + missing}, root, store) {
		t.Errorf("WGETRC naming a file that cannot be read: proxyRootsTrusted = true, want false")
	}
}

// TestProxyRootsTrustedReadsTheCertDirTheCommandHas: SSL_CERT_DIR replaces the system's
// certificate directory for Node's system store (NODE_USE_SYSTEM_CA), and with no bundle the
// command keeps the SSL_CERT_DIR it has. Node, OpenSSL and Go read it as a list of directories
// joined by ':', every file of each, together; so the directories together must hold every root,
// one of them that cannot be read adds nothing, and an empty value names no directory.
func TestProxyRootsTrustedReadsTheCertDirTheCommandHas(t *testing.T) {
	root, other := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test private authority")
	store := proxyTestPool(t, root)
	dirWithout := filepath.Dir(writeLoginFile(t, "private.pem", other))
	dirWith := filepath.Dir(writeLoginFile(t, "root.pem", root))
	missing := filepath.Join(t.TempDir(), "not-there")
	for _, row := range []struct {
		name string
		dirs []string
		want bool
	}{
		{name: "no SSL_CERT_DIR", want: true},
		{name: "an empty SSL_CERT_DIR", dirs: []string{""}, want: true},
		{name: "a directory that holds the root", dirs: []string{dirWith}, want: true},
		{name: "a directory without the root", dirs: []string{dirWithout}},
		{name: "two directories, the second holding the root", dirs: []string{dirWithout, dirWith}, want: true},
		{name: "a directory that cannot be read and one that holds the root", dirs: []string{missing, dirWith}, want: true},
		{name: "a directory that cannot be read", dirs: []string{missing}},
	} {
		t.Run(row.name, func(t *testing.T) {
			var environ []string
			if row.dirs != nil {
				environ = []string{"SSL_CERT_DIR=" + strings.Join(row.dirs, string(filepath.ListSeparator))}
			}
			if got := proxyRootsTrusted(environ, root, store); got != row.want {
				t.Fatalf("proxyRootsTrusted with %q = %v, want %v", environ, got, row.want)
			}
		})
	}
}

// useSystemTrustFiles points systemTrustFiles, the system bundles prepareAirlock reads the store
// from, at paths until the test ends. A test that calls it swaps a package variable, so neither it
// nor its subtests run in parallel.
func useSystemTrustFiles(t *testing.T, paths ...string) {
	t.Helper()
	old := systemTrustFiles
	systemTrustFiles = paths
	t.Cleanup(func() { systemTrustFiles = old })
}

// TestPrepareAirlockLeavesTheTrustAloneWhereTheSystemStoreHoldsTheRoot runs prepareAirlock and
// airlockVars against a system bundle of the test's own, with and without the release's roots in
// it. Where the bundle trusts every root and no trust variable or wgetrc of the command names a
// file that lacks one, no file is written and none of the trust variables or WGETRC is set, while
// the proxy still is. Every other row writes the bundle, holding every root, and points each trust
// variable and WGETRC at it. The store is the first of the system bundles that exists, as git and
// wget read it: a missing one is passed over, and one that is there but cannot be read is a store
// that trusts nothing, not a reason to read the next.
func TestPrepareAirlockLeavesTheTrustAloneWhereTheSystemStoreHoldsTheRoot(t *testing.T) {
	newRoot, oldRoot, private := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test old authority"), proxyTestRoot(t, "test private authority")
	withoutRoot := writeLoginFile(t, "private.pem", private)
	withRoot := writeLoginFile(t, "private-and-root.pem", private+newRoot)
	wgetrcWithoutRoot := writeLoginFile(t, "wgetrc", "ca_directory = "+filepath.Dir(withoutRoot)+"\n")
	missing := filepath.Join(t.TempDir(), "not-there.crt")
	unreadable := t.TempDir() // a directory: there, and not a file any client reads
	for _, row := range []struct {
		name       string
		before     []string // system bundles listed ahead of the one holding store
		store      []string
		release    string
		environ    []string
		wantBundle bool
	}{
		{name: "the store holds the root", store: []string{newRoot}, release: newRoot},
		{name: "the store lacks the root", store: []string{private}, release: newRoot, wantBundle: true},
		{name: "a two-root release and a store that holds one of them", store: []string{newRoot}, release: oldRoot + newRoot, wantBundle: true},
		{name: "a two-root release and a store that holds both", store: []string{newRoot, oldRoot}, release: oldRoot + newRoot},
		{name: "the command's CURL_CA_BUNDLE names a file without the root", store: []string{newRoot}, release: newRoot, environ: []string{"CURL_CA_BUNDLE=" + withoutRoot}, wantBundle: true},
		{name: "the command's CURL_CA_BUNDLE names a file that holds the root", store: []string{newRoot}, release: newRoot, environ: []string{"CURL_CA_BUNDLE=" + withRoot}},
		{name: "the command's wgetrc names a ca_directory without the root", store: []string{newRoot}, release: newRoot, environ: []string{"WGETRC=" + wgetrcWithoutRoot}, wantBundle: true},
		{name: "the first system bundle is missing and the next holds the root", before: []string{missing}, store: []string{newRoot}, release: newRoot},
		{name: "the first system bundle is there and cannot be read", before: []string{unreadable}, store: []string{newRoot}, release: newRoot, wantBundle: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			useSystemTrustFiles(t, append(slices.Clone(row.before), writeLoginFile(t, "ca-certificates.crt", strings.Join(row.store, "")))...)
			runtimeDir := t.TempDir()
			environ := append([]string{"XDG_RUNTIME_DIR=" + runtimeDir}, row.environ...)
			release := testRelease("AIRLOCK_ME", []string{"api.example.com"}, nil)
			release.CACertificate = row.release
			files, err := prepareAirlock(environ, release, "grant-one", nil)
			if err != nil {
				t.Fatalf("prepareAirlock: %v", err)
			}
			vars, err := airlockVars(environ, release, files)
			if err != nil {
				t.Fatalf("airlockVars: %v", err)
			}
			if vars["HTTPS_PROXY"] == "" || vars["NO_PROXY"] == "" {
				t.Errorf("the command's HTTPS_PROXY is %q and NO_PROXY %q, want both set whatever the store holds", vars["HTTPS_PROXY"], vars["NO_PROXY"])
			}
			grantDir := filepath.Join(runtimeDir, "agent-secrets", "airlock", "grant-one")
			if !row.wantBundle {
				if files.bundle != "" || files.wgetrc != "" {
					t.Errorf("prepareAirlock answered the files %q and %q, want none", files.bundle, files.wgetrc)
				}
				for _, key := range append(slices.Clone(airlockBundleVars), "WGETRC") {
					if value, set := vars[key]; set {
						t.Errorf("the command's %s is set to %q, want it left as the command has it", key, value)
					}
				}
				if _, err := os.Stat(filepath.Join(runtimeDir, "agent-secrets")); !os.IsNotExist(err) {
					t.Errorf("the runtime directory holds agent-secrets' files (stat: %v), want nothing written", err)
				}
				return
			}
			if want := filepath.Join(grantDir, "ca-bundle.pem"); files.bundle != want {
				t.Fatalf("the bundle is at %q, want %q", files.bundle, want)
			}
			bundle, err := os.ReadFile(files.bundle)
			if err != nil {
				t.Fatalf("read the bundle: %v", err)
			}
			for _, root := range []string{newRoot, oldRoot} {
				if strings.Contains(row.release, root) && !strings.Contains(string(bundle), root) {
					t.Errorf("the bundle lacks a root the release carries:\n%s", bundle)
				}
			}
			for _, key := range airlockBundleVars {
				if vars[key] != files.bundle {
					t.Errorf("the command's %s is %q, want the bundle %q", key, vars[key], files.bundle)
				}
			}
			if want := filepath.Join(grantDir, "wgetrc"); vars["WGETRC"] != want {
				t.Errorf("the command's WGETRC is %q, want %q", vars["WGETRC"], want)
			}
		})
	}
}
