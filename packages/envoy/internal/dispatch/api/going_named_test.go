// going_named_test.go holds which invocations of a proposal run at once to what the person's own
// typed words name: an item is named only by a reference to it as Dispatch reads references in
// text, a document only under an owner so named, a hand-over of the work - its route, its
// assignee - only where the words name it or the issue already holds it, and an upload over a
// document its owner holds only where the words write its name.
package api

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/going"
	"github.com/sjawhar/envoy/internal/contracts"
)

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
// finding D; review round 6, finding C): an upload with force under the name of a document the
// issue holds is offered where the person's words name that document as the store holds it - its
// name or its slug, the names the upload finds it by, whichever of them the answer wrote as the
// upload's name - and withheld, saying why, where the words name another document or none, or the
// upload carries no force. Words that write only the document's name without its extension do not
// name it: "add a note" writes the stem of note.md, "add a todo for the release" that of todo.md
// and "add a note to the plan" that of plan.md, each a word typed for every other reason, and none
// of them offers to replace that document whole.
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
		{"the slug", "replace notes-md with this", "notes.md", true, true},
		{"the name itself", "replace notes.md with this text", "notes.md", true, true},
		{"the stored name, where the answer wrote the slug", "overwrite notes.md with this text", "notes-md", true, true},
		{"another document's name", "replace note.md with this", "note.md", true, true},
		{"the name without its extension", "overwrite the notes with this text", "notes.md", true, false},
		{"the slug of the issue's own spec.md", "replace the spec with this", "spec.md", true, true},
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

// TestGoingHeldDocumentIsNamedByItsStoredNameOrSlugAlone (review round 5, thread 5; review round
// 6, finding C): the words name a held document by the name it is stored under or its slug, and by
// its name without the extension only where that is the stored name itself, a document stored
// with no extension. A stem the answer did not write as the upload's name is a word people type
// for every other reason: "add a note" names neither a.md nor note.md, and "the notes" and "the
// spec" no longer name notes.md and spec.md stored under the slugs an upload gives them (notes-md,
// spec-md); an issue's own spec.md, whose slug is spec, is named by "the spec" as by its slug.
func TestGoingHeldDocumentIsNamedByItsStoredNameOrSlugAlone(t *testing.T) {
	for _, tc := range []struct {
		document, words string
		named           bool
	}{
		{"a.md", "add a note", false},
		{"to.md", "send it to the team", false},
		{"add.md", "add a note", false},
		{"note.md", "add a note", false},
		{"todo.md", "add a todo for the release", false},
		{"plan.md", "add a note to the plan", false},
		{"notes.md", "overwrite the notes with this text", false},
		{"spec.md", "replace the spec with this", false},
		{"a.md", "overwrite a.md with this", true},
		{"a.md", "overwrite a-md with this", true},
		{"note.md", "replace note.md with this", true},
		{"plan", "add a note to the plan", true},
	} {
		held := goingHeldDocument{Name: tc.document, Slug: artifactSlug(tc.document)}
		if got := held.namedBy(tc.words); got != tc.named {
			t.Errorf("%q names %s (slug %s): %v, want %v", tc.words, held.Name, held.Slug, got, tc.named)
		}
	}
}

// TestGoingUploadOfferedForWordsNamingTheDocumentRunsWhenThePersonPicksIt (deep review round 5,
// finding D): an upload with force the proposal offers because the words name the held document
// by its stored name or by its slug is one the person can run: picked as the bar picks an offer
// (approved person), the write is made, the document's next version written, and no check the
// write makes when it runs refuses it for the words not writing the name the answer wrote.
func TestGoingUploadOfferedForWordsNamingTheDocumentRunsWhenThePersonPicksIt(t *testing.T) {
	rig := newGoingWritesRig(t)
	issue := createInteractionIssue(t, rig.mux, "DEMO", "An issue holding notes", "The spec.\n").Key
	uploadDocument(t, rig.mux, "/api/v1/issues/"+issue, "notes.md", "# Notes\n\nThe first line.\n")
	holds := rig.served.goingHoldsBy(context.Background())
	for index, words := range []string{"overwrite notes.md with this text", "replace notes-md with this"} {
		t.Run(words, func(t *testing.T) {
			arguments := map[string]any{"issue": issue, "name": "notes.md", "content": "# Notes\n\nNew words.\n", "force": true}
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
