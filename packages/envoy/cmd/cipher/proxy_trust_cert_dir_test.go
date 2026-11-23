// packages/envoy/cmd/cipher/proxy_trust_cert_dir_test.go
//
// SSL_CERT_DIR replaces a client's certificate directory: Node's system store
// (NODE_USE_SYSTEM_CA=1, measured with Node v24.22.1) loads the directory SSL_CERT_DIR names in
// place of /etc/ssl/certs, every file of it, and does not fall back to the system bundle file, so
// a command whose SSL_CERT_DIR names a directory without the proxy's root does not trust the proxy
// from Node, though the system bundle holds the root. With the bundle, NODE_EXTRA_CA_CERTS carries
// the root to Node. So where the command has an SSL_CERT_DIR the bundle must still be written:
// any SSL_CERT_DIR at all, since the clients that read it disagree on what it holds (airlock.go,
// proxyRootsTrusted).
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareAirlockWritesTheBundleWhereTheCommandsCertDirLacksTheRoot(t *testing.T) {
	root, other := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test private authority")
	empty := t.TempDir()
	without := filepath.Dir(writeLoginFile(t, "private.pem", other))
	with := filepath.Dir(writeLoginFile(t, "root.pem", root))
	if err := os.WriteFile(filepath.Join(with, "private.pem"), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name       string
		dir        string
		wantBundle bool
	}{
		{name: "SSL_CERT_DIR names an empty directory", dir: empty, wantBundle: true},
		{name: "SSL_CERT_DIR names a directory without the root", dir: without, wantBundle: true},
		// Changed from the acceptance tester's row (want no bundle): httpx reads this directory
		// through OpenSSL's capath, which finds a root only under its hash link, and this
		// directory holds root.pem with none, so any SSL_CERT_DIR now gets the bundle.
		{name: "SSL_CERT_DIR names a directory that holds the root", dir: with, wantBundle: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			// The machine's system bundle holds the root.
			useSystemTrustFiles(t, writeLoginFile(t, "ca-certificates.crt", root))
			runtimeDir := t.TempDir()
			environ := []string{"XDG_RUNTIME_DIR=" + runtimeDir, "SSL_CERT_DIR=" + row.dir}
			release := testRelease("AIRLOCK_ME", []string{"api.example.com"}, nil)
			release.CACertificate = root
			files, err := prepareAirlock(environ, release, "grant-one", nil)
			if err != nil {
				t.Fatalf("prepareAirlock: %v", err)
			}
			vars, err := airlockVars(environ, release, files)
			if err != nil {
				t.Fatalf("airlockVars: %v", err)
			}
			if !row.wantBundle {
				if files.bundle != "" {
					t.Errorf("prepareAirlock wrote the bundle %q, want none: the command's SSL_CERT_DIR holds the root", files.bundle)
				}
				return
			}
			if files.bundle == "" {
				t.Fatalf("prepareAirlock wrote no bundle, though the command's SSL_CERT_DIR, which Node reads in place of the system's directory, lacks the root")
			}
			for _, key := range airlockBundleVars {
				if vars[key] != files.bundle {
					t.Errorf("the command's %s is %q, want the bundle %q", key, vars[key], files.bundle)
				}
			}
		})
	}
}
