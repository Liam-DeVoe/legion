//go:build unix

// packages/envoy/cmd/cipher/proxy_trust_fifo_test.go
//
// A named pipe that the command's settings name: opening one for reading waits for a writer that
// never comes, so a read of it would hang agent-secrets before the command runs.
package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sjawhar/envoy/internal/testwait"
)

// TestProxyRootsTrustedSkipsANamedPipeInACertificateDirectory: the directory holds the root and a
// named pipe; the pipe is passed over, the root is read, and the answer comes.
func TestProxyRootsTrustedSkipsANamedPipeInACertificateDirectory(t *testing.T) {
	root := proxyTestRoot(t, "test proxy root")
	store := proxyTestPool(t, root)
	settings := proxyStoreSettings(t, writeLoginFile(t, "system.pem", root))
	dir := filepath.Dir(writeLoginFile(t, "root.pem", root))
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatalf("make the named pipe: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "pipe"), filepath.Join(dir, "link-to-pipe")); err != nil {
		t.Fatal(err)
	}
	environ := withSettings(settings, "WGETRC="+writeLoginFile(t, "wgetrc", "ca_directory = "+dir+"\n"))
	answered := make(chan bool, 1)
	go func() { answered <- proxyRootsTrusted(environ, root, store) }()
	if !testwait.Receive(t, "proxyRootsTrusted over a directory holding a named pipe", answered) {
		t.Errorf("a directory holding the root, a named pipe and a link to it: proxyRootsTrusted = false, want true")
	}
}

// TestPrepareAirlockSkipsANamedPipeATrustVariableOrWgetrcNames: a trust variable or WGETRC naming
// a named pipe, for a command that otherwise needs no bundle. Neither the decision nor the bundle
// writer opens the pipe: prepareAirlock answers, and writes the bundle, since a file it cannot
// read cannot show that it holds the root.
func TestPrepareAirlockSkipsANamedPipeATrustVariableOrWgetrcNames(t *testing.T) {
	root := proxyTestRoot(t, "test proxy root")
	system := writeLoginFile(t, "ca-certificates.crt", root)
	useSystemTrustFiles(t, system)
	pipe := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatalf("make the named pipe: %v", err)
	}
	type answer struct {
		files airlockFiles
		err   error
	}
	for _, key := range []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "WGETRC"} {
		t.Run(key, func(t *testing.T) {
			environ := withSettings(proxyStoreSettings(t, system), "XDG_RUNTIME_DIR="+t.TempDir(), key+"="+pipe)
			release := testRelease("AIRLOCK_ME", []string{"api.example.com"}, nil)
			release.CACertificate = root
			answered := make(chan answer, 1)
			go func() {
				files, err := prepareAirlock(environ, release, "grant-one", nil)
				answered <- answer{files, err}
			}()
			got := testwait.Receive(t, "prepareAirlock with "+key+" naming a named pipe", answered)
			if got.err != nil {
				t.Fatalf("prepareAirlock: %v", got.err)
			}
			if got.files.bundle == "" {
				t.Errorf("%s naming a named pipe: prepareAirlock wrote no bundle, want one", key)
			}
		})
	}
}
