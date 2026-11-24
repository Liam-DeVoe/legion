package classify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// This release judges these histories, every arrival naming no move, to these digests: the run's
// own fuzz (randomHistory, with pushes lost, delivered twice and in random order, settlements and
// reopens) and the fuzz of moves made again (movesMadeAgain, in the reference order and in one
// random or bounded order each). A digest is the SHA-256 of one line per history: its index,
// whether a head run is open, and its judgement (judgementOf) less the moves made.
// LEGION_PRINT_RELEASE_DIGESTS prints this tree's digests (printDigests), which is how these were
// made.
//
// They are not the digests of the release before, and cannot be: that release's judgement carried
// the fix-attempt accounting a CI result moved an issue by, which moves nothing now
// (dispatch://INFRA-714/spec R1), and its histories carried reads of what the base branch
// requires, which this release has no fact for. Every digest of both fuzzes moved again when each
// run of the fence gained the head GitHub reported it on and what it concluded
// (record.AttemptRun), which the judgement carries: both fuzzes settle heads, so every judgement
// with a check run in it reads differently, and a settlement that is newer for some names and
// older for others is applied per name now rather than dropped whole (ClassifySettlement), which
// moves what the fences of the run fuzz's several heads come to. The run fuzz's run-1 digest moved
// once more with the fence the pull request remembers for each settled head (SettledHeads,
// SettlementFor): in two of its histories a head's fence was reset - the branch left the head and
// came back, or a reopen - and a settlement of that head with an older run of ci than one applied
// before was delivered after, which that memory finds stale where the release before applied it.
// Weighing such a settlement against the head's whole remembered fence, rather than its runs alone,
// moved no digest: the fuzz's settlements each name one check and no commit status.
// What they fence is this release against itself: a daemon running ahead of its listener judges
// as recorded here, so a later change that moves that judgement is seen rather than deployed. They
// moved again when a push stopped naming whose role made it, since one App pushes for every role:
// the fuzzes no longer draw that mark, so each seed draws another history. Drawn as before, the
// histories still judged to the digests recorded before the mark went, so that change moved no
// judgement.
var noMoveNamedDigests = map[string]string{
	"run-1":          "245fb1515d4bfe0e6d5f80b70b168529119049c06e0a304fcc444560a8d5a195",
	"run-7":          "636aaa65ec64bb8ea6c3c63540e1c5ff736fbdfcee32618d198660eb6215b6b7",
	"move-1-random":  "de0b76d0bb8becf4530eb29e874479b287c7f8aae16f8f3612e0daf4f60aaaca",
	"move-1-bounded": "0a3e862d97b70d5009c6ca9fcebbb7ca133dd0db0b9d048dac6c6200a41f15ed",
	"move-7-random":  "4a401126c648b3172d9e59387c5917b1035440edbe698bfa611d338cfca25b8e",
	"move-7-bounded": "2e8020c18373dc9f3ecc61dc2de7e143893babf696166fcc8dbe28b5cc6a0a40",
}

// The same histories, every arrival naming its before but no sender, judge to these digests, made
// as noMoveNamedDigests were. The run fuzz's are the no-move ones - its synchronizes name no
// sender either way - and the move fuzz's are not: there the sender is what tells two pushes of
// the same two heads apart.
var noSenderNamedDigests = map[string]string{
	"run-1":          "245fb1515d4bfe0e6d5f80b70b168529119049c06e0a304fcc444560a8d5a195",
	"run-7":          "636aaa65ec64bb8ea6c3c63540e1c5ff736fbdfcee32618d198660eb6215b6b7",
	"move-1-random":  "fec4918ac26a89653730334d08da3c8a5ebca9b6f0e6934b2aae83ca35f85304",
	"move-1-bounded": "2405078960dade0aaed79a17095874391811731650e6e284e42468739e606a0d",
	"move-7-random":  "a532069e07a8b0696d47f6d3424e948362c79e5bcdb1d98d4211d81a15f12383",
	"move-7-bounded": "72e96c683aea948c0c9cf1c98181abfc1c218acddcf269e036dc100662b6678b",
}

// A daemon of this release behind a listener that sends a synchronize's before but not its sender
// judges every history of both fuzzes as recorded: an arrival that names no sender takes no push
// by it and names no move late (NameMove), so the sender is what changes the judgement.
func TestAnArrivalThatNamesNoSenderIsJudgedAsRecorded(t *testing.T) {
	got := fuzzDigests(t, 2000, func(f runFact) runFact {
		f.sender = ""
		return f
	})
	printDigests(t, got)
	for kind, want := range noSenderNamedDigests {
		if got[kind] != want {
			t.Errorf("%s: histories with no sender named digest to %s, want the recorded %s", kind, got[kind], want)
		}
	}
}

// A daemon of this release behind a listener that does not yet send a synchronize's before - an
// arrival that names no move, everywhere - judges every history of both fuzzes as recorded, and
// leaves the same head runs open: the daemon may deploy before the listener. Its behaviour
// changes only once a listener that sends before is deployed.
func TestAnArrivalThatNamesNoMoveIsJudgedAsRecorded(t *testing.T) {
	got := noMoveDigests(t, 2000)
	printDigests(t, got)
	for kind, want := range noMoveNamedDigests {
		if got[kind] != want {
			t.Errorf("%s: histories with no move named digest to %s, want the recorded %s", kind, got[kind], want)
		}
	}
}

// printDigests prints digests under LEGION_PRINT_RELEASE_DIGESTS, which is how the maps above are
// made.
func printDigests(t *testing.T, digests map[string]string) {
	if os.Getenv("LEGION_PRINT_RELEASE_DIGESTS") == "" {
		return
	}
	for _, kind := range slices.Sorted(maps.Keys(digests)) {
		t.Logf("%q: %q,", kind, digests[kind])
	}
}

// noMoveDigests judges each fuzz's first iters histories with every arrival naming no move.
func noMoveDigests(t *testing.T, iters int) map[string]string {
	return fuzzDigests(t, iters, namingNoMove)
}

// fuzzDigests judges each fuzz's first iters histories with every fact as arrive makes it.
func fuzzDigests(t *testing.T, iters int, arrive func(runFact) runFact) map[string]string {
	digests := map[string]string{}
	judge := func(kind string, histories func(it int) (record.PullRequest, []runFact)) {
		h := sha256.New()
		for it := range iters {
			pr, facts := histories(it)
			for _, f := range facts {
				pr = stored(t, deliver(arrive(f), pr))
			}
			j := judgementOf(pr)
			j.Moves = nil
			encoded, err := json.Marshal(j)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(h, "%d run=%v %s\n", it, pr.Run != nil, encoded)
		}
		digests[kind] = hex.EncodeToString(h.Sum(nil))
	}
	for _, seed := range []uint64{1, 7} {
		rng := rand.New(rand.NewPCG(seed, 7))
		judge(fmt.Sprintf("run-%d", seed), func(int) (record.PullRequest, []runFact) {
			pushes, facts := randomHistory(rng)
			if len(pushes) > 0 && rng.IntN(5) == 0 {
				drop := rng.IntN(len(pushes))
				pushes = append(pushes[:drop:drop], pushes[drop+1:]...)
			}
			red := record.PullRequest{HeadSHA: "h2", CheckedHead: "h2", Failing: []string{"ci"},
				CheckRuns: []record.AttemptRun{{Name: "ci", ID: 1}}, Generation: 1}
			order := append([]record.ClassifiedPush{}, pushes...)
			rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
			if len(order) > 0 && rng.IntN(4) == 0 {
				order = append(order, order[rng.IntN(len(order))])
			}
			var seq []runFact
			for f, p := 0, 0; f < len(facts) || p < len(order); {
				if p < len(order) && (f == len(facts) || rng.IntN(2) == 0) {
					seq, p = append(seq, pushed(order[p])), p+1
				} else {
					seq, f = append(seq, facts[f]), f+1
				}
			}
			return red, seq
		})
		for _, bounded := range []bool{false, true} {
			name := "random"
			if bounded {
				name = "bounded"
			}
			rng := rand.New(rand.NewPCG(seed, 3))
			var pending []runFact
			judge(fmt.Sprintf("move-%d-%s", seed, name), func(it int) (record.PullRequest, []runFact) {
				start := redAtH0()
				if it%2 == 0 {
					d := deliveries(rng, movesMadeAgain(rng), bounded)
					pending = d.order
					return start, d.reference
				}
				return start, pending
			})
		}
	}
	return digests
}

// namingNoMove is f as a listener that sends neither a synchronize's before nor its sender
// delivers it.
func namingNoMove(f runFact) runFact {
	f.before, f.sender = "", ""
	return f
}

// One change this release makes to the run with no move named, on purpose: a head run that a
// lost push opened closes at its bound (TestARunOneLostPushOpenedClosesAtItsBound), where the
// release before held it open for good once more heads than its bound on waiting pushes followed
// with no other steps between them. Neither fuzz above reaches that bound.
func TestARunALostPushOpenedClosesAtItsBoundWithNoMoveNamed(t *testing.T) {
	pr := delivered(t, redAtH0(), arrives("h1"))
	prev, closedAt := "h1", 0
	for i := 1; i <= 2*maxRunSteps && closedAt == 0; i++ {
		head := fmt.Sprintf("n%d", i)
		pr = delivered(t, pr, pushed(record.ClassifiedPush{SHA: head, Before: prev}), arrives(head))
		prev = head
		if pr.Run == nil {
			closedAt = i
		}
	}
	if closedAt != maxRunSteps {
		t.Errorf("the run a lost push opened closed after n%d (0: never), want n%d", closedAt, maxRunSteps)
	}
}

// The bound on pushes waiting for a link counts their pairs of heads, each once, as the release
// before kept one push of each two heads: sixteen pairs waiting, each pushed twice by two accounts,
// are all kept, and a seventeenth pair lets go every push of the pair whose newest push is oldest.
func TestTheBoundCountsEachTwoHeadsOnce(t *testing.T) {
	pr := redAtH0()
	for round := range 2 {
		for i := 1; i <= maxWaitingPushes; i++ {
			pr = delivered(t, pr, pushed(record.ClassifiedPush{SHA: fmt.Sprintf("w%d", i), Before: fmt.Sprintf("w%d", i-1), Sender: fmt.Sprintf("pusher-%d", round)}))
		}
	}
	if len(pr.Pushes) != 2*maxWaitingPushes {
		t.Fatalf("%d pairs pushed twice keep %d pushes, want all %d", maxWaitingPushes, len(pr.Pushes), 2*maxWaitingPushes)
	}
	next := record.ClassifiedPush{SHA: fmt.Sprintf("w%d", maxWaitingPushes+1), Before: fmt.Sprintf("w%d", maxWaitingPushes)}
	got, dropped := ApplyPushDropping(pr, next)
	want := record.ClassifiedPush{SHA: "w1", Before: "w0", Sender: "pusher-1"}
	if len(dropped.Waiting) != 1 || dropped.Waiting[0] != want || len(got.Pushes) != 2*maxWaitingPushes-1 {
		t.Errorf("the seventeenth pair let go %+v and kept %d pushes, want w0..w1 named once (its newest push) and %d kept", dropped.Waiting, len(got.Pushes), 2*maxWaitingPushes-1)
	}
}
