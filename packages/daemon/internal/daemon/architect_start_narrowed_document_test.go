package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/texts"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// Main's start task says where a root's gate stands step by step, "its spec is registered at
// version %d and a person has approved that version" (workflow/architect_start.go:gateStanding at
// 2f7ee6dc); the change gives each step's document (`document`), for a carried gate the old root's,
// named from Dispatch where the record cannot name its issue. The architect ruled that the start
// task reads Dispatch when the outbox runs the start row, and that the architect-root prompt must
// say so: the document is Dispatch's answer when the task was queued, or the bare reference where
// Dispatch did not answer then. The head's prompt (go/architect-root.md) says: "Where neither the
// daemon's record nor Dispatch, which the daemon asks when it queues the task, said which issue
// holds a registered step's document, `document` is instead that document's bare artifact
// reference ... The daemon does not ask Dispatch again when it sends you the task it queued, so
// the reference reaches you as it was queued, unless the daemon writes the task again at that send
// because a decision the task named has left your tree, and then it asks Dispatch at that send."
//
// The daemon's rootTask writes a queued task again at its send wherever a decision the task named
// is no longer held by its delivery, and a newer decision a person made on the same reference is
// one such (record.Store.KeepDecision clears the hold): that decision is still on an issue of the
// root's own tree, so nothing left the tree, yet the rewrite asks Dispatch at the send. Here the
// task was queued while Dispatch did not answer, so it gave the bare reference; at the send a
// newer decision has replaced one the task named, and Dispatch answers. The architect receives the
// old root's document as Dispatch named it at the send, which the prompt says it does not.
//
// INFRA-3686's R2: a text's fields are "the references (a `dispatch://` link for each Dispatch
// item), the versions and the state that the reader acts on that its event has", and the reader's
// prompt gives the steps and explanations the text does not; a passage that states when the
// reference was read must state it truly for every input.
func TestAHeldStartTaskNarrowedForAReplacedDecisionGivesItsDocumentAsItWasQueued(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	const key, oldRoot, artifact = "LEGION-312", "LEGION-208", "5b0c9d1e-7a3f-4e26-9c8b-1f2d3e4a5b6c"
	steps := []record.DesignStep{record.DesignStepSpec}
	bare := texts.ArtifactReference(artifact)

	// A root a hand-over created at generation 2 with its old root's gate, whose spec row records
	// no document issue and stands alone on its artifact, so the record cannot name the document's
	// issue (workflow's stepDocuments).
	putOutboxIssue(t, pool, records, record.Issue{Key: key, Repo: "acme/widgets", DefaultBranch: "main",
		GitHubRepo: "acme/widgets", Project: heldTaskProject, Tree: key, Title: "Carried", Phase: phase.Admitted,
		Generation: 2, Status: "in_progress", Rank: "A", HandedOver: true, LastDispatchSeq: 1})
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return records.PutHandedOverRoot(ctx, tx, record.HandedOverRoot{Issue: key, Generation: 2, GateCarried: true})
	}); err != nil {
		t.Fatalf("record %s as a root a hand-over created at generation 2 with its gate: %v", key, err)
	}
	if _, err := pool.Exec(ctx, `insert into design_gates (issue, step, artifact_id, latest_version, approved_version, ever_opened)
		values ($1, $2, $3, 2, 1, false)`, key, string(record.DesignStepSpec), artifact); err != nil {
		t.Fatalf("write the carried spec row of %s: %v", key, err)
	}
	root := readIssue(t, pool, key)
	if root == nil {
		t.Fatalf("%s is not recorded", key)
	}

	// The task names two decisions on asks of the root, the first held by the task's delivery; a
	// newer decision on the second's ask has replaced it since, so the delivery holds it no longer.
	token := mustClaimToken(t, key, claim.RoleArchitect)
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	kept := record.DecisionMark{Issue: key, Ref: "dispatch://" + key + "/ask/ask-0001", Decision: record.DecisionAskAnswered,
		Actor: "user:alice", At: at}
	replaced := record.DecisionMark{Issue: key, Ref: "dispatch://" + key + "/ask/ask-0002", Decision: record.DecisionAskAnswered,
		Actor: "user:alice", At: at.Add(time.Minute)}
	named := record.KeptDecisions{Decisions: []record.DecisionMark{kept, replaced}}
	if _, err := pool.Exec(ctx, `insert into parked_decisions (issue, ref, decision, actor, at, held_by_claim, held_by_delivery)
		values ($1, $2, $3, $4, $5, $6, 'outbox:7'), ($1, $7, $3, $4, $8, null, null)`,
		key, kept.Ref, string(kept.Decision), kept.Actor, kept.At, string(token), replaced.Ref, at.Add(time.Hour)); err != nil {
		t.Fatalf("keep the decisions of %s, the first held by the architect's task: %v", key, err)
	}
	encoded, err := json.Marshal(named)
	if err != nil {
		t.Fatalf("encode what the held task names: %v", err)
	}

	// The task as the outbox queued it, when Dispatch did not answer: the spec's document is the
	// bare artifact reference.
	var queued string
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		start, err := workflow.ArchitectStartOf(ctx, tx, records, steps, *root)
		if err != nil {
			return err
		}
		queued = workflow.NameStartDocuments(workflow.NarrowRootTask(start, 1, named), func(string) (string, bool) { return "", false })
		return nil
	}); err != nil {
		t.Fatalf("derive the start task of %s: %v", key, err)
	}
	if !strings.Contains(queued, ","+bare+",") || !strings.Contains(queued, replaced.Ref) {
		t.Fatalf("the queued start task:\n%s\nwant its spec row to give the bare reference %s and the task to name %s: this case proves nothing", queued, bare, replaced.Ref)
	}

	// The claim sends the task it queued, and Dispatch now answers which issue holds the artifact.
	w := &workflowRuntime{pool: pool, records: records, gatedSteps: steps, log: quietLogger(),
		dispatch: &documentHolders{holds: map[string]string{artifact: oldRoot}}}
	sent, _, _, err := w.rootTask(ctx, supervise.Claim{Token: token, Project: "legion", Tree: key, Issue: key, Role: claim.RoleArchitect},
		supervise.Delivery{ID: "outbox:7", Task: queued, Kept: encoded, Entry: 1})
	if err != nil {
		t.Fatalf("send the held task of %s: %v", key, err)
	}
	if strings.Contains(sent, replaced.Ref) || !strings.Contains(sent, kept.Ref) {
		t.Fatalf("the sent task:\n%s\nwant it to name %s and no longer %s, which a newer decision replaced: this case proves nothing", sent, kept.Ref, replaced.Ref)
	}
	if issue := readIssue(t, pool, kept.Issue); issue == nil || issue.Tree != key {
		t.Fatalf("the decision the task still names is on %+v, want an issue of %s's tree: this case proves nothing", issue, key)
	}

	prompt, err := os.ReadFile(filepath.Join("..", "prompts", "go", "architect-root.md"))
	if err != nil {
		t.Fatal(err)
	}
	const claimed = "so the reference reaches you as it was queued, unless the daemon writes the task again at that send because a decision the task named has left your tree"
	if !strings.Contains(string(prompt), claimed) {
		t.Fatalf("go/architect-root.md no longer says %q: this case proves nothing", claimed)
	}
	if !strings.Contains(sent, ","+bare+",") {
		t.Errorf("the sent start task:\n%s\ngives the spec step's document as Dispatch named it at the send, not as it was queued (%s): "+
			"no decision the task named left %s's tree, a newer decision replaced one on the same ask, and go/architect-root.md says %q",
			sent, bare, key, claimed)
	}
}
