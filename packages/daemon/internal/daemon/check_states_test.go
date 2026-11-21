package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// phaseVerdictGoneVersion is the migration that drops phases.verdict
// (internal/store/migrations/20261121074614_phase_verdict_gone.up.sql): a rollback to a release
// that reads that column runs its way back before any older one (docs/kubernetes.md).
const phaseVerdictGoneVersion = 20261121074614

// entryRigHead is the head of the pull request outbox_entry_test.go's rig records: a full commit id,
// since a check.state_changed event names its head as one.
const entryRigHead = "0000000000000000000000000000000000c0ffee"

// testedFact is the check.state_changed event that decides a tester's completion of issue's
// testing, and the event id the live consumer would apply it under: k1, the head's one check, yes
// from the tester where passes, and no for a fail where not, at head, or at the head of the pull
// request on record where head is "", as a completion naming no commit tests. It is the payload of
// packages/contracts/fixtures/dispatch-events/check-state-changed.json with those fields, decoded
// by the intake's own decoder of a Dispatch event, at the serial after the highest the daemon
// holds of issue.
func testedFact(t *testing.T, pool intake.Pool, issue, head string, passes bool) (intake.Fact, string) {
	t.Helper()
	ctx := context.Background()
	repo := "acme/widgets"
	var serial int64
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin the read of %s's pull request: %v", issue, err)
	}
	defer tx.Rollback(ctx)
	pr, err := record.NewStore().PullRequest(ctx, tx, issue)
	if err == nil {
		err = tx.QueryRow(ctx, `select coalesce(max(serial), 0) from check_state_serials where issue = $1`, issue).Scan(&serial)
	}
	if err != nil {
		t.Fatalf("read the pull request and the serials of %s: %v", issue, err)
	}
	if pr != nil {
		repo = pr.Repo
		if head == "" {
			head = pr.HeadSHA
		}
	}
	encoded, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "fixtures", "dispatch-events", "check-state-changed.json"))
	if err != nil {
		t.Fatalf("read the shared fixture of check.state_changed: %v", err)
	}
	var fixture struct {
		Type      string          `json:"type"`
		Actor     json.RawMessage `json:"actor"`
		CheckForm json.RawMessage `json:"check_form"`
	}
	if err := json.Unmarshal(encoded, &fixture); err != nil || fixture.Type == "" {
		t.Fatalf("decode the shared fixture of check.state_changed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(fixture.CheckForm, &payload); err != nil {
		t.Fatalf("decode the fixture's check form: %v", err)
	}
	serial++
	payload["issue"], payload["repo"], payload["head"], payload["serial"] = issue, repo, head, serial
	payload["checks"], payload["check"], payload["roles"], payload["ci_case"] = []string{"k1"}, "k1", []string{"tester"}, ""
	payload["state"], payload["reason"] = "yes", ""
	if !passes {
		payload["state"], payload["reason"], payload["roles"] = "no", "fail", []string{}
	}
	seq := 1000 + serial
	event, err := json.Marshal(map[string]any{"id": 5000 + seq, "issue_key": issue, "seq": seq, "type": fixture.Type, "notify": false,
		"created_at": "2026-11-21T06:00:00Z", "actor": fixture.Actor, "payload": payload})
	if err != nil {
		t.Fatalf("encode the state event: %v", err)
	}
	project, _, _ := strings.Cut(issue, "-")
	fact, unread, err := intake.DecodeDispatchEvent(project, issue, event)
	if _, ok := fact.(intake.DispatchCheckState); !ok || err != nil || len(unread) != 0 {
		t.Fatalf("the state event of %s at %s decoded to %#v, unread %q, %v; want a check state", issue, head, fact, unread, err)
	}
	return fact, fmt.Sprintf("dispatch-event:%s:%d", issue, seq)
}

// testedAs applies testedFact's event through intake.ApplyFact and handlers, as the live consumer
// applies it, ahead of the tester's completion it decides. With no handlers it applies the event
// through a workflow of its own over pool, which holds the state as the daemon's own workflow
// does: a live proof's daemon runs no consumer of Dispatch's events, and the completion that
// follows is the daemon's own to decide on what the store then holds.
func testedAs(t *testing.T, pool intake.Pool, issue, head string, passes bool, handlers ...intake.Handler) {
	t.Helper()
	if len(handlers) == 0 {
		handlers = []intake.Handler{workflow.New(record.NewStore(), workflow.Config{Project: "LEGION"}, quietLogger())}
	}
	fact, id := testedFact(t, pool, issue, head, passes)
	if result, err := intake.ApplyFact(context.Background(), pool, "dispatch", id, fact, handlers...); err != nil || result.Refusal != nil {
		t.Fatalf("apply the state event %s: %+v, %v", id, result.Refusal, err)
	}
}
