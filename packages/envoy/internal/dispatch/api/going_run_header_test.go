package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/going"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// requestForRun is dispatchRequest with the run named in contracts.GoingRunHeader, as a write
// made for that run sends it (GoingCaller.Do).
func requestForRun(t *testing.T, handler http.Handler, method, target string, body any, login, run string) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode request body: %v", err)
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Dispatch-User", login)
	request.Header.Set(contracts.GoingRunHeader, run)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// runWindowAhead moves the run's window an hour ahead in the test's own database, so its
// window never decides whether a write made for it is accepted.
func runWindowAhead(t *testing.T, database *store.Store, run string) {
	t.Helper()
	if _, err := database.Pool.Exec(context.Background(),
		`update going_requests set ends_at = now() + interval '1 hour' where id = $1::uuid`, run); err != nil {
		t.Fatalf("move run %s's window ahead: %v", run, err)
	}
}

// changedByRunCount is how many changed_by_run tuples out of the node point at the run.
func changedByRunCount(t *testing.T, database *store.Store, kind, id, run string) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(context.Background(), `
		select count(*) from graph_tuples
		where from_kind = $1 and from_id = $2 and kind = $3 and to_kind = $4 and to_id = $5
	`, kind, id, graphstore.TupleChangedByRun, graphstore.KindInteraction, run).Scan(&count); err != nil {
		t.Fatalf("count the changed_by_run of %s/%s to run %s: %v", kind, id, run, err)
	}
	return count
}

// tuplesIntoRun is how many tuples of any node point at the run's interaction.
func tuplesIntoRun(t *testing.T, database *store.Store, run string) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(context.Background(), `
		select count(*) from graph_tuples where to_kind = $1 and to_id = $2
	`, graphstore.KindInteraction, run).Scan(&count); err != nil {
		t.Fatalf("count the tuples into run %s: %v", run, err)
	}
	return count
}

// runReads is what one person reads of the issue and of the first person's person node, kept to
// compare before and after the run: the references of the issue in each direction, as
// edgeEnds names them, the backlink count the issue read carries, and the backlink count of the
// person node. bodies is every answer body read, for the words of the run to be looked for in.
type runReads struct {
	to, from    []string
	issueCount  int
	personCount int
	bodies      []string
}

// TestTheReferencesReadAnswersARunToThePersonWhoseRunItWas is c-interaction-page
// (dispatch://INFRA-1950/spec#b-r-interaction). Two people read the issue's references both
// ways, its backlink count and the backlink count of alice's person node. Alice's run is recorded
// with two frames; under the run's header and with her credential a comment is written on the
// issue and the issue's labels are changed through its PATCH; and the run is ended. Alice then
// reads, of the comment and of the issue each, one changed_by_run to the interaction of her run,
// which answers its kind, her words cut at the 240 runes an edge's excerpt keeps and its own
// address. At that address, read both ways, the interaction answers her words whole as its text,
// its one authored_by to her person node and its frames in sequence, the two it appended and then
// the terminal one. The issue's count moves alike for both, since each tuple points away from the
// node the run changed, her person node's count is one more for her, its authored_by, and bob
// reads nothing of the run anywhere.
func TestTheReferencesReadAnswersARunToThePersonWhoseRunItWas(t *testing.T) {
	handler, database, deps, _ := newRegisteredTestServer(t, testServerOptions{})
	server := directServer(deps)
	ctx := context.Background()
	issue := createTestIssue(t, handler, "RUN", "The issue a run writes on")
	issueRef := "dispatch://" + issue
	const alice, bob = "alice", "bob"
	// Longer than an edge's excerpt keeps, and with a rune of three bytes in its head, so the cut
	// and the whole are told apart rune for rune.
	const words = "Which plan holds the rollback step of the cutover — the step that moves every reader of the" +
		" store back to the tables it read before, in the order the deploy section gives, with the census" +
		" run after each move and the backfill run once the roll is done, so that nobody reads a node the" +
		" older build cannot address?"
	whole := []rune(words)
	if len(whole) <= 240 {
		t.Fatalf("the run's words are %d runes, want more than the 240 an excerpt keeps, so the cut proves something", len(whole))
	}
	excerpt := string(whole[:240])

	read := func(login string) runReads {
		t.Helper()
		var kept runReads
		for _, direction := range []string{"to", "from"} {
			response := referencesAt(t, handler, direction, issueRef, login)
			t.Logf("%s's ?%s=%s: status=%d body=%s", login, direction, issueRef, response.Code, response.Body.String())
			if response.Code != http.StatusOK {
				t.Fatalf("%s's ?%s= of the issue answers %d %s", login, direction, response.Code, response.Body.String())
			}
			kept.bodies = append(kept.bodies, response.Body.String())
			ends := edgeEnds(decodeBody[model.GraphReferences](t, response).Edges)
			if direction == "to" {
				kept.to = ends
			} else {
				kept.from = ends
			}
		}
		issueRead := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue, nil, login)
		if issueRead.Code != http.StatusOK {
			t.Fatalf("%s's read of the issue answers %d %s", login, issueRead.Code, issueRead.Body.String())
		}
		kept.bodies = append(kept.bodies, issueRead.Body.String())
		kept.issueCount = decodeBody[struct {
			ReferencedByCount int `json:"referenced_by_count"`
		}](t, issueRead).ReferencedByCount
		counts, err := refs.BacklinkCounts(ctx, database.Pool, model.Actor{Kind: "user", ID: login}, graphstore.KindPerson, []string{alice}, nil)
		if err != nil {
			t.Fatalf("count the backlinks of alice's person node as %s: %v", login, err)
		}
		kept.personCount = counts[alice]
		t.Logf("%s reads the issue's backlink count %d and alice's person node's %d", login, kept.issueCount, kept.personCount)
		return kept
	}
	before := map[string]runReads{alice: read(alice), bob: read(bob)}

	run, err := server.recordGoingRequest(ctx, model.Actor{Kind: "user", ID: alice}, "", words)
	if err != nil {
		t.Fatalf("record alice's run: %v", err)
	}
	runWindowAhead(t, database, run.ID)
	frames := []going.Event{going.Text("The rollback "), going.Text("is in the plan.")}
	for index, frame := range frames {
		if sequence, err := appendGoingEvent(ctx, database.Pool, run.ID, frame); err != nil || sequence != index+1 {
			t.Fatalf("append frame %d of the run: sequence %d, %v", index+1, sequence, err)
		}
	}
	written := requestForRun(t, handler, http.MethodPost, "/api/v1/issues/"+issue+"/comments",
		map[string]any{"body": "The run's comment on the issue."}, alice, run.ID)
	if written.Code != http.StatusCreated {
		t.Fatalf("write the comment under the run's header: status=%d body=%s", written.Code, written.Body.String())
	}
	comment := decodeBody[writtenRecord](t, written).ID
	// The issue's own field write, under the same run: its labels.
	labels := []string{"run-changed"}
	patched := requestForRun(t, handler, http.MethodPatch, "/api/v1/issues/"+issue, map[string]any{"labels": labels}, alice, run.ID)
	t.Logf("alice's PATCH of the issue's labels under the run: status=%d body=%s", patched.Code, patched.Body.String())
	if patched.Code != http.StatusOK {
		t.Fatalf("change the issue's labels under the run's header: status=%d body=%s", patched.Code, patched.Body.String())
	}
	if got := decodeBody[model.Issue](t, patched).Labels; !slices.Equal(got, labels) {
		t.Fatalf("the PATCH under the run left the issue's labels %v, want %v", got, labels)
	}
	terminal := going.End(6, 0)
	if _, err := endGoingRun(ctx, database.Pool, run.ID, terminal); err != nil {
		t.Fatalf("end the run: %v", err)
	}
	frames = append(frames, terminal)
	commentRef := "dispatch://" + issue + "/comment/" + comment
	interactionRef := "dispatch://interaction/" + run.ID

	// Each node the run changed - the comment it wrote, the issue whose labels it changed - is
	// read both ways by both people. Alice reads its one changed_by_run out of it to her run's
	// interaction, with the head of her words as the excerpt, and nothing of the run into it; bob
	// reads no changed_by_run and no interaction either way.
	after := map[string]runReads{}
	for _, login := range []string{alice, bob} {
		after[login] = read(login)
		for _, changed := range []struct{ what, id, ref string }{{"comment", comment, commentRef}, {"issue", issue, issueRef}} {
			for _, direction := range []string{"to", "from"} {
				response := referencesAt(t, handler, direction, changed.ref, login)
				t.Logf("%s's ?%s=%s: status=%d body=%s", login, direction, changed.ref, response.Code, response.Body.String())
				if response.Code != http.StatusOK {
					t.Fatalf("%s's ?%s= of the %s answers %d %s", login, direction, changed.what, response.Code, response.Body.String())
				}
				reads := after[login]
				reads.bodies = append(reads.bodies, response.Body.String())
				after[login] = reads
				edges := decodeBody[model.GraphReferences](t, response).Edges
				byRun := slices.DeleteFunc(slices.Clone(edges), func(edge model.GraphEdge) bool {
					return edge.Kind != graphstore.TupleChangedByRun && edge.Node.Kind != graphstore.KindInteraction
				})
				switch {
				case login == bob && len(byRun) != 0:
					t.Errorf("bob's ?%s= of the %s answers %+v, want no changed_by_run and no interaction", direction, changed.what, byRun)
				case login == alice && direction == "to" && len(byRun) != 0:
					t.Errorf("alice's ?to= of the %s answers %+v: the changed_by_run points away from it", changed.what, byRun)
				case login == alice && direction == "from":
					if len(byRun) != 1 || byRun[0].Kind != graphstore.TupleChangedByRun || byRun[0].Direction != "out" {
						t.Fatalf("alice's references of %s %s hold %+v, want its one changed_by_run to the interaction of run %s",
							changed.what, changed.id, byRun, run.ID)
					}
					node := byRun[0].Node
					if node.Kind != graphstore.KindInteraction || node.ID != run.ID || node.Ref != interactionRef {
						t.Errorf("the changed_by_run of the %s ends at %+v, want the interaction at %s", changed.what, node, interactionRef)
					}
					if byRun[0].Excerpt == nil || byRun[0].Excerpt.Text != excerpt {
						t.Errorf("the interaction at the %s's changed_by_run answers the excerpt %+v, want the run's words cut at 240 runes, %q, character for character",
							changed.what, byRun[0].Excerpt, excerpt)
					}
				}
			}
		}
	}

	// Each node the run changed holds one changed_by_run tuple, to the interaction of the run.
	for _, changed := range []struct{ kind, id string }{{graphstore.KindComment, comment}, {graphstore.KindIssue, issue}} {
		held, err := graphstore.CurrentOut(ctx, database.Pool, changed.kind, changed.id, []string{graphstore.TupleChangedByRun})
		if err != nil {
			t.Fatalf("read the %s's changed_by_run: %v", changed.kind, err)
		}
		t.Logf("the %s's changed_by_run in the store: %+v", changed.kind, held)
		if len(held) != 1 || held[0].ToKind != graphstore.KindInteraction || held[0].ToID != run.ID {
			t.Fatalf("%s %s holds the changed_by_run %+v, want one to the interaction of run %s", changed.kind, changed.id, held, run.ID)
		}
	}

	// Alice reads the interaction at its own address, both ways: her words whole as its text, and,
	// out of it, its one authored_by, to her person node, and its frames, the two it appended and
	// then the terminal one, in sequence.
	for _, direction := range []string{"to", "from"} {
		interaction := referencesAt(t, handler, direction, interactionRef, alice)
		t.Logf("alice's ?%s=%s: status=%d body=%s", direction, interactionRef, interaction.Code, interaction.Body.String())
		if interaction.Code != http.StatusOK {
			t.Fatalf("alice's ?%s= of her run's interaction answers %d %s", direction, interaction.Code, interaction.Body.String())
		}
		answer := decodeBody[model.GraphReferences](t, interaction)
		if answer.Node.Kind != graphstore.KindInteraction || answer.Node.Ref != interactionRef {
			t.Errorf("alice's ?%s= of %s answers the node %+v", direction, interactionRef, answer.Node)
		}
		if answer.Node.Text != words {
			t.Errorf("alice's ?%s= of %s answers the text %q, %d runes, want the run's whole words, %d runes, character for character",
				direction, interactionRef, answer.Node.Text, len([]rune(answer.Node.Text)), len(whole))
		}
		if direction == "to" {
			continue
		}
		authored := slices.DeleteFunc(slices.Clone(answer.Edges), func(edge model.GraphEdge) bool { return edge.Kind != graphstore.TupleAuthoredBy })
		if len(authored) != 1 || authored[0].Node.Kind != graphstore.KindPerson || authored[0].Node.ID != alice {
			t.Errorf("alice's run answers the authored_by %+v, want one to her person node", authored)
		}
		if len(answer.Node.Frames) < len(frames) {
			t.Fatalf("alice's run answers the frames %+v, want at least the %d it stored", answer.Node.Frames, len(frames))
		}
		for index, want := range frames {
			got := answer.Node.Frames[index]
			data, err := json.Marshal(got.Data)
			if err != nil {
				t.Fatalf("encode frame %d's data: %v", index+1, err)
			}
			wantData, err := json.Marshal(want.Data)
			if err != nil {
				t.Fatalf("encode the stored frame %d's data: %v", index+1, err)
			}
			if got.Sequence != index+1 || got.Name != want.Name || !jsonEqual(t, data, string(wantData)) {
				t.Errorf("frame value %d of the run is %d %s %s, want %d %s %s", index+1, got.Sequence, got.Name, data, index+1, want.Name, wantData)
			}
		}
	}

	// Each tuple points away from the node the run changed, so the issue's count moves alike for
	// both; alice's person node's is one more for her, the run's authored_by, and unmoved for bob.
	moved := func(login string) int { return after[login].issueCount - before[login].issueCount }
	if moved(alice) != moved(bob) {
		t.Errorf("the issue's backlink count moved by %d for alice and by %d for bob, want the same", moved(alice), moved(bob))
	}
	if after[alice].personCount != before[alice].personCount+1 || after[bob].personCount != before[bob].personCount ||
		after[alice].personCount != after[bob].personCount+1 {
		t.Errorf("alice's person node counts %d then %d for her and %d then %d for bob, want one more for her alone",
			before[alice].personCount, after[alice].personCount, before[bob].personCount, after[bob].personCount)
	}
	for _, direction := range []struct {
		name          string
		before, after []string
	}{{"to", before[bob].to, after[bob].to}, {"from", before[bob].from, after[bob].from}} {
		if slices.ContainsFunc(direction.after, func(end string) bool { return strings.Contains(end, graphstore.KindInteraction) }) {
			t.Errorf("bob's ?%s= of the issue answers %v, which names an interaction", direction.name, direction.after)
		}
	}
	for _, body := range after[bob].bodies {
		if strings.Contains(body, excerpt) || strings.Contains(body, run.ID) {
			t.Errorf("bob read the run in %s", body)
		}
	}
}

// A write that names a run it may not write for is refused before it runs, as the stream refuses
// a run nobody holds, and stores nothing: another person's run, a run past its window, a run the
// record does not hold and a header that names no uuid each answer 404 GOING_REQUEST_NOT_FOUND,
// the issue holds no comment more, and no tuple points at the run. The refusal claims no
// Idempotency-Key: the same request under the same key, without the header, then writes.
func TestAWriteNamingARunItMayNotWriteForStoresNothing(t *testing.T) {
	handler, database, deps, _ := newRegisteredTestServer(t, testServerOptions{})
	server := directServer(deps)
	ctx := context.Background()
	issue := createTestIssue(t, handler, "RFS", "The issue no refused run writes on")
	record := func(asker string) goingRequest {
		t.Helper()
		run, err := server.recordGoingRequest(ctx, model.Actor{Kind: "user", ID: asker}, "", "What may "+asker+" write for?")
		if err != nil {
			t.Fatalf("record %s's run: %v", asker, err)
		}
		runWindowAhead(t, database, run.ID)
		return run
	}
	bobs := record("bob")
	ended := record("alice")
	if _, err := database.Pool.Exec(ctx, `update going_requests set ends_at = now() - interval '1 second' where id = $1::uuid`, ended.ID); err != nil {
		t.Fatalf("end the run's window: %v", err)
	}
	comments := func() int {
		t.Helper()
		var count int
		if err := database.Pool.QueryRow(ctx, `select count(*) from comments where issue_key = $1`, issue).Scan(&count); err != nil {
			t.Fatalf("count the issue's comments: %v", err)
		}
		return count
	}
	held := comments()
	for _, refused := range []struct{ name, run string }{
		{"bob's run", bobs.ID},
		{"alice's run past its window", ended.ID},
		{"a run the record does not hold", uuid.NewString()},
		{"a header that is no uuid", "not-a-run"},
	} {
		response := requestForRun(t, handler, http.MethodPost, "/api/v1/issues/"+issue+"/comments",
			map[string]any{"body": "A comment written for " + refused.name + "."}, "alice", refused.run)
		t.Logf("a comment written for %s: status=%d body=%s", refused.name, response.Code, response.Body.String())
		if response.Code != http.StatusNotFound || decodeBody[errorBody](t, response).Code != codeGoingRequestNotFound {
			t.Errorf("a comment written for %s answers %d %s, want 404 %s", refused.name, response.Code, response.Body.String(), codeGoingRequestNotFound)
		}
		if count := comments(); count != held {
			t.Errorf("a comment written for %s left the issue with %d comments, want the %d it held", refused.name, count, held)
		}
		if _, err := uuid.Parse(refused.run); err == nil {
			if tuples := tuplesIntoRun(t, database, refused.run); tuples != 0 {
				t.Errorf("a comment written for %s left %d tuples pointing at the run, want none", refused.name, tuples)
			}
		}
	}

	const key = "run-refused-claims-no-key"
	keyed := func(run string) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"body": "A comment under a key."})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/issues/"+issue+"/comments", bytes.NewReader(data))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Dispatch-User", "alice")
		request.Header.Set(contracts.IdempotencyKeyHeader, key)
		if run != "" {
			request.Header.Set(contracts.GoingRunHeader, run)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if refused := keyed(bobs.ID); refused.Code != http.StatusNotFound {
		t.Fatalf("a keyed comment written for bob's run answers %d %s, want 404", refused.Code, refused.Body.String())
	}
	if written := keyed(""); written.Code != http.StatusCreated {
		t.Fatalf("the same keyed comment written for no run answers %d %s, want 201: the refusal claims no key", written.Code, written.Body.String())
	}
}

// A run named in the header in another of the spellings uuid.Parse admits - RFC 4122's URN form,
// the braced form, upper case - is the run its canonical id names, and a write made for it is
// admitted exactly as one naming that id is. withinGoingRun sends the parsed uuid's own spelling
// on, to the lookup and to the context, and never the header's: sent as written, the URN form
// reaches readLiveGoingRun's $1::uuid, which Postgres refuses ("invalid input syntax for type
// uuid", SQLSTATE 22P02), and the write answers 500 where the canonical id answers 201; and a
// spelling put on the context would be the far end of every changed_by_run the write appends,
// which names no interaction node. Each comment written for the run, under each spelling, answers
// 201 and holds the one changed_by_run its write appended, to the run's canonical id, and no
// tuple points at the spelling as the header wrote it.
func TestAWriteNamingARunInAnotherUuidSpellingIsAdmittedAsTheCanonicalID(t *testing.T) {
	handler, database, deps, _ := newRegisteredTestServer(t, testServerOptions{})
	server := directServer(deps)
	ctx := context.Background()
	issue := createTestIssue(t, handler, "RUU", "The issue a run named in every spelling writes on")
	run, err := server.recordGoingRequest(ctx, model.Actor{Kind: "user", ID: "alice"}, "", "Write for me, however my run is spelled.")
	if err != nil {
		t.Fatalf("record alice's run: %v", err)
	}
	runWindowAhead(t, database, run.ID)
	for _, spelling := range []struct{ name, header string }{
		{"the canonical id", run.ID},
		{"the URN form", "urn:uuid:" + run.ID},
		{"the braced form", "{" + run.ID + "}"},
		{"upper case", strings.ToUpper(run.ID)},
	} {
		response := requestForRun(t, handler, http.MethodPost, "/api/v1/issues/"+issue+"/comments",
			map[string]any{"body": "A comment written for the run named in " + spelling.name + "."}, "alice", spelling.header)
		t.Logf("a comment written for the run named in %s (%s): status=%d body=%s", spelling.name, spelling.header, response.Code, response.Body.String())
		if response.Code != http.StatusCreated {
			t.Errorf("a comment written for the run named in %s (%s) answers %d %s, want 201, as one naming the canonical id is answered",
				spelling.name, spelling.header, response.Code, response.Body.String())
			continue
		}
		comment := decodeBody[writtenRecord](t, response).ID
		if held := changedByRunCount(t, database, graphstore.KindComment, comment, run.ID); held != 1 {
			t.Errorf("the comment written for the run named in %s holds %d changed_by_run tuples to the canonical id %s, want the one its write appended",
				spelling.name, held, run.ID)
		}
		if spelling.header == run.ID {
			continue
		}
		if stray := tuplesIntoRun(t, database, spelling.header); stray != 0 {
			t.Errorf("%d tuples point at the run as %s spells it (%s), want none: a changed_by_run names the canonical id",
				stray, spelling.name, spelling.header)
		}
	}
}

// A version written for a run names as changed by it each block whose words it changed and each
// block it dropped, and not a block it left as it was.
func TestAVersionWrittenForARunNamesTheBlocksItChanged(t *testing.T) {
	handler, database, deps, _ := newRegisteredTestServer(t, testServerOptions{})
	server := directServer(deps)
	ctx := context.Background()
	issue := createInteractionIssue(t, handler, "RVB", "A spec a run edits",
		":::requirement{#kept}\nThe kept words stay.\n:::\n\n:::requirement{#edited}\nThe first words.\n:::\n\n:::requirement{#dropped}\nThe dropped words.\n:::\n")
	run, err := server.recordGoingRequest(ctx, model.Actor{Kind: "user", ID: "alice"}, "", "Edit the spec for me.")
	if err != nil {
		t.Fatalf("record alice's run: %v", err)
	}
	runWindowAhead(t, database, run.ID)
	edited := requestForRun(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/artifacts/spec/edits", map[string]any{
		"ops": []map[string]any{
			{"op": "replace", "find": "The first words.", "with": "The second words."},
			{"op": "delete", "block": "dropped"},
		},
	}, "alice", run.ID)
	t.Logf("the edit written for the run: status=%d body=%s", edited.Code, edited.Body.String())
	if edited.Code != http.StatusOK && edited.Code != http.StatusCreated {
		t.Fatalf("edit the spec under the run's header: status=%d body=%s", edited.Code, edited.Body.String())
	}
	spec := issue.Key + "/spec"
	for _, block := range []struct {
		id   string
		want int
	}{{"edited", 1}, {"dropped", 1}, {"kept", 0}} {
		if got := changedByRunCount(t, database, refs.BlockNodeKind, spec+"#"+block.id, run.ID); got != block.want {
			t.Errorf("block %s holds %d changed_by_run tuples to the run, want %d", block.id, got, block.want)
		}
	}
	var state string
	if err := database.Pool.QueryRow(ctx, `select state from graph_nodes where kind = $1 and id = $2`,
		refs.BlockNodeKind, spec+"#dropped").Scan(&state); err != nil || state != graphstore.StateGone {
		t.Fatalf("the dropped block's node is %q (%v), want gone: the edit did not drop it", state, err)
	}
}
