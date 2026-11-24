package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/githubrest"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// reviewPermissionTTL is how long an account GitHub gave no write access stands so for its
// repository and login before GitHub is asked again. Any account can review a public repository's
// pull request, and every deciding review would otherwise cost a call on the read lease of the
// repository's owner, whose hourly limit every role shares: within the window an account costs one
// call, however many reviews it submits. Its cost is an account just given write access, whose
// review was read without it, deciding nothing until the window passes. Write access is never
// kept: a writer reviews rarely, so keeping it would save almost nothing, and it would let a writer
// whose access was just removed decide, and skip the check that GitHub's answer names the review's
// author.
const reviewPermissionTTL = 5 * time.Minute

// permissionKey is one kept answer's repository and login, both lowercased, since GitHub matches
// either in any case.
type permissionKey struct{ repository, login string }

// reviewerCanWrite answers intake's ConsumerSpec.ReviewPermission: whether review's author is an
// account of type User with write access or higher to its repository, which is what lets a
// person's request for changes refuse a head (workflow's RefusesRound). It reads GitHub's
// collaborator permission for the user (`GET /repos/{owner}/{repo}/collaborators/{username}/permission`),
// which the Metadata read permission reaches, with the daemon's own read token of the repository's
// owner (appauth.Tokens.ReadToken). The answer's `permission` is GitHub's legacy summary of the
// role: `admin` or `write` is write access or higher - a maintainer reads as `write` there - and
// `read`, `none` or anything else is not. The answer's `user.type` must be `User`: a bot is an
// App, whose token an agent can hold, so its review decides nothing whatever access GitHub gives
// it, and an answer that names no type is no person either. An answer for another account than
// the author (GitHub resolves some logins to another user) is not either, and logged.
//
// GitHub is not asked about the one App's own reviews, which decide nothing (workflow's
// RefusesRound), nor about a review on a pull request the daemon does not record on a repository no
// verdict entry names, which the workflow drops; both are answered false. A review of an
// unrecorded pull request on a verdict repository is an outside review, and its author is read
// (reviewedRepository). An account answered no write access stands so for reviewPermissionTTL;
// write access is never kept (reviewPermissionTTL's comment).
//
// An account GitHub does not know on the repository is answered `404`: no write access, logged,
// and the review decides nothing. A `403` that is not GitHub's rate limit is no write access too,
// but it says the App's installation may not read collaborators at all, so no person's review
// decides while it lasts: it is logged at error, naming the installation. So is an App with no
// installation on the repository's owner (appauth.ErrNotInstalled: installed once, then removed,
// while a record still names the repository, which GitHub intake keeps hearing), answered at once,
// since a retry would only mint again on every redelivery. A `401` for its token decides nothing
// and keeps nothing: the token is dropped and the delivery retried with a fresh one
// (tokenRefused). A rate limit is returned as an intake.RetryLater naming the wait GitHub asks
// for, which intake waits out before the review's delivery is tried again, and is held: while it
// stands, every account with no answer kept is answered with the wait left and no call is made,
// since GitHub warns that calling on through a limit can get an integration banned. Any other
// failure - a mint that failed transiently, a network error, a 5xx, the pull request's record
// unread - is returned, and intake retries the delivery after its nak delay rather than applying
// it with a permission nobody read.
func (w *workflowRuntime) reviewerCanWrite(ctx context.Context, review intake.PullRequestReview) (bool, error) {
	if review.Author == w.appLogin {
		return false, nil
	}
	repository, recorded, err := w.reviewedRepository(ctx, review)
	if err != nil {
		return false, err
	}
	if !recorded {
		return false, nil
	}
	key := permissionKey{repository: strings.ToLower(repository.String()), login: strings.ToLower(review.Author)}
	if w.noWriteAccess(key) {
		return false, nil
	}
	if left := w.rateLimitLeft(); left > 0 {
		return false, rateLimitRefusal(left, fmt.Sprintf("read %s's permission on %s", review.Author, repository.String()))
	}
	canWrite, err := w.readPermission(ctx, repository, review.Author)
	if err != nil {
		return false, err
	}
	if !canWrite {
		w.keepNoWriteAccess(key)
	}
	return canWrite, nil
}

// reviewedRepository reads the record of the pull request review is on and parses its repository,
// the one lookup reviewerCanWrite needs before it reads GitHub. The record is read under the name
// the review carries and then, when that finds nothing, under the other name of the pair that
// names it (ghrepo.Under, repositories.renames): a pull request already open when its repository
// was transferred is recorded under the name it moved from, while the review names the one it
// moved to. A review on a pull request neither name finds answers recorded=false, which the caller
// takes as nothing left to do, unless its repository is a verdict repository: there the review is
// an outside review (intake.OutsideReview), which the daemon records and writes legion-verdict
// from, so its author's permission is read.
func (w *workflowRuntime) reviewedRepository(ctx context.Context, review intake.PullRequestReview) (repository ghrepo.Repository, recorded bool, err error) {
	var pr *record.PullRequest
	if err := store.TransactWith(ctx, w.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		pr, err = ghrepo.Under(w.repositories.renames, review.Repo, func(name string) (*record.PullRequest, error) {
			return w.records.PullRequestByNumber(ctx, tx, name, review.Number)
		})
		return err
	}); err != nil {
		return ghrepo.Repository{}, false, fmt.Errorf("read the record of pull request %s#%d: %w", review.Repo, review.Number, err)
	}
	if pr == nil {
		if _, verdicts := w.verdicts.entry(review.Repo); !verdicts {
			return ghrepo.Repository{}, false, nil
		}
	}
	repository, err = ghrepo.Parse("the review's repository", review.Repo)
	if err != nil {
		return ghrepo.Repository{}, false, err
	}
	return repository, true, nil
}

// rateLimitRefusal is the intake.RetryLater a read answers when GitHub's rate limit already
// stands for left, so the read is never made: what names the read reviewerCanWrite's permission
// read says for itself.
func rateLimitRefusal(left time.Duration, what string) *intake.RetryLater {
	return &intake.RetryLater{After: left,
		Err: fmt.Errorf("%s: GitHub's rate limit stands for another %s, so this read was not made", what, left)}
}

// tokenRefused answers a 401 for the read lease on repository's owner. GitHub answers it for a
// token it has not yet accepted, one minted moments before, as for one whose installation was
// removed since, and neither says anything of the author or the review, so the delivery is retried,
// unheld by the rate limit: a token younger than tokenAcceptWindow is kept and tried again once it
// is that old, as Octokit's auth-app does; an older one is dropped (appauth.Forgetter's ForgetRead,
// which drops the owner's read lease and keeps every role's write lease) and the delivery retried
// after tokenRefusedWait with a fresh mint, which either reads it or, for a removed App, is
// refused as not installed (appauth.ErrNotInstalled).
func (w *workflowRuntime) tokenRefused(repository ghrepo.Repository, lease appauth.Lease, err error) error {
	if age := time.Since(lease.Minted); age < tokenAcceptWindow {
		return &intake.RetryLater{After: tokenAcceptWindow - age,
			Err: fmt.Errorf("GitHub has not yet accepted the read token minted %s ago, tried again once it is %s old: %w", age.Round(time.Millisecond), tokenAcceptWindow, err)}
	}
	w.forget.ForgetRead(repository.Owner())
	return &intake.RetryLater{After: tokenRefusedWait,
		Err: fmt.Errorf("GitHub refused the read token, which is minted again: %w", err)}
}

// tokenAcceptWindow is how old an installation token must be before GitHub's refusal of it is
// read as more than its not yet being accepted.
const tokenAcceptWindow = 5 * time.Second

// tokenRefusedWait is how long a delivery whose read token GitHub refused waits before it is tried
// with a fresh one.
const tokenRefusedWait = 5 * time.Second

// permissionClient mints the read lease of the owner GitHub holds repository under now
// (repositories.serving) and returns a githubrest.Client addressing that repository, for
// readPermission. It answers that repository too, even when the mint failed, since it is the one
// the mint was made for and so the one its caller's log lines and refusals name.
//
// The rename rule is needed because the daemon hears both names of a configured pair
// (heardRepositories): a review GitHub published before a transfer and processed after it — the
// daemon down or behind across the transfer, or its delivery redelivered across it — arrives
// under the name the repository moved away from, whose owner's installation no longer covers it.
// A token minted for that owner reaches the repository under neither name, and GitHub answers
// the read 404, which reviewerCanWrite would take as the author having no write access. A
// repository GitHub already serves is its own serving repository, so a review published after the
// transfer is read exactly as it is today.
//
// A mint that fails as GitHub's trouble rather than an answer (appauth.TransientError) is
// returned as an intake.RetryLater naming a minute's wait, since minting on through the App's own
// rate limit risks the same ban GitHub warns a repeated call does, and that wait is held for every
// owner (holdRateLimit). A token_command's grant that waits instead (appauth.Pending: the command
// still running, or the secrets broker's request for this owner waiting on a person) is the same
// one-minute RetryLater with no hold: GitHub refused nothing, and the broker answers each owner's
// grant apart, so a review on another owner is read at once. A mint that fails for any other
// reason is returned as is.
func (w *workflowRuntime) permissionClient(ctx context.Context, repository ghrepo.Repository) (githubrest.Client, appauth.Lease, ghrepo.Repository, error) {
	serving := w.repositories.serving(repository)
	lease, err := w.tokens.ReadToken(ctx, serving.Owner())
	if err != nil {
		if appauth.Pending(err) {
			return githubrest.Client{}, appauth.Lease{}, serving, &intake.RetryLater{After: time.Minute,
				Err: fmt.Errorf("mint the read token for %s: its token_command's grant waits, retry in %s: %w", serving, time.Minute, err)}
		}
		var transient *appauth.TransientError
		if errors.As(err, &transient) {
			w.holdRateLimit(time.Minute)
			return githubrest.Client{}, appauth.Lease{}, serving, &intake.RetryLater{After: time.Minute,
				Err: fmt.Errorf("mint the read token for %s: a transient failure minting it, retry in %s: %w", serving, time.Minute, err)}
		}
		return githubrest.Client{}, appauth.Lease{}, serving, fmt.Errorf("mint the read token for %s: %w", serving, err)
	}
	return githubrest.Client{Token: lease.Token, API: githubrest.RepositoryAPI(w.githubAPI, serving)}, lease, serving, nil
}

// readPermission reads login's permission on repository from GitHub, as reviewerCanWrite says.
func (w *workflowRuntime) readPermission(ctx context.Context, repository ghrepo.Repository, login string) (bool, error) {
	// The mint and every call below are made on the repository GitHub holds this one under now,
	// so repository is that one from here on and each log line and refusal names what was read.
	client, lease, repository, err := w.permissionClient(ctx, repository)
	if errors.Is(err, appauth.ErrNotInstalled) {
		w.log.Error("workflow: the App is not installed on the owner GitHub holds the review's repository under, so the review decides nothing; install the App there for a person's review to decide a round",
			"installation_owner", repository.Owner(), "repo", repository.String(), "login", login, "error", err)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var answer struct {
		Permission string `json:"permission"`
		User       struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	}
	if err := client.Get(ctx, "/collaborators/"+url.PathEscape(login)+"/permission", &answer); err != nil {
		var refused *githubrest.Answer
		if !errors.As(err, &refused) {
			return false, fmt.Errorf("read %s's permission on %s: %w", login, repository.String(), err)
		}
		switch {
		case refused.RateLimited:
			w.holdRateLimit(refused.RetryAfter)
			return false, &intake.RetryLater{After: refused.RetryAfter,
				Err: fmt.Errorf("read %s's permission on %s: GitHub's rate limit, retry in %s: %w", login, repository.String(), refused.RetryAfter, err)}
		case refused.Status == http.StatusNotFound:
			w.log.Info("workflow: GitHub gives the review's author no write access to the repository",
				"repo", repository.String(), "login", login, "status", refused.Status)
			return false, nil
		case refused.Status == http.StatusForbidden:
			w.log.Error("workflow: GitHub refuses the App's read token a review author's repository permission, so the review decides nothing; while the installation may not read the repository's collaborators, no person's review decides a round",
				"bot", lease.Identity.Name, "installation_owner", repository.Owner(), "repo", repository.String(), "login", login, "status", refused.Status, "body", refused.Body)
			return false, nil
		case refused.Status == http.StatusUnauthorized:
			return false, w.tokenRefused(repository, lease, fmt.Errorf("read %s's permission on %s: %w", login, repository.String(), err))
		}
		return false, fmt.Errorf("read %s's permission on %s: %w", login, repository.String(), err)
	}
	if !strings.EqualFold(answer.User.Login, login) {
		w.log.Warn("workflow: GitHub answered the repository permission of another account than the review's author, so the review decides nothing",
			"repo", repository.String(), "login", login, "answered_login", answer.User.Login, "permission", answer.Permission)
		return false, nil
	}
	if answer.Permission != "admin" && answer.Permission != "write" {
		w.log.Info("workflow: GitHub gives the review's author no write access to the repository",
			"repo", repository.String(), "login", login, "permission", answer.Permission)
		return false, nil
	}
	if answer.User.Type != "User" {
		w.log.Info("workflow: the review's author is no account of type User, so the review decides nothing",
			"repo", repository.String(), "login", login, "type", answer.User.Type, "permission", answer.Permission)
		return false, nil
	}
	return true, nil
}

// noWriteAccess says whether key is an account this repository gave no write access within the
// window that still stands.
func (w *workflowRuntime) noWriteAccess(key permissionKey) bool {
	w.permissionsMu.Lock()
	defer w.permissionsMu.Unlock()
	return time.Now().Before(w.permissions[key])
}

// keepNoWriteAccess keeps key as an account with no write access for the permission TTL, and drops
// every answer that no longer stands, so no more is held than the accounts that reviewed within one
// window.
func (w *workflowRuntime) keepNoWriteAccess(key permissionKey) {
	ttl := w.permissionTTL
	if ttl == 0 {
		ttl = reviewPermissionTTL
	}
	now := time.Now()
	w.permissionsMu.Lock()
	defer w.permissionsMu.Unlock()
	if w.permissions == nil {
		w.permissions = map[permissionKey]time.Time{}
	}
	for cached, until := range w.permissions {
		if !now.Before(until) {
			delete(w.permissions, cached)
		}
	}
	w.permissions[key] = now.Add(ttl)
}

// rateLimitLeft is how long GitHub's last rate limit still asks to be left alone, zero when none
// stands. GitHub's documentation warns that calling on while a limit stands can get an integration
// banned, so no read is made within it.
func (w *workflowRuntime) rateLimitLeft() time.Duration {
	w.permissionsMu.Lock()
	defer w.permissionsMu.Unlock()
	return time.Until(w.limitedUntil)
}

// holdRateLimit records that GitHub's rate limit stands for another after, keeping the later of
// this limit and one already held.
func (w *workflowRuntime) holdRateLimit(after time.Duration) {
	until := time.Now().Add(after)
	w.permissionsMu.Lock()
	defer w.permissionsMu.Unlock()
	if until.After(w.limitedUntil) {
		w.limitedUntil = until
	}
}
