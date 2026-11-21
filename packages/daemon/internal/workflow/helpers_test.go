package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// refusalCode is the code of a fact's refusal, or "" when it was not refused.
func refusalCode(result intake.Result) string {
	if result.Refusal == nil {
		return ""
	}
	return result.Refusal.Code
}

// The heads the fixtures' pull requests stand at, spelled as GitHub spells a commit (40 lowercase
// hexadecimal characters), one per head of the branch.
const (
	// headEarlier is a head the branch left before headOne, for a fixture that needs a head no
	// round of it stands at.
	headEarlier = "9999999999999999999999999999999999999999"
	headOne     = "1111111111111111111111111111111111111111"
	headTwo     = "2222222222222222222222222222222222222222"
	headThree   = "3333333333333333333333333333333333333333"
)

// reviewBody is a review's body as a Legion role wrote it before the reviewer's verdict: what the
// role said, then the Legion footer naming the reviewer's session (seedRoundOneRows) and the head
// it read. The daemon reads no footer, so a fixture writes it only to show that a review carrying
// one decides nothing.
func reviewBody(said, head string) string {
	return said + "\n\n<!-- legion: {\"session\":\"ses-reviewer\",\"phase\":\"review\",\"head\":\"" + head + "\"} -->"
}

// commentBody is reviewBody for the reviewer's COMMENT: its Legion footer names the reviewer's
// session and no head.
func commentBody(said string) string {
	return said + "\n\n<!-- legion: {\"session\":\"ses-reviewer\",\"phase\":\"review\"} -->"
}

// mergingEngine is an engine of a gated project with a fixed clock, which the tests that drive an
// issue through its later phases share.
func mergingEngine() *Engine {
	return mergingEngineAt(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
}

// mergingEngineAt is mergingEngine with its clock at now.
func mergingEngineAt(now time.Time) *Engine {
	return New(record.NewStore(), Config{
		Project: "LEGION", Steps: config.DesignGateRootIssues.Steps(), Linger: time.Hour, AppLogin: testAppLogin, Repositories: testRepositories,
		// The project opens its production check with the tester, the role a project that names
		// no production_check gets (INFRA-809); an engine with no role opens no check at all.
		ProductionCheckRole: claim.RoleTester,
		Clock:               func() time.Time { return now },
	}, nil)
}

// applyFacts applies each fact to pool through engine, failing the test on an error, and returns
// the fact's result. The boot's open of each run no start counted comes before each fact
// (bootBefore), as it comes before any fact the daemon applies, and what the fact left is kept
// for the next pass's check (factLeft).
func applyFacts(t *testing.T, pool *pgxpool.Pool, engine *Engine) func(id string, fact intake.Fact) intake.Result {
	return func(id string, fact intake.Fact) intake.Result {
		t.Helper()
		bootBefore(t, pool, engine)
		result, err := intake.ApplyFact(context.Background(), pool, "api", id, fact, engine, admissionStub{})
		if err != nil {
			t.Fatalf("ApplyFact %s: %v", id, err)
		}
		factLeft(t, pool)
		return result
	}
}

// messageBodies is every Dispatch message the outbox holds, in order.
func messageBodies(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), "select payload->>'body' from outbox where kind = 'dispatch_message' order by id")
	if err != nil {
		t.Fatalf("read Dispatch messages: %v", err)
	}
	defer rows.Close()
	bodies := []string{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatalf("scan Dispatch message: %v", err)
		}
		bodies = append(bodies, body)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate Dispatch messages: %v", err)
	}
	return bodies
}
