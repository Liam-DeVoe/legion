package bus

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// fakeStartClock is a start's clock that only the start and its tries move: a sleep moves it by the
// wait, plus late[i] for the i'th sleep when late names one, as a process the scheduler wakes late
// from a wait finds the clock, and a try by the time the try takes.
type fakeStartClock struct {
	at    time.Time
	late  []time.Duration
	slept []time.Duration
}

func (c *fakeStartClock) clock() startClock {
	return startClock{
		now: func() time.Time { return c.at },
		sleep: func(wait time.Duration) {
			if i := len(c.slept); i < len(c.late) {
				c.at = c.at.Add(c.late[i])
			}
			c.slept = append(c.slept, wait)
			c.at = c.at.Add(wait)
		},
	}
}

// scriptedTry is one try of a start in a row of TestACredentialWaitWaitsOutRefusalsUpToItsBound:
// what it returns, and how long it takes.
type scriptedTry struct {
	err  error
	took time.Duration
}

// The schedule of a start that waits out refusals of its credential (waitOutCredentialRefusals), on
// a clock only the start moves: it waits 1 s after the first refusal and doubles each wait up to
// 10 s, cuts the last wait to end at its bound, counted from its first try, and lets a try in
// progress at the bound finish and succeed. It begins no try after the bound: a wait the clock
// wakes it from past the bound ends the start, except that the try at the bound, the one the
// wait cut to the bound leads to, may begin up to startTryLateness late. A refusal of the stream's
// ensure counts as one of the connect. Any other error counts toward main's ten tries in a row,
// one second apart, and a refusal ends such a row.
func TestACredentialWaitWaitsOutRefusalsUpToItsBound(t *testing.T) {
	refusal := fmt.Errorf("dial: %w", nats.ErrAuthorization)
	denied := fmt.Errorf("ensure stream: %w: nats: Permissions Violation for Publish to \"$JS.API.STREAM.INFO.ENVOY_NOTIFICATIONS\"", ErrPublishDenied)
	other := errors.New("nats: no servers available for connection")
	repeat := func(n int, try scriptedTry) []scriptedTry { return slices.Repeat([]scriptedTry{try}, n) }
	second := time.Second
	for _, row := range []struct {
		name  string
		bound time.Duration
		// tries are the tries' outcomes in order; a start that tries past the last one gets the last
		// one again.
		tries []scriptedTry
		// late is how far past its end the clock wakes the start from each of its waits, in order.
		late      []time.Duration
		wantSlept []time.Duration
		// wantTries is how many tries the start makes, and wantErr what its error wraps (nil: none).
		wantTries int
		wantErr   error
		// wantRefused is how many refusals the start reports to its caller.
		wantRefused int
	}{
		{
			name:        "refused until the bound",
			bound:       30 * time.Second,
			tries:       []scriptedTry{{err: refusal}},
			wantSlept:   []time.Duration{second, 2 * second, 4 * second, 8 * second, 10 * second, 5 * second},
			wantTries:   7,
			wantErr:     nats.ErrAuthorization,
			wantRefused: 7,
		},
		{
			name:  "a try in progress at the bound succeeds",
			bound: 30 * time.Second,
			tries: append(repeat(6, scriptedTry{err: refusal}), scriptedTry{took: 3 * time.Second}),
			// The seventh try starts at the bound and takes three seconds past it.
			wantSlept:   []time.Duration{second, 2 * second, 4 * second, 8 * second, 10 * second, 5 * second},
			wantTries:   7,
			wantRefused: 6,
		},
		{
			name:        "a refusal of the stream's ensure is waited out",
			bound:       30 * time.Second,
			tries:       []scriptedTry{{err: denied}, {err: denied}, {}},
			wantSlept:   []time.Duration{second, 2 * second},
			wantTries:   3,
			wantRefused: 2,
		},
		{
			name:      "other errors end the start after ten in a row",
			bound:     180 * time.Second,
			tries:     []scriptedTry{{err: other}},
			wantSlept: slices.Repeat([]time.Duration{second}, 9),
			wantTries: 10,
			wantErr:   other,
		},
		{
			name:  "a refusal ends a row of other errors",
			bound: 180 * time.Second,
			tries: slices.Concat(repeat(9, scriptedTry{err: other}), []scriptedTry{{err: refusal}}, repeat(9, scriptedTry{err: other}), []scriptedTry{{}}),
			wantSlept: slices.Concat(
				slices.Repeat([]time.Duration{second}, 9), []time.Duration{second},
				slices.Repeat([]time.Duration{second}, 9)),
			wantTries:   20,
			wantRefused: 1,
		},
		{
			name:  "other errors after a refusal end at the bound",
			bound: 5 * time.Second,
			tries: []scriptedTry{{err: refusal}, {err: other}},
			// One wait of a second after the refusal, then main's second after each other error,
			// until the bound: tries at 0, 1, 2, 3, 4 and 5 s.
			wantSlept:   slices.Repeat([]time.Duration{second}, 5),
			wantTries:   6,
			wantErr:     other,
			wantRefused: 1,
		},
		{
			name:  "a wait the clock wakes the start from past the bound begins no try",
			bound: 3 * time.Second,
			tries: []scriptedTry{{err: refusal}, {}},
			// The wait of a second after the first refusal ends at 11 s.
			late:        []time.Duration{10 * time.Second},
			wantSlept:   []time.Duration{second},
			wantTries:   1,
			wantErr:     nats.ErrAuthorization,
			wantRefused: 1,
		},
		{
			name:  "the try at the bound begins though its wait ends a little late",
			bound: 3 * time.Second,
			tries: []scriptedTry{{err: refusal}, {err: refusal}, {}},
			// The wait of 2 s after the second refusal is the one cut to end at the bound, and it
			// ends 200 ms past it.
			late:        []time.Duration{0, 200 * time.Millisecond},
			wantSlept:   []time.Duration{second, 2 * second},
			wantTries:   3,
			wantRefused: 2,
		},
		{
			name:        "the try at the bound begins no later than the start's lateness allows",
			bound:       3 * time.Second,
			tries:       []scriptedTry{{err: refusal}, {err: refusal}, {}},
			late:        []time.Duration{0, startTryLateness + time.Millisecond},
			wantSlept:   []time.Duration{second, 2 * second},
			wantTries:   2,
			wantErr:     nats.ErrAuthorization,
			wantRefused: 2,
		},
		{
			name:  "an error that ends the start ends it at once, as a failed start check's does",
			bound: 30 * time.Second,
			tries: []scriptedTry{{err: refusal}, {err: endsStart{other}}, {}},
			// After one wait, the second try fails in a way no wait repairs (a start check's error
			// that is no refusal, such as a stream the server does not hold): no third try.
			wantSlept:   []time.Duration{second},
			wantTries:   2,
			wantErr:     other,
			wantRefused: 1,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			clock := &fakeStartClock{at: time.Date(2026, 11, 22, 0, 3, 50, 0, time.UTC), late: row.late}
			made, tries, refused := &Client{}, 0, 0
			client, err := waitOutCredentialRefusals(row.bound, func(error, time.Duration) { refused++ }, clock.clock(), func() (*Client, error) {
				try := row.tries[min(tries, len(row.tries)-1)]
				tries++
				clock.at = clock.at.Add(try.took)
				if try.err != nil {
					return nil, try.err
				}
				return made, nil
			})
			if tries != row.wantTries {
				t.Errorf("the start made %d tries, want %d", tries, row.wantTries)
			}
			if !slices.Equal(clock.slept, row.wantSlept) {
				t.Errorf("the start waited %v, want %v", clock.slept, row.wantSlept)
			}
			if refused != row.wantRefused {
				t.Errorf("the start reported %d refusals, want %d", refused, row.wantRefused)
			}
			switch {
			case row.wantErr == nil && (err != nil || client != made):
				t.Fatalf("the start returned a client (%t) and %v; want the client its last try made", client != nil, err)
			case row.wantErr != nil && (err == nil || !errors.Is(err, row.wantErr)):
				t.Fatalf("the start returned a client (%t) and %v; want no client and an error wrapping %v", client != nil, err, row.wantErr)
			}
		})
	}
}

// A start that ends at its bound after refusals names the bound and the last refusal, and one
// whose last try failed otherwise names that error as well as the last refusal.
func TestACredentialWaitThatEndsNamesTheLastRefusal(t *testing.T) {
	refusal := fmt.Errorf("dial: %w", nats.ErrAuthorization)
	other := errors.New("nats: no servers available for connection")
	clock := &fakeStartClock{at: time.Date(2026, 11, 22, 0, 3, 50, 0, time.UTC)}
	tries := 0
	_, err := waitOutCredentialRefusals(5*time.Second, nil, clock.clock(), func() (*Client, error) {
		tries++
		if tries == 1 {
			return nil, refusal
		}
		return nil, other
	})
	if err == nil || !errors.Is(err, nats.ErrAuthorization) || !errors.Is(err, other) {
		t.Fatalf("the start ended with %v, want an error naming the last refusal and the last try's error", err)
	}
	for _, want := range []string{"5s", refusal.Error(), other.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the start ended with %q, want it to name %q", err, want)
		}
	}
}
