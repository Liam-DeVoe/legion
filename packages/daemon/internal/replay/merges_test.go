package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The span the tests of collect-merges's walk read acme/widgets's main over: the first six hours
// of 2026-11-20.
var (
	walkFrom = time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC)
	walkTo   = time.Date(2026, time.November, 20, 6, 0, 0, 0, time.UTC)
)

// stubCommit is one commit of acme/widgets's main that githubListing serves: its sha, its
// committer's time, the pull request its subject's squash suffix names (none where 0), its
// parents, the first parent first, and whether the listing lacks it whatever its time.
type stubCommit struct {
	sha      string
	at       time.Time
	pr       int
	parents  []string
	unlisted bool
}

// stubSHA is the sha of the stub commit numbered n: n in hex, 40 digits long.
func stubSHA(n int) string { return fmt.Sprintf("%040x", n) }

// stubbed is the stub commit numbered n, committed at at (RFC 3339), the squash merge of pull
// request pr (none where 0), whose parents are the commits numbered parents, the first parent
// first.
func stubbed(t *testing.T, n int, at string, pr int, parents ...int) stubCommit {
	t.Helper()
	commit := stubCommit{sha: stubSHA(n), at: capturedTime(t, at), pr: pr}
	for _, parent := range parents {
		commit.parents = append(commit.parents, stubSHA(parent))
	}
	return commit
}

// merge is c as collect-merges writes it: on main, at its committer's time, with its pull request.
func (c stubCommit) merge() Merge {
	return Merge{SHA: c.sha, At: c.at, Branch: "main", PR: c.pr}
}

// listed is c as GitHub's commit listing and its read of one commit hold it, cut to what
// collect-merges reads.
func (c stubCommit) listed() map[string]any {
	message := "feat(widgets): a change\n\nThe change's body."
	if c.pr != 0 {
		message = fmt.Sprintf("feat(widgets): a change (#%d)\n\nThe change's body.", c.pr)
	}
	parents := []map[string]string{}
	for _, parent := range c.parents {
		parents = append(parents, map[string]string{"sha": parent})
	}
	return map[string]any{"sha": c.sha, "parents": parents,
		"commit": map[string]any{"message": message, "committer": map[string]any{"date": c.at.Format(time.RFC3339)}}}
}

// githubListing serves acme/widgets's main from history, the branch's commits newest first.
// GitHub's commit listing of main is served as GitHub filters it: on one page, each commit whose
// committer's time is at or after the request's since and, where the request has an until, at or
// before it, but a commit marked unlisted. Each commit of history, unlisted or not, is served by
// its sha (/repos/acme/widgets/commits/<sha>). A request for anything else, or without the token,
// is refused.
func githubListing(t *testing.T, history []stubCommit) (*httptest.Server, *stubRequests) {
	t.Helper()
	requests := &stubRequests{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r)
		serve := func(answer any) {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(answer); err != nil {
				t.Errorf("serve %s: %v", r.URL.RequestURI(), err)
			}
		}
		query := r.URL.Query()
		if r.Header.Get("Authorization") != "Bearer "+stubGitHubToken || r.Method != http.MethodGet {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		if sha, one := strings.CutPrefix(r.URL.Path, "/repos/acme/widgets/commits/"); one {
			for _, commit := range history {
				if commit.sha == sha {
					serve(commit.listed())
					return
				}
			}
			http.Error(w, `{"message":"No commit found for SHA: `+sha+`"}`, http.StatusUnprocessableEntity)
			return
		}
		if r.URL.Path != "/repos/acme/widgets/commits" || query.Get("sha") != "main" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		bound := func(name string) *time.Time {
			if !query.Has(name) {
				return nil
			}
			at, err := time.Parse(time.RFC3339, query.Get(name))
			if err != nil {
				t.Errorf("the listing's %s is %q, no RFC 3339 time", name, query.Get(name))
			}
			return &at
		}
		since, until := bound("since"), bound("until")
		page := []map[string]any{}
		for _, commit := range history {
			if !commit.unlisted && (since == nil || !commit.at.Before(*since)) && (until == nil || !commit.at.After(*until)) {
				page = append(page, commit.listed())
			}
		}
		serve(page)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

// walkRun is one collect-merges of acme/widgets's main over [walkFrom, walkTo) against
// githubListing: what it answered, the window it wrote into, that window's files before it ran,
// and the requests the stub served.
type walkRun struct {
	merges   []Merge
	err      error
	window   string
	before   map[string][]byte
	requests []stubRequest
}

// collectWalk runs collect-merges of acme/widgets's main over [walkFrom, walkTo) against
// githubListing serving history, newest first, into a window of its own that holds no merges.
func collectWalk(t *testing.T, history ...stubCommit) walkRun {
	t.Helper()
	run := walkRun{window: filepath.Join(t.TempDir(), "window")}
	if err := (Window{Project: "ACME", From: walkFrom, To: walkTo}).Write(run.window); err != nil {
		t.Fatal(err)
	}
	run.before = windowFiles(t, run.window)
	server, seen := githubListing(t, history)
	run.merges, run.err = CollectMerges(context.Background(), MergesConfig{Repo: "acme/widgets", Branch: "main",
		From: walkFrom, To: walkTo, Token: stubGitHubToken, API: server.URL, Window: run.window})
	run.requests = seen.all()
	return run
}

// wantWritten fails t unless run answered want, oldest first, and wrote it into its window as
// the merges of acme/widgets.
func (run walkRun) wantWritten(t *testing.T, want []Merge) {
	t.Helper()
	if run.err != nil {
		t.Fatalf("CollectMerges: %v", run.err)
	}
	if !sameReading(run.merges, want) {
		t.Errorf("CollectMerges answered\n  %s\nwant\n  %s", describeReading(run.merges), describeReading(want))
	}
	read, err := ReadWindow(run.window)
	if err != nil {
		t.Fatalf("ReadWindow of the window collect-merges wrote into: %v", err)
	}
	if !sameReading(read.Merges["acme/widgets"], want) {
		t.Errorf("the window's merges of acme/widgets are\n  %s\nwant\n  %s", describeReading(read.Merges["acme/widgets"]), describeReading(want))
	}
}

// windowFiles is every file under dir, by its slash-separated path under dir, with its bytes.
func windowFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(name)], err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// A first parent can be committed after its child, and the walk goes on through it. main's
// first-parent chain holds a commit of 04:00 whose first parent was committed at 08:00, after the
// span's end; below that parent are two commits inside the span and one of the day before, whose
// first parent the listing lacks, where the walk ends. Every first-parent commit in [walkFrom,
// walkTo) is kept, and the one of 08:00 is walked and not kept. It fails where the listing has an
// until, which leaves out the parent of 08:00, so that the walk stops at the commit of 04:00.
func TestCollectMergesWalksThroughAParentCommittedAfterTheSpan(t *testing.T) {
	before := stubbed(t, 0x100, "2026-11-19T23:00:00Z", 100, 0x99)
	first := stubbed(t, 0x101, "2026-11-20T01:00:00Z", 101, 0x100)
	second := stubbed(t, 0x102, "2026-11-20T02:00:00Z", 102, 0x101)
	late := stubbed(t, 0x103, "2026-11-20T08:00:00Z", 103, 0x102)
	child := stubbed(t, 0x104, "2026-11-20T04:00:00Z", 104, 0x103)
	tip := stubbed(t, 0x105, "2026-11-20T05:00:00Z", 105, 0x104)
	run := collectWalk(t, tip, child, late, second, first, before)
	run.wantWritten(t, []Merge{first.merge(), second.merge(), child.merge(), tip.merge()})
}

// The walk starts at the branch's tip, the first commit listed, whatever its time. main's tip was
// committed at 09:00, after the span, and its first parent at 07:00: a merge commit, whose first
// parent is a squash merge of 05:00 and whose second a commit of 05:30 on the merged branch, both
// above the branch's root, of 03:00. The two first-parent commits in the span are kept, the two
// after it are walked and not kept, and the walk ends at the root, inside the span, as a branch's
// history does. It fails where the listing has an until: the first commit listed is then the
// merged branch's of 05:30, and a walk from it keeps that commit in place of the squash merge.
func TestCollectMergesWalksFromATipCommittedAfterTheSpan(t *testing.T) {
	root := stubbed(t, 0x200, "2026-11-20T03:00:00Z", 0)
	squash := stubbed(t, 0x201, "2026-11-20T05:00:00Z", 201, 0x200)
	side := stubbed(t, 0x202, "2026-11-20T05:30:00Z", 0, 0x200)
	merged := stubbed(t, 0x203, "2026-11-20T07:00:00Z", 0, 0x201, 0x202)
	tip := stubbed(t, 0x204, "2026-11-20T09:00:00Z", 204, 0x203)
	run := collectWalk(t, tip, merged, side, squash, root)
	run.wantWritten(t, []Merge{root.merge(), squash.merge()})
}

// A walk that ends at a first parent the listing lacks, while the last commit it walked is inside
// the span, reads that parent. One committed at or after the listing's since is a commit the
// listing should hold, so the span's history before it would be left out: collect-merges refuses
// the walk, naming the repository, the branch, the last commit walked and that parent, each with
// its time, and writes nothing into the window. Here the listing lacks the first parent, of 02:00,
// of main's commit of 03:00. It fails where the short history is written.
func TestCollectMergesRefusesAWalkThatEndsInsideTheSpan(t *testing.T) {
	lacked := stubbed(t, 0x300, "2026-11-20T02:00:00Z", 300, 0x299)
	lacked.unlisted = true
	last := stubbed(t, 0x301, "2026-11-20T03:00:00Z", 301, 0x300)
	tip := stubbed(t, 0x302, "2026-11-20T05:00:00Z", 302, 0x301)
	run := collectWalk(t, tip, last, lacked)
	if run.err == nil {
		t.Errorf("CollectMerges answered %s and no error, want a refusal", describeReading(run.merges))
	} else {
		for _, part := range []string{"acme/widgets", "main", last.sha, "2026-11-20T03:00:00Z", lacked.sha, "2026-11-20T02:00:00Z"} {
			if !strings.Contains(run.err.Error(), part) {
				t.Errorf("CollectMerges refuses with %q, which does not name %s", run.err, part)
			}
		}
	}
	if after := windowFiles(t, run.window); !reflect.DeepEqual(after, run.before) {
		t.Errorf("the window holds %v after the refusal, want its files as they were before it: %v",
			slices.Sorted(maps.Keys(after)), slices.Sorted(maps.Keys(run.before)))
	}
}

// A branch can be quiet for longer than the margin before the span: main's first commit in the
// span, of 03:00, has a first parent committed on 2026-11-16, days before the listing's since. The
// walk ends at that parent, which the listing lacks; collect-merges reads it, finds it committed
// before since, and keeps the span's commits, since none of them is left out. Its requests are the
// listing and that one read, each a GET. It fails where collect-merges refuses the walk or does not
// read the parent.
func TestCollectMergesEndsAQuietBranchsWalkAtAParentBeforeTheListing(t *testing.T) {
	quiet := stubbed(t, 0x500, "2026-11-16T12:00:00Z", 500, 0x499)
	first := stubbed(t, 0x501, "2026-11-20T03:00:00Z", 501, 0x500)
	tip := stubbed(t, 0x502, "2026-11-20T05:00:00Z", 502, 0x501)
	run := collectWalk(t, tip, first, quiet)
	run.wantWritten(t, []Merge{first.merge(), tip.merge()})
	want := []stubRequest{
		{Method: http.MethodGet, Target: "/repos/acme/widgets/commits?per_page=100&sha=main&since=2026-11-19T00%3A00%3A00Z"},
		{Method: http.MethodGet, Target: "/repos/acme/widgets/commits/" + quiet.sha},
	}
	if !slices.Equal(run.requests, want) {
		t.Errorf("collect-merges sent %v, want the listing and a read of %s: %v", run.requests, quiet.sha, want)
	}
}

// collect-merges lists main from a day before the span's start, so the walk reaches below the
// span, and with no until, so a first parent committed after its child is listed: its one request
// is sha=main, since=2026-11-19T00:00:00Z and per_page=100, with no until.
func TestCollectMergesListsFromADayBeforeTheSpanWithNoUntil(t *testing.T) {
	below := stubbed(t, 0x400, "2026-11-19T12:00:00Z", 400, 0x399)
	tip := stubbed(t, 0x401, "2026-11-20T05:00:00Z", 401, 0x400)
	run := collectWalk(t, tip, below)
	if run.err != nil {
		t.Fatalf("CollectMerges: %v", run.err)
	}
	if len(run.requests) != 1 {
		t.Fatalf("collect-merges sent %v, want one request", run.requests)
	}
	target, err := url.Parse(run.requests[0].Target)
	if err != nil {
		t.Fatal(err)
	}
	if query := target.Query(); query.Get("sha") != "main" || query.Get("since") != "2026-11-19T00:00:00Z" ||
		query.Get("per_page") != "100" || query.Has("until") {
		t.Errorf("collect-merges listed %s, want sha=main, since=2026-11-19T00:00:00Z and per_page=100, with no until", run.requests[0].Target)
	}
}
