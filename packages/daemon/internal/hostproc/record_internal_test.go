package hostproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useRecord points this process's record at a file of the test's own until the test's end, and
// answers a reader of the lines written to it since.
func useRecord(t *testing.T) func() []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scans")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	recorder.mu.Lock()
	previousFile, previousSeen, previousTests := recorder.file, recorder.seen, recorder.tests
	recorder.file, recorder.seen, recorder.tests = file, map[string]bool{}, map[uint64]string{}
	recorder.mu.Unlock()
	t.Cleanup(func() {
		recorder.mu.Lock()
		recorder.file, recorder.seen, recorder.tests = previousFile, previousSeen, previousTests
		recorder.mu.Unlock()
		_ = file.Close()
	})
	return func() []string {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	}
}

// Each scan is recorded as Check judged it: a root that is no mount of proc as the test's own, a
// mount of proc read by a reason as that, the /proc of a namespace this process is PID 1 of (the
// one way past a refusal with no reason) as that, and a refusal as one; each once, however often a
// loop makes it, and its text kept to one line.
func TestEachScanIsRecordedAsCheckJudgedIt(t *testing.T) {
	lines := useRecord(t)
	own := t.TempDir()
	record("the own scanner", own, Reason{}, false)
	record("the own scanner", own, Reason{}, false)
	record("the reasoned scanner", "/proc", Because("it reads\tits own pid\nalone"), false)
	record("the namespace's scanner", "/proc", Reason{}, false)
	record("the refused scanner", "/proc", Reason{}, true)
	const test = "TestEachScanIsRecordedAsCheckJudgedIt"
	want := []string{
		"scan\town\t" + test + "\tthe own scanner\t" + own + "\t",
		"scan\treason\t" + test + "\tthe reasoned scanner\t/proc\tit reads its own pid alone",
		"scan\tnamespace\t" + test + "\tthe namespace's scanner\t/proc\t",
		"scan\trefused\t" + test + "\tthe refused scanner\t/proc\t",
	}
	if got := lines(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the record holds\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A test's name is the outermost Test function on a stack: the test itself or a closure of it, and
// no function whose name only starts with Test.
func TestATestIsNamedByItsTestFunction(t *testing.T) {
	for function, want := range map[string]string{
		"github.com/sjawhar/legion/daemon/internal/launcher.TestFoo":             "TestFoo",
		"github.com/sjawhar/legion/daemon/internal/launcher.TestFoo.func1.2":     "TestFoo",
		"github.com/sjawhar/legion/daemon/internal/launcher_test.Test_underline": "Test_underline",
		"github.com/sjawhar/legion/daemon/internal/launcher.Test":                "Test",
		"github.com/sjawhar/legion/daemon/internal/launcher.Testing":             "",
		"github.com/sjawhar/legion/daemon/internal/launcher.(*T).TestFoo":        "",
		"github.com/sjawhar/legion/daemon/internal/launcher.launchFleet.func1":   "",
		"testing.tRunner": "",
	} {
		if got := testName(function); got != want {
			t.Errorf("testName(%q) = %q, want %q", function, got, want)
		}
	}
}

// A goroutine's test is the top-level test up its chain of starters, which a dump of every
// goroutine names: a launcher's loop takes its starter's starter's; a subtest made of another
// test's function takes the test that ran it, and not TestMain, which the main goroutine's stack
// holds under every test; a scan TestMain makes itself takes TestMain; and a goroutine whose chain
// reaches one that has returned takes none.
func TestAGoroutinesTestIsTheTopLevelTestUpItsStarters(t *testing.T) {
	const dump = `goroutine 40 [running]:
example.com/pkg.(*sweep).holders(0xc000123456)
	/src/pkg/sweep.go:10 +0x1
example.com/pkg.Run.func2()
	/src/pkg/run.go:20 +0x1
created by example.com/pkg.Run in goroutine 30
	/src/pkg/run.go:19 +0x1

goroutine 30 [select]:
example.com/pkg.Run({0x1, 0x2})
	/src/pkg/run.go:18 +0x1
example.com/pkg.launchFleet.func1()
	/src/pkg/fleet_test.go:9 +0x1
created by example.com/pkg.launchFleet in goroutine 23
	/src/pkg/fleet_test.go:8 +0x1

goroutine 23 [chan receive]:
example.com/pkg.newFleet(...)
	/src/pkg/fleet_test.go:5
example.com/pkg.TestTheLauncherSweeps(0xc000102380)
	/src/pkg/sweep_test.go:3 +0x1
testing.tRunner(0xc000102380, 0x1)
	/go/src/testing/testing.go:1792 +0xf4
created by testing.(*T).Run in goroutine 22
	/go/src/testing/testing.go:1851 +0x413

goroutine 22 [chan receive]:
testing.(*T).Run(0xc000102000, {0x1, 0x2}, 0x3)
	/go/src/testing/testing.go:1859 +0x431
example.com/pkg.TestTheOriginalConditions.func3(0xc000102000)
	/src/pkg/hostile_test.go:40 +0x1
testing.tRunner(0xc000102000, 0x1)
	/go/src/testing/testing.go:1792 +0xf4
created by testing.(*T).Run in goroutine 21
	/go/src/testing/testing.go:1851 +0x413

goroutine 21 [chan receive]:
testing.(*T).Run(0xc000101000, {0x1, 0x2}, 0x3)
	/go/src/testing/testing.go:1859 +0x431
example.com/pkg.TestTheOriginalConditions(0xc000101000)
	/src/pkg/hostile_test.go:30 +0x1
testing.tRunner(0xc000101000, 0x1)
	/go/src/testing/testing.go:1792 +0xf4
created by testing.(*T).Run in goroutine 1
	/go/src/testing/testing.go:1851 +0x413

goroutine 1 [chan receive]:
testing.(*T).Run(0xc000100000, {0x1, 0x2}, 0x3)
	/go/src/testing/testing.go:1859 +0x431
testing.runTests.func1(0xc000100000)
	/go/src/testing/testing.go:2279 +0x37
testing.tRunner(0xc000100000, 0x1)
	/go/src/testing/testing.go:1792 +0xf4
testing.runTests(...)
	/go/src/testing/testing.go:2277 +0x4a5
testing.(*M).Run(0xc000180000)
	/go/src/testing/testing.go:2145 +0x7a5
example.com/pkg.TestMain(0xc000180000)
	/src/pkg/main_test.go:12 +0x1
main.main()
	_testmain.go:100 +0xa8

goroutine 50 [running]:
example.com/pkg.orphan()
	/src/pkg/orphan.go:3 +0x1
created by example.com/pkg.starter in goroutine 49
	/src/pkg/orphan.go:2 +0x1
`
	for _, c := range []struct {
		id   uint64
		want string
		why  string
	}{
		{40, "TestTheOriginalConditions", "a loop of a launcher a subtest made of TestTheLauncherSweeps started"},
		{23, "TestTheOriginalConditions", "the subtest made of TestTheLauncherSweeps"},
		{21, "TestTheOriginalConditions", "the top-level test itself"},
		{50, "", "its starter, goroutine 49, has returned"},
	} {
		if got := testOfChain(c.id, []byte(dump)); got != c.want {
			t.Errorf("the test of goroutine %d is %q, want %q: %s", c.id, got, c.want, c.why)
		}
	}
	const helper = `goroutine 1 [running]:
example.com/pkg.reapNamespace()
	/src/pkg/reap.go:3 +0x1
example.com/pkg.TestMain(0xc000180000)
	/src/pkg/main_test.go:12 +0x1
main.main()
	_testmain.go:100 +0xa8
`
	if got := testOfChain(1, []byte(helper)); got != "TestMain" {
		t.Errorf("the test of a scan TestMain makes is %q, want TestMain", got)
	}
}
