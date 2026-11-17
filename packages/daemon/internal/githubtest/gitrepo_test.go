package githubtest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
)

// serveRepo is a fake GitHub that answers what g answers and fails the test on any other request.
func serveRepo(t *testing.T, g *GitRepo) string {
	t.Helper()
	return Server(t, Spec{Handle: func(w http.ResponseWriter, r *http.Request) {
		if !g.Handler(w, r) {
			Unexpected(t, w, r)
		}
	}})
}

// send makes one request of the fake and answers its status and body.
func send(t *testing.T, method, url string, body any) (int, string) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode %v: %v", body, err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		t.Fatalf("make %s %s: %v", method, url, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the answer to %s %s: %v", method, url, err)
	}
	return response.StatusCode, string(answer)
}

// gitIn runs git in the bare repository dir and answers its output, trimmed.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = fixtureenv.Isolated(t.TempDir())
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The fake answers the reads and the create of a ref as GitHub's REST does: the repository names
// its default branch, a branch it has names its head and one it lacks is 404, a create makes the
// ref at the commit it names, and a create of a ref it has is 422 "Reference already exists" with
// the ref left where it was. A path it does not serve is the caller's.
func TestTheFakeReadsAndCreatesRefsAsGitHubDoes(t *testing.T) {
	g := GitRepository(t, "acme", "widgets", "main")
	api := serveRepo(t, g)
	main := g.Head(t, "main")
	if main == "" || main != gitIn(t, g.Dir(), "rev-parse", "refs/heads/main") {
		t.Fatalf("Head(main) = %q, want the one commit the repository starts with", main)
	}
	if status, body := send(t, http.MethodGet, api+"/repos/acme/widgets", nil); status != http.StatusOK || !strings.Contains(body, `"default_branch":"main"`) || !strings.Contains(body, `"full_name":"acme/widgets"`) {
		t.Fatalf("GET the repository = %d %s", status, body)
	}
	if status, body := send(t, http.MethodGet, api+"/repos/acme/widgets/git/ref/heads/main", nil); status != http.StatusOK || !strings.Contains(body, main) {
		t.Fatalf("GET refs/heads/main = %d %s, want its head %s", status, body, main)
	}
	if status, _ := send(t, http.MethodGet, api+"/repos/acme/widgets/git/ref/heads/legion/ACME-1", nil); status != http.StatusNotFound {
		t.Fatalf("GET a branch the repository lacks = %d, want 404", status)
	}
	if status, body := send(t, http.MethodPost, api+"/repos/acme/widgets/git/refs", map[string]string{"ref": "refs/heads/legion/ACME-1", "sha": main}); status != http.StatusCreated {
		t.Fatalf("create legion/ACME-1 = %d %s, want 201", status, body)
	}
	moved := g.Commit(t, "legion/ACME-1", map[string]string{"b.txt": "b\n"}, "b")
	if status, body := send(t, http.MethodPost, api+"/repos/acme/widgets/git/refs", map[string]string{"ref": "refs/heads/legion/ACME-1", "sha": main}); status != http.StatusUnprocessableEntity || !strings.Contains(body, "Reference already exists") {
		t.Fatalf("create of a ref the repository has = %d %s, want 422 Reference already exists", status, body)
	}
	if head := g.Head(t, "legion/ACME-1"); head != moved {
		t.Fatalf("legion/ACME-1 after the second create stands at %s, want %s, where it was", head, moved)
	}
	recorder := httptest.NewRecorder()
	if g.Handler(recorder, httptest.NewRequest(http.MethodGet, "/repos/acme/widgets/pulls", nil)) {
		t.Fatal("the fake answered GET /repos/acme/widgets/pulls, a path it does not serve")
	}
	if recorder.Body.Len() != 0 || recorder.Code != http.StatusOK {
		t.Fatalf("the fake wrote %d %q for a path it does not serve, want nothing written", recorder.Code, recorder.Body.String())
	}
}

// A merge of one branch into another is GitHub's: a merge commit on the base whose parents are the
// base's head and the head's head, in that order, answered with 201 and its sha; a head the base
// already holds is 204 and moves nothing.
func TestTheFakeMergesTwoBranchesAsGitHubDoes(t *testing.T) {
	g := GitRepository(t, "acme", "widgets", "main")
	api := serveRepo(t, g)
	a := g.Commit(t, "a", map[string]string{"a.txt": "a\n"}, "a")
	c := g.Commit(t, "c", map[string]string{"c.txt": "c\n"}, "c")
	status, body := send(t, http.MethodPost, api+"/repos/acme/widgets/merges", map[string]string{"base": "a", "head": "c", "commit_message": "ACME-1: join the work of task c"})
	var merged struct {
		SHA string `json:"sha"`
	}
	if status != http.StatusCreated || json.Unmarshal([]byte(body), &merged) != nil || merged.SHA == "" {
		t.Fatalf("merge c into a = %d %s, want 201 with the merge commit's sha", status, body)
	}
	if parents := gitIn(t, g.Dir(), "rev-list", "--parents", "-n", "1", merged.SHA); parents != merged.SHA+" "+a+" "+c {
		t.Fatalf("the merge commit and its parents = %q, want %s %s %s", parents, merged.SHA, a, c)
	}
	if head := g.Head(t, "a"); head != merged.SHA {
		t.Fatalf("a stands at %s after the merge, want the merge commit %s", head, merged.SHA)
	}
	if message := gitIn(t, g.Dir(), "log", "-1", "--format=%s", merged.SHA); message != "ACME-1: join the work of task c" {
		t.Fatalf("the merge commit's message = %q", message)
	}
	if status, body := send(t, http.MethodPost, api+"/repos/acme/widgets/merges", map[string]string{"base": "a", "head": "c", "commit_message": "again"}); status != http.StatusNoContent || body != "" {
		t.Fatalf("merge of a head the base holds = %d %q, want 204 and no body", status, body)
	}
	if head := g.Head(t, "a"); head != merged.SHA {
		t.Fatalf("a stands at %s after a merge of nothing, want %s", head, merged.SHA)
	}
	if status, _ := send(t, http.MethodPost, api+"/repos/acme/widgets/merges", map[string]string{"base": "a", "head": "missing", "commit_message": "x"}); status != http.StatusNotFound {
		t.Fatalf("merge of a head the repository lacks = %d, want 404", status)
	}
}

// Two branches that change the same line of one file conflict: GitHub answers 409 "Merge conflict"
// and the base stays where it was.
func TestTheFakeAnswersAConflictWith409(t *testing.T) {
	g := GitRepository(t, "acme", "widgets", "main")
	api := serveRepo(t, g)
	a := g.Commit(t, "a", map[string]string{"x.txt": "from a\n"}, "a")
	g.Commit(t, "c", map[string]string{"x.txt": "from c\n"}, "c")
	status, body := send(t, http.MethodPost, api+"/repos/acme/widgets/merges", map[string]string{"base": "a", "head": "c", "commit_message": "join"})
	if status != http.StatusConflict || !strings.Contains(body, "Merge conflict") {
		t.Fatalf("merge of a conflicting head = %d %s, want 409 Merge conflict", status, body)
	}
	if head := g.Head(t, "a"); head != a {
		t.Fatalf("a stands at %s after the conflict, want %s, where it was", head, a)
	}
}
