package workflow

import (
	"os"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/texts"
)

// The wake task of a release says what stopped the session and nothing the daemon does not know
// of the workspace: it names stopped_by: release and holds none of the words that unpushed work
// was kept, which the release before said whatever its restore did, nor that nothing unpushed was
// lost, which a side head breaks. The wake's record carries no prose; the passage of each reader's
// prompt on the pod-wake task says of stopped_by: release what the daemon knows, in
// WorkspaceAfterRelease's words: what the release keeps, what a workspace made again holds, and
// that a commit no working copy and no bookmark reaches is not kept. A pause keeps the pod, so its
// wake task names stopped_by: pause, and the prompt's passage says the workspace is as the role
// left it.
func TestTheReleasesWakeTaskNeverSaysUnpushedWorkWasKept(t *testing.T) {
	kept := []string{"any work you had not pushed are as you left them", "saved", "put back", "restored", "holds nothing that was not pushed"}
	for _, kind := range []string{pods.WakeChecks, pods.WakeRoleMessage, pods.WakeAgentMessage, pods.WakeBackstop, pods.WakeDelivery} {
		task := WakeTask("LEGION-208", kind, "its detail", StoppedByRelease)
		if got := readTaskOf(t, task).fields["stopped_by"]; got != texts.StoppedByRelease {
			t.Errorf("WakeTask(%s) after a release names stopped_by %q, want %q", kind, got, texts.StoppedByRelease)
		}
		for _, word := range kept {
			if strings.Contains(task, word) {
				t.Errorf("WakeTask(%s) after a release = %q, want no %q: the daemon keeps no unpushed work", kind, task, word)
			}
		}
		paused := WakeTask("LEGION-208", kind, "its detail", StoppedByPause)
		if got := readTaskOf(t, paused).fields["stopped_by"]; got != texts.StoppedByPause {
			t.Errorf("WakeTask(%s) after a pause names stopped_by %q, want %q", kind, got, texts.StoppedByPause)
		}
		if strings.Contains(paused, WorkspaceAfterRelease) {
			t.Errorf("WakeTask(%s) after a pause = %q, want no word of a release's workspace: the pause kept the pod", kind, paused)
		}
	}

	data, err := os.ReadFile("workspace-after-release.txt")
	if err != nil {
		t.Fatalf("read the data file: %v", err)
	}
	if string(data) != WorkspaceAfterRelease+"\n" {
		t.Fatalf("workspace-after-release.txt = %q, want WorkspaceAfterRelease %q and one newline", data, WorkspaceAfterRelease)
	}
	for _, path := range []string{"../prompts/go/worker-common.md", "../prompts/go/architect-common.md"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var passages []string
		for _, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "A task of the kind `pod-wake`") {
				passages = append(passages, line)
			}
		}
		if len(passages) != 1 {
			t.Fatalf("%s holds %d passages on the pod-wake task, want one", path, len(passages))
		}
		passage := passages[0]
		if !strings.Contains(passage, "`stopped_by: release`, the daemon tells you this of your workspace. "+WorkspaceAfterRelease) {
			t.Errorf("%s's passage on the pod-wake task does not give stopped_by: release the words of WorkspaceAfterRelease %q: %q", path, WorkspaceAfterRelease, passage)
		}
		if !strings.Contains(passage, "With `stopped_by: pause`, your workspace and the work you had not pushed are as you left them, since the pause kept your pod") {
			t.Errorf("%s's passage on the pod-wake task does not say that a pause kept the workspace: %q", path, passage)
		}
		for _, word := range kept {
			if strings.Contains(passage, word) {
				t.Errorf("%s's passage on the pod-wake task says %q: the daemon keeps no unpushed work", path, word)
			}
		}
	}
}
