package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/mcpbridge"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// runAsMain is the variable that makes this test binary run envoy-mcp's main instead of its
// tests, so a test can start envoy-mcp as a process of its own and signal it.
const runAsMain = "ENVOY_MCP_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runAsMain) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// heldServer is a stand-in MCP server that never answers the handshake and never reads its
// standard input, so neither the bridge's wait nor the end of its input ends it: only a kill
// does. It records its pid in pidFile, written whole by a rename, and becomes `sleep <seconds>`
// under that pid, a command line serverRunning tells from a pid the kernel has given out again.
func heldServer(pidFile, seconds string) string {
	return "#!/bin/sh\necho $$ >'" + pidFile + ".new'\nmv '" + pidFile + ".new' '" + pidFile + "'\nexec sleep " + seconds + "\n"
}

// serverRunning reads the pid heldServer recorded in pidFile, and whether that pid is still its
// `sleep <seconds>`.
func serverRunning(pidFile, seconds string) (int, bool) {
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, false
	}
	cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return pid, false
	}
	return pid, strings.Join(strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00"), " ") == "sleep "+seconds
}

// TestATerminationDuringTheBridgesStartStopsTheServerItStarted: a SIGTERM sent to envoy-mcp alone
// - a container's stop, a `kill` - while the bridge waits on a server's handshake ends envoy-mcp
// within the stop's own bound (mcpbridge.StopGrace), not the 30 s the handshake's call may wait,
// and the server it started is gone with it. Before, the watch was armed only once every server
// had started, so the signal met its default action: envoy-mcp died at once and the server ran
// on, its input closed and nobody waiting for it.
func TestATerminationDuringTheBridgesStartStopsTheServerItStarted(t *testing.T) {
	if _, err := os.ReadFile("/proc/self/cmdline"); err != nil {
		t.Fatalf("/proc does not answer (%v): this test reads from it whether the stand-in server still runs", err)
	}
	_, natsURL := testnats.Start(t)
	dir := t.TempDir()
	const seconds = "289"
	pidFile := filepath.Join(dir, "server.pid")
	t.Cleanup(func() {
		if pid, running := serverRunning(pidFile, seconds); running {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	stub := filepath.Join(dir, "mcp-server")
	if err := os.WriteFile(stub, []byte(heldServer(pidFile, seconds)), 0o700); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{"servers": []map[string]any{{
		"name": "held", "command": []string{stub}, "source": "test",
		"topic_template": "test.{id}", "uri_pattern": "test://(?P<id>.+)",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	printed, err := os.Create(filepath.Join(dir, "printed"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { printed.Close() })
	envoyMCP := exec.Command(os.Args[0], "-test.run=^$")
	envoyMCP.Env = append(os.Environ(), runAsMain+"=1", "ENVOY_MCP_CONFIG="+configPath,
		"ENVOY_MACHINE_ID=test-machine", "NATS_URLS="+natsURL)
	envoyMCP.Stdout, envoyMCP.Stderr = printed, printed
	if err := envoyMCP.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = envoyMCP.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		select {
		case <-exited:
		default:
			_ = envoyMCP.Process.Kill()
			<-exited
		}
	})
	testwait.Eventually(t, "the bridge to start the stand-in server", func() bool {
		_, running := serverRunning(pidFile, seconds)
		return running
	})
	if err := envoyMCP.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	// The stop closes the server's input and kills it once StopGrace has passed, so StopGrace is
	// the longest envoy-mcp may take; the rest of the bound is room for a loaded machine, and the
	// whole stays short of the handshake's own 30 s wait, which a stop that waited on the start
	// would run to.
	bound := mcpbridge.StopGrace + 5*time.Second
	select {
	case <-exited:
	case <-time.After(bound):
		t.Fatalf("envoy-mcp still runs %s after its SIGTERM during the bridge's start; want it stopped within the stop's bound: %s", bound, readPrinted(t, printed.Name()))
	}
	pid, _ := serverRunning(pidFile, seconds)
	gone := testwait.Holds(bound, testwait.Poll, func() bool {
		_, running := serverRunning(pidFile, seconds)
		return !running
	})
	if !gone {
		t.Fatalf("the stand-in server (pid %d) still runs %s after envoy-mcp ended on a SIGTERM during the bridge's start; want it stopped with the bridge: %s", pid, bound, readPrinted(t, printed.Name()))
	}
	if state := envoyMCP.ProcessState; state == nil || !state.Exited() || state.ExitCode() != 0 {
		t.Errorf("envoy-mcp ended %v after a SIGTERM during the bridge's start; want it to stop the bridge and exit 0: %s", state, readPrinted(t, printed.Name()))
	}
}

// readPrinted is what envoy-mcp wrote, for a failure's message.
func readPrinted(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "(unread: " + err.Error() + ")"
	}
	return string(raw)
}
