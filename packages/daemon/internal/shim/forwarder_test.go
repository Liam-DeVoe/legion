package shim_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTheForwarderStartsOnceTheKeyExistsAndStopsWithTheShim: a shim told its role's GitHub
// forwarder starts it once keygen has written the role's key, in the shim's own environment with
// the role's key directory, and at the shim's end sends it SIGTERM and waits for it to finish,
// which is when the forwarder revokes its grants.
//
// The named revert: drop startForwarder's call from identity, and the forwarder never starts.
func TestTheForwarderStartsOnceTheKeyExistsAndStopsWithTheShim(t *testing.T) {
	path := socketPath(t)
	stub := listen(t, path)
	o := newOMP(t)
	cfg, keyDir, _ := agentSecretsConfig(t, path, o)
	log := filepath.Join(t.TempDir(), "forwarder.log")
	script := filepath.Join(t.TempDir(), "forwarder")
	body := `#!/bin/sh
trap 'kill "$pid" 2>/dev/null; printf "stopped\n" >>"` + log + `"; exit 0' TERM
key=no; [ -f "$AGENT_SECRETS_KEY_DIR/key.pem" ] && key=yes
printf 'started key=%s args=%s\n' "$key" "$*" >>"` + log + `"
sleep 300 >/dev/null 2>&1 </dev/null & pid=$!
wait "$pid"
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.GitHubForwarder = []string{script, "--role", "tester"}
	r := run(t, cfg, newClock())
	peer := stub.accept(t)
	peer.open(t)

	started := waitFor(t, log, "started")
	if !strings.Contains(started, "started key=yes args=--role tester") {
		t.Fatalf("the forwarder logged %q; want it started with its flags once %s/key.pem existed", started, keyDir)
	}
	r.stop(t)
	if stopped := waitFor(t, log, "stopped"); !strings.Contains(stopped, "stopped") {
		t.Fatalf("the forwarder logged %q; want its SIGTERM handled before the shim returned", stopped)
	}
	if lines := strings.Count(mustRead(t, log), "started"); lines != 1 {
		t.Fatalf("the forwarder started %d times; want once", lines)
	}
}

// waitFor waits up to ten seconds for the file at path to hold want, and answers what it holds.
func waitFor(t *testing.T, path, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), want) {
			return string(data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return mustRead(t, path)
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
