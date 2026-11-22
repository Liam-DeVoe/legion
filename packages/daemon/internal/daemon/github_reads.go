package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/githubrest"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// readEventID is the event id, under source "github", of a fact a read of GitHub builds about
// pull request number of repo: kind, the Dispatch project, the pull request, and observed, what
// the read saw, such as the two heads of a compare. It holds no read time, no boot and nothing of
// who made the read, so one observation is one fact however many reads, boots, daemons or outbox
// rows see it. The per-issue read and the outbox row that make the same read of GitHub share this
// id, which is what keeps them from applying one observation twice.
func readEventID(kind, project, repo string, number int, observed ...string) string {
	return fmt.Sprintf("%s:%s:%s#%d:%s", kind, project, repo, number, strings.Join(observed, ":"))
}

// githubPullRequest is one pull request as GitHub answers GET /pulls/{number} and lists it in
// GET /pulls: its number, state (open or closed), page, body, base branch, head branch and commit,
// when it last changed, its nullable, lazily computed `mergeable`, which the list leaves out, and
// `mergeable_state`, GitHub's own merge state for the head, which the list leaves out too. Merged
// and MergeCommitSHA are whether GitHub merged it and the commit the merge made, which the
// merge-queue read applies as the merge (outbox's mergeQueueRead): GitHub answers a merged pull
// request `closed`, so State alone cannot tell a merge from a close. The list leaves Merged out,
// and only the read of one pull request names it.
type githubPullRequest struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	Base    struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	UpdatedAt      time.Time `json:"updated_at"`
	Mergeable      *bool     `json:"mergeable"`
	MergeableState string    `json:"mergeable_state"`
	Merged         bool      `json:"merged"`
	MergeCommitSHA string    `json:"merge_commit_sha"`
}

// mergeability is the pull request's `mergeable` as the mergeability fact reads it: null
// is record.MergeabilityUnknown, GitHub not having computed it yet; true is
// record.MergeabilityMergeable; false is record.MergeabilityConflicting, a head that conflicts
// with its base, which a second read confirms before the workflow decides whether to merge the
// base forward (workflow's mergeability), and which the read carries the base's rules for
// (applyMergeability).
func (p githubPullRequest) mergeability() record.Mergeability {
	switch {
	case p.Mergeable == nil:
		return record.MergeabilityUnknown
	case *p.Mergeable:
		return record.MergeabilityMergeable
	default:
		return record.MergeabilityConflicting
	}
}

// mergeState is the pull request's `mergeable_state` as the record keeps it: GitHub's own merge
// state for the head, in GitHub's spelling, which is what an approval standing on that head
// waits for (workflow's reviewRound).
// An answer with no `mergeable_state` at all - a list entry, which leaves it out - is
// record.MergeStateUnread, since the read found none, and a spelling outside the eight GitHub
// documents is record.MergeStateUnknown, the one state that already means "GitHub has not said
// this head can be merged": the record's values are then exactly the set the state view and the
// contract name, and an unrecognised state is never read as one the issue may advance on
// (record.MergeState.Mergeable).
func (p githubPullRequest) mergeState() record.MergeState {
	switch state := record.MergeState(p.MergeableState); state {
	case record.MergeStateUnread, record.MergeStateClean, record.MergeStateUnstable, record.MergeStateHasHooks,
		record.MergeStateBlocked, record.MergeStateDirty, record.MergeStateBehind, record.MergeStateDraft:
		return state
	default:
		return record.MergeStateUnknown
	}
}

// readPullRequest reads pr from one GET /pulls/{number}: the base the mergeability is recorded
// against, that mergeability itself, and the head and its clock followHead compares with the
// record, in the single read they share. It also returns the githubrest.Client its own token mint
// built, so the per-issue read's further reads of the same pull request reuse it rather than
// minting a second token.
//
// The read is made of the repository GitHub serves the recorded one under (repositories.serving),
// as every GitHub call the daemon makes for a recorded tree is: a record keeps the name it was
// written under until this release's one-time rename pass writes a pair's `to` over it
// (renamePass), and after a transfer GitHub holds the repository under the other name, on the
// other owner's installation. Read under the recorded name, this would mint on the installation
// the repository has left and address a repository GitHub no longer answers for.
func (w *workflowRuntime) readPullRequest(ctx context.Context, pr record.PullRequest) (githubrest.Client, githubPullRequest, error) {
	recorded, err := ghrepo.Parse("the pull request's repository", pr.Repo)
	if err != nil {
		return githubrest.Client{}, githubPullRequest{}, err
	}
	repository := w.repositories.serving(recorded)
	github, err := readClient(ctx, w.tokens, w.githubAPI, repository)
	if err != nil {
		return githubrest.Client{}, githubPullRequest{}, fmt.Errorf("mint the read token for %s: %w", repository.Owner(), err)
	}
	var pull githubPullRequest
	if err := github.Get(ctx, fmt.Sprintf("/pulls/%d", pr.Number), &pull); err != nil {
		return githubrest.Client{}, githubPullRequest{}, err
	}
	return github, pull, nil
}

// readClient is the client for repository's REST API on the App's read lease for that
// repository's owner account: the token every read of an issue's own repository is made with, the
// per-issue reads' and the outbox rows' alike. The token names no repository, so one mint serves
// every repository of the owner the App is installed on. The mint failure is returned bare, so
// each caller says in its own words what it was minting the token for.
func readClient(ctx context.Context, tokens appauth.Tokens, githubAPI string, repository ghrepo.Repository) (githubrest.Client, error) {
	lease, err := tokens.ReadToken(ctx, repository.Owner())
	if err != nil {
		return githubrest.Client{}, err
	}
	return githubrest.Client{Token: lease.Token, API: githubrest.RepositoryAPI(githubAPI, repository)}, nil
}
