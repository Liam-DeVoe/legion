package testwait

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// childEnv tells the re-executed test binary to run the helpers below, which skip in an ordinary
// run. One gives up on purpose under a short -timeout and the other reads the deadline under
// -timeout 0, and the parent names each flag, so each runs in a binary of its own.
const childEnv = "TESTWAIT_CHILD"

// childTimeout is the -timeout the parent gives the child that gives up: its waits give up 5 s
// into the run, 30 s before its deadline.
const childTimeout = "35s"

// receiveChildTimeout is the -timeout TestReceiveGivesUpAtTheMargin gives its child: its first
// Receive gives up 15 s into the run, and 30 s are left for the Receives after it.
const receiveChildTimeout = "45s"

// A wait whose condition holds returns from its first question: it asks once and never sleeps
// at the pace first.
func TestAWaitThatHoldsReturnsAtOnce(t *testing.T) {
	asks := 0
	Eventually(t, "a condition that holds", func() bool { asks++; return true })
	if asks != 1 {
		t.Fatalf("Eventually asked a condition that holds %d times, want 1", asks)
	}
	asks = 0
	if got := Until(t, "a reading that holds", func() (string, bool) { asks++; return "the reading", true }); got != "the reading" {
		t.Fatalf("Until returned %q, want the reading that held", got)
	}
	if asks != 1 {
		t.Fatalf("Until asked a reading that holds %d times, want 1", asks)
	}
	sent := make(chan int, 1)
	sent <- 7
	if got := Receive(t, "a value already sent", sent); got != 7 {
		t.Fatalf("Receive returned %d, want the 7 sent", got)
	}
	var stream Stream[string]
	stream.Add("already there")
	if got := Next(t, "an item already added", &stream, func(string) bool { return true }); got != "already there" {
		t.Fatalf("Next returned %q, want the item already added", got)
	}
}

// Until returns the reading that held, not one it read before it.
func TestUntilReturnsTheReadingThatHeld(t *testing.T) {
	asks := 0
	got := Until(t, "the third reading", func() (int, bool) { asks++; return asks, asks == 3 })
	if got != 3 {
		t.Fatalf("Until returned %d, want the third reading, the one that held", got)
	}
}

// Next reads a stream in order: it returns the first unread item that matches, and the items
// before it are passed over, so no later Next reads them.
func TestNextAdvancesItsCursor(t *testing.T) {
	var stream Stream[string]
	for _, item := range []string{"a", "b", "c"} {
		stream.Add(item)
	}
	if got := Next(t, "b", &stream, func(item string) bool { return item == "b" }); got != "b" {
		t.Fatalf("Next for b returned %q", got)
	}
	// a came before b, so the Next for b passed it over: a match for a or c finds c.
	if got := Next(t, "a or c", &stream, func(item string) bool { return item == "a" || item == "c" }); got != "c" {
		t.Fatalf("Next for a or c returned %q, want c: a was passed over by the Next for b", got)
	}
	// Every item is read: the next Next waits for an Add, which a producer makes.
	go stream.Add("d")
	if got := Next(t, "an item added after the wait began", &stream, func(string) bool { return true }); got != "d" {
		t.Fatalf("Next for a later item returned %q, want d", got)
	}
}

// Producers on goroutines of their own add while the test goroutine reads: every item arrives
// once, and the race detector sees the stream's lock rather than a race.
func TestNextReadsEveryItemThatConcurrentProducersAdd(t *testing.T) {
	const producers, each = 4, 100
	var stream Stream[string]
	var wrote sync.WaitGroup
	for producer := range producers {
		wrote.Go(func() {
			for item := range each {
				stream.Add(fmt.Sprintf("%d/%d", producer, item))
			}
		})
	}
	read := make([]string, 0, producers*each)
	for range producers * each {
		read = append(read, Next(t, "the next item a producer adds", &stream, func(string) bool { return true }))
	}
	wrote.Wait()
	slices.Sort(read)
	if unique := slices.Compact(slices.Clone(read)); len(unique) != producers*each {
		t.Fatalf("Next read %d distinct items of %d added", len(unique), producers*each)
	}
}

// Receive returns a value already sent and one a sender sends later, and returns on a close with
// the zero value.
func TestReceiveReturnsASentValueAndAClose(t *testing.T) {
	ready := make(chan int, 1)
	ready <- 7
	if got := Receive(t, "a value already sent", ready); got != 7 {
		t.Fatalf("Receive returned %d, want the 7 sent", got)
	}
	sent := make(chan string)
	go func() { sent <- "sent later" }()
	if got := Receive(t, "a value a sender sends later", sent); got != "sent later" {
		t.Fatalf("Receive returned %q, want the value sent", got)
	}
	closed := make(chan string)
	close(closed)
	if got := Receive(t, "the close of a channel", closed); got != "" {
		t.Fatalf("Receive on a closed channel returned %q, want the zero value", got)
	}
}

// ReceiveInHelper returns a value a sender sends later with no error, and returns on a close with
// the zero value and no error.
func TestReceiveInHelperReturnsASentValueAndAClose(t *testing.T) {
	sent := make(chan string)
	go func() { sent <- "sent later" }()
	if got, err := ReceiveInHelper("a helper", "a value a sender sends later", sent); got != "sent later" || err != nil {
		t.Fatalf("ReceiveInHelper returned %q and %v, want the value sent and no error", got, err)
	}
	closed := make(chan string)
	close(closed)
	if got, err := ReceiveInHelper("a helper", "the close of a channel", closed); got != "" || err != nil {
		t.Fatalf("ReceiveInHelper on a closed channel returned %q and %v, want the zero value and no error", got, err)
	}
}

// Returns waits for its function, run on a goroutine of its own, to return, and what the function
// wrote is the caller's to read once it has. Under a short -timeout a function that never returns
// fails the test naming what, 30 s before the deadline and before Go's alarm, with the same line on
// stderr (TestTheWaitsGiveUpChild's "returns").
func TestReturnsWaitsForItsFunctionAndFailsNamingOneThatNeverReturns(t *testing.T) {
	release := make(chan struct{})
	go close(release)
	wrote := ""
	Returns(t, "a function let go by another goroutine", func() {
		<-release
		wrote = "written"
	})
	if wrote != "written" {
		t.Fatalf("Returns returned before its function did: the function wrote %q, want %q", wrote, "written")
	}

	// The child is waited for through Returns itself, and runs on Context, which ends it where that
	// wait gives up, so this row cannot outlast its own binary's deadline.
	child := exec.CommandContext(Context(t), os.Args[0], "-test.run=^TestTheWaitsGiveUpChild$/^returns$", "-test.timeout="+childTimeout, "-test.v")
	child.Env = append(os.Environ(), childEnv+"=1")
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	var ran error
	Returns(t, "the child under -timeout "+childTimeout+" to exit", func() { ran = child.Run() })
	if ran == nil {
		t.Fatalf("the child was meant to fail; it passed:\n%s\nstderr:\n%s", &stdout, &stderr)
	}
	if strings.Contains(stdout.String(), "panic: test timed out") || strings.Contains(stderr.String(), "panic: test timed out") {
		t.Fatalf("the child's Returns ran into the binary's deadline rather than giving up before it:\n%s\nstderr:\n%s", &stdout, &stderr)
	}
	line := "testwait: TestTheWaitsGiveUpChild/returns: gave up waiting for a function that never returns 30s before the test binary's deadline"
	if !slices.Contains(strings.Split(stderr.String(), "\n"), line) {
		t.Errorf("the child wrote no stderr line %q; its stderr:\n%s", line, &stderr)
	}
	if !strings.Contains(stdout.String(), line+"\n") {
		t.Errorf("the child failed no test with %q; its output:\n%s", line, &stdout)
	}
}

// Context's deadline is the moment a wait gives up, and the context ends with the test that took it.
func TestContextEndsWhereAWaitGivesUpAndWithTheTest(t *testing.T) {
	var taken context.Context
	t.Run("taker", func(t *testing.T) {
		taken = Context(t)
		end, bounded := GiveUpAt(t)
		deadline, has := taken.Deadline()
		if has != bounded || !deadline.Equal(end) {
			t.Fatalf("Context's deadline is %v (%t), want the moment a wait gives up, %v (%t)", deadline, has, end, bounded)
		}
		if err := taken.Err(); err != nil {
			t.Fatalf("Context ended before its test did: %v", err)
		}
	})
	if err := taken.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Context outlived the test that took it: its error is %v, want context.Canceled", err)
	}
}

// A wait gives up the margin before the test binary's deadline, at the moment GiveUpAt names. A
// *testing.T names that deadline; a testing.TB without Deadline, and TestMain, read it from the
// -timeout flag and the process's start, which is no later than testing's own.
func TestAWaitGivesUpTheMarginBeforeTheDeadline(t *testing.T) {
	deadline, bounded := t.Deadline()
	end, ok := GiveUpAt(t)
	if ok != bounded || ok && !end.Equal(deadline.Add(-margin)) {
		t.Fatalf("a wait in a test whose deadline is %v (%t) gives up at %v (%t), want %s before it", deadline, bounded, end, ok, margin)
	}
	fromFlag, ok := GiveUpAt(noDeadline{t})
	if ok != bounded || ok && fromFlag.After(end) {
		t.Fatalf("a wait in a testing.TB without Deadline gives up at %v (%t), want no later than %v (%t)", fromFlag, ok, end, bounded)
	}
	if inMain, ok := mainGiveUpAt(); ok != bounded || !inMain.Equal(fromFlag) {
		t.Fatalf("a wait in TestMain gives up at %v (%t), want where a testing.TB without Deadline does, %v (%t)", inMain, ok, fromFlag, bounded)
	}
}

// InMain returns nil from its first question when its condition holds, and once it holds when it
// comes to.
func TestInMainReturnsNilOnceItsConditionHolds(t *testing.T) {
	asks := 0
	if err := InMain("a condition that holds", func() bool { asks++; return true }); err != nil || asks != 1 {
		t.Fatalf("InMain over a condition that holds returned %v after %d questions, want nil after 1", err, asks)
	}
	asks = 0
	if err := InMain("a condition that holds at its third question", func() bool { asks++; return asks == 3 }); err != nil || asks != 3 {
		t.Fatalf("InMain over a condition that holds at its third question returned %v after %d questions, want nil after 3", err, asks)
	}
}

// Span and Quiet hold the test for their span when given a reason, and Quiet passes when nothing
// comes.
func TestSpanAndQuietHoldTheirSpan(t *testing.T) {
	Span(t, time.Millisecond, "the test of Span itself: its span is what it proves")
	Quiet(t, "a channel no one sends on", make(chan int), time.Millisecond, "the test of Quiet itself: its span is what it proves")
}

// Span and Quiet refuse a span with no reason: one is a sleep.
func TestSpanAndQuietRefuseAnEmptyWhy(t *testing.T) {
	for _, why := range []string{"", " \t\n"} {
		failure := failureOf(t, func(tb testing.TB) { Span(tb, time.Hour, why) })
		if want := "testwait: " + t.Name() + ": Span needs a why"; !strings.HasPrefix(failure, want) {
			t.Fatalf("Span with the why %q failed with %q, want a failure that begins %q", why, failure, want)
		}
		failure = failureOf(t, func(tb testing.TB) { Quiet(tb, "a channel", make(chan int), time.Hour, why) })
		if want := "testwait: " + t.Name() + ": Quiet needs a why"; !strings.HasPrefix(failure, want) {
			t.Fatalf("Quiet with the why %q failed with %q, want a failure that begins %q", why, failure, want)
		}
	}
}

// Quiet fails naming what yielded and the value, and a close counts as yielding.
func TestQuietFailsWhenItsChannelYields(t *testing.T) {
	sent := make(chan string, 1)
	sent <- "an early stop"
	failure := failureOf(t, func(tb testing.TB) { Quiet(tb, "the stop", sent, time.Hour, "the stop is reported nowhere else") })
	if want := "the stop yielded an early stop within 1h0m0s"; !strings.Contains(failure, want) {
		t.Fatalf("Quiet over a channel holding a value failed with %q, want it to say %q", failure, want)
	}
	closed := make(chan string)
	close(closed)
	failure = failureOf(t, func(tb testing.TB) { Quiet(tb, "the stop", closed, time.Hour, "the stop is reported nowhere else") })
	if want := "the stop was closed within 1h0m0s"; !strings.Contains(failure, want) {
		t.Fatalf("Quiet over a closed channel failed with %q, want it to say %q", failure, want)
	}
}

// Under a short -timeout every wait gives up 30 s before the deadline, before Go's alarm: it writes
// its line to stderr and fails the test with the same text, which carries the wait's last reading
// where it has one. A wait that starts once the margin has passed, a wait in a cleanup included,
// asks once and fails. The until wait's readings each carry their count and the child logs how
// many it read, so the parent wants the last read to name that count: the reading it read last,
// whatever pace the child's asks kept on a loaded machine.
func TestAWaitGivesUpWithItsLineOnStderrAndTheSameFailure(t *testing.T) {
	stdout, stderr := runChildMeantToFail(t, "TestTheWaitsGiveUpChild", childTimeout)
	asked := regexp.MustCompile(`the reading was asked (\d+) time\(s\)`).FindStringSubmatch(stdout)
	if asked == nil {
		t.Fatalf("the child logged no count of the until wait's readings; its output:\n%s", stdout)
	}
	name := "TestTheWaitsGiveUpChild/"
	ending := " 30s before the test binary's deadline"
	requireGaveUp(t, stdout, stderr, []string{
		"testwait: " + name + "until: gave up waiting for a reading that never holds" + ending + "; last read: reading " + asked[1],
		"testwait: " + name + "eventually: gave up waiting for a condition that never holds" + ending,
		"testwait: " + name + "next_on_an_empty_stream: gave up waiting for an item no one adds" + ending + "; last read: none",
		"testwait: " + name + "next_past_unmatched_items: gave up waiting for an item that matches" + ending + `; last read: the latest\nitem`,
		"testwait: " + name + "receive: gave up waiting for a send no one makes" + ending,
		"testwait: " + name + "returns: gave up waiting for a function that never returns" + ending,
		"testwait: " + name + "cleanup: gave up waiting for a condition a cleanup waits for" + ending,
		"testwait: " + name + "span: gave up waiting for the end of a 1h0m0s span (a span past the margin)" + ending,
		"testwait: TestMain: gave up waiting for a condition TestMain waits for" + ending,
		"testwait: a helper: gave up waiting for a send no one makes to a helper" + ending,
	})
	if !strings.Contains(stdout, "the condition was asked 1 time(s)") {
		t.Errorf("a wait that began after the margin did not ask exactly once; the child's output:\n%s", stdout)
	}
}

// Under -timeout 0 there is no deadline: a wait has no moment to give up at and no timer, in a
// test or in TestMain, and Context has no deadline. So a wait that is not answered at once keeps
// asking rather than giving up at a false reading or an empty channel: Until returns the third
// reading after three asks, Eventually and InMain hold at their third question, and Receive takes
// a value sent an hour after it began to wait, on a synctest bubble's clock, so no span of its own
// shorter than that passes it. The child has no deadline, so a wait of its that never returns, or a
// Receive that never blocks and so holds the bubble's clock still, would hold it for good: the
// child runs under Context, which ends it at this test's give-up, and the failure then shows the
// child's output up to the wait it hung in, rather than go test's alarm with none of it.
func TestUnderNoTimeoutAWaitHasNoEnd(t *testing.T) {
	child := exec.CommandContext(Context(t), os.Args[0], "-test.run=^TestNoDeadlineChild$", "-test.timeout=0", "-test.v")
	child.Env = append(os.Environ(), childEnv+"=1")
	output, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--- PASS: TestNoDeadlineChild (") {
		t.Fatalf("under -timeout 0 the child did not pass: %v\n%s", err, output)
	}
}

// TestTheWaitsGiveUpChild is the child of TestAWaitGivesUpWithItsLineOnStderrAndTheSameFailure,
// run under -timeout 35s: its first wait gives up 5 s in, and every later one starts past the
// margin. It runs only when the parent re-executes this binary.
func TestTheWaitsGiveUpChild(t *testing.T) {
	if os.Getenv(childEnv) == "" {
		t.Skip(childEnv + " is not set: this test is the child of TestAWaitGivesUpWithItsLineOnStderrAndTheSameFailure")
	}
	t.Run("until", func(t *testing.T) {
		asks := 0
		t.Cleanup(func() { t.Logf("the reading was asked %d time(s)", asks) })
		Until(t, "a reading that never holds", func() (string, bool) {
			asks++
			return fmt.Sprintf("reading %d", asks), false
		})
	})
	t.Run("eventually", func(t *testing.T) {
		asks := 0
		t.Cleanup(func() { t.Logf("the condition was asked %d time(s)", asks) })
		Eventually(t, "a condition that never holds", func() bool { asks++; return false })
	})
	t.Run("next on an empty stream", func(t *testing.T) {
		var stream Stream[string]
		Next(t, "an item no one adds", &stream, func(string) bool { return true })
	})
	t.Run("next past unmatched items", func(t *testing.T) {
		var stream Stream[string]
		stream.Add("an earlier item")
		stream.Add("the latest\nitem")
		Next(t, "an item that matches", &stream, func(string) bool { return false })
	})
	t.Run("receive", func(t *testing.T) {
		Receive(t, "a send no one makes", make(chan int))
	})
	t.Run("returns", func(t *testing.T) {
		never := make(chan struct{})
		Returns(t, "a function that never returns", func() { <-never })
	})
	t.Run("cleanup", func(t *testing.T) {
		t.Cleanup(func() { Eventually(t, "a condition a cleanup waits for", func() bool { return false }) })
	})
	t.Run("span", func(t *testing.T) {
		Span(t, time.Hour, "a span past the margin")
	})
	t.Run("in main", func(t *testing.T) {
		if err := InMain("a condition TestMain waits for", func() bool { return false }); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("receive in helper", func(t *testing.T) {
		if _, err := ReceiveInHelper("a helper", "a send no one makes to a helper", make(chan int)); err != nil {
			t.Fatal(err)
		}
	})
}

// Receive gives up when the margin passes and not before, and a Receive that starts past the
// margin takes only a value already sent and otherwise fails at once. The child runs under
// -timeout 45s, so the margin passes 15 s in: a Receive that waited a fixed span instead, of any
// length, fails one of the two reads, and no load can make the right Receive fail either. A span
// under 15 s gives up before the margin, which the child reads from the clock against GiveUpAt; a
// span of 15 s or more, taken again by each of the three Receives after it, runs past the 45 s
// deadline, which panics the child. Load only lengthens a step, and neither read is a span.
func TestReceiveGivesUpAtTheMargin(t *testing.T) {
	stdout, stderr := runChildMeantToFail(t, "TestReceiveGivesUpAtTheMarginChild", receiveChildTimeout)
	if strings.Contains(stdout, "before the margin, at ") {
		t.Errorf("the child's first Receive gave up before the margin; its output:\n%s", stdout)
	}
	name := "TestReceiveGivesUpAtTheMarginChild/"
	ending := " 30s before the test binary's deadline"
	want := []string{"testwait: " + name + "before_the_margin: gave up waiting for a send no one makes" + ending}
	for i := 1; i <= 3; i++ {
		want = append(want, fmt.Sprintf("testwait: %sno_value_past_the_margin_%d: gave up waiting for a later send no one makes%s", name, i, ending))
	}
	requireGaveUp(t, stdout, stderr, want)
	if !strings.Contains(stdout, "--- PASS: "+name+"a_value_past_the_margin (") {
		t.Errorf("a Receive past the margin did not take the value already sent; the child's output:\n%s", stdout)
	}
}

// runChildMeantToFail re-executes this binary to run only the test named child, under -timeout
// timeout and with childEnv set, and returns what the child wrote to stdout and to stderr. It
// fails t unless the child failed before its deadline: a child that passed, and one whose waits
// ran into go test's alarm rather than giving up at the margin, fail t at once.
func runChildMeantToFail(t *testing.T, child, timeout string) (stdout, stderr string) {
	t.Helper()
	run := exec.Command(os.Args[0], "-test.run=^"+child+"$", "-test.timeout="+timeout, "-test.v")
	run.Env = append(os.Environ(), childEnv+"=1")
	var out, errOut bytes.Buffer
	run.Stdout, run.Stderr = &out, &errOut
	if err := run.Run(); err == nil {
		t.Fatalf("the child was meant to fail; it passed:\n%s\nstderr:\n%s", &out, &errOut)
	}
	if strings.Contains(out.String(), "panic: test timed out") || strings.Contains(errOut.String(), "panic: test timed out") {
		t.Fatalf("the child's waits ran into the binary's deadline rather than giving up at the margin:\n%s\nstderr:\n%s", &out, &errOut)
	}
	return out.String(), errOut.String()
}

// requireGaveUp fails t for each give-up line of want that the child did not write to stderr as a
// line of its own, and for each it did not fail a test with on stdout, where testing prints the
// failure's text and a newline.
func requireGaveUp(t *testing.T, stdout, stderr string, want []string) {
	t.Helper()
	written := strings.Split(stderr, "\n")
	for _, line := range want {
		if !slices.Contains(written, line) {
			t.Errorf("the child wrote no stderr line %q; its stderr:\n%s", line, stderr)
		}
		if !strings.Contains(stdout, line+"\n") {
			t.Errorf("the child failed no test with %q; its output:\n%s", line, stdout)
		}
	}
}

// TestReceiveGivesUpAtTheMarginChild is the child of TestReceiveGivesUpAtTheMargin, run under
// -timeout 45s. It runs only when the parent re-executes this binary.
func TestReceiveGivesUpAtTheMarginChild(t *testing.T) {
	if os.Getenv(childEnv) == "" {
		t.Skip(childEnv + " is not set: this test is the child of TestReceiveGivesUpAtTheMargin")
	}
	end, bounded := GiveUpAt(t)
	if !bounded {
		t.Fatal("the child has no deadline: it needs the -timeout its parent gives it")
	}
	t.Run("before the margin", func(t *testing.T) {
		Receive(t, "a send no one makes", make(chan int))
	})
	if gaveUp := time.Now(); gaveUp.Before(end) {
		t.Errorf("the first Receive gave up %v before the margin, at %v", end.Sub(gaveUp), gaveUp)
	}
	t.Run("a value past the margin", func(t *testing.T) {
		ready := make(chan int, 1)
		ready <- 7
		if got := Receive(t, "a value already sent", ready); got != 7 {
			t.Fatalf("Receive past the margin returned %d, want the 7 sent", got)
		}
	})
	for i := 1; i <= 3; i++ {
		t.Run(fmt.Sprintf("no value past the margin %d", i), func(t *testing.T) {
			Receive(t, "a later send no one makes", make(chan int))
		})
	}
}

// TestNoDeadlineChild is the child of TestUnderNoTimeoutAWaitHasNoEnd, run under -timeout 0. Beside
// the moments and the timer a wait would give up on, which it reads, each wait runs as a subtest of
// its own, so one that gives up hides none of the others: Until reads false twice and true the
// third time, Eventually and InMain are answered true at their third question, and Receive's value
// is sent an hour after Receive began to wait on an empty channel. A wait that gave up on a false
// reading, on the empty channel or after a span of its own fails its subtest.
//
// Receive runs in a synctest bubble, whose clock moves only while every goroutine in it is durably
// blocked. The sender's synctest.Wait returns once the test goroutine is blocked in Receive, and
// at the bubble's start: an idle bubble wakes a goroutine blocked in Wait before it moves its clock
// (Go 1.27.3's runtime/synctest.go, maybeWakeLocked). The sender's hour then passes at once, but
// only once both goroutines are blocked again, and the bubble moves its clock to its earliest
// timer first: a timer of Receive's own shorter than the hour fires before the value is sent, and
// its give-up fails the subtest, as main's send 100 ms on the real clock failed a span under that.
// The sender also ends with the bubble's test (t.Context), so a Receive that gave up leaves no
// goroutine asleep in the bubble, which synctest.Test would end in a panic rather than in the
// subtest's failure. Receive reads the deadline of the test that started the bubble (bubbled). It
// runs only when the parent re-executes this binary.
func TestNoDeadlineChild(t *testing.T) {
	if os.Getenv(childEnv) == "" {
		t.Skip(childEnv + " is not set: this test is the child of TestUnderNoTimeoutAWaitHasNoEnd")
	}
	if end, ok := GiveUpAt(t); ok {
		t.Errorf("under -timeout 0 a wait gives up at %v, want never", end)
	}
	if end, ok := GiveUpAt(noDeadline{t}); ok {
		t.Errorf("under -timeout 0 a wait in a testing.TB without Deadline gives up at %v, want never", end)
	}
	if end, ok := mainGiveUpAt(); ok {
		t.Errorf("under -timeout 0 a wait in TestMain gives up at %v, want never", end)
	}
	expired, stop := expiry(t)
	stop()
	if expired != nil {
		t.Errorf("under -timeout 0 a wait has a timer to give up on, want none")
	}
	if deadline, ok := Context(t).Deadline(); ok {
		t.Errorf("under -timeout 0 Context has the deadline %v, want none", deadline)
	}
	t.Run("until", func(t *testing.T) {
		asks := 0
		if got := Until(t, "the third reading", func() (int, bool) { asks++; return asks, asks == 3 }); got != 3 || asks != 3 {
			t.Errorf("under -timeout 0 Until returned %d after %d readings, want the third reading after 3", got, asks)
		}
	})
	t.Run("eventually", func(t *testing.T) {
		asks := 0
		Eventually(t, "a condition that holds at its third question", func() bool { asks++; return asks == 3 })
		if asks != 3 {
			t.Errorf("under -timeout 0 Eventually returned after %d questions, want 3", asks)
		}
	})
	t.Run("in main", func(t *testing.T) {
		asks := 0
		if err := InMain("a condition that holds at its third question", func() bool { asks++; return asks == 3 }); err != nil || asks != 3 {
			t.Errorf("under -timeout 0 InMain returned %v after %d questions, want nil after 3", err, asks)
		}
	})
	t.Run("receive", func(t *testing.T) {
		outer := t
		synctest.Test(t, func(t *testing.T) {
			sent := make(chan string, 1)
			go func() {
				synctest.Wait()
				select {
				case <-time.After(time.Hour):
					sent <- "the value sent an hour after the wait began"
				case <-t.Context().Done():
				}
			}()
			if got := Receive(bubbled{T: t, outer: outer}, "a value sent an hour after the wait began", sent); got != "the value sent an hour after the wait began" {
				t.Errorf("under -timeout 0 Receive returned %q, want the value sent an hour after it began to wait", got)
			}
		})
	})
}

// noDeadline is a testing.TB with no Deadline method, as a benchmark's is.
type noDeadline struct{ testing.TB }

// bubbled is the *testing.T synctest.Test hands its function, whose Deadline panics there (a
// deadline on the real clock means nothing on the bubble's fake one), with the Deadline of outer,
// the test that started the bubble. TestNoDeadlineChild runs under -timeout 0, where that names
// no deadline, so the bubble's clock decides nothing.
type bubbled struct {
	*testing.T
	outer *testing.T
}

func (b bubbled) Deadline() (time.Time, bool) { return b.outer.Deadline() }

// failing is a testing.TB whose Fatalf keeps its text and ends the goroutine, as testing's does,
// so a test reads a failure the function under test raised without failing itself.
type failing struct {
	testing.TB
	text string
}

func (f *failing) Fatalf(format string, args ...any) {
	f.text = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// failureOf runs call on a goroutine of its own with a failing testing.TB, and returns the text it
// failed with, or "" when it returned.
func failureOf(t *testing.T, call func(testing.TB)) string {
	t.Helper()
	tb := &failing{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		call(tb)
	}()
	Receive(t, "the call to end", done)
	return tb.text
}
