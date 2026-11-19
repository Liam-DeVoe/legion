package hostproc_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/hostproc"
	"github.com/sjawhar/legion/daemon/internal/hostproc/hostproctest"
)

// newRecord is an empty record of scans for a child of this test binary, which opens the file
// RecordEnv names as it starts (the wrapper makes it the same way, before the binary runs).
func newRecord(t *testing.T) string {
	t.Helper()
	record := filepath.Join(t.TempDir(), "scans")
	if err := os.WriteFile(record, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(hostproc.RecordEnv, record)
	return record
}

// recordLines is every line of a record, its fields split at the tabs.
func recordLines(t *testing.T, record string) [][]string {
	t.Helper()
	body, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var lines [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		if line != "" {
			lines = append(lines, strings.Split(line, "\t"))
		}
	}
	return lines
}

// checkOnAGoroutinesGoroutine calls Check on a goroutine started by a goroutine this function
// starts, as a launcher's loops run. It is no function of a test, so neither goroutine's stack names
// one: only the chain of starters, up to the test that called this, can.
func checkOnAGoroutinesGoroutine(scanner, root string) {
	done := make(chan struct{})
	go func() {
		inner := make(chan struct{})
		go func() {
			defer close(inner)
			hostproc.Check(scanner, root, hostproc.Reason{})
		}()
		<-inner
		close(done)
	}()
	<-done
}

// A test binary with a record writes its binary line as it starts, and each scan Check lets list
// once, with the test that made it: on the test's own goroutine, and on a goroutine started by a
// goroutine the test's helper started (checkOnAGoroutinesGoroutine), whose stacks name no test.
// The root of the test's own is os.TempDir(), which is no mount of proc and the same in the child.
func TestEachScanIsRecordedWithTheTestThatMadeIt(t *testing.T) {
	record := newRecord(t)
	own := os.TempDir()
	reason := hostproc.Because("it takes only this test's own pid")
	hostproctest.Allowed(t, func() {
		hostproc.Check("the test goroutine's scanner", own, hostproc.Reason{})
		hostproc.Check("the test goroutine's scanner", own, hostproc.Reason{})
		hostproc.Check("the reasoned scanner", "/proc", reason)
		checkOnAGoroutinesGoroutine("the scanner of a goroutine's goroutine", own)
	})
	lines := recordLines(t, record)
	const test = "TestEachScanIsRecordedWithTheTestThatMadeIt"
	want := [][]string{
		{"scan", hostproc.ScanOwn, test, "the test goroutine's scanner", own, ""},
		{"scan", hostproc.ScanReason, test, "the reasoned scanner", "/proc", reason.String()},
		{"scan", hostproc.ScanOwn, test, "the scanner of a goroutine's goroutine", own, ""},
	}
	if len(lines) != 1+len(want) || len(lines[0]) != 2 || lines[0][0] != "binary" || lines[0][1] == "" {
		t.Fatalf("the record holds %q, want the child's binary line and then %q", lines, want)
	}
	for i, line := range lines[1:] {
		if !slices.Equal(line, want[i]) {
			t.Errorf("line %d of the record is %q, want %q", i+2, line, want[i])
		}
	}
}

// A scan Check refuses is recorded before the binary ends, with the test that made it.
func TestARefusedScanIsRecordedBeforeTheBinaryEnds(t *testing.T) {
	record := newRecord(t)
	hostproctest.Refused(t, "the refused scanner", func() {
		hostproc.Check("the refused scanner", "/proc", hostproc.Reason{})
	})
	lines := recordLines(t, record)
	want := []string{"scan", hostproc.ScanRefused, "TestARefusedScanIsRecordedBeforeTheBinaryEnds", "the refused scanner", "/proc", ""}
	if len(lines) != 2 || lines[0][0] != "binary" || !slices.Equal(lines[1], want) {
		t.Fatalf("the record holds %q, want the child's binary line and then %q", lines, want)
	}
}

// A binary given no record writes none: the control of the two above, a scan in a child whose
// record variable names nothing.
func TestABinaryGivenNoRecordWritesNone(t *testing.T) {
	record := newRecord(t)
	t.Setenv(hostproc.RecordEnv, "")
	hostproctest.Allowed(t, func() {
		hostproc.Check("the reasoned scanner", "/proc", hostproc.Because("it takes only this test's own pid"))
	})
	if lines := recordLines(t, record); len(lines) != 0 {
		t.Fatalf("a child with no record wrote %q", lines)
	}
}
