package workspace

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// The fix's working copy sits beside the issue's own, in the same directory of workspaces, named
// for it with "-fix" after. The shim derives it from its LEGION_WORKSPACE and from nothing a frame
// sends. It is no other working copy Location names: the working copy of a task of the issue is
// named for the issue, a dot and the task's part, and the fix's name holds no dot; and it is no
// issue's own, since its name is no issue key.
func TestTheFixDirIsBesideTheWorkspace(t *testing.T) {
	if got, want := FixDir("/s/workspaces/o/r/widgets-42"), "/s/workspaces/o/r/widgets-42-fix"; got != want {
		t.Errorf("FixDir(/s/workspaces/o/r/widgets-42) = %q, want %q", got, want)
	}
	for _, issue := range []string{"WIDGETS-42", "INFRA-7", "A-1"} {
		located, err := Location("/s", ghrepo.MustParse("acme/widgets"), issue)
		if err != nil {
			t.Fatalf("Location(%s): %v", issue, err)
		}
		fix := FixDir(located.Dir)
		if filepath.Dir(fix) != filepath.Dir(located.Dir) {
			t.Errorf("FixDir(%s) = %s, want a directory beside it", located.Dir, fix)
		}
		name := filepath.Base(fix)
		if strings.Contains(name, ".") {
			t.Errorf("FixDir(%s) = %s, whose name holds a dot, the form of a task's working copy beside the issue's", located.Dir, fix)
		}
		if claim.IsIssueKey(strings.ToUpper(name)) {
			t.Errorf("FixDir(%s) = %s, whose name is an issue key: Location names it for that issue", located.Dir, fix)
		}
	}
}
