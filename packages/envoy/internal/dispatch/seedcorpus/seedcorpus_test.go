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
// with the count the seed wrote, and name the two bullets left to a separate part. A second
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
	for _, left := range []string{"LEFT: bullet 15,", "LEFT: bullet 16, a message under"} {
		if !strings.Contains(printed.String(), left) {
			t.Errorf("the self-check printed no line %q", left)
		}
	}
	if strings.Contains(printed.String(), "LEFT: bullet 17") {
		t.Errorf("the self-check leaves bullet 17 to a separate part, which the default seed writes")
	}
	passed := 0
	for _, line := range strings.Split(printed.String(), "\n") {
		if strings.HasPrefix(line, "PASS: ") {
			passed++
		}
	}
	if passed != 29 {
		t.Errorf("the self-check passed %d clauses, want 29 (23 bullets, the three rows and the three replays)", passed)
	}

	// The map of a second conversion of the captured pre-mortem: one line of three tab-separated
	// fields for each of the eleven risks, risk 8 naming the seeded check k-pass and every other
	// risk none.
	preventedBy, err := corpus.PreventedBy()
	if err != nil {
		t.Fatalf("the map of a second pre-mortem conversion: %v", err)
	}
	risks := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(preventedBy, "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[2] == "" {
			t.Fatalf("the map holds the line %q, want three tab-separated fields with a reason", line)
		}
		risks[fields[0]] = fields[1]
	}
	if len(risks) != premortemRisks || risks["8"] != verificationRef(key, "k-pass") || risks["3"] != "none" {
		t.Errorf("the map names %d risks, risk 8 prevented by %q and risk 3 by %q; want %d, %q and none:\n%s",
			len(risks), risks["8"], risks["3"], premortemRisks, verificationRef(key, "k-pass"), preventedBy)
	}

	// The seeded role's claim ended through the store's writer and put back (EndClaim,
	// RestoreClaim), as agent-page.sh ends and restores it: with the claim ended the self-check
	// fails bullet 16 alone, on the read of the role's holder, and once it is back passes whole.
	ended, err := EndClaim(ctx, database.Pool, agentRole)
	if err != nil || len(ended) != 1 || ended[0] != agentSession {
		t.Fatalf("end the claim of %s: ended %v (%v), want %s", agentRole, ended, err, agentSession)
	}
	unclaimed, err := Check(ctx, database.Pool, options)
	var endedFailed *CheckFailed
	if !errors.As(err, &endedFailed) || len(endedFailed.Failed) != 1 ||
		!strings.HasPrefix(endedFailed.Failed[0], "bullet 16, an agent with one session") ||
		!strings.Contains(endedFailed.Failed[0], "read 0 held_by tuples from the role to the session") {
		var lines strings.Builder
		unclaimed.Print(&lines)
		t.Fatalf("the self-check with %s's claim ended answered %v, want one failure naming bullet 16's holder:\n%s", agentRole, err, lines.String())
	}
	if err := RestoreClaim(ctx, database.Pool, agentRole, agentSession); err != nil {
		t.Fatalf("put back the claim of %s: %v", agentRole, err)
	}
	if _, err := Check(ctx, database.Pool, options); err != nil {
		t.Fatalf("the self-check with %s's claim put back: %v", agentRole, err)
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
