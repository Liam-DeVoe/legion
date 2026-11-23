// packages/envoy/cmd/dev-proxy-stands/git_test.go
// The git stand-in as check k9 reaches it: a shallow clone of github.com's repository, at an
// address the client is told github.com is, verifies against the stack's authority and holds the
// commit the stand-in serves; a clone of another host at the same address does not verify.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules/proxyrulestest"
)

// gitCommand runs git with no configuration of the machine's and answers what it printed.
func gitCommand(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME="+home,
		"GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=stand-in", "GIT_AUTHOR_EMAIL=stand-in@example.com",
		"GIT_COMMITTER_NAME=stand-in", "GIT_COMMITTER_EMAIL=stand-in@example.com")
	out, err := command.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// standInRepository makes root/owner/name.git, a bare repository with one commit on main, and
// answers that commit.
func standInRepository(t *testing.T, home, root, owner, name string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	bare := filepath.Join(root, owner, name+".git")
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", "--initial-branch=main", bare},
		{"init", "--quiet", "--initial-branch=main", work},
		{"-C", work, "commit", "--quiet", "--allow-empty", "-m", "the stand-in's one commit"},
		{"-C", work, "push", "--quiet", bare, "main"},
	} {
		if out, err := gitCommand(t, home, args...); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	commit, err := gitCommand(t, home, "--git-dir="+bare, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("git rev-parse: %v: %s", err, commit)
	}
	return commit
}

func TestTheGitStandInServesAShallowCloneOfGitHubAndNoOtherHost(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("the git stand-in serves through git http-backend, and git is not on PATH: %v", err)
	}
	home, root, ca := t.TempDir(), t.TempDir(), t.TempDir()
	commit := standInRepository(t, home, root, "cybermonklabs", "legion")
	authority, err := proxyrulestest.NewAuthority("git stand-in test authority")
	if err != nil {
		t.Fatal(err)
	}
	caCert, caKey, err := authority.WriteFiles(ca)
	if err != nil {
		t.Fatal(err)
	}

	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runGit(ctx, []string{"--listen", "127.0.0.1:0", "--root", root, "--ca-cert", caCert, "--ca-key", caKey, "--host", "github.com"}, writer, &stderr)
		_ = writer.Close()
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the git stand-in did not stop when its context ended")
		}
	})
	var report gitReport
	if err := json.NewDecoder(reader).Decode(&report); err != nil {
		t.Fatalf("read the git stand-in's report: %v (stderr: %s)", err, stderr.String())
	}
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	_, port, err := net.SplitHostPort(report.Address)
	if err != nil {
		t.Fatal(err)
	}

	clone := func(host string) (string, string, error) {
		dir := filepath.Join(t.TempDir(), "clone")
		out, err := gitCommand(t, home, "-c", "http.sslCAInfo="+caCert, "-c", "http.curloptResolve="+host+":"+port+":127.0.0.1",
			"clone", "--depth", "1", "https://"+host+":"+port+"/cybermonklabs/legion", dir)
		return dir, out, err
	}
	dir, out, err := clone("github.com")
	if err != nil {
		t.Fatalf("a shallow clone of github.com/cybermonklabs/legion through the stand-in failed: %v: %s", err, out)
	}
	head, err := gitCommand(t, home, "-C", dir, "rev-parse", "HEAD")
	if err != nil || head != commit {
		t.Fatalf("the clone's HEAD is %q (%v), not the stand-in's commit %s", head, err, commit)
	}
	if shallow, _ := gitCommand(t, home, "-C", dir, "rev-parse", "--is-shallow-repository"); shallow != "true" {
		t.Errorf("the --depth 1 clone is not shallow: %s", shallow)
	}

	if _, out, err := clone("gitlab.com"); err == nil || !strings.Contains(out, "gitlab.com") {
		t.Fatalf("a clone of gitlab.com at the stand-in's address succeeded or named no host: %v: %s", err, out)
	}
}
