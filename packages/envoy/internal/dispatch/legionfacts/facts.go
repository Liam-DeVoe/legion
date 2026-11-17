// Package legionfacts computes one issue's stored Legion facts and writes them to issue_facts
// (dispatch://INFRA-2915), with the issues Legion sessions filed, which it writes to
// issue_filings (dispatch://INFRA-2916). The facts are computed once and stored as rows, so a
// reader of them reads rows and computes nothing but the close of each open part at its own
// clock (Row.At): the board reads a project's (Read), and the evals' Legion day rows and items
// read the issues closed in a window (ReadClosedIn) and the filings made in one (ReadFilingsIn).
//
// The computation holds no loop and no lease: facts.go answers the rows and the filings of a key
// set (computeKeys) or of a whole project (ComputeProject), and store.go writes them (WriteProject,
// WriteFilings). Run is the loop that decides when to call either, under one lease and woken by
// the feeds a row is computed from (consumer.go).
//
// The numbers are the metrics package's: each computation reads a population and calls
// metrics.ProjectFactsClosed, which is the one computation of the per-issue facts as a row holds
// them, each issue's facts closed at the newest input they read with how fast each grows after
// it, and the filings are metrics.ProjectFilings over the same population. Nothing here states a
// rule of its own, so a stored row can never disagree with the number a page computes. Neither
// read calls GitHub: a pass makes no GitHub call, and metrics documents that a merge time
// Dispatch does not hold leaves that issue with none.
package legionfacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/legion"
	"github.com/sjawhar/envoy/internal/dispatch/legion/metrics"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// Querier is what a read or a write in this package needs of its connection: the pool, or a
// transaction already open on it, which a consumer that writes the rows and its cursor in one
// commit holds (store.Pool refuses a second connection to a caller holding one).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Row is one issue's stored Legion facts, the row issue_facts holds. Every field is the column of
// the same name, and the migrations state what each one means (20261117100000, 20261117100017
// and 20261117100026).
//
// HoursByPhase holds the board's column ids, a column with no hours left out, so an issue whose
// work took no measured time holds an empty map and still has a row: a row means Legion worked
// the issue. WaitingReason is "" where the issue waits on nothing, which the column holds as
// null, and Repo is "" where no repository resolves, which the column holds as null too.
// WorkFirstAt and WorkLastAt are nil until the issue has measured work. Bars holds every one of
// the seven bars and IdleByRole every one of the six roles. UpdatedAt is when the row was
// written, never when it was read.
//
// The facts are closed at AsOf, the newest input the issue's facts read: every part of the issue
// still running at it - its current phase, its current segment, its current wait - began at or
// before AsOf, and Growth is how fast each fact grows after it (metrics.Growth, empty for a
// closed issue). A row as stored holds the facts as of AsOf, which no clock moves; a reader gives
// them at its own clock with At, which is the one computation a read makes.
type Row struct {
	Project       string
	Issue         string
	OpenedAt      time.Time
	ClosedAt      *time.Time
	HoursByPhase  map[string]float64
	WorkHours     float64
	ReviewRounds  int
	RootKey       string
	TreeDepth     int
	SlotHours     float64
	WaitingReason string
	WorkFirstAt   *time.Time
	WorkLastAt    *time.Time

	ParkedHours                  float64
	QueuedHours                  float64
	StartedAt                    *time.Time
	MergedAt                     *time.Time
	Bars                         map[string]metrics.BarPart
	IdleByRole                   map[string]float64
	SlotWaitHours                float64
	SentBack                     bool
	ImplementingAfterFirstReview float64
	TestingAfterFirstReview      float64
	Children                     int
	Repo                         string

	AsOf      time.Time
	Growth    metrics.Growth
	UpdatedAt time.Time
}

// Filing is one issue a Legion session filed, the row issue_filings holds (migration
// 20261117100007): the filed issue, the project whose session filed it, when, the role and the
// issue that filed it and the phase that issue was in, the class metric 4 gives the filing, and
// the filed issue's tree root as it stands now. FilerIssue and FilerPhase are "" for a filing by
// the controller, which works no issue.
type Filing struct {
	Project    string
	Issue      string
	FiledAt    time.Time
	FilerRole  string
	FilerIssue string
	FilerPhase string
	Class      string
	RootKey    string
}

// computeKeys answers the rows of the named issues, each closed at the newest input its facts
// read (metrics.ProjectFactsClosed), and the filings of the filed issues filed names that a
// Legion session of the project filed, computed over one population read up to now
// (metrics.ReadIssues). That population is wider than the keys asked for, because an issue's
// hours read its whole tree and a filing reads its filer's issue, and the rows of the keys alone
// are kept: a key that carries no Legion phase work gets no row, as it gets no facts entry. It is
// what one pass of the consumer computes for a project it does not read whole.
func computeKeys(ctx context.Context, pool *store.Pool, project string, keys, filed []string, now time.Time) ([]Row, []Filing, error) {
	if len(keys) == 0 && len(filed) == 0 {
		return nil, nil, nil
	}
	population, err := metrics.ReadIssues(ctx, pool, project, keys, filed, now)
	if err != nil {
		return nil, nil, err
	}
	wanted := make(map[string]bool, len(keys))
	for _, key := range keys {
		wanted[key] = true
	}
	rows, err := rowsOf(ctx, pool, population, project, wanted, now)
	if err != nil {
		return nil, nil, err
	}
	return rows, filingsOf(population, project), nil
}

// ComputeProject answers the rows of every issue of the project with Legion phase work, open or
// closed, each closed at the newest input its facts read, and every filing a Legion session of
// the project ever made, from one population read up to now (metrics.ReadProject): the whole
// read of a project, which the consumer writes whole.
func ComputeProject(ctx context.Context, pool *store.Pool, project string, now time.Time) ([]Row, []Filing, error) {
	population, err := metrics.ReadProject(ctx, pool, project, now)
	if err != nil {
		return nil, nil, err
	}
	rows, err := rowsOf(ctx, pool, population, project, nil, now)
	if err != nil {
		return nil, nil, err
	}
	return rows, filingsOf(population, project), nil
}

// rowsOf turns the population's facts into rows, in key order: each issue's facts closed at the
// newest input they read and their growth after it (metrics.ProjectFactsClosed), which no clock
// moves, so a row whose inputs did not move is the row the table already holds. wanted names the
// issues to keep, whose facts alone are computed, and a nil wanted keeps every issue with facts.
// now is the moment the row is written, its UpdatedAt.
//
// The waiting reason is the one card rule (legion.CardWaiting), read through the one waiting
// line (legion.WaitingLine), which the board builds from its own cards. The daemon's admission
// block is read once per computation, because it is one row of the project rather than a fact of
// any issue, and the repositories are read once for every row kept (ReadRepos).
func rowsOf(ctx context.Context, q Querier, population *metrics.Population, project string, wanted map[string]bool, now time.Time) ([]Row, error) {
	admission, err := readAdmission(ctx, q, project)
	if err != nil {
		return nil, err
	}
	line := legion.NewWaitingLine(legion.WaitingLineInput{
		Admission: admission,
		Issue:     func(key string) (legion.LineIssue, bool) { return lineIssue(population, key) },
		Tree:      population.TreeNode,
	})
	facts := metrics.ProjectFactsClosed(population, wanted)
	keys := slices.Sorted(maps.Keys(facts))
	repos, err := ReadRepos(ctx, q, keys)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(keys))
	for _, key := range keys {
		fact := facts[key]
		row := Row{
			Project:       project,
			Issue:         key,
			WaitingReason: reasonOf(line, population.Issues[key]),
			Repo:          repos[key],
			AsOf:          fact.AsOf,
			Growth:        fact.Growth,
			UpdatedAt:     now,
		}
		row.SetFacts(fact.IssueFacts)
		rows = append(rows, row)
	}
	return rows, nil
}

// Facts is row as the Legion day builders read it: the facts metrics.ProjectFactsClosed computed
// of the issue, which the row holds column for column (SetFacts writes them the other way), as of
// the moment the row is of (AsOf, which a reader moves to its own clock with At).
func (row Row) Facts() metrics.IssueFacts {
	return metrics.IssueFacts{
		OpenedAt: row.OpenedAt, ClosedAt: row.ClosedAt, PhaseHours: row.HoursByPhase, WorkHours: row.WorkHours,
		ReviewRounds: row.ReviewRounds, Root: row.RootKey == row.Issue, Tree: row.RootKey, TreeDepth: row.TreeDepth,
		SlotHours: row.SlotHours, WorkFirstAt: row.WorkFirstAt, WorkLastAt: row.WorkLastAt,
		ParkedHours: row.ParkedHours, QueuedHours: row.QueuedHours, StartedAt: row.StartedAt, MergedAt: row.MergedAt,
		Bars: row.Bars, SentBack: row.SentBack, ImplementingHoursAfterFirstReview: row.ImplementingAfterFirstReview,
		TestingHoursAfterFirstReview: row.TestingAfterFirstReview, Children: row.Children,
		SlotWaitHours: row.SlotWaitHours, IdleByRole: row.IdleByRole,
	}
}

// SetFacts writes an issue's facts into row, column for column: the reverse of Facts, and the one
// list of the fact columns a row takes its facts by, whether a pass writes them (rowsOf) or a
// reader closes them at its own clock (At). Root is no column: Facts reads it from RootKey, which
// Tree sets. Every other field of row is left as it was.
func (row *Row) SetFacts(facts metrics.IssueFacts) {
	row.OpenedAt, row.ClosedAt, row.HoursByPhase, row.WorkHours = facts.OpenedAt, facts.ClosedAt, facts.PhaseHours, facts.WorkHours
	row.ReviewRounds, row.RootKey, row.TreeDepth, row.SlotHours = facts.ReviewRounds, facts.Tree, facts.TreeDepth, facts.SlotHours
	row.WorkFirstAt, row.WorkLastAt = facts.WorkFirstAt, facts.WorkLastAt
	row.ParkedHours, row.QueuedHours, row.StartedAt, row.MergedAt = facts.ParkedHours, facts.QueuedHours, facts.StartedAt, facts.MergedAt
	row.Bars, row.IdleByRole, row.SlotWaitHours, row.SentBack = facts.Bars, facts.IdleByRole, facts.SlotWaitHours, facts.SentBack
	row.ImplementingAfterFirstReview = facts.ImplementingHoursAfterFirstReview
	row.TestingAfterFirstReview = facts.TestingHoursAfterFirstReview
	row.Children = facts.Children
}

// filingsOf turns the population's filings into rows, classified by metric 4's own rule
// (metrics.ProjectFilings, one per filed issue).
func filingsOf(population *metrics.Population, project string) []Filing {
	filings := []Filing{}
	for _, filed := range metrics.ProjectFilings(population) {
		filings = append(filings, Filing{
			Project:    project,
			Issue:      filed.Key,
			FiledAt:    filed.At,
			FilerRole:  filed.Filer,
			FilerIssue: filed.FilerIssue,
			FilerPhase: filed.FilerPhase,
			Class:      filed.Class,
			RootKey:    filed.Root,
		})
	}
	return filings
}

// IssueFiling is filing as the splitting builder reads it: the row metric 4's table names it by
// and the filed issue's tree root (filingsOf writes them the other way).
func (filing Filing) IssueFiling() metrics.IssueFiling {
	return metrics.IssueFiling{
		FiledIssue: metrics.FiledIssue{
			Key: filing.Issue, At: filing.FiledAt, Class: filing.Class, Filer: filing.FilerRole,
			FilerIssue: filing.FilerIssue, FilerPhase: filing.FilerPhase,
		},
		Root: filing.RootKey,
	}
}

// reposStatement resolves the repository of every issue its one argument names, by the server's
// one rule (refs.IssueRepoTerms), which the issue page reads a page of issues by. The text is
// built once, at start, as refs builds its own statement over the same terms.
var reposStatement = `
	with recursive subjects as (select unnest($1::text[]) as key), ` + refs.IssueRepoTerms + `
	select i.key, ` + refs.IssueRepoResolved + `, ` + refs.IssueRepoTooDeep + `
	from issues i ` + refs.IssueRepoJoin + `
	where i.key = any($1::text[])`

// ReadRepos is the repository each of the keys resolves to, by key, and no entry for an issue
// none resolves for: the repository a pass stores in a row of issue_facts, and the one the Legion
// evals' day rows cut an issue and a filing by (evals.ReadLegionDay) and the eval items route
// shows and filters an issue by, each resolved when it is read. A parent chain the walk could not
// finish names no answer either: the issue page refuses such an issue rather than answer its
// project's default, which would be a wrong one, and a refusal here would stop every pass of the
// project's facts for good. The row holds no repository for it, which is the answer a chain that
// names none gets.
func ReadRepos(ctx context.Context, q Querier, keys []string) (map[string]string, error) {
	repos := map[string]string{}
	if len(keys) == 0 {
		return repos, nil
	}
	rows, err := q.Query(ctx, reposStatement, keys)
	if err != nil {
		return nil, fmt.Errorf("resolve the repositories of %d issues: %w", len(keys), err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var repo *string
		var tooDeep bool
		if err := rows.Scan(&key, &repo, &tooDeep); err != nil {
			return nil, fmt.Errorf("resolve the repositories of %d issues: %w", len(keys), err)
		}
		if repo != nil && !tooDeep {
			repos[key] = *repo
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve the repositories of %d issues: %w", len(keys), err)
	}
	return repos, nil
}

// reresolveRepos resolves the repository of every stored row again by the rule that filled it
// (ReadRepos) and rewrites the repo of each row whose repository moved, in tx, answering their
// keys. It is what a change of a repository's mapping owes the stored rows (readChangePage): the
// rule falls back to the project's default mapping, so a mapping change moves the repository of
// rows whose issue no feed names. No read shows the stored repo; a pass's write compares it with
// the repository the pass resolved (upsert), so a later move of the issue's repository is a
// change of its row, which makes the day-row refresher rebuild the issue's day. A stored repo left
// behind by a mapping would make a move back to it no change at all. The rows' other columns and
// their updated_at stand, since nothing they were computed from moved. It reads every stored row
// and resolves them all in one statement: a mapping is an operator's setting, written rarely
// beside every other change this loop reads.
func reresolveRepos(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `select project_key, issue_key, coalesce(repo, '') from issue_facts order by project_key, issue_key`)
	if err != nil {
		return nil, fmt.Errorf("read the stored rows' repositories: %w", err)
	}
	var projects, keys, repos []string
	var project, key, repo string
	if _, err := pgx.ForEachRow(rows, []any{&project, &key, &repo}, func() error {
		projects, keys, repos = append(projects, project), append(keys, key), append(repos, repo)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read the stored rows' repositories: %w", err)
	}
	resolved, err := ReadRepos(ctx, tx, keys)
	if err != nil {
		return nil, err
	}
	var movedProjects, moved, movedRepos []string
	for i, key := range keys {
		if resolved[key] != repos[i] {
			movedProjects, moved, movedRepos = append(movedProjects, projects[i]), append(moved, key), append(movedRepos, resolved[key])
		}
	}
	if len(moved) == 0 {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `
		update issue_facts facts set repo = nullif(given.repo, '')
		from unnest($1::text[], $2::text[], $3::text[]) as given(project, issue, repo)
		where facts.project_key = given.project and facts.issue_key = given.issue
	`, movedProjects, moved, movedRepos); err != nil {
		return nil, fmt.Errorf("rewrite the repositories of %d stored rows: %w", len(moved), err)
	}
	return moved, nil
}

// lineIssue is what the waiting line reads of one issue the daemon reports (legion.LineIssue):
// the phase of its newest `phase` observation and the slot of its newest `slot` one. An issue none
// of whose fields holds a value at its newest observation is one the newest good read does not
// carry — the sampler recorded nothing of it, or wrote each of its fields `null` when it left the
// daemon's state — which is the rule the board's own line reads its issues by
// (legion.NewestObservations, api/legion_board.go's issues map), and the line numbers it in no
// place and draws it no card.
func lineIssue(population *metrics.Population, key string) (legion.LineIssue, bool) {
	issue := population.Issues[key]
	if issue == nil || !carriedByTheNewestRead(issue) {
		return legion.LineIssue{}, false
	}
	phase, _ := newestPhase(issue)
	return legion.LineIssue{Phase: phase, Slot: newestSlot(issue)}, true
}

// carriedByTheNewestRead is whether one of the issue's fields holds a value at its newest
// observation: the newest good read carries the issue (legion.NewestObservations' rule).
func carriedByTheNewestRead(issue *metrics.Issue) bool {
	for _, marks := range issue.Observed {
		if len(marks) > 0 && marks[len(marks)-1].Value != "null" {
			return true
		}
	}
	return false
}

// reasonOf is why the issue waits, by the one card rule (legion.CardWaiting), and "" where the
// rule answers that it waits for nothing. The rule reads the issue's phase, the column it stands
// in, its place in the daemon's waiting line, the issues a blocker link holds it on, when it
// entered its phase, and the wait the daemon recorded. The line answers the middle three, as it
// answers them for the card the board draws of the same issue. The phase and the wait come from
// the newest observations of the issue, which the population already holds.
//
// An issue the board draws no card for stands in no column, and the rule then answers that it
// waits for nothing: a child its tree has not started is counted on the card above it, and the
// stored row of it says what that card says of it, which is nothing.
func reasonOf(line *legion.WaitingLine, issue *metrics.Issue) string {
	if issue == nil {
		return ""
	}
	phase, phaseSince := newestPhase(issue)
	waiting := legion.CardWaiting(legion.WaitingInput{
		Phase:        phase,
		Column:       line.Column(issue.Key),
		WaitingPlace: line.Place(issue.Key),
		BlockedBy:    line.BlockedBy(issue.Key),
		PhaseSince:   phaseSince,
		WaitingOn:    newestWait(issue),
	})
	if waiting == nil {
		return ""
	}
	return waiting.Reason
}

// newestPhase is the phase the issue is in and when it entered it: the newest `phase` observation
// and the moment it was recorded, since the sampler writes a row only where the value moved. The
// marks are in order, so the newest is the last.
func newestPhase(issue *metrics.Issue) (string, *time.Time) {
	marks := issue.Observed[legion.FieldPhase]
	if len(marks) == 0 {
		return "", nil
	}
	newest := marks[len(marks)-1]
	since := newest.At.UTC()
	return legion.JSONString([]byte(newest.Value)), &since
}

// newestSlot is the slot the issue holds of its own, from its newest `slot` observation, and nil
// where it holds none or where the daemon sent a value of another shape (legion.ParseSlot).
func newestSlot(issue *metrics.Issue) *int {
	marks := issue.Observed[legion.FieldSlot]
	if len(marks) == 0 {
		return nil
	}
	slot, _ := legion.ParseSlot([]byte(marks[len(marks)-1].Value))
	return slot
}

// newestWait is the wait the daemon recorded of the issue, from its newest `waiting_on`
// observation (legion.ParseWait). A value of another shape, and a wait with no kind, read as no
// wait at all, which is the one decode the board reads the same field by.
func newestWait(issue *metrics.Issue) *legion.Wait {
	marks := issue.Observed[legion.FieldWaitingOn]
	if len(marks) == 0 {
		return nil
	}
	wait, _ := legion.ParseWait([]byte(marks[len(marks)-1].Value))
	return wait
}

// readAdmission is the daemon's newest admission block, which the sampler writes as one row of
// the project under legion.AdmissionKey. It is nil for a project whose daemon has never answered,
// for one that sends no block, and for one whose newest good read no longer carried it (a `null`
// newest row), and nil again where the row holds a value of another shape: the daemon owns the
// wire shape, and a reader of one names what it expects.
//
// The newest row is legion_current's, the one row per field the base keeps for its readers
// (migration 20261117022936, legion.NewestObservations), read by that table's primary key and
// under the rule the board reads the block by: a `null` newest row holds nothing. So the card's
// waiting line and the stored row's are read from one block.
func readAdmission(ctx context.Context, q Querier, project string) (*legion.Admission, error) {
	var value []byte
	err := q.QueryRow(ctx, `
		select value
		from legion_current
		where project = $1 and issue_key = $2 and field = $3 and value <> 'null'::jsonb
	`, project, legion.AdmissionKey, legion.FieldAdmission).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the admission block of %s: %w", project, err)
	}
	var admission legion.Admission
	if err := json.Unmarshal(value, &admission); err != nil {
		return nil, nil
	}
	return &admission, nil
}
