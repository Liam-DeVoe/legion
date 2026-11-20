package delivery

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// linkedPullRequestTerms are the `with recursive` terms of the pull requests the external links
// of the issue $1 name in a repository the issue does not resolve to: linked_pulls (repo,
// number, url), one row per link of the form https://github.com/<owner>/<repo>/pull/<n>, the
// repository lowercased, as refs.ExternalRepo spells a mapping, and under the canonical name
// it is called today (refs.CanonicalRepoSQL).
//
// The record route reads them to say which repositories a result of the issue may name a commit
// of (LinkedPullRequests, api/results_linked.go's refuseUnknownCommit): an issue whose work spans
// two repositories links the pull request it has in the second one, and a result at that pull
// request's head names a commit of the second repository. They are read from the links
// themselves, since the route asks GitHub about each pull request they name, and Dispatch may hold
// no row of it yet. The results read takes no head from them. A pull request is one of the issue's
// heads only where the attribution gives it the issue (HeadAttributedTo, HeadsAttributedTo): a
// link of a pull request's own URL gives it to the issue in any repository (attributionHeldFor),
// but not a pull request whose head is in a fork, which anyone may open at any commit
// (headAttribution), and only once a writer has judged its row by that rule, which a change of the
// links asks for at once (LinksChanged). The terms name the repository of such a head
// (AttributedHead.LinkedRepo) and the merge BaseBranchMergedIn walks, which is why they are read
// beside the attribution there. A link to a pull request of the repository the issue resolves to
// names nothing here, since that repository is asked about the commit before any link is read. The
// terms hold the issue to a repository by the attribution's own rule, so the two cannot disagree
// about which repository is the issue's.
//
// The link names the pull request and not its head: a head is the stored row's, matched by the
// repository and the number, so a push that moves the head moves the basis with it.
//
// The owner and the name are held to the patterns of GitHub's names that refs reads a name with
// (refs.GitHubOwnerPattern, refs.GitHubNamePattern): an owner of a letter or digit and then up to
// 38 letters, digits, `-` and `_`, as a managed user's login (`mona-cat_octo`) is, and a name of
// letters, digits, `.`, `-` and `_`. A name of `.` or `..` alone is no repository: the reads
// build `/repos/{owner}/{repo}/...` from the two parts, and url.PathEscape keeps a dot, so a link
// naming either would send a read up the path (`/repos/../x/installation`) rather than to a
// repository. The issue PATCH takes any absolute HTTP(S) URL as a link (api/issue_refs.go,
// validateExternalURL), so such a URL is still a link of the issue and is simply no pull request
// here, as api's parseSourceRepo refuses the same two names for a source. The rest of the rule,
// the name's 100 characters and a name that is empty, `.` or `..` once a trailing `.git` is
// trimmed, is held where a read puts the name into a GitHub path (refs.SplitGitHubRepo).
//
// The number is bounded to nine digits so that the cast cannot fail on a link a person typed.
var linkedPullRequestTerms = `subjects as (select $1::text as key), ` + refs.IssueRepoTerms + `,
	linked_pulls as (
		select ` + refs.CanonicalRepoSQL("link.repo") + ` as repo, link.number, link.url
		from (
			select parsed.url, lower(parsed.part[1] || '/' || parsed.part[2]) as repo, parsed.part[3]::int as number
			from (
				select l.url, regexp_match(l.url, '^https://github\.com/(` + refs.GitHubOwnerPattern + `)/(` + refs.GitHubNamePattern + `)/pull/([1-9][0-9]{0,8})$') as part
				from issue_external_links l where l.issue_key = $1
			) parsed
			where parsed.part is not null and parsed.part[2] not in ('.', '..')
		) link
		join issues i on i.key = $1
		` + refs.IssueRepoJoin + `
		where not ` + attributionHeld("link.repo") + `
	)`

// linkedHeadJoin joins each linked_pulls row, l, to the stored head row of the pull request it
// names, h: the same number in the same repository, compared without case, since a row written
// from a `pull_request` envelope carries GitHub's own spelling of the repository and a link is
// read lowercased.
const linkedHeadJoin = `join pull_request_heads h on lower(h.repo) = lower(l.repo) and h.number = l.number`

// LinkedPullRequest is one pull request an issue's external links name in a repository the
// issue does not resolve to (linkedPullRequestTerms).
type LinkedPullRequest struct {
	Repo   string
	Number int
}

// linkedPullRequestsQuery reads linkedPullRequestTerms in the order the links are listed in.
var linkedPullRequestsQuery = `with recursive ` + linkedPullRequestTerms + `
	select repo, number from linked_pulls order by url`

// LinkedPullRequests is every pull request the external links of issueKey name in a repository
// the issue does not resolve to, in the order the issue lists its links. The result route reads
// it to say which repositories a result of the issue may name a commit of (api/results_linked.go,
// refuseUnknownCommit).
func LinkedPullRequests(ctx context.Context, q refs.Queryer, issueKey string) ([]LinkedPullRequest, error) {
	rows, err := q.Query(ctx, linkedPullRequestsQuery, issueKey)
	if err != nil {
		return nil, fmt.Errorf("read the pull requests %s links: %w", issueKey, err)
	}
	defer rows.Close()
	var linked []LinkedPullRequest
	for rows.Next() {
		var pull LinkedPullRequest
		if err := rows.Scan(&pull.Repo, &pull.Number); err != nil {
			return nil, fmt.Errorf("read the pull requests %s links: %w", issueKey, err)
		}
		linked = append(linked, pull)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the pull requests %s links: %w", issueKey, err)
	}
	return linked, nil
}

// HeadOfChangedLink is one stored pull request whose own URL a change of an issue's links added
// or removed (LinksChanged): the key of its row, its URL, and whether the change took the pull
// request from the issue at once (Uncredited).
type HeadOfChangedLink struct {
	Repo       string
	Number     int
	URL        string
	Uncredited bool
}

// AttributionEvent is the issue.pull_request_attribution_changed event an uncredited pull request
// appends on issueKey, the issue it left: the event a head write appends for the same move
// (appendAttributionChange), from issueKey to nobody, with the source "none".
func (head HeadOfChangedLink) AttributionEvent(issueKey string) model.Event {
	left := issueKey
	return model.Event{
		IssueKey: &left, Type: AttributionEventType, Actor: AttributionEventActor,
		Payload: IssuePullRequestAttributionEventPayload{
			Repo: head.Repo, Number: head.Number, URL: head.URL, From: &left, To: nil,
			Source: string(sourceNone),
		},
	}
}

// linksChangedQuery is LinksChanged's statement. The inner select locks each row whose URL a
// changed link names before it reads whether the change took the row's issue away, so a writer of
// the same row waits for this transaction rather than having its issue read before its write and
// overwritten after it. No index serves `lower(url)`: a change of an issue's links is a person's
// edit, rare beside the reads of the table, and one scan of a row per pull request Dispatch has
// seen costs that edit a few milliseconds.
var linksChangedQuery = `
	update pull_request_heads h set
		attribution_rule = 0,
		issue_key = case when o.gone then null else h.issue_key end,
		attribution_source = case when o.gone then '` + string(sourceNone) + `' else h.attribution_source end
	from (
		select p.repo, p.number,
			p.issue_key is not distinct from $1::text and p.attribution_source = '` + string(sourceExternalLink) + `'
				and not exists (
					select 1 from issue_external_links l where l.issue_key = $1 and lower(l.url) = lower(p.url)
				) as gone
		from pull_request_heads p
		where lower(p.url) = any($2::text[])
		for no key update
	) o
	where h.repo = o.repo and h.number = o.number
	returning h.repo, h.number, h.url, o.gone`

// LinksChanged is what a change of issueKey's external links does to the stored pull requests
// whose own URL a link it added or removed names, urls being those links' URLs, run in the
// transaction that writes the links, after the write (api/issue_patch.go). A link of a pull
// request's own URL is the attribution's first source, held in any repository (attributionHeldFor,
// R19 of the spec of several open pull requests), so a pull request is the issue's while the
// issue links it, and its stored issue was judged with links it no longer has. It answers each
// such pull request, for the caller to read again from GitHub at once (RefreshPullRequestHead).
//
// A pull request whose issue was issueKey by the link this change removed is that issue's no
// longer, at once: the row is credited to nobody in this transaction, so no read of the issue's
// results counts its head after the change commits, and the caller appends the move on the issue
// (HeadOfChangedLink.AttributionEvent). Which issue its other sources give it is the read's to
// say: the body and the commit messages are on no stored row, so only GitHub's answer can judge
// it. A pull request the change linked keeps its stored issue until that read: whether its head
// is in a fork, which gives it no issue, linked or not (headAttribution), is on no stored row
// either.
//
// Each such row's attribution_rule is set to 0, a rule older than every rule that judges a row
// now, so the reconcile judges the row again on its own where the caller's read did not answer or
// was never made: the merged arm takes a merged row of an older rule (ListStalePullRequestHeads),
// and an open row is read again within PullRequestHeadStaleAfter whatever its rule. Without it a
// merged row would keep the judgement of links it no longer has for good, since nothing else
// revisits a merged row the current rule judged.
func LinksChanged(ctx context.Context, q refs.Queryer, issueKey string, urls []string) ([]HeadOfChangedLink, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	lowered := make([]string, 0, len(urls))
	for _, url := range urls {
		lowered = append(lowered, strings.ToLower(url))
	}
	rows, err := q.Query(ctx, linksChangedQuery, issueKey, lowered)
	if err != nil {
		return nil, fmt.Errorf("judge again the pull requests whose links of %s changed: %w", issueKey, err)
	}
	defer rows.Close()
	var changed []HeadOfChangedLink
	for rows.Next() {
		var head HeadOfChangedLink
		if err := rows.Scan(&head.Repo, &head.Number, &head.URL, &head.Uncredited); err != nil {
			return nil, fmt.Errorf("judge again the pull requests whose links of %s changed: %w", issueKey, err)
		}
		changed = append(changed, head)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("judge again the pull requests whose links of %s changed: %w", issueKey, err)
	}
	slices.SortFunc(changed, func(a, b HeadOfChangedLink) int {
		return cmp.Or(strings.Compare(a.Repo, b.Repo), cmp.Compare(a.Number, b.Number))
	})
	return changed, nil
}

// baseBranchMergedInQuery is BaseBranchMergedIn's statement: the merged rows attributed to the
// issue that its links name in a repository it does not resolve to, held to one repository, in
// BaseBranchMergedFor's order.
var baseBranchMergedInQuery = `with recursive ` + linkedPullRequestTerms + `
	select h.base_ref from linked_pulls l
	` + linkedHeadJoin + `
	where h.issue_key = $1 and lower(l.repo) = lower($2) and h.state = 'merged'
	order by h.event_at desc nulls last, h.number desc
	limit 1`

// BaseBranchMergedIn is BaseBranchMergedFor for a repository issueKey does not resolve to: the
// branch the merged pull request that the issue links by its URL in repoFull merged into, and ""
// where Dispatch holds no merge of such a pull request that the attribution gives the issue. An
// issue whose work spans two repositories links the pull request it has in the second one, and
// the attribution gives it that pull request (attributionHeldFor), unless its head is in a fork
// (headAttribution). The results read measures an `after_merge` result whose commit the second
// repository holds against the branch answered here, in that repository (api/results_linked.go,
// linkedBranchBases), and BaseBranchMergedFor answers the repository the issue resolves to.
//
// The rows are ordered as BaseBranchMergedFor orders them, and the empty answer means what its
// empty answer means: no branch to measure against. A link to a pull request of the
// repository the issue resolves to names nothing here, so a repoFull the issue resolves to
// answers "". repoFull is compared without case with the canonical name each link is read under,
// as every other comparison of a repository here is.
//
// The linked rows are read through pull_request_heads_repo_head's leading column, one
// repository's rows for each linked pull request.
func BaseBranchMergedIn(ctx context.Context, pool *store.Pool, issueKey, repoFull string) (string, error) {
	var branch string
	err := pool.QueryRow(ctx, baseBranchMergedInQuery, issueKey, repoFull).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the merged linked pull request base branch of %s in %s: %w", issueKey, repoFull, err)
	}
	return branch, nil
}
