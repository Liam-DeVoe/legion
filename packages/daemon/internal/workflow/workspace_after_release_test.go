package workflow

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/pods"
)

// The wake task of a release says what the daemon knows of the workspace and nothing it does not:
// the release took the pod only because nothing unpushed was in it, and a workspace made again holds
// what was pushed. It never says that unpushed work was kept, which the release before said whatever
// its restore did. A pause keeps the pod, so its wake task says the workspace is as the role left it.
func TestTheReleasesWakeTaskNeverSaysUnpushedWorkWasKept(t *testing.T) {
	for _, kind := range []string{pods.WakeChecks, pods.WakeRoleMessage, pods.WakeAgentMessage, pods.WakeBackstop, pods.WakeDelivery} {
		task := WakeTask(kind, "its detail", StoppedByRelease)
		if !strings.Contains(task, WorkspaceAfterRelease) {
			t.Errorf("WakeTask(%s) after a release = %q, want it to say %q", kind, task, WorkspaceAfterRelease)
		}
		for _, kept := range []string{"any work you had not pushed are as you left them", "saved", "put back", "restored"} {
			if strings.Contains(task, kept) {
				t.Errorf("WakeTask(%s) after a release = %q, want no %q: the daemon keeps no unpushed work", kind, task, kept)
			}
		}
		if paused := WakeTask(kind, "its detail", StoppedByPause); strings.Contains(paused, WorkspaceAfterRelease) {
			t.Errorf("WakeTask(%s) after a pause = %q, want no word of a release's workspace: the pause kept the pod", kind, paused)
		}
	}
}
