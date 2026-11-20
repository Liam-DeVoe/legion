package delivery

import (
	"cmp"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/metrics"
)

// commit_pull_reads.go bounds how often one commit's pull requests are read through
// (ReadThroughCommitPulls). It is branch.go's twin: that file holds how long an answer about a
// repository and a commit's place on one of its branches is believed, this one how long an
// answer about the pull requests of a commit stands before GitHub is asked again. Its one
// caller is the results read (api/results_comparison.go, comparisonReader.readThroughHead), which
// keeps a memo for the span of one request and no policy.
//
// It also holds the memory a read-only caller's results read keeps in place of the rows a
// writer's read stores (readOnlyAnswers, CommitPullAnswers), since that read stores nothing.

// CommitPullReadStaleAfter is how long one commit's read-through stands before another read may
// run for that commit. Each read costs one GET /repos/{owner}/{repo}/commits/{sha}/pulls, and
// one more per pull request GitHub names whose title, body, branch and links name nobody held to
// the repository, since that one's commit messages are paged too (attributeHead). The
// Verification tab refetches an issue's results on every result.recorded, so without a bound
// every refetch would spend those calls again for every result at a commit heading no pull
// request Dispatch knows — for ever, since nothing about such a commit is stored. Five minutes
// is the span every other answer about a commit is believed for (BranchCommitStaleAfter) and
// the one the reconcile re-reads an open head on (PullRequestHeadStaleAfter), so a pull request
// opened at a recorded commit is read as that result's basis within it.
const CommitPullReadStaleAfter = 5 * time.Minute

// CommitPullSettledFor is how long the answer "GitHub names a pull request for this commit whose
// head is another commit" is believed before the commit may be read through again (Settle). That
// answer is worth more than a plain read-through's and less than for ever, and the hour is the
// trade between its two costs: a commit whose pull request GitHub puts elsewhere costs one read
// an hour rather than one every CommitPullReadStaleAfter, and a pull request opened later with
// its head at that commit is seen within an hour rather than never. The second cost is the one
// an unbounded settling got wrong — a commit a push left behind is a commit a later pull request
// can open at — and an hour of `other_commit` on such a result is one refetch's wait away from
// `at_basis`, where never is a restart away.
const CommitPullSettledFor = time.Hour

// CommitPullReads is the moment before which one commit of one repository is not read through
// again, held across reads so a commit costs at most one read-through per span rather than one
// per read of the results. Its zero value is an empty cache, and the server holds one for the
// process (api.server.commitPulls), as it holds one Branches: the answer is a commit's, not a
// request's, and every goroutine reading results shares it.
//
// Only the read-time read-through records here, and the one a recorded result makes
// (api/results.go's readThroughPullRequests) must not: a result recorded while no pull request
// carries its commit would leave a fresh read behind, and the read a moment after the pull
// request opened would be refused a read-through of its own and answer `other_commit` — which
// is the whole of what dispatch://INFRA-1240 repairs.
//
// One decision, one lock: Claim tests the moment held and records the next one together, so no
// caller can hold the two apart and two reads that ask at once cannot both be granted. One
// entry per repository-and-commit pair carries both bounds, which differ only in their span:
// Claim records CommitPullReadStaleAfter past the read it grants, and Settle CommitPullSettledFor
// past GitHub's answer that the pull request's head is elsewhere, which is the longer answer and
// the one that overwrites the claim that bought it.
//
// What bounds the map is that one span. A granted claim drops every entry whose moment has
// passed once the map has passed commitPullReadSweepAt, so nothing is kept for ever and a
// process that reads the results of many issues for weeks holds one entry per commit its last
// hour asked about. That sweep walks the whole map under the lock, and it runs only on a claim
// that is granting a GitHub call, so what waits for it is already waiting for a round trip.
type CommitPullReads struct {
	mu        sync.Mutex
	notBefore map[commitPullKey]time.Time
}

// commitPullReadSweepAt is the size the map is swept from. An entry whose moment has passed
// refuses nothing and is overwritten by the next claim for its pair, so it is dead weight; below
// this size the weight is not worth a walk, and above it a process that reads the results of
// many issues for weeks would otherwise hold one entry per commit it ever asked about.
const commitPullReadSweepAt = 1024

// commitPullKey is the pair one read-through is spent on: a repository, under the canonical name
// its caller resolved (refs.CanonicalRepo), and a commit.
type commitPullKey struct {
	repo string
	sha  string
}

// Claim reports whether sha's pull requests in repoFull may be read through at now — nothing is
// held for the pair, or the moment held has passed — and records the next such moment,
// CommitPullReadStaleAfter past now, when it answers yes, under the one lock that read the
// bound.
//
// A read that fails is claimed with the rest: GitHub was asked, and a repository whose every ask
// fails must not be asked again on every read. One read of one issue's results asks about a
// commit once whatever this answers (api/results_comparison.go, comparisonReader's own memo).
func (c *CommitPullReads) Claim(repoFull, sha string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := commitPullKey{repo: repoFull, sha: sha}
	if until, found := c.notBefore[key]; found && now.Before(until) {
		return false
	}
	if c.notBefore == nil {
		c.notBefore = map[commitPullKey]time.Time{}
	} else if len(c.notBefore) >= commitPullReadSweepAt {
		for swept, until := range c.notBefore {
			if !now.Before(until) {
				delete(c.notBefore, swept)
			}
		}
	}
	c.notBefore[key] = now.Add(CommitPullReadStaleAfter)
	return true
}

// Settle holds the asking about one repository and commit off for CommitPullSettledFor: GitHub
// has been read for the pair and named a pull request whose head is another commit, so this
// commit is behind that pull request and nothing a read within that span can learn about it
// repairs a result there (api/results_comparison.go, comparisonReader.readThroughHead). Without it
// the pair would be claimed again every CommitPullReadStaleAfter for as long as the result is
// read, since nothing is stored at a commit no pull request heads.
//
// It carries a moment because the answer does go stale: GitHub has answered about this commit
// for now, and a pull request opened later can have its head at it, so the answer is believed
// for a span and then asked again. The moment is recorded in the same map a claim records in,
// which is what makes the settling a longer bound of the same kind rather than a second rule:
// the sweep drops it once it has passed, as it drops every other moment no claim can be refused
// by.
func (c *CommitPullReads) Settle(repoFull, sha string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.notBefore == nil {
		c.notBefore = map[commitPullKey]time.Time{}
	}
	c.notBefore[commitPullKey{repo: repoFull, sha: sha}] = now.Add(CommitPullSettledFor)
}

// ReadOnlyAnswerBound is the most answers one memory of a read-only caller's results read holds:
// CommitPullAnswers for the pull requests of a commit, and Branches' own for a commit's place on
// a branch. An answer of either is a handful of short strings (a pull request's head row keeps
// its title and URL, never its body), so the bound holds each memory to a few megabytes at most.
// A memory at the bound whose every answer still stands drops the answer it learned longest ago
// for each new commit (readOnlyAnswers.keep). That is the one place a read-only read may ask
// GitHub where a writer's stored row would answer: a dropped answer is read from GitHub again
// when its commit is read again. An answer that holds pull requests stands for good, as a
// branch's yes does, so once a process's read-only reads name more commits than the bound holds,
// the memory drops one for every new commit, and those extra GitHub reads repeat on every read-only
// read, where a writer's stored rows answer them.
// The memory counts every drop on the process's metrics (CommitPullAnswers.CountDrops,
// Branches.CountReadOnlyDrops) and logs one WARN when it first drops one, and no other until it
// has fallen below the bound.
const ReadOnlyAnswerBound = 1024

// readOnlyAnswers is what a read-only caller's results read remembers of GitHub's answers, since
// it stores none of them: one value per key, each learned at a moment and standing until a moment
// of its own or for good, and at most ReadOnlyAnswerBound of them. Its zero value is an empty
// memory, and the process holds one per kind of answer, shared by every goroutine reading
// results, as it holds CommitPullReads.
type readOnlyAnswers[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]readOnlyAnswer[V]
	// full is whether the memory has dropped an answer at its bound since it was last below it,
	// which is when its one WARN is written (makeRoomLocked).
	full bool
	// dropped counts every answer the memory drops at its bound (countDrops); nil counts none.
	dropped *metrics.Counter
}

// readOnlyAnswer is one remembered value, when it was learned, and the moment it stops standing:
// the zero moment for a value that stands for good, as the row a writer's read stores does.
type readOnlyAnswer[V any] struct {
	value V
	at    time.Time
	until time.Time
}

// standsAt is whether the answer still stands at now.
func (a readOnlyAnswer[V]) standsAt(now time.Time) bool {
	return a.until.IsZero() || now.Before(a.until)
}

// recall answers what is kept for key, and whether it still stands at now.
func (a *readOnlyAnswers[K, V]) recall(key K, now time.Time) (readOnlyAnswer[V], bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.recallLocked(key, now)
}

func (a *readOnlyAnswers[K, V]) recallLocked(key K, now time.Time) (readOnlyAnswer[V], bool) {
	held, found := a.entries[key]
	if !found || !held.standsAt(now) {
		return readOnlyAnswer[V]{}, false
	}
	return held, true
}

// keep remembers answer for key, replacing whatever was kept for key.
//
// A new key in a memory at ReadOnlyAnswerBound first drops every answer that no longer stands at
// now; where that leaves fewer than the bound, the memory has fallen below it. Where every answer
// still stands, it drops the one learned longest ago and counts the drop (countDrops). The first
// drop since the memory was last below its bound also logs one WARN, naming the bound and the
// key, so a memory at its bound is seen in the log rather than in a GitHub bill; the drops after
// it are seen in the count alone, since a memory whose answers stand for good drops one for every
// new key. Nothing grows past the bound.
func (a *readOnlyAnswers[K, V]) keep(key K, answer readOnlyAnswer[V], now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keepLocked(key, answer, now)
}

func (a *readOnlyAnswers[K, V]) keepLocked(key K, answer readOnlyAnswer[V], now time.Time) {
	if a.entries == nil {
		a.entries = map[K]readOnlyAnswer[V]{}
	}
	if _, replacing := a.entries[key]; !replacing && len(a.entries) >= ReadOnlyAnswerBound {
		a.makeRoomLocked(now)
	}
	a.entries[key] = answer
}

// makeRoomLocked frees one place at least in a memory at its bound, as keep's doc comment says.
func (a *readOnlyAnswers[K, V]) makeRoomLocked(now time.Time) {
	for key, held := range a.entries {
		if !held.standsAt(now) {
			delete(a.entries, key)
		}
	}
	if len(a.entries) < ReadOnlyAnswerBound {
		a.full = false
		return
	}
	var oldest K
	var learned time.Time
	first := true
	for key, held := range a.entries {
		if first || held.at.Before(learned) {
			oldest, learned, first = key, held.at, false
		}
	}
	delete(a.entries, oldest)
	a.dropped.Inc()
	if a.full {
		return
	}
	a.full = true
	slog.Warn("dispatch: a read-only results read's memory of GitHub's answers reached its bound with every answer still standing, so it drops the answer learned longest ago for each new one, and each dropped answer is read from GitHub again when its commit is read again, so past the bound those extra reads repeat on every read-only read; every drop is counted on the process's metrics, and this is not logged again until the memory has fallen below its bound",
		"bound", ReadOnlyAnswerBound, "dropped", fmt.Sprintf("%+v", oldest), "learned", learned)
}

// countDrops has the memory count every answer it drops at its bound on dropped.
func (a *readOnlyAnswers[K, V]) countDrops(dropped *metrics.Counter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropped = dropped
}

// CommitPullAnswers is CommitPullReads' twin for a read-only caller's results read: a claim of
// its own, and the answer GitHub gave for each commit it claimed, kept in this process's memory
// because that read stores no row (api/results_comparison.go, comparisonReader.learnCommitPulls).
// A read-only read never touches CommitPullReads, so a writer's read of the same commit inside
// the span still reads through and stores exactly as it would had no read-only read come first.
//
// The claim holds a commit off for the two spans a writer's read is held off for (Claim, Settle):
// CommitPullReadStaleAfter past a claim, and CommitPullSettledFor past an answer that named pull
// requests none of which has the commit as its head. So a read-only read asks GitHub no more and
// no less often than a writer's does. A read that failed keeps no answer, and its claim refuses
// the commit for the rest of CommitPullReadStaleAfter, as a writer's failed read is claimed with
// the rest. A read that failed after it had attributed some of the pull requests GitHub named
// keeps that part, as a writer's read stores it, and is not settled (KeepPart).
//
// The answer outlives its claim, as the rows a writer's read stores outlive the writer's claim:
// every later read-only read lays it on the stored rows (Learned, KeptHeads) until a newer answer
// for the same commit replaces it or the bound drops it. Each head of it is kept with what was
// stored for its pull request when it was learned (ReadStoredRows), and gives way, in those reads,
// to a row a writer has stored since.
//
// Its zero value is an empty memory, and the server holds one for the process
// (api.server.readOnlyCommitPulls). It holds at most ReadOnlyAnswerBound commits, and counts each
// one it drops at the bound on the counter the server hands it (CountDrops).
type CommitPullAnswers struct {
	answers readOnlyAnswers[commitPullKey, commitPullAnswer]
}

// commitPullAnswer is what CommitPullAnswers holds for one commit: GitHub's last answer, nil until
// a read keeps one, and the moment before which the commit is not claimed again.
type commitPullAnswer struct {
	pulls     *CommitPulls
	notBefore time.Time
}

// held is the entry of a memory that answer makes, learned at: one that stands for good where it
// holds an answer, and only while its claim does otherwise.
func (answer commitPullAnswer) held(at time.Time) readOnlyAnswer[commitPullAnswer] {
	held := readOnlyAnswer[commitPullAnswer]{value: answer, at: at}
	if answer.pulls == nil {
		held.until = answer.notBefore
	}
	return held
}

// CommitPullRecall is what CommitPullAnswers.Claim found for one commit.
type CommitPullRecall int

const (
	// CommitPullAsk is a commit no claim holds: Claim recorded the claim, so the caller asks
	// GitHub (ReadCommitPulls) and hands the answer to Keep.
	CommitPullAsk CommitPullRecall = iota
	// CommitPullRecalled is a commit an earlier read-only read claimed and kept an answer for,
	// inside the claim's span: the caller asks GitHub nothing, as a writer's read whose claim is
	// refused asks nothing, and the answer is already among those it reads (Learned).
	CommitPullRecalled
	// CommitPullHeld is a commit an earlier read-only read claimed and kept no answer for: that
	// read is still asking GitHub, or its read failed. The caller asks GitHub nothing, as a
	// writer's read whose claim is refused asks nothing.
	CommitPullHeld
)

// Claim answers what holds sha's pull requests in repoFull at now and, where nothing does,
// records the claim under the one lock that read it, so two read-only reads that ask at once
// spend one GitHub call. An answer an earlier read kept stays kept across the claim.
func (c *CommitPullAnswers) Claim(repoFull, sha string, now time.Time) CommitPullRecall {
	c.answers.mu.Lock()
	defer c.answers.mu.Unlock()
	key := commitPullKey{repo: repoFull, sha: sha}
	held, kept := c.answers.recallLocked(key, now)
	if kept && now.Before(held.value.notBefore) {
		if held.value.pulls == nil {
			return CommitPullHeld
		}
		return CommitPullRecalled
	}
	at := now
	if kept {
		at = held.at
	}
	claimed := commitPullAnswer{pulls: held.value.pulls, notBefore: now.Add(CommitPullReadStaleAfter)}
	c.answers.keepLocked(key, claimed.held(at), now)
	return CommitPullAsk
}

// Keep remembers pulls, GitHub's answer for sha's pull requests in repoFull read at now, with what
// was stored for each of them as ReadStoredRows read it right after, in place of the answer kept
// for it before, and holds the commit off for the span CommitPullAnswers' doc comment gives.
func (c *CommitPullAnswers) Keep(repoFull, sha string, pulls CommitPulls, now time.Time) {
	span := CommitPullReadStaleAfter
	if len(pulls.Heads) > 0 && !pulls.AtHead {
		span = CommitPullSettledFor
	}
	c.keep(repoFull, sha, pulls, now, span)
}

// KeepPart is Keep for the part of GitHub's answer a read attributed before a later step of it
// failed (ReadCommitPulls answers that part with its error), which a writer's read stores all the
// same (ReadThroughCommitPulls). The commit stays held off for CommitPullReadStaleAfter alone,
// whatever the part holds, as a writer's read that fails is claimed and not settled
// (api/results_comparison.go, comparisonReader.readThroughHead).
func (c *CommitPullAnswers) KeepPart(repoFull, sha string, pulls CommitPulls, now time.Time) {
	c.keep(repoFull, sha, pulls, now, CommitPullReadStaleAfter)
}

// keep remembers pulls for sha in repoFull, learned at now and held off for span.
func (c *CommitPullAnswers) keep(repoFull, sha string, pulls CommitPulls, now time.Time, span time.Duration) {
	kept := commitPullAnswer{pulls: &pulls, notBefore: now.Add(span)}
	c.answers.keep(commitPullKey{repo: repoFull, sha: sha}, kept.held(now), now)
}

// CountDrops has the memory count every answer it drops at ReadOnlyAnswerBound on dropped, the
// process's count of them, which the server registers on its metrics. A memory no counter was
// handed counts nothing.
func (c *CommitPullAnswers) CountDrops(dropped *metrics.Counter) {
	c.answers.countDrops(dropped)
}

// Learned is every head the memory keeps, as a read-only caller's reads of the results path lay
// them on the stored rows (KeptHeads): the answers in the order they were read, two read at one
// moment in the order of their repository and commit, and each answer's heads in the order GitHub
// named them, each with what was stored for its pull request when it was learned.
func (c *CommitPullAnswers) Learned() KeptHeads {
	type learned struct {
		key   commitPullKey
		pulls *CommitPulls
		at    time.Time
	}
	c.answers.mu.Lock()
	kept := make([]learned, 0, len(c.answers.entries))
	for key, held := range c.answers.entries {
		if held.value.pulls != nil {
			kept = append(kept, learned{key: key, pulls: held.value.pulls, at: held.at})
		}
	}
	c.answers.mu.Unlock()
	slices.SortFunc(kept, func(a, b learned) int {
		return cmp.Or(a.at.Compare(b.at), cmp.Compare(a.key.repo, b.key.repo), cmp.Compare(a.key.sha, b.key.sha))
	})
	var heads KeptHeads
	for _, one := range kept {
		for index, head := range one.pulls.Heads {
			var row storedRow
			if index < len(one.pulls.rows) {
				row = one.pulls.rows[index]
			}
			heads.columns.add(head, row, one.pulls.storedUnder)
			if head.seedsBranch() {
				heads.merges = append(heads.merges, head)
			}
		}
	}
	return heads
}
