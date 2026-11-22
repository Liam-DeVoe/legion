package api

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// gateSeeded is one issue the gate test seeds, with the row it puts in alice's Inbox and the gate
// that issue was seeded to: each line's step, its document ("" for none) and that document's
// approval state ("" for none).
type gateSeeded struct {
	issue model.Issue
	lines []gateSeededLine
}

type gateSeededLine struct {
	step, document, state string
}

// gateSeed is how one issue is seeded: its project and parent, whether the project gates
// [criteria, spec], and the state of its spec and of its Verification document (missing for an
// issue that holds none).
type gateSeed struct {
	project, parent    string
	gated              bool
	spec, verification string
}

// seedGateIssue writes one issue as seed says, through the API alone, and one row of it in alice's
// Inbox: the open approval request where its spec awaits approval, else a thread alice started
// that bob answered.
func seedGateIssue(t *testing.T, handler http.Handler, seed gateSeed, title string) gateSeeded {
	t.Helper()
	input := map[string]any{"project": seed.project, "title": title, "spec": "# Spec\n\n" + title + ".\n"}
	if seed.parent != "" {
		input["parent"] = seed.parent
	}
	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", input, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("create %q: status=%d body=%s", title, created.Code, created.Body.String())
	}
	issue := decodeBody[model.Issue](t, created)

	review := func(id, state string) {
		t.Helper()
		body := map[string]any{"state": state}
		if state == "changes_requested" {
			body["reason"] = "Name the rollback path."
		}
		if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+id+"/reviews", body, "alice"); response.Code != http.StatusCreated && response.Code != http.StatusOK {
			t.Fatalf("review %s of %s as %s: status=%d body=%s", id, issue.Key, state, response.Code, response.Body.String())
		}
	}
	verification := ""
	if seed.verification != "missing" {
		verification = uploadIssueDocument(t, handler, issue.Key, "verification.md", "# Verification\n\nOne check.\n").ID
		if seed.verification != "draft" {
			review(verification, seed.verification)
		}
	}
	switch seed.spec {
	case "approved", "changes_requested":
		review(issue.PrimaryArtifactID, seed.spec)
	case "awaiting":
		if response := sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+issue.PrimaryArtifactID+"/approval-requests", map[string]any{
			"actor": sessionActor(), "summary": "The spec of " + issue.Key + " is ready to read.",
		}); response.Code != http.StatusCreated && response.Code != http.StatusOK {
			t.Fatalf("request approval of %s's spec: status=%d body=%s", issue.Key, response.Code, response.Body.String())
		}
	}
	if seed.spec != "awaiting" {
		thread := createThreadComment(t, handler, issue.Key, map[string]any{"body": "Alice asks about " + title + "."}, "alice")
		createThreadComment(t, handler, issue.Key, map[string]any{"body": "Bob answers.", "reply_to": thread.ID}, "bob")
	}

	primary := gateSeededLine{document: issue.PrimaryArtifactID, state: seed.spec}
	if !seed.gated || seed.parent != "" {
		return gateSeeded{issue: issue, lines: []gateSeededLine{primary}}
	}
	criteria := gateSeededLine{step: model.GatedStepCriteria, document: verification, state: seed.verification}
	if verification == "" {
		criteria.state = ""
	}
	primary.step = model.GatedStepSpec
	return gateSeeded{issue: issue, lines: []gateSeededLine{criteria, primary}}
}

// gateLinesOf is lines as the seed spells them, so a read and a seed compare as one shape.
func gateLinesOf(lines []approvalGateLine) []gateSeededLine {
	read := make([]gateSeededLine, len(lines))
	for index, line := range lines {
		read[index].step = line.Step
		if line.ArtifactID != nil {
			read[index].document = *line.ArtifactID
		}
		if line.Approval != nil {
			read[index].state = line.Approval.State
		}
	}
	return read
}

// The one read carries each entry's approval gate in its one statement (c-approval-one-statement):
// over 3 issues with a row each and over 12, alice's Inbox is one statement, so no statement reads a
// gate document's versions, its review or its request for each document; each row carries the gate
// its issue was seeded to, on its ask and on its thread; and the issue read answers the same gate,
// field for field, so one computation answers both reads.
func TestTheOneReadCarriesTheGateInItsOneStatement(t *testing.T) {
	rig := newMeInboxRig(t)
	handler := rig.handler
	createReferenceAPIProject(t, handler, "GATE")
	publishGatedSteps(t, handler, "GATE", []string{model.GatedStepCriteria, model.GatedStepSpec})
	createReferenceAPIProject(t, handler, "FREE")

	seeded := map[string]gateSeeded{}
	seed := func(seed gateSeed, title string) model.Issue {
		t.Helper()
		issue := seedGateIssue(t, handler, seed, title)
		seeded[issue.issue.Key] = issue
		return issue.issue
	}
	// The first three: an issue of a project that gates nothing, whose spec is approved; a root
	// of the gating project with no Verification document and its spec under request; and that
	// root's child, sent back.
	seed(gateSeed{project: "FREE", spec: "approved", verification: "missing"}, "An approved spec")
	root := seed(gateSeed{project: "GATE", gated: true, spec: "awaiting", verification: "missing"}, "A root under request")
	seed(gateSeed{project: "GATE", gated: true, parent: root.Key, spec: "changes_requested", verification: "missing"}, "A child sent back")

	// read reads alice's Inbox on a request whose statements the rig records, and holds each
	// row's gate to what its issue was seeded to.
	read := func(rows int) (meInboxResponse, []string) {
		t.Helper()
		rig.statements.ownStatements(t, rig.traced)
		marked := context.WithValue(context.Background(), countedRead{}, true)
		answer := decodeMeInbox(t, meInboxRequestAs(t, handler, marked, "alice", url.Values{"state": {myRepliesStateInbox}}))
		statements := rig.statements.ownStatements(t, rig.traced)
		if len(answer.Entries) != rows {
			t.Fatalf("alice's Inbox holds %d rows, want the %d seeded: %v", len(answer.Entries), rows, meInboxNodes(answer))
		}
		asks, threads := 0, 0
		for _, entry := range answer.Entries {
			var key string
			switch {
			case entry.Ask != nil && entry.Ask.Issue != nil:
				key = entry.Ask.Issue.Key
				asks++
			case entry.Thread != nil && entry.Thread.Issue != nil:
				key = entry.Thread.Issue.Key
				threads++
			default:
				t.Fatalf("entry %s %s is no row of an issue", entry.Node.Kind, entry.Node.ID)
			}
			want, held := seeded[key]
			if !held {
				t.Fatalf("entry %s %s belongs to %s, which this test did not seed", entry.Node.Kind, entry.Node.ID, key)
			}
			if got := gateLinesOf(entry.ApprovalGate); !slices.Equal(got, want.lines) {
				t.Errorf("the row of %s carries the gate %+v, want the seeded %+v", key, got, want.lines)
			}
		}
		if asks == 0 || threads == 0 {
			t.Fatalf("alice's Inbox holds %d ask rows and %d thread rows, want both kinds", asks, threads)
		}
		return answer, statements
	}

	_, three := read(3)
	// Nine more, which make twelve: every state the seed writes, under both projects, as roots
	// and children, with and without a Verification document.
	seed(gateSeed{project: "FREE", spec: "draft", verification: "missing"}, "A spec nobody asked about")
	seed(gateSeed{project: "FREE", spec: "awaiting", verification: "draft"}, "An ungated spec under request")
	seed(gateSeed{project: "FREE", spec: "changes_requested", verification: "approved"}, "An ungated spec sent back")
	seed(gateSeed{project: "GATE", gated: true, spec: "draft", verification: "draft"}, "A root nobody asked about")
	seed(gateSeed{project: "GATE", gated: true, spec: "approved", verification: "approved"}, "A root approved twice")
	seed(gateSeed{project: "GATE", gated: true, spec: "awaiting", verification: "approved"}, "A root whose spec waits")
	seed(gateSeed{project: "GATE", gated: true, spec: "approved", verification: "changes_requested"}, "A root whose checks went back")
	seed(gateSeed{project: "GATE", gated: true, parent: root.Key, spec: "approved", verification: "missing"}, "An approved child")
	seed(gateSeed{project: "GATE", gated: true, parent: root.Key, spec: "draft", verification: "draft"}, "A child nobody asked about")
	twelve, twelveStatements := read(12)

	if len(three) != 1 || len(twelveStatements) != 1 {
		t.Fatalf("the 3-row read ran %d statements and the 12-row read %d, want 1 each:\n%s\n---\n%s",
			len(three), len(twelveStatements), strings.Join(three, "\n---\n"), strings.Join(twelveStatements, "\n---\n"))
	}

	// The issue read answers the gate each row carries, field for field.
	for _, entry := range twelve.Entries {
		var key string
		if entry.Ask != nil {
			key = entry.Ask.Issue.Key
		} else {
			key = entry.Thread.Issue.Key
		}
		response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+key, nil, "alice")
		if response.Code != http.StatusOK {
			t.Fatalf("read %s: status=%d body=%s", key, response.Code, response.Body.String())
		}
		issueGate := decodeBody[struct {
			ApprovalGate []approvalGateLine `json:"approval_gate"`
		}](t, response).ApprovalGate
		if len(issueGate) == 0 || !reflect.DeepEqual(issueGate, entry.ApprovalGate) {
			t.Errorf("%s's issue read answers the gate %+v, and its Inbox row carries %+v", key, issueGate, entry.ApprovalGate)
		}
	}
	t.Logf("the 3-row read and the 12-row read each ran one statement")
}
