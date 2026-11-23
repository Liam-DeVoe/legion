package prompts

import "testing"

// A merge forward's hold reaches the worker of the phase the issue stays in where that is the
// tester, the reviewer or, at step, the role of the deployment whose step runs
// (record.MergeForwardReaches, workflow's mergeForwardWorker), so both Legion parts that say who is
// told to stop writing the workspace name the role of the deployment too. Each part is read alone.
// This shows the text is loaded, never that a role follows it.
func TestTheHoldOfAMergeForwardNamesTheRoleOfARunningStep(t *testing.T) {
	for _, part := range []struct {
		name, text    string
		want, retired []string
	}{
		{"go/worker-common.md", goPart(t, "worker-common.md"), []string{
			"In `testing`, `reviewing` and `step`, the worker of the phase is told to stop writing the workspace at once. It is told not to complete its phase at the old head. Nothing of its is stopped:",
		}, []string{"The worker of the phase, when that is the tester or the reviewer, is told to stop writing"}},
		{"go/architect-common.md", goPart(t, "architect-common.md"), []string{
			"pushing with no handoff and no completion. The tester, the reviewer or the role of the deployment holding the phase got a message to stop writing the workspace. It keeps its turn, its jobs and its session.",
		}, []string{"the tester or the reviewer holding the phase got a message"}},
	} {
		holdsAndRetires(t, part.name, part.text, part.want, part.retired)
	}
}

// The daemon's render of a task that carries news of the approved design (daemon's renderNews)
// puts the news before a reviewer's two sentences of its round and after the whole text of every
// other task, a review step's of a role of the deployment included. So the part that says where
// `Task text:` stands names the news beside the review sentences, and no longer says that the
// review step's one sentence ends its task. This shows the text is loaded, never that a role
// follows it.
func TestTheTaskTextIsLastButForTheReviewSentencesAndTheNews(t *testing.T) {
	holdsAndRetires(t, "go/worker-common.md", goPart(t, "worker-common.md"), []string{
		"`Task text:` is the text of the task's block in the plan, what the task must deliver, which you read beside the issue's spec. It comes last in your task, except for the daemon's review sentences named below and the news of a news task (below).",
	}, []string{"the one that ends the task of a review step of a role of the deployment"})
}
