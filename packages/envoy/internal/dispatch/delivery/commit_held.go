package delivery

import (
	"context"

	"github.com/sjawhar/envoy/internal/dispatch/githubapp"
)

// commit_held.go holds the two reads the result handler makes before it stores a check result
// (api/results.go, refuseUnknownCommit): whether a repository holds a commit at all, and whether
// a pull request an issue links is open or merged, which says whether that pull request's
// repository is one a result of the issue may name a commit of. Neither is an answer about a
// branch, which branch.go owns alone, and neither keeps anything: GitHub is asked each time.

// RepositoryHoldsCommit answers whether repoFull holds sha, through the App: it mints the
// repository's installation token, through the repositoryRead prologue askGitHubCommitOnBranch
// and repositoryDefaultBranch share, and makes githubapp.Client.HasCommit's read.
//
// A false with no error is the one 422 whose message begins `No commit found for SHA`, the
// only answer that proves the repository does not hold the commit (githubapp.Client.HasCommit
// and its noCommitForSHA: GitHub answers the same status for a validation failure and for an
// endpoint it has throttled, so the message is what tells them apart). Every other failure is
// the error, which says only that Dispatch cannot tell: any other 422, a malformed repository
// name, a deployment with no App key, a repository the App cannot read, a rate limit, a 5xx, a
// transport failure.
func RepositoryHoldsCommit(ctx context.Context, client *githubapp.Client, repoFull, sha string) (bool, error) {
	owner, repo, token, err := repositoryRead(ctx, client, repoFull)
	if err != nil {
		return false, err
	}
	return client.HasCommit(ctx, token, owner, repo, sha)
}

// PullRequestOpenOrMerged answers whether pull request number of repoFull is open or merged,
// through the App: the same repositoryRead prologue, then fetchPullRequestPayload's
// `GET /repos/{owner}/{repo}/pulls/{number}`. The state is the one a head row stores
// (PullRequestHeadState), so a pull request this answers no for is one the head rows call
// closed: it closed and did not merge.
//
// Every failure is the error, which says only that Dispatch cannot tell: a malformed repository
// name, a deployment with no App key, a repository the App cannot read, a pull request GitHub
// answers 404 or 410 for (ErrPullRequestNotFound), a rate limit, a 5xx, a transport failure.
func PullRequestOpenOrMerged(ctx context.Context, client *githubapp.Client, repoFull string, number int) (bool, error) {
	owner, repo, token, err := repositoryRead(ctx, client, repoFull)
	if err != nil {
		return false, err
	}
	payload, err := fetchPullRequestPayload(ctx, client, token, owner, repo, number)
	if err != nil {
		return false, err
	}
	return PullRequestHeadState(payload.State, payload.Merged || payload.MergedAt != nil) != "closed", nil
}
