package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// linkedOpenPull is GitHub's answer for the open pull request number of repo at head sha. Its
// title, body and branch name no issue, so only a link ties it to an issue.
func linkedOpenPull(repo string, number int, sha string) string {
	return fmt.Sprintf(`{"number":%d,"title":"A change","body":"","html_url":"https://github.com/%s/pull/%d",
		"state":"open","merged":false,"updated_at":"2026-11-05T07:15:08Z",
		"head":{"ref":"feature/no-key","sha":%q,"repo":{"full_name":%q}},"base":{"ref":"main"}}`,
		number, repo, number, sha, repo)
}

// A person links an open pull request to an issue by hand, with a PATCH of external_links (R5 of
// the results lookup). Dispatch has no event, no result and no head row of the pull request, and
// GitHub holds its head. One reconcile pass reads the pull request from GitHub and stores its head
// row. The results read of the issue then compares check k1 with that head. The test fails where
// the link write owes no read: then no step reads the pull request, and no head row is stored.
func TestALinkByHandStoresTheHeadOfThePullRequestItLinks(t *testing.T) {
	handler, database, served, fake := readThroughGitHub(t, "acme/tools")
	issue := issueInRepository(t, handler, "ACME", "acme/tools", "Linked by hand")
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", twoChecks)
	fake.pullAnswers(func(repo, number string) string {
		if repo != "acme/tools" || number != "12" {
			return ""
		}
		return linkedOpenPull(repo, 12, headCommit)
	})
	patchIssue(t, handler, issue.Key, map[string]any{"external_links": []map[string]string{{"url": "https://github.com/acme/tools/pull/12"}}})
	onePass(t, database, served)
	var sha string
	var readAt *time.Time
	err := database.Pool.QueryRow(context.Background(), `
		select head_sha, github_read_at from pull_request_heads where repo = 'acme/tools' and number = 12
	`).Scan(&sha, &readAt)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("after the link by hand and one pass, no head row of acme/tools#12 is stored; want the head that GitHub holds, %s", headCommit)
	}
	if err != nil {
		t.Fatalf("read the head row of acme/tools#12: %v", err)
	}
	if sha != headCommit || readAt == nil {
		t.Fatalf("the head row of acme/tools#12 holds %s with github_read_at %v; want %s, from a read of GitHub", sha, readAt, headCommit)
	}
	read := readResults(t, handler, issue.Key)
	if !comparedWithPull(t, read, "k1", 12) {
		check := checkOf(t, read, "k1")
		t.Fatalf("check k1 compares with %+v for the reason %q; want the head of acme/tools#12", check.ComparedWith, check.ComparedNothing)
	}
}

// owedReads is each read that pull_request_reads_owed holds, as "<repo>#<number> <reason>", in
// order.
func owedReads(t *testing.T, database *store.Store) []string {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(),
		`select repo || '#' || number || ' ' || reason from pull_request_reads_owed order by 1`)
	if err != nil {
		t.Fatalf("read the owed reads: %v", err)
	}
	owed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the owed reads: %v", err)
	}
	return owed
}

// A link by hand owes a read for each URL of a pull request that it adds: also in a repository
// that no project maps and the App reads, and also where a head row of that pull request is stored
// (R5 of the results lookup). GitHub serves a pull request at /issues/<n> as well as at /pull/<n>,
// so the /issues/ link of #31 owes a read too, which tells whether #31 is a pull request. Three
// links owe nothing, because none of them is the URL of a pull request:
//
//   - a page of a pull request;
//   - a URL of another site;
//   - a number with a zero before it.
//
// A link held before the write, in other letters, owes nothing again.
func TestALinkByHandOwesAReadForEachURLOfAPullRequestAndForNoOtherURL(t *testing.T) {
	handler, database, _, _ := readThroughGitHub(t, "acme/tools", "acme/elsewhere")
	issue := issueInRepository(t, handler, "ACME", "acme/tools", "Links by hand")
	writeReadMergedRow(t, database, "acme/tools", 30, headCommit, "")
	others := []map[string]string{
		{"url": "https://github.com/acme/tools/issues/31", "kind": "github_issue"},
		{"url": "https://github.com/acme/tools/pull/32/files"},
		{"url": "https://example.com/acme/tools/pull/33"},
		{"url": "https://github.com/acme/tools/pull/035"},
	}
	patchIssue(t, handler, issue.Key, map[string]any{"external_links": others})
	if owed := owedReads(t, database); !slices.Equal(owed, []string{"acme/tools#31 failure"}) {
		t.Fatalf("after an /issues/ link and three links that are no URL of a pull request, the owed reads are %q; want the read of #31 alone", owed)
	}
	pulls := []map[string]string{
		{"url": "https://github.com/acme/elsewhere/pull/34"},
		{"url": "https://github.com/acme/tools/pull/30"},
		{"url": "https://github.com/acme/tools/pull/36"},
	}
	patchIssue(t, handler, issue.Key, map[string]any{"external_links": append(slices.Clone(others), pulls...)})
	want := []string{"acme/elsewhere#34 failure", "acme/tools#30 failure", "acme/tools#31 failure", "acme/tools#36 failure"}
	if owed := owedReads(t, database); !slices.Equal(owed, want) {
		t.Fatalf("after links to acme/elsewhere#34 (no project maps its repository), acme/tools#30 (its head row is stored) and acme/tools#36 (no row), the owed reads are %q; want %q", owed, want)
	}
	if _, err := database.Pool.Exec(context.Background(), `delete from pull_request_reads_owed`); err != nil {
		t.Fatalf("settle the owed reads: %v", err)
	}
	patchIssue(t, handler, issue.Key, map[string]any{"external_links": append(slices.Clone(others),
		map[string]string{"url": "https://GitHub.com/Acme/Elsewhere/pull/34"},
		map[string]string{"url": "https://github.com/ACME/tools/pull/30"},
		map[string]string{"url": "https://GitHub.com/Acme/Tools/pull/36"})})
	if owed := owedReads(t, database); len(owed) != 0 {
		t.Fatalf("after the same links in other letters, the owed reads are %q; want none", owed)
	}
}

// A person links an open pull request of acme/loose to an issue whose own repository is acme/loose.
// No project maps acme/loose, and the App reads it. No event and no result come. One reconcile pass
// reads the pull request from GitHub one time (a GET of the pull request) and stores its head, and
// the results read then compares check k1 with that head (R5 of the results lookup). That read
// reads no commit messages, since the link names the issue; the same pass's facts line asks its
// batched query for them (readFactsLine), which this test does not count. The test fails where the
// link owes a read only in a repository that a project maps: then no step reads the pull request,
// and k1 compares with nothing.
func TestALinkByHandInARepositoryThatNoProjectMapsGetsItsHeadFromOneRead(t *testing.T) {
	handler, database, served, fake := readThroughGitHub(t, "acme/loose")
	if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects",
		map[string]string{"key": "LOOS", "name": "Loose"}, "alice"); response.Code != http.StatusCreated {
		t.Fatalf("create project LOOS: status=%d body=%s", response.Code, response.Body.String())
	}
	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues",
		map[string]any{"project": "LOOS", "title": "Linked in a repository that no project maps", "spec": "A spec\n", "repo": "acme/loose"}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("create the issue in acme/loose: status=%d body=%s", created.Code, created.Body.String())
	}
	issue := decodeBody[model.Issue](t, created)
	if issue.ResolvedRepo == nil || *issue.ResolvedRepo != "acme/loose" {
		t.Fatalf("SETUP: the issue resolves to %v, want its own acme/loose", issue.ResolvedRepo)
	}
	var mappings int
	if err := database.Pool.QueryRow(context.Background(), `select count(*) from repo_projects`).Scan(&mappings); err != nil || mappings != 0 {
		t.Fatalf("SETUP: repo_projects holds %d rows (%v), want none", mappings, err)
	}
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", twoChecks)
	fake.pullAnswers(func(repo, number string) string {
		if repo != "acme/loose" || number != "12" {
			return ""
		}
		return linkedOpenPull(repo, 12, headCommit)
	})
	patchIssue(t, handler, issue.Key, map[string]any{"external_links": []map[string]string{{"url": "https://github.com/acme/loose/pull/12"}}})
	onePass(t, database, served)
	if reads := fake.pullReadsOf("acme/loose", 12); reads != 1 {
		t.Errorf("after the link by hand, one pass read acme/loose#12 from GitHub %d times; want 1", reads)
	}
	read := readResults(t, handler, issue.Key)
	if !comparedWithPull(t, read, "k1", 12) {
		check := checkOf(t, read, "k1")
		t.Fatalf("after the link by hand and one pass, check k1 compares with %+v for the reason %q; want the head of acme/loose#12", check.ComparedWith, check.ComparedNothing)
	}
	if owed := owedReads(t, database); len(owed) != 0 {
		t.Errorf("after the pass read acme/loose#12, the owed reads are %q; want none", owed)
	}
}

// A person links a pull request of acme/hidden to an issue of acme/hidden, a repository that the
// App is not installed on. GitHub refuses each read of it, so the read stays owed until a read
// succeeds (R4 of the results lookup): after two passes the read is owed as a refusal, with
// refused_as app_not_installed, read two times, and refused_since keeps the time of the first
// refusal. The results route says that it cannot read the repository: check k1 compares with
// nothing for repository_unreadable, and the recorded pass of k1 does not count (pass_stands
// unknown). The test fails where a refused read of an owed pull request ends the owe.
func TestALinkByHandToAPullRequestThatGitHubRefusesStaysOwedAsARefusal(t *testing.T) {
	handler, database, served, _ := readThroughGitHub(t, "acme/tools")
	issue := issueInRepository(t, handler, "HIDE", "acme/hidden", "Linked where the App cannot read")
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", twoChecks)
	recordResult(t, handler, issue.Key, resultBody("k1", "pass", headCommit, nil))
	patchIssue(t, handler, issue.Key, map[string]any{"external_links": []map[string]string{{"url": "https://github.com/acme/hidden/pull/40"}}})
	if owed, want := owedReads(t, database), []string{"acme/hidden#40 failure"}; !slices.Equal(owed, want) {
		t.Fatalf("after the link to acme/hidden#40, the owed reads are %q; want %q", owed, want)
	}
	var first time.Time
	for pass := 1; pass <= 2; pass++ {
		onePass(t, database, served)
		var reason string
		var reads int
		var refusedAs *string
		var refusedSince *time.Time
		err := database.Pool.QueryRow(context.Background(), `
			select reason, reads, refused_as, refused_since from pull_request_reads_owed where repo = 'acme/hidden' and number = 40
		`).Scan(&reason, &reads, &refusedAs, &refusedSince)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("after pass %d, no read of acme/hidden#40 is owed; want a refusal, since GitHub refused each read", pass)
		}
		if err != nil {
			t.Fatalf("read the owed read of acme/hidden#40: %v", err)
		}
		if pass == 1 && refusedSince != nil {
			first = *refusedSince
		}
		if reason != "refusal" || reads != pass || refusedAs == nil || *refusedAs != model.RefusedAsAppNotInstalled ||
			refusedSince == nil || !refusedSince.Equal(first) {
			t.Fatalf("after pass %d, acme/hidden#40 is owed as %q, read %d times, refused as %v since %v; want a refusal read %d times, refused as %s since the first refusal %v",
				pass, reason, reads, refusedAs, refusedSince, pass, model.RefusedAsAppNotInstalled, first)
		}
	}
	comparedNothing(t, readResults(t, handler, issue.Key), "k1", model.ReasonRepositoryUnreadable, model.PassStandsUnknown)
}
