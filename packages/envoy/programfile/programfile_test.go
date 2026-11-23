package programfile_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sjawhar/envoy/programfile"
	"github.com/sjawhar/envoy/testexec"
)

// A program Write writes runs at once however many goroutines of the process fork beside it.
// Eight goroutines start true without a pause while this test writes programs one at a time and
// runs each as soon as it is written. A write by os.WriteFile there runs into ETXTBSY ("text file
// busy"): a child forked inside the write holds the file open until its own exec, and the kernel
// refuses to run a file some process holds open for writing. With Write no run may, since a fork
// waits for the write to close the file.
//
// Not parallel: its forks are the condition it measures.
func TestAProgramWrittenWhileTheProcessForksRuns(t *testing.T) {
	dir := t.TempDir()
	testexec.ForkBeside(t, 8)
	const runs = 400
	busy := 0
	var first error
	for i := range runs {
		path := filepath.Join(dir, fmt.Sprintf("program-%d", i))
		if err := programfile.Write(path, os.O_EXCL, 0o755, strings.NewReader("#!/bin/sh\nexit 0\n")); err != nil {
			t.Fatal(err)
		}
		err := exec.Command(path).Run()
		switch {
		case errors.Is(err, syscall.ETXTBSY):
			busy++
			if first == nil {
				first = err
			}
		case err != nil:
			t.Fatalf("run the program: %v", err)
		}
	}
	if busy != 0 {
		t.Errorf("%d of %d programs Write wrote were refused as busy while the process forked; the first: %v", busy, runs, first)
	}
}

// Write gives the file the mode it names whatever the umask, writes what the reader holds, and
// opens with the caller's flag: os.O_EXCL refuses a file already there and leaves it as it was,
// and os.O_TRUNC writes over one.
//
// Not parallel: it sets the process's umask.
func TestWriteGivesTheModeItNamesAndOpensWithTheFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "program")
	umask := syscall.Umask(0o077)
	err := programfile.Write(path, os.O_EXCL, 0o755, strings.NewReader("#!/bin/sh\necho first\n"))
	syscall.Umask(umask)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("the program's mode = %v under umask 077, want -rwxr-xr-x", info.Mode().Perm())
	}
	if out, err := exec.Command(path).Output(); err != nil || string(out) != "first\n" {
		t.Errorf("the program printed %q, %v, want first", out, err)
	}

	if err := programfile.Write(path, os.O_EXCL, 0o700, strings.NewReader("#!/bin/sh\necho second\n")); !errors.Is(err, fs.ErrExist) {
		t.Errorf("Write with os.O_EXCL over a file = %v, want it refused as existing", err)
	}
	if out, err := exec.Command(path).Output(); err != nil || string(out) != "first\n" {
		t.Errorf("after the refused write the program printed %q, %v, want first", out, err)
	}

	if err := programfile.Write(path, os.O_TRUNC, 0o700, strings.NewReader("#!/bin/sh\necho third\n")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Errorf("after the write over it the program's mode = %v, want -rwx------", info.Mode().Perm())
	}
	if out, err := exec.Command(path).Output(); err != nil || string(out) != "third\n" {
		t.Errorf("after the write over it the program printed %q, %v, want third", out, err)
	}
}
