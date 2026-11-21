package api

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
)

// TestADeliveryReadHoldsARenamedPrivateRepositoryByItsID: Delivery holds a pull request that names
// no issue, and a deploy run that shipped none, to the repository rule by the GitHub id GitHub's
// answer about it carries, as the GitHub proxy holds a repository, so a private project's
// repository GitHub renamed is hidden under its new name too, and a name another repository took
// later shows that repository.
//
// Over seedDeliveryReads' seed, PRIV-1 names acme/privold with the id 9001 its write recorded;
// GitHub renamed it acme/privnew, and a new repository (9010) took the name acme/privold.
// acme/privnew#12 (9001) and acme/privold#13 (9010) name no issue. alice, who does not see PRIV, is
// shown #13 and acme/other#3 and not #12; carol, who sees PRIV, is shown both. Over
// seedRunThatShippedNothing's seed, PRIV-1 names acme/widgetsold with 9020, the deploy repository
// acme/widgets' id, which every run of it records: alice's timeline lists no run and her read of
// run 702 answers what run 999 answers; carol reads run 702.
//
// Control: judge each by its name (askedOf ignoring the id), and alice is shown #12, hidden #13 and
// shown run 702; the test fails naming each.
func TestADeliveryReadHoldsARenamedPrivateRepositoryByItsID(t *testing.T) {
	seed := seedDeliveryReads(t, true, true)
	ctx, pool := t.Context(), seed.database.Pool
	if _, err := pool.Exec(ctx, `update issues set repo = 'acme/privold', repo_id = 9001 where key = $1`, seed.issue[seedPrivate]); err != nil {
		t.Fatalf("name acme/privold on PRIV's issue: %v", err)
	}
	merged := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	opened := merged.Add(-2 * time.Hour)
	id := func(value int64) *int64 { return &value }
	upsertPR(t, seed.database, delivery.DeliveryPullRequest{
		Repo: "acme/privnew", RepoID: id(9001), Number: 12, Title: "change 12", Author: "octocat",
		URL: "https://github.com/acme/privnew/pull/12", CreatedAt: &opened, MergedAt: &merged,
	})
	upsertPR(t, seed.database, delivery.DeliveryPullRequest{
		Repo: "acme/privold", RepoID: id(9010), Number: 13, Title: "change 13", Author: "octocat",
		URL: "https://github.com/acme/privold/pull/13", CreatedAt: &opened, MergedAt: &merged,
	})
	shown := readShownDelivery(t, seed, callerAlice).prs
	if slices.Contains(shown, "acme/privnew#12") || !slices.Contains(shown, "acme/privold#13") || !slices.Contains(shown, "acme/other#3") {
		t.Errorf("alice is shown the pull requests %v, want acme/privold#13 and acme/other#3 and not acme/privnew#12, whose repository 9001 is PRIV's", shown)
	}
	if carol := readShownDelivery(t, seed, callerCarol).prs; !slices.Contains(carol, "acme/privnew#12") || !slices.Contains(carol, "acme/privold#13") {
		t.Errorf("carol, who sees PRIV, is shown the pull requests %v, want acme/privnew#12 and acme/privold#13 among them", carol)
	}

	runs := seedRunThatShippedNothing(t, true)
	for _, statement := range []string{
		`update issues set repo = 'acme/widgetsold', repo_id = 9020 where key = $1`,
		`update delivery_runs set repo_id = 9020 where repo = 'acme/widgets' and $1::text is not null`,
	} {
		if _, err := runs.database.Pool.Exec(ctx, statement, runs.issue[seedPrivate]); err != nil {
			t.Fatalf("record acme/widgets as 9020, PRIV's: %v\n%s", err, statement)
		}
	}
	if got := readShownDelivery(t, runs, callerAlice).runs; len(got) != 0 {
		t.Errorf("alice reads the timeline's runs as %v, want none: the deploy repository 9020 is PRIV's under its old name", got)
	}
	missing := runAnswer(t, runs, callerAlice, 999)
	if !strings.HasPrefix(missing, "404 ") {
		t.Fatalf("alice reads run 999, which no run holds, as %s: the comparison proves nothing", missing)
	}
	if got := runAnswer(t, runs, callerAlice, deliveryRunFailed); got != missing {
		t.Errorf("alice reads run 702 as %s, want what run 999 answers: %s", got, missing)
	}
	if got := runAnswer(t, runs, callerCarol, deliveryRunFailed); !strings.HasPrefix(got, "200 ") {
		t.Errorf("carol, who sees PRIV, reads run 702 as %s, want its jobs", got)
	}
}
