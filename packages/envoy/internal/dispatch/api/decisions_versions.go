package api

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// markKeptAcrossRenderers decides waiting_review for each agent decision whose keep holds another
// sum than its text's. A keep holds the sum of the text it agreed with and the version the
// decision then stood at (decision_keeps), not the text, and that version's stored markdown holds
// the text. A renderer change (pmdoc.RendererVersion) leaves every version stored before it in its
// renderer's spelling of the document, so a decision kept before the change sums otherwise in this
// build's spelling though nobody changed it. Such a decision is kept when another renderer
// version stored the version its keep names, or none stamped it (pmdoc.OtherRenderer), that
// version's text of the decision (decisionCuts, named by its sectionKey) has the keep's sum,
// which makes it the text the keep agreed with, the decision stood in no container there and
// stands in none now, and this build reads that text, alone, as the decision's text now
// (pmdoc.FragmentReadsAs). alone[i] is whether decision i's heading block stands in no other
// block of the document now (decisionSection.alone), and decisionCut.alone whether the kept text
// stood in none: a decision's text read alone loses the list item, quote, footnote definition or
// typed block it stands in, so two trees can read as one. The decision waits for review
// otherwise: a version that holds no such text - the decision's heading changed since, or a block
// left the decision after that version - a decision inside a container then or now, kept text
// that reads otherwise alone, and kept text or text now that holds `[^`, which reads as a
// footnote reference or as text by the labels the whole document defines (another section among
// them), leave it waiting, as a version this renderer version stored does.
//
// Of the versions the keeps of decisions standing in no container name, those another renderer
// version stored, or none stamped, are read in one query (otherRendererDecisionsAt), and a
// version this renderer version stored is not read, so a decisions view pays nothing for a keep
// whose version this renderer version wrote; only such a keep whose version holds the keep's sum
// pays a parse and a render.
func (s *server) markKeptAcrossRenderers(
	ctx context.Context, artifactID string, decisions []model.Decision, keys []sectionKey, alone []bool,
) error {
	numbers := map[int]bool{}
	for i, decision := range decisions {
		if decision.WaitingReview && decision.Kept != nil && alone[i] {
			numbers[decision.Kept.ChangedVersion] = true
		}
	}
	if len(numbers) == 0 {
		return nil
	}
	versions, err := s.otherRendererDecisionsAt(ctx, artifactID, sortedNumbers(numbers))
	if err != nil {
		return err
	}
	for i := range decisions {
		decision := &decisions[i]
		if !decision.WaitingReview || decision.Kept == nil || !alone[i] {
			continue
		}
		version, read := versions[decision.Kept.ChangedVersion]
		if !read {
			continue
		}
		kept, held := version.decisions[keys[i]]
		if held && kept.alone && kept.sum == decision.Kept.TextSHA256 && pmdoc.FragmentReadsAs(kept.text, decision.Text) {
			decision.WaitingReview = false
		}
	}
	return nil
}

// storedDecisions is one stored version's decisions, cut from its markdown (decisionCuts), and
// whether another renderer version stored that markdown or none stamped it (pmdoc.OtherRenderer):
// such a version may spell a decision otherwise than this build does though its tree is the same.
type storedDecisions struct {
	otherRenderer bool
	decisions     map[sectionKey]decisionCut
}

// holds reports whether this version holds now, the text of the decision key as the document
// stands: its text of that decision has now's sum, or, for a version of another renderer version,
// both texts stand alone (decisionCut.alone) and this build reads its text of that decision,
// alone, as now's text (pmdoc.FragmentReadsAs), which a text that holds `[^` on either side does
// not. Only a version of another renderer version whose sum differs pays the parse and the render.
// A version that gives the decision no text holds none of it.
func (v storedDecisions) holds(key sectionKey, now decisionCut) bool {
	stored, ok := v.decisions[key]
	return ok && (stored.sum == now.sum || v.otherRenderer && stored.alone && now.alone && pmdoc.FragmentReadsAs(stored.text, now.text))
}

// storedDecisionsAt is each named version's decisions, each one's text and its sum: the version's
// own stored markdown, cut by the same rule as the document as it stands, read beside the
// renderer version that stored it.
func (s *server) storedDecisionsAt(
	ctx context.Context, artifactID string, numbers []int,
) (map[int]storedDecisions, error) {
	if len(numbers) == 0 {
		return map[int]storedDecisions{}, nil
	}
	rows, err := s.deps.Store.Pool.Query(ctx, `
		select number, renderer_version, coalesce(markdown, '') from artifact_versions
		where artifact_id = $1::uuid and number = any($2::int[])
	`, artifactID, numbers)
	if err != nil {
		return nil, err
	}
	return scanStoredDecisions(rows)
}

// otherRendererDecisionsAt is storedDecisionsAt for those of the named versions another renderer
// version stored or none stamped (pmdoc.OtherRenderer), the only versions that may spell a
// decision otherwise than this build does: a version this renderer version stored is not read.
func (s *server) otherRendererDecisionsAt(
	ctx context.Context, artifactID string, numbers []int,
) (map[int]storedDecisions, error) {
	rows, err := s.deps.Store.Pool.Query(ctx, `
		select number, renderer_version, coalesce(markdown, '') from artifact_versions
		where artifact_id = $1::uuid and number = any($2::int[]) and renderer_version is distinct from $3
	`, artifactID, numbers, pmdoc.RendererVersion)
	if err != nil {
		return nil, err
	}
	return scanStoredDecisions(rows)
}

// scanStoredDecisions reads rows of versions' number, renderer_version and markdown into each
// version's decisions, its markdown cut by decisionCuts, and closes them.
func scanStoredDecisions(rows pgx.Rows) (map[int]storedDecisions, error) {
	defer rows.Close()
	versions := map[int]storedDecisions{}
	for rows.Next() {
		var number int
		var stamp *int
		var markdown string
		if err := rows.Scan(&number, &stamp, &markdown); err != nil {
			return nil, err
		}
		versions[number] = storedDecisions{
			otherRenderer: pmdoc.OtherRenderer(stamp),
			decisions:     decisionCuts(markdown),
		}
	}
	return versions, rows.Err()
}
