package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// statePath is the route path, under /api/v1/issues/{key}, of a node's state route or its settle,
// the node's address one escaped segment.
func statePath(issue, address, route string) string {
	return "/api/v1/issues/" + issue + "/nodes/" + url.PathEscape(address) + "/" + route
}

// writeStateAs is POST .../nodes/{id}/state of state on the node address names, as login's cookie.
func writeStateAs(t *testing.T, handler http.Handler, issue, address, state, login string) *httptest.ResponseRecorder {
	t.Helper()
	return dispatchRequest(t, handler, http.MethodPost, statePath(issue, address, "state"), map[string]any{"state": state}, login)
}

// writeState writes state on the node address names as login, failing on anything but 200.
func (r reasoningRig) writeState(address, state, login string) nodeStateWritten {
	r.t.Helper()
	response := writeStateAs(r.t, r.handler, r.issue, address, state, login)
	if response.Code != http.StatusOK {
		r.t.Fatalf("write %s on %s as %s: status=%d body=%s", state, address, login, response.Code, response.Body.String())
	}
	return decodeBody[nodeStateWritten](r.t, response)
}

// nodeStateRead is the node state GET /api/v1/references answers for the node address names, as
// login: the state every read of the node answers.
func nodeStateRead(t *testing.T, handler http.Handler, address, login string) string {
	t.Helper()
	response := dispatchReferences(t, handler, address, login)
	if response.Code != http.StatusOK {
		t.Fatalf("read %s as %s: status=%d body=%s", address, login, response.Code, response.Body.String())
	}
	return decodeBody[model.GraphReferences](t, response).Node.State
}

// requireNodeState fails unless every read of the node address names answers want.
func requireNodeState(t *testing.T, handler http.Handler, address, login, want string) {
	t.Helper()
	if got := nodeStateRead(t, handler, address, login); got != want {
		t.Fatalf("%s reads node state %q, want %q", address, got, want)
	}
}

// proposedRead is GET /api/v1/issues/{key}/proposed as login, failing on anything but 200.
func proposedRead(t *testing.T, handler http.Handler, issue, login string) issueProposed {
	t.Helper()
	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue+"/proposed", nil, login)
	if response.Code != http.StatusOK {
		t.Fatalf("read the proposed nodes of %s: status=%d body=%s", issue, response.Code, response.Body.String())
	}
	return decodeBody[issueProposed](t, response)
}

// proposedRefs is the address of each node a proposed read answers.
func proposedRefs(read issueProposed) []string {
	addresses := make([]string, len(read.Nodes))
	for index, node := range read.Nodes {
		addresses[index] = node.Node.Ref
	}
	return addresses
}

// newestStateTuple is the newest state tuple the store holds of the node it keys by kind and id.
func newestStateTuple(t *testing.T, database *store.Store, kind, id string) graphstore.Tuple {
	t.Helper()
	history, err := graphstore.History(context.Background(), database.Pool, kind, id, graphstore.AttributeState)
	if err != nil || len(history) == 0 {
		t.Fatalf("read the state history of %s %s: %d tuples, %v", kind, id, len(history), err)
	}
	return history[len(history)-1]
}

// TestTheStateRouteTakesTheFiveValuesWhereTheyApplyAndRefusesTheRest drives
// POST /api/v1/issues/{key}/nodes/{id}/state as a person's cookie, the Go twin of c-state-closed
// (dispatch://INFRA-1950/verification#c-state-closed). A lens moves through proposed, overtaken,
// live and gone, each read back from the node and from its newest state tuple, written by her;
// after gone it takes no value, live or proposed, and answers 409 NODE_STATE_NOT_WRITABLE naming
// its kind, its state and the value. A requirement moves through proposed, overtaken and back to
// live, and gone on it is refused and leaves it live. settled through this route is refused, and a
// word that is none of the five, `propsed`, is 400 INVALID_ATTRIBUTE_VALUE naming the attribute
// and the word, with no tuple carrying it stored and the node at the state it held. A node of the
// issue another person may not see answers that person as an id nobody wrote.
func TestTheStateRouteTakesTheFiveValuesWhereTheyApplyAndRefusesTheRest(t *testing.T) {
	rig := newReasoningRig(t)
	ctx := context.Background()
	lens := rig.invoke(rig.spec, "trade-off", "alice", nil)

	for _, state := range []string{graphstore.StateProposed, graphstore.StateOvertaken, graphstore.StateLive, graphstore.StateGone} {
		before := nodeStateRead(t, rig.handler, lens.Address, "alice")
		written := rig.writeState(lens.Address, state, "alice")
		if written.Node.State != state || written.Previous != before || written.Node.Kind != graphstore.KindLens {
			t.Fatalf("the write of %s on the lens answers %+v, want the lens at %s from %s", state, written, state, before)
		}
		newest := newestStateTuple(t, rig.database, graphstore.KindLens, lens.ID)
		if newest.ToID != state || newest.Who.Kind != "user" || newest.Who.ID != "alice" || newest.SourceEvent != graphstore.NoEvent {
			t.Fatalf("the lens's newest state tuple is %+v, want %s written by alice at no event", newest, state)
		}
		if state != graphstore.StateGone {
			requireNodeState(t, rig.handler, lens.Address, "alice", state)
		}
	}
	var row string
	if err := rig.database.Pool.QueryRow(ctx, `select state from graph_nodes where kind = $1 and id = $2`,
		graphstore.KindLens, lens.ID).Scan(&row); err != nil || row != graphstore.StateGone {
		t.Fatalf("the lens's row is at %q (%v), want gone", row, err)
	}
	for _, asked := range []string{graphstore.StateLive, graphstore.StateProposed} {
		refusal := requireRefused(t, asked+" on the gone lens", writeStateAs(t, rig.handler, rig.issue, lens.Address, asked, "alice"),
			http.StatusConflict, codeStateNotWritable, "is gone and takes no "+asked)
		if !strings.Contains(refusal.Error, "lens") {
			t.Fatalf("the refusal of %s on the gone lens does not name its kind: %q", asked, refusal.Error)
		}
	}

	for _, state := range []string{graphstore.StateProposed, graphstore.StateOvertaken, graphstore.StateLive} {
		rig.writeState(rig.requirement, state, "alice")
		requireNodeState(t, rig.handler, rig.requirement, "alice", state)
	}
	requireRefused(t, "gone on the requirement", writeStateAs(t, rig.handler, rig.issue, rig.requirement, graphstore.StateGone, "alice"),
		http.StatusConflict, codeStateNotWritable, "the block "+rig.requirement+" is live and takes no gone")
	requireNodeState(t, rig.handler, rig.requirement, "alice", graphstore.StateLive)
	requireRefused(t, "settled through the state route", writeStateAs(t, rig.handler, rig.issue, rig.requirement, graphstore.StateSettled, "alice"),
		http.StatusConflict, codeStateNotWritable, "is live and takes no settled here: a proposed node is settled by POST")

	tuples := func() int {
		var count int
		if err := rig.database.Pool.QueryRow(ctx, `select count(*) from graph_tuples where kind = $1`, graphstore.AttributeState).Scan(&count); err != nil {
			t.Fatalf("count the state tuples: %v", err)
		}
		return count
	}
	before := tuples()
	requireRefused(t, "a word that is no value", writeStateAs(t, rig.handler, rig.issue, rig.requirement, "propsed", "alice"),
		http.StatusBadRequest, "INVALID_ATTRIBUTE_VALUE", `the node state "propsed" is none of live, gone, proposed, settled, overtaken`)
	var misspelled int
	if err := rig.database.Pool.QueryRow(ctx, `select count(*) from graph_tuples where to_id = 'propsed'`).Scan(&misspelled); err != nil {
		t.Fatalf("count the tuples at propsed: %v", err)
	}
	if misspelled != 0 || tuples() != before {
		t.Fatalf("the refused write stored %d tuples at propsed and moved the state tuples from %d to %d", misspelled, before, tuples())
	}
	requireNodeState(t, rig.handler, rig.requirement, "alice", graphstore.StateLive)

	hidden := rig.invoke(rig.spec, "trade-off", "alice", map[string]any{"visibility": graphstore.VisibilityAuthor})
	nobody := graphstore.KindLens + "/" + uuid.NewString()
	seen := writeStateAs(t, rig.handler, rig.issue, graphstore.KindLens+"/"+hidden.ID, graphstore.StateProposed, "bob")
	unwritten := writeStateAs(t, rig.handler, rig.issue, nobody, graphstore.StateProposed, "bob")
	if seen.Code != http.StatusNotFound || unwritten.Code != seen.Code ||
		!bytes.Equal(seen.Body.Bytes(), bytes.ReplaceAll(unwritten.Body.Bytes(), []byte(nobody), []byte(graphstore.KindLens+"/"+hidden.ID))) {
		t.Fatalf("bob's write on alice's hidden lens answers %d %s, and on a lens nobody wrote %d %s",
			seen.Code, seen.Body.String(), unwritten.Code, unwritten.Body.String())
	}
}

// threeRequirements is a spec marking three requirements, r1, r2 and r3.
const threeRequirements = ":::requirement{#r1}\nThe cache holds every read for a minute.\n:::\n\n" +
	":::requirement{#r2}\nA write evicts what it changed.\n:::\n\n" +
	":::requirement{#r3}\nThe cache is shared by every pod.\n:::\n"

// TestTheReadsAnswerEachNodeStateAndCountNoProposedNodeAsSettled is c-proposed-page
// (dispatch://INFRA-1950/verification#c-proposed-page): a spec of three requirements and a plan,
// nothing serving any of them. With her cookie a person writes r1 proposed, r2 proposed and then
// settles it, and r3 overtaken. Each node reads the state she wrote; the proposed read answers r1
// alone; the coverage read and the approval request's answer raise no gap for r1 and both for r2.
// She settles r1, and with no restart the same reads answer it settled, out of the proposed read
// and with both gaps. Last, a new version of the spec changes r3's text, and r3 still reads
// overtaken, since the index pass keeps every node state that is not gone.
func TestTheReadsAnswerEachNodeStateAndCountNoProposedNodeAsSettled(t *testing.T) {
	handler, _ := newTestHandlerWithStore(t)
	issue := createInteractionIssue(t, handler, "PRP", "Proposed requirements", threeRequirements)
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "plan.md", "The plan holds no task yet.\n")
	spec := "dispatch://" + issue.Key + "/spec"
	r1, r2, r3 := refs.BlockRef(spec, "r1"), refs.BlockRef(spec, "r2"), refs.BlockRef(spec, "r3")

	write := func(address, state string) {
		t.Helper()
		if response := writeStateAs(t, handler, issue.Key, address, state, "alice"); response.Code != http.StatusOK {
			t.Fatalf("write %s on %s: status=%d body=%s", state, address, response.Code, response.Body.String())
		}
	}
	settle := func(address string) {
		t.Helper()
		response := dispatchRequest(t, handler, http.MethodPost, statePath(issue.Key, address, "settle"), nil, "alice")
		if response.Code != http.StatusOK {
			t.Fatalf("settle %s: status=%d body=%s", address, response.Code, response.Body.String())
		}
		if settled := decodeBody[nodeStateWritten](t, response); settled.Node.State != graphstore.StateSettled || settled.Previous != graphstore.StateProposed {
			t.Fatalf("the settle of %s answers %+v, want settled from proposed", address, settled)
		}
	}
	write(r1, graphstore.StateProposed)
	write(r2, graphstore.StateProposed)
	settle(r2)
	write(r3, graphstore.StateOvertaken)

	for address, want := range map[string]string{r1: graphstore.StateProposed, r2: graphstore.StateSettled, r3: graphstore.StateOvertaken} {
		requireNodeState(t, handler, address, "alice", want)
	}

	// requireCounted holds the coverage read and the approval request's answer to counting
	// counted and not proposed: both gaps for each counted requirement, none for the proposed one.
	requireCounted := func(when string, counted []string, proposed string) {
		t.Helper()
		coverage := readCoverage(t, handler, issue.Key)
		if !coverage.HasPlan {
			t.Fatalf("%s: the coverage read holds no plan, so the no-plan-task gap is never raised: %+v", when, coverage)
		}
		if got := requirementOf(t, coverage, proposed); len(got.Gaps) != 0 || got.State != graphstore.StateProposed {
			t.Errorf("%s: the coverage read raises %q for the proposed requirement %s (state %q), want no gap", when, got.Gaps, proposed, got.State)
		}
		for _, gap := range coverage.Gaps {
			if strings.HasPrefix(gap, proposed+" ") {
				t.Errorf("%s: the coverage read's gaps hold %q for the proposed requirement %s", when, gap, proposed)
			}
		}
		for _, blockID := range counted {
			got := requirementOf(t, coverage, blockID)
			if want := []string{noCheckReason, noPlanTaskReason}; !slices.Equal(got.Gaps, want) {
				t.Errorf("%s: the coverage read raises %q for %s (state %q), want %q", when, got.Gaps, blockID, got.State, want)
			}
			for _, reason := range []string{noCheckReason, noPlanTaskReason} {
				if !slices.Contains(coverage.Gaps, blockID+" "+reason) {
					t.Errorf("%s: the coverage read's gaps %q lack %q", when, coverage.Gaps, blockID+" "+reason)
				}
			}
		}
		answer := requestSpecApproval(t, handler, issue.PrimaryArtifactID)
		for _, gap := range answer.Coverage {
			if strings.HasPrefix(gap, proposed+" ") {
				t.Errorf("%s: the approval request's answer lists %q for the proposed requirement %s", when, gap, proposed)
			}
		}
		for _, blockID := range counted {
			if !slices.Contains(answer.Coverage, noCheckGap(blockID)) {
				t.Errorf("%s: the approval request's answer %q lacks %q", when, answer.Coverage, noCheckGap(blockID))
			}
		}
	}

	if got := proposedRefs(proposedRead(t, handler, issue.Key, "alice")); !slices.Equal(got, []string{r1}) {
		t.Fatalf("the proposed read answers %v, want %s alone", got, r1)
	}
	requireCounted("before the settle", []string{"r2"}, "r1")

	settle(r1)
	requireNodeState(t, handler, r1, "alice", graphstore.StateSettled)
	if got := proposedRead(t, handler, issue.Key, "alice"); len(got.Nodes) != 0 {
		t.Fatalf("the proposed read answers %v after the settle, want none", proposedRefs(got))
	}
	coverage := readCoverage(t, handler, issue.Key)
	for _, blockID := range []string{"r1", "r2"} {
		if got := requirementOf(t, coverage, blockID); !slices.Equal(got.Gaps, []string{noCheckReason, noPlanTaskReason}) || got.State != graphstore.StateSettled {
			t.Errorf("after the settle the coverage read answers %s at %q with %q, want settled with both gaps", blockID, got.State, got.Gaps)
		}
	}
	answer := requestSpecApproval(t, handler, issue.PrimaryArtifactID)
	for _, blockID := range []string{"r1", "r2"} {
		if !slices.Contains(answer.Coverage, noCheckGap(blockID)) {
			t.Errorf("after the settle the approval request's answer %q lacks %q", answer.Coverage, noCheckGap(blockID))
		}
	}

	edited := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+issue.PrimaryArtifactID+"/edits", map[string]any{
		"ops":     []map[string]string{{"op": "replace", "find": "The cache is shared by every pod.", "with": "Each pod keeps a cache of its own."}},
		"summary": "Rewrite the overtaken requirement",
	}, "alice")
	if edited.Code != http.StatusOK {
		t.Fatalf("rewrite r3: status=%d body=%s", edited.Code, edited.Body.String())
	}
	reread := decodeBody[model.GraphReferences](t, dispatchReferences(t, handler, r3, "alice"))
	if !strings.Contains(reread.Node.Text, "Each pod keeps a cache of its own.") {
		t.Fatalf("r3 reads %q after the new version, want its new text", reread.Node.Text)
	}
	if reread.Node.State != graphstore.StateOvertaken {
		t.Fatalf("r3 reads %q after a new version changed its text, want overtaken", reread.Node.State)
	}
}

// TestSettlingAProposedNodeIsOneWriteByAPersonOrItsAuthor is c-settle
// (dispatch://INFRA-1950/verification#c-settle). alice signs in with her cookie and holds a
// personal token with the person scope; two agents each hold a broker-issued token bound to a
// session of its own, for bob, so each proves an author and holds no person scope. The spec holds
// four requirements, each the block a version of its own first held: one the first agent wrote,
// two the second wrote, one written on the shared bearer; alice marks all four proposed, and her
// own lens, reserved for her, too. She settles the second agent's first proposal, and the first
// agent its own, each read back settled with its writer as the tuple's writer. The first agent is
// refused the second agent's other proposal, which alice's token then settles as her; alice is
// refused a node already settled, and the shared bearer its own proposal. The first agent's settle
// of alice's hidden lens answers, byte for byte, what a lens nobody wrote answers.
func TestSettlingAProposedNodeIsOneWriteByAPersonOrItsAuthor(t *testing.T) {
	rig := newBrokerTokenRig(t)
	ctx := context.Background()
	if created := rig.asPerson(t, http.MethodPost, "/api/v1/projects", map[string]any{"key": brokerTestProject, "name": "Broker"}); created.Code != http.StatusCreated {
		t.Fatalf("create the project: status=%d body=%s", created.Code, created.Body.String())
	}
	created := rig.asPerson(t, http.MethodPost, "/api/v1/issues", map[string]any{
		"project": brokerTestProject, "title": "Settled by whom", "spec": "An opening paragraph.\n",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create the issue: status=%d body=%s", created.Code, created.Body.String())
	}
	issue := decodeBody[struct {
		Key               string `json:"key"`
		PrimaryArtifactID string `json:"primary_artifact_id"`
	}](t, created)
	spec := "dispatch://" + issue.Key + "/spec"
	refKey := issue.Key + "/spec"

	const (
		firstSession  = "ses_first_agent_0001"
		secondSession = "ses_second_agent_002"
		agentPerson   = "bob"
	)
	agent := func(session, token string) *brokerCaller {
		caller := newBrokerCaller(t)
		answer := liveNamedToken(caller.jkt)
		answer.Session, answer.Person = session, agentPerson
		rig.broker.Says(token, answer)
		return caller
	}
	first, second := agent(firstSession, "the-first-agent's-token"), agent(secondSession, "the-second-agent's-token")
	asFirst := func(method, path string, body any) *httptest.ResponseRecorder {
		return rig.serve(first.presenting(t, method, path, "the-first-agent's-token", body))
	}
	asSecond := func(method, path string, body any) *httptest.ResponseRecorder {
		return rig.serve(second.presenting(t, method, path, "the-second-agent's-token", body))
	}
	asBearer := func(method, path string, body map[string]any) *httptest.ResponseRecorder {
		body["actor"] = sessionActor()
		return rig.serve(brokerRequest(t, method, path, body, "Bearer agent-token", ""))
	}
	personal := personalTokenOwnedBy(t, rig.database, "alice", model.ScopePerson)
	asPersonalToken := func(method, path string) *httptest.ResponseRecorder {
		return rig.serve(brokerRequest(t, method, path, map[string]any{}, "Bearer "+personal, ""))
	}

	// Each requirement is the block one version first held, written by its agent.
	edits := "/api/v1/artifacts/" + issue.PrimaryArtifactID + "/edits"
	insert := func(blockIDs ...string) map[string]any {
		var markdown strings.Builder
		for _, id := range blockIDs {
			markdown.WriteString(":::requirement{#" + id + "}\nThe requirement " + id + " holds.\n:::\n\n")
		}
		return map[string]any{
			"ops":     []map[string]string{{"op": "insert", "after": "end", "markdown": markdown.String()}},
			"summary": "Add " + strings.Join(blockIDs, " and "),
		}
	}
	for name, response := range map[string]func() *httptest.ResponseRecorder{
		"the first agent's": func() *httptest.ResponseRecorder { return asFirst(http.MethodPost, edits, insert("r-first")) },
		"the second agent's": func() *httptest.ResponseRecorder {
			return asSecond(http.MethodPost, edits, insert("r-second-a", "r-second-b"))
		},
		"the shared bearer's": func() *httptest.ResponseRecorder {
			return asBearer(http.MethodPost, edits, insert("r-shared"))
		},
	} {
		if written := response(); written.Code != http.StatusOK {
			t.Fatalf("write %s requirement: status=%d body=%s", name, written.Code, written.Body.String())
		}
	}
	owner := agentPerson
	authors := map[string]model.Actor{
		"r-first":    {Kind: "session", ID: firstSession, Owner: &owner},
		"r-second-a": {Kind: "session", ID: secondSession, Owner: &owner},
		"r-second-b": {Kind: "session", ID: secondSession, Owner: &owner},
		"r-shared":   {Kind: "session", ID: sessionActor()["id"].(string)},
	}
	for blockID, author := range authors {
		node, held, err := graphstore.ReadNode(ctx, rig.database.Pool, refs.BlockNodeKind, refKey+"#"+blockID)
		if err != nil || !held || !node.MadeBy.SameCredential(author) {
			t.Fatalf("%s is made by %+v (held %t, %v), want %+v: the test's premise is who wrote each requirement", blockID, node.MadeBy, held, err, author)
		}
	}

	requirement := func(blockID string) string { return refs.BlockRef(spec, blockID) }
	for blockID := range authors {
		if response := writeStateAs(t, rig.handler, issue.Key, requirement(blockID), graphstore.StateProposed, "alice"); response.Code != http.StatusOK {
			t.Fatalf("mark %s proposed: status=%d body=%s", blockID, response.Code, response.Body.String())
		}
	}
	lensWritten := dispatchRequest(t, rig.handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/lenses", map[string]any{
		"on": spec, "kind": "trade-off", "visibility": graphstore.VisibilityAuthor,
	}, "alice")
	if lensWritten.Code != http.StatusCreated {
		t.Fatalf("write alice's hidden lens: status=%d body=%s", lensWritten.Code, lensWritten.Body.String())
	}
	hidden := decodeBody[lensInvoked](t, lensWritten)
	if response := writeStateAs(t, rig.handler, issue.Key, hidden.Address, graphstore.StateProposed, "alice"); response.Code != http.StatusOK {
		t.Fatalf("mark alice's hidden lens proposed: status=%d body=%s", response.Code, response.Body.String())
	}

	settlePath := func(address string) string { return statePath(issue.Key, address, "settle") }
	// requireSettled holds a settle to 200 and the node to settled, with writer the newest state
	// tuple's writer.
	requireSettled := func(name, blockID string, response *httptest.ResponseRecorder, writer model.Actor) {
		t.Helper()
		if response.Code != http.StatusOK {
			t.Fatalf("%s settles %s: status=%d body=%s, want 200", name, blockID, response.Code, response.Body.String())
		}
		requireNodeState(t, rig.handler, requirement(blockID), "alice", graphstore.StateSettled)
		if newest := newestStateTuple(t, rig.database, refs.BlockNodeKind, refKey+"#"+blockID); newest.ToID != graphstore.StateSettled || !newest.Who.SameAs(writer) {
			t.Fatalf("%s's newest state tuple is %s written by %+v, want settled written by %+v", blockID, newest.ToID, newest.Who, writer)
		}
	}
	// requireRefusedSettle holds a settle to its refusal, naming the node, and the node to proposed.
	requireRefusedSettle := func(name, blockID string, response *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		refusal := requireRefused(t, name, response, status, code, "")
		if !strings.Contains(refusal.Error, requirement(blockID)) {
			t.Fatalf("%s is refused %q, which does not name the node %s", name, refusal.Error, requirement(blockID))
		}
	}

	alice := model.Actor{Kind: "user", ID: "alice"}
	requireSettled("alice's cookie", "r-second-a",
		dispatchRequest(t, rig.handler, http.MethodPost, settlePath(requirement("r-second-a")), nil, "alice"), alice)
	requireSettled("the first agent", "r-first",
		asFirst(http.MethodPost, settlePath(requirement("r-first")), map[string]any{}), model.Actor{Kind: "session", ID: firstSession})

	if response := asFirst(http.MethodPost, settlePath(requirement("r-second-b")), map[string]any{}); response.Code != http.StatusForbidden {
		t.Fatalf("the first agent's settle of the second agent's proposal %s answers %d %s, want 403 %s naming the node id",
			requirement("r-second-b"), response.Code, response.Body.String(), codeSettleNotPermitted)
	} else {
		requireRefusedSettle("the first agent's settle of the second agent's proposal", "r-second-b", response, http.StatusForbidden, codeSettleNotPermitted)
	}
	requireNodeState(t, rig.handler, requirement("r-second-b"), "alice", graphstore.StateProposed)

	requireSettled("alice's personal token", "r-second-b", asPersonalToken(http.MethodPost, settlePath(requirement("r-second-b"))), alice)

	requireRefusedSettle("alice's settle of a node already settled", "r-second-a",
		dispatchRequest(t, rig.handler, http.MethodPost, settlePath(requirement("r-second-a")), nil, "alice"),
		http.StatusConflict, codeNotProposed)

	requireRefusedSettle("the shared bearer's settle of its own proposal", "r-shared",
		asBearer(http.MethodPost, settlePath(requirement("r-shared")), map[string]any{}), http.StatusForbidden, codeSettleNotPermitted)
	requireNodeState(t, rig.handler, requirement("r-shared"), "alice", graphstore.StateProposed)

	nobody := graphstore.KindLens + "/" + uuid.NewString()
	hiddenID := graphstore.KindLens + "/" + hidden.ID
	seen := asFirst(http.MethodPost, settlePath(hiddenID), map[string]any{})
	unwritten := asFirst(http.MethodPost, settlePath(nobody), map[string]any{})
	t.Logf("the first agent's settle of alice's hidden lens: status=%d body=%s", seen.Code, seen.Body.String())
	t.Logf("the first agent's settle of a lens nobody wrote: status=%d body=%s", unwritten.Code, unwritten.Body.String())
	if seen.Code != unwritten.Code || unwritten.Code != http.StatusNotFound ||
		!bytes.Equal(seen.Body.Bytes(), bytes.ReplaceAll(unwritten.Body.Bytes(), []byte(nobody), []byte(hiddenID))) {
		t.Fatalf("the hidden lens answers %d %s and a lens nobody wrote %d %s, want one 404 once the id is substituted",
			seen.Code, seen.Body.String(), unwritten.Code, unwritten.Body.String())
	}
	requireNodeState(t, rig.handler, hidden.Address, "alice", graphstore.StateProposed)
}

// TestTheProposedReadTakesTheProposedIndex holds the proposed read (issueProposedQuery) to the
// partial index graph_nodes_proposed: its plan reads graph_nodes through that index and scans the
// table nowhere, so the read costs the few rows at proposed and not every node of the store.
func TestTheProposedReadTakesTheProposedIndex(t *testing.T) {
	_, database := newTestHandlerWithStore(t)
	plan := planWithoutSeqScans(t, database, issueProposedQuery, "PRP-1", refs.VisibilityArg(model.Actor{Kind: "user", ID: "alice"}))
	if explaintest.SeqScansRelation(t, plan, "graph_nodes") {
		t.Fatalf("the proposed read scans graph_nodes:\n%s", plan)
	}
	if !planReadsIndex(t, plan, "graph_nodes_proposed") {
		t.Fatalf("the proposed read does not read graph_nodes_proposed:\n%s", plan)
	}
}
