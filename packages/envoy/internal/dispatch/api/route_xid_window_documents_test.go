//go:build !race

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/files/filestest"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// documentRouteXidWindows is the rows of the route window table (route_xid_window_test.go) for the
// routes that write a document: its reviews, its title, its removal, its versions by upload or
// edit, under an open approval request too, and the anchored ask and comment on a project
// document.
var documentRouteXidWindows = []routeXidWindowCase{
	// The review's one write phase is the ledger's batch - the review row and artifact.approved
	// - with everything it is decided on read before it: the document under its owner's
	// advisory lock, its settled version and whether an approval request is open.
	{
		name: "the review route", route: "POST /api/v1/artifacts/{id}/reviews (createArtifactReview)",
		ready: readyArtifactReview, window: storetest.XidBatchedWindow,
	},
	// The review naming the version its caller read shares that handler (recordArtifactReview):
	// the version it names is compared in the read phase, before anything is queued, so its write
	// phase is that same batch.
	{
		name: "the version review route", route: "POST /api/v1/artifacts/{id}/versions/{number}/reviews (createArtifactVersionReview)",
		ready: readyArtifactVersionReview, window: storetest.XidBatchedWindow,
	},
	// Answering the open request adds the ask's answer, its block state, its parts' index and
	// ask.answered to that batch (closeAskTx queues them on the ledger), and nothing to the
	// window.
	{
		name: "the review route answering an approval request", route: "POST /api/v1/artifacts/{id}/reviews (createArtifactReview, closeAskTx)",
		ready: readyReviewAnsweringApprovalRequest, window: storetest.XidBatchedWindow,
	},
	{
		name: "the title correction route", route: "PATCH /api/v1/artifacts/{id} (renameArtifact)",
		ready: readyArtifactRename, window: storetest.XidWindowOf(4,
			"the title's update, the one batch reading the versions and the approval the renamed row "+
				"answers with (loadArtifactDetails), the batch appending artifact.renamed and the COMMIT ending them"),
	},
	// A project document is its own owner: the correction takes the project's upload lock and
	// then the document's owner lock (beginArtifactWrite) before the same writes.
	{
		name: "the title correction route on a project document", route: "PATCH /api/v1/artifacts/{id} on a project document (renameArtifact)",
		ready: readyProjectDocumentRename, window: storetest.XidWindowOf(4,
			"the title's update, the one batch reading the versions and the approval the renamed row "+
				"answers with (loadArtifactDetails), the batch appending artifact.renamed and the COMMIT ending them"),
	},
	{
		name: "the document removal route", route: "DELETE /api/v1/artifacts/{id} (removeArtifact)",
		ready: readyArtifactRemoval, window: storetest.XidWindowOf(16,
			"the eleven deletes of the document's rows in other tables - its versions, updates, snapshots, "+
				"checkpoints, search row, compaction debt, search-term queue, threads, "+
				"references, grammar debt and excerpt backoff - the node write stating the document's node gone, "+
				"the delete of its own row, the dispatch_doc_removed notify, the batch appending artifact.removed "+
				"and the gone statement it causes, and the COMMIT ending them"),
	},
	{
		name: "the project file upload route", route: "POST /api/v1/projects/{key}/artifacts on a file it has (storeArtifact)",
		ready: readyProjectFileUpload, window: storetest.XidBatchedWindow,
	},
	{
		name: "the project document upload route", route: "POST /api/v1/projects/{key}/artifacts on a document it has (storeArtifact)",
		ready: readyProjectDocumentUpload, window: storetest.XidBatchedWindow,
	},
	// A version written under an open approval request moves the request to it
	// (docs.MoveApprovalAsk): the rewording's reads - its references' plan, its parts' plan and
	// its edit time (docs.RewriteApprovalAsk) - run in the read phase, and its update, index,
	// parts, ask.edited and stamp join the version's batch. The upload writes its version itself
	// (storeArtifact, then MoveApprovalAsk); the edit writes it through the document service
	// (docs' writeVersionTx, then MoveApprovalAsk). The spec is a document the spec query's record
	// covers, so the version's version_renders row is filled once the batch has run: the record
	// is the walk of the store as the batch leaves it (R14, R15), and so stands in the window
	// (specVersionWindow). Its approval advice is read in the read phase.
	{
		name: "the spec upload under an open approval request", route: "POST /api/v1/issues/{key}/artifacts under an open approval request (storeArtifact, docs.MoveApprovalAsk)",
		ready: readyUploadUnderApprovalRequest, window: specVersionWindow,
	},
	{
		name: "the spec edit under an open approval request", route: "POST /api/v1/artifacts/{id}/edits under an open approval request (editArtifact, docs' writeVersionTx, MoveApprovalAsk)",
		ready: readyEditUnderApprovalRequest, window: specVersionWindow,
	},
	// A version that restores the approved text under an open request moves the request and
	// then answers it in the approver's name (docs' writeCarriedReview, answerApprovalAsk): the
	// answer's reads - the transaction's time, its parts' plan - run in the read phase beside the
	// move's, and its update, parts, ask.answered and stamp, with the carried review and its
	// artifact.approved, join the version's batch, from the version's insert on behind the
	// savepoint the write phase takes for the restore's carry (docs' writeRestored), and the fill
	// holds the pin to the record's digest (specRestoreWindow).
	{
		name: "the spec edit restoring the approved text under an open approval request", route: "POST /api/v1/artifacts/{id}/edits restoring the approved text (docs' carryApproval, writeCarriedReview, answerApprovalAsk)",
		ready: readyEditRestoringTheApprovedText, window: specRestoreWindow,
	},
	{
		name: "the spec upload restoring the approved text under an open approval request", route: "POST /api/v1/issues/{key}/artifacts restoring the approved text (docs.CarryUploadedApproval, writeCarriedReview, answerApprovalAsk)",
		ready: readyUploadRestoringTheApprovedText, window: specRestoreWindow,
	},
	{
		name: "the agent conversation upload route", route: "POST /api/v1/agents/{session_id}/artifacts (uploadAgentArtifact, storeArtifact)",
		ready: readyAgentArtifactUpload, window: storetest.XidBatchedWindow,
	},
	{
		name: "the document ask route", route: "POST /api/v1/artifacts/{id}/asks (createArtifactAsk, createAskFor)",
		ready: readyDocumentAsk, window: storetest.XidBatchedWindow,
	},
	{
		name: "the document comment route", route: "POST /api/v1/artifacts/{id}/comments (createArtifactComment, createCommentFor)",
		ready: readyDocumentComment, window: storetest.XidBatchedWindow,
	},
	// A Verification past the bound leaves its blocks to the reconciliation and writes its checks,
	// with their rows, their serves and their ancestors, in its own transaction (docs'
	// planTypedPasses): every read of that pass runs in the read phase, and its writes join the
	// write phase's one batch. The table's other uploads hold storetest.XidSpec's 40 KB document,
	// which cannot pass 2,000 blocks, so these two rows are the ones that run the pass. On an issue
	// that has not converted the issue is created beside a process of the release before
	// (holdIssuesUnconverted), which registers this build's owner lock protocol, so the row measures
	// this build's owner locks.
	{
		name: "the Verification upload above the bound on an issue that has not converted", route: "POST /api/v1/issues/{key}/artifacts past the bound, an issue that has not converted (storeArtifact, docs' indexDeferredVersion, planTypedPasses)",
		ready: readyVerificationUploadAboveTheBound(false), window: storetest.XidBatchedWindow,
	},
	{
		name: "the Verification upload above the bound on a converted issue", route: "POST /api/v1/issues/{key}/artifacts past the bound, a converted issue (storeArtifact, docs' indexDocumentInRoom, indexDeferredVersion, planTypedPasses)",
		ready: readyVerificationUploadAboveTheBound(true), window: storetest.XidBatchedWindow,
	},
}

// specFill names what the fill of a spec version's version_renders row runs once the write phase's
// batch has run (docs' writeStatedRenders): it reads the record of the spec's query at one cut, by
// the walk of the issue's tree as the batch left the store (R14, R15) - the cut, the five reads of
// the issue's conversation (its items, the asks the conversation test passes, their blocks, and the
// ask and its blocks as their loaders read them), the read of the items' rows, the one read of the
// tree, every level of it (refs.TreeSubtrees), the read of the issues above the issue and that of
// the issue a view shows. The spec query takes none of the fixture's blocks, so the record reads no
// node's values or relations.
const specFill = "the fill, which reads the spec query's record of the issue at one cut once the batch has run (R14, R15): " +
	"the cut, the five reads of the issue's conversation - its items, the asks the conversation test passes, their blocks, and the ask and its blocks as their loaders read them - " +
	"the read of the items' rows, the one read of the tree, every level of it, the read of the issues above the issue and that of the issue a view shows"

// specVersionWindow is the window of a version of the spec under an open approval request: the
// write phase's batch, the fill (specFill), the version_renders row it writes (R15) and the
// COMMIT.
var specVersionWindow = storetest.XidWindowOf(13,
	"the write phase's one batch, "+specFill+", the version_renders row it writes with the record's digest (R15), and the COMMIT ending them")

// specRestoreWindow is the window of a version of the spec restoring the approved text under an
// open approval request (docs' writeRestored): the batch up to the version's insert, the savepoint
// taken before it, the batch from it on - the version's version_renders row pinned to the
// approval's digest, the carried review and its artifact.approved, the request's answer and the
// version's artifact.version - the fill (specFill), the update giving the pinned row its cut, the
// savepoint's release, the batch of the settlement credit's statements and the COMMIT.
var specRestoreWindow = storetest.XidWindowOf(17,
	"the write phase's batch up to the restored version's insert, the savepoint the write phase takes before that insert (writeRestored), "+
		"the batch from the insert on - the version's version_renders row pinned to the approval's digest, the carried review, its artifact.approved and the request's answer before the version's artifact.version - "+
		specFill+", the update giving the pinned version_renders row the cut (R15), "+
		"the release of the savepoint where the fill found the approval's digest (a rollback to it where it did not, which writes the version again with no carry), "+
		"the batch of the settlement credit's statements, and the COMMIT ending them")

// readyReviewAnsweringApprovalRequest drives POST /api/v1/artifacts/{id}/reviews on a spec with
// an approval request open, so the person's review answers that request (closeAskTx) and
// reviews the document at the version the request named.
func readyReviewAnsweringApprovalRequest(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	fixture := base.issue(t, "Review answering a request")
	waitForArtifactVersion(t, fixture.handler, fixture.artifactID, 1)
	requested := sessionRequest(t, fixture.handler, http.MethodPost, "/api/v1/artifacts/"+fixture.artifactID+"/approval-requests", map[string]any{
		"summary": "The migration section is rewritten; the rest of the spec stands.", "actor": sessionActor(),
	})
	if requested.Code != http.StatusCreated {
		t.Fatalf("open the approval request: status=%d body=%s", requested.Code, requested.Body.String())
	}
	request := decodeBody[approvalRequestAsk](t, requested)
	if request.Ask == nil {
		t.Fatalf("the approval request opened no ask: %s", requested.Body.String())
	}
	return fixture.routeWrite(t, "the review answering the open request",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return dispatchRequest(t, handler, http.MethodPost,
				"/api/v1/artifacts/"+fixture.artifactID+"/reviews", map[string]any{"state": "approved"}, "alice")
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusCreated {
				t.Fatalf("review the spec: status=%d body=%s", answer.Code, answer.Body.String())
			}
			if review := decodeBody[model.ArtifactReview](t, answer); review.AskID == nil || *review.AskID != request.Ask.ID {
				t.Fatalf("the review = %#v, want the one answering approval request %s", review, request.Ask.ID)
			}
		})
}

// readyProjectDocumentUpload drives POST /api/v1/projects/{key}/artifacts naming a document the
// project already has, the harness's large spec, which writes its next version through the
// document service (storeArtifact).
func readyProjectDocumentUpload(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	fixture := base.issue(t, "Project document upload")
	document := createProjectDocument(t, fixture.handler, "LOCK", "Design notes", base.spec)
	waitForArtifactVersion(t, fixture.handler, document.ID, 1)
	revised := strings.Replace(base.spec, "Revision token r0", "Revision token r1", 1)
	// The upload replaces the document, so it names the token of the version it replaces.
	replaced := uploadBase(t, fixture.handler, document.ID)
	return fixture.routeWrite(t, "the document's next version",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects/LOCK/artifacts", map[string]any{
				"name": "Design notes", "content": revised, "precondition": replaced,
			}, "alice")
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusCreated {
				t.Fatalf("upload the document again: status=%d body=%s", answer.Code, answer.Body.String())
			}
			if uploaded := decodeBody[uploadedArtifact](t, answer); uploaded.Artifact.ID != document.ID || uploaded.Version.Number != 2 {
				t.Fatalf("the upload wrote version %d of %s, want version 2 of %s",
					uploaded.Version.Number, uploaded.Artifact.ID, document.ID)
			}
		})
}

// readyAgentArtifactUpload drives POST /api/v1/agents/{session_id}/artifacts naming a file the
// conversation already has, which writes its next version under the conversation's lock
// (uploadAgentArtifact, storeArtifact).
func readyAgentArtifactUpload(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	base.options.files = filestest.NewMemory()
	fixture := base.issue(t, "Agent conversation upload")
	target := "/api/v1/agents/" + agentSession + "/artifacts"
	first := multipartRequest(t, fixture.handler, target, map[string]string{"name": "shot.png"},
		"shot.png", "image/png", pngBytes("first"), "alice")
	if first.Code != http.StatusCreated {
		t.Fatalf("upload the conversation's file: status=%d body=%s", first.Code, first.Body.String())
	}
	file := decodeBody[uploadedArtifact](t, first).Artifact
	return fixture.routeWrite(t, "the file's next version",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return multipartRequest(t, handler, target, map[string]string{"name": "shot.png"},
				"shot.png", "image/png", pngBytes("second"), "alice")
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusCreated {
				t.Fatalf("upload the file again: status=%d body=%s", answer.Code, answer.Body.String())
			}
			if uploaded := decodeBody[uploadedArtifact](t, answer); uploaded.Artifact.ID != file.ID || uploaded.Version.Number != 2 {
				t.Fatalf("the upload wrote version %d of %s, want version 2 of %s",
					uploaded.Version.Number, uploaded.Artifact.ID, file.ID)
			}
		})
}

// readyDocumentAsk drives POST /api/v1/artifacts/{id}/asks, a session's anchored ask on a
// project document (createArtifactAsk, createAskFor).
func readyDocumentAsk(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	fixture := base.issue(t, "Document ask")
	document := projectDesignNotes(t, fixture, base.spec)
	_, quotes := xidSpec(1)
	return fixture.routeWrite(t, "the document's ask",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+document.ID+"/asks", map[string]any{
				"question": "Which team signs off on this requirement?",
				"options":  []map[string]string{{"label": "Platform"}, {"label": "Product"}},
				"anchor":   map[string]any{"artifact": document.Slug, "quote": quotes[0]}, "actor": sessionActor(),
			})
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusCreated {
				t.Fatalf("ask on the document: status=%d body=%s", answer.Code, answer.Body.String())
			}
		})
}

// readyDocumentComment drives POST /api/v1/artifacts/{id}/comments, a session's anchored comment
// on a project document (createArtifactComment, createCommentFor).
func readyDocumentComment(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	fixture := base.issue(t, "Document comment")
	document := projectDesignNotes(t, fixture, base.spec)
	_, quotes := xidSpec(1)
	return fixture.routeWrite(t, "the document's comment",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+document.ID+"/comments", map[string]any{
				"body":   "Does this still hold once the outbox is sharded?",
				"anchor": map[string]any{"artifact": document.Slug, "quote": quotes[0]}, "actor": sessionActor(),
			})
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusCreated {
				t.Fatalf("comment on the document: status=%d body=%s", answer.Code, answer.Body.String())
			}
		})
}

// requireApprovalRequestMovedTo2 fails t unless the approval request askID is still open, names
// version 2 of its document - the version the measured write wrote - and still names version 1 as
// the one its agent handed a person, reworded by the move (edited_at).
func requireApprovalRequestMovedTo2(t *testing.T, fixture itemRowFixture, askID string) {
	t.Helper()
	if moved := fixture.rowCount(t, `
		select count(*) from asks
		where id = $1 and state = 'open' and edited_at is not null
			and (approval->>'version')::int = 2 and (approval->>'requested_version')::int = 1
	`, askID); moved != 1 {
		t.Fatalf("approval request %s was not moved to version 2 by the write: %#v", askID, readBlockAsk(t, fixture.handler, askID))
	}
}

// readyUploadUnderApprovalRequest drives POST /api/v1/issues/{key}/artifacts replacing the spec
// while an approval request is open on it, so the version the upload writes moves the request
// (storeArtifact, docs.MoveApprovalAsk, RewriteApprovalAsk).
func readyUploadUnderApprovalRequest(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	fixture := base.issue(t, "Upload under a request")
	waitForArtifactVersion(t, fixture.handler, fixture.artifactID, 1)
	request := requestApproval(t, fixture.handler, fixture.artifactID)
	revised := strings.Replace(base.spec, "Revision token r0", "Revision token r1", 1)
	// The upload replaces the spec, so it names the token of the version it replaces.
	replaced := uploadBase(t, fixture.handler, fixture.artifactID)
	return fixture.routeWrite(t, "the spec's next version and the request it moves",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return sessionRequest(t, handler, http.MethodPost, "/api/v1/issues/"+fixture.issueKey+"/artifacts", map[string]any{
				"name": "spec.md", "content": revised, "summary": "Bump the revision", "actor": sessionActor(),
				"precondition": replaced,
			})
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusCreated {
				t.Fatalf("upload the spec under the open request: status=%d body=%s", answer.Code, answer.Body.String())
			}
			requireApprovalRequestMovedTo2(t, fixture, request.ID)
		})
}

// readyEditUnderApprovalRequest drives POST /api/v1/artifacts/{id}/edits on the spec while an
// approval request is open on it: the edit versions the document in its own transaction through
// the document service, whose version write moves the request (docs' writeVersionTx,
// MoveApprovalAsk, RewriteApprovalAsk).
func readyEditUnderApprovalRequest(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	fixture := base.issue(t, "Edit under a request")
	waitForArtifactVersion(t, fixture.handler, fixture.artifactID, 1)
	request := requestApproval(t, fixture.handler, fixture.artifactID)
	return fixture.routeWrite(t, "the spec's next version and the request it moves",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+fixture.artifactID+"/edits", map[string]any{
				"ops": []map[string]string{replaceOp("Revision token r0", "Revision token r1")}, "actor": sessionActor(),
			})
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusOK {
				t.Fatalf("edit the spec under the open request: status=%d body=%s", answer.Code, answer.Body.String())
			}
			requireApprovalRequestMovedTo2(t, fixture, request.ID)
		})
}

// approvedThenRequested has alice approve the fixture's spec at version 1, the agent edit it to
// version 2, which stales that approval, and the agent open an approval request at version 2,
// which follows the approval of version 1. It answers the request's ask.
func approvedThenRequested(t *testing.T, fixture itemRowFixture) carriedAsk {
	t.Helper()
	waitForArtifactVersion(t, fixture.handler, fixture.artifactID, 1)
	if review := reviewDocument(t, fixture.handler, fixture.artifactID, "alice", "approved", ""); review.Version != 1 {
		t.Fatalf("review = %#v, want an approval of version 1", review)
	}
	reviseDocument(t, fixture.handler, fixture.artifactID, "Revision token r0", "Revision token r1", 2)
	request := requestApproval(t, fixture.handler, fixture.artifactID)
	if request.Approval.Version != 2 || request.Approval.Approved == nil || request.Approval.Approved.Version != 1 {
		t.Fatalf("request = %#v, want one at version 2 following the approval of version 1", request.Approval)
	}
	return request
}

// requireRequestAnsweredByTheCarry fails t unless the request is answered Approve in alice's name
// at version 3, the version the measured write restored, and that version carries her approval
// of version 1 against the request.
func requireRequestAnsweredByTheCarry(t *testing.T, fixture itemRowFixture, askID string) {
	t.Helper()
	waitForArtifactVersion(t, fixture.handler, fixture.artifactID, 3)
	requireReviewCarriedWith(t, fixture.handler, fixture.artifactID, 3, 1, restoreSentence(1), &askID)
	if answered := readBlockAsk(t, fixture.handler, askID); answered.State != "answered" || answered.Answer == nil ||
		answered.Answer.User != "alice" || answered.Approval == nil || answered.Approval.Version != 3 {
		t.Fatalf("the request after the restore = state %q answer %#v approval %#v, want Approve by alice at version 3",
			answered.State, answered.Answer, answered.Approval)
	}
}

// readyEditRestoringTheApprovedText drives POST /api/v1/artifacts/{id}/edits undoing the agent's
// edit under an open approval request: the version holds the approved text again, so the
// document service's version write moves the request to it and then carries alice's approval,
// which answers the request (docs' writeVersionTx, carryApproval, writeCarriedReview,
// answerApprovalAsk).
func readyEditRestoringTheApprovedText(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	fixture := base.issue(t, "Edit restoring the approved text")
	request := approvedThenRequested(t, fixture)
	return fixture.routeWrite(t, "the restoring version and the request its carry answers",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+fixture.artifactID+"/edits", map[string]any{
				"ops": []map[string]string{replaceOp("Revision token r1", "Revision token r0")}, "actor": sessionActor(),
			})
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusOK {
				t.Fatalf("undo the edit under the open request: status=%d body=%s", answer.Code, answer.Body.String())
			}
			requireRequestAnsweredByTheCarry(t, fixture, request.ID)
		})
}

// readyUploadRestoringTheApprovedText drives POST /api/v1/issues/{key}/artifacts uploading the
// approved version's text under an open approval request: the upload moves the request to its
// version and then carries alice's approval, which answers the request
// (docs.CarryUploadedApproval, writeCarriedReview, answerApprovalAsk).
func readyUploadRestoringTheApprovedText(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	fixture := base.issue(t, "Upload restoring the approved text")
	request := approvedThenRequested(t, fixture)
	approved := versionMarkdown(t, fixture.handler, fixture.artifactID, 1)
	// The upload replaces the spec, so it names the token of the version it replaces.
	replaced := uploadBase(t, fixture.handler, fixture.artifactID)
	return fixture.routeWrite(t, "the restoring version and the request its carry answers",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return sessionRequest(t, handler, http.MethodPost, "/api/v1/issues/"+fixture.issueKey+"/artifacts", map[string]any{
				"name": "spec.md", "content": approved, "actor": sessionActor(), "precondition": replaced,
			})
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusCreated {
				t.Fatalf("upload the approved text under the open request: status=%d body=%s", answer.Code, answer.Body.String())
			}
			requireRequestAnsweredByTheCarry(t, fixture, request.ID)
		})
}

// readyVerificationUploadAboveTheBound readies POST /api/v1/issues/{key}/artifacts uploading the
// issue's first Verification document, a list past the bound and 103 checks serving the ten
// requirements of its spec, on an issue born converted or, where not converted, created beside a
// process of the release before (holdIssuesUnconverted): the upload leaves its blocks to the
// block-index reconciliation and writes its checks at once (docs' planTypedPasses).
func readyVerificationUploadAboveTheBound(converted bool) func(*testing.T, routeFixture) lockedItemWrite {
	return func(t *testing.T, base routeFixture) lockedItemWrite {
		t.Helper()
		handler, database, deps := newTestServer(t, base.options)
		if !converted {
			holdIssuesUnconverted(t, database)
		}
		requirements := make([]acceptedBlock, 10)
		for index := range requirements {
			requirements[index] = acceptedRequirement(fmt.Sprintf("r%d", index+1))
		}
		issue := createInteractionIssue(t, handler, "LOCK", "Verification upload above the bound", base.spec+"\n"+typedDocument("", false, requirements...))
		if tree, err := issueTreeOf(context.Background(), database.Pool, issue.Key); err != nil || (tree != "") != converted {
			t.Fatalf("issue %s holds the tree %q (%v), want it converted %t", issue.Key, tree, err, converted)
		}
		checks := make([]acceptedBlock, 103)
		for index := range checks {
			checks[index] = acceptedCheck(fmt.Sprintf("c%d", index+1), fmt.Sprintf("r%d", index%10+1))
		}
		content := typedDocument(issue.Key, true, checks...)
		fixture := itemRowFixture{handler: handler, database: database, deps: deps, issueKey: issue.Key, artifactID: issue.PrimaryArtifactID}
		return fixture.routeWrite(t, "the Verification's first version and its checks",
			func(handler http.Handler) *httptest.ResponseRecorder {
				return dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/artifacts",
					map[string]any{"name": "verification.md", "content": content}, "alice")
			},
			func(t *testing.T, answer *httptest.ResponseRecorder) {
				if answer.Code != http.StatusCreated {
					t.Fatalf("upload the Verification: status=%d body=%s", answer.Code, answer.Body.String())
				}
				verification := decodeBody[uploadedArtifact](t, answer).Artifact.ID
				if indexed := blockIndexVersion(t, database, verification); indexed != nil {
					t.Fatalf("the upload indexed the Verification at %d in its own request, want its blocks left to the reconciliation", *indexed)
				}
				if held := readHeldTypedBlock(t, database, blockKeyOf(t, database, verification)+"#c103"); held.state != graphstore.StateLive || held.rows != 1 || held.serves != 1 {
					t.Fatalf("the upload left its check c103 as %+v, want it written at once with its row and its serves", held)
				}
			})
	}
}
