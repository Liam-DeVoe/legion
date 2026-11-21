package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

// testDeadlineEnv names, to this package's test binary run as `legion` (TestMain), the deadline of
// the test binary that started it, in RFC 3339 with nanoseconds. Only this test binary reads it;
// the legion binary has no such variable.
const testDeadlineEnv = "LEGION_CMD_TEST_DEADLINE"

// testBoundMargin is how long before the test binary's deadline each of testBounds runs out: the
// time a row whose program never answered has to fail by name, and its cleanups to run, before go
// test's alarm panics the binary. It is testwait.Receive's.
const testBoundMargin = 30 * time.Second

// testBounds are the bounds `legion` sets on a program it starts and waits for, in the commands
// these tests run with stand-ins: the git reads of `legion gh` (ghGitReadBound), its read of gh's
// help (ghHelpTimeout), its answer to the shared check's own visibility read
// (visibilityAnswerBound), and the credential helper's fork read (forkReadBound). Each fits the real
// program, and none is what a test here reads: each is all the time a started program has to start
// and answer, and a loaded machine makes starts slow. At a box load of about 1,100 a git behind a
// one-line script ran out ghGitReadBound's 30 s in TestGhRepoGitConfigRefusalWithNoGitOrAnOldGit,
// and at a load average of 744 a run of this binary as `legion gh` reached the stand-in gh after
// visibilityAnswerBound's 15 s in TestLegionGhReadsOnlyWhatTheSharedCheckReadsAsARead. So in this
// binary each read takes what is left of the binary's deadline less testBoundMargin when the read
// starts (boundByTheDeadline), in its own process and in each run of it as `legion` (TestMain,
// testDeadlineEnviron): a program that never answers still fails its row by name before the
// binary's deadline, however late in the run its read starts. A test that reads a bound's own stop
// sets that bound itself, and a bound a test has set keeps that test's value.
var testBounds = []*time.Duration{&ghGitReadBound, &ghHelpTimeout, &visibilityAnswerBound, &forkReadBound}

// testBinaryDeadline is the deadline go test's -timeout gives this binary, reckoned from now, and
// false under -timeout 0, which gives none. TestMain calls it before m.Run, which starts its own
// alarm a moment later, so this deadline is never the later of the two.
func testBinaryDeadline() (time.Time, bool) {
	if !flag.Parsed() {
		flag.Parse()
	}
	timeout := flag.Lookup("test.timeout").Value.(flag.Getter).Get().(time.Duration)
	if timeout <= 0 {
		return time.Time{}, false
	}
	return time.Now().Add(timeout), true
}

// boundByTheDeadline makes programBound answer, for each of testBounds that still holds the value
// it held when this was called, what is left of deadline less testBoundMargin at the moment the
// read asks; a bound a test has set answers its own value. It hands deadline to each run of this
// binary as `legion` whose environment is this process's own (testDeadlineEnv).
func boundByTheDeadline(deadline time.Time) error {
	product := make(map[*time.Duration]time.Duration, len(testBounds))
	for _, bound := range testBounds {
		product[bound] = *bound
	}
	programBound = func(bound *time.Duration) time.Duration {
		if held, ok := product[bound]; !ok || *bound != held {
			return *bound
		}
		return time.Until(deadline) - testBoundMargin
	}
	return os.Setenv(testDeadlineEnv, deadline.Format(time.RFC3339Nano))
}

// boundByTheEnvironment is boundByTheDeadline for a run of this binary as `legion`, by the deadline
// testDeadlineEnv names; a run that names none keeps the product's bounds.
func boundByTheEnvironment() error {
	named, ok := os.LookupEnv(testDeadlineEnv)
	if !ok {
		return nil
	}
	deadline, err := time.Parse(time.RFC3339Nano, named)
	if err != nil {
		return fmt.Errorf("%s=%q is no RFC 3339 time: %w", testDeadlineEnv, named, err)
	}
	return boundByTheDeadline(deadline)
}

// testDeadlineEnviron is the variable that hands a run of this binary as `legion` the deadline of
// the binary that started it, for an environment a test builds itself rather than from os.Environ,
// which already holds it; none under -timeout 0.
func testDeadlineEnviron() []string {
	if deadline, ok := os.LookupEnv(testDeadlineEnv); ok {
		return []string{testDeadlineEnv + "=" + deadline}
	}
	return nil
}
