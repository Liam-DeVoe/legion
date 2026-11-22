package replay

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// errRolledBack ends a transaction of a test that must leave nothing behind it.
var errRolledBack = errors.New("the test's transaction is rolled back")

// The merger's ready mark of the release at 9542c6fb reaches the admission pass through its facts
// under the rule that reads it alone, and only for the visit to merging it fell in; and the covers
// a pass keeps read back as it kept them (ruleTables, design 10.10). Under each rule the repository
// keeps, in a replay's own database, an issue stands in merging at generation 1 holding the
// merger's phase entry 3, and a ReadyMark goes through the replay's handler: under legion-v19.57.3
// the pass's facts carry the mark (record.LineFacts.Ready) and the issue keeps no slot in merging
// (record.LineFacts.KeepsSlot), and once the merger's entry moves on, as the next visit to merging
// moves it, the mark reads nothing; under every other rule the handler writes no mark. Under cover
// the covers kept are the covers read, and a second keep replaces the first; under every other rule
// the facts carry none.
func TestTheReadyMarkAndTheCoversReadThroughTheFacts(t *testing.T) {
	dsn := replayDSN(t)
	ctx := context.Background()
	db, st, err := openReplayDatabase(ctx, dsn)
	if err != nil {
		t.Fatalf("make a replay's database: %v", err)
	}
	defer func() {
		st.Close()
		if err := db.drop(ctx); err != nil {
			t.Errorf("drop the replay's database: %v", err)
		}
	}()
	records := record.NewStore()
	issue := record.Issue{Key: "ACME-1", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: "ACME-1",
		Project: "ACME", Title: "ACME-1", Phase: phase.Merging, Generation: 1, Status: "retro", Rank: "a0", LastDispatchSeq: 1}
	merger := func(entry int64) record.PhaseRow {
		return record.PhaseRow{Issue: issue.Key, Role: claim.RoleMerger, Claim: "acme-1-merger", Entry: entry}
	}
	read := func(tx pgx.Tx, rule record.SlotRule) record.LineFacts {
		t.Helper()
		facts := record.LineFacts{Rule: rule}
		if err := (ruleTables{}).ReadRuleFacts(ctx, tx, []record.Issue{issue}, &facts); err != nil {
			t.Fatalf("under the rule %q, read the facts the rule keeps: %v", rule.Name, err)
		}
		return facts
	}
	for _, rule := range record.SlotRules() {
		t.Run(rule.Name, func(t *testing.T) {
			err := pgx.BeginTxFunc(ctx, st.Pool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
				if err := records.PutIssue(ctx, tx, issue); err != nil {
					t.Fatalf("record %s: %v", issue.Key, err)
				}
				if err := records.PutPhase(ctx, tx, merger(3)); err != nil {
					t.Fatalf("record the merger's phases row of %s: %v", issue.Key, err)
				}
				handler := replayHandler{clock: func() time.Time { return time.Date(2001, 3, 5, 9, 0, 0, 0, time.UTC) }, rule: rule}
				if _, err := handler.Apply(ctx, tx, ReadyMark{Issue: issue.Key, Generation: issue.Generation}); err != nil {
					t.Fatalf("apply the ready mark of %s: %v", issue.Key, err)
				}
				var marks int
				if err := tx.QueryRow(ctx, `select count(*) from replay_ready_marks`).Scan(&marks); err != nil {
					t.Fatalf("count the ready marks: %v", err)
				}
				reads, want := rule.Name == "legion-v19.57.3", 0
				if reads {
					want = 1
				}
				if marks != want {
					t.Errorf("the handler wrote %d ready marks, want %d", marks, want)
				}
				facts := read(tx, rule)
				if _, ready := facts.Ready[issue.Key]; ready != reads {
					t.Errorf("the facts carry the ready mark of %s: %t, want %t", issue.Key, ready, reads)
				}
				if keeps := facts.KeepsSlot(issue); keeps != (rule.PhaseKeepsSlot(phase.Merging) && !reads) {
					t.Errorf("%s keeps its slot in merging after the mark: %t", issue.Key, keeps)
				}
				if err := records.PutPhase(ctx, tx, merger(4)); err != nil {
					t.Fatalf("move the merger's phase entry of %s on: %v", issue.Key, err)
				}
				if _, ready := read(tx, rule).Ready[issue.Key]; ready {
					t.Errorf("the ready mark of %s's merger entry 3 reads at its entry 4", issue.Key)
				}

				first, second := map[string]string{"ACME-2": "ACME-3", "ACME-4": "ACME-5"}, map[string]string{"ACME-2": "ACME-3"}
				for _, kept := range []map[string]string{first, second} {
					if err := (ruleTables{}).KeepCovers(ctx, tx, kept); err != nil {
						t.Fatalf("keep the covers %v: %v", kept, err)
					}
					covers := read(tx, rule).Covers
					if rule.Take != record.TakeCover {
						if covers != nil {
							t.Errorf("the facts carry the covers %v under a rule that takes none", covers)
						}
						continue
					}
					if !maps.Equal(covers, kept) {
						t.Errorf("the covers read are %v, and the pass kept %v", covers, kept)
					}
				}
				return errRolledBack
			})
			if !errors.Is(err, errRolledBack) {
				t.Fatalf("the test's transaction: %v", err)
			}
		})
	}
}
