//go:build linux

package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sjawhar/envoy/testexec"
)

// A program in the pod's shared config home runs from a role's own copy at once, however many
// goroutines of the process start commands beside the copy. ownConfigHome keeps each file's mode,
// execute bit included, and a child forked while a copied program was open for writing holds it
// open until its own exec, and the kernel refuses to run a file some process holds open for
// writing (ETXTBSY, "text file busy"). role-init copies before it starts any process, so in a pod
// nothing forks beside it; eight goroutines that start true without a pause stand in here for a
// process that does, while this test makes a role's config home again and again and runs the
// program in each copy as soon as it is made.
func TestAProgramCopiedIntoARolesConfigHomeWhileTheProcessForksRuns(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared")
	if err := os.MkdirAll(filepath.Join(shared, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "bin", "tool"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	testexec.ForkBeside(t, 8)
	const copies = 400
	busy := 0
	var first error
	for index := range copies {
		own, err := ownConfigHome(shared, filepath.Join(dir, fmt.Sprintf("own-%d", index)), nil)
		if err != nil {
			t.Fatal(err)
		}
		err = exec.Command(filepath.Join(own, "bin", "tool")).Run()
		switch {
		case errors.Is(err, syscall.ETXTBSY):
			busy++
			if first == nil {
				first = err
			}
		case err != nil:
			t.Fatalf("run the copied program: %v", err)
		}
	}
	if busy != 0 {
		t.Errorf("%d of %d programs copied into a role's config home were refused as busy while the process forked; the first: %v", busy, copies, first)
	}
}
