package replay

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/blockers"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
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

// Under the rule returned-by-first-slot the line's returned part stands by the moment of each
// issue's first slot in its current generation (record.LineReturnedByFirstSlot), which the replay
// keeps in a table of its own database through its rule tables (ruleTables): each pass's read of its
// facts keeps the slot each holder holds as its generation's first where the table holds none, and
// reads the first slots back into the facts (record.LineFacts.FirstSlots). In a replay's own
// database, three roots of the project stand in implementing in trees that run. ACME-1 takes a slot
// at 09:00, ACME-2 at 09:20 and ACME-3 at 09:30, each read by a pass; ACME-2 gives its slot back
// for a wait at 10:00, ACME-3 at 10:10 and ACME-1 at 10:20, and each wait has ended by the pass at
// 10:30. Under main the returned part stands by the moment each gave its slot back, ACME-1 last
// behind the two that came back before it; under returned-by-first-slot ACME-1, first admitted,
// stands ahead of them. ACME-1 then takes a slot again at 10:40, which leaves its first slot of
// the generation at 09:00; its next generation reads no first slot until a pass reads the slot
// it holds in it. Under main no pass keeps any.
func TestTheFirstSlotsPlaceTheReturnedPart(t *testing.T) {
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
	token, err := claim.ProjectToken("ACME")
	if err != nil {
		t.Fatal(err)
	}
	at := func(hour, minute int) time.Time { return time.Date(2001, 3, 5, hour, minute, 0, 0, time.UTC) }
	root := func(key, rank string, generation uint64) record.Issue {
		return record.Issue{Key: key, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: key, Project: "ACME",
			Title: key, Phase: phase.Implementing, Generation: generation, Status: "in_progress", DispatchStatus: "in_progress", Rank: rank,
			HandedOver: true, LastDispatchSeq: 1}
	}
	for _, name := range []string{record.MainRule.Name, "returned-by-first-slot"} {
		rule, kept := record.SlotRuleNamed(name)
		if !kept {
			t.Fatalf("the repository keeps no rule named %q", name)
		}
		byFirstSlot := name != record.MainRule.Name
		t.Run(name, func(t *testing.T) {
			err := pgx.BeginTxFunc(ctx, st.Pool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
				must := func(err error, what string) {
					t.Helper()
					if err != nil {
						t.Fatalf("%s: %v", what, err)
					}
				}
				for i, key := range []string{"ACME-1", "ACME-2", "ACME-3"} {
					must(records.PutIssue(ctx, tx, root(key, fmt.Sprintf("a%d", i+1), 1)), "record "+key)
					_, err := records.OpenTreeLifecycle(ctx, tx, token, key, treelifecycle.AuthorityWorkflow)
					must(err, "open the tree of "+key)
				}
				// pass reads the facts as a pass under rule reads them (record.ReadLineFactsUnder and
				// the replay's rule tables) and judges the line at now over them.
				pass := func(now time.Time) ([]record.LineEntry, record.LineFacts) {
					t.Helper()
					issues, err := records.LiveIssues(ctx, tx, "ACME")
					must(err, "read the live issues")
					slots, err := records.Slots(ctx, tx)
					must(err, "read the slots")
					facts, err := record.ReadLineFactsUnder(ctx, tx, records, "ACME", issues, nil, rule)
					must(err, "read the line facts")
					must((ruleTables{}).ReadRuleFacts(ctx, tx, issues, &facts), "read the facts the rule keeps")
					_, facts, line, _, err := blockers.JudgeLine(ctx, tx, records, "ACME", issues, slots, facts, nil, now)
					must(err, "judge the line")
					return line, facts
				}
				slot := func(key string, index int, now time.Time) {
					t.Helper()
					must(records.PutSlot(ctx, tx, record.Slot{Issue: key, Index: index, AdmittedAt: now}), "give "+key+" a slot")
					pass(now)
				}
				slot("ACME-1", 0, at(9, 0))
				slot("ACME-2", 1, at(9, 20))
				slot("ACME-3", 2, at(9, 30))
				for _, release := range []struct {
					key string
					at  time.Time
				}{{"ACME-2", at(10, 0)}, {"ACME-3", at(10, 10)}, {"ACME-1", at(10, 20)}} {
					must(records.ReleaseSlotForWait(ctx, tx, release.key, release.at), "release "+release.key+" for a wait")
				}
				line, facts := pass(at(10, 30))
				order, firsts := lineWordOf(line), "the first slots are "+firstSlotsWord(facts.FirstSlots)
				want, wantFirsts := "[ACME-2 ACME-3 ACME-1]", "the first slots are none"
				if byFirstSlot {
					want, wantFirsts = "[ACME-1 ACME-2 ACME-3]", "the first slots are ACME-1 09:00, ACME-2 09:20, ACME-3 09:30"
				}
				if order != want || firsts != wantFirsts {
					t.Errorf("the line at 10:30 is %s and %s, want %s and %s", order, firsts, want, wantFirsts)
				}
				for _, entry := range line {
					if entry.Part != record.PartReturned {
						t.Errorf("%s stands in the part %q, want the returned part", entry.Issue.Key, entry.Part)
					}
				}

				slot("ACME-1", 0, at(10, 40))
				if _, facts := pass(at(10, 45)); byFirstSlot && !facts.FirstSlots["ACME-1"].Equal(at(9, 0)) {
					t.Errorf("after ACME-1's slot again at 10:40 its first slot reads %v, want 09:00", facts.FirstSlots["ACME-1"])
				}
				must(records.PutIssue(ctx, tx, root("ACME-1", "a1", 2)), "move ACME-1 to its next generation")
				issues, err := records.LiveIssues(ctx, tx, "ACME")
				must(err, "read the live issues")
				read, err := readFirstSlots(ctx, tx, issues)
				must(err, "read the first slots before a pass")
				if first, kept := read["ACME-1"]; kept {
					t.Errorf("ACME-1's next generation reads the first slot %v before any pass read a slot of it", first)
				}
				if _, facts := pass(at(11, 0)); byFirstSlot && !facts.FirstSlots["ACME-1"].Equal(at(10, 40)) {
					t.Errorf("ACME-1's next generation reads the first slot %v, want the slot it holds, 10:40", facts.FirstSlots["ACME-1"])
				}
				var count int
				must(tx.QueryRow(ctx, `select count(*) from replay_first_slots`).Scan(&count), "count the first slots kept")
				if want := map[bool]int{false: 0, true: 4}[byFirstSlot]; count != want {
					t.Errorf("the table keeps %d first slots under %s, want %d", count, name, want)
				}
				return errRolledBack
			})
			if !errors.Is(err, errRolledBack) {
				t.Fatalf("the test's transaction: %v", err)
			}
		})
	}
}

// lineWordOf is the keys of a waiting line, in its order.
func lineWordOf(line []record.LineEntry) string {
	keys := make([]string, 0, len(line))
	for _, entry := range line {
		keys = append(keys, entry.Issue.Key)
	}
	return fmt.Sprint(keys)
}

// firstSlotsWord is the first slots of the facts, by key in order and to the minute, or "none".
func firstSlotsWord(firsts map[string]time.Time) string {
	if len(firsts) == 0 {
		return "none"
	}
	var said []string
	for _, key := range slices.Sorted(maps.Keys(firsts)) {
		said = append(said, key+" "+firsts[key].UTC().Format("15:04"))
	}
	return strings.Join(said, ", ")
}
