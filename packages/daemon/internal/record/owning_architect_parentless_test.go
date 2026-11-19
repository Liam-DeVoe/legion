package record

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// A child-closed or a child-status notice is about the child, and the architect above it decides
// what the rest of the tree does: a child-status tells of the child's first phase start, which
// starts the child's own architect beside it, of its leave or its re-entry, of its joining the
// tree, or of its hold under an accepted parent, and a child-closed tells of its close. The
// phase-finished of the gate's opening, which names no role, tells of the move that starts the
// child's own architect, which learns of it from its catch-up. So the owner walk of each starts at
// the child's parent and never ends at the child's own architect (NoticeStartsAtParent). A child
// of a tree whose recorded parent is nil — a person moved it out from under its parent in
// Dispatch, and admission kept it in its tree (admit's recordObservation) — has no parent to start
// the walk at, so the architect above it is the tree root's. The outbox can publish such a notice
// while the child's own architect holds its role (one just started with the child's first phase,
// or one whose stop, on another lane, has not run yet), and that architect must not be the one
// told of its own issue.
func TestAParentlessChildsCloseAndStatusChangeReachTheTreeRoot(t *testing.T) {
	root, orphan := "LEGION-1", "LEGION-5"
	tree := map[string]Issue{
		root:   {Key: root, Tree: root},
		orphan: {Key: orphan, Tree: root},
	}
	// The child's own architect holds its role, as a newly started one does, and as one does until
	// its stop runs.
	runs := func(token claim.Token) bool {
		return token == architectToken(t, root) || token == architectToken(t, orphan)
	}
	for _, notice := range []struct {
		kind NoticeKind
		role claim.Role
	}{{"child-closed", claim.RoleArchitect}, {"child-status", claim.RoleArchitect}, {"phase-finished", ""}} {
		t.Run(string(notice.kind), func(t *testing.T) {
			if got := OwningIssue("legion", tree, tree[orphan], NoticeStartsAtParent(notice.kind, notice.role), runs); got != root {
				t.Fatalf("a %s notice of the parentless child %s goes to the architect of %s, want the tree root %s's", notice.kind, orphan, got, root)
			}
		})
	}
}

// The phase-finished that names no role is the gate's opening (workflow's noticeFor), and its walk
// starts at the parent. Every phase-finished that names a role is a worker's own, which the
// issue's own architect hears while it holds its role, so its walk starts at the issue.
func TestNoticeStartsAtParentForTheGateOpeningAndNoWorkersPhaseFinished(t *testing.T) {
	if !NoticeStartsAtParent("phase-finished", "") {
		t.Error("the phase-finished of the gate's opening starts at the issue, want its parent")
	}
	for _, role := range claim.Roles {
		if NoticeStartsAtParent("phase-finished", role) {
			t.Errorf("a phase-finished of the %s starts at the issue's parent, want the issue", role)
		}
	}
}
