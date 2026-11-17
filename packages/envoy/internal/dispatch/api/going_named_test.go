// going_named_test.go holds which invocations of a proposal run at once to what the person's own
// typed words name: an item is named only by a reference to it as Dispatch reads references in
// text, a document only under an owner so named, a hand-over of the work - its route, its
// assignee - only where the words name it or the issue already holds it, and an upload over a
// document its owner holds only where its name is the one that document is stored under and the
// words write it.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/going"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// goingInternals is what a reason the bar shows a person may not name, since the person at the bar
// holds none of it: a tool's id (dispatch_doc_edit), the dispatch command line - the CLI, one of
// its commands or flags (dispatch artifact, --force) - and the members of a write the bar fills in
// itself (force, approved person). A reason naming one tells that person of nothing they can do.
var goingInternals = regexp.MustCompile(`dispatch_[a-z_]+|--[a-z]|\bCLI\b|\bdispatch (command|artifact|[a-z]+-[a-z]+)\b|\bforce\b|\bapproved (auto|person|confirmed)\b`)

// goingAutomaticOf is whether the one invocation the answer wrote for action, as the proposal of a
// run typed as words marks it: about the page's DEMO-12 where onPage, about the items the words
// name otherwise.
func goingAutomaticOf(t *testing.T, words string, onPage bool, action string, arguments map[string]any, holds goingHolds) bool {
	t.Helper()
	written, err := json.Marshal(map[string][]map[string]any{action: {arguments}})
	if err != nil {
		t.Fatal(err)
	}
	start := goingBrowserStart(t)
	start.words = words
	start.serverURL = testServerURL
	start.chip = &goingContext{Path: "/issues/DEMO-12", Project: "DEMO", Issue: "DEMO-12"}
	context := going.ContextNamed
	if onPage {
		context = going.ContextPage
	}
	proposal := goingPropose(goingDecision{Context: context, offered: []string{action}},
		&going.Final{Answer: "Done.", Arguments: string(written)}, start, holds)
	if len(proposal.Offers) != 1 || len(proposal.Offers[0].Invocations) != 1 {
		t.Fatalf("%q proposed %+v, want the one invocation of %s the answer wrote", words, proposal, action)
	}
	return proposal.Offers[0].Invocations[0].Automatic
}

// goingDocumentsHeld is a store whose owners hold the documents named, each as its owner's key,
// a slash and the document's name, found by that name or its slug, and which answers every read.
func goingDocumentsHeld(documents ...string) goingDocumentHeld {
	return func(owner artifactTarget, name string) (*goingHeldDocument, bool) {
		key := owner.Project
		if owner.IssueKey != nil {
			key = *owner.IssueKey
		}
		for _, document := range documents {
			held, holds := strings.CutPrefix(document, key+"/")
			if holds && (held == name || artifactSlug(held) == name) {
				return &goingHeldDocument{Name: held, Slug: artifactSlug(held)}, true
			}
		}
		return nil, true
	}
}

// TestGoingAutomaticNamesAnItemOnlyByAReferenceTheWordsWrite: an ordinary English word that is
// also a project key names no project, and a document's slug names nothing under an owner the
// words do not name; an issue key, a project key typed in upper case, a reference and a whole id
// each name their item, and a longer key or a key in lower case names nothing.
func TestGoingAutomaticNamesAnItemOnlyByAReferenceTheWordsWrite(t *testing.T) {
	const ask = "0199e2a0-2222-7000-8000-000000000002"
	edit := []any{map[string]any{"op": "replace", "find": "first line", "with": "opening line"}}
	for _, tc := range []struct {
		name, words, action string
		onPage              bool
		arguments           map[string]any
		automatic           bool
	}{
		{"a project key the words spell as an English word", "write the stock count down as a document", "dispatch_artifact", false,
			map[string]any{"project": "AS", "name": "stock.md", "content": "# Stock\n"}, false},
		{"a document under a project the words spell as an English word", "update the notes", "dispatch_doc_edit", false,
			map[string]any{"project": "THE", "artifact": "notes", "ops": edit}, false},
		{"a project key typed in upper case as a token of its own", "write the stock count down as a document in AS", "dispatch_artifact", false,
			map[string]any{"project": "AS", "name": "stock.md", "content": "# Stock\n"}, true},
		{"a document under the page's project", "update the notes", "dispatch_doc_edit", true,
			map[string]any{"project": "DEMO", "artifact": "notes", "ops": edit}, true},
		{"a document under an issue the words name", "call it the opening line in the notes of DEMO-7", "dispatch_doc_edit", false,
			map[string]any{"issue": "DEMO-7", "artifact": "notes", "ops": edit}, true},
		{"a document a reference names", "fix dispatch://DEMO-7/artifact/notes", "dispatch_doc_edit", false,
			map[string]any{"issue": "DEMO-7", "artifact": "notes", "ops": edit}, true},
		{"a document whose name the words never write", "fix DEMO-7", "dispatch_doc_edit", false,
			map[string]any{"issue": "DEMO-7", "artifact": "notes", "ops": edit}, false},
		{"an issue key the words write", "mark DEMO-12 done", "dispatch_issue_update", false,
			map[string]any{"issue": "DEMO-12", "status": "done", "reason": "Shipped."}, true},
		{"a key the words write a longer key of", "mark DEMO-123 done", "dispatch_issue_update", false,
			map[string]any{"issue": "DEMO-12", "status": "done", "reason": "Shipped."}, false},
		{"a key the words write in lower case", "mark demo-12 done", "dispatch_issue_update", false,
			map[string]any{"issue": "DEMO-12", "status": "done", "reason": "Shipped."}, false},
		{"an ask's id the words write whole", "move the ship ask " + ask + " to Monday", "dispatch_edit_ask", false,
			map[string]any{"ask": ask, "question": "Ship it on Monday?"}, true},
		{"an ask's id the words do not write", "move the ship ask to Monday", "dispatch_edit_ask", false,
			map[string]any{"ask": ask, "question": "Ship it on Monday?"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := goingAutomaticOf(t, tc.words, tc.onPage, tc.action, tc.arguments, goingHolds{document: goingDocumentsHeld()}); got != tc.automatic {
				t.Fatalf("%q with %v is automatic=%v, want %v", tc.words, tc.arguments, got, tc.automatic)
			}
		})
	}
}

// TestGoingAutomaticHoldsAHandOverAndALinkToTheWordsToo: on the page's own issue, an update that
// also routes the issue to a session, assigns it to a person, makes it wait on another issue or
// ranks it beside one, each of which only stored text named, is an offer; a route or an assignee
// the words name, or one the issue already holds, runs at once.
func TestGoingAutomaticHoldsAHandOverAndALinkToTheWordsToo(t *testing.T) {
	update := func(extra map[string]any) map[string]any {
		arguments := map[string]any{"issue": "DEMO-12", "status": "done", "reason": "Shipped."}
		for name, value := range extra {
			arguments[name] = value
		}
		return arguments
	}
	holds := func(route, assignee string) goingHeld {
		return func(issue string) (map[string]string, bool) {
			return map[string]string{"route": route, "assignee": assignee}, issue == "DEMO-12"
		}
	}
	for _, tc := range []struct {
		name, words string
		extra       map[string]any
		held        goingHeld
		automatic   bool
	}{
		{"the update alone", "mark this done", nil, nil, true},
		{"a route only stored text named", "mark this done", map[string]any{"route": "session:0199e2a0-9999-7000-8000-000000000009"}, holds("", ""), false},
		{"an assignee only stored text named", "mark this done", map[string]any{"assignee": "mallory@example.com"}, holds("", "alice@example.com"), false},
		{"an unassign the words did not ask for", "mark this done", map[string]any{"assignee": nil}, holds("", "alice@example.com"), false},
		{"a blocker only stored text named", "mark this done", map[string]any{"blocked_by": []any{"DEMO-99"}}, nil, false},
		{"a rank beside an issue only stored text named", "mark this done", map[string]any{"before": "DEMO-99"}, nil, false},
		{"a route the issue already holds", "mark this done", map[string]any{"route": "role:reviewer"}, holds("role:reviewer", ""), true},
		{"an assignee the issue already holds", "mark this done", map[string]any{"assignee": "alice@example.com"}, holds("", "alice@example.com"), true},
		{"a route the words name", "mark this done and route it to role:reviewer.", map[string]any{"route": "role:reviewer"}, nil, true},
		{"an assignee the words name", "mark this done for alice@example.com", map[string]any{"assignee": "alice@example.com"}, nil, true},
		{"a blocker the words name", "mark this done, blocked by DEMO-99", map[string]any{"blocked_by": []any{"DEMO-99"}}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := goingAutomaticOf(t, tc.words, true, "dispatch_issue_update", update(tc.extra), goingHolds{issue: tc.held}); got != tc.automatic {
				t.Fatalf("%q with %v is automatic=%v, want %v", tc.words, tc.extra, got, tc.automatic)
			}
		})
	}
}

// TestGoingAutomaticHoldsTheAdvancesRelationToTheWordsToo (merge of the `advances` relation):
// on the page's own issue, an update that also states which priorities the issue advances names
// existing items, so a relation only stored text named is an offer, and runs at once only where
// the words name the priority by its id. A priority id is a slug (priorityIDPattern); the words
// name one only where it is spelled as an id written short (text.IsIDPrefix), so a priority whose
// id is ordinary words is never named and its update is always an offer. The revision token the
// write carries beside the list (advances_revision) names no item and is a precondition, so it
// neither runs anything at once on its own nor holds back an update whose priorities the words
// name.
func TestGoingAutomaticHoldsTheAdvancesRelationToTheWordsToo(t *testing.T) {
	// A priority id spelled as hex digits is both a slug the grammar admits (priorityIDPattern)
	// and an id written short (text.IsIDPrefix), so the words can name it as goingNamesItem reads
	// an id.
	const priority = "2a7f1c90"
	update := func(extra map[string]any) map[string]any {
		arguments := map[string]any{"issue": "DEMO-12", "status": "done", "reason": "Shipped."}
		for name, value := range extra {
			arguments[name] = value
		}
		return arguments
	}
	for _, tc := range []struct {
		name, words string
		extra       map[string]any
		automatic   bool
	}{
		{"the update alone", "mark this done", nil, true},
		{"a priority only stored text named", "mark this done", map[string]any{"advances": []any{priority}}, false},
		{"a priority whose id is ordinary words", "mark this done and advance the daemon", map[string]any{"advances": []any{"ship-the-daemon"}}, false},
		{"a priority the words name by its id", "mark this done; it advances 2a7f1c90", map[string]any{"advances": []any{priority}}, true},
		{"a revision token beside a priority the words name", "mark this done; it advances 2a7f1c90", map[string]any{"advances": []any{priority}, "advances_revision": float64(7)}, true},
		{"a revision token beside a priority only stored text named", "mark this done", map[string]any{"advances": []any{priority}, "advances_revision": float64(7)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := goingAutomaticOf(t, tc.words, true, "dispatch_issue_update", update(tc.extra), goingHolds{}); got != tc.automatic {
				t.Fatalf("%q with %v is automatic=%v, want %v", tc.words, tc.extra, got, tc.automatic)
			}
		})
	}
}

// TestGoingAutomaticHoldsAnUploadsNameToTheWordsWhereItsOwnerHoldsThatDocument (review round 2,
// finding A; round 6, finding 2): an upload whose name is a document the page's issue already
// holds would write that document's next version, so it is offered only where it carries force -
// without it every write of it is refused - and the person's own words write the name, and then
// runs at once; any other such upload is withheld, the offer saying why. An upload under a name
// the issue holds no document of makes one and runs at once, and one whose owner could not be read
// is an offer.
func TestGoingAutomaticHoldsAnUploadsNameToTheWordsWhereItsOwnerHoldsThatDocument(t *testing.T) {
	upload := func(name string, force bool) map[string]any {
		arguments := map[string]any{"issue": "DEMO-12", "name": name, "content": "# Notes\n\nModel-written text.\n"}
		if force {
			arguments["force"] = true
		}
		return arguments
	}
	unread := func(artifactTarget, string) (*goingHeldDocument, bool) { return nil, false }
	for _, tc := range []struct {
		name, words, document string
		force                 bool
		holds                 goingDocumentHeld
		withheld, automatic   bool
	}{
		{"a name the issue holds a document of that only stored text wrote", "save this as a doc", "spec", false, goingDocumentsHeld("DEMO-12/spec"), true, false},
		{"a name the issue holds a document of that only stored text wrote, with force", "save this as a doc", "spec", true, goingDocumentsHeld("DEMO-12/spec"), true, false},
		{"a name the issue holds a document of that the words write, without force", "save this over the spec", "spec", false, goingDocumentsHeld("DEMO-12/spec"), true, false},
		{"a name the issue holds a document of that the words write, with force", "save this over the spec", "spec", true, goingDocumentsHeld("DEMO-12/spec"), false, true},
		{"a name the issue holds no document of", "save this as a doc", "summary", false, goingDocumentsHeld("DEMO-12/spec"), false, true},
		{"a name another owner holds a document of", "save this as a doc", "spec", false, goingDocumentsHeld("DEMO-7/spec"), false, true},
		{"a name whose owner could not be read", "save this as a doc", "summary", false, unread, false, false},
		{"a name with no read of the store", "save this as a doc", "summary", false, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			holds := goingHolds{document: tc.holds}
			if tc.withheld {
				written, err := json.Marshal(map[string][]map[string]any{"dispatch_artifact": {upload(tc.document, tc.force)}})
				if err != nil {
					t.Fatal(err)
				}
				start := goingBrowserStart(t)
				start.words = tc.words
				start.chip = &goingContext{Path: "/issues/DEMO-12", Project: "DEMO", Issue: "DEMO-12"}
				proposal := goingPropose(goingDecision{Context: going.ContextPage, offered: []string{"dispatch_artifact"}},
					&going.Final{Answer: "Done.", Arguments: string(written)}, start, holds)
				if len(proposal.Offers) != 1 || proposal.Offers[0].Offered || len(proposal.Offers[0].Withheld) != 1 {
					t.Fatalf("%q uploading as %q (force %v) proposed %+v, want the upload withheld, saying why", tc.words, tc.document, tc.force, proposal)
				}
				return
			}
			if got := goingAutomaticOf(t, tc.words, true, "dispatch_artifact", upload(tc.document, tc.force), holds); got != tc.automatic {
				t.Fatalf("%q uploading as %q (force %v) is automatic=%v, want %v", tc.words, tc.document, tc.force, got, tc.automatic)
			}
		})
	}
}

// TestGoingUploadOverAHeldDocumentIsOfferedWhereTheWordsNameItAsItIsStored (deep review round 5,
// finding D; review round 6, finding C; review round 7, finding 1): an upload with force under the
// name of a document the issue holds is offered only where the answer wrote the name that document
// is stored under, its file name, and the person's words write that name as a whole word. An
// upload that finds the document by its slug is always withheld, whatever the words: every issue's
// spec is stored as spec.md under the slug spec, so "replace spec.md with this" offers spec.md,
// while "replace the spec with this" and "add a note to the spec" offer no upload named spec or
// spec.md. Words that write only the document's name without its extension name nothing: "add a
// note" (note.md), "add a todo for the release" (todo.md) and "add a note to the plan" (plan.md)
// offer none of them.
func TestGoingUploadOverAHeldDocumentIsOfferedWhereTheWordsNameItAsItIsStored(t *testing.T) {
	handler, _, _, served := newRegisteredTestServer(t, testServerOptions{})
	issue := createInteractionIssue(t, handler, "DEMO", "An issue holding notes", "The spec says hello.\n").Key
	for _, name := range []string{"notes.md", "note.md", "todo.md", "plan.md"} {
		uploadDocument(t, handler, "/api/v1/issues/"+issue, name, "# Held\n\nThe first line.\n")
	}
	holds := served.goingHoldsBy(context.Background())
	for _, tc := range []struct {
		name, words, upload string
		force, offered      bool
	}{
		{"the name itself", "replace notes.md with this text", "notes.md", true, true},
		{"the slug, where the answer wrote the slug", "replace notes-md with this", "notes-md", true, false},
		{"the slug, where the answer wrote the name", "replace notes-md with this", "notes.md", true, false},
		{"the stored name, where the answer wrote the slug", "overwrite notes.md with this text", "notes-md", true, false},
		{"another document's name", "replace note.md with this", "note.md", true, true},
		{"the name without its extension", "overwrite the notes with this text", "notes.md", true, false},
		{"the spec's name as the answer wrote it", "replace spec.md with this", "spec.md", true, true},
		{"the spec's slug as the answer wrote it", "replace the spec with this", "spec", true, false},
		{"a note to the spec, where the answer wrote its slug", "add a note to the spec", "spec", true, false},
		{"the spec's slug, where the answer wrote spec.md", "replace the spec with this", "spec.md", true, false},
		{"a note to the spec", "add a note to the spec", "spec.md", true, false},
		{"a note", "add a note", "note.md", true, false},
		{"a todo", "add a todo for the release", "todo.md", true, false},
		{"a note to the plan", "add a note to the plan", "plan.md", true, false},
		{"a longer name the stem runs on into", "replace the notes-old with this", "notes.md", true, false},
		{"no document at all", "save this as a doc", "notes.md", true, false},
		{"the name, without force", "overwrite notes.md with this text", "notes.md", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arguments := map[string]any{"issue": issue, "name": tc.upload, "content": "# Notes\n\nNew words.\n"}
			if tc.force {
				arguments["force"] = true
			}
			written, err := json.Marshal(map[string][]map[string]any{"dispatch_artifact": {arguments}})
			if err != nil {
				t.Fatal(err)
			}
			start := goingBrowserStart(t)
			start.words = tc.words
			start.chip = &goingContext{Path: "/issues/" + issue, Project: "DEMO", Issue: issue}
			proposal := goingPropose(goingDecision{Context: going.ContextPage, offered: []string{"dispatch_artifact"}},
				&going.Final{Answer: "Done.", Arguments: string(written)}, start, holds)
			if len(proposal.Offers) != 1 {
				t.Fatalf("%q uploading as %q proposed %+v, want one offer", tc.words, tc.upload, proposal)
			}
			offer := proposal.Offers[0]
			offered := offer.Offered && len(offer.Invocations) == 1 && len(offer.Withheld) == 0
			withheld := !offer.Offered && len(offer.Invocations) == 0 && len(offer.Withheld) == 1
			if (tc.offered && !offered) || (!tc.offered && !withheld) {
				t.Fatalf("%q uploading as %q (force %v) proposed %+v, want offered=%v", tc.words, tc.upload, tc.force, offer, tc.offered)
			}
		})
	}
}

// TestGoingUploadOverAHeldDocumentIsWithheldUnlessTheWordsWriteTheNameTheAnswerWrote (review round
// 6, finding C; review round 7, finding 1): goingUploadWithheld offers an upload with force over a
// held document only where the answer wrote the name that document is stored under and the words
// write it, as a whole word and whatever its case. An upload that finds the document by its slug
// is withheld however the words read, its reason naming the stored name the person would have to
// write; the document's name without the extension counts for nothing. So "add a note to the
// spec" and "replace the spec with this" withhold an upload named spec or spec.md over a spec
// stored as spec.md under the slug spec, and "add a note" one named note.md or a.md.
func TestGoingUploadOverAHeldDocumentIsWithheldUnlessTheWordsWriteTheNameTheAnswerWrote(t *testing.T) {
	issue := "DEMO-12"
	for _, tc := range []struct {
		stored, slug, upload, words string
		force, offered              bool
	}{
		{"spec.md", "spec", "spec.md", "add a note to the spec", true, false},
		{"spec.md", "spec", "spec.md", "replace the spec with this", true, false},
		{"spec.md", "spec", "spec.md", "replace SPEC.MD with this", true, true},
		{"spec.md", "spec", "spec", "replace the spec with this", true, false},
		{"spec.md", "spec", "spec", "add a note to the spec", true, false},
		{"spec.md", "spec", "spec", "replace spec.md with this", true, false},
		{"spec.md", "spec", "spec", "replace the specification with this", true, false},
		{"note.md", "note-md", "note.md", "add a note", true, false},
		{"a.md", "a-md", "a.md", "add a note", true, false},
		{"a.md", "a-md", "a.md", "overwrite a.md with this", true, true},
		{"notes.md", "notes-md", "notes.md", "replace notes-md with this", true, false},
		{"notes.md", "notes-md", "notes-md", "replace notes-md with this", true, false},
		{"notes.md", "notes-md", "notes.md", "overwrite the notes with this text", true, false},
		{"notes.md", "notes-md", "notes.md", "overwrite notes.md with this text", false, false},
	} {
		held := func(owner artifactTarget, name string) (*goingHeldDocument, bool) {
			if owner.IssueKey == nil || *owner.IssueKey != issue || (name != tc.stored && name != tc.slug) {
				return nil, true
			}
			return &goingHeldDocument{Name: tc.stored, Slug: tc.slug}, true
		}
		arguments := map[string]any{"issue": issue, "name": tc.upload, "content": "# Held\n\nNew words.\n"}
		if tc.force {
			arguments["force"] = true
		}
		withheld := goingUploadWithheld("dispatch_artifact", arguments, "", tc.words, held)
		if (withheld == "") != tc.offered {
			t.Errorf("%q uploading as %q over %s (slug %s, force %v) is withheld %q, want offered=%v",
				tc.words, tc.upload, tc.stored, tc.slug, tc.force, withheld, tc.offered)
		}
		if withheld != "" && !strings.Contains(withheld, tc.stored) {
			t.Errorf("%q uploading as %q over %s (slug %s) is withheld %q, which never names %s, the name to write",
				tc.words, tc.upload, tc.stored, tc.slug, withheld, tc.stored)
		}
		if internals := goingInternals.FindAllString(withheld, -1); len(internals) > 0 {
			t.Errorf("%q uploading as %q over %s is withheld %q, which names %q, none of which the person at the bar holds",
				tc.words, tc.upload, tc.stored, withheld, internals)
		}
	}
}

// TestGoingUploadRefusalsSayWhatThePersonCanDoInTheBar: each reason the writes route gives for an
// upload it does not send - one over a document the words do not ask for, by its slug or by its
// stored name, one whose name the person filled in, one with no force over a held name or over an
// owner it cannot name, and one sent at once whose owner it cannot name - reaches the person in
// the bar's panel. So it names the name to write in the question, the document's stored name or
// the upload's own, and none of goingInternals; and each keeps the status and the code it is
// answered with (goingWriteClaim.refuse). The one refused for being sent at once tells the person
// to pick or confirm nothing: the bar shows no pick of an invocation it sent at once, so writing
// the name in the question is what that person can do.
func TestGoingUploadRefusalsSayWhatThePersonCanDoInTheBar(t *testing.T) {
	issueUpload := GoingRoute{Method: http.MethodPost, Pattern: "/api/v1/issues/{key}/artifacts"}
	unnamedUpload := GoingRoute{Method: http.MethodPost, Pattern: "/api/v1/somewhere/{key}/artifacts"}
	plan := &model.Artifact{Name: "plan.md", Slug: "plan-md", Kind: "doc", CreatedBy: model.Actor{Kind: "user", ID: "alice"},
		Versions: []model.Version{{Number: 2, Authors: []model.Actor{{Kind: "user", ID: "bob"}}}}}
	content := "# Plan\n\nWritten by the model.\n"
	unnamedPrepared := goingPrepared{action: "dispatch_artifact", route: unnamedUpload,
		arguments: map[string]any{"name": "notes", "content": content}, call: goingCall{path: map[string]string{"key": "DEMO-12"}}}
	unnamed, err := (goingObserver{}).uploadedOver(context.Background(), unnamedPrepared, goingFields("artifact"), "")
	if err != nil || !unnamed.ownerUnnamed {
		t.Fatalf("read what an upload to an owner it cannot name writes over: %+v, %v", unnamed, err)
	}
	// answered is a refusal as the writes route answers it (goingWriteClaim.refuse).
	answered := func(err error) (int, string, string) {
		var argument GoingArgumentError
		if errors.As(err, &argument) {
			return http.StatusBadRequest, codeGoingArguments, argument.Reason
		}
		var refused *apiError
		if errors.As(err, &refused) {
			return refused.status, refused.code, refused.message
		}
		return 0, "", fmt.Sprint(err)
	}
	for _, tc := range []struct {
		name, approved string
		route          GoingRoute
		arguments      map[string]any
		before         goingObservation
		person         goingPerson
		status         int
		code, write    string
	}{
		{"its slug, which the words do not ask for", goingApprovedPerson, issueUpload,
			map[string]any{"issue": "DEMO-12", "name": "plan-md", "content": content, "force": true},
			goingObservation{held: plan}, goingPerson{words: "save this as plan-md"}, http.StatusConflict, codeGoingNotProposed, "plan.md"},
		{"its stored name, which the words do not write", goingApprovedConfirmed, issueUpload,
			map[string]any{"issue": "DEMO-12", "name": "plan.md", "content": content, "force": true},
			goingObservation{held: plan}, goingPerson{words: "save this as the plan"}, http.StatusConflict, codeGoingNotProposed, "plan.md"},
		{"its slug, filled in by the person", goingApprovedPerson, issueUpload,
			map[string]any{"issue": "DEMO-12", "name": "plan-md", "content": content, "force": true},
			goingObservation{held: plan}, goingPerson{words: "save this to the issue", filled: "name"}, http.StatusConflict, codeGoingNotProposed, "plan.md"},
		{"its stored name, filled in by the person", goingApprovedPerson, issueUpload,
			map[string]any{"issue": "DEMO-12", "name": "plan.md", "content": content, "force": true},
			goingObservation{held: plan}, goingPerson{words: "save this to the issue", filled: "name"}, http.StatusConflict, codeGoingNotProposed, "plan.md"},
		{"a held name, with no force", goingApprovedAuto, issueUpload,
			map[string]any{"issue": "DEMO-12", "name": "plan.md", "content": content},
			goingObservation{held: plan}, goingPerson{words: "replace plan.md with this"}, http.StatusBadRequest, codeGoingArguments, "plan.md"},
		{"an owner it cannot name, with no force", goingApprovedPerson, unnamedUpload,
			map[string]any{"name": "notes", "content": content},
			unnamed, goingPerson{words: "save this as a doc"}, http.StatusBadRequest, codeGoingArguments, "notes"},
		{"an owner it cannot name, sent at once", goingApprovedAuto, unnamedUpload,
			map[string]any{"name": "notes", "content": content, "force": true},
			unnamed, goingPerson{words: "save this as a doc"}, http.StatusConflict, codeGoingNotAutomatic, "notes"},
	} {
		prepared := goingPrepared{action: "dispatch_artifact", route: tc.route, arguments: tc.arguments,
			call: goingCall{path: map[string]string{"key": "DEMO-12"}}}
		status, code, reason := answered(goingSendRefusal(tc.approved, prepared, tc.before, tc.person))
		if status != tc.status || code != tc.code {
			t.Errorf("%s: the upload is answered %d %s %q, want %d %s", tc.name, status, code, reason, tc.status, tc.code)
		}
		if !strings.Contains(reason, tc.write) {
			t.Errorf("%s: the upload is refused %q, which never names %s, the name to write in the question", tc.name, reason, tc.write)
		}
		if internals := goingInternals.FindAllString(reason, -1); len(internals) > 0 {
			t.Errorf("%s: the upload is refused %q, which names %q, none of which the person at the bar holds", tc.name, reason, internals)
		}
		if tc.code == codeGoingNotAutomatic && goingPickOrConfirm.MatchString(reason) {
			t.Errorf("%s: the upload is refused %q, which tells the person to pick or confirm an invocation the bar sent at once and shows no pick of", tc.name, reason)
		}
	}
}

// goingPickOrConfirm is a reason telling the person to pick or to confirm a write.
var goingPickOrConfirm = regexp.MustCompile(`(?i)\b(pick|confirm)`)

// goingUnsendableCalls is a call of each action the bar can never send as its answer wrote it,
// by the argument that makes it so: a picture file or a file to read the text from, which only
// the machine the answer was written for can open, a dispatch:// link naming where the call goes,
// an upload with no text, a closing note on an update that does not close, and a source
// repository not written as owner/name. Each is a call the same arguments without that one make,
// and the bar sends, except that an upload without its file needs its text instead.
var goingUnsendableCalls = []struct {
	name, action, argument string
	arguments, sendable    map[string]any
}{
	{"a comment's pictures", "dispatch_comment", "images",
		map[string]any{"issue": "DEMO-12", "body": "See the screenshot.", "images": []any{"shot.png"}},
		map[string]any{"issue": "DEMO-12", "body": "See the screenshot."}},
	{"a comment's dispatch:// link", "dispatch_comment", "ref",
		map[string]any{"issue": "DEMO-12", "body": "On the spec.", "ref": "dispatch://DEMO-12/spec"},
		map[string]any{"issue": "DEMO-12", "body": "On the spec."}},
	{"a question's pictures", "dispatch_ask", "images",
		map[string]any{"issue": "DEMO-12", "question": "Ship it as shown?", "images": []any{"shot.png"}},
		map[string]any{"issue": "DEMO-12", "question": "Ship it as shown?"}},
	{"a message's pictures", "dispatch_message", "images",
		map[string]any{"issue": "DEMO-12", "body": "See the screenshot.", "images": []any{"shot.png"}},
		map[string]any{"issue": "DEMO-12", "body": "See the screenshot."}},
	{"a suggestion's dispatch:// link", "dispatch_suggest", "ref",
		map[string]any{"issue": "DEMO-12", "quote": "first line", "replace_with": "opening line", "ref": "dispatch://DEMO-12/spec"},
		map[string]any{"issue": "DEMO-12", "quote": "first line", "replace_with": "opening line"}},
	{"a document edit's dispatch:// link", "dispatch_doc_edit", "ref",
		map[string]any{"issue": "DEMO-12", "artifact": "notes", "ops": []any{map[string]any{"op": "replace", "find": "first line", "with": "opening line"}}, "ref": "dispatch://DEMO-12/notes"},
		map[string]any{"issue": "DEMO-12", "artifact": "notes", "ops": []any{map[string]any{"op": "replace", "find": "first line", "with": "opening line"}}}},
	{"an upload's file", "dispatch_artifact", "path",
		map[string]any{"issue": "DEMO-12", "name": "notes.md", "path": "./notes.md"},
		map[string]any{"issue": "DEMO-12", "name": "notes.md", "content": "# Notes\n"}},
	{"an upload with no text", "dispatch_artifact", "content",
		map[string]any{"issue": "DEMO-12", "name": "notes.md"},
		map[string]any{"issue": "DEMO-12", "name": "notes.md", "content": "# Notes\n"}},
	{"a closing note on an update that does not close", "dispatch_issue_update", "reason",
		map[string]any{"issue": "DEMO-12", "status": "in_progress", "reason": "Started on it."},
		map[string]any{"issue": "DEMO-12", "status": "in_progress"}},
	{"a source repository not written as owner/name", "dispatch_architecture_sync", "repo",
		map[string]any{"project": "DEMO", "repo": "widgets"},
		map[string]any{"project": "DEMO", "repo": "acme/widgets"}},
}

// TestGoingProposalWithholdsACallTheBarCanNeverSend (deep review, Minor finding): the answer
// wrote a call the bar can never send (goingUnsendableCalls) - a comment with a picture file, one
// naming its place by a dispatch:// link, and the rest - and the person's words name its issue, so
// it would have been offered and marked automatic, a submit then running a write that can only be
// refused. The proposal withholds it instead, as it withholds an upload over a held document:
// no invocation of it is offered, none is automatic, and the offer's withheld says why in words
// for the person at the bar: it names nothing of goingInternals, and says what of the call the
// bar cannot send - a picture, a link, a file - rather than giving the argument's name as the
// reason, which says nothing to a person who did not write the call. The same call without that
// argument is offered.
func TestGoingProposalWithholdsACallTheBarCanNeverSend(t *testing.T) {
	catalogue, err := BuildGoingCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	start := goingRunStart{actions: catalogue.Actions(), words: "post this on DEMO-12 and sync DEMO"}
	propose := func(action string, arguments map[string]any) goingOffer {
		t.Helper()
		written, err := json.Marshal(map[string][]map[string]any{action: {arguments}})
		if err != nil {
			t.Fatal(err)
		}
		proposal := goingPropose(goingDecision{Context: going.ContextNamed, offered: []string{action}},
			&going.Final{Answer: "Done.", Arguments: string(written)}, start, goingHolds{})
		if len(proposal.Offers) != 1 {
			t.Fatalf("%s proposed %+v, want the one offer", action, proposal)
		}
		return proposal.Offers[0]
	}
	for _, tc := range goingUnsendableCalls {
		if sendable := propose(tc.action, tc.sendable); !sendable.Offered || len(sendable.Invocations) != 1 || len(sendable.Withheld) != 0 {
			t.Errorf("%s: the call without %s proposed %+v, want it offered", tc.name, tc.argument, sendable)
			continue
		}
		offer := propose(tc.action, tc.arguments)
		if offer.Offered || len(offer.Invocations) != 0 || len(offer.Withheld) != 1 {
			t.Errorf("%s: the call proposed %+v, want it withheld, saying why, and no invocation of it offered", tc.name, offer)
			continue
		}
		why := offer.Withheld[0]
		if internals := goingInternals.FindAllString(why, -1); len(internals) > 0 {
			t.Errorf("%s: the call is withheld %q, which names %q, none of which the person at the bar holds", tc.name, why, internals)
		}
		if regexp.MustCompile(`\b` + tc.argument + `\b`).MatchString(why) {
			t.Errorf("%s: the call is withheld %q, which gives the argument %s as its reason rather than saying what the bar cannot send", tc.name, why, tc.argument)
		}
	}
}

// TestGoingWriteTheBarCanNeverSendIsRefusedInTheBarsWords (deep review, Minor finding): a run
// recorded by a build that offered a call the bar can never send - a comment with a picture file,
// and one naming its place by a dispatch:// link, each marked automatic - is refused when the
// person picks it, 400 GOING_ARGUMENTS, with nothing written, and the refusal is the words the
// proposal now withholds it with: no tool id before it, nothing of goingInternals, and what of
// the call the bar cannot send rather than the argument's name as the reason.
func TestGoingWriteTheBarCanNeverSendIsRefusedInTheBarsWords(t *testing.T) {
	rig := newGoingWritesRig(t)
	issue := createInteractionIssue(t, rig.mux, "DEMO", "Commented on", "The spec.\n").Key
	catalogue, err := BuildGoingCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	comments := func() int {
		t.Helper()
		return len(decodeBody[[]model.Comment](t, rig.fixture(t, "alice", http.MethodGet, "/api/v1/issues/"+issue+"/comments", nil)))
	}
	for _, tc := range goingUnsendableCalls {
		if tc.action != "dispatch_comment" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			arguments := maps.Clone(tc.arguments)
			arguments["issue"] = issue
			proposal := goingProposal{Offers: []goingOffer{{Action: tc.action, Offered: true, Invocations: []goingInvocation{
				{Arguments: arguments, Automatic: true, TargetArgument: "issue"},
			}}}}
			run := rig.recorded(t, "alice", true, tc.action, "post this on "+issue, proposal)
			refused := rig.write(t, "alice", run, map[string]any{"offer": tc.action, "invocation": 0, "approved": goingApprovedPerson})
			refusal := decodeBody[errorBody](t, refused)
			if refused.Code != http.StatusBadRequest || refusal.Code != codeGoingArguments {
				t.Fatalf("the pick answered %d %s, want 400 %s", refused.Code, refused.Body.String(), codeGoingArguments)
			}
			if internals := goingInternals.FindAllString(refusal.Error, -1); len(internals) > 0 {
				t.Errorf("the pick is refused %q, which names %q, none of which the person at the bar holds", refusal.Error, internals)
			}
			if regexp.MustCompile(`\b` + tc.argument + `\b`).MatchString(refusal.Error) {
				t.Errorf("the pick is refused %q, which gives the argument %s as its reason rather than saying what the bar cannot send", refusal.Error, tc.argument)
			}
			start := goingRunStart{actions: catalogue.Actions(), words: "post this on " + issue}
			written, _ := json.Marshal(map[string][]map[string]any{tc.action: {arguments}})
			withheld := goingPropose(goingDecision{Context: going.ContextNamed, offered: []string{tc.action}},
				&going.Final{Answer: "Done.", Arguments: string(written)}, start, goingHolds{}).Offers[0].Withheld
			if len(withheld) != 1 || withheld[0] != refusal.Error {
				t.Errorf("the pick is refused %q, and a proposal made now withholds the call with %q, want the same words", refusal.Error, withheld)
			}
			if count := comments(); count != 0 {
				t.Fatalf("%s holds %d comments after the refused pick, want none", issue, count)
			}
		})
	}
}

// goingEmptyArguments is, by action, each argument goingUnsendable refuses a call for carrying, as
// an answer may write it empty: a list of no pictures, a link or a file path of no text. Such a
// call names no picture, no link and no file, and no translator sends the argument on, so the
// bar sends the call as it would without it.
var goingEmptyArguments = map[string]map[string]any{
	"dispatch_comment":  {"images": []any{}, "ref": ""},
	"dispatch_ask":      {"images": []any{}},
	"dispatch_message":  {"images": []any{}},
	"dispatch_suggest":  {"ref": ""},
	"dispatch_doc_edit": {"ref": ""},
	"dispatch_artifact": {"path": ""},
}

// TestGoingCallWithAnEmptyPictureListLinkOrFileIsOfferedAndSent (deep review, Minor finding): an
// answer that wrote an empty list of pictures, an empty link or an empty file path named no
// picture, link or file, so no reason the bar can never send the call is true of it. For every row
// of those actions a person's browser reaches (goingRowCases), the call with each such argument
// empty is offered, with nothing withheld, and the person's pick of it, every such argument empty
// at once, is sent and lands.
func TestGoingCallWithAnEmptyPictureListLinkOrFileIsOfferedAndSent(t *testing.T) {
	rig := newGoingWritesRig(t)
	cases := goingRowCases(newGoingRowFixtures(t, rig))
	catalogue, err := BuildGoingCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	const words = "do what the answer says"
	start := goingBrowserStart(t)
	start.words = words
	for _, action := range catalogue.Actions() {
		empties := goingEmptyArguments[action.Action]
		if len(empties) == 0 {
			continue
		}
		for _, route := range action.Routes {
			row := action.Action + " " + route.Method + " " + route.Pattern
			call, held := cases[row]
			if !held || call.bearer || call.session != "" {
				continue
			}
			t.Run(row, func(t *testing.T) {
				arguments := maps.Clone(call.arguments)
				for argument, empty := range empties {
					one := maps.Clone(call.arguments)
					one[argument] = empty
					written, err := json.Marshal(map[string][]map[string]any{action.Action: {one}})
					if err != nil {
						t.Fatal(err)
					}
					offer := goingPropose(goingDecision{Context: going.ContextNamed, offered: []string{action.Action}},
						&going.Final{Answer: "Done.", Arguments: string(written)}, start, goingHolds{}).Offers[0]
					if !offer.Offered || len(offer.Invocations) != 1 || len(offer.Withheld) != 0 {
						t.Errorf("the call with %s %#v proposed %+v, want it offered with nothing withheld", argument, empty, offer)
					}
					arguments[argument] = empty
				}
				proposal := goingProposal{Offers: []goingOffer{{Action: action.Action, Offered: true, Invocations: []goingInvocation{
					{Arguments: arguments, TargetArgument: goingTargetArgument(route)},
				}}}}
				run := rig.recorded(t, "alice", false, action.Action, words, proposal)
				response := rig.write(t, "alice", run, map[string]any{"offer": action.Action, "invocation": 0, "approved": goingApprovedPerson})
				if response.Code != http.StatusOK {
					t.Fatalf("the pick of %v answered %d %s, want it sent", arguments, response.Code, response.Body.String())
				}
				if line := decodeBody[goingWriteLine](t, response); !line.Outcome.OK {
					t.Errorf("the pick of %v answered %+v, want it landed", arguments, line.Outcome)
				}
			})
		}
	}
}

// TestGoingUploadOfferedForWordsNamingTheDocumentRunsWhenThePersonPicksIt (deep review round 5,
// finding D): an upload with force the proposal offers because the answer wrote the held
// document's stored name and the words write it, whatever its case, is one the person can run:
// picked as the bar picks an offer (approved person), the write is made and the document's next
// version written.
func TestGoingUploadOfferedForWordsNamingTheDocumentRunsWhenThePersonPicksIt(t *testing.T) {
	rig := newGoingWritesRig(t)
	issue := createInteractionIssue(t, rig.mux, "DEMO", "An issue holding notes", "The spec.\n").Key
	uploadDocument(t, rig.mux, "/api/v1/issues/"+issue, "notes.md", "# Notes\n\nThe first line.\n")
	holds := rig.served.goingHoldsBy(context.Background())
	for index, tc := range []struct{ words, upload string }{
		{"overwrite notes.md with this text", "notes.md"},
		{"replace NOTES.MD with this", "notes.md"},
	} {
		words := tc.words
		t.Run(words, func(t *testing.T) {
			arguments := map[string]any{"issue": issue, "name": tc.upload, "content": "# Notes\n\nNew words.\n", "force": true}
			written, err := json.Marshal(map[string][]map[string]any{"dispatch_artifact": {arguments}})
			if err != nil {
				t.Fatal(err)
			}
			start := goingBrowserStart(t)
			start.words = words
			start.chip = &goingContext{Path: "/issues/" + issue, Project: "DEMO", Issue: issue}
			proposal := goingPropose(goingDecision{Context: going.ContextPage, offered: []string{"dispatch_artifact"}},
				&going.Final{Answer: "Done.", Arguments: string(written)}, start, holds)
			if len(proposal.Offers) != 1 || !proposal.Offers[0].Offered || len(proposal.Offers[0].Invocations) != 1 {
				t.Fatalf("%q proposed %+v, want the upload offered", words, proposal)
			}
			run := rig.recorded(t, "alice", true, "dispatch_artifact", words, proposal)
			line := rig.wrote(t, "alice", run, map[string]any{"offer": "dispatch_artifact", "invocation": 0, "approved": goingApprovedPerson})
			if !line.Outcome.OK {
				t.Fatalf("the picked upload under %q answered %+v, want it made", words, line)
			}
			var versions int
			if err := rig.database.Pool.QueryRow(context.Background(), `
				select count(*) from artifact_versions v join artifacts a on a.id = v.artifact_id
				where a.issue_key = $1 and a.name = 'notes.md'`, issue).Scan(&versions); err != nil {
				t.Fatal(err)
			}
			if versions != index+2 {
				t.Fatalf("notes.md holds %d versions after the picked upload under %q, want %d", versions, words, index+2)
			}
		})
	}
}

// TestGoingAutomaticNeverRunsAnUploadOverADocumentByItsSlug (review round 7, finding 1): an upload
// runs at once where its owner holds no document of its name, or where the answer wrote the name
// the document it finds is stored under and the words write that name. One whose name finds a
// held document only as its slug is never automatic, whatever the words: every issue's spec is
// stored as spec.md under the slug spec, so words that write "the spec" never run an upload named
// spec at once over it. Where the owner cannot be read, the words writing the name still decide.
func TestGoingAutomaticNeverRunsAnUploadOverADocumentByItsSlug(t *testing.T) {
	spec := func(owner artifactTarget, name string) (*goingHeldDocument, bool) {
		if owner.IssueKey == nil || *owner.IssueKey != "DEMO-12" || (name != "spec.md" && name != "spec") {
			return nil, true
		}
		return &goingHeldDocument{Name: "spec.md", Slug: "spec"}, true
	}
	unread := func(artifactTarget, string) (*goingHeldDocument, bool) { return nil, false }
	chip := &goingContext{Path: "/issues/DEMO-12", Project: "DEMO", Issue: "DEMO-12"}
	for _, tc := range []struct {
		words, upload string
		holds         goingDocumentHeld
		automatic     bool
	}{
		{"replace the spec with this", "spec", spec, false},
		{"add a note to the spec", "spec", spec, false},
		{"replace spec.md with this", "spec", spec, false},
		{"replace spec.md with this", "spec.md", spec, true},
		{"replace the spec with this", "spec.md", spec, false},
		{"save this as the summary", "summary", spec, true},
		{"save this as a doc", "summary", spec, true},
		{"save this as the summary", "summary", unread, true},
		{"save this as a doc", "summary", unread, false},
	} {
		arguments := map[string]any{"issue": "DEMO-12", "name": tc.upload, "content": "# Held\n\nNew words.\n", "force": true}
		named := goingNamedBy(tc.words, testServerURL)
		if got := goingAutomatic("dispatch_artifact", arguments, chip, "", named, goingHolds{document: tc.holds}); got != tc.automatic {
			t.Errorf("%q uploading as %q is automatic=%v, want %v", tc.words, tc.upload, got, tc.automatic)
		}
	}
}

// goingPathArgumentsNamingNoItem are the arguments a row of the catalogue binds into its path that
// name no item of Dispatch's: a handoff's phase, of which the write appends the next version on
// the issue the call names.
var goingPathArgumentsNamingNoItem = map[string]bool{"phase": true}

// TestGoingAutomaticClassesEveryArgumentOfTheCatalogue (review round 2, finding C): every
// argument of every action is classed exactly once - an item (goingItemArguments), a hand-over
// (goingHeldArguments) or content (goingContentArguments) - every argument a row binds into its
// path and every argument the bar prompts for as an item is an item, and the classes hold no
// argument the catalogue does not have. An argument no class holds runs nothing at once.
func TestGoingAutomaticClassesEveryArgumentOfTheCatalogue(t *testing.T) {
	catalogue, err := BuildGoingCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	arguments := map[string]bool{}
	for _, action := range catalogue.Actions() {
		for _, field := range action.Request {
			arguments[field.Name] = true
			classes := 0
			for _, classed := range []bool{goingItemArguments[field.Name] != "", goingHeldArguments[field.Name], goingContentArguments[field.Name]} {
				if classed {
					classes++
				}
			}
			if classes != 1 {
				t.Errorf("%s's argument %s is in %d classes, want exactly one", action.Action, field.Name, classes)
			}
			if _, item := goingItemFields[field.Name]; item && goingItemArguments[field.Name] == "" {
				t.Errorf("%s's argument %s is prompted for as an item and not classed as one", action.Action, field.Name)
			}
		}
		for _, route := range action.Routes {
			for placeholder, argument := range route.path {
				if argument != goingCallerSession && goingItemArguments[argument] == "" && !goingPathArgumentsNamingNoItem[argument] {
					t.Errorf("%s %s binds %s to %s, which is not classed as an item", route.Method, route.Pattern, placeholder, argument)
				}
			}
		}
		if upload, uploads := goingUploadNames[action.Action]; uploads && !slices.ContainsFunc(action.Request, func(field contracts.ToolField) bool { return field.Name == upload }) {
			t.Errorf("%s takes no argument %s, which goingUploadNames names", action.Action, upload)
		}
	}
	classed := len(goingItemArguments) + len(goingHeldArguments) + len(goingContentArguments)
	if classed != len(arguments) {
		t.Errorf("the classes hold %d arguments and the catalogue %d: a class names an argument no action takes", classed, len(arguments))
	}
	named := goingNamedBy("mark DEMO-12 done", testServerURL)
	if goingAutomatic("dispatch_issue_update", map[string]any{"issue": "DEMO-12", "status": "done", "watcher": "DEMO-99"}, nil, "", named, goingHolds{}) {
		t.Error("an invocation with an argument no class holds is automatic")
	}
}
