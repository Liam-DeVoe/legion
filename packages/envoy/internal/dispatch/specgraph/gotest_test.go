package specgraph

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The files in testdata/gotest are real output of `go test -json -count=1 -trimpath` (Go 1.27.3),
// each of one package of a scratch module example.com/acme/gotest:
//
//	pass.jsonl          TestA writes one log line and passes; TestAB passes
//	fail.jsonl          TestA fails with t.Error; TestSkipped calls t.Skip
//	subtest.jsonl       TestParent runs the subtests one, two (which runs two/deeper) and three,
//	                    and three fails
//	timeout.jsonl       TestFast passes; TestSlow runs a subtest first, which passes, then sleeps
//	                    5 s under -timeout 1s
//	p-and-p-test.jsonl  TestB of b_test.go (package b) passes; TestB of bx_test.go (package
//	                    b_test) fails
//	exit.jsonl          TestFirst passes; TestExits runs a subtest first, which passes, then
//	                    calls os.Exit(3)
//	long-output.jsonl   TestLong prints 20 lines of 4,000 bytes, then "the last line"

// Each top-level test gives one entry with its outcome, in the order the tests ran.
func TestReadGoTestJSONReadsEachOutcome(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		file string
		want []string // "<package> <test> <outcome>"
	}{
		{"pass.jsonl", []string{"example.com/acme/gotest/pass TestA pass", "example.com/acme/gotest/pass TestAB pass"}},
		{"fail.jsonl", []string{"example.com/acme/gotest/fail TestA fail", "example.com/acme/gotest/fail TestSkipped skip"}},
	} {
		t.Run(row.file, func(t *testing.T) {
			t.Parallel()
			if got := outcomes(readReport(t, row.file)); !reflect.DeepEqual(got, row.want) {
				t.Errorf("ReadGoTestJSON(%s) = %q, want %q", row.file, got, row.want)
			}
		})
	}
}

// A test with subtests gives one entry, with the outcome of the parent itself and the output of
// its subtests. A name that runs twice in one package, as TestB of package b and of package b_test
// does, gives two entries, each with its own outcome and output.
func TestReadGoTestJSONGivesTopLevelTests(t *testing.T) {
	t.Parallel()
	parent := readReport(t, "subtest.jsonl")
	if got, want := outcomes(parent), []string{"example.com/acme/gotest/sub TestParent fail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadGoTestJSON(subtest.jsonl) = %q, want %q", got, want)
	}
	if !strings.Contains(parent[0].Output, "sub_test.go:11: the third case fails") {
		t.Errorf("TestParent's output holds no line of its subtest three: %q", parent[0].Output)
	}
	twice := readReport(t, "p-and-p-test.jsonl")
	if got, want := outcomes(twice), []string{"example.com/acme/gotest/b TestB pass", "example.com/acme/gotest/b TestB fail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadGoTestJSON(p-and-p-test.jsonl) = %q, want %q", got, want)
	}
	if !strings.Contains(twice[0].Output, "b_test.go:6: the test of package b\n") || strings.Contains(twice[0].Output, "bx_test.go") {
		t.Errorf("the first TestB's output is not its own: %q", twice[0].Output)
	}
	if !strings.Contains(twice[1].Output, "bx_test.go:6: the test of package b_test fails\n") || strings.Contains(twice[1].Output, "b_test.go:6: the test of package b\n") {
		t.Errorf("the second TestB's output is not its own: %q", twice[1].Output)
	}
}

// A test that go test killed at its time limit could not run: it never reads fail, since the
// infrastructure, not the code, ended it, nor pass from a subtest that passed before the kill.
// Should a fail event of the test follow the panic, it still could not run.
func TestAKilledTestCouldNotRun(t *testing.T) {
	t.Parallel()
	want := []string{"example.com/acme/gotest/timeout TestFast pass", "example.com/acme/gotest/timeout TestSlow could_not_run"}
	if got := outcomes(readReport(t, "timeout.jsonl")); !reflect.DeepEqual(got, want) {
		t.Errorf("ReadGoTestJSON(timeout.jsonl) = %q, want %q", got, want)
	}
	report, err := os.ReadFile("testdata/gotest/timeout.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ended := string(report) + `{"Action":"fail","Package":"example.com/acme/gotest/timeout","Test":"TestSlow","Elapsed":1}` + "\n"
	got, err := ReadGoTestJSON(strings.NewReader(ended))
	if err != nil || !reflect.DeepEqual(outcomes(got), want) {
		t.Errorf("ReadGoTestJSON(timeout.jsonl and a fail event) = %q, %v, want %q", outcomes(got), err, want)
	}
}

// A test that started and has no end, as when its process exits in the middle of it, could not
// run, even after one of its subtests passed.
func TestAStartedTestWithNoEndCouldNotRun(t *testing.T) {
	t.Parallel()
	report := readReport(t, "exit.jsonl")
	if got, want := outcomes(report), []string{"example.com/acme/gotest/exit TestFirst pass", "example.com/acme/gotest/exit TestExits could_not_run"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ReadGoTestJSON(exit.jsonl) = %q, want %q", got, want)
	}
}

// The output of an entry is the last 64 KiB of what its test wrote: TestLong writes 80,060 bytes,
// so the first 14,524 go, lines 00 to 03 with them.
func TestOutputIsTheLast64KiB(t *testing.T) {
	t.Parallel()
	report := readReport(t, "long-output.jsonl")
	if len(report) != 1 || report[0].Outcome != "pass" {
		t.Fatalf("ReadGoTestJSON(long-output.jsonl) = %q, want one pass", outcomes(report))
	}
	output := report[0].Output
	if len(output) != 64<<10 {
		t.Errorf("the output holds %d bytes, want %d", len(output), 64<<10)
	}
	if !strings.HasSuffix(output, "the last line\n--- PASS: TestLong (0.00s)\n") {
		t.Errorf("the output does not end with the test's last lines: %q", output[max(0, len(output)-80):])
	}
	if strings.Contains(output, "line 03 ") || !strings.Contains(output, "line 04 ") {
		t.Errorf("the output does not start inside line 03: %q", output[:80])
	}
}

// A report with a line that is no event of go test -json is refused, so that an unreadable report
// never reads as a run of no tests. An empty report holds no test.
func TestReadGoTestJSONRefusesALineThatIsNoEvent(t *testing.T) {
	t.Parallel()
	if _, err := ReadGoTestJSON(strings.NewReader("{\"Action\":\"run\",\"Package\":\"p\",\"Test\":\"TestA\"}\nok  \tp\t0.1s\n")); err == nil {
		t.Error("ReadGoTestJSON read a line of plain text without an error")
	}
	if got, err := ReadGoTestJSON(strings.NewReader("")); err != nil || len(got) != 0 {
		t.Errorf("ReadGoTestJSON(\"\") = %+v, %v, want no entry and no error", got, err)
	}
}

// readReport reads testdata/gotest/<file> through ReadGoTestJSON.
func readReport(t *testing.T, file string) []TestOutcome {
	t.Helper()
	f, err := os.Open("testdata/gotest/" + file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	report, err := ReadGoTestJSON(f)
	if err != nil {
		t.Fatalf("ReadGoTestJSON(%s): %v", file, err)
	}
	return report
}

// outcomes is "<package> <test> <outcome>" for each entry, in order.
func outcomes(report []TestOutcome) []string {
	var all []string
	for _, o := range report {
		all = append(all, o.Package+" "+o.Test+" "+o.Outcome)
	}
	return all
}
