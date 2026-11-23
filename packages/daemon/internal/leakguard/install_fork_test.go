package leakguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sjawhar/envoy/testexec"
	"github.com/sjawhar/legion/daemon/internal/procgroup"
)

// A served check runs at once however many goroutines of the process start commands beside its
// install. The pod's launcher installs it beside the goroutines that start its roles, and the
// install itself runs the check it unpacked (verifyServed). A child forked while the check was
// open for writing holds it open until its own exec, and the kernel refuses to run a file some
// process holds open for writing (ETXTBSY, "text file busy"), which would refuse the install.
// Eight goroutines start true without a pause while this test installs the check again and again.
func TestAServedCheckInstalledWhileTheProcessForksRuns(t *testing.T) {
	body := served(t, wholeCheck(t, answersItsHelp))
	parent := t.TempDir()
	ctx := bound(t)
	testexec.ForkBeside(t, 8)
	const installs = 300
	busy := 0
	var first error
	for index := range installs {
		dir := filepath.Join(parent, fmt.Sprintf("leak-check-%d", index))
		err := InstallServed(ctx, ServedInstall{Dir: dir, Tag: theTag, Body: body, Start: procgroup.NoReaper})
		switch {
		case errors.Is(err, syscall.ETXTBSY):
			busy++
			if first == nil {
				first = err
			}
		case err != nil:
			t.Fatalf("install %d: %v", index, err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	if busy != 0 {
		t.Errorf("%d of %d installs were refused because their check was busy while the process forked; the first: %v", busy, installs, first)
	}
}

// The private copy of the held check runs at once however many goroutines of the process start
// commands beside the copy: a role's guard copies the check and runs the copy, beside the
// goroutines of the shim or the tree command that start other commands.
func TestAPrivateCopyMadeWhileTheProcessForksRuns(t *testing.T) {
	held := heldCheck{files: map[string][]byte{}}
	for _, name := range ManifestFiles {
		held.files[name] = []byte("the copy of " + name + "\n")
	}
	held.files[checkName] = []byte("#!/bin/sh\nexit 0\n")
	base := t.TempDir()
	testexec.ForkBeside(t, 8)
	const copies = 400
	busy := 0
	var first error
	for range copies {
		dir, err := held.privateCopy(base, base)
		if err != nil {
			t.Fatal(err)
		}
		err = exec.Command(filepath.Join(dir, checkName)).Run()
		switch {
		case errors.Is(err, syscall.ETXTBSY):
			busy++
			if first == nil {
				first = err
			}
		case err != nil:
			t.Fatalf("run the private copy: %v", err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	if busy != 0 {
		t.Errorf("%d of %d private copies were refused as busy while the process forked; the first: %v", busy, copies, first)
	}
}
