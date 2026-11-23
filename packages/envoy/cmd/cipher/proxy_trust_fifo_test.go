//go:build unix

// packages/envoy/cmd/cipher/proxy_trust_fifo_test.go
//
// A named pipe in wget's ca_directory: opening one for reading waits for a writer that never
// comes, so a read of every entry would hang agent-secrets before the command runs.
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
