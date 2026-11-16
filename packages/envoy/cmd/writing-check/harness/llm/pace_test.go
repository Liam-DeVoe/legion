package llm

import (
	"context"
	"crypto/sha256"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sjawhar/envoy/agentloop"
	"github.com/sjawhar/envoy/internal/dispatch/modeltest"
)

// The tests of the pacer run in a synctest bubble, whose clock is fake: it moves only when every
// goroutine of the bubble waits, so a test reads the exact time a call went through.

// admitLater asks p to admit e in a goroutine of its own, and answers the channel that gets the
// time the pacer admitted it.
func admitLater(p *Pacer, e estimate) <-chan time.Time {
	at := make(chan time.Time, 1)
	go func() {
		if _, err := p.admit(context.Background(), e); err == nil {
			at <- time.Now()
		}
	}()
	return at
}

// admittedAt is the time the call of at went through, once every goroutine of the bubble waits;
// false while it still waits.
func admittedAt(at <-chan time.Time) (time.Time, bool) {
	synctest.Wait()
	select {
	case got := <-at:
		return got, true
	default:
		return time.Time{}, false
	}
}

// admitNow admits e on p, which must not wait.
func admitNow(t *testing.T, p *Pacer, e estimate) paced {
	t.Helper()
	c, err := p.admit(t.Context(), e)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A call whose estimate the last minute's charges leave no room for waits until enough of those
// charges are a minute old.
func TestACallBeyondTheBudgetWaitsForTheWindowToFree(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := NewPacer(1000)
		start := time.Now()
		admitNow(t, p, estimate{whole: 600})
		time.Sleep(10 * time.Second)
		admitNow(t, p, estimate{whole: 400})
		third := admitLater(p, estimate{whole: 500})
		time.Sleep(49 * time.Second)
		if got, ok := admittedAt(third); ok {
			t.Fatalf("the third call went through %s after the start, while the minute's 1000 tokens were spent", got.Sub(start))
		}
		time.Sleep(time.Second)
		got, ok := admittedAt(third)
		if !ok || !got.Equal(start.Add(time.Minute)) {
			t.Fatalf("the third call went through %s after the start (%t), want 1m0s, when the first charge left the minute", got.Sub(start), ok)
		}
		// The minute to 10 s holds the first two charges, 1000 tokens.
		if u := p.Use(); u.Tokens != 1500 || u.BusiestMinute != 1000 {
			t.Errorf("the use is %+v, want 1500 tokens and a busiest minute of 1000", u)
		}
	})
}

// The correction after a reply gives back what the estimate charged above the reply's uncached
// input, and a call that waited for those tokens goes through at once. A reply that reports no
// counts keeps the estimate.
func TestACorrectionGivesBackAnOvercharge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := NewPacer(1000)
		start := time.Now()
		first := admitNow(t, p, estimate{whole: 900})
		second := admitLater(p, estimate{whole: 500})
		time.Sleep(5 * time.Second)
		if _, ok := admittedAt(second); ok {
			t.Fatal("the second call went through while the first's estimate held the minute")
		}
		// Uncached input is the input and the cache writes; the cache reads are not.
		first.settle(agentloop.Usage{Input: 200, CacheWrite: 100, CacheRead: 5000, Metered: true})
		got, ok := admittedAt(second)
		if !ok || !got.Equal(start.Add(5*time.Second)) {
			t.Fatalf("the second call went through %s after the start (%t), want 5s, when the first's reply gave tokens back", got.Sub(start), ok)
		}
		unmetered := admitNow(t, p, estimate{whole: 50})
		unmetered.settle(agentloop.Usage{})
		if u := p.Use(); u.Tokens != 850 {
			t.Errorf("the use is %d tokens, want 300 reported, then the estimates 500 and 50", u.Tokens)
		}
	})
}

// A call larger than the budget goes through at once into an empty minute; into a minute that
// holds a charge, it waits until the minute is empty. Then it is the only call of its minute: the
// calls after it wait until it is a minute old.
func TestACallLargerThanTheBudgetPassesAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		admitNow(t, NewPacer(1000), estimate{whole: 5000})

		p := NewPacer(1000)
		start := time.Now()
		admitNow(t, p, estimate{whole: 100})
		large := admitLater(p, estimate{whole: 5000})
		synctest.Wait()
		small := admitLater(p, estimate{whole: 10})
		time.Sleep(59 * time.Second)
		if _, ok := admittedAt(large); ok {
			t.Fatal("the large call went through while the minute held another charge")
		}
		time.Sleep(time.Second)
		got, ok := admittedAt(large)
		if !ok || !got.Equal(start.Add(time.Minute)) {
			t.Fatalf("the large call went through %s after the start (%t), want 1m0s, when the minute was empty", got.Sub(start), ok)
		}
		if _, ok := admittedAt(small); ok {
			t.Fatal("a small call went through in the large call's minute")
		}
		time.Sleep(time.Minute)
		got, ok = admittedAt(small)
		if !ok || !got.Equal(start.Add(2*time.Minute)) {
			t.Errorf("the small call went through %s after the start (%t), want 2m0s, when the large call left the minute", got.Sub(start), ok)
		}
	})
}

// A call is charged its whole prompt until a reply of an earlier attempt with the same prefix
// reports the prefix written to the cache or read from it; then, for the cache's life from that
// attempt's start, it is charged only the part after the prefix. A call that starts before such a
// reply is charged whole.
func TestAPrefixIsChargedOnlyWhileTheCacheHoldsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := NewPacer(0)
		e := estimate{whole: 1000, prefix: 900, key: sha256.Sum256([]byte("a case's parts")), marked: true}
		charged := func(want int, when string) {
			t.Helper()
			if got := p.charges[len(p.charges)-1].tokens; got != want {
				t.Errorf("%s: charged %d tokens, want %d", when, got, want)
			}
		}
		first := admitNow(t, p, e)
		together := admitNow(t, p, e)
		charged(1000, "a call that starts with the first")
		first.settle(agentloop.Usage{Input: 100, CacheWrite: 900, Metered: true})
		together.settle(agentloop.Usage{Input: 100, CacheWrite: 900, Metered: true})
		time.Sleep(time.Minute)
		read := admitNow(t, p, e)
		charged(100, "a call after the first's reply")
		read.settle(agentloop.Usage{Input: 100, CacheRead: 900, Metered: true})
		time.Sleep(5*time.Minute - time.Second)
		admitNow(t, p, e)
		charged(100, "a call within five minutes of the last read")
		time.Sleep(time.Second)
		admitNow(t, p, e)
		charged(1000, "a call five minutes after the last read")

		// A prefix the provider did not cache, such as one too short for it, stays charged whole.
		short := estimate{whole: 50, prefix: 40, key: sha256.Sum256([]byte("a short prefix")), marked: true}
		admitNow(t, p, short).settle(agentloop.Usage{Input: 50, Metered: true})
		admitNow(t, p, short)
		charged(50, "a call whose prefix no reply reported cached")
	})
}

// The busiest minute is the most uncached input of the charges less than a minute apart.
func TestTheUseHoldsTheBusiestMinute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := NewPacer(0)
		for _, c := range []struct {
			after  time.Duration
			tokens int
		}{{0, 300}, {30 * time.Second, 400}, {40 * time.Second, 500}, {20 * time.Second, 50}} {
			time.Sleep(c.after)
			admitNow(t, p, estimate{whole: c.tokens})
		}
		// The charges are at 0 s, 30 s, 70 s and 90 s. The minute to 70 s holds 400 and 500; the
		// minute to 90 s holds 500 and 50, since the charge at 30 s is then a minute old.
		if u := p.Use(); u.Tokens != 1250 || u.BusiestMinute != 900 {
			t.Errorf("the use is %+v, want 1250 tokens and a busiest minute of 900", u)
		}
	})
}

// Every attempt of a call passes the pacer, and its charge ends as the uncached input its reply
// reported. A call that holds a slot of its own counts the usage it reports.
func TestEveryAttemptPassesThePacer(t *testing.T) {
	stand := modeltest.New(t,
		modeltest.Rule{Once: true, Reply: modeltest.Reply{Text: "not an object"}},
		modeltest.Rule{Reply: modeltest.Reply{Text: `{"ok":true}`}})
	p := NewPacer(1_000_000)
	r := &Runner{Limit: NewLimit(1), Pacer: p}
	resp, err := r.Run(context.Background(), standInModel(stand.URL), Request{Purpose: "test", System: "s", Shared: []string{"the case's parts"}, User: "u", Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	reported := resp.Usage.Input + resp.Usage.CacheWrite
	if resp.Attempts != 2 || len(p.charges) != 2 || reported == 0 {
		t.Fatalf("%d attempts, %d charges, %d tokens reported: want 2 attempts, each charged", resp.Attempts, len(p.charges), reported)
	}
	if u := p.Use(); u.Tokens != reported {
		t.Errorf("the pacer counts %d tokens, want the %d the replies reported", u.Tokens, reported)
	}
	release, err := r.Hold(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release(agentloop.Usage{Input: 70, Metered: true}, agentloop.Usage{Input: 30, CacheWrite: 5, CacheRead: 400, Metered: true})
	if u := p.Use(); u.Tokens != reported+105 {
		t.Errorf("after a held call, the pacer counts %d tokens, want %d", u.Tokens, reported+105)
	}
}
