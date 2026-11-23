package intake

import (
	"encoding/json"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// A pull request whose head branch lives in another repository than the one it is opened against
// - a fork's - is no issue's, whatever its branch is named: anyone may open one and name its head
// legion/<KEY> or a task's branch, legion/<KEY>.<task>. The listener names the repository of the
// head (head_repo, GitHub's head.repo.full_name, which it leaves out once GitHub no longer has the
// fork), and every event of such a pull request is no fact, so it is acknowledged without a
// transaction and never reaches the workflow. The same events of a pull request whose head is a
// branch of the repository itself, in whatever case GitHub spells its name, decode to their facts,
// and the opened carries the repository of its head.
//
// The fault: the decode reads head_ref alone, so a fork's pull request on a task's branch decodes
// to that task's facts. The control: the decode without its read of head_repo.
func TestAPullRequestFromAForkIsNoFact(t *testing.T) {
	repositories := []ghrepo.Repository{ghrepo.MustParse("sjawhar/legion")}
	decode := func(t *testing.T, action, branch, headRepo string) Fact {
		t.Helper()
		payload := map[string]string{"kind": "pr", "action": action, "repo": "sjawhar/legion", "number": "9", "head_ref": branch,
			"head_sha": "head-9", "before": "head-8", "base_ref": "main", "merged": "false", "updated_at": "2026-11-23T09:00:00Z"}
		if action == "merged" {
			payload["action"], payload["merged"], payload["merge_commit_sha"] = "closed", "true", "merge-9"
		}
		if headRepo != "" {
			payload["head_repo"] = headRepo
		}
		text, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeMessage("notifications.github.sjawhar.legion.pr.9", "LEGION", repositories, envelopeJSON(t, "fork-"+action, "github", string(text)))
		if err != nil {
			t.Fatalf("decode the %s of #9 on %s: %v", action, branch, err)
		}
		return decoded.Fact
	}
	for _, branch := range []string{"legion/LEGION-208.impl", "legion/LEGION-208"} {
		for _, action := range []string{"opened", "reopened", "synchronize", "closed", "merged", "dequeued"} {
			// A fork's head, and the head of a fork GitHub no longer has.
			for _, headRepo := range []string{"someone/legion", ""} {
				if fact := decode(t, action, branch, headRepo); fact != nil {
					t.Errorf("the %s of #9 on %s, its head in %q, decoded %#v; want no fact: its head is no branch of sjawhar/legion", action, branch, headRepo, fact)
				}
			}
			if fact := decode(t, action, branch, "Sjawhar/Legion"); fact == nil {
				t.Errorf("the %s of #9 on %s, its head in Sjawhar/Legion, decoded no fact; want its fact: its head is a branch of sjawhar/legion", action, branch)
			}
		}
		if opened, ok := decode(t, "opened", branch, "sjawhar/legion").(PullRequestOpened); !ok || opened.Branch != branch || opened.HeadRepo != "sjawhar/legion" {
			t.Errorf("the open of #9 on %s decoded %#v; want the opened on %s whose head is in sjawhar/legion", branch, opened, branch)
		}
	}
}
