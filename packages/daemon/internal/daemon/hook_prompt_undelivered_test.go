package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// loopHook is a second hook firing on the move back firedHook fired on, as two hooks of the
// deployment firing on one move back queue two prompts for one role.
var loopHook = record.HookPrompt{Hook: "review-loop", Role: claim.RoleImplementer,
	From: phase.Reviewing, To: phase.Implementing, Count: 3,
	Counts: map[string]int{"reviewing-implementing": 3, "testing-implementing": 1},
	Prompt: "Say what the reviewer asked for last time and what you did about it."}

// unheldHookRunner is hookRunner against a listener that keeps nothing for a role no live session
// holds, as the listener answers a publish that names no source session: every publish to the
// implementer's topic is refused notify.ErrNoHolder until the test hands the role to a session
// (holderPublisher.setAbsent with no topic).
func unheldHookRunner(t *testing.T) (*outbox, *supervisor, *store.Pool, *holderPublisher, string) {
	t.Helper()
	publisher := &holderPublisher{}
	runner, sup, pool := hookRunner(t, publisher)
	topic := notify.RoleTopicPrefix + string(mustClaimToken(t, "LEGION-208", claim.RoleImplementer))
	publisher.setAbsent(topic)
	return runner, sup, pool, publisher, topic
}

// hookRowMadeAt enqueues payload's hook-prompt row for LEGION-208 as made at made, due now.
func hookRowMadeAt(t *testing.T, runner *outbox, pool *store.Pool, payload record.HookPrompt, made time.Time) {
	t.Helper()
	row := mustOutboxRow(t, "LEGION-208", payload, time.Now())
	row.CreatedAt = made
	enqueueOutbox(t, pool, runner.records, row)
}

// rowsOf is how many rows of kind the outbox holds for LEGION-208.
func rowsOf(t *testing.T, pool *store.Pool, kind record.OutboxKind) int {
	t.Helper()
	var rows int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where issue = 'LEGION-208' and kind = $1`, string(kind)).Scan(&rows); err != nil {
		t.Fatalf("count the %s rows: %v", kind, err)
	}
	return rows
}

// issueMessages is the body of every Dispatch message the outbox holds for LEGION-208, in order.
func issueMessages(t *testing.T, pool *store.Pool) []string {
	t.Helper()
	var bodies []string
	if err := store.Transact(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select payload->>'body' from outbox where issue = 'LEGION-208' and kind = $1 order by id`,
			string(record.OutboxKindDispatchMessage))
		if err != nil {
			return err
		}
		bodies, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatalf("read the Dispatch messages of LEGION-208: %v", err)
	}
	return bodies
}

// requireSays fails unless body holds every one of words.
func requireSays(t *testing.T, body string, words ...string) {
	t.Helper()
	for _, word := range words {
		if !strings.Contains(body, word) {
			t.Fatalf("the message on the issue is\n\t%s\nwant it to say %q", body, word)
		}
	}
}

// A hook prompt the listener refuses because no live session holds its role (a listener that keeps
// nothing for a publish naming no source session, as the daemon's prompts name none) waits for a
// session to hold the role while the hour after its row was made runs: retried on the row's
// backoff, nothing posted on the issue. The listener used to keep the prompt for that hour; the row
// is its keeper now.
func TestAHookPromptNoSessionHoldsWaitsForTheRoleInsideTheHour(t *testing.T) {
	t.Parallel()
	runner, _, pool, publisher, _ := unheldHookRunner(t)
	hookRowMadeAt(t, runner, pool, firedHook, time.Now().Add(-time.Minute))

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("drain the hook prompt: %v", err)
	}

	if attempts, delivered := publisher.snapshot(); attempts != 1 || len(delivered) != 0 || len(publisher.keptCopies()) != 0 {
		t.Fatalf("publish attempts = %d, %d delivered, %d kept; want the one refused attempt", attempts, len(delivered), len(publisher.keptCopies()))
	}
	if left := rowsOf(t, pool, record.OutboxKindHookPrompt); left != 1 {
		t.Fatalf("hook-prompt rows left = %d, want the row waiting for a session to hold the role", left)
	}
	var waited string
	if err := pool.QueryRow(context.Background(), `select last_error from outbox where kind = $1`, string(record.OutboxKindHookPrompt)).Scan(&waited); err != nil {
		t.Fatalf("read the row's last error: %v", err)
	}
	if !strings.Contains(waited, "waits for a session to hold") {
		t.Fatalf("the row's last error is %q, want the wait for a session to hold the role", waited)
	}
	if posted := issueMessages(t, pool); len(posted) != 0 {
		t.Fatalf("messages on the issue = %q, want none inside the hour", posted)
	}
}

// Once the hour after its row was made has passed with no session holding its role, the prompt's
// row finishes, and one Dispatch message on the issue says so, in the form of the hook's own firing
// message: the hook, the role and the issue, why the prompt ended, the move back it was written
// for and its words, and that no session got them and nothing sends them again. The control is
// the same row at the base, which finishes at its first refusal with no message.
func TestAHookPromptNoSessionHeldForAnHourEndsWithAMessageOnTheIssue(t *testing.T) {
	t.Parallel()
	runner, _, pool, publisher, _ := unheldHookRunner(t)
	hookRowMadeAt(t, runner, pool, firedHook, time.Now().Add(-time.Hour-time.Minute))

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("drain the hook prompt: %v", err)
	}

	if attempts, delivered := publisher.snapshot(); attempts != 1 || len(delivered) != 0 {
		t.Fatalf("publish attempts = %d with %d delivered, want the one refused attempt", attempts, len(delivered))
	}
	if left := rowsOf(t, pool, record.OutboxKindHookPrompt); left != 0 {
		t.Fatalf("hook-prompt rows left = %d, want the row finished", left)
	}
	posted := issueMessages(t, pool)
	if len(posted) != 1 {
		t.Fatalf("messages on the issue = %q, want one", posted)
	}
	t.Logf("the message on the issue: %s", posted[0])
	requireSays(t, posted[0], "Legion hook back-to-implementing", "implementer", "LEGION-208",
		"no session held the implementer's role in the hour after the hook fired",
		"fired at 3 moves back: reviewing to implementing", firedHook.Prompt,
		"No session of the implementer received", "will not send")
}

// A role whose claim failed or was retired has no session coming back for it, so its prompt ends
// at its first refusal, inside the hour, with the message on the issue naming that end. The control
// is the base, which finishes the row with no message.
func TestAHookPromptToARoleThatIsNotComingEndsAtOnceWithAMessageOnTheIssue(t *testing.T) {
	t.Parallel()
	for _, state := range []supervise.ClaimState{supervise.StateFailed, supervise.StateRetired} {
		t.Run(string(state), func(t *testing.T) {
			runner, sup, pool, _, _ := unheldHookRunner(t)
			if _, _, err := sup.Create(context.Background(), supervise.Claim{Token: mustClaimToken(t, "LEGION-208", claim.RoleImplementer),
				Project: "legion", Tree: "LEGION-208", Issue: "LEGION-208", Role: claim.RoleImplementer, Generation: 1, ServingGeneration: 1,
				State: state}, ""); err != nil {
				t.Fatalf("create the %s implementer's claim: %v", state, err)
			}
			hookRowMadeAt(t, runner, pool, firedHook, time.Now().Add(-time.Minute))

			if err := runner.RunOnce(context.Background()); err != nil {
				t.Fatalf("drain the hook prompt: %v", err)
			}

			if left := rowsOf(t, pool, record.OutboxKindHookPrompt); left != 0 {
				t.Fatalf("hook-prompt rows left = %d, want the row finished at once", left)
			}
			posted := issueMessages(t, pool)
			if len(posted) != 1 {
				t.Fatalf("messages on the issue = %q, want one", posted)
			}
			requireSays(t, posted[0], "Legion hook back-to-implementing", "LEGION-208",
				"the implementer's claim is "+string(state)+", so no session of it will hold the role again", firedHook.Prompt)
		})
	}
}

// Prompts for one role that end together are named in one message, so a role that is not coming
// posts one message rather than one per hook: when a row ends undelivered, every other prompt row
// of that role on the issue ends with it, a younger one inside its own hour included, and the one
// message names each hook and its words. A prompt for another role is no part of it. The control is
// the base, which finishes the first row at its refusal and posts nothing.
func TestTwoHookPromptsOfOneRoleThatEndTogetherAreNamedInOneMessage(t *testing.T) {
	t.Parallel()
	runner, _, pool, _, _ := unheldHookRunner(t)
	tester := firedHook
	tester.Role, tester.Hook, tester.Prompt = claim.RoleTester, "tester-loop", "Run the failing case first."
	hookRowMadeAt(t, runner, pool, firedHook, time.Now().Add(-time.Hour-time.Minute))
	hookRowMadeAt(t, runner, pool, loopHook, time.Now().Add(-5*time.Minute))
	// The tester's own row is not due yet, so the drain runs the implementer's alone.
	testerRow := mustOutboxRow(t, "LEGION-208", tester, time.Now().Add(time.Hour))
	enqueueOutbox(t, pool, runner.records, testerRow)

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("drain the hook prompts: %v", err)
	}

	if left := rowsOf(t, pool, record.OutboxKindHookPrompt); left != 1 {
		t.Fatalf("hook-prompt rows left = %d, want the tester's alone", left)
	}
	posted := issueMessages(t, pool)
	if len(posted) != 1 {
		t.Fatalf("messages on the issue = %q, want one naming both of the implementer's prompts", posted)
	}
	t.Logf("the message on the issue: %s", posted[0])
	requireSays(t, posted[0], "back-to-implementing", firedHook.Prompt, "review-loop", loopHook.Prompt, "implementer")
	if strings.Contains(posted[0], tester.Prompt) {
		t.Fatalf("the message on the issue names the tester's prompt, which is still waiting: %s", posted[0])
	}
}

// A released role's woken session can claim its role only after several of the row's attempts:
// the first wakes the pod and waits, and each later one before the claim meets a role no live
// session holds, which the listener refuses with nothing kept. The row waits through each of them
// on its backoff, and its first attempt once the session holds the role delivers the prompt, once:
// a refused publish stores nothing, so the one forward is the one the session gets. The pod is
// woken once and nothing is posted on the issue. The control is the base, whose row finishes at
// the first refusal, the prompt kept nowhere.
func TestAHookPromptReachesItsWokenRoleAfterAClaimSlowerThanTwoRetries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner, sup, pool, publisher, topic := unheldHookRunner(t)
	machine := releasedClaim(t, sup, claim.RoleImplementer)
	hookRowMadeAt(t, runner, pool, firedHook, time.Now().Add(-time.Minute))

	if err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("the row's first attempt: %v", err)
	}
	if attempts, _ := publisher.snapshot(); attempts != 0 {
		t.Fatalf("the first attempt published %d times, want none: it wakes the released pod and waits", attempts)
	}
	if held := machine.Claim(); held.Released() {
		t.Fatalf("the implementer is %s by %s after the first attempt, want the wake to have resumed it", held.State, held.SuspendedBy)
	}
	// Two attempts, each past the row's backoff, while the woken session has claimed nothing.
	for step := 1; step <= 2; step++ {
		drainAt(t, runner, time.Duration(step)*time.Minute, "a retry before the claim")
		if attempts, delivered := publisher.snapshot(); attempts != step || len(delivered) != 0 {
			t.Fatalf("after retry %d: %d publishes, %d delivered; want %d refused publishes", step, attempts, len(delivered), step)
		}
		if left := rowsOf(t, pool, record.OutboxKindHookPrompt); left != 1 {
			t.Fatalf("after retry %d: hook-prompt rows left = %d, want the row still waiting", step, left)
		}
	}
	// The woken session holds the role now.
	publisher.setAbsent()
	drainAt(t, runner, 3*time.Minute, "the attempt after the claim")

	attempts, delivered := publisher.snapshot()
	if attempts != 3 || len(delivered) != 1 || delivered[0].topic != topic || delivered[0].message != firedHook.Message("LEGION-208") {
		t.Fatalf("%d publishes delivered %+v, want the prompt delivered once to %s on the third", attempts, delivered, topic)
	}
	if left := rowsOf(t, pool, record.OutboxKindHookPrompt); left != 0 {
		t.Fatalf("hook-prompt rows left = %d, want the row finished", left)
	}
	if posted := issueMessages(t, pool); len(posted) != 0 {
		t.Fatalf("messages on the issue = %q, want none: the prompt was delivered", posted)
	}
	if resumes := podResumes(t, pool, "LEGION-208"); resumes != 1 {
		t.Fatalf("recorded resumes = %d, want the one wake the row's first attempt took", resumes)
	}
}
