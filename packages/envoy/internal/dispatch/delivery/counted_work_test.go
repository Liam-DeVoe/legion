package delivery

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/envoy/internal/testwait"
)

// THE COUNTED WORK OF A CITATION READING (INFRA-3149). The rows that hold citedIssues to work
// linear in its body (TestCitedIssuesCostsNoMoreThanTheBodyIsLong and
// TestCitedIssuesCostsNoMoreForACitationFarFromASpace) count that work with Go's coverage counters,
// as pmdoc's linear rows count theirs (pmdoc/counted_work_test.go): the compiler puts a counter in
// every basic block of each package it instruments, so no loop of those packages escapes them. The
// two counts the rows kept before see a walk back that calls authorityBefore (AuthorityReads) or
// that copies what it reads (allocatedBy). A walk back to the nearest space through
// strings.LastIndexAny or strings.LastIndexByte does neither: its loop runs in the standard
// library, and the counters see it there.
//
// The counted binary is this package's test binary built once for the whole run, with -cover
// -covermode=count and -coverpkg naming countedPackages: this package, and the standard library's
// packages the reading runs in or a walk back over a string is written with. A body is read in that
// binary as a child process (TestCountedCitedIssuesOfOneBody), selected by the environment
// countedChild sets, so nothing a row does in its own process is counted. The child runs twice for
// a body: once to build the body alone, and once to build it and read its citations. The work of
// the reading is the statements of each block times the times it ran, summed over the profile the
// second run writes, less the same sum over the first's: what the binary's start and the body's
// building cost is never the reading's. The child is bounded only by the shared wait
// (testwait.Context): a run that never ends fails the row at the binary's deadline, naming the run.
//
// The one gap: work in code the counters do not instrument, the assembly of internal/bytealg's
// forward searches (strings.Index, IndexByte) and the runtime's copy of a string, which count as
// the one statement that makes the call. A walk back is a backward search, which internal/bytealg
// and strings write in Go, so the counters see it.
//
// A block's counter is 32 bits wide, so a block run 2^31 times or more in one run fails that run,
// naming the block (countedBlockLimit): its count can have wrapped, and the work summed from it is
// no count at all.

// countedPackages are the packages the counted binary counts (-coverpkg): this one, whose loops
// authorityBefore's are; regexp, regexp/syntax and unicode/utf8, which find every issue path and
// bare reference; net/url, which reads each match's host; and strings, bytes and internal/bytealg,
// whose backward searches a walk back to the nearest space is written with.
var countedPackages = []string{".", "strings", "bytes", "internal/bytealg", "unicode/utf8", "regexp", "regexp/syntax", "net/url"}

// The environment TestCountedCitedIssuesOfOneBody reads: the body to build, the size to build it
// at, and whether to read its citations ("1") or only build it ("0").
const (
	countedBodyEnv = "DELIVERY_COUNTED_BODY"
	countedSizeEnv = "DELIVERY_COUNTED_SIZE"
	countedOpEnv   = "DELIVERY_COUNTED_OP"
)

// countedMark begins the line a counted run writes to its output: the bytes its body holds.
const countedMark = "delivery counted run: "

// countedBlockLimit is the count at which a block's counter, 32 bits wide, is half way to wrapping.
const countedBlockLimit = 1 << 31

// The bodies the counted rows read, each built at a size by its builder in countedBodies.
const (
	countedPathsEveryThirteenBytes = "an issue path every thirteen bytes and no whitespace"
	countedCitationsBySpace        = "200 citations separated by spaces after a word of prose"
	countedCitationsByComma        = "200 citations separated by commas after a word of prose"
)

var countedBodies = map[string]func(size int) string{
	countedPathsEveryThirteenBytes: issuePathsEveryThirteenBytes,
	countedCitationsBySpace:        func(size int) string { return citationsAfterProse(" ", size) },
	countedCitationsByComma:        func(size int) string { return citationsAfterProse(",", size) },
}

// countedSink keeps the child's reading, so nothing of it is left unused.
var countedSink []string

// TestCountedCitedIssuesOfOneBody builds the one body countedBodyEnv names, at the size
// countedSizeEnv names, reads its citations when countedOpEnv is "1", and writes the bytes the body
// holds. It is the child countedChild starts in the counted test binary; with no body named it
// skips.
func TestCountedCitedIssuesOfOneBody(t *testing.T) {
	name := os.Getenv(countedBodyEnv)
	if name == "" {
		t.Skipf("%s is unset: the rows that count their work run this row in the counted test binary", countedBodyEnv)
	}
	build, ok := countedBodies[name]
	if !ok {
		t.Fatalf("%s = %q names no body of countedBodies", countedBodyEnv, name)
	}
	size, err := strconv.Atoi(os.Getenv(countedSizeEnv))
	if err != nil || size < 1 {
		t.Fatalf("%s = %q, want a size", countedSizeEnv, os.Getenv(countedSizeEnv))
	}
	body := build(size)
	if os.Getenv(countedOpEnv) == "1" {
		countedSink = citedIssues("acme/widgets", body)
	}
	fmt.Printf("%s%d\n", countedMark, len(body))
}

// counted is what one counted reading of a body read.
type counted struct {
	// bytes is the length of the body.
	bytes int
	// work is the statements the reading ran in the counted packages, and byPackage the same by
	// package.
	work      int64
	byPackage map[string]int64
	// busiest is the count of the block the reading's run ran most often, building included, and
	// busiestBlock that block, as the profile names it.
	busiest      int64
	busiestBlock string
}

// text is the reading's line in a failure or a log: its work, by package, largest first.
func (c counted) text() string {
	packages := slices.SortedFunc(maps.Keys(c.byPackage), func(a, b string) int {
		if c.byPackage[a] != c.byPackage[b] {
			if c.byPackage[a] > c.byPackage[b] {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	var parts []string
	for _, pkg := range packages {
		if c.byPackage[pkg] != 0 {
			parts = append(parts, fmt.Sprintf("%s %d", pkg, c.byPackage[pkg]))
		}
	}
	return fmt.Sprintf("%d bytes, %d counted statements (%s), busiest block %d", c.bytes, c.work, strings.Join(parts, ", "), c.busiest)
}

// countedCitedIssues counts citedIssues' reading of the body named at size: the run that builds the
// body and reads it, less the run that only builds it. It fails t when a block of either run was
// counted countedBlockLimit times or more.
func countedCitedIssues(t *testing.T, body string, size int) counted {
	t.Helper()
	built := countedChild(t, body, size, false)
	whole := countedChild(t, body, size, true)
	if built.bytes != whole.bytes {
		t.Fatalf("%s at %d: the build-only run holds %d bytes and the reading's run %d: the body must be built the same each time",
			body, size, built.bytes, whole.bytes)
	}
	for _, run := range []counted{built, whole} {
		if run.busiest >= countedBlockLimit {
			t.Fatalf("%s at %d: the block %s ran %d times, at or past 2^31: its counter is 32 bits wide, so its count can have wrapped, and the work summed from it is no count",
				body, size, run.busiestBlock, run.busiest)
		}
	}
	whole.work -= built.work
	for pkg, work := range built.byPackage {
		whole.byPackage[pkg] -= work
	}
	return whole
}

// countedBuild is the counted test binary, built once for the whole run (countedBinary) and removed
// by TestMain once every row has run (removeCountedBuild), and the go command that built it, which
// reads the counters each run of it writes (countedChild).
var countedBuild struct {
	once  sync.Once
	dir   string
	bin   string
	goCmd string
	err   string
}

// countedBinary is the path of this package's test binary built with Go's coverage counters in
// countedPackages, and the go command that built it. The first row to ask builds it; a build that
// failed fails every row that asks.
func countedBinary(t *testing.T) (bin, goCmd string) {
	t.Helper()
	countedBuild.once.Do(func() { buildCounted(t) })
	if countedBuild.err != "" {
		t.Fatal(countedBuild.err)
	}
	return countedBuild.bin, countedBuild.goCmd
}

// buildCounted builds the counted test binary into a directory of its own. The build passes
// -race=false: the go command reads GOFLAGS from the test's environment, and a developer's GOFLAGS
// holding -race would make it refuse the build, -covermode=count where cgo is on (`-covermode must
// be "atomic"`, since the race detector takes only atomic counters) and -race itself where it is off
// (`-race requires cgo`). The counts read here need no detector, and a flag on the command line
// overrides GOFLAGS's.
func buildCounted(t *testing.T) {
	goCmd, err := exec.LookPath("go")
	if err != nil {
		countedBuild.err = "go is not on PATH. The rows that count their work build this package's test binary with " +
			"Go's coverage counters, so they need the go command: go test puts its own GOROOT/bin first on a " +
			"test's PATH, and a test binary run by hand needs Go's bin directory on PATH. They fail rather than skip."
		return
	}
	dir, err := os.MkdirTemp("", "delivery-counted-")
	if err != nil {
		countedBuild.err = err.Error()
		return
	}
	countedBuild.dir = dir
	bin := filepath.Join(dir, "delivery.test")
	if _, err := countedCommand(t, "the build of the counted test binary", nil, goCmd, "test", "-c", "-race=false", "-cover",
		"-covermode=count", "-coverpkg="+strings.Join(countedPackages, ","), "-o", bin, "."); err != nil {
		countedBuild.err = err.Error()
		return
	}
	countedBuild.bin, countedBuild.goCmd = bin, goCmd
}

// removeCountedBuild removes the counted test binary's directory, once every row has run.
func removeCountedBuild() {
	if countedBuild.dir == "" {
		return
	}
	if err := os.RemoveAll(countedBuild.dir); err != nil {
		fmt.Fprintf(os.Stderr, "remove the counted test binary's directory: %v\n", err)
	}
}

// countedCommand runs name with args, and env added to the test's own, its output to one file read
// once it exits, and returns that output. A run that does not exit before the shared wait's end
// (testwait.Context), or that exits with an error, returns an error carrying its output.
func countedCommand(t *testing.T, what string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	out, err := os.Create(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		return "", err
	}
	defer out.Close()
	ctx := testwait.Context(t)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = out, out
	runErr := cmd.Run()
	text, err := os.ReadFile(out.Name())
	if err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("testwait: %s: gave up waiting for %s at the shared wait's margin before the test "+
			"binary's deadline; its output:\n%s", t.Name(), what, text)
	}
	if runErr != nil {
		return "", fmt.Errorf("%s: %v; its output:\n%s", what, runErr, text)
	}
	return string(text), nil
}

// countedChild builds the body named at size in the counted test binary, reading its citations or
// not, and reads the statements the run counted and the bytes it says the body holds. The counts
// are the ones the run writes to its coverage directory, read by go tool covdata textfmt; that
// directory is one under the counted binary's own (countedBuild.dir), whose name holds no comma,
// since covdata reads -i as a comma-separated list of directories.
func countedChild(t *testing.T, body string, size int, read bool) counted {
	t.Helper()
	bin, goCmd := countedBinary(t)
	coverDir, err := os.MkdirTemp(countedBuild.dir, "cover-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(coverDir); err != nil {
			t.Errorf("remove a counted run's coverage directory: %v", err)
		}
	})
	what := fmt.Sprintf("the counted run of %s at %d", body, size)
	op := "1"
	if !read {
		op = "0"
		what += ", building it alone"
	}
	out, err := countedCommand(t, what, []string{countedBodyEnv + "=" + body, countedSizeEnv + "=" + strconv.Itoa(size),
		countedOpEnv + "=" + op}, bin, "-test.run=^TestCountedCitedIssuesOfOneBody$", "-test.gocoverdir="+coverDir)
	if err != nil {
		t.Fatal(err)
	}
	var run counted
	found := false
	for _, line := range strings.Split(out, "\n") {
		if value, ok := strings.CutPrefix(line, countedMark); ok {
			if run.bytes, err = strconv.Atoi(value); err != nil {
				t.Fatalf("%s wrote %q, not the bytes its body holds: %v", what, line, err)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("%s wrote no %q line; its output:\n%s", what, countedMark, out)
	}
	profile := filepath.Join(t.TempDir(), "profile.txt")
	if _, err := countedCommand(t, "go tool covdata textfmt of "+what, nil, goCmd, "tool", "covdata", "textfmt",
		"-i="+coverDir, "-o="+profile); err != nil {
		t.Fatal(err)
	}
	run.work, run.byPackage, run.busiest, run.busiestBlock = countedWork(t, profile)
	return run
}

// countedWork is the work a coverage profile in Go's text format counts: the statements of each
// block times the times it ran, summed, in all and by the block's package, and the count of the
// block that ran most often, with that block.
func countedWork(t *testing.T, profile string) (work int64, byPackage map[string]int64, busiest int64, busiestBlock string) {
	t.Helper()
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if lines[0] != "mode: count" {
		t.Fatalf("the profile %s begins %q, not mode: count", profile, lines[0])
	}
	byPackage = map[string]int64{}
	for _, line := range lines[1:] {
		block, counts, _ := strings.Cut(line, " ")
		file, _, _ := strings.Cut(block, ":")
		f := strings.Fields(counts)
		if len(f) != 2 {
			t.Fatalf("the profile line %q is not a block, its statements and its count", line)
		}
		stmts, err1 := strconv.ParseInt(f[0], 10, 64)
		count, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("the profile line %q is not a block, its statements and its count", line)
		}
		work += stmts * count
		byPackage[path.Dir(file)] += stmts * count
		if count > busiest {
			busiest, busiestBlock = count, block
		}
	}
	return work, byPackage, busiest, busiestBlock
}
