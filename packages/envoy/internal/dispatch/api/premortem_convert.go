package api

// premortem_convert.go is the pre-mortem conversion of dispatch://INFRA-1950 (spec
// r-premortem-nodes): one function, ConvertPremortem, which turns a pre-mortem document of an
// issue into one lens of kind pre-mortem under the issue and one risk under that lens for each
// risk the document holds, and moves the document's address to the lens; and its route, POST
// /api/v1/issues/{key}/artifacts/{slug}/premortem-convert. `envoy-dispatch premortem-convert`
// (cmd/dispatch/premortem_convert.go) is its other caller, and both answer the one report the
// function answers.
//
// The function lives in this package because it writes through writeReasoningNodes and
// writeReasoningPointer, the one write path of the reasoning kinds, and the route here calls it:
// a package of its own would make the import cycle api -> premortem -> api, which Go refuses. The
// command reaches it as cmd/dispatch reaches the rest of api.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// tupleMovedTo is the tuple kind that points from the address a node was read at to the node that
// stands there now, which the three resolvers of dispatch://INFRA-1150 follow. The merge of
// dispatch://INFRA-1150 replaces this constant with graphstore.TupleMovedTo.
const tupleMovedTo = "moved_to"

// checkBlockType is the block type of a check, read off its document_blocks row, the one block a
// risk's prevented_by may point at (graphstore's pointerRules).
const checkBlockType = "check"

// PremortemConverter is the actor `envoy-dispatch premortem-convert` writes as: the server's own
// credential, which proves no author a node could be reserved for and names no person or session
// an authored_by tuple could point at, as graphstore.LensKindWriter is the catalogue command's.
var PremortemConverter = model.Actor{Kind: "system", ID: "premortem-convert"}

// PremortemEntry is one entry of the map from each risk of a pre-mortem to the checks that prevent
// it: one line of the file `envoy-dispatch premortem-convert --prevented-by` reads, and one member
// of the route's prevented_by.
type PremortemEntry struct {
	// Risk is the risk's number as the document numbers it: 0 for risk 0, 1 to 10 for the
	// numbered risks.
	Risk int `json:"risk" required:"true"`
	// Checks are the dispatch:// addresses of the check blocks that prevent the risk, each a
	// prevented_by tuple from the risk; empty where no check does.
	Checks []string `json:"checks" required:"true"`
	// Reason is why those checks prevent the risk, or why none does.
	Reason string `json:"reason" required:"true"`
}

// PremortemReport is what one conversion did, or with dry_run would do: the document it reads, the
// lens and the risks it writes, each risk with the checks its prevented_by tuples point at and the
// reason its entry gave. A conversion of a document converted already writes nothing and names the
// lens the document's moved_to tuple points at, with no risk.
type PremortemReport struct {
	Document PremortemDocument `json:"document"`
	// DryRun is a call that wrote nothing: every write ran and was rolled back.
	DryRun bool `json:"dry_run"`
	// AlreadyConverted is a document whose address moved to a lens already, so the call wrote
	// nothing.
	AlreadyConverted bool            `json:"already_converted"`
	Lens             PremortemLens   `json:"lens"`
	Risks            []PremortemRisk `json:"risks"`
	// Summary says in one sentence what the call did.
	Summary string `json:"summary"`
}

// PremortemDocument is the document a conversion reads and retires: its address, its id, the
// version read, and the lens its address moves to, null on a dry run.
type PremortemDocument struct {
	Address string  `json:"address"`
	ID      string  `json:"id"`
	Version int     `json:"version"`
	MovedTo *string `json:"moved_to"`
}

// PremortemLens is the lens a conversion writes: its address, null on a dry run, its kind, the
// issue it stands under, and its verdict - the document's verdict followed by its two closing
// sections - null for a document converted already.
type PremortemLens struct {
	Address *string `json:"address"`
	Kind    string  `json:"kind"`
	On      string  `json:"on"`
	Verdict *string `json:"verdict"`
}

// PremortemRisk is one risk a conversion writes under the lens: its number in the document, its
// address (null on a dry run), its heading, early warning and prevention word for word, the
// addresses of the checks its prevented_by tuples point at, and the reason its entry gave.
type PremortemRisk struct {
	Number       int      `json:"number"`
	Address      *string  `json:"address"`
	Heading      string   `json:"heading"`
	EarlyWarning string   `json:"early_warning"`
	Prevention   string   `json:"prevention"`
	PreventedBy  []string `json:"prevented_by"`
	Reason       string   `json:"reason"`
}

// PremortemMapError is a map the conversion refuses, naming the entry: its position in the map,
// from 1, and the risk it names, or, with Entry 0, the risk the document holds that no entry names.
// The route answers it 400 ARTIFACT_INPUT, and the command names the line of its file that entry
// came from.
type PremortemMapError struct {
	Entry   int
	Risk    int
	Problem string
}

func (e *PremortemMapError) Error() string {
	if e.Entry == 0 {
		return fmt.Sprintf("risk %d has no entry in prevented_by: %s", e.Risk, e.Problem)
	}
	return fmt.Sprintf("prevented_by entry %d (risk %d): %s", e.Entry, e.Risk, e.Problem)
}

// PremortemDocumentError is a document the conversion cannot read as a pre-mortem: no document of
// the issue at that slug (Missing), a file that holds no markdown, or markdown that lacks a section
// a pre-mortem holds. The route answers a missing one 404 ARTIFACT_NOT_FOUND and any other 400
// ARTIFACT_INPUT.
type PremortemDocumentError struct {
	Missing bool
	Problem string
}

func (e *PremortemDocumentError) Error() string { return e.Problem }

// The sections a pre-mortem holds, by the words of their headings (the capture of
// dispatch://INFRA-1098/artifact/graph-premortem-md, packages/dispatch/e2e/fixtures/
// infra-1098-premortem.json): a verdict, risk 0, the numbered risks under one heading, and the two
// closing sections that go into the lens's verdict after the document's own. Each risk carries an
// early warning and a prevention, each a list item that opens with its label in bold.
const (
	premortemVerdictHeading    = "Verdict"
	premortemRisksHeading      = "The ten ways it goes wrong"
	premortemWorldClassHeading = "What a world-class implementation looks like"
	premortemFirstSliceHeading = "The smallest first slice that proves the model"
	premortemEarlyWarningLabel = "Early warning:"
	premortemPreventionLabel   = "Prevention:"
)

var (
	// premortemRiskZero is the heading of a risk the document numbers in words, "Risk 0, ...".
	premortemRiskZero = regexp.MustCompile(`^Risk (\d+)\b`)
	// premortemNumberedRisk is the heading of a numbered risk, "1\. Live-data migration: ...".
	premortemNumberedRisk = regexp.MustCompile(`^(\d+)\\?[.)][ \t]`)
	// premortemListItem is a line that opens a list item.
	premortemListItem = regexp.MustCompile(`^ {0,3}(?:[-*+]|\d+[.)])[ \t]`)
)

// premortemRisk is one risk a pre-mortem holds: its number, its heading as the document writes it,
// and the text of its early warning and its prevention.
type premortemRisk struct {
	number                            int
	heading, earlyWarning, prevention string
}

// premortem is what a pre-mortem document holds: its verdict, its risks in the order of their
// numbers, and its two closing sections, each word for word.
type premortem struct {
	verdict, worldClass, firstSlice string
	risks                           []premortemRisk
}

// lensVerdict is the verdict of the lens a conversion writes: the document's verdict, then each
// closing section under its own heading, word for word.
func (p premortem) lensVerdict() string {
	return p.verdict + "\n\n## " + premortemWorldClassHeading + "\n\n" + p.worldClass +
		"\n\n## " + premortemFirstSliceHeading + "\n\n" + p.firstSlice
}

// held is the numbers of the risks the document holds, as a refusal names them: "0 to 10", or each
// number where they are not one run.
func (p premortem) held() string {
	numbers := make([]string, len(p.risks))
	run := true
	for index, risk := range p.risks {
		numbers[index] = strconv.Itoa(risk.number)
		run = run && risk.number == p.risks[0].number+index
	}
	if run && len(p.risks) > 1 {
		return numbers[0] + " to " + numbers[len(numbers)-1]
	}
	return strings.Join(numbers, ", ")
}

// risk is the risk the document numbers number, and false where it holds none.
func (p premortem) risk(number int) (premortemRisk, bool) {
	index := slices.IndexFunc(p.risks, func(risk premortemRisk) bool { return risk.number == number })
	if index < 0 {
		return premortemRisk{}, false
	}
	return p.risks[index], true
}

// documentHeading is one heading of a document: its line, its level and its text.
type documentHeading struct {
	line, level int
	text        string
}

// parsePremortem reads a pre-mortem's markdown: the verdict, under the level-2 heading that opens
// with "Verdict"; risk 0, under the level-2 heading "Risk 0, ..."; each numbered risk, under a
// level-3 heading "<n>\. ..." beneath "The ten ways it goes wrong"; and the two closing sections,
// each under its level-2 heading. A section runs to the next heading of its level or above, and its
// text is its lines word for word, without the blank lines around them. Headings and fenced code
// blocks are read as the decisions view reads them (markdownHeading, codeFence), so a heading
// inside a fenced code block is the code's own text. A section missing, a risk numbered twice, and
// a risk without its early warning or its prevention are refused, naming what is wrong.
func parsePremortem(markdown string) (premortem, error) {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	var headings []documentHeading
	fenced := ""
	for index, line := range lines {
		if fence := codeFence(line); fence != "" {
			if fenced == "" {
				fenced = fence
			} else if strings.HasPrefix(fence, fenced[:1]) && len(fence) >= len(fenced) {
				fenced = ""
			}
			continue
		}
		if fenced != "" {
			continue
		}
		if level, text := markdownHeading(line); level > 0 {
			headings = append(headings, documentHeading{line: index, level: level, text: text})
		}
	}
	// section is the text under the heading at headings[at], to the next heading of its level or
	// above.
	section := func(at int) []string {
		end := len(lines)
		for _, next := range headings[at+1:] {
			if next.level <= headings[at].level {
				end = next.line
				break
			}
		}
		return lines[headings[at].line+1 : end]
	}

	var document premortem
	var found struct{ verdict, worldClass, firstSlice bool }
	// inRisks is whether the heading read is under "The ten ways it goes wrong", which a heading
	// of its level or above closes.
	inRisks := false
	addRisk := func(number int, heading string, body []string) error {
		if _, held := document.risk(number); held {
			return fmt.Errorf("it numbers two risks %d", number)
		}
		earlyWarning, held := listItemText(body, premortemEarlyWarningLabel)
		if !held {
			return fmt.Errorf("risk %d (%s) holds no list item opening with **%s**", number, heading, premortemEarlyWarningLabel)
		}
		prevention, held := listItemText(body, premortemPreventionLabel)
		if !held {
			return fmt.Errorf("risk %d (%s) holds no list item opening with **%s**", number, heading, premortemPreventionLabel)
		}
		document.risks = append(document.risks, premortemRisk{
			number: number, heading: heading, earlyWarning: earlyWarning, prevention: prevention,
		})
		return nil
	}
	for at, heading := range headings {
		if heading.level <= 2 {
			inRisks = false
		}
		switch {
		case heading.level == 2 && strings.HasPrefix(heading.text, premortemVerdictHeading):
			document.verdict, found.verdict = sectionText(section(at)), true
		case heading.level == 2 && heading.text == premortemWorldClassHeading:
			document.worldClass, found.worldClass = sectionText(section(at)), true
		case heading.level == 2 && heading.text == premortemFirstSliceHeading:
			document.firstSlice, found.firstSlice = sectionText(section(at)), true
		case heading.level == 2 && heading.text == premortemRisksHeading:
			inRisks = true
		case heading.level == 2 && premortemRiskZero.MatchString(heading.text):
			number, err := strconv.Atoi(premortemRiskZero.FindStringSubmatch(heading.text)[1])
			if err != nil {
				return premortem{}, fmt.Errorf("the heading %q numbers no risk: %w", heading.text, err)
			}
			if err := addRisk(number, heading.text, section(at)); err != nil {
				return premortem{}, err
			}
		case heading.level == 3 && inRisks && premortemNumberedRisk.MatchString(heading.text):
			number, err := strconv.Atoi(premortemNumberedRisk.FindStringSubmatch(heading.text)[1])
			if err != nil {
				return premortem{}, fmt.Errorf("the heading %q numbers no risk: %w", heading.text, err)
			}
			if err := addRisk(number, heading.text, section(at)); err != nil {
				return premortem{}, err
			}
		}
	}
	switch {
	case !found.verdict || blankText(document.verdict):
		return premortem{}, fmt.Errorf("it holds no section under a heading opening with %q", "## "+premortemVerdictHeading)
	case !found.worldClass || blankText(document.worldClass):
		return premortem{}, fmt.Errorf("it holds no section under %q", "## "+premortemWorldClassHeading)
	case !found.firstSlice || blankText(document.firstSlice):
		return premortem{}, fmt.Errorf("it holds no section under %q", "## "+premortemFirstSliceHeading)
	case len(document.risks) == 0:
		return premortem{}, errors.New(`it holds no risk: neither a "## Risk 0, ..." section nor a numbered risk under "## ` + premortemRisksHeading + `"`)
	}
	slices.SortFunc(document.risks, func(a, b premortemRisk) int { return a.number - b.number })
	return document, nil
}

// sectionText is the lines of a section word for word, joined, without the blank lines that open
// and close it.
func sectionText(lines []string) string {
	start, end := 0, len(lines)
	for start < end && blankText(lines[start]) {
		start++
	}
	for end > start && blankText(lines[end-1]) {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// listItemText is the text of the list item in lines that opens with label in bold - `- **Early
// warning:** text` - after the label, with the lines that continue the item beneath it, each
// without its indent; false where no item opens with label.
func listItemText(lines []string, label string) (string, bool) {
	for index, line := range lines {
		marker := premortemListItem.FindString(line)
		if marker == "" {
			continue
		}
		item := strings.TrimLeft(line[len(marker):], " \t")
		var rest string
		switch {
		case strings.HasPrefix(item, "**"+label+"**"):
			rest = item[len("**"+label+"**"):]
		case strings.HasPrefix(item, "__"+label+"__"):
			rest = item[len("__"+label+"__"):]
		default:
			continue
		}
		text := []string{strings.TrimSpace(rest)}
		for _, next := range lines[index+1:] {
			if level, _ := markdownHeading(next); level > 0 || blankText(next) || premortemListItem.MatchString(next) {
				break
			}
			text = append(text, strings.TrimSpace(next))
		}
		return strings.Join(text, "\n"), true
	}
	return "", false
}

// premortemSource is the document a conversion reads: its id, its store address (the ref_key a
// tuple names it by), its dispatch:// address, and its newest version's number and markdown.
type premortemSource struct {
	id, refKey, address string
	version             int
	markdown            string
}

// readPremortemSource reads the newest version of the document slug of the issue key.
func readPremortemSource(ctx context.Context, tx pgx.Tx, key, slug string) (premortemSource, error) {
	source := premortemSource{refKey: graphstore.ArtifactRefKey(key, "", "", slug)}
	var markdown *string
	var kind string
	var primary bool
	err := tx.QueryRow(ctx, `
		select a.id::text, a.kind, a.is_primary, v.number, v.markdown
		from artifacts a join artifact_versions v on v.artifact_id = a.id
		where a.ref_key = $1 and a.issue_key = $2
		order by v.number desc limit 1
	`, source.refKey, key).Scan(&source.id, &kind, &primary, &source.version, &markdown)
	if errors.Is(err, pgx.ErrNoRows) {
		return premortemSource{}, &PremortemDocumentError{Missing: true, Problem: fmt.Sprintf("%s holds no document %q", key, slug)}
	}
	if err != nil {
		return premortemSource{}, fmt.Errorf("read the document %s: %w", source.refKey, err)
	}
	source.address = refs.ArtifactRef(&key, "", nil, slug, kind, primary)
	if markdown == nil {
		return premortemSource{}, &PremortemDocumentError{Problem: fmt.Sprintf(
			"%s is a file and holds no markdown: a pre-mortem is a markdown document", source.address)}
	}
	source.markdown = *markdown
	return source, nil
}

// premortemPrevention is the checks one entry of the map names, resolved: each check's node, and
// the kind and the id a prevented_by tuple names it by.
type premortemPrevention struct {
	entry  PremortemEntry
	checks []model.GraphNode
	ends   []storedEnd
}

// checkPremortemMap holds the map to the document and resolves the checks it names, read for who:
// one entry for each risk the document holds, each naming a risk it holds, once, with a reason and
// with check addresses each named once, each a check block that is not gone and that who may see.
// The first entry that breaks one of those, in the map's order, is refused, and then the first risk
// no entry names; each is a PremortemMapError. It answers each entry by its risk's number.
func checkPremortemMap(ctx context.Context, q refs.Queryer, who model.Actor, document premortem, entries []PremortemEntry) (map[int]premortemPrevention, error) {
	resolved := make(map[int]premortemPrevention, len(entries))
	positions := make(map[int]int, len(entries))
	for index, entry := range entries {
		refused := func(format string, args ...any) error {
			return &PremortemMapError{Entry: index + 1, Risk: entry.Risk, Problem: fmt.Sprintf(format, args...)}
		}
		if blankText(entry.Reason) {
			return nil, refused("the reason is blank: an entry says why its checks prevent the risk, or why none does")
		}
		for at, address := range entry.Checks {
			if blankText(address) {
				return nil, refused("check %d is blank: a check is the dispatch:// address of a check block", at+1)
			}
			if slices.Index(entry.Checks, address) != at {
				return nil, refused("it names the check %s twice", address)
			}
		}
		if _, held := document.risk(entry.Risk); !held {
			return nil, refused("the document holds no risk %d: it holds risks %s", entry.Risk, document.held())
		}
		if first, named := positions[entry.Risk]; named {
			return nil, refused("risk %d has an entry already, entry %d: a risk takes one entry", entry.Risk, first)
		}
		positions[entry.Risk] = index + 1
		prevention := premortemPrevention{entry: entry}
		for _, address := range entry.Checks {
			check, end, found, err := checkNamed(ctx, q, who, address)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, refused("%s names no check block that is not gone", address)
			}
			prevention.checks = append(prevention.checks, check)
			prevention.ends = append(prevention.ends, end)
		}
		resolved[entry.Risk] = prevention
	}
	for _, risk := range document.risks {
		if _, named := resolved[risk.number]; !named {
			return nil, &PremortemMapError{Risk: risk.number, Problem: fmt.Sprintf(
				"every risk the document holds takes one entry, its checks or none, and %q has none", risk.heading)}
		}
	}
	return resolved, nil
}

// checkNamed is the check block address names, read through the read registry with who's
// visibility (decodeNodeAddress, then refs.Node), as the lens read reads its node: a block of any
// issue's document, since a risk of one issue may be prevented by a check of another. An address
// that decodes to no document block, a block no current version holds, one who may not see, one
// that is gone and one that is no check all answer false, alike, so the answer tells nothing of a
// node who may not see. It answers the node and the kind and the id a prevented_by tuple names it
// by (farEndOf).
func checkNamed(ctx context.Context, q refs.Queryer, who model.Actor, address string) (model.GraphNode, storedEnd, bool, error) {
	kind, id, ok := decodeNodeAddress(address)
	if !ok || kind != refs.BlockNodeKind {
		return model.GraphNode{}, storedEnd{}, false, nil
	}
	node, found, err := refs.Node(ctx, q, who, kind, id, true)
	if err != nil {
		return model.GraphNode{}, storedEnd{}, false, err
	}
	if !found || node.State == graphstore.StateGone {
		return model.GraphNode{}, storedEnd{}, false, nil
	}
	var blockType string
	err = q.QueryRow(ctx, `select type from document_blocks where node_id = $1`, node.ID).Scan(&blockType)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.GraphNode{}, storedEnd{}, false, nil
	}
	if err != nil {
		return model.GraphNode{}, storedEnd{}, false, fmt.Errorf("read the type of the block %s: %w", address, err)
	}
	if blockType != checkBlockType {
		return model.GraphNode{}, storedEnd{}, false, nil
	}
	return node, farEndOf(node, address), true, nil
}

// ConvertPremortem converts the pre-mortem document slug of the issue key into nodes, in the
// caller's transaction tx, as the credential proving that proved the caller and the actor recorded
// the write records (reasoningWrite): it is the one conversion, which the route and the command
// both run.
//
// It takes the issue's row `for no key update` first, the owner row the lock order on
// api/inbox_read.go's markInboxReadThreads puts before every other, so two conversions of one
// issue queue there and the second reads what the first wrote. It reads the document's newest
// version. A document whose address carries a moved_to tuple already is converted: the call
// answers that lens, AlreadyConverted, and writes nothing. Otherwise it parses the document
// (parsePremortem), holds entries to it (checkPremortemMap) and writes, through
// writeReasoningNodes at graphstore.NoEvent, one lens of kind pre-mortem under the issue's node,
// whose verdict is the document's verdict followed by its two closing sections, and under it one
// risk for each risk of the document in the order of their numbers, each with its heading, its
// early warning and its prevention and a prevented_by tuple to each check its entry names. Then it
// retires the document (retireConvertedDocument), which moves its address to the lens.
//
// With dryRun it runs every one of those writes in a savepoint of tx and rolls the savepoint back,
// so the store's own rules hold the write as they would hold the real one and nothing of it
// remains, and it answers the report without the addresses nothing holds.
//
// A map the document refuses is a PremortemMapError, and a document that is none or no pre-mortem
// a PremortemDocumentError; each is refused before anything is written.
func ConvertPremortem(ctx context.Context, tx pgx.Tx, proving, recorded model.Actor, key, slug string, entries []PremortemEntry, dryRun bool) (PremortemReport, error) {
	var held bool
	err := tx.QueryRow(ctx, `select true from issues where key = $1`+ownerRowLock, key).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return PremortemReport{}, &PremortemDocumentError{Missing: true, Problem: fmt.Sprintf("no issue %s holds a document %q", key, slug)}
	}
	if err != nil {
		return PremortemReport{}, fmt.Errorf("take the row of the issue %s: %w", key, err)
	}
	source, err := readPremortemSource(ctx, tx, key, slug)
	if err != nil {
		return PremortemReport{}, err
	}
	report := PremortemReport{
		Document: PremortemDocument{Address: source.address, ID: source.id, Version: source.version},
		DryRun:   dryRun,
		Lens:     PremortemLens{Kind: graphstore.LensKindPreMortem, On: "dispatch://" + key},
		Risks:    []PremortemRisk{},
	}
	moved, err := graphstore.CurrentOut(ctx, tx, graphstore.KindArtifact, source.refKey, []string{tupleMovedTo})
	if err != nil {
		return PremortemReport{}, err
	}
	if len(moved) > 0 {
		lens := "dispatch://" + moved[0].ToKind + "/" + moved[0].ToID
		report.AlreadyConverted, report.Lens.Address, report.Document.MovedTo = true, &lens, &lens
		report.Summary = fmt.Sprintf("%s is already converted into the pre-mortem lens %s, so nothing was written; "+
			"GET /api/v1/lenses?on=%s reads that lens with its risks.", source.address, lens, lens)
		return report, nil
	}
	document, err := parsePremortem(source.markdown)
	if err != nil {
		return PremortemReport{}, &PremortemDocumentError{Problem: fmt.Sprintf(
			"%s (version %d) is no pre-mortem this conversion reads: %v", source.address, source.version, err)}
	}
	reader := proving.Recorded()
	preventions, err := checkPremortemMap(ctx, tx, reader, document, entries)
	if err != nil {
		return PremortemReport{}, err
	}
	kind, err := lensKindNamed(ctx, tx, reader, graphstore.LensKindPreMortem)
	if err != nil {
		return PremortemReport{}, err
	}
	at, err := transactionNow(ctx, tx)
	if err != nil {
		return PremortemReport{}, err
	}

	lensID, verdict := uuid.NewString(), document.lensVerdict()
	nodes := []reasoningNode{{
		kind: graphstore.KindLens, id: lensID, parentKind: graphstore.KindIssue, parentID: key,
		attributes: map[string]any{graphstore.AttributeVerdict: verdict},
		tuples: []graphstore.Tuple{{
			FromKind: graphstore.KindLens, FromID: lensID, Kind: graphstore.TupleHasKind,
			ToKind: graphstore.KindLensKind, ToID: kind.ID,
		}},
	}}
	report.Lens.Verdict = &verdict
	for _, risk := range document.risks {
		id, prevention := uuid.NewString(), preventions[risk.number]
		node := reasoningNode{
			kind: graphstore.KindRisk, id: id, parentKind: graphstore.KindLens, parentID: lensID,
			attributes: map[string]any{
				graphstore.AttributeHeading:      risk.heading,
				graphstore.AttributeEarlyWarning: risk.earlyWarning,
				graphstore.AttributePrevention:   risk.prevention,
			},
		}
		written := PremortemRisk{
			Number: risk.number, Heading: risk.heading, EarlyWarning: risk.earlyWarning, Prevention: risk.prevention,
			PreventedBy: make([]string, 0, len(prevention.checks)), Reason: prevention.entry.Reason,
		}
		for index, end := range prevention.ends {
			node.tuples = append(node.tuples, graphstore.Tuple{
				FromKind: graphstore.KindRisk, FromID: id, Kind: graphstore.TuplePreventedBy, ToKind: end.kind, ToID: end.id,
			})
			written.PreventedBy = append(written.PreventedBy, prevention.checks[index].Ref)
		}
		if !dryRun {
			address := "dispatch://" + graphstore.KindRisk + "/" + id
			written.Address = &address
		}
		nodes = append(nodes, node)
		report.Risks = append(report.Risks, written)
	}

	lens := lensAddress(lensID)
	convert := func(q pgx.Tx) error {
		if _, err := writeReasoningNodes(ctx, q, reasoningWrite{
			issue: key, proving: proving, recorded: recorded, at: at, nodes: nodes,
		}); err != nil {
			return err
		}
		return retireConvertedDocument(ctx, q, source.id, lens, recorded)
	}
	if dryRun {
		savepoint, err := tx.Begin(ctx)
		if err != nil {
			return PremortemReport{}, fmt.Errorf("open the dry run's savepoint: %w", err)
		}
		err = convert(savepoint)
		if rollback := savepoint.Rollback(ctx); err == nil && rollback != nil {
			err = fmt.Errorf("roll the dry run back: %w", rollback)
		}
		if err != nil {
			return PremortemReport{}, err
		}
		report.Summary = fmt.Sprintf("Dry run, nothing written: converting %s (version %d) writes one pre-mortem lens "+
			"under %s with %d risks and moves the document's address to that lens.",
			source.address, source.version, key, len(report.Risks))
		return report, nil
	}
	if err := convert(tx); err != nil {
		return PremortemReport{}, err
	}
	report.Lens.Address, report.Document.MovedTo = &lens, &lens
	report.Summary = fmt.Sprintf("Converted %s (version %d) into the pre-mortem lens %s under %s, with %d risks; "+
		"the document's address moves to the lens.", source.address, source.version, lens, key, len(report.Risks))
	return report, nil
}

// retireConvertedDocument retires the document artifactID names once the conversion wrote the lens
// lensAddress names, as actor: it appends the moved_to tuple from the document's own address - the
// store's form of `dispatch://<KEY>/artifact/<slug>`, kind artifact at its ref_key, as a tuple
// names a document (farEndOf) - to the lens, through writeReasoningPointer, at the transaction's
// own time and at graphstore.NoEvent, so the resolvers of dispatch://INFRA-1150 answer the
// document's old address with the lens. Then it retires the document through
// dispatch://INFRA-1150's retirement of a document, the one retire path, whose reason names the
// lens: "converted into the pre-mortem lens <lensAddress>".
func retireConvertedDocument(ctx context.Context, tx pgx.Tx, artifactID, lensAddress string, actor model.Actor) error {
	var refKey, key string
	var at time.Time
	if err := tx.QueryRow(ctx, `select ref_key, issue_key, now() from artifacts where id = $1`, artifactID).Scan(&refKey, &key, &at); err != nil {
		return fmt.Errorf("read the converted document %s: %w", artifactID, err)
	}
	kind, lensID, ok := decodeNodeAddress(lensAddress)
	if !ok || kind != graphstore.KindLens {
		return fmt.Errorf("%s is no lens's address", lensAddress)
	}
	if err := writeReasoningPointer(ctx, tx, reasoningPointer{issue: key, action: pointerAppend, pointer: graphstore.Tuple{
		FromKind: graphstore.KindArtifact, FromID: refKey, Kind: tupleMovedTo, ToKind: graphstore.KindLens, ToID: lensID,
		At: at.UTC(), Who: actor, SourceEvent: graphstore.NoEvent, Asserted: true,
	}}); err != nil {
		return err
	}
	// INFRA-1150 T11: docs.RetireDocument(ctx, tx, artifactID, actor, reason)
	return nil
}

// --- POST /api/v1/issues/{key}/artifacts/{slug}/premortem-convert ------------------------------

// premortemConvertInput is the body of a conversion.
type premortemConvertInput struct {
	Actor *model.Actor `json:"actor"`
	// PreventedBy is the map from each risk of the document to the checks that prevent it, one
	// entry {risk, checks, reason} for each risk the document holds, checks empty for none. The
	// handler reads each entry on its own (decodePremortemEntries), so an entry of the wrong shape
	// is refused by its position.
	PreventedBy json.RawMessage `json:"prevented_by" required:"true"`
	// DryRun answers what the conversion would write, and writes nothing.
	DryRun bool `json:"dry_run"`
}

// premortemEntryInput is one entry of prevented_by as the route reads it, each member a pointer so
// a member the entry leaves out is told from its zero value.
type premortemEntryInput struct {
	Risk   *int      `json:"risk"`
	Checks *[]string `json:"checks"`
	Reason *string   `json:"reason"`
}

// premortemEntryShape is the shape of an entry as a refusal of another names it.
const premortemEntryShape = `{"risk": <the risk's number>, "checks": [<check address>, ...], "reason": "<text>"}, with checks [] for none`

// decodePremortemEntries reads prevented_by entry by entry: a body that leaves it out, or holds no
// list there, and an entry that is no object of the three members, holds another member, or holds
// a member of the wrong type, are refused, naming the entry by its position from 1 and the risk it
// names where that could be read. It answers the refusal's words, empty for a map it reads whole.
func decodePremortemEntries(raw json.RawMessage) ([]PremortemEntry, string) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, "prevented_by is required: one entry " + premortemEntryShape + " for each risk the document holds"
	}
	var listed []json.RawMessage
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, "prevented_by is a list of entries " + premortemEntryShape
	}
	entries := make([]PremortemEntry, 0, len(listed))
	for index, member := range listed {
		var input premortemEntryInput
		decoder := json.NewDecoder(bytes.NewReader(member))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&input)
		// A risk of the wrong type is left at its zero value, never at what the entry holds, so it
		// names the entry only where it was read.
		var mistyped *json.UnmarshalTypeError
		isMistyped := errors.As(err, &mistyped)
		named := fmt.Sprintf("prevented_by entry %d", index+1)
		if input.Risk != nil && (err == nil || !isMistyped || mistyped.Field != "risk") {
			named += fmt.Sprintf(" (risk %d)", *input.Risk)
		}
		if err != nil {
			cause := strings.TrimPrefix(err.Error(), "json: ")
			if isMistyped && mistyped.Field != "" {
				cause = fmt.Sprintf("%s holds a JSON %s", mistyped.Field, mistyped.Value)
			}
			return nil, fmt.Sprintf("%s is no entry %s: %s", named, premortemEntryShape, cause)
		}
		var missing []string
		for _, field := range []struct {
			name string
			held bool
		}{{"risk", input.Risk != nil}, {"checks", input.Checks != nil}, {"reason", input.Reason != nil}} {
			if !field.held {
				missing = append(missing, field.name)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Sprintf("%s holds no %s: an entry is %s", named, strings.Join(missing, ", "), premortemEntryShape)
		}
		entries = append(entries, PremortemEntry{Risk: *input.Risk, Checks: *input.Checks, Reason: *input.Reason})
	}
	return entries, ""
}

// convertPremortemRoute is POST /api/v1/issues/{key}/artifacts/{slug}/premortem-convert: the
// conversion of the issue's pre-mortem document {slug} (ConvertPremortem) in the request's one
// transaction, as the credential that proved the caller, answering the report. A body that is not
// JSON is decodeJSON's 400 INVALID_JSON; a map of any other shape, and a map the document refuses,
// are 400 ARTIFACT_INPUT naming the entry by its position and its risk, or the risk no entry names;
// no document {slug} of the issue is 404 ARTIFACT_NOT_FOUND, and a document that is no pre-mortem
// 400 ARTIFACT_INPUT. Each writes nothing. A document converted already answers 200 naming its lens
// and writes nothing, and so does a dry run, naming what it would write.
func (s *server) convertPremortemRoute(w http.ResponseWriter, r *http.Request) {
	var input premortemConvertInput
	if err := decodeJSON(r, &input); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	caller, ok := s.reasoningCallerOf(w, r, input.Actor)
	if !ok {
		return
	}
	entries, refusal := decodePremortemEntries(input.PreventedBy)
	if refusal != "" {
		writeError(w, "ARTIFACT_INPUT", http.StatusBadRequest, refusal)
		return
	}
	ctx := r.Context()
	tx, err := s.begin(ctx)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	report, err := ConvertPremortem(ctx, tx, caller.proving, caller.recorded, r.PathValue("key"), r.PathValue("slug"), entries, input.DryRun)
	var refusedMap *PremortemMapError
	var refusedDocument *PremortemDocumentError
	switch {
	case errors.As(err, &refusedMap):
		writeError(w, "ARTIFACT_INPUT", http.StatusBadRequest, refusedMap.Error())
		return
	case errors.As(err, &refusedDocument) && refusedDocument.Missing:
		writeError(w, "ARTIFACT_NOT_FOUND", http.StatusNotFound, refusedDocument.Error())
		return
	case errors.As(err, &refusedDocument):
		writeError(w, "ARTIFACT_INPUT", http.StatusBadRequest, refusedDocument.Error())
		return
	case err != nil:
		s.writeHandlerError(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, report)
}
