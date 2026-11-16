package specgraph

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// The files in testdata/gotest are real output of `go test -json -count=1 -trimpath` (Go 1.27.3),
// each of one package of a scratch module example.com/acme/gotest. These seven come from an
// earlier scratch module:
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
//
// Each other report <name>.jsonl is of the package testdata/gotest/cases/<name>, whose doc says
// what its tests do, and testdata/gotest/make-reports.sh makes it again: the packages kill* under
// -timeout 1s, count2 under -count=2 in place of -count=1.

// acme is the import path of the packages of testdata/gotest/cases, before the package's name.
const acme = "example.com/acme/gotest/"

// Each top-level test gives one entry with its outcome, in the order the tests ran. Under
// -count=2 each test runs two times, and each run is an entry of its own.
func TestReadGoTestJSONReadsEachOutcome(t *testing.T) {
	t.Parallel()
	readReports(t, []reportRow{
		{"pass.jsonl", []string{"example.com/acme/gotest/pass TestA pass", "example.com/acme/gotest/pass TestAB pass"}},
		{"fail.jsonl", []string{"example.com/acme/gotest/fail TestA fail", "example.com/acme/gotest/fail TestSkipped skip"}},
		{"count2.jsonl", []string{
			acme + "count2 TestSequentialPasses pass", acme + "count2 TestParallelPasses pass", acme + "count2 TestParallelFails fail",
			acme + "count2 TestSequentialPasses pass", acme + "count2 TestParallelPasses pass", acme + "count2 TestParallelFails fail"}},
	})
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

// A test that go test killed at its time limit has no end event of its own in the report, so it
// could not run: it never reads fail, nor pass from a subtest that passed before the kill. A test
// that never started has no entry. A test that ended before the kill reads its own outcome, also
// when go test's converter gives it the kill line, as it gives each line to the test it named
// last: in killparallel.jsonl that is the last of TestPasses and TestFails to end.
func TestAKilledTestCouldNotRun(t *testing.T) {
	t.Parallel()
	readReports(t, []reportRow{
		{"timeout.jsonl", []string{"example.com/acme/gotest/timeout TestFast pass", "example.com/acme/gotest/timeout TestSlow could_not_run"}},
		{"killalone.jsonl", []string{acme + "killalone TestSleepsPastTheTimeout could_not_run"}},
		{"killfirst.jsonl", []string{acme + "killfirst TestSleepsPastTheTimeout could_not_run"}},
		{"killparallel.jsonl", []string{acme + "killparallel TestSleeps could_not_run", acme + "killparallel TestPasses pass", acme + "killparallel TestFails fail"}},
	})
}

// What a test prints decides no outcome: the outcome is the test's own end event. Here a test
// prints go test's kill line at the start of a line, on its standard output or its standard
// error, from a child go test that timed out, in a line longer than the 1,024 bytes at which go
// test's converter cuts an event, or in a subtest; a test that prints nothing gets the line of its
// parallel sibling from the converter, after a frame of its own (silent: t.Attr; silentlog: t.Log);
// a test quotes the kill line in its log; and a test prints, with no marker, result lines and JSON
// events of the other tests and of a test that does not exist.
func TestWhatATestPrintsDecidesNoOutcome(t *testing.T) {
	t.Parallel()
	readReports(t, []reportRow{
		{"printfails.jsonl", []string{acme + "printfails TestPrintsTheKillLineThenFails fail"}},
		{"printpasses.jsonl", []string{acme + "printpasses TestPrintsTheKillLineThenPasses pass"}},
		{"stderr.jsonl", []string{acme + "stderr TestPrintsTheKillLineOnStandardErrorThenFails fail", acme + "stderr TestPrintsTheKillLineOnStandardErrorThenPasses pass"}},
		{"child.jsonl", []string{acme + "child TestChildTimesOut fail", acme + "child TestChildSleeps skip"}},
		{"longline.jsonl", []string{acme + "longline TestPrintsALongLineThatHoldsTheKillLineThenFails fail"}},
		{"silent.jsonl", []string{acme + "silent TestSilent pass", acme + "silent TestLoud pass"}},
		{"silentlog.jsonl", []string{acme + "silentlog TestP1 pass", acme + "silentlog TestP2 pass"}},
		{"subprint.jsonl", []string{acme + "subprint TestParentFails fail", acme + "subprint TestParentPasses pass"}},
		{"logquote.jsonl", []string{acme + "logquote TestLogQuotesTheKillLineThenFails fail", acme + "logquote TestLogQuotesTheKillLineThenPasses pass",
			acme + "logquote TestSubtestLogQuotesTheKillLineThenFails fail"}},
		{"plainlines.jsonl", []string{acme + "plainlines TestPasses pass", acme + "plainlines TestFails fail", acme + "plainlines TestPrinter pass"}},
	})
}

// A test that started and has no end, as when its process exits in the middle of it, could not
// run, even after one of its subtests passed, and so does one that printed the kill line first.
func TestAStartedTestWithNoEndCouldNotRun(t *testing.T) {
	t.Parallel()
	readReports(t, []reportRow{
		{"exit.jsonl", []string{"example.com/acme/gotest/exit TestFirst pass", "example.com/acme/gotest/exit TestExits could_not_run"}},
		{"exitkill.jsonl", []string{acme + "exitkill TestPrintsTheKillLineThenExits could_not_run"}},
	})
}

// A run has one end event of its own. A test that prints a result line with the byte 0x16 before
// it, the marker of the testing package's own frames, makes go test's converter write an end
// event for the test that the line names: after that test's real end, before it when a parallel
// test prints it, or for itself. And two parallel tests of one name, one of package samename and
// one of samename_test, both start before either ends, so the report gives both ends to the
// second run and none to the first. So a run with two ends could not run, and neither its first
// nor its last end is its outcome.
func TestARunWithTwoEndsCouldNotRun(t *testing.T) {
	t.Parallel()
	readReports(t, []reportRow{
		{"markedpass.jsonl", []string{acme + "markedpass TestFails could_not_run", acme + "markedpass TestPrinter pass", acme + "markedpass TestSelfFails could_not_run"}},
		{"markedfail.jsonl", []string{acme + "markedfail TestPasses could_not_run", acme + "markedfail TestPrinter pass", acme + "markedfail TestSelfPasses could_not_run"}},
		{"markedparallel.jsonl", []string{acme + "markedparallel TestRunningFails could_not_run", acme + "markedparallel TestPrinter pass"}},
		{"samename.jsonl", []string{acme + "samename TestSame could_not_run", acme + "samename TestSame could_not_run"}},
	})
}

// The output of an entry is what its test wrote, whole up to 64 KiB. A longer output keeps its
// first 4 KiB, which name the test, and its last 60 KiB, which say how it ended, with one line
// between them that says how many bytes are cut. TestLong writes 80,060 bytes: the first part ends
// inside line 01 and the last part starts inside line 04, so 14,524 bytes are cut.
func TestOutputKeepsItsStartAndItsEnd(t *testing.T) {
	t.Parallel()
	report := readReport(t, "long-output.jsonl")
	if len(report) != 1 || report[0].Outcome != "pass" {
		t.Fatalf("ReadGoTestJSON(long-output.jsonl) = %q, want one pass", outcomes(report))
	}
	const note = "\n[The middle of the output is cut here: 14524 of its 80060 bytes.]\n"
	start, end, cut := strings.Cut(report[0].Output, note)
	if !cut {
		t.Fatalf("the output holds no line %q: %q … %q", note, report[0].Output[:80], report[0].Output[len(report[0].Output)-80:])
	}
	if len(start) != 4<<10 || len(end) != 60<<10 {
		t.Errorf("the output keeps %d bytes before the note and %d after it, want %d and %d", len(start), len(end), 4<<10, 60<<10)
	}
	if !strings.HasPrefix(start, "=== RUN   TestLong\nline 00 ") || !strings.Contains(start, "line 01 ") || strings.Contains(start, "line 02 ") {
		t.Errorf("the first part is not the start of the output up to line 01: %q", start[:80])
	}
	if !strings.HasSuffix(end, "the last line\n--- PASS: TestLong (0.00s)\n") || !strings.Contains(end, "line 05 ") || strings.Contains(end, "line 04 ") {
		t.Errorf("the last part is not the end of the output from inside line 04: %q", end[:80])
	}
}

// The bound counts bytes, and each part starts and ends at a whole UTF-8 character: a character
// that the cut falls inside goes with the cut bytes.
func TestOutputIsCutAtItsBound(t *testing.T) {
	t.Parallel()
	x := strings.Repeat
	for _, row := range []struct{ name, output, want string }{
		{"64 KiB", x("a", 65536), x("a", 65536)},
		{"one byte more", x("a", 65537),
			x("a", 4096) + "\n[The middle of the output is cut here: 1 of its 65537 bytes.]\n" + x("a", 61440)},
		{"1 MiB between a first and a last line", "the first line\n" + x("x", 1<<20) + "\nthe last line\n",
			"the first line\n" + x("x", 4081) + "\n[The middle of the output is cut here: 983070 of its 1048606 bytes.]\n" + x("x", 61425) + "\nthe last line\n"},
		{"the first cut inside a character of 4 bytes", x("a", 4094) + "😀" + x("b", 70000),
			x("a", 4094) + "\n[The middle of the output is cut here: 8564 of its 74098 bytes.]\n" + x("b", 61440)},
		{"the last cut inside a character of 3 bytes", x("a", 70000) + "€" + x("b", 61438),
			x("a", 4096) + "\n[The middle of the output is cut here: 65907 of its 131441 bytes.]\n" + x("b", 61438)},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadGoTestJSON(strings.NewReader(reportOf(row.output)))
			if err != nil || len(got) != 1 {
				t.Fatalf("ReadGoTestJSON = %q, %v, want one entry", outcomes(got), err)
			}
			if got[0].Output != row.want {
				g, w := got[0].Output, row.want
				t.Errorf("the output holds %d bytes, want %d: got %q … %q, want %q … %q", len(g), len(w), g[:min(len(g), 120)], g[max(0, len(g)-120):], w[:min(len(w), 120)], w[max(0, len(w)-120):])
			}
		})
	}
}

// A report with a line that is no JSON value is refused. An empty report holds no test.
func TestReadGoTestJSONRefusesALineThatIsNoEvent(t *testing.T) {
	t.Parallel()
	if _, err := ReadGoTestJSON(strings.NewReader("{\"Action\":\"run\",\"Package\":\"p\",\"Test\":\"TestA\"}\nok  \tp\t0.1s\n")); err == nil {
		t.Error("ReadGoTestJSON read a line of plain text without an error")
	}
	if got, err := ReadGoTestJSON(strings.NewReader("")); err != nil || len(got) != 0 {
		t.Errorf("ReadGoTestJSON(\"\") = %+v, %v, want no entry and no error", got, err)
	}
}

// A JSON value that decodes and is no event, a null or an object with no Action, is refused too,
// alone, before an event and after one. The control, the same report with each such value left
// out, reads with no error.
func TestReadGoTestJSONRefusesAValueWithNoAction(t *testing.T) {
	t.Parallel()
	const event = `{"Action":"run","Package":"p","Test":"TestA"}`
	for _, row := range []struct{ name, value string }{
		{"a null", "null"},
		{"an empty object", "{}"},
		{"an object of a test with no Action", `{"Package":"p","Test":"TestA","Output":"ok\n"}`},
		{"an object of another tool", `{"name":"TestA","status":"passed"}`},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			for _, report := range []string{row.value + "\n", row.value + "\n" + event + "\n", event + "\n" + row.value + "\n"} {
				if got, err := ReadGoTestJSON(strings.NewReader(report)); err == nil {
					t.Errorf("ReadGoTestJSON(%q) = %q with no error, want an error", report, outcomes(got))
				}
			}
		})
	}
	if got, err := ReadGoTestJSON(strings.NewReader(event + "\n")); err != nil || !reflect.DeepEqual(outcomes(got), []string{"p TestA could_not_run"}) {
		t.Errorf("ReadGoTestJSON(the event alone) = %q, %v, want one entry and no error", outcomes(got), err)
	}
}

// reportRow is a report of testdata/gotest and the entries that ReadGoTestJSON gives for it.
type reportRow struct {
	file string
	want []string // "<package> <test> <outcome>"
}

// readReports reads each row's report in a subtest named for its file.
func readReports(t *testing.T, rows []reportRow) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.file, func(t *testing.T) {
			t.Parallel()
			if got := outcomes(readReport(t, row.file)); !reflect.DeepEqual(got, row.want) {
				t.Errorf("ReadGoTestJSON(%s) = %q, want %q", row.file, got, row.want)
			}
		})
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

// reportOf is a report of one test, TestA of package p, that writes output and passes. As go
// test's converter does, it gives the output in events of 1,024 bytes at most, each of whole UTF-8
// characters.
func reportOf(output string) string {
	var b strings.Builder
	b.WriteString(`{"Action":"run","Package":"p","Test":"TestA"}` + "\n")
	for output != "" {
		n := min(len(output), 1024)
		for n < len(output) && !utf8.RuneStart(output[n]) {
			n--
		}
		event, err := json.Marshal(map[string]string{"Action": "output", "Package": "p", "Test": "TestA", "Output": output[:n]})
		if err != nil {
			panic(err)
		}
		b.Write(append(event, '\n'))
		output = output[n:]
	}
	b.WriteString(`{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0}` + "\n")
	return b.String()
}

// outcomes is "<package> <test> <outcome>" for each entry, in order.
func outcomes(report []TestOutcome) []string {
	var all []string
	for _, o := range report {
		all = append(all, o.Package+" "+o.Test+" "+o.Outcome)
	}
	return all
}
