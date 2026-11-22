package replay

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/admit"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// ruleTables is where the replay's admission pass reads and keeps what a rule the daemon does not
// run records beyond the daemon's own tables (admit.RuleTables), over the tables the replay makes
// in its own database (replayTables): the merger's ready marks the rule infra-w2 gives the slot
// back at, which the replay's handler writes (ReadyMark), and the covers that stand under the rule
// cover, which the pass itself keeps. A rule that reads neither reads nothing here.
type ruleTables struct{}

var _ admit.RuleTables = ruleTables{}

// readsReadyMarks says whether rule reads the merger's ready mark: it keeps a slot in merging and
// gives it back there (record.SlotRule.ReleaseAtReady), as record.ReadLineFactsUnder reads it.
func readsReadyMarks(rule record.SlotRule) bool {
	rule = rule.OrMain()
	return rule.ReleaseAtReady && rule.PhaseKeepsSlot(phase.Merging)
}

// ReadRuleFacts adds the ready marks, the waits beneath and the covers the facts' rule reads to
// facts. A ready mark reads for an issue standing in merging at the generation and the merger's
// phase entry it was made at, so a mark of an earlier visit to merging reads nothing once the issue
// has left it and come back (record.LineFacts.Ready). Under a rule whose start may take a cover, an
// issue standing in accepting whose sign-off the workflow held at its generation and its
// architect's entry (HeldSignOff), and an issue standing in merging whose pull request of its
// generation merged (the replay's own pull request record, merged by the recorded merge), wait on
// the issues beneath them still in the workflow, the set both of the workflow's holds read
// (record.OpenBeneath; record.LineFacts.BeneathWaits). The covers are the table's whole
// (record.LineFacts.Covers): a replay runs one project.
func (ruleTables) ReadRuleFacts(ctx context.Context, tx pgx.Tx, issues []record.Issue, facts *record.LineFacts) error {
	rule := facts.Rule.OrMain()
	if readsReadyMarks(rule) {
		ready, err := readMarks(ctx, tx, `select m.issue, m.generation from replay_ready_marks m
			left join phases p on p.issue = m.issue and p.role = $2
			where m.issue = any($1) and m.entry = coalesce(p.entry, 0)`, standingIn(issues, phase.Merging), string(claim.RoleMerger))
		if err != nil {
			return fmt.Errorf("read the merger's ready marks: %w", err)
		}
		if facts.Ready == nil {
			facts.Ready = make(map[string]struct{})
		}
		for key := range ready {
			facts.Ready[key] = struct{}{}
		}
	}
	if rule.Take != record.TakeCover {
		return nil
	}
	held, err := readMarks(ctx, tx, `select s.issue, s.generation from replay_held_signoffs s
		left join phases p on p.issue = s.issue and p.role = $2
		where s.issue = any($1) and s.entry = coalesce(p.entry, 0)`, standingIn(issues, phase.Accepting), string(claim.RoleArchitect))
	if err != nil {
		return fmt.Errorf("read the held sign-offs: %w", err)
	}
	merged, err := readMarks(ctx, tx, `select issue, generation from pull_requests where issue = any($1) and state = $2`,
		standingIn(issues, phase.Merging), string(record.PullRequestMerged))
	if err != nil {
		return fmt.Errorf("read the merged pull requests: %w", err)
	}
	facts.BeneathWaits = make(map[string][]string)
	for _, issue := range issues {
		_, signs := held[issue.Key]
		_, closes := merged[issue.Key]
		if !signs && !closes {
			continue
		}
		if open := record.OpenBeneath(issue, issues); len(open) > 0 {
			facts.BeneathWaits[issue.Key] = open
		}
	}
	rows, err := tx.Query(ctx, `select waiter, holder from replay_covers`)
	if err != nil {
		return fmt.Errorf("read the covers that stand: %w", err)
	}
	defer rows.Close()
	facts.Covers = make(map[string]string)
	for rows.Next() {
		var waiter, holder string
		if err := rows.Scan(&waiter, &holder); err != nil {
			return fmt.Errorf("read a cover: %w", err)
		}
		facts.Covers[waiter] = holder
	}
	return rows.Err()
}

// standingIn is the generation of each issue of issues that stands in at (record.StandsAt), by
// key.
func standingIn(issues []record.Issue, at phase.Phase) map[string]uint64 {
	generations := make(map[string]uint64)
	for _, issue := range issues {
		if record.StandsAt(issue) == at {
			generations[issue.Key] = issue.Generation
		}
	}
	return generations
}

// readMarks is the issues of generations that query, given their keys and arg, answers at the
// generation each stands at: query selects an issue's key and a generation, and a row of another
// generation is no mark of the one the issue stands at.
func readMarks(ctx context.Context, tx pgx.Tx, query string, generations map[string]uint64, arg string) (map[string]struct{}, error) {
	marked := make(map[string]struct{})
	if len(generations) == 0 {
		return marked, nil
	}
	rows, err := tx.Query(ctx, query, slices.Sorted(maps.Keys(generations)), arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var generation int64
		if err := rows.Scan(&key, &generation); err != nil {
			return nil, err
		}
		if uint64(generation) == generations[key] {
			marked[key] = struct{}{}
		}
	}
	return marked, rows.Err()
}

// KeepCovers makes covers the table's covers: the ones that ended go and the new ones are written.
func (ruleTables) KeepCovers(ctx context.Context, tx pgx.Tx, covers map[string]string) error {
	if _, err := tx.Exec(ctx, `delete from replay_covers`); err != nil {
		return fmt.Errorf("clear the covers: %w", err)
	}
	for _, waiter := range slices.Sorted(maps.Keys(covers)) {
		if _, err := tx.Exec(ctx, `insert into replay_covers (waiter, holder) values ($1, $2)`, waiter, covers[waiter]); err != nil {
			return fmt.Errorf("keep the cover of %s by %s: %w", waiter, covers[waiter], err)
		}
	}
	return nil
}
