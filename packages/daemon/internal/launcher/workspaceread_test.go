package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// R6 of dispatch://INFRA-3271: where no role process runs in an issue pod, the pod's launcher reads
// the issue workspace for the daemon, with the count a role's shim makes (workspace.ReadWork). The
// launcher's hello says it answers the read, and it answers on the connection the daemon asked
// through: a clean workspace reads as no work, and a change in the working copy reads as work, the
// changed working-copy commit counted.
//
// The repository's and the workspace's configuration are a role's to write, and the launcher holds
// every role's launcher token, so the read runs no program that any configuration names, whichever
// jj the image carries: it runs jj with no user, repository or workspace configuration at all
// (launcherReadEnv). The configuration this test writes would run three programs: a signing
// program that the snapshot of a signed working copy would run to sign the rewritten commit, the
// same program as the verifier that a template alias of `empty` calls through the working copy's
// signature, and a Watchman that the file monitor backend starts. It also holds a revset alias that
// would hide every working copy from the count. The launcher's own environment points at that
// configuration, as a launcher whose home a role could write would, and the read runs none of the
// programs and counts the work. A workspace that holds a configuration file inside the repository
// itself, which a jj could load from there whatever its home says, is not read at all: the read is
// refused, and the daemon keeps the pod as for any read that cannot be made.
//
// No row of this package calls t.Parallel (gitlocks_test.go).
func TestTheLauncherReadsTheWorkspaceItRunsIn(t *testing.T) {
	for _, tool := range []string{"git", "jj"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("this row drives the real %s, which the worker image and CI both install: %v", tool, err)
		}
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{home, bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// marker is a program that leaves the file it names and refuses, as a program a configuration
	// names that must never run.
	marker := func(name string) (program, left string) {
		left = filepath.Join(root, name+"-ran")
		program = filepath.Join(root, name+".sh")
		if err := os.WriteFile(program, []byte("#!/bin/sh\ntouch '"+left+"'\ncat >/dev/null\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return program, left
	}
	signer, signed := marker("sign")
	watchmanScript, watched := marker("watchman")
	if err := os.Rename(watchmanScript, filepath.Join(bin, "watchman")); err != nil {
		t.Fatal(err)
	}
	// okSigner signs: it answers an armored block, so the fixture can sign the working copy.
	okSigner := filepath.Join(root, "ok-sign.sh")
	if err := os.WriteFile(okSigner, []byte("#!/bin/sh\ncat >/dev/null\nprintf -- '-----BEGIN PGP SIGNATURE-----\\nfixture\\n-----END PGP SIGNATURE-----\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The launcher's own environment reaches the configuration the fixture writes: the home whose
	// config directory jj keeps each repository's and workspace's configuration in, and a PATH that
	// holds the Watchman.
	for _, entry := range fixtureenv.Home(home) {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	run := fixturegit.Isolated(t, home)
	remote, seed, clone, ws := filepath.Join(root, "remote"), filepath.Join(root, "seed"), filepath.Join(root, "clone"), filepath.Join(root, "ws")
	run.Run(root, fixturegit.InitBare(remote, "main")...)
	run.Run(root, fixturegit.InitWorkTree(seed, "main")...)
	if err := os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run.Run(seed, "git", "add", "base.txt")
	run.Run(seed, "git", "commit", "--quiet", "-m", "base")
	run.Run(seed, "git", "push", "--quiet", remote, "main")
	run.Run(root, "git", "clone", "--quiet", "--no-local", remote, clone)
	run.Run(clone, fixturegit.JJInit(clone, true)...)
	run.Run(clone, "jj", "workspace", "add", ws, "--name", "ws", "--revision", "main", "-R", clone)
	// The working copy is signed, empty and undescribed, so it is no work and a snapshot that
	// rewrites it would sign the rewrite where the configuration says so.
	run.Run(ws, "jj", "--config=signing.backend=gpg", "--config=signing.backends.gpg.program="+okSigner, "sign", "-r", "@", "-R", ws)

	// A configuration a role could write: sign every commit and verify signatures with the marker
	// program, an `empty` that verifies the working copy's signature, no working copies in any
	// revset, and a Watchman file monitor, set last so the fixture's own commands start no Watchman.
	for _, setting := range [][2]string{
		{"signing.behavior", "force"}, {"signing.backend", "gpg"}, {"signing.backends.gpg.program", signer},
		{"template-aliases.empty", `if(self.signature(), self.signature().status() == "good", self.empty())`},
		{"revset-aliases.'working_copies()'", "none()"},
		{"fsmonitor.backend", "watchman"},
	} {
		run.Run(ws, "jj", "config", "set", "--repo", setting[0], setting[1], "-R", ws)
	}
	ran := func() {
		t.Helper()
		for _, left := range []string{signed, watched} {
			if _, err := os.Stat(left); err == nil {
				t.Fatalf("the launcher's read ran %s, which the repository's configuration names", strings.TrimSuffix(filepath.Base(left), "-ran"))
			}
		}
	}

	g := newSessionRig(t, Config{Workspace: ws})
	if !g.roleConn.hello.ReadsWorkspace {
		t.Fatalf("hello = %#v, want it to say the launcher reads a workspace", g.roleConn.hello)
	}
	answer := func(id string) shimwire.WorkspaceWorkResult {
		t.Helper()
		got, ok := g.roleConn.answer(shimwire.WorkspaceWork{ID: id, Issue: "LEGION-208", TimeoutMs: 120_000}).(shimwire.WorkspaceWorkResult)
		if !ok || got.ID != id {
			t.Fatalf("the launcher answered %#v, want the result of %s", got, id)
		}
		return got
	}
	read := func(id string) shimwire.WorkspaceWorkResult {
		t.Helper()
		got := answer(id)
		if !got.OK {
			t.Fatalf("the launcher refused read %s: %s", id, got.Error)
		}
		ran()
		return got
	}

	if clean := read("clean"); clean.Count != 0 || clean.Changed || clean.Head == "" {
		t.Fatalf("the read of a clean workspace = %+v, want its working copy and no work", clean)
	}
	if err := os.WriteFile(filepath.Join(ws, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := read("changed")
	if !changed.Changed || changed.Count != 1 || len(changed.Commits) != 1 || changed.Commits[0] != changed.Head {
		t.Fatalf("the read of a workspace with a change = %+v, want the changed working copy counted as the one work commit", changed)
	}

	// A configuration file inside the repository: jj loads such a file where the repository holds
	// no id of its own configuration, and another jj could load it however its home is set.
	if err := os.WriteFile(filepath.Join(clone, ".jj", "repo", "config.toml"), []byte("[signing]\nbehavior = \"force\"\nbackend = \"gpg\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if refused := answer("repository-config"); refused.OK || !strings.Contains(refused.Error, "config.toml") {
		t.Fatalf("the read of a workspace whose repository holds a configuration file = %+v, want it refused, naming the file", refused)
	}
	ran()
}
