package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/testbin"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// reviewOf is the reviewer's own clone of the fixture's issue beside the workspace ws, which a pod
// tells its reviewer as LEGION_WORKSPACE (workspace.ReviewLocation): ws is a workspace secondPod
// laid out where provisioning puts it, so its state directory is four directories above it.
func reviewOf(t *testing.T, ws string) string {
	t.Helper()
	state := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(ws))))
	review, err := workspace.ReviewLocation(state, ghrepo.MustParse(bundleOwner+"/"+bundleRepo), bundleIssue)
	if err != nil {
		t.Fatal(err)
	}
	return review
}

// A release asks the reviewer's shim for the pod's bundle when the reviewer is its only live role,
// and the reviewer's LEGION_WORKSPACE is its own clone of the issue. The shim bundles the issue's
// workspace all the same — the one every other role works in, whose unpushed work the release
// keeps — so all of that work reaches the pod that replaces it; and it reads that workspace as its
// last snapshot left it, taking no snapshot of its own, which would run the working-copy filters
// a configuration of the shared workspace names under the reviewer's claim.
func TestAReviewersShimBundlesTheIssuesWorkspace(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	released := f.secondPod("released")
	f.ws = released
	f.workOne()
	if err := os.WriteFile(filepath.Join(released, "since-the-last-snapshot.txt"), []byte("edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.ws = reviewOf(t, released)
	saved := f.bundleWorkspaceOne()
	if saved.Head != f.commits["working copy"] {
		t.Fatalf("the reviewer's shim bundled a workspace on %s, want the issue's working copy as its last snapshot left it, %s", saved.Head, f.commits["working copy"])
	}
	replacement := f.secondPod("replacement")
	if notice := f.restoreInto(replacement, saved, 1); !strings.Contains(notice, "is restored") {
		t.Fatalf("the restore's notice is %q", notice)
	}
	f.requireRestored(replacement, saved)
}

// A replacement pod's one restore may run at the reviewer's start, the first role the pod starts,
// whose LEGION_WORKSPACE is its own clone of the issue. The launcher restores the saved work
// into the issue's workspace all the same, which every other role works in.
func TestAReviewersStartRestoresTheIssuesWorkspace(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	released := f.secondPod("released")
	f.ws = released
	f.workOne()
	saved := f.bundleWorkspaceOne()
	replacement := f.secondPod("replacement")
	if notice := f.restoreInto(reviewOf(t, replacement), saved, 1); !strings.Contains(notice, "is restored") {
		t.Fatalf("the restore at the reviewer's start answered %q, want the issue's workspace restored", notice)
	}
	f.requireRestored(replacement, saved)
}
