package testexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// An executable WriteFile writes runs at once, however many goroutines of the process fork beside
// it, as tests running beside each other (t.Parallel) fork each time they start a command. Eight
// goroutines start /bin/true without a pause while this test writes executables one at a time and
// runs each as soon as it is written. A write by os.WriteFile there runs into ETXTBSY: on a dev box
// with eight CPUs, 99 runs of 2,000; with a fork's copy of the write descriptor in some child at
// the moment of the run, the kernel refuses the exec. With WriteFile no run may: a fork waits for
// the write to close the file, so no child holds it open. The test is no proof that it cannot, but
// the 400 runs it makes would meet ETXTBSY about 20 times without the lock.
//
// Not parallel: its forks are the condition it measures, and a run of its own keeps them its own.
func TestAnExecutableWrittenWhileTheProcessForksRuns(t *testing.T) {
	dir := t.TempDir()
	ForkBeside(t, 8)
	busy := 0
	var other error
	for i := range 400 {
		path := filepath.Join(dir, fmt.Sprintf("fake-%d", i))
		if err := WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			other = err
			break
		}
		if err := exec.Command(path).Run(); errors.Is(err, syscall.ETXTBSY) {
			busy++
		} else if err != nil {
			other = err
			break
		}
	}
	if other != nil {
		t.Fatalf("write or run a fake executable: %v", other)
	}
	if busy != 0 {
		t.Errorf("%d of 400 executables written by WriteFile were refused as busy (ETXTBSY) while the process forked", busy)
	}
}

// Unguarded names a write of a file with an execute bit by os.WriteFile or os.OpenFile in any file
// that is no test file, in a test file of a package whose tests call Parallel, and in any file of a
// test-support package, by whatever name the file imports os and however the mode is spelled, and
// a mode it cannot read as holding no execute bit, a mask that leaves one included. It names an
// open by os.WriteFile, os.OpenFile, os.Create or os.CreateTemp whose file the same function gives
// an execute bit by os.Chmod of its path or of its Name, or by the file's Chmod method, in a
// closure too, a use of one of those functions as a value, and a dot import of os. It leaves out a
// mode with no execute bit, a mode masked so it cannot hold one (mode&^0o111, mode&0o644), a Chmod
// with none or of a path no open of the function names, a test file of a package whose tests do
// not run beside each other, a write by testexec.WriteFile, another module below root, and
// programfile's own Write. The code a deployment runs is planted in daemonish: the shapes the
// daemon's own program files had before programfile.Write, the git helper's os.WriteFile at 0o700
// and workerbin's os.CreateTemp with a Chmod of the file to 0o700, and a copy that keeps a file's
// mode.
func TestUnguardedNamesEveryExecutableWriteThatMayRunBesideAFork(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const parallel = "func TestParallel(t *testing.T) { t.Parallel() }\n"
	write("beside/beside.go", "package beside\n")
	write("beside/a_test.go", `package beside

import (
	"io/fs"
	"os"
	"testing"
)

`+parallel+`
func writes(t *testing.T) {
	_ = os.WriteFile("a", nil, 0o755)
	_ = os.WriteFile("b", nil, 0o644)
	_ = os.WriteFile("c", nil, 0700)
	_ = os.WriteFile("d", nil, fs.FileMode(0o500))
	_ = os.WriteFile("e", nil, 0o600|0o100)
	f, _ := os.OpenFile("f", os.O_CREATE|os.O_WRONLY, 0o755)
	_ = f
	var mode fs.FileMode = 0o755
	_ = os.WriteFile("g", nil, mode)
}
`)
	write("beside/b_test.go", `package beside_test

import (
	osx "os"
	"testing"

	"github.com/sjawhar/envoy/testexec"
)

func writesToo(t *testing.T) {
	_ = osx.WriteFile("h", nil, 0o755)
	_ = testexec.WriteFile("i", nil, 0o755)
}
`)
	write("alone/alone_test.go", `package alone

import (
	"os"
	"testing"
)

func TestAlone(t *testing.T) { _ = os.WriteFile("j", nil, 0o755) }
`)
	write("fakestest/fakestest.go", `package fakestest

import "os"

func Fake() { _ = os.WriteFile("k", nil, 0o755) }
`)
	write("programfile/programfile.go", `package programfile

import "os"

func Write(name string, flag int, perm os.FileMode) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|flag, perm)
	if err != nil {
		return err
	}
	return file.Close()
}
`)
	write("daemonish/install.go", `package daemonish

import "os"

func helper(dir string) error { return os.WriteFile(dir+"/helper", []byte("#!/bin/sh\n"), 0o700) }

func script(dir string) error {
	temporary, err := os.CreateTemp(dir, ".script-")
	if err != nil {
		return err
	}
	_ = temporary.Chmod(0o700)
	return temporary.Close()
}

func named(dir string) {
	temporary, _ := os.CreateTemp(dir, ".named-")
	_ = os.Chmod(temporary.Name(), 0o755)
}

func data(dir string) {
	temporary, _ := os.CreateTemp(dir, ".data-")
	_ = temporary.Chmod(0o600)
	_ = os.WriteFile(dir+"/data", nil, 0o644)
}

func copied(path string, mode os.FileMode) error { return os.WriteFile(path, nil, mode) }

var create = os.CreateTemp

func masked(path string, mode os.FileMode) {
	_ = os.WriteFile(path, nil, mode&^0o111)
	_ = os.WriteFile(path, nil, mode&0o644)
	_ = os.WriteFile(path, nil, mode&0o755)
	_ = os.WriteFile(path, nil, mode&^0o100)
}
`)
	write("daemonish/install_test.go", `package daemonish

import (
	"os"
	"testing"
)

func TestAlone(t *testing.T) { _ = os.WriteFile("v", nil, 0o755) }
`)
	write("other/go.mod", "module other\n")
	write("other/other_test.go", `package other

import (
	"os"
	"testing"
)

`+parallel+`
func writes() { _ = os.WriteFile("l", nil, 0o755) }
`)
	write("beside/chmod_test.go", `package beside

import (
	"os"
	"testing"
)

func chmods(t *testing.T, mode os.FileMode) {
	_ = os.WriteFile("m", nil, 0o644)
	_ = os.Chmod("m", 0o755)
	f, _ := os.Create("n")
	_ = f.Chmod(0o700)
	g, _ := os.OpenFile("o", os.O_CREATE|os.O_WRONLY, 0o600)
	_ = g.Chmod(0o755)
	_ = os.WriteFile("p", nil, 0o600)
	t.Cleanup(func() { _ = os.Chmod("p", mode) })
	h, _ := os.Create("q")
	_ = h.Chmod(0o600)
	_ = os.WriteFile("r", nil, 0o644)
	_ = os.Chmod("r", 0o640)
	_ = os.Chmod(t.TempDir(), 0o755)
	var i, _ = os.Create("s")
	_ = i.Chmod(0o755)
}

func values() {
	write := os.WriteFile
	_ = write("t", nil, 0o644)
}
`)
	write("beside/dot_test.go", `package beside

import (
	. "os"
	"testing"
)

func dotted(t *testing.T) { _ = WriteFile("u", nil, 0o755) }
`)
	got, err := Unguarded(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"beside/a_test.go:12", "beside/a_test.go:14", "beside/a_test.go:15", "beside/a_test.go:16",
		"beside/a_test.go:17", "beside/a_test.go:20", "beside/b_test.go:11",
		"beside/chmod_test.go:11", "beside/chmod_test.go:13", "beside/chmod_test.go:15",
		"beside/chmod_test.go:22", "beside/chmod_test.go:27", "beside/chmod_test.go:9",
		"beside/dot_test.go:4",
		"daemonish/install.go:17", "daemonish/install.go:27", "daemonish/install.go:29",
		"daemonish/install.go:34", "daemonish/install.go:35",
		"daemonish/install.go:5", "daemonish/install.go:8",
		"fakestest/fakestest.go:5",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Unguarded named\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

// Unguarded refuses a root under which it finds no Go file of the module: an empty directory, one
// holding only files of other kinds, and one whose only Go files are another module's. A walk that
// read nothing would otherwise answer what a walk that found nothing answers.
func TestUnguardedRefusesARootItReadsNoGoFileUnder(t *testing.T) {
	t.Parallel()
	empty := t.TempDir()
	prose := t.TempDir()
	if err := os.WriteFile(filepath.Join(prose, "README.md"), []byte("# not Go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := t.TempDir()
	if err := os.MkdirAll(filepath.Join(nested, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"go.mod": "module other\n", "other.go": "package other\n"} {
		if err := os.WriteFile(filepath.Join(nested, "other", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range []string{empty, prose, nested, filepath.Join(empty, "missing")} {
		if got, err := Unguarded(root); err == nil {
			t.Errorf("Unguarded(%s) = %q and no error, want it refused: it read no Go file", root, got)
		}
	}
}
