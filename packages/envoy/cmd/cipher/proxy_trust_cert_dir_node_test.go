// packages/envoy/cmd/cipher/proxy_trust_cert_dir_node_test.go
//
// Node's system store (NODE_USE_SYSTEM_CA=1) reads SSL_CERT_DIR as one directory path, not as a
// list joined by ':' (measured with Node v24.22.1: a directory literally named "a:b" that holds
// the root is trusted, and "<dir without the root>:<dir with the root>" is not, in either order).
// A set but empty SSL_CERT_DIR leaves Node with no directory at all, and the proxy's root with it.
// Go and OpenSSL read the system bundle file beside SSL_CERT_DIR, so they trust the root in every
// row here; Node, which reads no bundle file beside it, is the client the bundle is kept for. So a
// list, or an empty value, must still get the bundle, whose NODE_EXTRA_CA_CERTS carries the root
// to Node; so must one directory, since httpx reads it by hash link alone (airlock.go,
// proxyRootsTrusted).
package main

import (
	"path/filepath"
	"testing"
)

func TestPrepareAirlockReadsTheCertDirAsNodeDoes(t *testing.T) {
	root, other := proxyTestRoot(t, "test proxy root"), proxyTestRoot(t, "test private authority")
	without := filepath.Dir(writeLoginFile(t, "private.pem", other))
	with := filepath.Dir(writeLoginFile(t, "root.pem", root))
	for _, row := range []struct {
		name       string
		value      string
		wantBundle bool
	}{
		{name: "a list whose second directory holds the root", value: without + ":" + with, wantBundle: true},
		{name: "a list whose first directory holds the root", value: with + ":" + without, wantBundle: true},
		{name: "a list of empty entries around a directory that holds the root", value: "::" + with + ":", wantBundle: true},
		{name: "a set but empty SSL_CERT_DIR", value: "", wantBundle: true},
		// Changed from the acceptance tester's row (want no bundle): httpx reads this directory
		// through OpenSSL's capath, which finds a root only under its hash link, and this
		// directory holds root.pem with none, so any SSL_CERT_DIR now gets the bundle.
		{name: "one directory that holds the root", value: with, wantBundle: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			// The machine's system bundle holds the root.
			useSystemTrustFiles(t, writeLoginFile(t, "ca-certificates.crt", root))
			runtimeDir := t.TempDir()
			environ := []string{"XDG_RUNTIME_DIR=" + runtimeDir, "SSL_CERT_DIR=" + row.value}
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
					t.Errorf("prepareAirlock wrote the bundle %q, want none: the one directory SSL_CERT_DIR names holds the root", files.bundle)
				}
				return
			}
			if files.bundle == "" {
				t.Fatalf("prepareAirlock wrote no bundle for SSL_CERT_DIR=%q, which Node reads as one directory path that does not hold the root", row.value)
			}
			if vars["NODE_EXTRA_CA_CERTS"] != files.bundle {
				t.Errorf("the command's NODE_EXTRA_CA_CERTS is %q, want the bundle %q", vars["NODE_EXTRA_CA_CERTS"], files.bundle)
			}
		})
	}
}
