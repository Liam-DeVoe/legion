package pause

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// Main's project-paused notice (pause/pause.go:pausedNotice at d46ec4e0) named who paused in
// `by`, as record.ControllerNotice{Kind: record.ProjectPausedNotice, Pause:
// &record.ProjectPause{ID: pause.ID, By: actor(pause.By), At: pause.Since, Reason: pause.Reason}},
// and the controller can pause its project itself (api/pause.go: a grant's session pauses as
// store.PauseByController), so that notice reaches the controller with `by.kind: controller`.
// INFRA-3686's R2: "The fields are those of the kind, the issue, the act, the actor, the time …";
// the change's own wrong fact is its defect (the architect's ruling). The branch's controller
// prompt, which main did not have for this kind, says of every project-paused notice that "the
// operator paused the project" and that "the operator's reason is in a block of its own".
func TestTheControllersPromptSaysWhoPausedAsTheNoticeDoes(t *testing.T) {
	at := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	notice, err := record.ControllerNoticeOf(PausedNotice(store.Pause{ID: 7, Project: "LEGION", Since: at,
		By: store.PauseActor{Kind: store.PauseByController, Session: "01a1ed54-c1b4-73b0-89f1-a9327401d421"}, Reason: "the controller's own words"}))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(notice)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"kind":"controller"`) {
		t.Fatalf("the notice of the controller's pause does not name the controller: %s", payload)
	}
	body, err := os.ReadFile(filepath.Join("..", "prompts", "roles", "controller-root.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.Contains(line, "A `project-paused` notice") {
			continue
		}
		if strings.Contains(line, "the operator paused the project") || strings.Contains(line, "the operator's reason") {
			t.Errorf("roles/controller-root.md says of every project-paused notice %q, but the notice of the controller's own pause is:\n%s", line, payload)
		}
	}
}
