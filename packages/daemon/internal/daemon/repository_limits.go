package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/sjawhar/legion/daemon/internal/ghbranch"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/githubrest"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// limitedRead is a mapped repository whose default-branch read GitHub refused for its rate limit
// (nameAccepted): GitHub's answer, which names how long it asks the caller to wait, and the
// read's error, which wraps it.
type limitedRead struct {
	mapped mappedRepository
	answer *githubrest.Answer
	err    error
}

// rateLimited is GitHub's answer in err when it is GitHub's rate limit (githubrest.Answer's
// RateLimited: a 429, or a 403 its headers or its body name a limit on).
func rateLimited(err error) (*githubrest.Answer, bool) {
	var answer *githubrest.Answer
	if errors.As(err, &answer) && answer.RateLimited {
		return answer, true
	}
	return nil, false
}

// bootReadTransient is a failure of the boot's read of a repository that GitHub reports as its own
// trouble, which the boot retries on appMintRetry: the App token's mint failing so
// (appTransient), or a 5xx.
func bootReadTransient(err error) bool {
	var answer *githubrest.Answer
	return appTransient(err) || errors.As(err, &answer) && answer.Status >= 500
}

// holdLimited serves each of limited the daemon holds a default branch for on that branch, at
// once: GitHub's rate limit refuses a read for a while and says nothing about the repository, and
// the branch held is the one the daemon last read or recorded an issue with, which a read after
// the limit corrects if GitHub has since renamed it. The held branch is the last read of the
// repository (defaultBranches.Kept) or, for one this process never read, the one a record of the
// project's issues on it holds (recordedBranches), read only when a read was refused and once
// for all of limited. Each held one stands until the limit's reset (defaultBranches.hold), so
// nothing reads it inside the limit, and a read of the mapping is asked for then
// (requestMappingRead), which reads it from GitHub again. held is each so served, as GitHub last
// named it; unheld is each the daemon holds nothing for, for the caller to wait out (the boot's
// waitOutLimits) or to leave out for now (refreshMapping).
func (w *workflowRuntime) holdLimited(ctx context.Context, limited []limitedRead) (held []mappedRepository, unheld []limitedRead, err error) {
	var recorded map[string]ghbranch.Repository
	for _, read := range limited {
		name := read.mapped.Repo.String()
		branch, ok := w.defaultBranches.Kept(name)
		if !ok {
			if recorded == nil {
				if recorded, err = w.recordedBranches(ctx); err != nil {
					return nil, nil, err
				}
			}
			branch, ok = recorded[strings.ToLower(name)]
		}
		if !ok {
			unheld = append(unheld, read)
			continue
		}
		until := w.defaultBranches.hold(read.mapped.Repo, branch, read.answer.RetryAfter)
		time.AfterFunc(read.answer.RetryAfter, func() { w.requestMappingRead() })
		w.log.Warn("workflow: GitHub's rate limit refused the read of a repository's default branch, so the daemon serves it on the default branch it holds and reads it again after the limit's reset",
			append([]any{"repo", name, "defaultBranch", branch.DefaultBranch, "readAgainAt", until.UTC().Format(time.RFC3339)}, limitAttributes(read)...)...)
		held = append(held, mappedRepository{Repo: branch.Name, Default: read.mapped.Default})
	}
	return held, unheld, nil
}

// recordedBranches is the repository and the default branch the project's records hold for each
// repository a record names (record.Issue.GitHubRepo and DefaultBranch), keyed by GitHub's
// spelling lowercased, read in one read-only transaction bounded by appMintAttempt. A record keeps
// the name it was recorded under for its whole life, so one recorded before a configured rename's
// transfer names the repository as it was and is no answer for the name it moved to. A repository
// several records name takes the first record in the store's order: each holds the default branch
// GitHub named when its issue was recorded, and the one taken is served only until GitHub answers a
// read of it again. A record from before the record carried its repository holds nothing.
func (w *workflowRuntime) recordedBranches(ctx context.Context) (map[string]ghbranch.Repository, error) {
	read, cancel := context.WithTimeout(ctx, appMintAttempt)
	defer cancel()
	var issues []record.Issue
	if err := pgx.BeginTxFunc(read, w.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		issues, err = w.records.Issues(read, tx)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read the default branches the project's records hold: %w", err)
	}
	recorded := map[string]ghbranch.Repository{}
	for _, issue := range issues {
		key := strings.ToLower(issue.GitHubRepo)
		if _, taken := recorded[key]; taken || issue.GitHubRepo == "" || issue.DefaultBranch == "" {
			continue
		}
		name, err := ghrepo.Parse("GitHub's spelling of the repository of "+issue.Key, issue.GitHubRepo)
		if err != nil {
			return nil, err
		}
		recorded[key] = ghbranch.Repository{Name: name, DefaultBranch: issue.DefaultBranch}
	}
	return recorded, nil
}

// waitOutLimits is the boot's answer for each of unheld, a repository whose default-branch read
// GitHub's rate limit refused and that the daemon holds no default branch for (holdLimited): it
// waits as long as GitHub asks (githubrest's retryAfter: the Retry-After, else until the reset when
// no call is left, at most an hour, else a minute), logging one line that names the repository,
// the limit as GitHub's answer gave it and the time it waits until, and then reads it once more. A
// repository that read answers is served as GitHub names it (named). One it does not answer — the
// limit again, the trouble the boot's own read retries (bootReadTransient), or no answer within
// appMintAttempt — is left out for now (unread), as a repository whose installation read failed
// is, and read again on installReadRetry until GitHub answers, so the limit costs that repository
// and not the project's boot. Any other answer, such as a 404, refuses the boot, as it does on the
// first read. The repositories wait side by side, each for its own limit, so the boot waits as long
// as the longest of them asks.
func (w *workflowRuntime) waitOutLimits(ctx context.Context, unheld []limitedRead) (named []mappedRepository, unread []refusedRepository, _ error) {
	if len(unheld) == 0 {
		return nil, nil, nil
	}
	// Each read writes its own index of answered or failed, never both, and only it does.
	answered := make([]mappedRepository, len(unheld))
	failed := make([]error, len(unheld))
	group, waiting := errgroup.WithContext(ctx)
	for index, read := range unheld {
		group.Go(func() error {
			name := read.mapped.Repo.String()
			wait := read.answer.RetryAfter
			w.log.Warn("workflow: GitHub's rate limit refused the read of a repository's default branch and the daemon holds none for it, so the boot waits as long as GitHub asks and reads it once more",
				append([]any{"repo", name, "waitUntil", time.Now().Add(wait).UTC().Format(time.RFC3339), "wait", wait.String()}, limitAttributes(read)...)...)
			select {
			case <-waiting.Done():
				return waiting.Err()
			case <-time.After(wait):
			}
			attempt, cancel := context.WithTimeout(waiting, appMintAttempt)
			repository, err := w.defaultBranches.Named(attempt, read.mapped.Repo)
			cancel()
			_, limited := rateLimited(err)
			switch {
			case err == nil:
				answered[index] = mappedRepository{Repo: repository.Name, Default: read.mapped.Default}
			case waiting.Err() != nil:
				return waiting.Err()
			case limited || bootReadTransient(err) || errors.Is(err, context.DeadlineExceeded):
				failed[index] = err
			default:
				return err
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, nil, err
	}
	for index, read := range unheld {
		if failed[index] == nil {
			named = append(named, answered[index])
			continue
		}
		w.log.Warn("workflow: GitHub did not answer the read of a repository's default branch after its rate limit, so the daemon boots leaving it out and reads it again",
			"repo", read.mapped.Repo.String(), "retryIn", installReadRetry.Initial.String(), "error", failed[index])
		unread = append(unread, unreadDefaultBranch(read.mapped, failed[index]))
	}
	return named, unread, nil
}

// unreadDefaultBranch is mapped left out for now because GitHub did not answer the read of its
// default branch with err: a refusal carrying err as its readErr, which is what the refresher reads
// the mapping again for (awaitingRead, installReadRetry), and admission reads as a repository
// GitHub is being read again for (lookup's unread).
func unreadDefaultBranch(mapped mappedRepository, err error) refusedRepository {
	return refusedRepository{Repo: mapped.Repo.String(), Reason: fmt.Sprintf("GitHub did not answer the read of its default branch: %v; Legion reads it again", err),
		readErr: err, mappedDefault: mapped.Default}
}

// limitAttributes is what a log line says of read's rate limit: the budget the client read it as
// spending, the answer's rate-limit headers as GitHub sent them ("" for each it did not send), so
// the line names the limit that refused the read in GitHub's own words, and the read's error,
// whose body names the installation the limit is on.
func limitAttributes(read limitedRead) []any {
	limit := read.answer.Limit
	return []any{"budget", string(read.answer.Budget), "rateLimit", limit.Limit, "rateLimitRemaining", limit.Remaining,
		"rateLimitReset", limit.Reset, "rateLimitResource", limit.Resource, "retryAfter", limit.RetryAfter, "error", read.err}
}
