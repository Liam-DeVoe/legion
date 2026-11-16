package specgraph

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// maxOutput is how much of a test's output an entry keeps: its last 64 KiB.
const maxOutput = 64 << 10

// timedOut is the line go test writes when it kills a test binary at its -timeout.
const timedOut = "panic: test timed out after"

// TestOutcome is one run of one top-level Go test, as a go test -json report gives it: its
// package, its name, its outcome and the last 64 KiB of its output, its subtests' output included.
// Outcome is "pass", "fail", "skip" or "could_not_run": a test that started and has no end, or that
// go test killed at its time limit, could not run, and it never reads "fail", since the
// infrastructure and not the code ended it.
type TestOutcome struct {
	Package, Test, Outcome, Output string
}

// event is the part of one go test -json event that the report reads (go doc test2json).
type event struct {
	Action, Package, Test, Output string
}

// run is one entry while the report is read: what its test wrote so far, and whether go test
// killed it.
type run struct {
	outcome  TestOutcome
	output   []byte
	timedOut bool
}

// ReadGoTestJSON reads the report that go test -json writes and gives one entry for each run of a
// top-level test, in the order the tests started. A name that runs twice in one package, as a test
// defined in both package p and package p_test does, gives two entries; the events of that name
// after its second start belong to the second. The outcome is the test's own last pass, fail or
// skip event, never a subtest's. A report that holds anything but events is refused, so that an
// unreadable report never reads as a run of no tests.
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
			this.timedOut = this.timedOut || strings.Contains(e.Output, timedOut)
			this.output = append(this.output, e.Output...)
			if len(this.output) > 2*maxOutput {
				this.output = append(this.output[:0], this.output[len(this.output)-maxOutput:]...)
			}
		case "pass", "fail", "skip":
			if !sub {
				this.outcome.Outcome = e.Action
			}
		}
	}
	outcomes := make([]TestOutcome, 0, len(runs))
	for _, this := range runs {
		if this.timedOut || this.outcome.Outcome == "" {
			this.outcome.Outcome = "could_not_run"
		}
		this.outcome.Output = lastBytes(this.output, maxOutput)
		outcomes = append(outcomes, this.outcome)
	}
	return outcomes, nil
}

// lastBytes is the last n bytes of b at most, starting at the start of a UTF-8 character.
func lastBytes(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
		for len(b) > 0 && !utf8.RuneStart(b[0]) {
			b = b[1:]
		}
	}
	return string(b)
}
