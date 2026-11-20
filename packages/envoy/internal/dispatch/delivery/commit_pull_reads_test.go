package delivery

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/metrics"
)

// One claim per pair per CommitPullReadStaleAfter, and the decision and the record are one step:
// the second claim of a pair is refused however soon it asks, and the claim one span later is
// granted. Another commit of the same repository, and the same commit of another, are their own
// pairs.
func TestACommitPullReadIsClaimedOncePerSpan(t *testing.T) {
	var reads CommitPullReads
	start := time.Date(2026, 11, 5, 7, 15, 8, 0, time.UTC)
	if !reads.Claim("acme/widgets", "abc", start) {
		t.Fatal("the first claim of a pair was refused, want it granted")
	}
	if reads.Claim("acme/widgets", "abc", start.Add(CommitPullReadStaleAfter-time.Nanosecond)) {
		t.Fatal("a claim inside the span was granted, want it refused")
	}
	if !reads.Claim("acme/widgets", "def", start) || !reads.Claim("acme/gadgets", "abc", start) {
		t.Fatal("a claim of another pair was refused, want each pair bounded on its own")
	}
	if !reads.Claim("acme/widgets", "abc", start.Add(CommitPullReadStaleAfter)) {
		t.Fatal("the claim one span later was refused, want it granted")
	}
}

// The sweep: a claim granted once the map has passed commitPullReadSweepAt drops every entry no
// claim can be refused by any more, so a process reading results for weeks holds the commits of
// the last CommitPullReadStaleAfter and not every commit it ever asked about. The entries inside
// the span stay, bound and all.
func TestAClaimSweepsTheEntriesNoBoundCanRefuse(t *testing.T) {
	var reads CommitPullReads
	start := time.Date(2026, 11, 5, 7, 15, 8, 0, time.UTC)
	for number := range commitPullReadSweepAt {
		if !reads.Claim("acme/widgets", fmt.Sprintf("%040x", number), start) {
			t.Fatalf("claim %d of a pair of its own was refused, want it granted", number)
		}
	}
	fresh := start.Add(CommitPullReadStaleAfter)
	if !reads.Claim("acme/widgets", "fresh", fresh) {
		t.Fatal("a claim of a pair nothing is held for was refused, want it granted")
	}
	if held := len(reads.notBefore); held != 1 {
		t.Fatalf("the sweep left %d entries, want the one claim inside the span", held)
	}
	if reads.Claim("acme/widgets", "fresh", fresh.Add(time.Minute)) {
		t.Fatal("the entry the sweep kept refused nothing, want the bound to stand")
	}
}

// A settled pair is refused every claim until its span ends, and the sweep keeps it that long.
// GitHub has named a pull request for that commit whose head is another commit
// (ReadThroughCommitPulls' atHead), so it has answered about this commit for now: a claim inside
// CommitPullSettledFor buys the same answer again, and the sweep, which drops the moments no
// claim can be refused by, must not drop one that refuses every claim yet. Once the span has
// passed the pair is a pair like any other — a pull request opened later can have its head at
// that commit — so the claim is granted and the sweep drops what is left.
func TestASettledCommitIsRefusedUntilItsSpanEndsAndTheSweepKeepsItUntilThen(t *testing.T) {
	var reads CommitPullReads
	start := time.Date(2026, 11, 5, 7, 15, 8, 0, time.UTC)
	if !reads.Claim("acme/widgets", "abc", start) || !reads.Claim("acme/widgets", "def", start) {
		t.Fatal("the first claim of a pair was refused, want it granted")
	}
	reads.Settle("acme/widgets", "abc", start)
	if reads.Claim("acme/widgets", "abc", start.Add(CommitPullSettledFor-time.Nanosecond)) {
		t.Fatal("a claim of a settled pair inside its span was granted, want it refused")
	}
	// Enough pairs to pass commitPullReadSweepAt, then one claim to run the sweep over them: the
	// claims' own moments have passed and go, and the settled pair's stands and stays.
	for number := range commitPullReadSweepAt {
		if !reads.Claim("acme/widgets", fmt.Sprintf("%040x", number), start) {
			t.Fatalf("claim %d of a pair of its own was refused, want it granted", number)
		}
	}
	if !reads.Claim("acme/widgets", "fresh", start.Add(CommitPullReadStaleAfter)) {
		t.Fatal("a claim of a pair nothing is held for was refused, want it granted")
	}
	if reads.Claim("acme/widgets", "abc", start.Add(CommitPullReadStaleAfter)) {
		t.Fatal("the swept pair was claimed again, want the settling kept: GitHub has answered about it")
	}
	// A pair nothing settled is bounded by the claim's span alone, so the settling refuses its
	// own pair and no other.
	if !reads.Claim("acme/widgets", "def", start.Add(CommitPullReadStaleAfter)) {
		t.Fatal("a claim of an unsettled pair one span later was refused, want it granted")
	}
	// Past the settled span the pair is read through again, and the sweep drops what the
	// settling left: nothing here is held for the life of the process.
	ended := start.Add(CommitPullSettledFor)
	if !reads.Claim("acme/widgets", "abc", ended) {
		t.Fatal("a claim of the settled pair once its span had passed was refused, want it granted")
	}
	reads.Settle("acme/widgets", "ghi", start)
	for number := range commitPullReadSweepAt {
		if !reads.Claim("acme/widgets", fmt.Sprintf("%040x", number), ended) {
			t.Fatalf("claim %d past the settled span was refused, want it granted", number)
		}
	}
	if _, held := reads.notBefore[commitPullKey{repo: "acme/widgets", sha: "ghi"}]; held {
		t.Fatal("the sweep kept a settled pair whose span had passed, want it dropped with the rest")
	}
}

// The bound of a read-only read's memory is kept and said once. A memory at ReadOnlyAnswerBound
// whose every answer still stands drops the answer learned longest ago for each new one, and an
// answer that holds pull requests stands for good, as a branch's yes does, so a process that has
// learned that many commits drops one for every new commit: the memory writes one WARN when it
// first drops one, and counts every drop on the counter the server hands it. Keeping
// ReadOnlyAnswerBound + 6 commits writes one WARN and counts six drops. Once six answers have
// stopped standing, the next new commit sweeps them and the memory is below its bound, which
// writes and counts nothing; filling it again, and six past, writes one more WARN and counts six
// more. Both memories of the type run it, each handed its counter as the server hands it
// (CommitPullAnswers.CountDrops, Branches.CountReadOnlyDrops), and the count is read off the
// registry's /metrics text, as a scrape reads it.
func TestTheReadOnlyMemoryWarnsOnceAtItsBound(t *testing.T) {
	start := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	commit := func(n int) string { return fmt.Sprintf("%040x", n) }
	t.Run("the pull requests of a commit", func(t *testing.T) {
		var answers CommitPullAnswers
		boundedMemory{
			held:  func() int { return len(answers.answers.entries) },
			count: answers.CountDrops,
			stands: func(n int, at time.Time) {
				answers.Keep("acme/widgets", commit(n), CommitPulls{AtHead: true}, at)
			},
			// A claim no answer came back for stands while it refuses its commit, as a failed
			// read's claim does, and no longer.
			lapses: func(n int, at time.Time) {
				if recalled := answers.Claim("acme/widgets", commit(n), at); recalled != CommitPullAsk {
					t.Fatalf("the claim of a commit nothing was kept for answered %d, want CommitPullAsk", recalled)
				}
			},
			span: CommitPullReadStaleAfter,
		}.warnsOnce(t, start)
	})
	t.Run("a commit's place on a branch", func(t *testing.T) {
		var branches Branches
		// What ReadCommitOnBranch keeps: a yes for good, and a no for BranchCommitStaleAfter.
		keep := func(n int, at time.Time, onBranch bool) {
			kept := readOnlyAnswer[bool]{value: onBranch, at: at}
			if !onBranch {
				kept.until = at.Add(BranchCommitStaleAfter)
			}
			branches.readOnly.keep(branchCommitKeyOf("acme/widgets", "main", commit(n)), kept, at)
		}
		boundedMemory{
			held:   func() int { return len(branches.readOnly.entries) },
			count:  branches.CountReadOnlyDrops,
			stands: func(n int, at time.Time) { keep(n, at, true) },
			lapses: func(n int, at time.Time) { keep(n, at, false) },
			span:   BranchCommitStaleAfter,
		}.warnsOnce(t, start)
	})
}

// boundedMemory is one memory of the readOnlyAnswers type as
// TestTheReadOnlyMemoryWarnsOnceAtItsBound fills it: how many answers it holds, how it is handed
// the counter of its drops, and the two kinds of answer it keeps for commit n learned at: one that
// stands for good, and one that stops standing span after it was learned.
type boundedMemory struct {
	held   func() int
	count  func(*metrics.Counter)
	stands func(n int, at time.Time)
	lapses func(n int, at time.Time)
	span   time.Duration
}

// droppedMetric is the name the test registers the memory's counter under; the server registers
// its own (api's registerReadOnlyAnswerDrops).
const droppedMetric = "read_only_answers_dropped_total"

// warnsOnce fills the memory past its bound twice, falling below it between, and holds it to one
// WARN each time and a count of every drop.
func (m boundedMemory) warnsOnce(t *testing.T, start time.Time) {
	t.Helper()
	logged := captureLogs(t)
	registry := metrics.New()
	m.count(registry.NewCounter(droppedMetric, "Answers the memory dropped at its bound"))
	check := func(when string, warns, drops, held int) {
		t.Helper()
		var warned []map[string]any
		for _, record := range logged() {
			if record["level"] == "WARN" {
				warned = append(warned, record)
			}
		}
		if len(warned) != warns {
			t.Fatalf("%s: the memory wrote %d WARNs, want %d; the first: %v", when, len(warned), warns, warned[:min(len(warned), 1)])
		}
		if warns > 0 && warned[0]["bound"] != float64(ReadOnlyAnswerBound) {
			t.Fatalf("%s: the WARN names the bound %v, want %d: %v", when, warned[0]["bound"], ReadOnlyAnswerBound, warned[0])
		}
		if counted := droppedCounted(t, registry); counted != drops {
			t.Fatalf("%s: the metrics count %d drops, want %d", when, counted, drops)
		}
		if got := m.held(); got != held {
			t.Fatalf("%s: the memory holds %d answers, want %d", when, got, held)
		}
	}
	for n := range ReadOnlyAnswerBound {
		m.stands(n, start.Add(time.Duration(n)))
	}
	check("filled to its bound", 0, 0, ReadOnlyAnswerBound)
	// Six more commits, each dropping the answer learned longest ago, since every answer still
	// stands: these six until a span after they were learned.
	later := start.Add(time.Minute)
	for n := range 6 {
		m.lapses(ReadOnlyAnswerBound+n, later)
	}
	check("six commits past its bound", 1, 6, ReadOnlyAnswerBound)
	// A span later those six no longer stand, and the next new commit sweeps them: the memory is
	// below its bound and drops nothing.
	lapsed := later.Add(m.span)
	m.stands(ReadOnlyAnswerBound+6, lapsed)
	check("swept below its bound", 1, 6, ReadOnlyAnswerBound-5)
	// Filled again, and six past.
	for n := range 5 + 6 {
		m.stands(ReadOnlyAnswerBound+7+n, lapsed.Add(time.Duration(n)))
	}
	check("filled again and six past", 2, 12, ReadOnlyAnswerBound)
}

// droppedCounted is the drops registry's /metrics text counts under droppedMetric: 0 while the
// text carries no sample of it, as a counter carries none before its first count.
func droppedCounted(t *testing.T, registry *metrics.Registry) int {
	t.Helper()
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for line := range strings.SplitSeq(response.Body.String(), "\n") {
		if value, found := strings.CutPrefix(line, droppedMetric+" "); found {
			counted, err := strconv.Atoi(value)
			if err != nil {
				t.Fatalf("the metrics line %q counts no whole number: %v", line, err)
			}
			return counted
		}
	}
	return 0
}
