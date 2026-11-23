package replay

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// DefaultGitHubAPI is GitHub's REST API, which collect-merges reads where its caller names no
// other (a GitHub Enterprise server's, or a test's).
const DefaultGitHubAPI = "https://api.github.com"

// mergesPageSize is how many commits a page of GitHub's listing holds, its largest.
const mergesPageSize = 100

// mergesPageLimit bounds the pages one branch is read in, so a listing that never ends is refused
// rather than read for ever: a hundred thousand commits in a window's span.
const mergesPageLimit = 1000

// MergesConfig is one read of a base branch's first-parent history into a window (CollectMerges).
type MergesConfig struct {
	// Repo is the repository as owner/name, and Branch its base branch.
	Repo, Branch string
	// From and To bound the commits listed: GitHub's since and until.
	From, To time.Time
	// Token is the GitHub token the reads are made with, sent as the Authorization header alone
	// and never written anywhere.
	Token string
	// API is GitHub's REST API (DefaultGitHubAPI); every page is read from its scheme and host.
	API string
	// Window is the directory of the window the merges are written into, which collect wrote.
	Window string
	// Client makes the requests; nil is a client whose every request ends within a minute.
	Client *http.Client
	// Wait is how a read GitHub refused for now waits before it is tried again; nil waits on a
	// timer.
	Wait func(ctx context.Context, d time.Duration) error
}

// CollectMerges reads cfg.Branch's first-parent history in [cfg.From, cfg.To) from GitHub (GET
// /repos/<owner>/<name>/commits?sha=<branch>&since=<from>&until=<to>, every page by its Link
// header, then the walk along first parents ParseMerges makes) and writes it into the window in
// cfg.Window as merges/<owner>__<name>.jsonl, in place of the commits of that branch the file held
// and beside those of the repository's other base branches, with window.json's sha256 of the file
// written again. Each commit keeps the pull request it merged (Merge.PR) where the first line of
// its message ends with GitHub's squash suffix ` (#<n>)`, and none where it does not, so the
// conflict draw can leave a stretch's own merge out of its count. It sends GET alone, each read
// GitHub refuses for now (429, a 5xx, no answer) tried again with a growing wait. It returns the
// branch's commits as written, oldest first.
func CollectMerges(ctx context.Context, cfg MergesConfig) ([]Merge, error) {
	owner, name, ok := strings.Cut(cfg.Repo, "/")
	switch {
	case !ok || owner == "" || name == "" || strings.Contains(name, "/") || strings.Contains(owner, "__"):
		return nil, fmt.Errorf("replay: collect-merges: %q is no owner/name of a repository", cfg.Repo)
	case cfg.Branch == "":
		return nil, errors.New("replay: collect-merges: no branch is named")
	case !cfg.From.Before(cfg.To):
		return nil, fmt.Errorf("replay: collect-merges: the span [%s, %s) is empty", cfg.From.Format(time.RFC3339), cfg.To.Format(time.RFC3339))
	case cfg.Token == "":
		return nil, errors.New("replay: collect-merges: no GitHub token is given")
	}
	api, err := url.Parse(strings.TrimRight(cmp.Or(cfg.API, DefaultGitHubAPI), "/"))
	if err != nil || (api.Scheme != "https" && api.Scheme != "http") || api.Host == "" {
		return nil, fmt.Errorf("replay: collect-merges: %q is no http or https URL of GitHub's API", cfg.API)
	}
	w, err := ReadWindow(cfg.Window)
	if err != nil {
		return nil, fmt.Errorf("replay: collect-merges: %w", err)
	}
	reader := githubReader{api: api, token: cfg.Token, client: cfg.Client, wait: cfg.Wait}
	if reader.client == nil {
		reader.client = &http.Client{Timeout: time.Minute}
	}
	if reader.wait == nil {
		reader.wait = waitOnTimer
	}
	query := url.Values{}
	query.Set("sha", cfg.Branch)
	query.Set("since", cfg.From.UTC().Format(time.RFC3339))
	query.Set("until", cfg.To.UTC().Format(time.RFC3339))
	query.Set("per_page", fmt.Sprint(mergesPageSize))
	first := api.JoinPath("repos", owner, name, "commits")
	first.RawQuery = query.Encode()
	pages, err := reader.pages(ctx, first)
	if err != nil {
		return nil, fmt.Errorf("replay: collect-merges: the commits of %s %s: %w", cfg.Repo, cfg.Branch, err)
	}
	merges, err := ParseMerges(cfg.Branch, pages...)
	if err != nil {
		return nil, fmt.Errorf("replay: collect-merges: %w", err)
	}
	kept := slices.DeleteFunc(slices.Clone(w.Merges[cfg.Repo]), func(m Merge) bool { return m.Branch == cfg.Branch })
	kept = append(kept, merges...)
	slices.SortStableFunc(kept, func(a, b Merge) int { return a.At.Compare(b.At) })
	w.Merges[cfg.Repo] = kept
	if err := w.Write(cfg.Window); err != nil {
		return nil, fmt.Errorf("replay: collect-merges: %w", err)
	}
	return merges, nil
}

// githubReader reads the pages of one listing of GitHub's REST API with a token.
type githubReader struct {
	api    *url.URL
	token  string
	client *http.Client
	wait   func(ctx context.Context, d time.Duration) error
}

// nextPage is the next page's URL in a Link header GitHub writes on a listing's page.
var nextPage = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// pages reads first and every page its Link headers lead to, in order. A next page on another
// scheme or host than the API's is refused, so the token goes nowhere else.
func (r githubReader) pages(ctx context.Context, first *url.URL) ([][]byte, error) {
	var pages [][]byte
	for next := first; next != nil; {
		if len(pages) == mergesPageLimit {
			return nil, fmt.Errorf("the listing runs past %d pages", mergesPageLimit)
		}
		if next.Scheme != r.api.Scheme || next.Host != r.api.Host {
			return nil, fmt.Errorf("the next page is at %s://%s, not at the API's %s://%s", next.Scheme, next.Host, r.api.Scheme, r.api.Host)
		}
		var body []byte
		var link string
		err := retry(ctx, r.wait, retryableGitHub, func() (err error) {
			body, link, err = r.get(ctx, next)
			return err
		})
		if err != nil {
			return nil, err
		}
		pages = append(pages, body)
		next = nil
		if match := nextPage.FindStringSubmatch(link); match != nil {
			if next, err = url.Parse(match[1]); err != nil {
				return nil, fmt.Errorf("the next page %q is no URL: %w", match[1], err)
			}
		}
	}
	return pages, nil
}

// get reads one page: its body and its Link header.
func (r githubReader) get(ctx context.Context, page *url.URL) ([]byte, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, page.String(), nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("Authorization", "Bearer "+r.token)
	response, err := r.client.Do(request)
	if err != nil {
		return nil, "", &githubUnanswered{err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, "", &githubUnanswered{err: err}
	}
	if response.StatusCode/100 != 2 {
		return nil, "", &githubRefusal{status: response.StatusCode, body: string(bytes.TrimSpace(body[:min(len(body), 300)]))}
	}
	return body, response.Header.Get("Link"), nil
}

// githubRefusal is a page GitHub answered with a status other than 2xx, and the start of what it
// said.
type githubRefusal struct {
	status int
	body   string
}

func (e *githubRefusal) Error() string {
	return fmt.Sprintf("GitHub answered %d %s: %s", e.status, http.StatusText(e.status), e.body)
}

// githubUnanswered is a page GitHub gave no whole answer to.
type githubUnanswered struct{ err error }

func (e *githubUnanswered) Error() string { return "GitHub gave no answer: " + e.err.Error() }
func (e *githubUnanswered) Unwrap() error { return e.err }

// retryableGitHub says whether GitHub refused a read for now: it answered 429 or a 5xx, or gave no
// whole answer, the client's own timeout among them (net/http reports it as a deadline exceeded).
// A read the caller cancelled is no refusal of GitHub's, and is not tried again; a caller's
// deadline that passed ends the wait before the next attempt (retry).
func retryableGitHub(err error) bool {
	var refusal *githubRefusal
	if errors.As(err, &refusal) {
		return refusal.status == http.StatusTooManyRequests || refusal.status >= 500
	}
	var unanswered *githubUnanswered
	return errors.As(err, &unanswered) && !errors.Is(err, context.Canceled)
}
