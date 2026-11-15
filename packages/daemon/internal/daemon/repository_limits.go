package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
// (defaultBranchLimit): GitHub's answer, the read's error, which wraps it, how long GitHub asks the
// reader to wait, and until, when that wait ends, counted from when the answer came, so a caller
// that does other work first, such as reading the records, waits no longer for it.
type limitedRead struct {
	mapped mappedRepository
	answer *githubrest.Answer
	err    error
	wait   time.Duration
	until  time.Time
}

// primaryLimitMessage is what GitHub's primary rate limit says in the body of its 403: "API rate
// limit exceeded for installation ID <id>", or for a user or an address.
const primaryLimitMessage = "api rate limit exceeded"

// unnamedLimitWait is how long a limit named by its body alone is waited: githubrest's wait for a
// rate limit whose answer names no wait (its retryAfter), the minute GitHub's REST documentation
// gives as the least to wait.
const unnamedLimitWait = time.Minute

// defaultBranchLimit is GitHub's answer in err, a read of a default branch that failed, when that
// answer is GitHub's rate limit, with how long GitHub asks the reader to wait: an answer githubrest
// reads as the limit (Answer.RateLimited: a 429, or a 403 its headers or the secondary limit's
// message name a limit on), waited its RetryAfter, or a 403 whose body names the primary limit
// (primaryLimitMessage) while its headers do not, waited unnamedLimitWait. GitHub has refused an
// installation's reads of a repository with that 403 while the same installation's other requests
// were answered with thousands of calls left in x-ratelimit-remaining. The body is read here, for
// the reads of a default branch alone, and not in githubrest, whose other callers each decide
// what such an answer means for their own reads.
func defaultBranchLimit(err error) (*githubrest.Answer, time.Duration, bool) {
	var answer *githubrest.Answer
	switch {
	case !errors.As(err, &answer):
		return nil, 0, false
	case answer.RateLimited:
		return answer, answer.RetryAfter, true
	case answer.Status == http.StatusForbidden && strings.Contains(strings.ToLower(answer.Body), primaryLimitMessage):
		return answer, unnamedLimitWait, true
	}
	return nil, 0, false
}

// answeredRefusal is whether err, a read of a default branch that failed, is GitHub's answer about
// the repository, which no later read is expected to change: a 404, or any other answer below 500
// that is no rate limit (defaultBranchLimit). Every other failure is a read GitHub did not answer:
// its rate limit, a 5xx, a failed mint of the token, a transport failure or a read cut off by its
// bound.
func answeredRefusal(err error) bool {
	var answer *githubrest.Answer
	if !errors.As(err, &answer) || answer.Status >= 500 {
		return false
	}
	_, _, limited := defaultBranchLimit(err)
	return !limited
}

// bootReadTransient is a failure of the boot's read of a repository that GitHub reports as its own
// trouble, which the boot retries on appMintRetry: the App token's mint failing so
// (appTransient), or a 5xx.
func bootReadTransient(err error) bool {
	var answer *githubrest.Answer
	return appTransient(err) || errors.As(err, &answer) && answer.Status >= 500
}

// heldThroughLimit is told of each repository served on a default branch the daemon holds in place
// of a read GitHub refused for its rate limit (holdLimited, and defaultBranches.Named for any read
// of a repository read or held before): it says so, naming the repository, the branch, the limit as
// GitHub's headers gave it and when the hold ends, and asks for a read of the mapping then
// (requestMappingRead), which reads the repository from GitHub again. A request made before the
// refresher runs, during the boot, waits for it.
func (w *workflowRuntime) heldThroughLimit(read limitedRead, held ghbranch.Repository) {
	time.AfterFunc(time.Until(read.until), func() { w.requestMappingRead() })
	w.log.Warn("workflow: GitHub's rate limit refused the read of a repository's default branch, so the daemon serves it on the default branch it holds and reads it again after the limit's reset",
		append([]any{"repo", read.mapped.Repo.String(), "defaultBranch", held.DefaultBranch, "readAgainAt", read.until.UTC().Format(time.RFC3339)}, limitAttributes(read)...)...)
}

// holdLimited serves each of limited that a record of the project's issues holds a default branch
// for on that branch, at once: GitHub's rate limit refuses a read for a while and says nothing
// about the repository, and the branch a record holds is the one GitHub named when the issue was
// recorded, which a read after the limit corrects if GitHub has since renamed it. limited is only
// ever repositories the daemon held no read of, since Named answers a limited read of one it holds
// from what it holds; the records are read once, and only when a read was refused, and matched to
// the repositories this read of the mapping serves (serves: every one it will publish, limited
// ones among them), which is not yet the mapping published (recordedBranches). Each held one
// stands until the limit's reset (defaultBranches.hold), so nothing reads it inside the limit, and
// every later read GitHub's limit refuses is answered from it too, the boot's own included
// (defaultBranches.Named); heldThroughLimit says so and has the mapping read again then.
//
// A record gives the branch, and the spelling only of a repository it names itself: the record's
// is GitHub's own, as it named the repository when the issue was recorded. A record that names the
// repository only under the other name of a configured rename gives no spelling, since GitHub's
// spelling of the name the repository moved to is neither that record's nor the pair's as
// configured; the repository is served as this read accepted it, and intake hears it so
// (heardRepositories), as it hears it once GitHub answers.
//
// held is each so served; unheld is each no record holds a branch for, for the caller to wait out
// (the boot's waitOutLimits) or to leave out for now (refreshMapping).
func (w *workflowRuntime) holdLimited(ctx context.Context, limited []limitedRead, serves []mappedRepository) (held []mappedRepository, unheld []limitedRead, err error) {
	if len(limited) == 0 {
		return nil, nil, nil
	}
	recorded, err := w.recordedBranches(ctx, serves)
	if err != nil {
		return nil, nil, err
	}
	for _, read := range limited {
		branch, ok := recorded[strings.ToLower(read.mapped.Repo.String())]
		if !ok {
			unheld = append(unheld, read)
			continue
		}
		if !strings.EqualFold(branch.Name.String(), read.mapped.Repo.String()) {
			branch.Name = read.mapped.Repo
		}
		w.defaultBranches.hold(read.mapped.Repo, branch, read.until)
		w.heldThroughLimit(read, branch)
		held = append(held, mappedRepository{Repo: branch.Name, Default: read.mapped.Default})
	}
	return held, unheld, nil
}

// recordedBranches is the default branch the project's records hold for each repository a record
// names (record.Issue.GitHubRepo, in GitHub's spelling, and DefaultBranch), keyed by the repository
// GitHub holds it under now, lowercased, read in one read-only transaction bounded by
// appMintAttempt. A record keeps the name it was recorded under for its whole life, so one recorded
// before a configured rename's transfer names the repository as it was: the rename's serving rule
// (ghrepo.Renames.Serving, the rule repositories.serving reads) maps it to the name it moved to
// when serves, the repositories the caller's read of the mapping serves, holds that name and not
// the old one, and leaves it under its own name when serves holds both, which are then two
// repositories. serves is the read's own set rather than the mapping as last published, since a
// read of the mapping can be the one that finds the transfer. A repository several records name
// takes the first record in the store's order: each holds the default branch GitHub named when its
// issue was recorded, and the one taken is served only until GitHub answers a read of it again. A
// record from before the record carried its repository holds nothing.
func (w *workflowRuntime) recordedBranches(ctx context.Context, serves []mappedRepository) (map[string]ghbranch.Repository, error) {
	served := make(map[string]bool, len(serves))
	for _, mapped := range serves {
		served[strings.ToLower(mapped.Repo.String())] = true
	}
	isServed := func(repo ghrepo.Repository) bool { return served[strings.ToLower(repo.String())] }
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
		if issue.GitHubRepo == "" || issue.DefaultBranch == "" {
			continue
		}
		name, err := ghrepo.Parse("GitHub's spelling of the repository of "+issue.Key, issue.GitHubRepo)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(w.repositories.renames.Serving(name, isServed).String())
		if _, taken := recorded[key]; !taken {
			recorded[key] = ghbranch.Repository{Name: name, DefaultBranch: issue.DefaultBranch}
		}
	}
	return recorded, nil
}

// waitOutLimits is the boot's answer for each of unheld, a repository whose default-branch read
// GitHub's rate limit refused and that the daemon holds no default branch for (holdLimited): it
// waits as long as GitHub asks (githubrest's retryAfter: the Retry-After, else until the reset when
// no call is left, at most an hour, else a minute; a minute too for a limit named by its body
// alone), logging one line that names the repository, the limit as GitHub's answer gave it and the
// time it waits until, and then reads it once more. A repository that read answers is served as
// GitHub names it (named). One GitHub answers about, such as with a 404, refuses the boot, as it
// does on the first read (answeredRefusal). One it does not answer — the limit again, a 5xx, a
// failed mint of the token, a transport failure, or no answer within appMintAttempt — is left out
// for now (unread), as a repository whose installation read failed is, and read again on
// installReadRetry until GitHub answers, so the limit costs that repository and not the project's
// boot. The waits overlap, each for its own limit, so the boot waits as long as the longest of them
// asks, and the reads after them run mappingReads at a time, as the first reads did.
func (w *workflowRuntime) waitOutLimits(ctx context.Context, unheld []limitedRead) (named []mappedRepository, unread []refusedRepository, _ error) {
	if len(unheld) == 0 {
		return nil, nil, nil
	}
	// Each read writes its own index of answered or failed, never both, and only it does.
	answered := make([]mappedRepository, len(unheld))
	failed := make([]error, len(unheld))
	reading := make(chan struct{}, mappingReads)
	group, waiting := errgroup.WithContext(ctx)
	for index, read := range unheld {
		group.Go(func() error {
			w.log.Warn("workflow: GitHub's rate limit refused the read of a repository's default branch and the daemon holds none for it, so the boot waits as long as GitHub asks and reads it once more",
				append([]any{"repo", read.mapped.Repo.String(), "waitUntil", read.until.UTC().Format(time.RFC3339)}, limitAttributes(read)...)...)
			select {
			case <-waiting.Done():
				return waiting.Err()
			case <-time.After(time.Until(read.until)):
			}
			select {
			case <-waiting.Done():
				return waiting.Err()
			case reading <- struct{}{}:
			}
			attempt, cancel := context.WithTimeout(waiting, appMintAttempt)
			repository, err := w.defaultBranches.Named(attempt, read.mapped.Repo)
			cancel()
			<-reading
			switch {
			case err == nil:
				answered[index] = mappedRepository{Repo: repository.Name, Default: read.mapped.Default}
			case waiting.Err() != nil:
				return waiting.Err()
			case answeredRefusal(err):
				return err
			default:
				failed[index] = err
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

// limitAttributes is what a log line says of read's rate limit: how long GitHub asked the reader to
// wait, the budget githubrest read the limit as spending ("" for a limit named by its body alone),
// the answer's rate-limit headers as GitHub sent them ("" for each it did not send), so the line
// names the limit that refused the read in GitHub's own words, and the read's error, whose body
// names the installation the limit is on.
func limitAttributes(read limitedRead) []any {
	limit := read.answer.Limit
	return []any{"wait", read.wait.String(), "budget", string(read.answer.Budget), "rateLimit", limit.Limit, "rateLimitRemaining", limit.Remaining,
		"rateLimitReset", limit.Reset, "rateLimitResource", limit.Resource, "retryAfter", limit.RetryAfter, "error", read.err}
}
