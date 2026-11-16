package specgraph

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// An entry keeps an output of maxOutput bytes whole. Of a longer output it keeps the first
// outputStart bytes, which name the test, and the last outputEnd, which say how it ended.
const (
	maxOutput   = 64 << 10
	outputStart = 4 << 10
	outputEnd   = maxOutput - outputStart
)

// TestOutcome is one run of one top-level Go test, as a go test -json report gives it: its
// package, its name, its outcome and its output.
//
// Outcome is the run's one end event of its own, "pass", "fail" or "skip", or "could_not_run" for
// a run with no end event of its own or with more than one. A run has none when go test killed
// its test at its -timeout, or when the test's process ended inside it (os.Exit with a status
// that is not 0, a fatal error of the runtime). It has two when a test printed a result line with
// the marker of the testing package's own lines before it, which the reader cannot tell from the
// real end, and the second of two parallel tests of one name, of package p and of p_test, gets
// the ends of both. What a test prints otherwise decides no outcome, go test's kill line included.
//
// Output is what the test wrote, its subtests' output and the testing package's own lines
// included, whole when it is 64 KiB or less. Of a longer output it holds the first 4 KiB, then the
// line "[The middle of the output is cut here: <n> of its <size> bytes.]", then the last 60 KiB;
// each part starts and ends at a whole UTF-8 character, so a part can hold up to 3 bytes less.
type TestOutcome struct {
	Package, Test, Outcome, Output string
}

// event is the part of one go test -json event that the report reads (go doc test2json).
type event struct {
	Action, Package, Test, Output string
}

// run is one entry while the report is read: its end events of its own so far and what its test
// wrote.
type run struct {
	outcome TestOutcome
	ends    int
	size    int    // how many bytes of output the test wrote
	start   []byte // the first outputStart bytes of them
	end     []byte // the bytes after start, the last outputEnd of them at least
}

// write adds out to what the run's test wrote.
func (r *run) write(out string) {
	r.size += len(out)
	n := min(outputStart-len(r.start), len(out))
	r.start = append(r.start, out[:n]...)
	r.end = append(r.end, out[n:]...)
	if len(r.end) > 2*outputEnd {
		r.end = append(r.end[:0], r.end[len(r.end)-outputEnd:]...)
	}
}

// output is what TestOutcome.Output keeps of what the run's test wrote. Each event's Output is
// valid UTF-8, since encoding/json decodes a string so, and so is what the test wrote: the first
// part ends before a character that its last bytes cut, and the last part starts after one.
func (r *run) output() string {
	if r.size <= maxOutput {
		return string(r.start) + string(r.end)
	}
	start := r.start
	last := len(start) - 1
	for last > 0 && !utf8.RuneStart(start[last]) {
		last--
	}
	if !utf8.FullRune(start[last:]) {
		start = start[:last]
	}
	end := r.end[len(r.end)-outputEnd:]
	for len(end) > 0 && !utf8.RuneStart(end[0]) {
		end = end[1:]
	}
	return fmt.Sprintf("%s\n[The middle of the output is cut here: %d of its %d bytes.]\n%s", start, r.size-len(start)-len(end), r.size, end)
}

// ReadGoTestJSON reads the report that go test -json writes and gives one entry for each run of a
// top-level test, in the order the tests started. A name that runs twice in one package, as under
// -count=2 or as a test defined in both package p and package p_test, gives two entries; the events
// of that name after its second start belong to the second. The outcome is the run's one end event
// of its own, never a subtest's: a run with none or with two could not run (TestOutcome).
//
// The report is the test binary's own account. A line that a test prints with no marker is
// output, whatever it says. But the reader cannot tell a line that the binary prints with the
// marker, or before its first test, from the testing package's own, so the binary can still
// print a whole run, of a test that does not exist or of one that does, or an end for a run that
// then never ends.
//
// A value that is no JSON object, a null and an object with no Action are refused. Any other
// report that names no test, an empty one included, reads as a run of no tests: each check of it
// then reads as not run, which is unknown and never a pass. Events of a package and not of a test
// (no Test), events of a test that never started, and the actions that end no test (start, pause,
// cont, attr and the rest) are passed over.
func ReadGoTestJSON(r io.Reader) ([]TestOutcome, error) {
	var runs []*run
	current := map[[2]string]*run{} // package and top-level name -> its latest run
	decoder := json.NewDecoder(r)
	for {
		var e event
		if err := decoder.Decode(&e); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("specgraph: the go test report holds no event at byte %d: %w", decoder.InputOffset(), err)
		}
		if e.Action == "" {
			return nil, fmt.Errorf("specgraph: the go test report holds no event at byte %d: a value with no Action", decoder.InputOffset())
		}
		if e.Test == "" {
			continue
		}
		top, _, sub := strings.Cut(e.Test, "/")
		key := [2]string{e.Package, top}
		if e.Action == "run" && !sub {
			current[key] = &run{outcome: TestOutcome{Package: e.Package, Test: top}}
			runs = append(runs, current[key])
			continue
		}
		this := current[key]
		if this == nil {
			continue
		}
		switch e.Action {
		case "output":
			this.write(e.Output)
		case "pass", "fail", "skip":
			if !sub {
				this.ends++
				this.outcome.Outcome = e.Action
			}
		}
	}
	outcomes := make([]TestOutcome, 0, len(runs))
	for _, this := range runs {
		if this.ends != 1 {
			this.outcome.Outcome = "could_not_run"
		}
		this.outcome.Output = this.output()
		outcomes = append(outcomes, this.outcome)
	}
	return outcomes, nil
}
