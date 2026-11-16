package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// TestFindNodeUnderIssueDecodesTheStoreAddress: the `{id}` of an issue's node routes is the node's
// store address, in each of its three spellings, and names a node only while that node stands
// under the issue the path names and the caller may see it. Every other answer is one 404, in the
// same words for a node that does not exist, one of another issue and one reserved for another
// author.
//
// The fixture is the private-node seed: an issue with a spec of public blocks and one block its
// author reserved for herself, a comment everyone reads and one she keeps to herself. A second
// issue, of another project, holds nodes of its own.
func TestFindNodeUnderIssueDecodesTheStoreAddress(t *testing.T) {
	fixture := seedPrivateNode(t, true)
	ctx := context.Background()
	pool := fixture.database.Pool
	reader, author := privateNodeReader(), privateNodeAuthor()
	other := createInteractionIssue(t, fixture.handler, "OTHR", "Another issue", "# Spec\n\nElsewhere.\n")
	var otherSpec string
	if err := pool.QueryRow(ctx, `select ref_key from artifacts where id = $1::uuid`, other.PrimaryArtifactID).Scan(&otherSpec); err != nil {
		t.Fatalf("read the other issue's spec address: %v", err)
	}
	otherComment := createThreadComment(t, fixture.handler, other.Key, map[string]any{"body": "A remark elsewhere."}, "alice").ID
	document := "dispatch://" + fixture.refKey

	for _, test := range []struct {
		name, id   string
		who        model.Actor
		kind, node string
		visibility string
	}{
		{"the issue by its store address", "issue/" + fixture.issue, reader, "issue", fixture.issue, graphstore.VisibilityProject},
		{"the issue by its dispatch address", "dispatch://" + fixture.issue, reader, "issue", fixture.issue, graphstore.VisibilityProject},
		{"the spec by its ref_key", "artifact/" + fixture.refKey, reader, "artifact", fixture.specID, graphstore.VisibilityProject},
		{"a block of the spec by its node id", fixture.first, reader, refs.BlockNodeKind, fixture.first, graphstore.VisibilityProject},
		{"a block of the spec by its dispatch address", refs.BlockRef(document, "first"), reader, refs.BlockNodeKind, fixture.first, graphstore.VisibilityProject},
		{"a comment by its store address", "comment/" + fixture.publicComment, reader, graphstore.KindComment, fixture.publicComment, graphstore.VisibilityProject},
		{"a comment by its dispatch address", "dispatch://" + fixture.issue + "/comment/" + fixture.publicComment, reader, graphstore.KindComment, fixture.publicComment, graphstore.VisibilityProject},
		{"the private block to its author", fixture.private, author, refs.BlockNodeKind, fixture.private, graphstore.VisibilityAuthor},
		{"the private comment to its author", "comment/" + fixture.privateComment, author, graphstore.KindComment, fixture.privateComment, graphstore.VisibilityAuthor},
	} {
		t.Run(test.name, func(t *testing.T) {
			node, err := FindNodeUnderIssue(ctx, pool, test.who, fixture.issue, test.id)
			if err != nil {
				t.Fatalf("FindNodeUnderIssue(%q, %q) as %s: %v", fixture.issue, test.id, test.who.ID, err)
			}
			if node.Kind != test.kind || node.ID != test.node {
				t.Fatalf("FindNodeUnderIssue(%q, %q) = %s %s, want %s %s", fixture.issue, test.id, node.Kind, node.ID, test.kind, test.node)
			}
			if node.IssueKey == nil || *node.IssueKey != fixture.issue {
				t.Fatalf("FindNodeUnderIssue(%q, %q) answers a node of issue %v", fixture.issue, test.id, node.IssueKey)
			}
			if node.State != graphstore.StateLive || node.Visibility != test.visibility {
				t.Fatalf("FindNodeUnderIssue(%q, %q) answers state %q and visibility %q, want %q and %q",
					fixture.issue, test.id, node.State, node.Visibility, graphstore.StateLive, test.visibility)
			}
		})
	}

	for _, test := range []struct {
		name, key, id string
		who           model.Actor
	}{
		{"another issue under this one", fixture.issue, "issue/" + other.Key, reader},
		{"this issue under another", other.Key, "issue/" + fixture.issue, reader},
		{"another issue's spec", fixture.issue, "artifact/" + otherSpec, reader},
		{"another issue's comment", fixture.issue, "comment/" + otherComment, reader},
		{"this issue's block under another", other.Key, fixture.first, reader},
		{"a comment nobody wrote", fixture.issue, "comment/00000000-0000-4000-8000-000000000000", reader},
		{"a block no version holds", fixture.issue, fixture.refKey + "#nosuchblock", reader},
		{"a kind the registry does not resolve", fixture.issue, "lens/00000000-0000-4000-8000-000000000000", reader},
		{"a kind with no loader", fixture.issue, "supertag/requirement", reader},
		{"a kind that names nothing", fixture.issue, "widget/" + fixture.issue, reader},
		{"no kind", fixture.issue, fixture.issue, reader},
		{"no id", fixture.issue, "issue/", reader},
		{"nothing", fixture.issue, "", reader},
		{"a dispatch address that names nothing", fixture.issue, "dispatch://" + fixture.issue + "/nothing", reader},
		{"the private block to another reader", fixture.issue, fixture.private, reader},
		{"the private block's dispatch address to another reader", fixture.issue, refs.BlockRef(document, privateBlockID), reader},
		{"the private comment to another reader", fixture.issue, "comment/" + fixture.privateComment, reader},
	} {
		t.Run(test.name, func(t *testing.T) {
			node, err := FindNodeUnderIssue(ctx, pool, test.who, test.key, test.id)
			var refused *apiError
			if !errors.As(err, &refused) {
				t.Fatalf("FindNodeUnderIssue(%q, %q) = %s %s, %v; want a 404", test.key, test.id, node.Kind, node.ID, err)
			}
			want := fmt.Sprintf("no node %q under %s", test.id, test.key)
			if refused.status != http.StatusNotFound || refused.code != "NOT_FOUND" || refused.message != want {
				t.Fatalf("FindNodeUnderIssue(%q, %q) refused %d %s %q, want 404 NOT_FOUND %q",
					test.key, test.id, refused.status, refused.code, refused.message, want)
			}
		})
	}
}
