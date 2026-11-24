package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// listingDispatch lists summaries for the project.
type listingDispatch struct {
	outboxDispatch
	summaries []dispatch.IssueSummary
}

func (d *listingDispatch) ListIssues(context.Context, string, []string) ([]dispatch.IssueSummary, error) {
	return d.summaries, nil
}

// A record written before the record carried the issue's repository is filled at boot from the
// repository Dispatch resolves the issue to and that repository's default branch, GitHub read once
// per repository. A record Dispatch resolves to none is left as it is, and the issue and the cause
// are logged, while every other record is filled: one record's repository never stops the boot
// (INFRA-4401 D11), and the backfill answers GitHub's spelling of each record it filled, for the
// refresher's backfill to have GitHub intake hear it (backfillLater), and that a record is left,
// for the refresher to try it again.
func TestTheBootFillsRecordsWithNoRepository(t *testing.T) {
	seed := func(t *testing.T) *workflowRuntime {
		t.Helper()
		pool := isolatedOutboxPool(t)
		if err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
			for _, key := range []string{"LEGION-7", "LEGION-9", "LEGION-12"} {
				if _, err := tx.Exec(context.Background(), `insert into issues (key, tree, project, title, phase, generation, status, rank, last_dispatch_seq)
					values ($1, $1, 'LEGION', $1, 'planning', 1, 'in_progress', 'A', 1)`, key); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		github := newRepositoryGitHub(t, "release/v7.1")
		github.fullName = "Acme/Widgets"
		return &workflowRuntime{pool: pool, records: projectRecords{Store: record.NewStore(), project: "LEGION"},
			defaultBranches: newDefaultBranches(roleTokens{}, nil, github.url, defaultBranchTTL, time.Now)}
	}

	t.Run("Dispatch resolves none for some", func(t *testing.T) {
		w := seed(t)
		client := &listingDispatch{summaries: []dispatch.IssueSummary{{Key: "LEGION-7"}, {Key: "LEGION-9", ResolvedRepo: "acme/widgets"}}}
		var logs bytes.Buffer
		filled, left, err := backfillRepositories(context.Background(), w.pool, w.records, client, "LEGION", t.TempDir(), w.defaultBranches, ghrepo.Renames{}, slog.New(slog.NewTextHandler(&logs, nil)))
		if err != nil || !slices.Equal(repositoryNames(filled), []string{"Acme/Widgets"}) || !left {
			t.Fatalf("backfill = filled %v, left %t, %v; want LEGION-9 filled on GitHub's Acme/Widgets, the two Dispatch resolves none for left, and no error", filled, left, err)
		}
		if unfilled := unfilledKeys(t, w); !slices.Equal(slices.Sorted(slices.Values(unfilled)), []string{"LEGION-12", "LEGION-7"}) {
			t.Fatalf("unfilled after the backfill = %v, want LEGION-12 and LEGION-7 left as they are", unfilled)
		}
		for _, key := range []string{"LEGION-7", "LEGION-12"} {
			if !strings.Contains(logs.String(), leftAs(key, `""`, "Dispatch resolves no repository for it: set its repository in Dispatch")) {
				t.Fatalf("backfill log = %s, want %s named as left with its cause", logs.String(), key)
			}
		}
	})

	t.Run("Dispatch resolves every one", func(t *testing.T) {
		w := seed(t)
		github := newRepositoryGitHub(t, "release/v7.1")
		github.fullName = "Acme/Widgets"
		w.defaultBranches = newDefaultBranches(roleTokens{}, nil, github.url, defaultBranchTTL, time.Now)
		client := &listingDispatch{summaries: []dispatch.IssueSummary{
			{Key: "LEGION-7", ResolvedRepo: "acme/widgets"}, {Key: "LEGION-9", ResolvedRepo: "acme/widgets"}, {Key: "LEGION-12", ResolvedRepo: "acme/widgets"},
		}}
		var logs bytes.Buffer
		if _, _, err := backfillRepositories(context.Background(), w.pool, w.records, client, "LEGION", t.TempDir(), w.defaultBranches, ghrepo.Renames{}, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
			t.Fatalf("backfill: %v", err)
		}
		if unfilled := unfilledKeys(t, w); len(unfilled) != 0 {
			t.Fatalf("unfilled after the backfill = %v, want none", unfilled)
		}
		if reads := github.reads.Load(); reads != 1 {
			t.Fatalf("GitHub was read %d times for one repository, want once", reads)
		}
		if !strings.Contains(logs.String(), "legion workflow backfill filled LEGION-12 with Acme/Widgets@release/v7.1") {
			t.Fatalf("backfill log = %s, want each record named", logs.String())
		}
	})
}

func unfilledKeys(t *testing.T, w *workflowRuntime) []string {
	t.Helper()
	var keys []string
	if err := pgx.BeginFunc(context.Background(), w.pool, func(tx pgx.Tx) error {
		unfilled, err := w.records.UnfilledIssues(context.Background(), tx, "LEGION")
		for _, issue := range unfilled {
			keys = append(keys, issue.Key)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return keys
}

// A record written before the record carried its repository is filled only where the pull request
// of each of its merges is on the repository Dispatch resolves it to. LEGION-1's plan holds a and b,
// each a task that no task needs, so each merges by itself: a's pull request #7 is on acme/widgets,
// which Dispatch resolves; where b's #8 is on zeta/tools, the record is left, named with #8 and the
// fix; where #8 is on acme/widgets too, the record is filled.
//
// The fault it catches is a record filled on a repository the issue did not work in, read from one
// merge of several (its control: the backfill reading the issue's merge pull request alone, a's
// #7, under which the record is filled on acme/widgets while #8 is on zeta/tools).
func TestTheBackfillReadsThePullRequestOfEachMerge(t *testing.T) {
	ctx := context.Background()
	github := newNamedGitHub(t, map[string]string{"acme/widgets": "acme/widgets@main", "zeta/tools": "zeta/tools@main"})
	client := &listingDispatch{summaries: []dispatch.IssueSummary{{Key: "LEGION-1", ResolvedRepo: "acme/widgets"}}}
	for _, tc := range []struct{ name, bRepo, want string }{
		{"b's pull request on another repository", "zeta/tools", "Dispatch resolves it to acme/widgets, but its pull request #8 is on zeta/tools: set the issue's repository in Dispatch to zeta/tools (dispatch issue-update --issue LEGION-1 --repo zeta/tools)"},
		{"each pull request on the resolved repository", "acme/widgets", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := isolatedOutboxPool(t)
			if _, err := pool.Exec(ctx, `insert into issues (key, tree, project, title, phase, generation, status, rank, last_dispatch_seq, repo, default_branch)
				values ('LEGION-1', 'LEGION-1', 'LEGION', 'root', 'implementing', 1, 'in_progress', 'A', 1, '', '')`); err != nil {
				t.Fatal(err)
			}
			records := record.NewStore()
			if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				v := record.PlanVersion{Issue: "LEGION-1", Generation: 1, Version: 1, ArtifactID: "plan-1", Source: record.PlanSourceOwner,
					GivenBy: "legion-legion-legion-1-architect", GivenAt: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)}
				var tasks []record.PlanTask
				for _, id := range []string{"a", "b"} {
					tasks = append(tasks, record.PlanTask{Issue: "LEGION-1", Generation: 1, Task: id, PlanVersion: 1, BlockText: "Do " + id + ".",
						State: record.TaskRunning})
				}
				if err := records.PutPlan(ctx, tx, v, tasks, nil); err != nil {
					return err
				}
				for _, pr := range []record.PullRequest{
					{Issue: "LEGION-1", Repo: "acme/widgets", Number: 7, HeadSHA: "head-a", State: record.PullRequestOpen},
					{Issue: "LEGION-1", Repo: tc.bRepo, Number: 8, HeadSHA: "head-b", State: record.PullRequestOpen},
				} {
					task := map[int]string{7: "a", 8: "b"}[pr.Number]
					branch, err := records.AssignTaskBranch(ctx, tx, "LEGION-1", 1, task)
					if err != nil {
						return err
					}
					pr.Branch = branch
					if err := records.PutPullRequest(ctx, tx, pr); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			w := &workflowRuntime{pool: pool, records: records, log: quietLogger()}
			left, logged := backfillLogged(t, pool, records, client, t.TempDir(),
				newDefaultBranches(roleTokens{}, nil, github.url, defaultBranchTTL, time.Now), ghrepo.Renames{})
			got := recordedIssue(t, w, "LEGION-1")
			if tc.want != "" {
				if want := leftAs("LEGION-1", "acme/widgets", tc.want); !left || !strings.Contains(logged, want) {
					t.Fatalf("backfill left %t, logging:\n%s\nwant the record left, logged as %s", left, logged, want)
				}
				if got.Repo != "" {
					t.Fatalf("LEGION-1 = %s after the backfill left it, want it unfilled", got.Repo)
				}
				return
			}
			if left || got.Repo != "acme/widgets" {
				t.Fatalf("backfill left %t and LEGION-1 = %q, logging:\n%s\nwant LEGION-1 filled with acme/widgets", left, got.Repo, logged)
			}
		})
	}
}
