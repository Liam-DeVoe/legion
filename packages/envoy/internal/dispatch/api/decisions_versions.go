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
// (pmdoc.FragmentReadsAs). Both sides are read by one cut (decisionCuts): the stored version's,
// and the document's as it stands (current, readDecisions), each cut's decisionCut.alone saying
// whether no typed block holds the decision's text there or now; a decision whose heading stands
// in a list item, a quote or a footnote definition has no cut now. A decision's text read alone
// loses the list item, quote, footnote definition or typed block it stands in, so two trees can
// read as one. The decision waits for review otherwise: a version the view could not read - the
// parser refuses its markdown (storedDecisionsAt) - a version that holds no such text - the
// decision's heading changed since, or a block left the decision after that version - a decision
// inside a container then or now, kept text that reads otherwise alone, and kept text or text now
// that holds `[^`, which reads as a footnote reference or as text by the labels the whole document
// defines (another section among them), leave it waiting, as a version this renderer version
// stored does.
//
// Of the versions the keeps of decisions standing in no container name, those another renderer
// version stored, or none stamped, are read in one query (otherRendererDecisionsAt), and a
// version this renderer version stored is not read, so a decisions view pays nothing for a keep
// whose version this renderer version wrote. Each version read is parsed whole to be cut
// (decisionCuts), unless its markdown is the document's as it stands, whose cut it takes
// (documentCut); only a keep whose text there has the keep's sum also pays the parse and the
// render of pmdoc.FragmentReadsAs. It answers the versions it read, which the links' pass then
// does not read again (markStaleLinks).
func (s *server) markKeptAcrossRenderers(
	ctx context.Context, artifactID string, decisions []model.Decision, keys []sectionKey,
	current documentCut,
) (map[int]storedDecisions, error) {
	// A candidate is a decision waiting for review that a keep names and that stands alone now.
	candidate := func(i int) bool {
		now, ok := current.cuts[keys[i]]
		return decisions[i].WaitingReview && decisions[i].Kept != nil && ok && now.alone
	}
	numbers := map[int]bool{}
	for i, decision := range decisions {
		if candidate(i) {
			numbers[decision.Kept.ChangedVersion] = true
		}
	}
	if len(numbers) == 0 {
		return nil, nil
	}
	versions, err := s.otherRendererDecisionsAt(ctx, artifactID, sortedNumbers(numbers), current)
	if err != nil {
		return nil, err
	}
	for i := range decisions {
		decision := &decisions[i]
		if !candidate(i) {
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
	return versions, nil
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
// renderer version that stored it. A version in read, which the keeps' pass read
// (markKeptAcrossRenderers), is taken from there, and only the others are read. A version whose
// markdown the parser refuses is left out, as one the view could not read (scanStoredDecisions).
func (s *server) storedDecisionsAt(
	ctx context.Context, artifactID string, numbers []int, current documentCut, read map[int]storedDecisions,
) (map[int]storedDecisions, error) {
	versions := make(map[int]storedDecisions, len(numbers))
	var unread []int
	for _, number := range numbers {
		if version, ok := read[number]; ok {
			versions[number] = version
		} else {
			unread = append(unread, number)
		}
	}
	if len(unread) == 0 {
		return versions, nil
	}
	rows, err := s.deps.Store.Pool.Query(ctx, `
		select number, renderer_version, coalesce(markdown, '') from artifact_versions
		where artifact_id = $1::uuid and number = any($2::int[])
	`, artifactID, unread)
	if err != nil {
		return nil, err
	}
	if err := scanStoredDecisions(rows, current, versions); err != nil {
		return nil, err
	}
	return versions, nil
}

// otherRendererDecisionsAt is storedDecisionsAt for those of the named versions another renderer
// version stored or none stamped (pmdoc.OtherRenderer), the only versions that may spell a
// decision otherwise than this build does: a version this renderer version stored is not read.
func (s *server) otherRendererDecisionsAt(
	ctx context.Context, artifactID string, numbers []int, current documentCut,
) (map[int]storedDecisions, error) {
	rows, err := s.deps.Store.Pool.Query(ctx, `
		select number, renderer_version, coalesce(markdown, '') from artifact_versions
		where artifact_id = $1::uuid and number = any($2::int[]) and renderer_version is distinct from $3
	`, artifactID, numbers, pmdoc.RendererVersion)
	if err != nil {
		return nil, err
	}
	versions := map[int]storedDecisions{}
	if err := scanStoredDecisions(rows, current, versions); err != nil {
		return nil, err
	}
	return versions, nil
}

// scanStoredDecisions reads rows of versions' number, renderer_version and markdown into versions,
// each version's decisions its markdown cut by decisionCuts, and closes them. A version whose
// markdown is current's, the document's as it stands, takes current's cut rather than a second
// parse of the same markdown. A version whose markdown the parser refuses gives no cuts, and is
// left out rather than read as a version that holds no decision's text: a link that names it stays
// as it is, as for a version the document does not hold (staleLink), and a keep that names it
// waits for review (markKeptAcrossRenderers).
func scanStoredDecisions(rows pgx.Rows, current documentCut, versions map[int]storedDecisions) error {
	defer rows.Close()
	for rows.Next() {
		var number int
		var stamp *int
		var markdown string
		if err := rows.Scan(&number, &stamp, &markdown); err != nil {
			return err
		}
		decisions, read := current.cuts, current.read
		if markdown != current.markdown {
			decisions, read = decisionCuts(markdown)
		}
		if !read {
			continue
		}
		versions[number] = storedDecisions{otherRenderer: pmdoc.OtherRenderer(stamp), decisions: decisions}
	}
	return rows.Err()
}
