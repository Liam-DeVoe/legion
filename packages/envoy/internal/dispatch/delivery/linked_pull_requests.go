package delivery

import (
	"context"
	"fmt"

	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// linkedPullRequestTerms are the `with recursive` terms of the pull requests the external links
// of the issue $1 name in a repository the issue does not resolve to: linked_pulls (repo,
// number, url), one row per link that names a pull request by the one link rule
// (LinkedPullRequestSQL, linkedPullRequest in Go), the repository lowercased, as refs.ExternalRepo
// spells a repository, and under the canonical name it is called today (refs.CanonicalRepoSQL
// over renames, the pairs the boot read).
//
// The record route reads them to say which repositories a result of the issue may name a commit
// of (LinkedPullRequests, api/results_linked.go's refuseUnknownCommit): an issue whose work spans
// two repositories links the pull request it has in the second one, and a result at that pull
// request's head names a commit of the second repository. They are read from the links
// themselves, since the route asks GitHub about each pull request they name, and Dispatch may hold
// no row of it yet. The results read takes the same pull request from the statement instead
// (HeadsAttributedTo, BaseBranchMergedIn): a link of a pull request's own URL gives that pull
// request to the issue in any repository (attributedTerms), so it is one of the issue's heads
// once Dispatch holds its row, and the timeline names the same issue for it. A link to a pull
// request of the repository the issue resolves to names nothing here, since that repository is
// asked about the commit before any link is read. The terms hold the issue to a repository by the
// attribution's own rule, so the two cannot disagree about which repository is the issue's.
//
// The link rule is the link write's, so each link the write owes a read for names its pull request
// here too: the /pull/ and the /issues/ form, the scheme, the host and the path word in any letter
// case, a number up to 2147483647, and an owner and a name GitHub can have (refs.SplitGitHubRepo).
// A link the rule refuses names nothing here.
func linkedPullRequestTerms(renames refs.Renames) string {
	return `subjects as (select $1::text as key), ` + refs.IssueRepoTerms + `,
	linked_pulls as (
		select ` + refs.CanonicalRepoSQL(renames, "link.repo") + ` as repo, link.number, link.url
		from (
			select l.url, lower(lp.repo) as repo, lp.number
			from issue_external_links l cross join lateral ` + LinkedPullRequestSQL("l.url") + ` lp
			where l.issue_key = $1
		) link
		join issues i on i.key = $1
		` + refs.IssueRepoJoin + `
		where not ` + attributionHeld(renames, "link.repo") + `
	)`
}

// LinkedPullRequest is one pull request an issue's external links name in a repository the
// issue does not resolve to (linkedPullRequestTerms).
type LinkedPullRequest struct {
	Repo   string
	Number int
}

// linkedPullRequestsQuery reads linkedPullRequestTerms in the order the links are listed in.
func linkedPullRequestsQuery(renames refs.Renames) string {
	return `with recursive ` + linkedPullRequestTerms(renames) + `
	select repo, number from linked_pulls order by url`
}

// LinkedPullRequests is every pull request the external links of issueKey name in a repository
// the issue does not resolve to, in the order the issue lists its links. The result route reads
// it to say which repositories a result of the issue may name a commit of (api/results_linked.go,
// refuseUnknownCommit).
func (a *Attributor) LinkedPullRequests(ctx context.Context, q refs.Queryer, issueKey string) ([]LinkedPullRequest, error) {
	rows, err := q.Query(ctx, linkedPullRequestsQuery(a.renames), issueKey)
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
