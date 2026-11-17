package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// c-reading-approval (dispatch://INFRA-1150 R2): a reading never changes the decision it reads, so
// writing, editing or removing one never stales a person's approval of the document that holds
// the decision, and never clears it. The approval carry's reading case (docs.readingCase) holds
// that at the version the write stores: the document's current version reads approved through
// GET /api/v1/artifacts/{id}, the read the Legion daemon's design gate makes
// (packages/daemon/internal/dispatch HTTPClient.Approval and Approval.ApprovedLatest). The broken
// state this fails on is a write that carries the approval onto the old version alone and leaves
// the new current version unapproved, which a test reading the old approval would pass.

// The fixture spec of the reading tests: prose, an agent's decision d1 that the reading r1 reads,
// a decision d2 nobody reads yet, and two asks of the person's, which the fixture answers: a1,
// which offers options, and a2, whose answer is the person's own words.
const (
	readingProse       = "The service ships behind a flag."
	readingDecisionOne = "We chose REST."
	readingDecisionTwo = "We chose Postgres."
	readingOfOne       = "The agent reads it as REST for every service."
	readingQuestion    = "Which transport do we expose?"
	readingOpenWords   = "Which region goes first?"
	readingAskWithout  = ":::ask{#a2 urgency=\"med\" multiple=\"false\"}\n" + readingOpenWords + "\n:::\n"
	readingSpec        = "# Plan\n\n" + readingProse + "\n\n" +
		"::::decision{#d1}\n" + readingDecisionOne + "\n\n:::reading{#r1}\n" + readingOfOne + "\n:::\n::::\n\n" +
		":::decision{#d2}\n" + readingDecisionTwo + "\n:::\n\n" +
		":::ask{#a1 urgency=\"med\" multiple=\"false\"}\n" + readingQuestion + "\n\n- REST: Plain HTTP\n- gRPC: Streaming\n:::\n\n" +
		readingAskWithout
	// The readings the writes add: r2 under d2, and r3 inside an answered ask.
	readingOfTwo    = "The agent reads it as Postgres for every store."
	readingOfAnswer = "The agent reads the answer as the quiet region first."
)

// readingCarrySentence is the reason the reading case records, carrying the approval of version
// from (docs.carriedReasonPrefix, readingCase, carriedReasonEnd).
func readingCarrySentence(from int) string {
	return fmt.Sprintf("Carried forward from the approval of version %d: the readings of its decisions and of its answered asks, which change no decision, were the whole change since it.", from)
}

// readingFixture is one issue whose spec, readingSpec, a person approved once its asks held
// their answers: the issue, its spec and that approval.
type readingFixture struct {
	issueKey, artifactID string
	approval             model.ArtifactReview
}

// approvedReadingSpec creates an issue of project TEST holding readingSpec as alice, waits for
// settlement to open the asks of a1 and a2, has alice answer both - which writes no version - and
// approve the spec's latest version.
func approvedReadingSpec(t *testing.T, handler http.Handler, title string) readingFixture {
	t.Helper()
	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]any{
		"project": "TEST", "title": title, "spec": readingSpec, "force": true,
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("create the issue %q: status=%d body=%s", title, created.Code, created.Body.String())
	}
	issue := decodeBody[model.Issue](t, created)
	asks := indexedDecisionBlocks(t, handler, issue.Key, issue.PrimaryArtifactID, map[string]string{"a1": readingQuestion, "a2": readingOpenWords})
	answerDecisionBlock(t, handler, asks["a1"], "alice", []string{"REST"}, "", nil)
	answerDecisionBlock(t, handler, asks["a2"], "alice", []string{}, "The quiet one, East.", nil)
	approval := reviewDocument(t, handler, issue.PrimaryArtifactID, "alice", "approved", "")
	return readingFixture{issueKey: issue.Key, artifactID: issue.PrimaryArtifactID, approval: approval}
}

// replacedOnce is markdown with old replaced by with, failing unless markdown holds old once.
func replacedOnce(t *testing.T, markdown, old, with string) string {
	t.Helper()
	if count := strings.Count(markdown, old); count != 1 {
		t.Fatalf("the spec holds %q %d times, want once:\n%s", old, count, markdown)
	}
	return strings.Replace(markdown, old, with, 1)
}

// readingWrite is one write a session makes to the approved spec: the upload of the whole
// document, made from the markdown the spec reads back, and the edit of it, as operations of the
// edits route (nil where the edits route writes no such change); and a passage of the spec the
// write leaves, which the read-back must hold, and one it takes away, which it must not ("" for
// none).
type readingWrite struct {
	name   string
	upload func(t *testing.T, markdown string) string
	edit   []map[string]string
	holds  string
	drops  string
}

// The writes that touch readings alone: one added under a decision, one added inside each
// answered ask, a reading's text edited, and a reading removed. An edit's find holds one
// textblock, and a with that names blocks rewrites the typed block it lands in only where it
// rewrites all of it, so the edits route writes a reading after an ask's question and never
// after the options that follow the question: the ask that offers options takes its reading
// through the upload alone.
var readingWrites = []readingWrite{
	{
		name: "a reading added under a decision",
		upload: func(t *testing.T, markdown string) string {
			return replacedOnce(t, markdown, ":::decision{#d2 replaced=\"false\"}\n"+readingDecisionTwo+"\n:::\n",
				"::::decision{#d2 replaced=\"false\"}\n"+readingDecisionTwo+"\n\n:::reading{#r2}\n"+readingOfTwo+"\n:::\n::::\n")
		},
		edit: []map[string]string{replaceOp(readingDecisionTwo,
			"::::decision{#d2}\n"+readingDecisionTwo+"\n\n:::reading{#r2}\n"+readingOfTwo+"\n:::\n::::")},
		holds: ":::reading{#r2}\n" + readingOfTwo + "\n:::\n",
	},
	{
		name: "a reading added inside an answered ask",
		upload: func(t *testing.T, markdown string) string {
			opened := replacedOnce(t, markdown, ":::ask{#a2 ", "::::ask{#a2 ")
			return replacedOnce(t, opened, readingOpenWords+"\n:::\n",
				readingOpenWords+"\n\n:::reading{#r3}\n"+readingOfAnswer+"\n:::\n::::\n")
		},
		edit: []map[string]string{replaceOp(readingOpenWords,
			"::::ask{#a2 urgency=\"med\" multiple=\"false\"}\n"+readingOpenWords+"\n\n:::reading{#r3}\n"+readingOfAnswer+"\n:::\n::::")},
		holds: ":::reading{#r3}\n" + readingOfAnswer + "\n:::\n",
	},
	{
		name: "a reading added inside the answered ask that offers options",
		upload: func(t *testing.T, markdown string) string {
			opened := replacedOnce(t, markdown, ":::ask{#a1 ", "::::ask{#a1 ")
			return replacedOnce(t, opened, "Streaming\n:::\n",
				"Streaming\n\n:::reading{#r4}\n"+readingOfAnswer+"\n:::\n::::\n")
		},
		holds: ":::reading{#r4}\n" + readingOfAnswer + "\n:::\n",
	},
	{
		name: "a reading's text edited",
		upload: func(t *testing.T, markdown string) string {
			return replacedOnce(t, markdown, "for every service.", "for every public service.")
		},
		edit:  []map[string]string{replaceOp("for every service.", "for every public service.")},
		holds: "The agent reads it as REST for every public service.",
		drops: readingOfOne,
	},
	{
		name: "a reading removed",
		upload: func(t *testing.T, markdown string) string {
			return replacedOnce(t, markdown, "::::decision{#d1 replaced=\"false\"}\n"+readingDecisionOne+"\n\n:::reading{#r1}\n"+readingOfOne+"\n:::\n::::\n",
				":::decision{#d1 replaced=\"false\"}\n"+readingDecisionOne+"\n:::\n")
		},
		edit:  []map[string]string{{"op": "delete", "block": "r1"}},
		holds: ":::decision{#d1 replaced=\"false\"}\n" + readingDecisionOne + "\n:::\n",
		drops: readingOfOne,
	},
}

// The four writes that must still supersede the approval: the decision's own words, prose
// outside any reading, a write that changes both at once beside a reading, and a reading retyped
// into a decision.
var supersedingWrites = []readingWrite{
	{name: "the decision's own words changed", edit: []map[string]string{replaceOp(readingDecisionOne, "We chose gRPC.")}},
	{name: "prose outside any reading changed", edit: []map[string]string{replaceOp(readingProse, "The service ships behind a flag and a canary.")}},
	{name: "the decision's words and the prose changed at once, beside a reading", edit: []map[string]string{
		replaceOp(readingDecisionOne, "We chose gRPC."),
		replaceOp(readingProse, "The service ships behind a flag and a canary."),
		replaceOp("for every service.", "for every public service."),
	}},
	{name: "a reading retyped into a decision", edit: []map[string]string{{"op": "retype", "block": "r1", "type": "decision"}}},
}

// writeReadingSpec makes write to fixture's spec as the session, through the upload of the whole
// document when upload is set and through the edits route otherwise, waits for the version it
// stores, one past the approved one, and answers the advice the write came back with. It reads
// the document back and fails unless it holds what the write leaves.
func writeReadingSpec(t *testing.T, handler http.Handler, fixture readingFixture, write readingWrite, upload bool) *testWriteAdvice {
	t.Helper()
	version := fixture.approval.Version + 1
	var advice *testWriteAdvice
	if upload {
		uploaded := sessionRequest(t, handler, http.MethodPost, "/api/v1/issues/"+fixture.issueKey+"/artifacts", map[string]any{
			"actor": sessionActor(), "name": "spec.md", "content": write.upload(t, documentMarkdown(t, handler, fixture.artifactID)),
		})
		advice = adviceFromResponse(t, uploaded.Code, uploaded.Body.String())
		waitForArtifactVersion(t, handler, fixture.artifactID, version)
	} else {
		advice = agentEdit(t, handler, fixture.artifactID, version, write.edit...)
	}
	stored := versionMarkdown(t, handler, fixture.artifactID, version)
	if !strings.Contains(stored, write.holds) || write.drops != "" && strings.Contains(stored, write.drops) {
		t.Fatalf("%s: version %d reads back as\n%s\nwant it holding %q and not %q", write.name, version, stored, write.holds, write.drops)
	}
	return advice
}

// requireApprovedAtTheCurrentVersion is the four reads after a write that touched readings alone:
// GET /api/v1/artifacts/{id} answers the approval approved at a version equal to the latest
// version, which is the version the write stored; the carried review, the document's newest,
// approves that version in the approver's name and records the version the person approved; the
// write's advice names no superseded approval; and the person's own approval still stands at its
// own version.
func requireApprovedAtTheCurrentVersion(t *testing.T, handler http.Handler, fixture readingFixture, advice *testWriteAdvice, what string) {
	t.Helper()
	approved, written := fixture.approval.Version, fixture.approval.Version+1
	reviews := documentReviews(t, handler, fixture.artifactID)
	reached := 0
	for _, review := range reviews {
		if review.State == "approved" {
			reached = review.Version
			break
		}
	}
	got := readApproval(t, handler, fixture.artifactID)
	if got.Approval == nil || got.Approval.State != "approved" || got.Approval.Version == nil || *got.Approval.Version != got.Approval.LatestVersion {
		state, at, latest := "none", 0, 0
		if got.Approval != nil {
			state, latest = got.Approval.State, got.Approval.LatestVersion
			if got.Approval.Version != nil {
				at = *got.Approval.Version
			}
		}
		t.Fatalf("%s: GET /api/v1/artifacts/%s answers the approval %s at version %d with the latest version %d, so the current version is not approved: the carry reached version %d, and the person must approve version %d again",
			what, fixture.artifactID, state, at, latest, reached, latest)
	}
	if got.Approval.LatestVersion != written {
		t.Fatalf("%s: the latest version is %d, want %d, the one version the write stored", what, got.Approval.LatestVersion, written)
	}
	var person *servedReview
	for index := range reviews {
		if reviews[index].ID == fixture.approval.ID {
			person = &reviews[index]
		}
	}
	if person == nil || person.Version != approved || person.State != "approved" {
		t.Fatalf("%s: the reviews are %+v, want the person's approval %s still standing at version %d", what, reviews, fixture.approval.ID, approved)
	}
	carried := reviews[0]
	if carried.ID == fixture.approval.ID || carried.Version != written || carried.State != "approved" || string(carried.Actor) != string(person.Actor) ||
		carried.Reason == nil || *carried.Reason != readingCarrySentence(approved) {
		reason := "<none>"
		if carried.Reason != nil {
			reason = *carried.Reason
		}
		t.Fatalf("%s: the newest review is %+v (reason %q), want the carry approving version %d as %s with the reason %q",
			what, carried, reason, written, person.Actor, readingCarrySentence(approved))
	}
	if advice.SupersededApproval != nil {
		t.Fatalf("%s: the write's advice reports the superseded approval %+v, want none: its version carries the approval", what, advice.SupersededApproval)
	}
}

// requireSupersededByTheWrite is the reads after a write that must supersede the approval: GET
// /api/v1/artifacts/{id} answers no approval of the latest version, and the write's advice
// reports the person's approval as the one it superseded.
func requireSupersededByTheWrite(t *testing.T, handler http.Handler, fixture readingFixture, advice *testWriteAdvice, what string) {
	t.Helper()
	approved := fixture.approval.Version
	got := readApproval(t, handler, fixture.artifactID)
	if got.Approval == nil {
		t.Fatalf("%s: GET /api/v1/artifacts/%s answers no approval state at all", what, fixture.artifactID)
	}
	if got.Approval.State == "approved" && got.Approval.Version != nil && *got.Approval.Version == got.Approval.LatestVersion {
		t.Fatalf("%s: GET /api/v1/artifacts/%s answers the latest version %d approved, want no approval of it: the write changed more than a reading",
			what, fixture.artifactID, got.Approval.LatestVersion)
	}
	if superseded := advice.SupersededApproval; superseded == nil || superseded.ApprovedVersion != approved || superseded.Version != approved+1 || superseded.ApprovedBy != "alice" {
		t.Fatalf("%s: the write's advice reports the superseded approval %+v, want alice's approval of version %d superseded by version %d",
			what, advice.SupersededApproval, approved, approved+1)
	}
}

// A person approves the fixture spec at a version, and a session writes a reading under one of
// its decisions and reads the document back; the four reads after the write find the current
// version approved (requireApprovedAtTheCurrentVersion). The same holds for a reading added inside
// the person's answered ask, an edit of a reading's text and the removal of one, each written as
// an upload of the whole document and as an edit of it (the reading inside the ask that offers
// options as an upload alone: readingWrites). Four writes still supersede the approval: the
// decision's own words, prose outside any reading, both at once beside a reading, and a reading
// retyped into a decision (requireSupersededByTheWrite).
func TestAReadingKeepsAPersonsApprovalOfTheCurrentSpec(t *testing.T) {
	handler, _, _ := settlingCarryDocuments(t, 20*time.Millisecond)
	if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects", map[string]string{
		"key": "TEST", "name": "TEST project",
	}, "alice"); response.Code != http.StatusCreated {
		t.Fatalf("create project: status=%d body=%s", response.Code, response.Body.String())
	}
	issues := 0
	fixture := func(t *testing.T) readingFixture {
		t.Helper()
		issues++
		return approvedReadingSpec(t, handler, fmt.Sprintf("Reading fixture %d", issues))
	}
	for _, write := range readingWrites {
		for _, upload := range []bool{true, false} {
			if !upload && write.edit == nil {
				continue
			}
			how := "an edit of the document"
			if upload {
				how = "an upload of the whole document"
			}
			t.Run(write.name+", by "+how, func(t *testing.T) {
				spec := fixture(t)
				advice := writeReadingSpec(t, handler, spec, write, upload)
				requireApprovedAtTheCurrentVersion(t, handler, spec, advice, write.name+", by "+how)
				t.Logf("%s, by %s: version %d reads approved, carried from the person's approval of version %d",
					write.name, how, spec.approval.Version+1, spec.approval.Version)
			})
		}
	}
	for _, write := range supersedingWrites {
		t.Run(write.name, func(t *testing.T) {
			spec := fixture(t)
			advice := agentEdit(t, handler, spec.artifactID, spec.approval.Version+1, write.edit...)
			requireSupersededByTheWrite(t, handler, spec, advice, write.name)
			t.Logf("%s: version %d supersedes the person's approval of version %d", write.name, spec.approval.Version+1, spec.approval.Version)
		})
	}
}
