package evals

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The seed writes the catalogue into a project (dispatch://INFRA-2916, R1 and R5): every computed
// eval as version 1, written_via seed, with its series, and one section of the project's
// `evals-md` per eval, whose method block the version's doc_block names. It runs at two places,
// so every project holds the catalogue however it was made: in the transaction that creates a
// project through the API (api's createProject, through EnsureTx); and on the day-row refresher's
// passes (refresh.load), which seed every project on the first pass of each process, so a
// catalogue the release changed reaches every project, and on each later pass every project no
// seed has reached (UnseededProjects: one a command or a fixture wrote by SQL while the server
// ran), with each project whose seed failed (seeding).
//
// A seed that finds an eval of the catalogue already written writes a new version of it only
// where the release changed what the catalogue states of it, its Computation or its series'
// SeriesComputation, or where its section is gone. The change is read against the version the
// seed last wrote, which holds every field the catalogue owns as that seed's build stated it, so
// every version the seed writes takes all of them from the catalogue. A person cannot set one of
// them on a computed eval (the register write refuses it, computationField). Every field a person
// sets is carried forward from the newest version, so no such write is accepted and then lost at
// the next boot (R5): the target, its inclusiveness and its judges, the owner role and the person,
// the issue, the links, the state and what replaced it, and the doc block. A series is read and
// written the same way, each on its own, its min_n carried forward: the catalogue states a series'
// min_n for its first version alone, as it states the target. A method block that exists is never
// written: a person's edit of it stands. An eval whose doc block the document no longer holds (no
// version yet, the block deleted, or the document gone) gets a section at the end of the
// document, and a version naming that section's block.
//
// An eval the seed takes over, one of the catalogue's slugs a reading or a register write
// registered before any seed of its project (a project made by SQL, or a slug a later release adds
// to the catalogue), holds what that registration defaulted where the catalogue states a value.
// The seed takes the catalogue's target, its inclusiveness and its judges where no register write
// wrote a version of the eval, since no person had a write in which to set them; and its method
// where the eval's section still holds the sentence its registration wrote
// (registrationSentence), which says no one has written one.

// Seeded is what one seed of a project wrote: the versions of definitions, the versions of
// series, the sections of `evals-md`, and whether the seed created the document. A seed that
// found the catalogue written wrote none of them.
type Seeded struct {
	Versions int
	Series   int
	Sections int
	Created  bool
}

// Ensure writes the catalogue into project in a transaction of its own (the seed's rules above),
// and answers what it wrote. A second Ensure with the same catalogue writes nothing.
func (s *Store) Ensure(ctx context.Context, project string) (Seeded, error) {
	var seeded Seeded
	err := s.inWrite(ctx, project, func(ctx context.Context, w *writeTx) error {
		var err error
		seeded, err = s.ensure(ctx, w)
		return err
	})
	if err != nil {
		return Seeded{}, err
	}
	return seeded, nil
}

// PendingSeed is a seed written in a caller's transaction (EnsureTx): what it wrote, and what the
// caller's commit owes, which Committed pays.
type PendingSeed struct {
	Seeded
	store *Store
	write *writeTx
}

// EnsureTx writes the catalogue into project in tx, the transaction that created the project, so
// the project and its evals commit together or not at all. ctx is the context the document
// service's Join of tx returned, whose ledger the caller commits. tx took the change log's lock
// first, before any other (delivery.LockChanges), as every registry write does (inWrite), since
// the seed's versions log their changes under it. Once tx has committed, the caller calls
// Committed on the answer, and never when tx did not commit.
func (s *Store) EnsureTx(ctx context.Context, tx pgx.Tx, project string) (PendingSeed, error) {
	w, err := openWrite(ctx, tx, project)
	if err != nil {
		return PendingSeed{}, err
	}
	seeded, err := s.ensure(ctx, w)
	if err != nil {
		return PendingSeed{}, err
	}
	return PendingSeed{Seeded: seeded, store: s, write: w}, nil
}

// Committed schedules the settlement of the `evals-md` the seed wrote and publishes the document
// events it appended, as Ensure does once its own transaction commits.
func (p PendingSeed) Committed() {
	if p.store != nil {
		p.store.committed(p.write)
	}
}

// UnseededProjects is every project that holds no version the seed wrote, in key order: a project
// made by SQL since the refresher's first pass, which its later passes seed (refresh.load). It is
// one statement, whose probe of each project is an index range of eval_versions_project.
func (s *Store) UnseededProjects(ctx context.Context) ([]string, error) {
	rows, err := s.database.Pool.Query(ctx, `
		select p.key from projects p
		where not exists (
			select 1 from eval_versions v where v.project_key = p.key and v.written_via = 'seed'
		)
		order by p.key
	`)
	if err != nil {
		return nil, fmt.Errorf("read the projects with no seeded eval: %w", err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the projects with no seeded eval: %w", err)
	}
	return keys, nil
}

// seeding is what the refresher's loop keeps of its seeds from one pass to the next
// (refresh.load): whether a pass of this process has seeded every project yet, the projects
// whose seed failed, which the next pass seeds again, and those whose seed was refused for their
// `evals-md` (seedRefusals). One goroutine runs the loop's passes, so it takes no lock.
type seeding struct {
	everyProject bool
	failed       map[string]bool
	refused      seedRefusals
}

// owed is the projects a pass seeds besides those it reads to seed: every one whose seed failed,
// and every one whose seed was refused for its document, in key order.
func (s *seeding) owed() []string {
	return slices.Concat(slices.Sorted(maps.Keys(s.failed)), slices.Sorted(maps.Keys(s.refused)))
}

// note records what a seed of project answered: a refusal for its document is held (seedRefusals),
// any other failure is seeded again by the next pass, and a project no row holds any more is no
// failure to seed again, since its seed is refused PROJECT_NOT_FOUND, and so is every later one.
func (s *seeding) note(project string, err error) {
	s.refused.note(project, err)
	var refusal *Error
	if err != nil && !errors.As(err, &refusal) {
		if s.failed == nil {
			s.failed = map[string]bool{}
		}
		s.failed[project] = true
		return
	}
	delete(s.failed, project)
}

// seedRefusals is the projects whose seed was refused because their `evals-md` artifact is no
// document (CodeDocumentNotDocument). The seed reads that artifact whatever else it writes
// (ensure, existingDocument), so it refuses such a project again for as long as the artifact
// stands, and only a person's write of the artifact moves it. A seed of such a project is
// therefore not tried again while one read says the artifact is still no document (pending),
// rather than once a pass, each a transaction of about ten statements. The zero value holds none.
type seedRefusals map[string]bool

// note records what a seed of project answered: a refusal for its document is held, and any other
// answer lets go of one.
func (r *seedRefusals) note(project string, err error) {
	var refusal *Error
	if !errors.As(err, &refusal) || refusal.Code != CodeDocumentNotDocument {
		delete(*r, project)
		return
	}
	if *r == nil {
		*r = seedRefusals{}
	}
	(*r)[project] = true
}

// pending is projects, each once and in their order, less each whose refusal is held and whose
// `evals-md` is still no document, which one statement reads for all of them. A held project
// whose artifact is a document now, or gone, is let go of and kept, so its seed runs again.
func (r seedRefusals) pending(ctx context.Context, pool *store.Pool, projects []string) ([]string, error) {
	seen := make(map[string]bool, len(projects))
	var unique, held []string
	for _, project := range projects {
		if seen[project] {
			continue
		}
		seen[project] = true
		unique = append(unique, project)
		if r[project] {
			held = append(held, project)
		}
	}
	if len(held) == 0 {
		return unique, nil
	}
	rows, err := pool.Query(ctx, `
		select project_key from artifacts
		where project_key = any($1) and issue_key is null and session_id is null and slug = $2 and kind <> 'doc'
	`, held, DocumentSlug)
	if err != nil {
		return nil, fmt.Errorf("read the %s artifacts of the projects whose seed was refused: %w", DocumentSlug, err)
	}
	still, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the %s artifacts of the projects whose seed was refused: %w", DocumentSlug, err)
	}
	for _, project := range held {
		if !slices.Contains(still, project) {
			delete(r, project)
		}
	}
	return slices.DeleteFunc(unique, func(project string) bool { return slices.Contains(still, project) }), nil
}

// seedWrite is one eval's write of a seed: the version of its definition and the versions of its
// series.
type seedWrite struct {
	definition Definition
	series     []Series
}

// ensure is the seed of w's project in w's transaction. It takes the lock of every eval of the
// catalogue, in the catalogue's order, before it reads what the project holds, so a second seed
// of the project, a register write and a readings write of one of those evals each wait for it
// and then read what it wrote. It then reads the newest version of each eval, the version the seed
// last wrote, the current series and the series as the seed last wrote them, of an eval it takes
// over its first version and whether a register write wrote one, and the blocks of `evals-md`;
// writes the missing sections and the methods it takes over in one write of the document; and
// writes the versions. The locks are one batch of statements and the versions another, each sent
// in one round trip and run in the order it was queued: a project's first seed writes 20 versions
// and 95 of series, which one statement each would make 115 round trips.
func (s *Store) ensure(ctx context.Context, w *writeTx) (Seeded, error) {
	ids := make([]string, len(catalogue))
	locks := &pgx.Batch{}
	for i, entry := range catalogue {
		ids[i] = EvalID(w.project, entry.definition.Slug)
		locks.Queue(lockEvalStatement, ids[i])
	}
	if err := w.SendBatch(ctx, locks).Close(); err != nil {
		return Seeded{}, fmt.Errorf("lock the seeded evals of %s: %w", w.project, err)
	}
	newest, err := readDefinitions(ctx, w.Tx, `
		select `+definitionColumns+` from evals_current where project_key = $1 and eval_id = any($2)
	`, w.project, ids)
	if err != nil {
		return Seeded{}, err
	}
	lastSeeded, err := readDefinitions(ctx, w.Tx, `
		select distinct on (eval_id) `+definitionColumns+` from eval_versions
		where project_key = $1 and eval_id = any($2) and written_via = 'seed'
		order by eval_id, version desc
	`, w.project, ids)
	if err != nil {
		return Seeded{}, err
	}
	current, err := readSeriesByEval(ctx, w.Tx, `
		select `+seriesColumns+` from eval_series_current where eval_id = any($1)
	`, ids)
	if err != nil {
		return Seeded{}, err
	}
	// The series as the seed last wrote them: the newest version of each that the seed wrote,
	// which is a version written in the transaction of a version of the definition the seed
	// wrote, since every seed that writes a series writes a version of its definition too.
	seededSeries, err := readSeriesByEval(ctx, w.Tx, `
		select distinct on (eval_id, series) `+seriesColumns+` from eval_series s
		where eval_id = any($1) and exists (
			select 1 from eval_versions v
			where v.eval_id = s.eval_id and v.written_via = 'seed' and v.created_at = s.created_at
		)
		order by eval_id, series, version desc
	`, ids)
	if err != nil {
		return Seeded{}, err
	}
	taken, err := readTakeovers(ctx, w, newest, lastSeeded)
	if err != nil {
		return Seeded{}, err
	}
	document, found, err := existingDocument(ctx, w)
	if err != nil {
		return Seeded{}, err
	}
	var markdown string
	var blocks []model.ArtifactBlock
	switch {
	case found && len(taken.firsts) > 0:
		if markdown, blocks, err = s.documents.TextWithBlocks(ctx, document.id); err != nil {
			return Seeded{}, fmt.Errorf("read the %s document of %s: %w", DocumentSlug, w.project, err)
		}
	case found:
		if blocks, err = s.documents.Blocks(ctx, document.id); err != nil {
			return Seeded{}, fmt.Errorf("read the blocks of the %s document of %s: %w", DocumentSlug, w.project, err)
		}
	}

	var sections []string
	var replaced []model.EditOp
	var writes []seedWrite
	for i, entry := range catalogue {
		existing, has := newest[ids[i]]
		last, seeded := lastSeeded[ids[i]]
		missing := !has || !hasBlock(blocks, existing.DocBlock)
		first, takeover := taken.firsts[ids[i]]
		unwritten := takeover && !missing && holdsRegistrationSentence(markdown, blocks, existing.DocBlock, first)
		series := seriesToWrite(ids[i], entry.series, current[ids[i]], seededSeries[ids[i]])
		if has && seeded && !missing && len(series) == 0 && entry.definition.Computation.same(last.Computation) {
			continue
		}
		next := existing
		if !has {
			next = Definition{EvalID: ids[i], ProjectKey: w.project, Slug: entry.definition.Slug, State: StateActive, Links: []Link{}}
		}
		if !has || (takeover && !taken.registered[ids[i]]) {
			next.Target, next.TargetInclusive, next.TargetJudges = entry.definition.Target, entry.definition.TargetInclusive, entry.definition.TargetJudges
		}
		next.Computation = entry.definition.Computation
		switch {
		case missing:
			next.DocBlock = uuid.NewString()
			sections = append(sections, sectionMarkdown(entry.definition.Slug, next.DocBlock, entry.method()))
		case unwritten:
			next.DocBlock = uuid.NewString()
			replaced = append(replaced,
				model.EditOp{Op: "insert", After: "block:" + existing.DocBlock, Markdown: methodBlock(next.DocBlock, entry.method())},
				model.EditOp{Op: "delete", Block: existing.DocBlock})
		}
		next.Version = existing.Version + 1
		next.WrittenVia, next.WrittenBy = ViaSeed, RegistryActor
		writes = append(writes, seedWrite{definition: next, series: series})
	}

	seeded := Seeded{Sections: len(sections) + len(replaced)/2, Created: !found}
	switch {
	case !found:
		if err := s.createDocument(ctx, w, strings.Join(sections, "\n"), RegistryActor); err != nil {
			return Seeded{}, err
		}
	case len(sections) > 0 || len(replaced) > 0:
		if err := lockDocumentOwner(ctx, w, document); err != nil {
			return Seeded{}, err
		}
		ops := replaced
		if len(sections) > 0 {
			ops = append(ops, model.EditOp{Op: "insert", After: "end", Markdown: strings.Join(sections, "\n")})
		}
		if err := s.editDocument(ctx, w, document, ops, RegistryActor); err != nil {
			return Seeded{}, err
		}
	}
	inserts := &pgx.Batch{}
	for _, write := range writes {
		statement, args, err := definitionInsert(write.definition, false)
		if err != nil {
			return Seeded{}, err
		}
		inserts.Queue(statement, args...)
		seeded.Versions++
		for _, series := range write.series {
			statement, args, err := seriesInsert(series)
			if err != nil {
				return Seeded{}, err
			}
			inserts.Queue(statement, args...).Exec(func(tag pgconn.CommandTag) error {
				if tag.RowsAffected() != 1 {
					return fmt.Errorf("version %d of series %s of %s was written by a write that holds no lock of the eval", series.Version, series.Series, series.EvalID)
				}
				return nil
			})
			seeded.Series++
		}
	}
	if inserts.Len() == 0 {
		return seeded, nil
	}
	if err := w.SendBatch(ctx, inserts).Close(); err != nil {
		return Seeded{}, fmt.Errorf("write the seeded versions of %s: %w", w.project, err)
	}
	return seeded, nil
}

// takeovers is what a seed reads of the evals of the catalogue it takes over (the seed's rules
// above), each by its id: its version 1, the registration whose defaults it holds, and whether a
// register write wrote any version of it, in which a person could have set its target.
type takeovers struct {
	firsts     map[string]Definition
	registered map[string]bool
}

// readTakeovers reads the takeovers of the evals newest holds a version of and lastSeeded none,
// in two statements, and none where there are none, as on every seed of a project seeded before.
func readTakeovers(ctx context.Context, w *writeTx, newest, lastSeeded map[string]Definition) (takeovers, error) {
	var ids []string
	for id := range newest {
		if _, seeded := lastSeeded[id]; !seeded {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return takeovers{}, nil
	}
	firsts, err := readDefinitions(ctx, w.Tx, `
		select `+definitionColumns+` from eval_versions where project_key = $1 and eval_id = any($2) and version = 1
	`, w.project, ids)
	if err != nil {
		return takeovers{}, err
	}
	rows, err := w.Query(ctx, `
		select distinct eval_id from eval_versions where project_key = $1 and eval_id = any($2) and written_via = $3
	`, w.project, ids, ViaRegister)
	if err != nil {
		return takeovers{}, fmt.Errorf("read which taken-over evals of %s a register write wrote: %w", w.project, err)
	}
	written, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return takeovers{}, fmt.Errorf("read which taken-over evals of %s a register write wrote: %w", w.project, err)
	}
	registered := make(map[string]bool, len(written))
	for _, id := range written {
		registered[id] = true
	}
	return takeovers{firsts: firsts, registered: registered}, nil
}

// holdsRegistrationSentence reports whether the block id of the document whose markdown and
// blocks these are still holds the sentence the registration of first, an eval's version 1 that a
// reading or a register write wrote, put in its section (registrationSentence): no one has
// written the eval's method since.
func holdsRegistrationSentence(markdown string, blocks []model.ArtifactBlock, id string, first Definition) bool {
	held, found := blockMethod(markdown, blocks, id)
	return found && sameMethod(held, registrationSentence(first.WrittenVia, first.WrittenBy, first.CreatedAt))
}

// seriesToWrite is the versions of the series of eval id a seed writes: version 1 of each series
// of the catalogue the eval does not hold, and the next version of each one the seed never wrote
// or whose SeriesComputation the catalogue states otherwise than the version the seed last wrote
// of it (seeded), with every field of the catalogue's and the min_n the series holds carried
// forward. A series the release did not change is left as it is, whatever a person set on it
// since. The min_n a series holds is carried forward, so the version the seed last wrote may hold
// a person's, which no catalogue change is read against.
func seriesToWrite(id string, catalogue []Series, current, seeded map[string]Series) []Series {
	var writes []Series
	for _, want := range catalogue {
		existing, has := current[want.Series]
		if last, wasSeeded := seeded[want.Series]; has && wasSeeded && want.SeriesComputation.same(last.SeriesComputation) {
			continue
		}
		next := want
		next.EvalID, next.Version = id, existing.Version+1
		if has {
			next.MinN = existing.MinN
		}
		writes = append(writes, next)
	}
	return writes
}

// readDefinitions answers the definitions statement reads, by eval id.
func readDefinitions(ctx context.Context, tx pgx.Tx, statement string, args ...any) (map[string]Definition, error) {
	rows, err := tx.Query(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("read the seeded evals: %w", err)
	}
	definitions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Definition, error) { return scanDefinition(row) })
	if err != nil {
		return nil, fmt.Errorf("read the seeded evals: %w", err)
	}
	byID := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		byID[definition.EvalID] = definition
	}
	return byID, nil
}

// readSeriesByEval answers the series statement reads, by eval id and then by name.
func readSeriesByEval(ctx context.Context, tx pgx.Tx, statement string, args ...any) (map[string]map[string]Series, error) {
	rows, err := tx.Query(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("read the series of the seeded evals: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Series, error) { return scanSeries(row) })
	if err != nil {
		return nil, fmt.Errorf("read the series of the seeded evals: %w", err)
	}
	byEval := map[string]map[string]Series{}
	for _, series := range all {
		if byEval[series.EvalID] == nil {
			byEval[series.EvalID] = map[string]Series{}
		}
		byEval[series.EvalID][series.Series] = series
	}
	return byEval, nil
}
