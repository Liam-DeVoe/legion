package refs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// The canonical name of a repository is what its repo_projects row's renamed_to names, else the
// name itself, compared case-insensitively. A repository Dispatch holds no mapping for, and one
// whose mapping names no rename, is its own canonical name, so a repository Dispatch does not
// map is unaffected by this rule: it reads exactly as it did before the column existed.
//
// This is the server's one repository-renaming rule, and it is written here once because six
// sites must come to the same answer or the rename is only half known: delivery's attributionHeld
// puts both the issue's resolved repository and the pull request's own through it, the results
// route canonicalises the repository of a check's issue before it asks GitHub anything, in the
// one helper its results read and its record route share (api/issue_repo.go,
// server.checkIssueRepository), the three writers of a pull_request_heads row key the row by it
// (delivery/pull_request_heads.go: RefreshPullRequestHead, ReadThroughCommitPulls and
// Intake.recordPullRequestHead), the settings route reads it to decide whether a rename's
// target is itself renamed (api/repo_projects.go, refuseRepoRenameChain), and the CI reconcile
// reads every repository it covers under the name it has now, so no pass asks GitHub about a
// name it cannot read (cireconcile/repositories.go, Reconciler.covered).
//
// One step is enough: the settings route refuses a pair pointing at a repository that is itself
// renamed, and refuses renaming a repository another row already points at, so no chain and no
// cycle exists to walk (api/repo_projects.go, putRepoProject; migration
// 20261105075455_repo_projects_renamed_to). Dispatch never asks GitHub to resolve a name — for a
// repository renamed away, GitHub answers 404 for its installation, so the old name cannot be
// read at all and the pair is declared, never discovered.

// CanonicalRepoSQL is that rule as a SQL expression over repo, itself an expression naming a
// repository: null where repo is null, so a comparison against it stays null rather than
// becoming true. It reads repo_projects by a lowercased name on both sides, as every comparison
// of a repository here does, because GitHub spells an owner and a repository as their owners do
// (`Acme/Widgets`) while Dispatch stores a mapping's path canonicalised (api's canonicalRepo).
// The expression text is spliced twice, so repo must be a column, a bound parameter or another
// side-effect-free expression.
//
// The inner read is an aggregate, so it answers exactly one row however many rows match: the
// primary key is the repository's own spelling, so two mappings whose names differ only by case
// can exist — direct SQL, or a hand repair, which migration 20261105075455_repo_projects_renamed_to
// expects — and a plain scalar subquery over both would fail every statement this expression is
// spliced into with `more than one row returned by a subquery`. It reads only the mappings that
// name a rename, so a repository mapped with no rename coalesces to its own name.
//
// That read goes through a derived table that renames both columns it keeps, because repo is
// the caller's own expression spliced into this one and SQL resolves an unqualified name
// against the innermost query's columns first. Reading repo_projects directly here would put a
// column named `repo` in scope, so a caller splicing a bare `repo` would compare that column
// with itself — true for every row, which answers the whole table's greatest renamed_to for
// every row of every caller rather than each row's own canonical name. No caller's column is
// named rename_source or rename_target, so a caller's bare `repo` reaches the caller's own
// table, and a caller whose statement has no such column fails loudly with `column "repo" does
// not exist` rather than silently answering some other repository's name.
func CanonicalRepoSQL(repo string) string {
	return `coalesce((select max(rename.rename_target) from (
			select renamed.repo as rename_source, renamed.renamed_to as rename_target
			from repo_projects renamed where renamed.renamed_to is not null
		) rename where lower(rename.rename_source) = lower(` + repo + `)), ` + repo + `)`
}

// CanonicalRepo is that rule read through q for one full `owner/name`: the expression above,
// selected for repo, so the Go caller and every statement that splices the expression read one
// rule and cannot come to different answers. The caller's own spelling comes back unchanged
// where there is no rename, so canonicalising a name never re-cases a stored row's key; where
// there is one, the answer is the pair as the settings route canonicalised it.
func CanonicalRepo(ctx context.Context, q Queryer, repo string) (string, error) {
	if repo == "" {
		return repo, nil
	}
	var canonical string
	if err := q.QueryRow(ctx, `select `+CanonicalRepoSQL("$1::text"), repo).Scan(&canonical); err != nil {
		return "", fmt.Errorf("read the canonical name of %s: %w", repo, err)
	}
	return canonical, nil
}

// The two halves of a repository GitHub could hold, copied from the daemon's own reading of the
// same grammar (packages/daemon/internal/ghrepo's ownerName and repositoryName): packages/envoy
// cannot import packages/daemon, and the two gates of one product must hold a repository to one
// rule, so the rule is written out here with its source named, once for every package of the
// server that reads a repository name: the API routes that take one (api's parseSourceRepo and
// githubCouldHoldRepo), and the three that put a stored name's halves into GitHub paths
// (delivery's and cireconcile's splitRepo, and the architecture importer's resolve). An owner is
// a letter or digit and then up to 38 letters, digits, hyphens and underscores, which is looser
// than GitHub's sign-up form on purpose: that form's rule, single hyphens with none leading or
// trailing, binds new accounts alone, and GitHub still serves a login with a doubled hyphen
// (`hello--world`, the organisation `foo--bar`), one ending in a hyphen (`ArtOfCode-`) and a
// managed user's `_<shortcode>` (`mona-cat_octo`), whose repositories an issue has to be able to
// name. A repository's own name is 1 to 100 letters, digits, `.`, `-` and `_`, the most GitHub
// allows. Both cases are written out because the grammar is the grammar whatever the case, so a
// name GitHub holds is taken however it was typed. The two names `.` and `..`, which no working
// copy could hold and which a GitHub path reads as a dot segment, are refused beside the
// patterns, where a pattern would state them worse.
var (
	repoOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,38}$`)
	repoNamePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// GitHubOwner answers whether owner is a login GitHub serves, by the grammar above.
func GitHubOwner(owner string) bool {
	return repoOwnerPattern.MatchString(owner)
}

// GitHubName answers whether name is a repository name GitHub allows, by the grammar above:
// 1 to 100 letters, digits, `.`, `-` and `_`, and neither `.` nor `..`.
func GitHubName(name string) bool {
	return name != "." && name != ".." && repoNamePattern.MatchString(name)
}

// ErrNotGitHubRepo is SplitGitHubRepo's refusal: a name no repository GitHub could hold is
// called, which no read of GitHub, made now or later, can find.
var ErrNotGitHubRepo = errors.New("not an owner/name GitHub allows")

// SplitGitHubRepo splits full, an `owner/name`, into the two halves a GitHub path takes, each
// held to the names GitHub allows (GitHubOwner, GitHubName), and refuses a name that is empty,
// `.` or `..` once a trailing `.git` is trimmed too, since every canonical name Dispatch stores
// has that suffix trimmed (api's canonicalRepo): `acme/...git` is `acme/..` stored, and
// `acme/.git` is `acme/`. The halves come back as full spells them, so a caller keeps its own
// case and its own suffix; the refusal is ErrNotGitHubRepo, naming full.
//
// It is the one rule every end reads: the API routes that take a repository from a person put the
// canonical form they store to it and refuse a name it refuses (api's parseSourceRepo and
// githubCouldHoldRepo), and each place that turns a stored name into a GitHub path refuses one
// there (delivery's and cireconcile's splitRepo, and the architecture importer's resolve), so a
// name stored before the routes held it to the rule, or by direct SQL, never reaches GitHub
// either.
func SplitGitHubRepo(full string) (owner, name string, err error) {
	owner, name, found := strings.Cut(full, "/")
	trimmed := strings.TrimSuffix(strings.ToLower(name), ".git")
	if !found || !GitHubOwner(owner) || !GitHubName(name) || !GitHubName(trimmed) {
		return "", "", fmt.Errorf("repository %q is %w: an owner of letters, digits, - and _, and a name of at most 100 letters, digits, ., - and _ that is not empty, . or .., before or after a trailing .git is trimmed", full, ErrNotGitHubRepo)
	}
	return owner, name, nil
}
