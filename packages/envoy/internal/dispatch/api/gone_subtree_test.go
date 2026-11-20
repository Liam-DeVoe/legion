package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// storedStateOf is the state the store holds for the node address names, failing where it holds
// no row of it.
func storedStateOf(t *testing.T, rig reasoningRig, address string) string {
	t.Helper()
	kind, id, ok := decodeNodeAddress(address)
	if !ok {
		t.Fatalf("decode the node address %s", address)
	}
	var state string
	if err := rig.database.Pool.QueryRow(context.Background(), `select state from graph_nodes where kind = $1 and id = $2`,
		refs.StoredKindOf(kind), id).Scan(&state); err != nil {
		t.Fatalf("read the stored state of %s: %v", address, err)
	}
	return state
}

// A node taken to gone takes its whole part_of subtree with it, transitively and in one write,
// whatever the kind of each node under it and whoever may see it (the architect's ruling of
// 2026-11-20 on INFRA-1950's R5): a lens invoked on a lens and one invoked on a finding hang under
// that lens or finding through part_of (R6), so a person's gone on the first lens takes its
// finding, the lens on it with its finding, the lens on its finding with that lens's finding, and a
// lens another person keeps for themselves under it. A risk and a constraint taken to gone take the
// lens invoked on each. A move of a finding, a risk or a constraint into gone is a person's alone,
// as a lens's is (the ruling of 2026-11-20 07:15Z): the node's own author, proved by a token
// without the person scope, is refused 403 STATE_NOT_PERMITTED naming the node and marks it
// overtaken instead.
func TestANodeTakenGoneTakesItsWholePartOfSubtree(t *testing.T) {
	rig := newReasoningRig(t)
	first := rig.invoke(rig.spec, "trade-off", "alice", map[string]any{"findings": []string{"The first lens's finding."}})
	finding := first.Findings[0].Address
	onLens := rig.invoke(first.Address, "trade-off", "alice", map[string]any{"findings": []string{"Said of the first lens."}})
	onFinding := rig.invoke(finding, "trade-off", "alice", map[string]any{"findings": []string{"Said of the first lens's finding."}})
	hidden := rig.invoke(first.Address, "trade-off", "bob", map[string]any{
		"visibility": graphstore.VisibilityAuthor, "findings": []string{"Bob keeps this one."}})

	rig.writeState(first.Address, graphstore.StateGone, "alice")
	under := map[string]string{
		"the first lens's finding":                    finding,
		"the lens on the first lens":                  onLens.Address,
		"the finding of the lens on the first lens":   onLens.Findings[0].Address,
		"the lens on the first lens's finding":        onFinding.Address,
		"that lens's own finding":                     onFinding.Findings[0].Address,
		"bob's author-only lens on the first lens":    hidden.Address,
		"the finding of bob's author-only lens on it": hidden.Findings[0].Address,
	}
	for name, address := range under {
		if state := storedStateOf(t, rig, address); state != graphstore.StateGone {
			t.Errorf("after alice took the lens %s to gone, %s %s is %s, want gone with it", first.Address, name, address, state)
		}
	}

	premortem := rig.invoke(rig.spec, graphstore.LensKindPreMortem, "alice", nil)
	riskWrite := rig.post("/risks", map[string]any{
		"lens": premortem.Address, "heading": "The cache goes cold.", "early_warning": "Misses climb.", "prevention": "Warm it first.",
	}, "alice")
	if riskWrite.Code != http.StatusCreated {
		t.Fatalf("write the risk: status=%d body=%s", riskWrite.Code, riskWrite.Body.String())
	}
	risk := decodeBody[riskWritten](t, riskWrite)
	onRisk := rig.invoke(risk.Address, "trade-off", "alice", map[string]any{"findings": []string{"Said of the risk."}})
	constraintWrite := rig.post("/constraints", map[string]any{"standard": "RFC 9110", "reference": "section 9.2.2"}, "alice")
	if constraintWrite.Code != http.StatusCreated {
		t.Fatalf("write the constraint: status=%d body=%s", constraintWrite.Code, constraintWrite.Body.String())
	}
	constraint := decodeBody[riskWritten](t, constraintWrite).Address
	onConstraint := rig.invoke(constraint, "trade-off", "alice", nil)

	rig.writeState(risk.Address, graphstore.StateGone, "alice")
	rig.writeState(constraint, graphstore.StateGone, "alice")
	for name, address := range map[string]string{
		"the lens on the risk": onRisk.Address, "that lens's finding": onRisk.Findings[0].Address,
		"the lens on the constraint": onConstraint.Address,
	} {
		if state := storedStateOf(t, rig, address); state != graphstore.StateGone {
			t.Errorf("after alice took its parent to gone, %s %s is %s, want gone with it", name, address, state)
		}
	}

	// alice's token without the person scope proves her, the author of what it writes, and is no person.
	token := personalTokenOwnedBy(t, rig.database, "alice")
	asAgent := func(path string, body map[string]any) *httptest.ResponseRecorder {
		body["actor"] = map[string]any{"kind": "session", "id": "ses_alice_gone_agent"}
		response := httptest.NewRecorder()
		rig.handler.ServeHTTP(response, brokerRequest(t, http.MethodPost, path, body, "Bearer "+token, ""))
		return response
	}
	issuePath := "/api/v1/issues/" + rig.issue
	ownRisk := asAgent(issuePath+"/risks", map[string]any{
		"lens": premortem.Address, "heading": "A job runs twice.", "early_warning": "Two drainers.", "prevention": "One drainer.",
	})
	ownConstraint := asAgent(issuePath+"/constraints", map[string]any{"standard": "RFC 9111", "reference": "section 4"})
	ownLens := asAgent(issuePath+"/lenses", map[string]any{"on": rig.spec, "kind": "trade-off", "findings": []string{"The agent's own finding."}})
	if ownLens.Code != http.StatusCreated {
		t.Fatalf("the agent invokes its own lens: status=%d body=%s", ownLens.Code, ownLens.Body.String())
	}
	ownFinding := decodeBody[lensInvoked](t, ownLens).Findings[0].Address
	written := map[string]*httptest.ResponseRecorder{"risk": ownRisk, "constraint": ownConstraint}
	for name, response := range written {
		if response.Code != http.StatusCreated {
			t.Fatalf("the agent writes its own %s: status=%d body=%s", name, response.Code, response.Body.String())
		}
	}
	for name, address := range map[string]string{
		"risk": decodeBody[riskWritten](t, ownRisk).Address, "constraint": decodeBody[riskWritten](t, ownConstraint).Address,
		"finding": ownFinding,
	} {
		refused := asAgent(statePath(rig.issue, address, "state"), map[string]any{"state": graphstore.StateGone})
		if refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), codeStateNotPermitted) ||
			!strings.Contains(refused.Body.String(), address) {
			t.Errorf("the agent's gone on its own %s %s answers %d %s, want 403 %s naming it: a person alone takes a %s to gone",
				name, address, refused.Code, refused.Body.String(), codeStateNotPermitted, name)
		}
		if state := storedStateOf(t, rig, address); state != graphstore.StateLive {
			t.Errorf("after the agent's gone on its own %s %s, it is %s, want the live it held", name, address, state)
		}
		if overtaken := asAgent(statePath(rig.issue, address, "state"), map[string]any{"state": graphstore.StateOvertaken}); overtaken.Code != http.StatusOK {
			t.Errorf("the agent marks its own %s %s overtaken: status=%d body=%s, want 200", name, address, overtaken.Code, overtaken.Body.String())
		}
	}
}

// A lens on a block that a version removes, or on a document that is retired, or on a block of
// that document, stays live, and neither read answers it a null `on` or parent (spec v59 R5): GET
// /api/v1/lenses answers its `on` as the block's address at the last version that held it,
// `dispatch://KEY/artifact/<slug>#b-<id>@v<N>`, or as the retired document's at its last version,
// `dispatch://KEY/artifact/<slug>@v<N>`, and GET /api/v1/references answers the node's `parent`
// naming that block or document with `parent_version` N.
func TestALensOnARemovedBlockOrARetiredDocumentNamesItsParent(t *testing.T) {
	rig := newReasoningRig(t)
	onBlock := rig.invoke(rig.secondCheck, "trade-off", "alice", nil)
	uploadDocument(t, rig.handler, "/api/v1/issues/"+rig.issue, "verification.md", checkServing("k1", rig.requirement))
	if removed := dispatchReferences(t, rig.handler, rig.secondCheck, "alice"); removed.Code != http.StatusNotFound {
		t.Fatalf("version 2 of the verification document drops k2, yet %s answers %d %s", rig.secondCheck, removed.Code, removed.Body.String())
	}
	if state := storedStateOf(t, rig, onBlock.Address); state != graphstore.StateLive {
		t.Errorf("the lens %s on the removed block is %s, want live", onBlock.Address, state)
	}
	pinned := rig.secondCheck + "@v1"
	if read := rig.lenses(onBlock.Address, "alice"); read.Lens == nil || read.Lens.On == nil || *read.Lens.On != pinned {
		t.Errorf("the lens read of %s answers on=%v, want %s, the block at the last version that held it", onBlock.Address, lensOn(read), pinned)
	}
	requireNamedParent(t, rig, onBlock.Address, rig.secondCheck, 1)

	notes := uploadDocument(t, rig.handler, "/api/v1/issues/"+rig.issue, "notes.md", "# Notes\n\nOne paragraph.\n")
	document := "dispatch://" + rig.issue + "/artifact/" + notes.Artifact.Slug
	block := refs.BlockRef(document, firstBlockOf(t, rig.handler, notes.Artifact.ID))
	onDocument := rig.invoke(document, "trade-off", "alice", nil)
	onNotesBlock := rig.invoke(block, "trade-off", "alice", nil)
	ctx := context.Background()
	tx, err := rig.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the retirement: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := docs.RetireDocument(ctx, tx, notes.Artifact.ID, model.Actor{Kind: "user", ID: "alice"}, "The test retires the notes."); err != nil {
		t.Fatalf("retire %s: %v", document, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the retirement: %v", err)
	}
	for address, on := range map[string]string{onDocument.Address: document, onNotesBlock.Address: block} {
		if state := storedStateOf(t, rig, address); state != graphstore.StateLive {
			t.Errorf("the lens %s on %s of the retired document is %s, want live", address, on, state)
		}
		if read := rig.lenses(address, "alice"); read.Lens == nil || read.Lens.On == nil || *read.Lens.On != on+"@v1" {
			t.Errorf("the lens read of %s answers on=%v, want %s@v1, at the last version of the retired document", address, lensOn(read), on)
		}
		requireNamedParent(t, rig, address, on, 1)
	}
}

// lensOn is the `on` a lens read answers, "<nil>" where it answers none.
func lensOn(read nodeLenses) string {
	if read.Lens == nil || read.Lens.On == nil {
		return "<nil>"
	}
	return *read.Lens.On
}

// requireNamedParent fails unless GET /api/v1/references of address answers its node's parent
// naming want, bare or pinned at version, and parent_version version.
func requireNamedParent(t *testing.T, rig reasoningRig, address, want string, version int) {
	t.Helper()
	response := dispatchReferences(t, rig.handler, address, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read %s: status=%d body=%s", address, response.Code, response.Body.String())
	}
	node := decodeBody[model.GraphReferences](t, response).Node
	if node.Parent == nil || (node.Parent.Ref != want && node.Parent.Ref != fmt.Sprintf("%s@v%d", want, version)) {
		t.Errorf("GET /api/v1/references of %s answers parent %+v, want one naming %s", address, node.Parent, want)
	}
	if node.ParentVersion == nil || *node.ParentVersion != version {
		t.Errorf("GET /api/v1/references of %s answers parent_version %v, want %d, the last version that held its parent", address, node.ParentVersion, version)
	}
}

// A finding asked for `project` under a lens its author keeps for themselves is refused 400
// VISIBILITY_WIDER_THAN_PARENT naming the lens, and nothing is stored, as a risk or a lens asked for
// `project` under or on such a node is (the ruling of 2026-11-20 07:15Z on INFRA-1950's R1): no
// write is narrowed in silence. A finding asked for no visibility takes its lens's, `author`.
func TestAFindingAskedForProjectUnderAnAuthorOnlyLensIsRefused(t *testing.T) {
	rig := newReasoningRig(t)
	lens := rig.invoke(rig.spec, "trade-off", "alice", map[string]any{"visibility": graphstore.VisibilityAuthor})
	nodes, tuples := rig.counts(refs.BlockNodeKind, graphstore.TuplePartOf)
	asked := rig.post(nodePath(lens.Address, "findings"), map[string]any{
		"text": "Every reader would see this.", "visibility": graphstore.VisibilityProject}, "alice")
	if asked.Code != http.StatusBadRequest || !strings.Contains(asked.Body.String(), "VISIBILITY_WIDER_THAN_PARENT") ||
		!strings.Contains(asked.Body.String(), lens.Address) {
		t.Errorf("a finding asked for project under alice's author-only lens %s answers %d %s, want 400 VISIBILITY_WIDER_THAN_PARENT naming the lens",
			lens.Address, asked.Code, asked.Body.String())
	}
	if afterNodes, afterTuples := rig.counts(refs.BlockNodeKind, graphstore.TuplePartOf); afterNodes != nodes || afterTuples != tuples {
		t.Errorf("the refused finding left %d block nodes and %d part_of tuples, want the %d and %d before it", afterNodes, afterTuples, nodes, tuples)
	}
	unasked := rig.post(nodePath(lens.Address, "findings"), map[string]any{"text": "Alice keeps this."}, "alice")
	if unasked.Code != http.StatusCreated {
		t.Fatalf("a finding asked for no visibility under %s: status=%d body=%s", lens.Address, unasked.Code, unasked.Body.String())
	}
	if written := decodeBody[lensFinding](t, unasked); written.Visibility != graphstore.VisibilityAuthor {
		t.Errorf("a finding asked for no visibility under the author-only lens %s is %q, want author, its lens's", lens.Address, written.Visibility)
	}
}
