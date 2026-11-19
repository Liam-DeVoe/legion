package hostproc

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// RecordEnv names the file a test binary records its scans in, which
// .github/scripts/private_proc.py sets for each test binary it runs: Check writes each scan it
// judges there, once per test, scanner, root and verdict, and the wrapper prints from it the tests
// that read a process table, each with the root it read. A binary started with no such file, a test
// run by hand, records nothing.
//
// The file holds one line per entry, its fields separated by tabs:
//
//	binary <pid>                                  written once, when a test binary that links
//	                                              this package starts, so the wrapper knows a
//	                                              binary with no scan line recorded and scanned
//	                                              nothing
//	scan <kind> <test> <scanner> <root> <reason>  one scan: kind is ScanOwn, ScanNamespace,
//	                                              ScanReason or ScanRefused; test is "" when no
//	                                              goroutine up the scan's chain runs a test
const RecordEnv = "HOSTPROC_RECORD"

// The kinds of root a scan line names.
const (
	// ScanOwn is a root that is no mount of proc: a directory the test wrote, or one of links to
	// the processes it started.
	ScanOwn = "own"
	// ScanNamespace is the /proc of a PID namespace whose first process is this one.
	ScanNamespace = "namespace"
	// ScanReason is a mount of proc the scanner read by its Reason: the machine's /proc in a test
	// run by hand, and the binary's own private /proc under the wrapper.
	ScanReason = "reason"
	// ScanRefused is a mount of proc the scanner had neither a root of the test's own nor a Reason
	// for: Check ended the binary on it.
	ScanRefused = "refused"
)

// recorder is this process's record, when RecordEnv names one it can open: seen keeps each line
// to one write, and tests the test of each goroutine whose own stack names none (testOf).
var recorder struct {
	mu    sync.Mutex
	file  *os.File
	seen  map[string]bool
	tests map[uint64]string
}

// A test binary opens its record as it starts, before any test runs, and writes its binary line,
// whether or not a test of it ever scans.
func init() {
	openRecord()
}

// openRecord opens the file RecordEnv names, in a test binary. The file is the wrapper's, made
// before the binary ran, and opened without O_CREAT, for the reason the wrapper's own record is
// (private_proc.py). A process that cannot open it — a child a test started in a mount namespace
// of its own, where the path is another directory — records nothing; the wrapper holds each binary
// to its own binary line.
func openRecord() {
	path := os.Getenv(RecordEnv)
	if path == "" || !inTest() {
		return
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return
	}
	recorder.file, recorder.seen, recorder.tests = file, map[string]bool{}, map[uint64]string{}
	writeRecord("binary", strconv.Itoa(os.Getpid()))
}

// record writes what Check judged of one scan, once per test, scanner, root, verdict and reason.
func record(scanner, root string, reason Reason, refused bool) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.file == nil {
		return
	}
	kind := ScanRefused
	switch {
	case refused:
	case !isProc(root):
		kind = ScanOwn
	case reason.why != "":
		kind = ScanReason
	default:
		kind = ScanNamespace
	}
	fields := []string{"scan", kind, testOf(), scanner, root, reason.why}
	key := strings.Join(fields, "\x00")
	if recorder.seen[key] {
		return
	}
	recorder.seen[key] = true
	writeRecord(fields...)
}

// writeRecord writes one line in one write, so lines of the binary and of its children never
// interleave. A record that takes no line ends the binary: a list with a scan missing from it would
// say a test read nothing that read a process table.
func writeRecord(fields ...string) {
	for i, field := range fields {
		fields[i] = strings.Join(strings.Fields(field), " ")
	}
	if _, err := recorder.file.WriteString(strings.Join(fields, "\t") + "\n"); err != nil {
		fmt.Fprintf(os.Stderr, "hostproc: cannot record a scan in %s (%s): %v\n", recorder.file.Name(), RecordEnv, err)
		exit(1)
	}
}

// testOf is the test the calling goroutine scans for: the top-level test go test ran, which is
// the outermost Test function on the stack of the calling goroutine or of the goroutines that
// started it, one up from the next, as Go's dump of every goroutine names each one's starter
// ("created by … in goroutine N"). That chain holds a test's own goroutine and every subtest's, so
// a subtest made of another test's function (t.Run(name, TestX)), or a launcher's loop a test
// started, is named by the test that ran it. TestMain stands for no test the chain passes through,
// and names a scan only where no other test does, one a helper child makes from its TestMain. It is
// "" when no goroutine of the chain still runs a test: a starter that has returned takes its stack
// with it. The answer for each goroutine is kept, so a loop that scans every second dumps every
// goroutine once. The caller holds recorder.mu.
func testOf() string {
	id := goroutineID()
	if test, ok := recorder.tests[id]; ok {
		return test
	}
	test := testOfChain(id, allStacks())
	recorder.tests[id] = test
	return test
}

// testName is the test a function belongs to: TestX for "<package path>.TestX" and for its
// closures, "<package path>.TestX.func1.2", or "" for a function of no test. A test's name is Test
// and then nothing or a character that is no lower-case letter, as go test reads it.
func testName(function string) string {
	name := function[strings.LastIndexByte(function, '/')+1:]
	_, rest, ok := strings.Cut(name, ".")
	if !ok {
		return ""
	}
	test, _, _ := strings.Cut(rest, ".")
	if !strings.HasPrefix(test, "Test") {
		return ""
	}
	if next, _ := utf8.DecodeRuneInString(test[len("Test"):]); next != utf8.RuneError && unicode.IsLower(next) {
		return ""
	}
	return test
}

// goroutineID is the calling goroutine's id, from the first line of its stack: "goroutine N [".
func goroutineID() uint64 {
	var buf [64]byte
	fields := strings.Fields(string(buf[:runtime.Stack(buf[:], false)]))
	if len(fields) < 2 {
		return 0
	}
	id, _ := strconv.ParseUint(fields[1], 10, 64)
	return id
}

// allStacks is Go's dump of every goroutine of the process.
func allStacks() []byte {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
}

// goroutineStack is what testOfChain needs of one goroutine of a dump: the outermost Test function
// on its stack but TestMain, whether TestMain is on it, and the goroutine that started it, 0 for
// none.
type goroutineStack struct {
	test    string
	main    bool
	starter uint64
}

// testOfChain is testOf's answer for goroutine id, read from dump: the test of the highest
// goroutine of id's chain of starters whose stack names one, TestMain when only TestMain is named,
// and "" when none is.
func testOfChain(id uint64, dump []byte) string {
	stacks := parseStacks(dump)
	test, main := "", false
	seen := map[uint64]bool{}
	for g := id; g != 0 && !seen[g]; g = stacks[g].starter {
		seen[g] = true
		stack, ok := stacks[g]
		if !ok {
			break
		}
		if stack.test != "" {
			test = stack.test
		}
		main = main || stack.main
	}
	if test == "" && main {
		return "TestMain"
	}
	return test
}

// parseStacks reads a dump of every goroutine: blocks apart by a blank line, each a "goroutine N
// [state]:" line, then a line naming each function, innermost first, with its file and line on the
// line after it, then "created by F in goroutine M" for a goroutine another started.
func parseStacks(dump []byte) map[uint64]goroutineStack {
	stacks := map[uint64]goroutineStack{}
	for _, block := range bytes.Split(dump, []byte("\n\n")) {
		lines := strings.Split(strings.TrimSpace(string(block)), "\n")
		header := strings.Fields(lines[0])
		if len(header) < 2 || header[0] != "goroutine" {
			continue
		}
		id, err := strconv.ParseUint(header[1], 10, 64)
		if err != nil {
			continue
		}
		var stack goroutineStack
		for _, line := range lines[1:] {
			if strings.HasPrefix(line, "\t") {
				continue
			}
			if created, ok := strings.CutPrefix(line, "created by "); ok {
				if _, starter, ok := strings.Cut(created, " in goroutine "); ok {
					stack.starter, _ = strconv.ParseUint(strings.TrimSpace(starter), 10, 64)
				}
				continue
			}
			function := line
			if open := strings.LastIndexByte(line, '('); open > 0 {
				function = line[:open]
			}
			switch test := testName(function); test {
			case "":
			case "TestMain":
				stack.main = true
			default:
				stack.test = test
			}
		}
		stacks[id] = stack
	}
	return stacks
}
