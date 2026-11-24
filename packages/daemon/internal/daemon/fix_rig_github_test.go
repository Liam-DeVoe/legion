// The GitHub stand-in of the fix rig's daemon (fix_rig_skeleton_test.go): GitHub's REST API for
// acme/widgets as that daemon reads it, serving the rig's one pull request in step with the events
// the rig sends.
package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/githubtest"
)

// fixRigBase is the commit the base branch main stands at on the stand-in, which every event of
// the rig's pull request names as its base_sha.
const fixRigBase = "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"

// fixRigGitHub is GitHub's REST API as the fix rig's daemon reads it: any repository, on its
// default branch main at fixRigBase, and the create of an issue branch, which admission and the
// launches read; the rig's one pull request, number on branch, once the rig has opened it (open),
// at the head and with the updated_at the rig last gave it (move), mergeable and clean while it is
// open, and closed and merged at its merge commit once the rig has merged it (merge); its reviews
// as none; and a GraphQL root that knows no pull request. Every other request is answered 404 and
// kept (unanswered), so nothing reaches github.com and a read the rig did not plan is named.
//
// Each read of the pull request (GET /pulls/{number}) is kept with what it answered (reads): it is
// the read the daemon's merge state, its per-issue read and its head following all make, so the
// rig can say which read brought each merge state the daemon recorded.
type fixRigGitHub struct {
	t      *testing.T
	url    string
	number int
	branch string
	title  string

	mu         sync.Mutex
	opened     bool
	head       string
	updated    time.Time
	merged     bool
	mergeSHA   string
	reads      []fixRigPullRead
	unanswered []string
}

// fixRigPullRead is one read of the pull request the stand-in answered: when, and what it served.
type fixRigPullRead struct {
	at         time.Time
	state      string
	head       string
	mergeState string
	updated    time.Time
}

func (read fixRigPullRead) String() string {
	return fmt.Sprintf("state %s, head %s, mergeable_state %s, updated_at %s", read.state, read.head, read.mergeState,
		read.updated.Format(time.RFC3339))
}

// newFixRigGitHub starts the stand-in for pull request number of acme/widgets, on branch, titled
// title, which the rig has not opened yet.
func newFixRigGitHub(t *testing.T, number int, branch, title string) *fixRigGitHub {
	t.Helper()
	g := &fixRigGitHub{t: t, number: number, branch: branch, title: title}
	g.url = githubtest.Server(t, githubtest.Spec{Handle: g.serve})
	return g
}

// open opens the pull request at head, GitHub's clock of it at updated.
func (g *fixRigGitHub) open(head string, updated time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.opened, g.head, g.updated = true, head, updated
}

// move moves the pull request's head to head, as a push does on GitHub, GitHub's clock of it at
// updated.
func (g *fixRigGitHub) move(head string, updated time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.head, g.updated = head, updated
}

// merge merges the pull request at its head into mergeSHA, GitHub's clock of it at updated.
func (g *fixRigGitHub) merge(mergeSHA string, updated time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.merged, g.mergeSHA, g.updated = true, mergeSHA, updated
}

// pullReads is each read of the pull request the stand-in answered, in order.
func (g *fixRigGitHub) pullReads() []fixRigPullRead {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.reads)
}

// notAnswered is each request the stand-in answered 404, as "METHOD /path?query", in order.
func (g *fixRigGitHub) notAnswered() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.unanswered)
}

func (g *fixRigGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rest, isRepository := strings.CutPrefix(r.URL.Path, "/repos/")
	parts := strings.SplitN(rest, "/", 3)
	tail := ""
	if len(parts) == 3 {
		tail = parts[2]
	}
	pull := "pulls/" + strconv.Itoa(g.number)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/graphql":
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":null}}}`)
		return
	case !isRepository || len(parts) < 2:
	case r.Method == http.MethodGet && len(parts) == 2:
		g.write(w, map[string]any{"full_name": parts[0] + "/" + parts[1], "default_branch": "main"})
		return
	case parts[0]+"/"+parts[1] != liveRepo:
	case r.Method == http.MethodGet && tail == "git/ref/heads/main":
		g.write(w, map[string]any{"ref": "refs/heads/main", "object": map[string]string{"sha": fixRigBase, "type": "commit"}})
		return
	case r.Method == http.MethodPost && tail == "git/refs":
		w.WriteHeader(http.StatusCreated)
		g.write(w, map[string]any{"ref": "refs/heads/" + g.branch, "object": map[string]string{"sha": fixRigBase}})
		return
	case r.Method == http.MethodGet && tail == "pulls":
		// The daemon lists the open pull request of a branch (openPullRequestOn): the rig's, once
		// it is open and until it merges.
		listed := []any{}
		if g.opened && !g.merged {
			listed = append(listed, g.pull())
		}
		g.write(w, listed)
		return
	case r.Method == http.MethodGet && tail == pull && g.opened:
		answer := g.pull()
		g.reads = append(g.reads, fixRigPullRead{at: time.Now(), state: answer["state"].(string), head: g.head,
			mergeState: answer["mergeable_state"].(string), updated: g.updated})
		g.write(w, answer)
		return
	case r.Method == http.MethodGet && tail == pull+"/reviews" && g.opened:
		g.write(w, []any{})
		return
	}
	g.unanswered = append(g.unanswered, r.Method+" "+r.URL.RequestURI())
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"message":"Not Found"}`)
}

// pull is the pull request as GitHub's REST API answers it now: open, mergeable and clean at its
// head, or closed and merged at its merge commit, where GitHub answers `unknown` for good. Its
// caller holds g.mu.
func (g *fixRigGitHub) pull() map[string]any {
	state, mergeState := "open", "clean"
	if g.merged {
		state, mergeState = "closed", "unknown"
	}
	return map[string]any{
		"number": g.number, "state": state, "title": g.title, "body": "",
		"html_url":   fmt.Sprintf("https://github.com/%s/pull/%d", liveRepo, g.number),
		"base":       map[string]string{"ref": "main", "sha": fixRigBase},
		"head":       map[string]any{"ref": g.branch, "sha": g.head, "repo": map[string]string{"full_name": liveRepo}},
		"updated_at": g.updated.UTC().Format(time.RFC3339), "mergeable": true, "mergeable_state": mergeState,
		"merged": g.merged, "merge_commit_sha": g.mergeSHA,
	}
}

// write encodes value as the answer.
func (g *fixRigGitHub) write(w http.ResponseWriter, value any) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		g.t.Errorf("write the fix rig's GitHub stand-in's answer: %v", err)
	}
}
