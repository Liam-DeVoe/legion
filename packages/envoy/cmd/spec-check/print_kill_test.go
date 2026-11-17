//go:build linux

package main

import (
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// killSteps are the steps of a run of print that a kill can cut. For trial i of a tree k, target
// is the system calls and the paths that strace kills the run at, on the entry of the first such
// call: strace counts calls for each thread, and a Go program makes its calls on more than one, so
// a kill is aimed at a path, not at a count. The build of the new directory: the call that makes
// the new directory, the call that makes each spec file in it (a link or a new file), and the sync
// of the new directory. The exchange: the one call that puts the new directory in the place of the
// old, the sync of their parent after it, and the move of each entry that is no spec file from the
// old directory into the new one. The removal of the old directory: the removal of each spec file
// of it, and of the directory itself. The exchange step also names the rename of a changed file
// into the directory, the place where a run of the head before this change, which renamed its
// files one at a time, is killed between two of those renames.
var killSteps = []struct {
	name string
	// target is the calls and the paths of the kill of trial i, and what the trial leaves: the
	// spec files as they were ("before") or as the run leaves them ("after"), and whether the
	// directory that the run builds stands beside dir after the kill, which shows that the kill
	// landed inside the step.
	target func(k killTree, i int) (calls string, paths []string, state string, leftover bool)
}{
	{"the build of the new directory", func(k killTree, i int) (string, []string, string, bool) {
		switch {
		case i == 0:
			return "mkdirat", []string{k.staging}, "before", false
		case i == killTrials-1:
			return "fsync", []string{k.staging}, "before", true
		}
		return "linkat,openat", []string{filepath.Join(k.staging, k.names[(i-1)%len(k.names)])}, "before", true
	}},
	{"the exchange", func(k killTree, i int) (string, []string, string, bool) {
		head := filepath.Join(k.dir, k.changed[i%len(k.changed)])
		switch i % 3 {
		case 1:
			return "renameat,renameat2,fsync", []string{filepath.Dir(k.dir), head}, "after", true
		case 2:
			return "renameat,renameat2", []string{filepath.Join(k.dir, k.others[i%len(k.others)]), head}, "after", true
		}
		return "renameat,renameat2", []string{k.dir, head}, "before", true
	}},
	{"the removal of the old directory", func(k killTree, i int) (string, []string, string, bool) {
		if i == killTrials-1 {
			return "unlinkat", []string{k.staging}, "after", true
		}
		return "unlinkat", []string{filepath.Join(k.staging, k.names[i%len(k.names)])}, "after", true
	}},
}

// killTrials is the number of trials of each step.
const killTrials = 20

// A kill of print at each step of its write leaves the spec files as they were before the run or
// as the run leaves them, byte for byte, and tree takes them (D5); the next run of print finishes
// the change, removes what the killed run left beside the directory, and keeps each entry that is
// no spec file, the same inode, in the directory. Each trial has a tree of its own: 24 statements,
// each with its check in a file of its own, a file and a symbolic link that are no spec files, and
// 0 to 3 subdirectories; the run prints a second version of 12 statements. strace kills the run at
// one call of the step (killSteps), and each trial must be killed. The test needs strace; it fails
// in a CI job without it, and skips elsewhere. SPEC_CHECK_PRINT_BINARY runs the trials against
// another build of spec-check, the control: a build that renames its files one at a time fails.
func TestAKillAtEachStepOfPrintLeavesTheFilesBeforeOrAfter(t *testing.T) {
	t.Parallel()
	strace, err := exec.LookPath("strace")
	if err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("strace is not installed, and the job installs it for this test")
		}
		t.Skip("strace is not installed, and this test kills print with strace's inject")
	}
	binary := os.Getenv("SPEC_CHECK_PRINT_BINARY")
	if binary == "" {
		binary = cmdtest.Build(t, "spec-check")
	}
	for s, step := range killSteps {
		for i := range killTrials {
			t.Run(fmt.Sprintf("%s, trial %d", step.name, i+1), func(t *testing.T) {
				t.Parallel()
				rng := rand.New(rand.NewPCG(uint64(s), uint64(i)))
				k := newKillTree(t, binary, rng)
				calls, paths, state, leftover := step.target(k, i)
				args := []string{"-f", "-qq", "-o", os.DevNull}
				for _, path := range paths {
					args = append(args, "-P", path)
				}
				args = append(args, "-e", "inject="+calls+":signal=KILL", binary)
				killed := exec.Command(strace, append(args, k.args...)...)
				killed.Env = childEnv()
				out, err := killed.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || !killedBySIGKILL(exit) {
					t.Fatalf("strace with the kill at %s of %q: %v, output %q; the kill must reach the run", calls, paths, err, out)
				}
				files := specFiles(t, k.dir)
				got := "neither"
				switch {
				case maps.Equal(files, specFiles(t, k.before)):
					got = "before"
				case maps.Equal(files, specFiles(t, k.after)):
					got = "after"
				}
				if got == "neither" {
					t.Fatalf("after the kill at %s of %q, the spec files are neither as before nor as after the run:\n%s", calls, paths, describeFiles(files))
				}
				if _, err := os.Lstat(k.staging); got != state || (err == nil) != leftover {
					t.Fatalf("after the kill at %s of %q, the spec files are as %s the run and the directory beside it stands: %t; want as %s and %t, or the kill landed outside its step",
						calls, paths, got, err == nil, state, leftover)
				}
				if got := runBinary(t, binary, "", "tree", "--dir", k.dir); got.code != 0 {
					t.Fatalf("tree after the kill: exit %d, stderr %q", got.code, got.stderr)
				}
				if again := runBinary(t, binary, "", k.args...); again.code != 0 {
					t.Fatalf("print after the kill: exit %d, stderr %q", again.code, again.stderr)
				}
				if got, want := entries(t, k.dir), entries(t, k.after); !maps.Equal(got, want) {
					t.Fatalf("after the next run of print, the directory is not as the run leaves it:\n got %q\nwant %q", got, want)
				}
				if names := dirNames(t, filepath.Dir(k.dir)); !slices.Equal(names, []string{filepath.Base(k.dir)}) {
					t.Errorf("after the next run of print, the parent of the directory holds %q, want only the directory", names)
				}
				for name, ino := range k.kept {
					if got := inodeOf(t, filepath.Join(k.dir, name)); got != ino {
						t.Errorf("%s is inode %d after the next run of print, and was inode %d: print must keep an entry that is no spec file, not copy it", name, got, ino)
					}
				}
			})
		}
	}
}

// killTree is the tree of one trial: dir, the spec directory of the trial; before and after,
// copies of dir before the run and after a run that no kill cut; args, the arguments of the run;
// staging, the directory that the run builds beside dir; names, each spec file of dir, sorted;
// changed, each spec file that the run changes; others, each entry of dir that is no spec file;
// and kept, the inode of each entry of dir that is no spec file and that is no symbolic link.
type killTree struct {
	dir, before, after, staging  string
	args, names, changed, others []string
	kept                         map[string]uint64
}

// newKillTree makes the tree of one trial with binary: it prints version 1 of 24 statements and
// their checks into a new directory, adds the entries that are no spec files, and copies the
// directory before and after a run that prints version 2 of 12 of the statements.
func newKillTree(t *testing.T, binary string, rng *rand.Rand) killTree {
	t.Helper()
	statement := func(id string, version, lines int) string {
		var b strings.Builder
		b.WriteString("---\ntype: requirement\nid: " + id + "\nkind: behavior\ntext:\n")
		for line := range lines {
			fmt.Fprintf(&b, "  - The daemon keeps each claim of the group %s, line %d of version %d.\n", id[len("claim-"):], line+1, version)
		}
		return b.String()
	}
	check := func(id string) string {
		return "---\ntype: check\nid: " + id + "-check\nserves: " + id + "\nsubject: graph\nprocedure: query\nclaim: The graph holds each claim.\n" +
			"procedure_text:\n  - Read each claim.\nfailing_example:\n  - A graph with no claims.\n"
	}
	at := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	var first, second specgraph.ApprovedFile
	var all, ids []string
	for n := range 24 {
		id := fmt.Sprintf("claim-%c%c", 'a'+n/26, 'a'+n%26)
		lines := 1 + rng.IntN(40)
		v1 := specgraph.ApprovedVersion{Version: 1, At: at, Document: statement(id, 1, lines)}
		c := specgraph.ApprovedNode{ID: id + "-check", Versions: []specgraph.ApprovedVersion{{Version: 1, At: at, Document: check(id)}}}
		first.Nodes = append(first.Nodes, specgraph.ApprovedNode{ID: id, Versions: []specgraph.ApprovedVersion{v1}}, c)
		versions := []specgraph.ApprovedVersion{v1}
		if n%2 == 0 {
			versions = append(versions, specgraph.ApprovedVersion{Version: 2, At: at.Add(time.Hour), Document: statement(id, 2, 1+rng.IntN(40))})
			ids = append(ids, "--id", id)
		}
		second.Nodes = append(second.Nodes, specgraph.ApprovedNode{ID: id, Versions: versions}, c)
		all = append(all, "--id", id)
	}
	root, err := filepath.EvalSymlinks(t.TempDir()) // strace names each path as the kernel resolves it
	if err != nil {
		t.Fatal(err)
	}
	firstPath, secondPath := filepath.Join(root, "first.json"), filepath.Join(root, "second.json")
	writeJSONFile(t, firstPath, first)
	writeJSONFile(t, secondPath, second)
	k := killTree{dir: filepath.Join(root, "repo", "spec"), others: []string{"README", "readme-link"}, kept: map[string]uint64{}}
	if got := runBinary(t, binary, "", append([]string{"print", "--approved", firstPath, "--dir", k.dir}, all...)...); got.code != 0 {
		t.Fatalf("print of version 1: exit %d, stderr %q", got.code, got.stderr)
	}
	writeFile(t, filepath.Join(k.dir, "README"), []byte("The spec files of the daemon.\n"))
	if err := os.Symlink("README", filepath.Join(k.dir, "readme-link")); err != nil {
		t.Fatal(err)
	}
	k.kept["README"] = inodeOf(t, filepath.Join(k.dir, "README"))
	for n := range rng.IntN(4) {
		sub := filepath.Join(k.dir, fmt.Sprintf("notes-%d", n))
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(sub, "note.md"), []byte(fmt.Sprintf("Note %d.\n", n)))
		k.kept[filepath.Base(sub)] = inodeOf(t, sub)
		k.others = append(k.others, filepath.Base(sub))
	}
	k.staging = stagingPath(k.dir, inodeOf(t, k.dir))
	k.args = append([]string{"print", "--approved", secondPath, "--dir", k.dir}, ids...)
	k.before = copyTree(t, k.dir)
	k.after = copyTree(t, k.dir)
	if got := runBinary(t, binary, "", append([]string{"print", "--approved", secondPath, "--dir", k.after}, ids...)...); got.code != 0 {
		t.Fatalf("print of version 2 with no kill: exit %d, stderr %q", got.code, got.stderr)
	}
	before, after := specFiles(t, k.before), specFiles(t, k.after)
	k.names = slices.Sorted(maps.Keys(before))
	for _, name := range slices.Sorted(maps.Keys(after)) {
		if before[name] != after[name] {
			k.changed = append(k.changed, name)
		}
	}
	if len(k.changed) == 0 {
		t.Fatal("the run with no kill changed no spec file")
	}
	return k
}

// killedBySIGKILL reports whether exit is the end of strace for a run that SIGKILL ended: strace
// ends itself with the signal that ended the run, or exits 128 and the signal's number.
func killedBySIGKILL(exit *exec.ExitError) bool {
	status, ok := exit.Sys().(syscall.WaitStatus)
	return ok && (status.Signaled() && status.Signal() == syscall.SIGKILL || status.Exited() && status.ExitStatus() == 128+int(syscall.SIGKILL))
}

// describeFiles is one line for each spec file: its name and its size, enough to tell a version
// from another.
func describeFiles(files map[string]string) string {
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(&b, "  %s: %d bytes\n", name, len(files[name]))
	}
	return b.String()
}
