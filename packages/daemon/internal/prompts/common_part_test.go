package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// Each of the seven roles an issue's agents start with composes a part this daemon writes for
// every role of its kind: architect-common.md for an owner, worker-common.md for the agent of a
// step. The test plants a marker in both parts of the snapshot a launch reads (New's state
// directory) and finds it in the composed prompt of each role. It reads no word of Legion's own
// texts: the composition is checked before the planting too, and holds no marker there. So it
// shows which parts each role composes, not what they say: that the separate-pod line stands in
// those two parts is for the runs of real agents of the `v-own-pod` check to show.
func TestEveryRoleComposesADaemonCommonPart(t *testing.T) {
	const marker = "planted-common-part-marker-5a1c"
	stateDir := t.TempDir()
	composer, err := New(stateDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	roles := []struct {
		name   string
		role   claim.Role
		isRoot bool
	}{
		{"root architect", claim.RoleArchitect, true},
		{"sub-architect", claim.RoleArchitect, false},
		{"planner", claim.RolePlanner, false},
		{"implementer", claim.RoleImplementer, false},
		{"tester", claim.RoleTester, false},
		{"reviewer", claim.RoleReviewer, false},
		{"merger", claim.RoleMerger, false},
	}
	for _, tc := range roles {
		if strings.Contains(composedText(t, composer, tc.role, tc.isRoot), marker) {
			t.Fatalf("the composed %s prompt holds %q before it is planted", tc.name, marker)
		}
	}
	for _, part := range []string{"architect-common.md", "worker-common.md"} {
		path := filepath.Join(stateDir, "prompts", "go", part)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the snapshot's go/%s: %v", part, err)
		}
		if err := os.WriteFile(path, append(body, []byte("\n"+marker+"\n")...), 0o600); err != nil {
			t.Fatalf("plant the marker in the snapshot's go/%s: %v", part, err)
		}
	}
	for _, tc := range roles {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(composedText(t, composer, tc.role, tc.isRoot), marker) {
				t.Errorf("the composed %s prompt holds no marker planted in go/architect-common.md or go/worker-common.md", tc.name)
			}
		})
	}
}
