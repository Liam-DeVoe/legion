package daemon

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// countedBasisDispatch is basisDispatch counting each document's approval reads and each issue's
// reads, and failing every read of the document or issue failing names.
type countedBasisDispatch struct {
	*basisDispatch
	approvalReads map[string]int
	issueReads    map[string]int
	failing       string
}

func (d *countedBasisDispatch) Approval(ctx context.Context, artifactID string) (dispatch.Approval, error) {
	d.approvalReads[artifactID]++
	if artifactID == d.failing {
		return dispatch.Approval{}, errors.New("Dispatch answered 502")
	}
	return d.basisDispatch.Approval(ctx, artifactID)
}

func (d *countedBasisDispatch) GetIssue(ctx context.Context, key string) (dispatch.Issue, error) {
	d.issueReads[key]++
	if key == d.failing {
		return dispatch.Issue{}, errors.New("Dispatch answered 502")
	}
	return d.basisDispatch.GetIssue(ctx, key)
}

// The daemon's preparation reads Dispatch for an event only where it is a cue for a step waiting
// on an ancestor's approval request (workflow.PendingBasisCue), and sets what it read on the fact,
// which is otherwise the one decoded: a document event at the basis version or a newer one, other
// than that version's own version event, reads the document's approval and the ancestor's issue;
// so does a close of the request other than a person's answer; an event that shows the ancestor
// leaving R1's first condition reads the ancestor's issue alone. Every other event reads nothing:
// the basis version's version event, an older version's review, another document's event, an
// answer, another request's close, an event that shows the ancestor at work, another issue's
// leave, and any event of a document a step stands open on rather than waits on. A failed read
// fails the preparation, naming what it read, so the event is delivered again.
func TestPrepareBasisReadsDispatchOnlyForACue(t *testing.T) {
	person := dispatch.Actor{Kind: "user", ID: "alice"}
	review := func(kind intake.DispatchArtifactKind, version int) intake.DispatchArtifact {
		fact := intake.DispatchArtifact{Key: basisSeedAncestor, ArtifactID: basisSeedDoc, Kind: kind, Version: version, Mark: record.EventMark{Seq: 60}}
		if kind != intake.DispatchArtifactVersion {
			fact.Actor = person
		}
		return fact
	}
	closed := func(ask, state string) intake.DispatchAskClosed {
		return intake.DispatchAskClosed{Key: basisSeedAncestor, Ask: ask, Kind: record.AskApproval, Artifact: basisSeedDoc, State: state, Actor: person,
			Mark: record.EventMark{Seq: 61}}
	}
	issue := func(key, status string, labelled bool) intake.DispatchIssue {
		return intake.DispatchIssue{Key: key, Seq: 62, Type: "issue.updated", Status: status, Title: key, Rank: "T", HandedOver: labelled}
	}
	// What Dispatch shows: ask-100 open on version 4, and the ancestor at work.
	shown := dispatch.Approval{IssueKey: basisSeedAncestor, State: "awaiting", LatestVersion: 4, AskID: "ask-100", WaitingOn: "human"}
	read := &intake.BasisRead{Approval: shown, Ancestor: intake.AncestorRead{Status: "in_progress", HandedOver: true}}
	for _, tc := range []struct {
		name string
		fact intake.Fact
		// open is a step standing open on the document rather than waiting on its request.
		open          bool
		failing       string
		approvals     int
		issues        int
		basisRead     *intake.BasisRead
		ancestorRead  *intake.AncestorRead
		failureNaming string
	}{
		{name: "a person's approval of the version", fact: review(intake.DispatchArtifactApproved, 4), approvals: 1, issues: 1, basisRead: read},
		{name: "a changes request on the version", fact: review(intake.DispatchArtifactChangesRequested, 4), approvals: 1, issues: 1, basisRead: read},
		{name: "a newer version", fact: review(intake.DispatchArtifactVersion, 5), approvals: 1, issues: 1, basisRead: read},
		{name: "the version's own version event", fact: review(intake.DispatchArtifactVersion, 4)},
		{name: "an approval of an older version", fact: review(intake.DispatchArtifactApproved, 3)},
		{name: "an event of another document", fact: func() intake.DispatchArtifact {
			fact := review(intake.DispatchArtifactApproved, 4)
			fact.ArtifactID = "artifact-other"
			return fact
		}()},
		{name: "a close of the request", fact: closed("ask-100", "resolved"), approvals: 1, issues: 1, basisRead: read},
		{name: "a person's answer of the request", fact: closed("ask-100", "answered")},
		{name: "a close of another request", fact: closed("ask-9", "resolved")},
		{name: "the ancestor done", fact: issue(basisSeedAncestor, "done", true), issues: 1, ancestorRead: &intake.AncestorRead{Status: "in_progress", HandedOver: true}},
		{name: "the ancestor at work", fact: issue(basisSeedAncestor, "in_progress", true)},
		{name: "another issue done", fact: issue("LEGION-300", "done", true)},
		{name: "an approval of a document a step stands open on", fact: review(intake.DispatchArtifactApproved, 4), open: true},
		{name: "a failed read of the document", fact: review(intake.DispatchArtifactApproved, 4), failing: basisSeedDoc, approvals: 1, failureNaming: basisSeedDoc},
		{name: "a failed read of the ancestor", fact: issue(basisSeedAncestor, "done", true), failing: basisSeedAncestor, issues: 1, failureNaming: basisSeedAncestor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newBasisSeedRig(t)
			if tc.open {
				rig.openOnBasis(basisSeedRoot, basisSeedRootDoc)
			} else {
				rig.pendingOnBasis(basisSeedRoot, basisSeedRootDoc)
			}
			rig.client.approvals[basisSeedDoc] = shown
			client := &countedBasisDispatch{basisDispatch: rig.client, approvalReads: map[string]int{}, issueReads: map[string]int{}, failing: tc.failing}
			w := &workflowRuntime{pool: rig.pool, records: rig.records, dispatch: client, log: quietLogger()}

			got, err := w.prepareBasis(context.Background(), tc.fact)

			switch {
			case tc.failureNaming != "" && (err == nil || !strings.Contains(err.Error(), tc.failureNaming) || !strings.Contains(err.Error(), "502")):
				t.Fatalf("prepareBasis = %#v, %v; want the failed read to fail the preparation, naming %s", got, err, tc.failureNaming)
			case tc.failureNaming == "" && err != nil:
				t.Fatalf("prepareBasis: %v", err)
			}
			if client.approvalReads[basisSeedDoc] != tc.approvals || client.issueReads[basisSeedAncestor] != tc.issues {
				t.Fatalf("the document was read %d times and the ancestor %d, want %d and %d", client.approvalReads[basisSeedDoc],
					client.issueReads[basisSeedAncestor], tc.approvals, tc.issues)
			}
			if tc.failureNaming != "" {
				return
			}
			want := tc.fact
			switch fact := want.(type) {
			case intake.DispatchArtifact:
				fact.BasisRead = tc.basisRead
				want = fact
			case intake.DispatchAskClosed:
				fact.BasisRead = tc.basisRead
				want = fact
			case intake.DispatchIssue:
				fact.AncestorRead = tc.ancestorRead
				want = fact
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("prepareBasis = %#v, want %#v: the fact as decoded, with the read set only for a cue", got, want)
			}
		})
	}
}
