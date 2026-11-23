package testexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	forker, err := exec.LookPath("true")
	if err != nil {
		t.Fatalf("no true command on PATH to fork, so the test cannot make the forks it measures: %v", err)
	}
	dir := t.TempDir()
	var stop atomic.Bool
	var forks sync.WaitGroup
	for range 8 {
		forks.Go(func() {
			for !stop.Load() {
				_ = exec.Command(forker).Run()
			}
		})
	}
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
	stop.Store(true)
	forks.Wait()
	if other != nil {
		t.Fatalf("write or run a fake executable: %v", other)
	}
	if busy != 0 {
		t.Errorf("%d of 400 executables written by WriteFile were refused as busy (ETXTBSY) while the process forked", busy)
	}
}

// writeUnder writes body to path under root, at 0o644, making the directories it needs: the one
// way the guard's tests write a fixture's files.
func writeUnder(t *testing.T, root, path, body string) {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Unguarded names a write of a file with an execute bit by os.WriteFile or os.OpenFile in a test
// file of a package whose tests call Parallel, and in any file of a test-support package, by
// whatever name the file imports os and however the mode is spelled, and a mode it cannot
// evaluate. It names an open by os.WriteFile, os.OpenFile, os.Create or os.CreateTemp whose file
// the same function gives an execute bit by os.Chmod of its path or of its file's Name(), or by
// the file's Chmod method, in a closure too. It follows the file through calls that wrap the open,
// one or two deep, a Chmod or a Name() called on such a call, a variable or a field the file is
// assigned to, by itself or beside other values, a copy of one, assigned or declared, a field a
// keyed composite literal or its address sets, in a nested literal too, a field written or read
// through (*h).f, and an os.Rename, which moves to its new path the opens made before it. It names
// a use of one of those functions as a value, and a dot import of os. It leaves out a mode with no
// execute bit, a Chmod with none or of a path no open of the function names, a Chmod of a copy
// made before the open or of a variable that never held one, a Chmod on a call that opens no file
// for writing, the old path of an os.Rename that moved another file onto the open's path, a
// package whose tests do not run beside each other, a write by WriteFile, another module below
// root, and this package's own WriteFile.
func TestUnguardedNamesEveryExecutableWriteATestMayRunBesideAFork(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		writeUnder(t, root, path, body)
	}
	const parallel = "func TestParallel(t *testing.T) { t.Parallel() }\n"
	write("go.mod", "module example.com/checked\n")
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
	write("testexec/testexec.go", `package testexec

import "os"

func WriteFile(name string, data []byte, perm os.FileMode) error { return os.WriteFile(name, data, perm) }
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
	write("beside/createtemp_test.go", `package beside

import (
	"os"
	"testing"
)

func temporaries(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "fake-")
	_ = f.Chmod(0o700)
	g, _ := os.CreateTemp(t.TempDir(), "fake-")
	_ = os.Chmod(g.Name(), 0o755)
	h, _ := os.CreateTemp(t.TempDir(), "log-")
	_ = h.Chmod(0o600)
	create := os.CreateTemp
	_, _ = create("", "")
}
`)
	write("beside/held_test.go", `package beside

import (
	"os"
	"path/filepath"
	"testing"
)

type holder struct{ f *os.File }

func must(f *os.File, err error) *os.File { return f }

func held(t *testing.T, dir string) {
	a, _ := os.Create(filepath.Join(dir, "a"))
	_ = os.Chmod(a.Name(), 0o755)
	b, _ := os.OpenFile(filepath.Join(dir, "b"), os.O_CREATE|os.O_WRONLY, 0o600)
	_ = os.Chmod(b.Name(), 0o700)
	var h holder
	h.f, _ = os.CreateTemp(dir, "d")
	_ = h.f.Chmod(0o755)
	c := must(os.CreateTemp(dir, "c"))
	_ = c.Chmod(0o755)
	e, _ := os.CreateTemp(dir, "e")
	_ = os.Rename(e.Name(), filepath.Join(dir, "run"))
	_ = os.Chmod(filepath.Join(dir, "run"), 0o755)
	k, _ := os.CreateTemp(dir, "k")
	_ = os.Rename(filepath.Join(dir, "old"), k.Name())
	_ = os.Chmod(filepath.Join(dir, "old"), 0o755)
}
`)
	write("beside/followed_test.go", `package beside

import (
	"os"
	"path/filepath"
	"testing"
)

type nest struct{ h holder }

func wrap(f *os.File, err error) (*os.File, error) { return f, err }

func followed(t *testing.T, dir string) {
	f, _ := os.CreateTemp(dir, "f")
	g := f
	_ = g.Chmod(0o755)
	a, _ := os.Create(filepath.Join(dir, "a"))
	var b = a
	_ = os.Chmod(b.Name(), 0o755)
	c, d := must(os.CreateTemp(dir, "c")),
		must(os.CreateTemp(dir, "d"))
	_ = d.Chmod(0o755)
	h := holder{f: must(os.CreateTemp(dir, "h"))}
	_ = h.f.Chmod(0o755)
	p := &holder{f: must(os.CreateTemp(dir, "p"))}
	_ = p.f.Chmod(0o755)
	e, _ := os.CreateTemp(dir, "e")
	r := holder{f: e}
	_ = r.f.Chmod(0o755)
	n := nest{h: holder{f: must(os.CreateTemp(dir, "n"))}}
	_ = n.h.f.Chmod(0o755)
	v := holder{f: must(os.CreateTemp(dir, "v"))}
	y := v.f
	_ = y.Chmod(0o755)
	s := &holder{f: must(os.CreateTemp(dir, "s"))}
	_ = (*s).f.Chmod(0o755)
	u := &holder{f: must(os.CreateTemp(dir, "u"))}
	_ = os.Chmod((*u).f.Name(), 0o755)
	w := &holder{}
	(*w).f, _ = os.CreateTemp(dir, "w")
	_ = w.f.Chmod(0o755)
	_ = must(os.CreateTemp(dir, "chained")).Chmod(0o755)
	_ = os.Chmod(must(os.CreateTemp(dir, "named")).Name(), 0o755)
	_ = os.Rename(must(os.CreateTemp(dir, "moved")).Name(), filepath.Join(dir, "moved"))
	_ = os.Chmod(filepath.Join(dir, "moved"), 0o755)
	x := must(wrap(os.CreateTemp(dir, "x")))
	_ = x.Chmod(0o755)
	var none *os.File
	copied := none
	_ = copied.Chmod(0o755)
	var late *os.File
	early := late
	late, _ = os.CreateTemp(dir, "late")
	_ = early.Chmod(0o755)
	_ = must(os.Open(filepath.Join(dir, "read"))).Chmod(0o755)
	_ = c
}
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
		"beside/createtemp_test.go:11", "beside/createtemp_test.go:15", "beside/createtemp_test.go:9",
		"beside/dot_test.go:4",
		"beside/followed_test.go:14", "beside/followed_test.go:17", "beside/followed_test.go:21",
		"beside/followed_test.go:23", "beside/followed_test.go:25", "beside/followed_test.go:27",
		"beside/followed_test.go:30", "beside/followed_test.go:32", "beside/followed_test.go:35",
		"beside/followed_test.go:37", "beside/followed_test.go:40", "beside/followed_test.go:42",
		"beside/followed_test.go:43", "beside/followed_test.go:44", "beside/followed_test.go:46",
		"beside/held_test.go:14", "beside/held_test.go:16", "beside/held_test.go:19", "beside/held_test.go:21",
		"beside/held_test.go:23", "fakestest/fakestest.go:5",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Unguarded named\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

// Unguarded refuses, naming the root and what it could not read, a root that is no module's: the
// parent of a module's directory, or a directory inside one, holds no go.mod. It refuses a module
// root under which it read no test file, because the module holds none or because each one is in
// another module below it. A check of such a root would name no write and pass while it read
// nothing. A module root with one test file and no write is named nothing and no error.
func TestUnguardedRefusesARootUnderWhichItReadsNoTestFile(t *testing.T) {
	t.Parallel()
	const test = "package beside\n\nimport \"testing\"\n\nfunc TestParallel(t *testing.T) { t.Parallel() }\n"
	parent := t.TempDir()
	writeUnder(t, parent, "module/go.mod", "module example.com/checked\n")
	writeUnder(t, parent, "module/beside/beside_test.go", test)
	noTests := t.TempDir()
	writeUnder(t, noTests, "go.mod", "module example.com/no-tests\n")
	writeUnder(t, noTests, "beside/beside.go", "package beside\n")
	nested := t.TempDir()
	writeUnder(t, nested, "go.mod", "module example.com/outer\n")
	writeUnder(t, nested, "inner/go.mod", "module example.com/inner\n")
	writeUnder(t, nested, "inner/beside_test.go", test)
	for _, row := range []struct{ what, root, want string }{
		{"the parent of a module's directory", parent, "holds no go.mod"},
		{"a directory inside a module", filepath.Join(parent, "module", "beside"), "holds no go.mod"},
		{"a module with no test file", noTests, "read no Go test file under"},
		{"a module whose one test file is in another module below it", nested, "read no Go test file under"},
	} {
		got, err := Unguarded(row.root)
		if got != nil || err == nil || !strings.Contains(err.Error(), row.want) || !strings.Contains(err.Error(), row.root) {
			t.Errorf("Unguarded on %s (%s) = %v, %v; want no names and an error naming the root and saying %q", row.what, row.root, got, err, row.want)
		}
	}
	if got, err := Unguarded(filepath.Join(parent, "module")); err != nil || len(got) != 0 {
		t.Errorf("Unguarded on a module root with one test file and no write = %v, %v; want nothing and no error", got, err)
	}
}
