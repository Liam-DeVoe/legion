package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// R6 of dispatch://INFRA-3271: where no role process runs in an issue pod, the pod's launcher reads
// the issue workspace for the daemon, with the count a role's shim makes (workspace.ReadWork). The
// launcher's hello says it answers the read, and it answers on the connection the daemon asked
// through: a clean workspace reads as no work, and a change in the working copy reads as work, the
// changed working-copy commit counted. The repository's configuration is a role's to write, and the
// launcher holds every role's launcher token, so a configuration that signs every commit with a
// program of its own is not run by the read's snapshot, which writes the changed working copy's
// commit (launcherReadConfig).
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
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
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

	// A configuration a role could write into the shared repository: sign every commit, with a
	// program that leaves a marker and refuses.
	marker := filepath.Join(root, "signed")
	program := filepath.Join(root, "sign.sh")
	if err := os.WriteFile(program, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, setting := range [][2]string{{"signing.behavior", "force"}, {"signing.backend", "gpg"}, {"signing.backends.gpg.program", program}} {
		run.Run(ws, "jj", "config", "set", "--repo", setting[0], setting[1], "-R", ws)
	}

	g := newSessionRig(t, Config{Workspace: ws})
	if !g.roleConn.hello.ReadsWorkspace {
		t.Fatalf("hello = %#v, want it to say the launcher reads a workspace", g.roleConn.hello)
	}
	read := func(id string) shimwire.WorkspaceWorkResult {
		t.Helper()
		got, ok := g.roleConn.answer(shimwire.WorkspaceWork{ID: id, Issue: "LEGION-208", TimeoutMs: 120_000}).(shimwire.WorkspaceWorkResult)
		if !ok || got.ID != id {
			t.Fatalf("the launcher answered %#v, want the result of %s", got, id)
		}
		if !got.OK {
			t.Fatalf("the launcher refused read %s: %s", id, got.Error)
		}
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
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the launcher's read ran the signing program the repository's configuration names")
	}
}
