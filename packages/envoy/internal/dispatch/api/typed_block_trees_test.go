package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// wholeBlockContent is what the typed block of each test below holds between its opening line
// and its closing one: a paragraph with a link and a dispatch:// reference, and a second
// paragraph after it. blockWords drops the link's text and the reference from the first and
// joins the second onto it, which is the `text` each read has always answered; the tree is the
// two paragraphs as they stand.
func wholeBlockContent(issueKey string) string {
	return "Read [the deploy guide](https://example.com/guide) and dispatch://" + issueKey + "/spec#b-r2 first.\n\n" +
		"Then **ship** it."
}

// wordsOfWholeBlock is blockWords of wholeBlockContent: no link text, no reference.
const wordsOfWholeBlock = "Read and first. Then ship it."

// requireWholeBlockTree fails unless tree is the display tree of content - the tree a reader of
// that markdown standing alone is shown - and holds the link's text, the link's address and the
// dispatch:// reference, which are what the read's `text` leaves out.
func requireWholeBlockTree(t *testing.T, what string, tree json.RawMessage, content, issueKey string) {
	t.Helper()
	want := sameShape(t, pmdoc.DisplayJSON(content, pmdoc.DisplayLatest))
	if got := sameShape(t, tree); !bytes.Equal(got, want) {
		t.Errorf("%s is\n\t%s\nwant the tree of its whole content %q\n\t%s", what, got, content, want)
	}
	for _, kept := range []string{"the deploy guide", "https://example.com/guide", "dispatch://" + issueKey + "/spec#b-r2", "ship"} {
		if !strings.Contains(string(tree), kept) {
			t.Errorf("%s holds no %q:\n\t%s", what, kept, tree)
		}
	}
}

// treeAt is the tree standing at path of a read made with `Dispatch-Trees: 1`, after the same
// read without the header answered none there.
func treeAt(t *testing.T, handler http.Handler, target, path string) json.RawMessage {
	t.Helper()
	if _, held := valueAt(t, treesRequest(t, handler, target, false), path); held {
		t.Errorf("GET %s without the header carries %s", target, path)
	}
	tree, held := valueAt(t, treesRequest(t, handler, target, true), path)
	if !held {
		t.Fatalf("GET %s with the header carries no %s", target, path)
	}
	return tree
}

// The coverage read answers each requirement's whole content as `text_tree`, for a caller that
// asked for trees: the link and the dispatch:// reference `text` drops are both in it, and
// `text` is what it was.
func TestTheCoverageReadCarriesARequirementsContentWhole(t *testing.T) {
	handler := newTestHandler(t)
	issue := createInteractionIssue(t, handler, "TBT", "Whole requirements",
		":::requirement{#r1}\n"+wholeBlockContent("TBT-1")+"\n:::\n\n"+
			":::requirement{#r2}\nAn answered ask leaves the list.\n:::\n")
	if issue.Key != "TBT-1" {
		t.Fatalf("the issue is %s, want TBT-1, which the requirement's reference names", issue.Key)
	}
	target := "/api/v1/issues/" + issue.Key + "/coverage"

	requireWholeBlockTree(t, "r1's text_tree", treeAt(t, handler, target, "requirements.0.text_tree"),
		wholeBlockContent(issue.Key), issue.Key)
	const plain = "An answered ask leaves the list."
	if got, want := sameShape(t, treeAt(t, handler, target, "requirements.1.text_tree")),
		sameShape(t, pmdoc.DisplayJSON(plain, pmdoc.DisplayLatest)); !bytes.Equal(got, want) {
		t.Errorf("r2's text_tree is\n\t%s\nwant the tree of %q\n\t%s", got, plain, want)
	}

	coverage := decodeBody[model.IssueCoverage](t, treesRequest(t, handler, target, true))
	if got := requirementOf(t, coverage, "r1").Text; got != wordsOfWholeBlock {
		t.Errorf("r1's text = %q, want the words it always answered, %q", got, wordsOfWholeBlock)
	}
}

// The results read answers each check's whole content as `text_tree`, as the coverage read does
// a requirement's.
func TestTheResultsReadCarriesAChecksContentWhole(t *testing.T) {
	handler := newTestHandler(t)
	issue := createInteractionIssue(t, handler, "TBT", "Whole checks", twoRequirements)
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md",
		":::check{#k1 surface=\"cli\" run=\"go test ./...\"}\n"+wholeBlockContent(issue.Key)+"\n:::\n")
	target := "/api/v1/issues/" + issue.Key + "/results"

	requireWholeBlockTree(t, "k1's text_tree", treeAt(t, handler, target, "checks.0.text_tree"),
		wholeBlockContent(issue.Key), issue.Key)

	read := decodeBody[issueResults](t, treesRequest(t, handler, target, true))
	if got := checkOf(t, read, "k1").Text; got != wordsOfWholeBlock {
		t.Errorf("k1's text = %q, want the words it always answered, %q", got, wordsOfWholeBlock)
	}
}

// referenceTargetsOf is the address of one batched reference read of references, in order.
func referenceTargetsOf(references ...string) string {
	query := url.Values{}
	for _, reference := range references {
		query.Add("ref", reference)
	}
	return "/api/v1/reference-targets?" + query.Encode()
}

// The batched reference read answers each block's whole text as `text_tree`, for a caller that
// asked for trees, which is where a plan task's words are read whole: its name is the block's
// stored excerpt, which holds the directive line and stops at 480 runes. A typed block's tree is
// of its own content, as the coverage and the results reads answer one; a block of no declared
// type, of a comment and of a message answers the tree of its own text; a member that names no
// block carries none, and neither does any member of a read that asked for none. The names are
// the same either way.
func TestTheReferenceTargetsReadCarriesATypedBlocksContentWhole(t *testing.T) {
	handler, _ := blockIndexHandler(t)
	createReferenceAPIProject(t, handler, "TBT")
	issue := createSpecIssue(t, handler, "TBT", twoRequirements)
	plan := uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "plan.md",
		"A paragraph of the plan.\n\n"+
			":::plan_task{#t1 serves=\"dispatch://"+issue.Key+"/spec#b-r1\"}\n"+wholeBlockContent(issue.Key)+"\n:::\n")
	planRef := "dispatch://" + issue.Key + "/artifact/" + plan.Artifact.Slug
	paragraphID := blockIDsByText(t, handler, plan.Artifact.ID)["A paragraph of the plan."]
	if paragraphID == "" {
		t.Fatalf("the plan lists no block for its paragraph")
	}
	const commentBlock = "Its second block, with [a link](https://example.com/second)."
	comment := createReferenceAPIComment(t, handler, issue.Key, "A comment's first block.\n\n"+commentBlock)
	const messageBlock = "A message's second block, with dispatch://TBT-1 in it."
	message := createIssueMessage(t, handler, issue.Key, map[string]any{"body": "A message's first block.\n\n" + messageBlock}, "alice")

	references := []string{
		planRef + "#b-t1",
		planRef + "#b-" + text.EncodeBlockID(paragraphID),
		"dispatch://" + issue.Key + "/comment/" + comment.ID + "#b-b2",
		"dispatch://" + issue.Key + "/message/" + message.ID + "#b-b2",
		"dispatch://" + issue.Key,
	}
	target := referenceTargetsOf(references...)
	bare := decodeBody[model.ReferenceTargets](t, treesRequest(t, handler, target, false)).Targets
	read := decodeBody[model.ReferenceTargets](t, treesRequest(t, handler, target, true)).Targets
	if len(bare) != len(references) || len(read) != len(references) {
		t.Fatalf("the reads answered %d and %d members, want %d each", len(bare), len(read), len(references))
	}
	for index, member := range bare {
		if member.TextTree != nil {
			t.Errorf("without the header %s carries a text_tree: %s", member.Ref, member.TextTree)
		}
		if member.Gone || member.Name != read[index].Name || member.Kind != read[index].Kind {
			t.Errorf("%s answers %#v without the header and %#v with it, want one name and one kind, and neither gone",
				member.Ref, member, read[index])
		}
	}

	task := read[0]
	if !strings.Contains(task.Name, "plan_task") {
		t.Errorf("the plan task's name = %q, want the stored excerpt, which opens with its directive line", task.Name)
	}
	requireWholeBlockTree(t, "the plan task's text_tree", json.RawMessage(task.TextTree), wholeBlockContent(issue.Key), issue.Key)
	if strings.Contains(string(task.TextTree), "plan_task") || strings.Contains(string(task.TextTree), "serves") {
		t.Errorf("the plan task's text_tree holds its directive line, which is no part of its content:\n\t%s", task.TextTree)
	}
	for index, content := range map[int]string{1: "A paragraph of the plan.", 2: commentBlock, 3: messageBlock} {
		want := sameShape(t, pmdoc.DisplayJSON(content, pmdoc.DisplayLatest))
		if read[index].TextTree == nil {
			t.Errorf("%s carries no text_tree, want the tree of %q", read[index].Ref, content)
			continue
		}
		if got := sameShape(t, json.RawMessage(read[index].TextTree)); !bytes.Equal(got, want) {
			t.Errorf("%s: text_tree is\n\t%s\nwant the tree of %q\n\t%s", read[index].Ref, got, content, want)
		}
	}
	if read[4].Kind != "issue" || read[4].TextTree != nil {
		t.Errorf("the issue answers %#v, want no text_tree: only a block carries one", read[4])
	}
}

// The batched read's ETag covers each block's tree. A block's name is its stored excerpt, which
// stops at 480 runes, so a change past them moves the tree and leaves every name as it was: a
// tag over the names alone would answer 304 for a tree the page no longer holds. The same set
// unchanged is answered 304, and a read that asked for no tree never shares a tag with one that
// did.
func TestTheReferenceTargetsETagCoversABlocksWholeText(t *testing.T) {
	handler, _ := blockIndexHandler(t)
	createReferenceAPIProject(t, handler, "TBT")
	issue := createSpecIssue(t, handler, "TBT", twoRequirements)
	head := strings.Repeat("The task says the same thing again. ", 20)
	if runes := len([]rune(head)); runes <= 480 {
		t.Fatalf("the task's head is %d runes, too few to carry its tail past the stored excerpt", runes)
	}
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "plan.md",
		":::plan_task{#t1 serves=\"dispatch://"+issue.Key+"/spec#b-r1\"}\n"+head+"It ends before.\n:::\n")
	target := referenceTargetsOf("dispatch://" + issue.Key + "/artifact/plan-md#b-t1")
	conditional := func(etag string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("X-Dispatch-User", "alice")
		request.Header.Set("Dispatch-Trees", "1")
		request.Header.Set("If-None-Match", etag)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	first := treesRequest(t, handler, target, true)
	etag := first.Header().Get("ETag")
	before := decodeBody[model.ReferenceTargets](t, first).Targets
	if len(before) != 1 || before[0].Gone || !strings.Contains(string(before[0].TextTree), "It ends before.") {
		t.Fatalf("the read answered %#v, want the plan task with its whole text", before)
	}
	if strings.Contains(before[0].Name, "It ends before.") {
		t.Fatalf("the task's name %q holds its tail, so the fixture decides nothing about a change past the excerpt", before[0].Name)
	}
	if bare := treesRequest(t, handler, target, false).Header().Get("ETag"); bare == "" || bare == etag {
		t.Errorf("the read without trees carries the ETag %q and the read with them %q, want two tags for two bodies", bare, etag)
	}
	if unchanged := conditional(etag); unchanged.Code != http.StatusNotModified || unchanged.Body.Len() != 0 {
		t.Fatalf("the unchanged set answered status=%d body=%q, want 304", unchanged.Code, unchanged.Body.String())
	}

	editIssueDocument(t, handler, issue.Key, "plan-md", []map[string]any{
		{"op": "replace", "find": "It ends before.", "with": "It ends after."},
	})
	changed := conditional(etag)
	if changed.Code != http.StatusOK || changed.Header().Get("ETag") == etag {
		t.Fatalf("the set whose block changed past its excerpt answered status=%d etag=%q (was %q), want the new answer under a new tag",
			changed.Code, changed.Header().Get("ETag"), etag)
	}
	after := decodeBody[model.ReferenceTargets](t, changed).Targets
	if len(after) != 1 || after[0].Name != before[0].Name {
		t.Fatalf("the changed set answered %#v, want the name %q unchanged", after, before[0].Name)
	}
	if !strings.Contains(string(after[0].TextTree), "It ends after.") || strings.Contains(string(after[0].TextTree), "It ends before.") {
		t.Errorf("the changed block's text_tree is\n\t%s\nwant its new tail", after[0].TextTree)
	}
}
