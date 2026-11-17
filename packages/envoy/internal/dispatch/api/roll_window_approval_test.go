package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// registerTwoBuilds registers, in the registry the document service reads the fleet from
// (processes.Live), a process of each of two builds, live for the rest of the test, as a roll
// serves them: the document service then keeps every ask block in step with its asks row
// (docs' rollWindowAskAttributes), the release before's second copy of the answer. Both read and
// create this build's schema, as docs' serveTwoBuilds registers them: a process with no schema
// numbers counts as reading version 0 (docs' schemaFleet), and its document sockets would then
// admit a browser read-only, dropping every edit it sends.
func registerTwoBuilds(t *testing.T, database *store.Store) {
	t.Helper()
	if _, err := database.Pool.Exec(context.Background(), `
		insert into server_processes (instance, commit, schema_version, schema_creatable, heartbeat_at)
		values (gen_random_uuid(), 'the-release-before', $1, $2, now() + interval '1 hour'),
		       (gen_random_uuid(), 'this-release', $1, $2, now() + interval '1 hour')
	`, pmdoc.SchemaVersion(), pmdoc.SchemaCreatable()); err != nil {
		t.Fatalf("register a process of each of two builds: %v", err)
	}
}

// requireCarriedTo fails t unless the document reads approved by alice at version number, its
// approval carried from her approval of version from by the server-owned ask attributes' case
// (docs' attributeRemovalCase).
func requireCarriedTo(t *testing.T, handler http.Handler, artifactID string, from, number int, what string) {
	t.Helper()
	read := readApproval(t, handler, artifactID)
	prefix := fmt.Sprintf("Carried forward from the approval of version %d: the server-owned attributes of its ask blocks", from)
	if read.Approval == nil || read.Approval.State != "approved" || read.Approval.LatestVersion != number ||
		read.Approval.Version == nil || *read.Approval.Version != number || read.Approval.By == nil || read.Approval.By.ID != "alice" ||
		read.Approval.Reason == nil || !strings.HasPrefix(*read.Approval.Reason, prefix) {
		t.Fatalf("%s: the spec's approval = %s, want approved by alice at version %d with a reason opening %q", what, describeApproval(read), number, prefix)
	}
}

// While two builds serve, a person's answer is written into the ask's block for the release
// before's pages (the roll window), so the version settlement writes of it differs from the
// approved one in the block's server-owned attributes alone, and the approval is carried to it by
// the case that compares the two without them, which subsumes the answer carry of the release
// before: answering keeps the approval current.
func TestWhileTwoBuildsServeAnAnswerKeepsTheApprovalCurrent(t *testing.T) {
	handler, database, documentService := settlingCarryDocuments(t, time.Hour)
	registerTwoBuilds(t, database)
	ctx := context.Background()
	issue := createInteractionIssue(t, handler, "TEST", "An answer while two builds serve", reopenDecisionSpec)
	spec := issue.PrimaryArtifactID
	settle := func(what string) {
		t.Helper()
		if err := documentService.SettleIssueDocuments(ctx, issue.Key); err != nil {
			t.Fatalf("settle %s: %v", what, err)
		}
		awaitSettled(t, database, spec)
	}
	settle("the spec")
	awaitIndexedAskBlock(t, handler, spec, "reopen-decision", "Ship it?")
	settle("the spec's block written in step")
	approved := latestVersion(t, database, spec).Number
	reviewDocument(t, handler, spec, "alice", "approved", "")

	answerDecisionBlock(t, handler, requireBlockAsk(t, handler, issue.Key, spec, "reopen-decision"), "alice", []string{"Yes"}, "Now it is.", nil)
	settle("the answered spec")
	answered := latestVersion(t, database, spec).Number
	if _, written := documentTexts(t, handler, spec); answered != approved+1 || !strings.Contains(written, `state="answered" answered_by="alice"`) {
		t.Fatalf("the answered spec is at version %d reading:\n%s\nwant version %d holding the answer the roll window wrote", answered, written, approved+1)
	}
	requireCarriedTo(t, handler, spec, approved, answered, "after the answer")
}

// While two builds serve, an issue's close writes each of its asks' blocks resolved and its reopen
// writes them open again, so the versions the settlements of both write differ from the one a
// person approved in the blocks' server-owned attributes alone, and the approval is carried by
// the case that compares them without those attributes, which subsumes the reopen carry of the
// release before: an approval given on the closed issue's version stays current through the
// reopen that restores its ask.
func TestWhileTwoBuildsServeAReopenThatRestoresAnAskKeepsTheApprovalCurrent(t *testing.T) {
	handler, database, documentService := settlingCarryDocuments(t, time.Hour)
	registerTwoBuilds(t, database)
	ctx := context.Background()
	issue := createInteractionIssue(t, handler, "TEST", "A reopen while two builds serve", reopenDecisionSpec)
	spec := issue.PrimaryArtifactID
	settle := func(what string) {
		t.Helper()
		if err := documentService.SettleIssueDocuments(ctx, issue.Key); err != nil {
			t.Fatalf("settle %s: %v", what, err)
		}
		awaitSettled(t, database, spec)
	}
	settle("the spec")
	awaitIndexedAskBlock(t, handler, spec, "reopen-decision", "Ship it?")
	settle("the spec's block written in step")

	closeArtifactIssue(t, handler, issue.Key)
	settle("the closed issue's spec")
	closed := latestVersion(t, database, spec).Number
	if _, written := documentTexts(t, handler, spec); !strings.Contains(written, `#reopen-decision urgency="med" multiple="false" state="resolved"`) {
		t.Fatalf("the closed issue's spec at version %d reads:\n%s\nwant the withdrawn decision resolved", closed, written)
	}
	if reopened := dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+issue.Key, map[string]string{"status": "todo"}, "alice"); reopened.Code != http.StatusOK {
		t.Fatalf("reopen: status=%d body=%s", reopened.Code, reopened.Body.String())
	}
	if review := reviewDocument(t, handler, spec, "alice", "approved", ""); review.Version != closed {
		t.Fatalf("alice's approval reviews version %d, want %d, the closed issue's version", review.Version, closed)
	}
	settle("the reopened issue's spec")
	reopened := latestVersion(t, database, spec).Number
	if _, written := documentTexts(t, handler, spec); reopened != closed+1 || !strings.Contains(written, `#reopen-decision urgency="med" multiple="false" state="open"`) {
		t.Fatalf("the reopened issue's spec is at version %d reading:\n%s\nwant version %d showing the restored decision open", reopened, written, closed+1)
	}
	requireCarriedTo(t, handler, spec, closed, reopened, "after the reopen")
}
