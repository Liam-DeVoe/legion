package evals

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
)

// The stored figures (dispatch://INFRA-2916, k8; migration 20261120072042) are the eval list's
// folds of the ranges the Evals tab offers, kept ahead of the read, so a paint of an offered range
// reads its figures by key where it would read every day row of the range and fold them
// (DayWindow, SeriesValuesOf). k8 measured the first paint at 84 days with a compare range on the
// farm's slowest node: 95.6 ms of database time over the day rows, 12.2 ms over the stored rows.
//
//   - eval_range_figures holds, for each project, repository ('' the project's own rows), range
//     (one of FigureLengths and its end day, exclusive) and eval, the statistics FoldStatistics
//     answers for the eval's listed series over the range's rows, those rows in day order, each
//     series' via-repository count, and the oldest updated_at of the rows (the read's freshness).
//     A range that holds no listed row of the eval has no row: its fold is the fold of no parts.
//   - eval_day_figures holds, for each project, repository, day and eval that holds a listed row
//     of the eval, the day's figures of each headline series that holds a row that day (the per-day
//     array SeriesValuesOf answers), and the stamp of the rows it folded (rowsStamp).
//
// A range exists for every length of FigureLengths and every end day from the day after the
// eval's first row through FigureLead days after today, for the project's own rows and for each
// repository the eval holds rows of: a compare range ends on any day before the window's, and a
// page held open keeps its compare range while its window rolls on.
//
// Every write keeps one rule: a stored figure is a fold of day rows, so it moves only with them.
// Each write of day rows folds again, in its own transaction, the figures of every day whose rows
// it moved (refoldFigures): the day's own row and every range whose span holds the day (D < E <=
// D+n), up to the last end day the project's figures are built through (figureScope), each
// upserted only where its figures moved and deleted where it holds no row any more. A day's rows
// moved where their stamp is not the one its stored day row recorded, or that row was folded under
// another fold key (foldKeyOf). So a write that moved nothing, such as a new build's rebuild of
// rows whose values stood, reads two short index ranges and writes nothing.
//
// The writers are the refresher's writes of a project's day (writeProjectDay), its unowned-p0 row
// (WriteUnownedP0Day) and its logged zero fill (fillLoggedZeros), and the readings write
// (WriteReadings); the walks fold the days they wrote at the end of their batch (refresh.walk),
// and the refresher's pass folds the ranges of each new end day and a new build's whole set
// (refresh.walkFigures), and again the days whose rows a build with no figures wrote
// (refresh.figureChanges). Every build that holds this code writes them, as every build writes the
// day rows, and each row records the build that folded it (built_by): a read serves a stored row
// whichever build folded it, as it serves a day row whichever build built it, and a build whose
// folds differ rewrites the rows an older build folded once its whole walk reaches them, writing
// those whose figures move and no other (figures_refresher.go says what a read sees meanwhile).

// FigureLengths is every range length the figures are stored for, ascending: each preset of the
// Evals tab (7, 14, 28 and 84 days) and each one day longer. A read from midnight to midnight
// covers L days ending today; the live view reads a preset from L days before the moment it
// reads to that moment, which covers L+1 days ending tomorrow at any moment but a midnight (the
// read's days, api/evals.go's evalDaysOf).
var FigureLengths = []int{7, 8, 14, 15, 28, 29, 84, 85}

// figureSpan is the longest of FigureLengths: no stored range holds a day more than figureSpan
// days before its end.
const figureSpan = 85

// FigureLead is how many days after today the last stored end day lies, the day a project's
// figures stand built through once its walk is done (FiguresThrough). Just after a midnight the
// live view's window ends the new tomorrow, two days after the day before's today, and a read can
// come before the pass of the new day has written the ranges ending then, so the pass of the day
// before writes them already: their last day holds no row yet, and a fold divides by a range's
// days, not by its rows.
const FigureLead = 2

// foldKeyOf is the fingerprint of what entry's definition folds its figures by (fold_key): the
// eval's id and source, and each series' name, fold, listing and role, in name order. A register
// write that moves one of them moves it, and a read whose eval no longer has the key a stored row
// carries reads the day rows instead. The build is no part of it: two builds whose folds agree fold
// one figure, which neither then writes again.
func foldKeyOf(entry Entry) int64 {
	hash := fnv.New64a()
	write := func(value string) {
		var length [binary.MaxVarintLen64]byte
		hash.Write(length[:binary.PutUvarint(length[:], uint64(len(value)))])
		hash.Write([]byte(value))
	}
	write(entry.Definition.EvalID)
	write(entry.Definition.Source)
	series := slices.SortedFunc(slices.Values(entry.Series), func(a, b Series) int { return strings.Compare(a.Series, b.Series) })
	for _, one := range series {
		write(one.Series)
		write(one.Agg)
		write(strconv.FormatBool(one.Listed))
		write(one.Role)
	}
	return int64(hash.Sum64())
}

// listedAndHeadline is entry's listed series, and those of them that are headline series, in
// entry's order: the series a range's figures and a day's figures fold (SeriesValuesOf).
func listedAndHeadline(entry Entry) (listed, headline []Series) {
	for _, series := range entry.Series {
		if !series.Listed {
			continue
		}
		listed = append(listed, series)
		if series.Role == RoleHeadline {
			headline = append(headline, series)
		}
	}
	return listed, headline
}

// encodeFigures is a fold's map and its via-repository counts as a figures column holds them:
// the count of series, then each series by name (its name, and its statistics: none, or their
// count and each by name, its name and either no value or the 64 bits of its value), then the
// count of series whose via-repository count is not 0 and each by name with its count. Every
// figure is its float's 64 bits, so the fold is stored bit for bit, -0 included.
func encodeFigures(folded map[string]Statistics, via map[string]int) []byte {
	b := make([]byte, 0, 256)
	appendString := func(value string) {
		b = binary.AppendUvarint(b, uint64(len(value)))
		b = append(b, value...)
	}
	names := slices.Sorted(maps.Keys(folded))
	b = binary.AppendUvarint(b, uint64(len(names)))
	for _, name := range names {
		appendString(name)
		stats := folded[name]
		if stats == nil {
			b = append(b, 0)
			continue
		}
		b = append(b, 1)
		statistics := slices.Sorted(maps.Keys(stats))
		b = binary.AppendUvarint(b, uint64(len(statistics)))
		for _, statistic := range statistics {
			appendString(statistic)
			if value := stats[statistic]; value == nil {
				b = append(b, 0)
			} else {
				b = append(b, 1)
				b = binary.LittleEndian.AppendUint64(b, math.Float64bits(*value))
			}
		}
	}
	var counted []string
	for name, count := range via {
		if count != 0 {
			counted = append(counted, name)
		}
	}
	slices.Sort(counted)
	b = binary.AppendUvarint(b, uint64(len(counted)))
	for _, name := range counted {
		appendString(name)
		b = binary.AppendVarint(b, int64(via[name]))
	}
	return b
}

// errFiguresEncoding is a figures column encodeFigures did not write.
var errFiguresEncoding = errors.New("evals: a stored figures column is not one encodeFigures writes")

// figuresDecoder reads a figures column from its front.
type figuresDecoder struct {
	b   []byte
	err error
}

func (d *figuresDecoder) uvarint() uint64 {
	value, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.err, d.b = errFiguresEncoding, nil
		return 0
	}
	d.b = d.b[n:]
	return value
}

func (d *figuresDecoder) varint() int64 {
	value, n := binary.Varint(d.b)
	if n <= 0 {
		d.err, d.b = errFiguresEncoding, nil
		return 0
	}
	d.b = d.b[n:]
	return value
}

func (d *figuresDecoder) flag() bool {
	if len(d.b) == 0 {
		d.err = errFiguresEncoding
		return false
	}
	set := d.b[0] == 1
	d.b = d.b[1:]
	return set
}

func (d *figuresDecoder) text() string {
	n := d.uvarint()
	if uint64(len(d.b)) < n {
		d.err, d.b = errFiguresEncoding, nil
		return ""
	}
	value := string(d.b[:n])
	d.b = d.b[n:]
	return value
}

func (d *figuresDecoder) float() float64 {
	if len(d.b) < 8 {
		d.err, d.b = errFiguresEncoding, nil
		return 0
	}
	value := math.Float64frombits(binary.LittleEndian.Uint64(d.b))
	d.b = d.b[8:]
	return value
}

// decodeFigures is the fold's map and the via-repository counts encodeFigures wrote into b.
func decodeFigures(b []byte) (map[string]Statistics, map[string]int, error) {
	d := figuresDecoder{b: b}
	count := d.uvarint()
	folded := map[string]Statistics{}
	for range count {
		if d.err != nil {
			break
		}
		name := d.text()
		if !d.flag() {
			folded[name] = nil
			continue
		}
		statistics := d.uvarint()
		stats := Statistics{}
		for range statistics {
			if d.err != nil {
				break
			}
			statistic := d.text()
			if !d.flag() {
				stats[statistic] = nil
				continue
			}
			value := d.float()
			stats[statistic] = &value
		}
		folded[name] = stats
	}
	via := map[string]int{}
	for range d.uvarint() {
		if d.err != nil {
			break
		}
		name := d.text()
		via[name] = int(d.varint())
	}
	if d.err == nil && len(d.b) > 0 {
		d.err = errFiguresEncoding
	}
	if d.err != nil {
		return nil, nil, d.err
	}
	return folded, via, nil
}

// stampedRow is what a day's stamp reads of one of its rows: its series and its updated_at.
type stampedRow struct {
	series    string
	updatedAt time.Time
}

// rowsStamp is the fingerprint of one eval's listed rows of one repository and day
// (eval_day_figures.rows_stamp): each row's series and updated_at to the microsecond Postgres
// keeps, in series order, which it sorts rows into. Every write that moves a row's value moves
// its updated_at (upsertDays, zeroStaleDays, writeDays, relistDays), and one that moves nothing,
// such as a new build's rebuild of rows whose values stood, leaves it, so the stamp moves exactly
// when the day's rows do, whichever build wrote them.
func rowsStamp(rows []stampedRow) int64 {
	slices.SortFunc(rows, func(a, b stampedRow) int { return strings.Compare(a.series, b.series) })
	hash := fnv.New64a()
	var word [binary.MaxVarintLen64]byte
	for _, row := range rows {
		hash.Write(word[:binary.PutUvarint(word[:], uint64(len(row.series)))])
		hash.Write([]byte(row.series))
		hash.Write(binary.LittleEndian.AppendUint64(word[:0], uint64(row.updatedAt.UnixMicro())))
	}
	return int64(hash.Sum64())
}

// figureGroup is one eval's listed day rows of one repository, in day order, and what folds
// them: the stored figures of every range and day of the rows are folded from it.
type figureGroup struct {
	entry            Entry
	listed, headline []Series
	key              int64
	rows             []WindowRow
	parts            []measures.DayPart
}

// newFigureGroup is the group of rows, one eval's listed rows of one repository as DayWindow reads
// them, whose figures fold under key. It sorts rows by day, keeping the read's order within a day,
// the order SeriesValuesOf's inputs are folded in too, so a range's figures are the bits a read
// folds. Each series whose every row holds samples a sort keeps bit for bit (SortableSamples) has
// each row's samples sorted in place, once, so every range of the series reads its ranks across
// them (measures.MergedSamples, which finds them sorted); a series with a -0 or a NaN keeps its
// rows' samples in the order they were stored, which the merge and sort of each range reads as a
// read of the rows does, in the same order.
func newFigureGroup(entry Entry, key int64, rows []WindowRow) *figureGroup {
	slices.SortStableFunc(rows, func(a, b WindowRow) int { return a.Part.Day.Compare(b.Part.Day) })
	group := &figureGroup{entry: entry, key: key, rows: rows, parts: make([]measures.DayPart, len(rows))}
	group.listed, group.headline = listedAndHeadline(entry)
	sortable := map[string]bool{}
	for _, row := range rows {
		held, seen := sortable[row.Part.Series]
		sortable[row.Part.Series] = (held || !seen) && measures.SortableSamples(row.Part.Samples)
	}
	for index, row := range rows {
		if sortable[row.Part.Series] {
			slices.Sort(row.Part.Samples)
		}
		group.parts[index] = row.Part
	}
	return group
}

// span is the index range of the group's rows of the days [from, to).
func (g *figureGroup) span(from, to time.Time) (int, int) {
	first := sort.Search(len(g.rows), func(k int) bool { return !g.rows[k].Part.Day.Before(from) })
	last := sort.Search(len(g.rows), func(k int) bool { return !g.rows[k].Part.Day.Before(to) })
	return first, last
}

// rangeOf is the n days ending at end, exclusive.
func rangeOf(n int, end time.Time) Range {
	return Range{From: end.AddDate(0, 0, -n), To: end}
}

// rangeFigure is the stored row of the n days ending at end: its figures and the oldest updated_at
// of the rows it folds, and false where the range holds no row of the group.
func (g *figureGroup) rangeFigure(n int, end time.Time) ([]byte, time.Time, bool) {
	r := rangeOf(n, end)
	first, last := g.span(r.From, r.To)
	if first == last {
		return nil, time.Time{}, false
	}
	folded := FoldStatistics(g.entry.Definition, g.listed, g.parts[first:last], r)
	via := map[string]int{}
	oldest := g.rows[first].UpdatedAt
	for index := first; index < last; index++ {
		via[g.parts[index].Series] += g.parts[index].ViaRepository
		if g.rows[index].UpdatedAt.Before(oldest) {
			oldest = g.rows[index].UpdatedAt
		}
	}
	return encodeFigures(folded, via), oldest, true
}

// dayFigure is the stored day row of day: the figures of each headline series that holds a row
// that day, each folded over the day's rows alone as SeriesValuesOf folds a day of its per-day
// array, and the stamp of the day's rows; false where the day holds no row of the group.
func (g *figureGroup) dayFigure(day time.Time) ([]byte, int64, bool) {
	first, last := g.span(day, day.AddDate(0, 0, 1))
	if first == last {
		return nil, 0, false
	}
	stamped := make([]stampedRow, 0, last-first)
	for _, row := range g.rows[first:last] {
		stamped = append(stamped, stampedRow{series: row.Part.Series, updatedAt: row.UpdatedAt})
	}
	kept := map[string]Statistics{}
	if len(g.headline) > 0 {
		parts := g.parts[first:last]
		folded := FoldStatistics(g.entry.Definition, g.headline, parts, Range{From: day, To: day.AddDate(0, 0, 1)})
		for _, series := range g.headline {
			if holds(parts, series.Series) {
				kept[series.Series] = folded[series.Series]
			}
		}
	}
	return encodeFigures(kept, nil), rowsStamp(stamped), true
}

// figureScope is what one write of a project's stored figures writes under: the build that folds
// them, which each row it writes records (built_by), and through, the last end day any range of the
// project's figures has been folded through: the end day eval_figures_built says they are built
// through, or the farthest any build's figures walk has come (refresh.walkFigures), whichever is
// later. Every range ending on or before it may be stored, whichever build folded it, so every
// write keeps each of them true to its rows; no range ending after it is written, nor the day row
// of a day on or after it: those are the walks' to fold, from the rows that stand when they do.
type figureScope struct {
	build   string
	project string
	through time.Time
}

// readFigureScope reads, in q, how far project's stored figures are folded (figureScope), for a
// write of them by build. ok is false where no build's figures walk of the project has begun and
// none has built them, and a write of the day rows then writes no figure: the walk, once it
// begins, folds them from the rows as they then stand.
func readFigureScope(ctx context.Context, q delivery.Querier, build, project string) (figureScope, bool, error) {
	var through *time.Time
	if err := q.QueryRow(ctx, `
		select greatest(
			(select through from eval_figures_built where project_key = $2),
			(select max(next_day) - 1 from eval_days_walks where family = $1 and project_key = $2))
	`, walkFigures, project).Scan(&through); err != nil {
		return figureScope{}, false, fmt.Errorf("read how far the stored figures of %s are folded: %w", project, err)
	}
	if through == nil {
		return figureScope{}, false, nil
	}
	return figureScope{build: build, project: project, through: measures.Midnight(*through)}, true, nil
}

// readEntries reads, in q, project's evals as Current reads each, those of ids where ids is not
// nil: the newest definition of each and the newest version of each of its series.
func readEntries(ctx context.Context, q delivery.Querier, project string, ids []string) ([]Entry, error) {
	filter, args := `project_key = $1`, []any{project}
	if ids != nil {
		filter, args = filter+` and eval_id = any($2)`, append(args, ids)
	}
	rows, err := q.Query(ctx, `select `+definitionColumns+` from evals_current where `+filter+` order by slug collate "C"`, args...)
	if err != nil {
		return nil, fmt.Errorf("read the evals of %s: %w", project, err)
	}
	definitions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Definition, error) { return scanDefinition(row) })
	if err != nil {
		return nil, fmt.Errorf("read the evals of %s: %w", project, err)
	}
	entries := make([]Entry, len(definitions))
	position := make(map[string]int, len(definitions))
	evalIDs := make([]string, len(definitions))
	for index, definition := range definitions {
		entries[index] = Entry{Definition: definition, Series: []Series{}}
		position[definition.EvalID], evalIDs[index] = index, definition.EvalID
	}
	if len(evalIDs) == 0 {
		return entries, nil
	}
	rows, err = q.Query(ctx, `
		select `+seriesColumns+` from eval_series_current where eval_id = any($1)
		order by eval_id, is_primary desc, series
	`, evalIDs)
	if err != nil {
		return nil, fmt.Errorf("read the series of the evals of %s: %w", project, err)
	}
	series, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Series, error) { return scanSeries(row) })
	if err != nil {
		return nil, fmt.Errorf("read the series of the evals of %s: %w", project, err)
	}
	for _, one := range series {
		entry := &entries[position[one.EvalID]]
		entry.Series = append(entry.Series, one)
	}
	return entries, nil
}

// daySpan is the days first to last of one eval, both included, whose figures a write may have
// moved.
type daySpan struct {
	first, last time.Time
}

// widen is s widened to hold day; the zero span holds day alone.
func (s daySpan) widen(day time.Time) daySpan {
	if s.first.IsZero() || day.Before(s.first) {
		s.first = day
	}
	if s.last.IsZero() || day.After(s.last) {
		s.last = day
	}
	return s
}

// figureGroupKey is one eval's rows of one repository.
type figureGroupKey struct {
	eval, repo string
}

// movedFigureDays reads, in q, which days of spans (by eval id, each eval one of entries) hold
// figures that no longer fold their rows: each eval, repository and day whose listed rows' stamp
// (rowsStamp) is not the one the day's stored row recorded, which holds rows and no stored row or a
// stored row and no rows, or whose stored row was folded under another fold key than the eval's
// definition now has (a register write). Which build folded a stored row is no part of the test: a
// figure whose rows stand is the fold every build of these folds answers. A day on or after
// scope.through is the figures walks', and none of it is read.
func movedFigureDays(ctx context.Context, q delivery.Querier, scope figureScope, entries map[string]Entry, spans map[string]daySpan) (map[figureGroupKey]map[time.Time]bool, error) {
	var evalIDs, writers []string
	var firsts, lasts []time.Time
	for _, evalID := range slices.Sorted(maps.Keys(spans)) {
		entry, held := entries[evalID]
		span := spans[evalID]
		last := span.last
		if before := scope.through.AddDate(0, 0, -1); last.After(before) {
			last = before
		}
		if !held || span.first.After(last) {
			continue
		}
		evalIDs, writers = append(evalIDs, evalID), append(writers, entry.Definition.writer())
		firsts, lasts = append(firsts, span.first), append(lasts, last)
	}
	moved := map[figureGroupKey]map[time.Time]bool{}
	if len(evalIDs) == 0 {
		return moved, nil
	}
	type dayKey struct {
		group figureGroupKey
		day   time.Time
	}
	rows, err := q.Query(ctx, `
		select given.eval_id, d.repo, d.day, d.series, d.updated_at
		from unnest($2::text[], $3::text[], $4::date[], $5::date[]) as given(eval_id, writer, first_day, last_day)
		join eval_days d on d.project_key = $1 and d.eval_id = given.eval_id and d.writer = given.writer
			and d.day >= given.first_day and d.day <= given.last_day and d.listed
	`, scope.project, evalIDs, writers, firsts, lasts)
	if err != nil {
		return nil, fmt.Errorf("read the stamps of %s's day rows: %w", scope.project, err)
	}
	stamped := map[dayKey][]stampedRow{}
	var key dayKey
	var row stampedRow
	if _, err := pgx.ForEachRow(rows, []any{&key.group.eval, &key.group.repo, &key.day, &row.series, &row.updatedAt}, func() error {
		key.day = key.day.UTC()
		stamped[key] = append(stamped[key], row)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read the stamps of %s's day rows: %w", scope.project, err)
	}
	rows, err = q.Query(ctx, `
		select f.eval_id, f.repo, f.day, f.fold_key, f.rows_stamp
		from unnest($2::text[], $3::date[], $4::date[]) as given(eval_id, first_day, last_day)
		join eval_day_figures f on f.project_key = $1 and f.eval_id = given.eval_id
			and f.day >= given.first_day and f.day <= given.last_day
	`, scope.project, evalIDs, firsts, lasts)
	if err != nil {
		return nil, fmt.Errorf("read the stored day figures of %s: %w", scope.project, err)
	}
	type storedDay struct{ key, stamp int64 }
	stored := map[dayKey]storedDay{}
	var day storedDay
	if _, err := pgx.ForEachRow(rows, []any{&key.group.eval, &key.group.repo, &key.day, &day.key, &day.stamp}, func() error {
		key.day = key.day.UTC()
		stored[key] = day
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read the stored day figures of %s: %w", scope.project, err)
	}
	keys := map[string]int64{}
	note := func(at dayKey) {
		if moved[at.group] == nil {
			moved[at.group] = map[time.Time]bool{}
		}
		moved[at.group][at.day] = true
	}
	for at, held := range stamped {
		if _, done := keys[at.group.eval]; !done {
			keys[at.group.eval] = foldKeyOf(entries[at.group.eval])
		}
		if figure, found := stored[at]; !found || figure.key != keys[at.group.eval] || figure.stamp != rowsStamp(held) {
			note(at)
		}
	}
	for at := range stored {
		if _, found := stamped[at]; !found {
			note(at)
		}
	}
	return moved, nil
}

// figureWrites is what one fold writes of a project's stored figures: the range and day rows to
// upsert where they moved, and the ranges and days that hold no row any more, whose stored rows
// it deletes.
type figureWrites struct {
	ranges     []rangeFigureRow
	goneRanges []rangeFigureKey
	days       []dayFigureRow
	goneDays   []dayFigureKey
}

// rangeFigureKey is one range row's key within its project; rangeFigureRow the row.
type rangeFigureKey struct {
	repo string
	days int
	end  time.Time
	eval string
}

type rangeFigureRow struct {
	rangeFigureKey
	key     int64
	figures []byte
	oldest  time.Time
}

// dayFigureKey is one day row's key within its project; dayFigureRow the row.
type dayFigureKey struct {
	repo string
	day  time.Time
	eval string
}

type dayFigureRow struct {
	dayFigureKey
	key     int64
	figures []byte
	stamp   int64
}

// figureRepos is the repositories of moved's groups, in byte order.
func figureRepos(moved map[figureGroupKey]map[time.Time]bool) []string {
	repos := map[string]bool{}
	for group := range moved {
		repos[group.repo] = true
	}
	return slices.Sorted(maps.Keys(repos))
}

// foldMovedDays folds, in q, the stored figures of each day moved names (by eval and repository)
// and of every range whose span holds one, up to scope.through: one read of each repository's rows
// of the moved evals over every day those ranges span, then each range and day folded once,
// however many moved days it holds.
func foldMovedDays(ctx context.Context, q delivery.Querier, scope figureScope, entries map[string]Entry, moved map[figureGroupKey]map[time.Time]bool) (figureWrites, error) {
	var writes figureWrites
	for _, repo := range figureRepos(moved) {
		var evalIDs []string
		var first, last time.Time
		for group, days := range moved {
			if group.repo != repo {
				continue
			}
			evalIDs = append(evalIDs, group.eval)
			for day := range days {
				if first.IsZero() || day.Before(first) {
					first = day
				}
				if day.After(last) {
					last = day
				}
			}
		}
		slices.Sort(evalIDs)
		read := Range{From: first.AddDate(0, 0, 1-figureSpan), To: last.AddDate(0, 0, figureSpan)}
		if read.To.After(scope.through) {
			read.To = scope.through
		}
		groups, err := readFigureGroups(ctx, q, scope, entries, evalIDs, repo, read)
		if err != nil {
			return figureWrites{}, err
		}
		for _, evalID := range evalIDs {
			group := groups[evalID]
			targets := map[rangeFigureKey]bool{}
			days := slices.SortedFunc(maps.Keys(moved[figureGroupKey{eval: evalID, repo: repo}]), time.Time.Compare)
			for _, day := range days {
				for _, n := range FigureLengths {
					for end := day.AddDate(0, 0, 1); !end.After(day.AddDate(0, 0, n)) && !end.After(scope.through); end = end.AddDate(0, 0, 1) {
						targets[rangeFigureKey{repo: repo, days: n, end: end, eval: evalID}] = true
					}
				}
			}
			for target := range targets {
				writes.addRange(group, target)
			}
			for _, day := range days {
				writes.addDay(group, dayFigureKey{repo: repo, day: day, eval: evalID})
			}
		}
	}
	return writes, nil
}

// addRange adds the stored row of target, a range of group's rows, or its deletion where it holds
// none (group nil: the eval holds no row the read spans).
func (w *figureWrites) addRange(group *figureGroup, target rangeFigureKey) {
	if group != nil {
		if figures, oldest, held := group.rangeFigure(target.days, target.end); held {
			w.ranges = append(w.ranges, rangeFigureRow{rangeFigureKey: target, key: group.key, figures: figures, oldest: oldest})
			return
		}
	}
	w.goneRanges = append(w.goneRanges, target)
}

// addDay adds the stored day row of target, a day of group's rows, or its deletion where the day
// holds none.
func (w *figureWrites) addDay(group *figureGroup, target dayFigureKey) {
	if group != nil {
		if figures, stamp, held := group.dayFigure(target.day); held {
			w.days = append(w.days, dayFigureRow{dayFigureKey: target, key: group.key, figures: figures, stamp: stamp})
			return
		}
	}
	w.goneDays = append(w.goneDays, target)
}

// readFigureGroups reads, in q, the listed rows of repo (the empty string for the project's own) of
// each eval of evalIDs over r, as DayWindow reads a single repository's, and answers each eval's
// group, none for an eval with no row there. An eval none of whose series is listed any more, whose
// rows a register write has not relisted yet, still has a group of the rows DayWindow reads, whose
// figures fold no series: so a day that holds a listed row always holds its stored row, the one a
// read of the stamps compares (movedFigureDays).
func readFigureGroups(ctx context.Context, q delivery.Querier, scope figureScope, entries map[string]Entry, evalIDs []string, repo string, r Range) (map[string]*figureGroup, error) {
	groups := map[string]*figureGroup{}
	var read []Entry
	for _, evalID := range evalIDs {
		if entry, held := entries[evalID]; held {
			read = append(read, entry)
		}
	}
	if len(read) == 0 || !r.From.Before(r.To) {
		return groups, nil
	}
	var repos []string
	if repo != "" {
		repos = []string{repo}
	}
	rows, err := dayWindow(ctx, q, []string{scope.project}, read, repos, r, nil)
	if err != nil {
		return nil, err
	}
	byEval := map[string][]WindowRow{}
	for _, row := range rows {
		byEval[row.EvalID] = append(byEval[row.EvalID], row)
	}
	for evalID, held := range byEval {
		entry := entries[evalID]
		groups[evalID] = newFigureGroup(entry, foldKeyOf(entry), held)
	}
	return groups, nil
}

// refoldFigures folds again, in tx, the stored figures of every day of spans (by eval id: the days
// a write moved, or may have) whose rows moved since their figures were folded (movedFigureDays),
// and of every range that holds one, and writes them where they moved (writeFigures). entries is
// the project's evals by id, every eval spans names among them.
func refoldFigures(ctx context.Context, tx pgx.Tx, scope figureScope, entries map[string]Entry, spans map[string]daySpan) error {
	moved, err := movedFigureDays(ctx, tx, scope, entries, spans)
	if err != nil || len(moved) == 0 {
		return err
	}
	writes, err := foldMovedDays(ctx, tx, scope, entries, moved)
	if err != nil {
		return err
	}
	return writeFigures(ctx, tx, scope, writes)
}

// foldFiguresInTx is the stored figures' part of a write of project's day rows by build, in tx, the
// write's own transaction, made after its rows: where any of the project's figures are folded
// (readFigureScope), it folds again the figures of the days of spans (by eval id) whose rows moved,
// and of every range that holds one (refoldFigures).
func foldFiguresInTx(ctx context.Context, tx pgx.Tx, build, project string, spans map[string]daySpan) error {
	if len(spans) == 0 {
		return nil
	}
	scope, ok, err := readFigureScope(ctx, tx, build, project)
	if err != nil || !ok {
		return err
	}
	entries, err := readEntries(ctx, tx, project, slices.Sorted(maps.Keys(spans)))
	if err != nil {
		return err
	}
	return refoldFigures(ctx, tx, scope, entriesByID(entries), spans)
}

// entriesByID is entries by eval id.
func entriesByID(entries []Entry) map[string]Entry {
	byID := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		byID[entry.Definition.EvalID] = entry
	}
	return byID
}

// figureChunk is the most rows one statement of writeFigures sends: its arrays are its
// parameters, and a whole build's batch of a large project holds tens of thousands of rows.
const figureChunk = 5000

// writeFigures writes w's rows of scope's project's stored figures in tx, each recording
// scope.build as the build that folded it: each range and day row upserted only where its fold key,
// figures, oldest updated_at or stamp moved, so a fold that moved nothing writes nothing (a row
// another build folded to the same figures keeps its built_by, as the build that folded those
// figures), and each gone range and day deleted. Every statement takes its rows in the byte order of
// their keys, as every writer of them does, so two writers of overlapping rows wait on each other
// rather than deadlock.
func writeFigures(ctx context.Context, tx pgx.Tx, scope figureScope, w figureWrites) error {
	project := scope.project
	slices.SortFunc(w.ranges, func(a, b rangeFigureRow) int { return compareRangeKeys(a.rangeFigureKey, b.rangeFigureKey) })
	slices.SortFunc(w.goneRanges, compareRangeKeys)
	slices.SortFunc(w.days, func(a, b dayFigureRow) int { return compareDayFigureKeys(a.dayFigureKey, b.dayFigureKey) })
	slices.SortFunc(w.goneDays, compareDayFigureKeys)
	for chunk := range slices.Chunk(w.ranges, figureChunk) {
		repos, ns, ends, evalIDs := make([]string, len(chunk)), make([]int32, len(chunk)), make([]time.Time, len(chunk)), make([]string, len(chunk))
		keys, figures, oldest := make([]int64, len(chunk)), make([][]byte, len(chunk)), make([]time.Time, len(chunk))
		for index, row := range chunk {
			repos[index], ns[index], ends[index], evalIDs[index] = row.repo, int32(row.days), row.end, row.eval
			keys[index], figures[index], oldest[index] = row.key, row.figures, row.oldest
		}
		if _, err := tx.Exec(ctx, `
			insert into eval_range_figures (project_key, repo, days, end_day, eval_id, fold_key, figures, oldest_updated_at, built_by)
			select $1, given.repo, given.days, given.end_day, given.eval_id, given.fold_key, given.figures, given.oldest, $9
			from unnest($2::text[], $3::int[], $4::date[], $5::text[], $6::bigint[], $7::bytea[], $8::timestamptz[])
				as given(repo, days, end_day, eval_id, fold_key, figures, oldest)
			order by given.repo collate "C", given.days, given.end_day, given.eval_id collate "C"
			on conflict (project_key, repo, days, end_day, eval_id) do update
			set fold_key = excluded.fold_key, figures = excluded.figures, oldest_updated_at = excluded.oldest_updated_at,
				built_by = excluded.built_by
			where (eval_range_figures.fold_key, eval_range_figures.figures, eval_range_figures.oldest_updated_at)
				is distinct from (excluded.fold_key, excluded.figures, excluded.oldest_updated_at)
		`, project, repos, ns, ends, evalIDs, keys, figures, oldest, scope.build); err != nil {
			return fmt.Errorf("write the stored range figures of %s: %w", project, err)
		}
	}
	for chunk := range slices.Chunk(w.goneRanges, figureChunk) {
		repos, ns, ends, evalIDs := make([]string, len(chunk)), make([]int32, len(chunk)), make([]time.Time, len(chunk)), make([]string, len(chunk))
		for index, row := range chunk {
			repos[index], ns[index], ends[index], evalIDs[index] = row.repo, int32(row.days), row.end, row.eval
		}
		if _, err := tx.Exec(ctx, `
			delete from eval_range_figures f
			using unnest($2::text[], $3::int[], $4::date[], $5::text[]) as gone(repo, days, end_day, eval_id)
			where f.project_key = $1 and f.repo = gone.repo and f.days = gone.days and f.end_day = gone.end_day
				and f.eval_id = gone.eval_id
		`, project, repos, ns, ends, evalIDs); err != nil {
			return fmt.Errorf("delete the stored range figures of %s that hold no row: %w", project, err)
		}
	}
	for chunk := range slices.Chunk(w.days, figureChunk) {
		repos, days, evalIDs := make([]string, len(chunk)), make([]time.Time, len(chunk)), make([]string, len(chunk))
		keys, figures, stamps := make([]int64, len(chunk)), make([][]byte, len(chunk)), make([]int64, len(chunk))
		for index, row := range chunk {
			repos[index], days[index], evalIDs[index] = row.repo, row.day, row.eval
			keys[index], figures[index], stamps[index] = row.key, row.figures, row.stamp
		}
		if _, err := tx.Exec(ctx, `
			insert into eval_day_figures (project_key, repo, day, eval_id, fold_key, figures, rows_stamp, built_by)
			select $1, given.repo, given.day, given.eval_id, given.fold_key, given.figures, given.rows_stamp, $8
			from unnest($2::text[], $3::date[], $4::text[], $5::bigint[], $6::bytea[], $7::bigint[])
				as given(repo, day, eval_id, fold_key, figures, rows_stamp)
			order by given.repo collate "C", given.day, given.eval_id collate "C"
			on conflict (project_key, repo, day, eval_id) do update
			set fold_key = excluded.fold_key, figures = excluded.figures, rows_stamp = excluded.rows_stamp, built_by = excluded.built_by
			where (eval_day_figures.fold_key, eval_day_figures.figures, eval_day_figures.rows_stamp)
				is distinct from (excluded.fold_key, excluded.figures, excluded.rows_stamp)
		`, project, repos, days, evalIDs, keys, figures, stamps, scope.build); err != nil {
			return fmt.Errorf("write the stored day figures of %s: %w", project, err)
		}
	}
	for chunk := range slices.Chunk(w.goneDays, figureChunk) {
		repos, days, evalIDs := make([]string, len(chunk)), make([]time.Time, len(chunk)), make([]string, len(chunk))
		for index, row := range chunk {
			repos[index], days[index], evalIDs[index] = row.repo, row.day, row.eval
		}
		if _, err := tx.Exec(ctx, `
			delete from eval_day_figures f
			using unnest($2::text[], $3::date[], $4::text[]) as gone(repo, day, eval_id)
			where f.project_key = $1 and f.repo = gone.repo and f.day = gone.day and f.eval_id = gone.eval_id
		`, project, repos, days, evalIDs); err != nil {
			return fmt.Errorf("delete the stored day figures of %s that hold no row: %w", project, err)
		}
	}
	return nil
}

// compareRangeKeys orders range rows as eval_range_figures' primary key does within a project,
// each string by its bytes.
func compareRangeKeys(a, b rangeFigureKey) int {
	if c := strings.Compare(a.repo, b.repo); c != 0 {
		return c
	}
	if a.days != b.days {
		return a.days - b.days
	}
	if c := a.end.Compare(b.end); c != 0 {
		return c
	}
	return strings.Compare(a.eval, b.eval)
}

// compareDayFigureKeys orders day rows as eval_day_figures' primary key does within a project.
func compareDayFigureKeys(a, b dayFigureKey) int {
	if c := strings.Compare(a.repo, b.repo); c != 0 {
		return c
	}
	if c := a.day.Compare(b.day); c != 0 {
		return c
	}
	return strings.Compare(a.eval, b.eval)
}

// buildFigureEnds folds, in tx, every stored figure of project whose range ends on a day from
// first through last, and the day row of every day before one of those end days, of each eval of
// entries in every repository it holds rows of, and writes them where they moved (writeFigures):
// one read of each repository's rows over the days those ranges span. Every stored row of those
// end days and days that no row holds any more (one an earlier build folded, of an eval or a
// repository that holds none now) is deleted. It is a batch of the figures walk, and the whole of
// a new end day's ranges once the walk stands at today.
func buildFigureEnds(ctx context.Context, tx pgx.Tx, scope figureScope, entries map[string]Entry, first, last time.Time) error {
	read := Range{From: first.AddDate(0, 0, -figureSpan), To: last}
	repoRows, err := tx.Query(ctx, `
		select distinct repo from eval_days where project_key = $1 and listed and day >= $2 and day < $3
	`, scope.project, read.From, read.To)
	if err != nil {
		return fmt.Errorf("read the repositories of %s's day rows: %w", scope.project, err)
	}
	repos, err := pgx.CollectRows(repoRows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read the repositories of %s's day rows: %w", scope.project, err)
	}
	slices.Sort(repos)
	evalIDs := slices.Sorted(maps.Keys(entries))
	var writes figureWrites
	built := map[rangeFigureKey]bool{}
	builtDays := map[dayFigureKey]bool{}
	for _, repo := range repos {
		groups, err := readFigureGroups(ctx, tx, scope, entries, evalIDs, repo, read)
		if err != nil {
			return err
		}
		for _, evalID := range slices.Sorted(maps.Keys(groups)) {
			group := groups[evalID]
			for end := first; !end.After(last); end = end.AddDate(0, 0, 1) {
				for _, n := range FigureLengths {
					target := rangeFigureKey{repo: repo, days: n, end: end, eval: evalID}
					if figures, oldest, held := group.rangeFigure(n, end); held {
						writes.ranges = append(writes.ranges, rangeFigureRow{rangeFigureKey: target, key: group.key, figures: figures, oldest: oldest})
						built[target] = true
					}
				}
				day := dayFigureKey{repo: repo, day: end.AddDate(0, 0, -1), eval: evalID}
				if figures, stamp, held := group.dayFigure(day.day); held {
					writes.days = append(writes.days, dayFigureRow{dayFigureKey: day, key: group.key, figures: figures, stamp: stamp})
					builtDays[day] = true
				}
			}
		}
	}
	rows, err := tx.Query(ctx, `
		select repo, days, end_day, eval_id from eval_range_figures where project_key = $1 and end_day >= $2 and end_day <= $3
	`, scope.project, first, last)
	if err != nil {
		return fmt.Errorf("read %s's stored range figures ending %s to %s: %w", scope.project, first.Format(time.DateOnly), last.Format(time.DateOnly), err)
	}
	var stored rangeFigureKey
	var n int32
	if _, err := pgx.ForEachRow(rows, []any{&stored.repo, &n, &stored.end, &stored.eval}, func() error {
		stored.days, stored.end = int(n), stored.end.UTC()
		if !built[stored] {
			writes.goneRanges = append(writes.goneRanges, stored)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("read %s's stored range figures ending %s to %s: %w", scope.project, first.Format(time.DateOnly), last.Format(time.DateOnly), err)
	}
	rows, err = tx.Query(ctx, `
		select repo, day, eval_id from eval_day_figures where project_key = $1 and day >= $2 and day < $3
	`, scope.project, first.AddDate(0, 0, -1), last)
	if err != nil {
		return fmt.Errorf("read %s's stored day figures: %w", scope.project, err)
	}
	var storedDay dayFigureKey
	if _, err := pgx.ForEachRow(rows, []any{&storedDay.repo, &storedDay.day, &storedDay.eval}, func() error {
		storedDay.day = storedDay.day.UTC()
		if !builtDays[storedDay] {
			writes.goneDays = append(writes.goneDays, storedDay)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("read %s's stored day figures: %w", scope.project, err)
	}
	return writeFigures(ctx, tx, scope, writes)
}
