//go:build !unix

// packages/envoy/cmd/cipher/proxy_trust_read_other.go
package main

import "os"

// readTrustSource is what the file at path holds, for the bundle writer, read whole as
// os.ReadFile reads it. agent-secrets ships for macOS and Linux (proxy_trust_read_unix.go); this
// keeps the package building elsewhere.
func readTrustSource(path string) ([]byte, error) {
	return os.ReadFile(path)
}
