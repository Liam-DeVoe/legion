// check_states_install_test.go holds the settler to the former heads the install of
// pull_request_former_heads seeded (migration 20261122071001): the commits of results recorded on
// an open pull request's issue, unverified until the settler has read that pull request's commits
// once (verifyFormerHeads). The rig is check_states_test.go's.
package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// seedInstallCandidates writes what the install leaves for #70 of the rig's repository: each of
// shas as a candidate former head, unverified, and the issue marked owed.
func seedInstallCandidates(t *testing.T, rig *checkStatesRig, shas ...string) {
	t.Helper()
	if _, err := rig.database.Pool.Exec(context.Background(), `
		insert into pull_request_former_heads (repo, number, head_sha, verified)
		select $1, 70, sha, false from unnest($2::text[]) sha`, ciJobRepository, shas); err != nil {
		t.Fatalf("seed the install's candidates %v: %v", shas, err)
	}
	if _, err := rig.database.Pool.Exec(context.Background(), `select check_states_owe($1)`, rig.issue); err != nil {
		t.Fatalf("mark %s owed: %v", rig.issue, err)
	}
}

// candidatesOf is each former head the rig's #70 holds, by commit, and whether it is verified.
func candidatesOf(t *testing.T, rig *checkStatesRig) map[string]bool {
	t.Helper()
	rows, err := rig.database.Pool.Query(context.Background(), `
		select head_sha, verified from pull_request_former_heads where repo = $1 and number = 70`, ciJobRepository)
	if err != nil {
		t.Fatalf("read the former heads of #70: %v", err)
	}
	defer rows.Close()
	held := map[string]bool{}
	for rows.Next() {
		var sha string
		var verified bool
		if err := rows.Scan(&sha, &verified); err != nil {
			t.Fatalf("read the former heads of #70: %v", err)
		}
		held[sha] = verified
	}
	return held
}

// TestAFormerHeadTheInstallSeededIsPublishedOnceItsPullRequestListsIt: before the install, the tester
// recorded k1 passing at H1, while #70 was at H1, and a result at H3, a commit of another pull
// request; #70 is now at H2, and nothing recorded that it left H1. The install seeds H1 and H3 as
// #70's candidates and marks the issue owed. The settle reads #70's commits once, which list H1
// and H2: it keeps H1, publishes k1 yes there, and deletes H3, where nothing is published. A later
// settle reads #70's commits no more.
func TestAFormerHeadTheInstallSeededIsPublishedOnceItsPullRequestListsIt(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", eachHeadH1)
	eachHeadResult(t, rig.handler, rig.issue, "k2", "tester", "pass", eachHeadH3)
	upsertHead(t, rig, 70, eachHeadH2, rig.issue, "A change")
	settleOwed(t, rig)
	if _, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, eachHeadH1, "k1"); held {
		t.Fatal("k1 is published at H1 before the install, which nothing recorded #70 left")
	}
	rig.github.setPullCommits(70, eachHeadH1, eachHeadH2)
	seedInstallCandidates(t, rig, eachHeadH1, eachHeadH3)

	settleOwed(t, rig)
	if row, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, eachHeadH1, "k1"); !held || row.state == nil ||
		*row.state != model.PassStandsYes {
		t.Fatalf("k1 at H1, a head #70 left before the install and its commits list, is published as %s, want yes", publishedLine(row))
	}
	if _, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, eachHeadH3, ""); held {
		t.Fatal("H3, a commit #70's commits do not list, holds published states")
	}
	if got := candidatesOf(t, rig); len(got) != 1 || !got[eachHeadH1] {
		t.Fatalf("#70 holds the former heads %v after the settle, want H1 alone, verified", got)
	}
	if reads := rig.github.pullCommitReadsOf(70); reads != 1 {
		t.Fatalf("the settle read #70's commits %d times, want once", reads)
	}
	eachHeadResult(t, rig.handler, rig.issue, "k2", "tester", "fail", eachHeadH1)
	settleOwed(t, rig)
	if row, _ := publishedRow(t, rig.database, rig.issue, ciJobRepository, eachHeadH1, "k2"); row.state == nil || *row.state != model.PassStandsNo {
		t.Fatalf("k2 at H1 after the tester's fail is published as %s, want no", publishedLine(row))
	}
	if reads := rig.github.pullCommitReadsOf(70); reads != 1 {
		t.Fatalf("#70's commits were read %d times after a later settle, want still once: the candidates are verified", reads)
	}
}

// TestAFormerHeadTheInstallSeededWaitsForAReadOfItsPullRequestThatFailed: the install seeded H1 for
// #70 as above, and GitHub fails the read of #70's commits. H1 stays an unverified candidate with
// nothing published there, and the issue keeps a mark due after the backoff; once GitHub answers
// and that mark falls due, the settle verifies H1 and publishes k1 there. A pull request GitHub
// answers 404 for confirms nothing, then or later: its candidates go.
func TestAFormerHeadTheInstallSeededWaitsForAReadOfItsPullRequestThatFailed(t *testing.T) {
	rig := newCheckStatesRig(t, checkStatesTwoChecks)
	eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", eachHeadH1)
	upsertHead(t, rig, 70, eachHeadH2, rig.issue, "A change")
	settleOwed(t, rig)
	rig.github.setPullCommits(70, eachHeadH1, eachHeadH2)
	rig.github.failPullCommits(http.StatusBadGateway)
	seedInstallCandidates(t, rig, eachHeadH1)

	settleOwed(t, rig)
	if got := candidatesOf(t, rig); len(got) != 1 || got[eachHeadH1] {
		t.Fatalf("#70 holds the former heads %v after a failed read of its commits, want H1, unverified", got)
	}
	if _, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, eachHeadH1, "k1"); held {
		t.Fatal("k1 is published at H1, which no read of #70's commits has confirmed")
	}
	var later int
	if err := rig.database.Pool.QueryRow(context.Background(), `
		select count(*) from check_states_owed where issue_key = $1 and due_at > now() + interval '10 seconds'`, rig.issue).Scan(&later); err != nil {
		t.Fatalf("read the marks of %s: %v", rig.issue, err)
	}
	if later != 1 {
		t.Fatalf("%s holds %d marks due after the backoff, want one: the unverified candidate owes another settle", rig.issue, later)
	}

	rig.github.failPullCommits(0)
	if _, err := rig.database.Pool.Exec(context.Background(), `update check_states_owed set due_at = now() where issue_key = $1`, rig.issue); err != nil {
		t.Fatalf("bring %s's mark due: %v", rig.issue, err)
	}
	settleOwed(t, rig)
	if row, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, eachHeadH1, "k1"); !held || row.state == nil ||
		*row.state != model.PassStandsYes {
		t.Fatalf("k1 at H1 once #70's commits are read is published as %s, want yes", publishedLine(row))
	}

	seedInstallCandidates(t, rig, eachHeadH3)
	rig.github.mu.Lock()
	delete(rig.github.pullCommits, 70)
	rig.github.mu.Unlock()
	settleOwed(t, rig)
	if got := candidatesOf(t, rig); len(got) != 1 || !got[eachHeadH1] {
		t.Fatalf("#70 holds the former heads %v after GitHub answered 404 for its commits, want H1 alone: a 404 confirms nothing", got)
	}
}
