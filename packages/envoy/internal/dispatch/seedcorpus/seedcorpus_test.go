package seedcorpus

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// TestTheCorpusIsSeededAndReadBack seeds the whole default corpus into an empty database of its
// own and reads it back with the self-check, which must find every bullet and every named row
// with the count the seed wrote, and name the three bullets left to a separate part. A second
// seed of the same project is refused, since it would double every count. Then one bullet is
// broken in the store by a statement of the test's own - the answered ask put back to open - and
// the self-check must fail naming that bullet and the read that found it, so a check that passes
// is one that could have failed.
func TestTheCorpusIsSeededAndReadBack(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	fixtures, err := FindFixtures(".")
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := Seed(ctx, database, Options{Fixtures: fixtures})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	key := corpus.Get("model.issue")
	if key == "" {
		t.Fatal("the seed names no seeded issue")
	}
	options := CheckOptions{Fixtures: fixtures}
	report, err := Check(ctx, database.Pool, options)
	var printed strings.Builder
	report.Print(&printed)
	t.Log(printed.String())
	if err != nil {
		t.Fatalf("the self-check of a fresh seed: %v", err)
	}
	for _, left := range []string{"LEFT: bullet 15,", "LEFT: bullet 16, a message under", "LEFT: bullet 17,"} {
		if !strings.Contains(printed.String(), left) {
			t.Errorf("the self-check printed no line %q", left)
		}
	}
	passed := 0
	for _, line := range strings.Split(printed.String(), "\n") {
		if strings.HasPrefix(line, "PASS: ") {
			passed++
		}
	}
	if passed != 28 {
		t.Errorf("the self-check passed %d clauses, want 28 (22 bullets, the three rows and the three replays)", passed)
	}

	// A second seed finds the first one's events, and as a test copy finds its corpus.
	if _, err := Seed(ctx, database, Options{Fixtures: fixtures}); !errors.Is(err, ErrForeignEvents) {
		t.Errorf("a second seed of the database answered %v, want ErrForeignEvents", err)
	}
	if _, err := Seed(ctx, database, Options{Fixtures: fixtures, TestCopy: true}); err == nil || !strings.Contains(err.Error(), "holds the corpus already") {
		t.Errorf("a second seed of the same project as a test copy answered %v, want a refusal naming the corpus it holds", err)
	}

	if _, err := database.Pool.Exec(ctx, `update asks set state = 'open', answer = null where issue_key = $1 and block_id = 'q-answered'`, key); err != nil {
		t.Fatalf("break bullet 1: %v", err)
	}
	broken, err := Check(ctx, database.Pool, options)
	var failed *CheckFailed
	if !errors.As(err, &failed) || len(failed.Failed) != 1 || !strings.HasPrefix(failed.Failed[0], "bullet 1, an answered ask") ||
		!strings.Contains(failed.Failed[0], "read 0 asks rows of block q-answered answered") {
		var lines strings.Builder
		broken.Print(&lines)
		t.Fatalf("the self-check of the broken bullet answered %v, want one failure naming bullet 1 and its read:\n%s", err, lines.String())
	}
}
