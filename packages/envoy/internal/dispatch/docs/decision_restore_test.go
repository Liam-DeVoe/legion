package docs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The section reader cuts a decisions document as the Decisions read of the release before cut
// it: each `##` heading outside `## Replaced` and each `###` heading under it opens a section,
// which holds every top-level block up to the next heading of its level or above; a `###` under
// an ordinary section is a block of that section; the document's title and `## Replaced` open
// none. A section's `Unsure:` paragraph is read with the rest of its words.
func TestTheSectionReaderCutsADecisionsDocumentIntoItsSections(t *testing.T) {
	tree, err := pmdoc.Parse("# Decisions\n\nAn introduction no section holds.\n\n" +
		"## Readings come from the seeded database\n\nThe person ruled it.\n\n### A detail\n\nIt stays small.\n\n" +
		"## Files stay out of the corpus\n\nWe write no file.\n\nUnsure: a reader who wants a file export would change this.\n\n" +
		"## Replaced\n\n### Files held the corpus once\n\nWe first kept the corpus in files.\n\n" +
		"### A second replaced one\n\nIt went too.\n")
	if err != nil {
		t.Fatal(err)
	}
	sections := decisionSectionsOf(tree)
	type read struct {
		title    string
		replaced bool
		body     int
		unsure   string
	}
	var got []read
	for _, section := range sections {
		_, reason := section.unsure()
		got = append(got, read{section.title, section.replaced, len(section.body), reason})
	}
	want := []read{
		{"Readings come from the seeded database", false, 3, ""},
		{"Files stay out of the corpus", false, 2, "a reader who wants a file export would change this."},
		{"Files held the corpus once", true, 1, ""},
		{"A second replaced one", true, 1, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("the reader cut %d sections %+v, want %d: %+v", len(got), got, len(want), want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("section %d reads %+v, want %+v", index, got[index], want[index])
		}
	}
}

// restoreIssue is issue DOC-1 of a test database, its spec and the documents the decisions step
// writes, each written through the document service and settled as the release before left it.
type restoreIssue struct {
	t        *testing.T
	database *store.Store
	service  *Service
	spec     string
}

// newRestoreIssue creates project DOC, issue DOC-1 and its spec holding markdown, settled, so each
// ask block of it holds its ask.
func newRestoreIssue(t *testing.T, markdown string) *restoreIssue {
	t.Helper()
	database := storetest.Open(t)
	if _, err := database.Pool.Exec(context.Background(), `
		insert into projects (key, name, default_assignee) values ('DOC', 'Documents', 'alice');
		insert into issues (key, project_key, number, title, created_by, rank)
		values ('DOC-1', 'DOC', 1, 'Document', '{"kind":"user","id":"alice"}', 'U');
	`); err != nil {
		t.Fatalf("create the issue: %v", err)
	}
	service := newTestServiceOn(t, database)
	service.settle = time.Hour
	issue := &restoreIssue{t: t, database: database, service: service}
	issue.spec = issue.document("spec", "spec.md", true, "", markdown)
	return issue
}

// document creates a document of DOC-1 holding markdown as its version 1, written by alice and
// indexed as an upload of a new document is, and settles it, so each ask block holds its ask.
func (issue *restoreIssue) document(slug, name string, primary bool, role, markdown string) string {
	t := issue.t
	t.Helper()
	var id string
	var rolePointer *string
	if role != "" {
		rolePointer = &role
	}
	if err := issue.database.Pool.QueryRow(context.Background(), `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, role, created_by)
		values ('DOC-1', 'DOC', $1, $2, 'doc', $3, $4, '{"kind":"user","id":"alice"}')
		returning id::text
	`, slug, name, primary, rolePointer).Scan(&id); err != nil {
		t.Fatalf("create document %s: %v", slug, err)
	}
	if err := joinedWrite(issue.service, func(ctx context.Context) error {
		written, err := issue.service.SeedText(ctx, id, markdown, model.Actor{Kind: "user", ID: "alice"})
		if err != nil {
			return err
		}
		tx, _ := txFromContext(ctx)
		if _, err := tx.Exec(ctx, `
			insert into artifact_versions (artifact_id, number, markdown, authors)
			values ($1, 1, $2, '[{"kind":"user","id":"alice"}]')
		`, id, written); err != nil {
			return err
		}
		_, write, err := issue.service.IndexDocumentReferences(ctx, id, 1, written)
		if err != nil {
			return err
		}
		return graphstore.Caused(ctx, tx, write, graphstore.NoEvent)
	}); err != nil {
		t.Fatalf("write document %s: %v", slug, err)
	}
	settleCurrentGeneration(t, issue.service, id)
	return id
}

// replace writes markdown over document as alice, a person, and its version, and settles it.
func (issue *restoreIssue) replace(document, markdown string) {
	t := issue.t
	t.Helper()
	alice := model.Actor{Kind: "user", ID: "alice"}
	if err := joinedWrite(issue.service, func(ctx context.Context) error {
		if _, err := issue.service.ReplaceText(ctx, document, markdown, alice); err != nil {
			return err
		}
		_, err := issue.service.SnapshotVersion(ctx, document, alice)
		return err
	}); err != nil {
		t.Fatalf("replace %s: %v", document, err)
	}
	settleCurrentGeneration(t, issue.service, document)
}

// answer has alice answer the ask of block blockID of document in text, and answers its id.
func (issue *restoreIssue) answer(document, blockID, text string) string {
	t := issue.t
	t.Helper()
	ask := blockAskOn(t, issue.service, document, blockID)
	alice := model.Actor{Kind: "user", ID: "alice"}
	at := time.Now().UTC()
	change := asks.Answered(model.AskAnswer{User: "alice", Selected: []string{}, Text: &text, At: at})
	change.Who, change.At = alice, at
	settleAskAs(t, issue.service, document, blockID, ask.ID, alice, change)
	return ask.ID
}

// run runs the decisions step's third phase over DOC, converting where convert, and answers what
// it did to DOC-1.
func (issue *restoreIssue) run(convert bool) DecisionIssue {
	t := issue.t
	t.Helper()
	step := NewDecisionStep(issue.database, "", nil)
	step.UseDocuments(issue.service)
	done, failed, err := step.RestoreAndConvert(context.Background(), "DOC", convert, false)
	if err != nil || len(failed) > 0 {
		t.Fatalf("the third phase answered %v with failures %+v", err, failed)
	}
	if len(done) != 1 || done[0].Issue != "DOC-1" {
		t.Fatalf("the third phase touched %+v, want DOC-1 alone", done)
	}
	return done[0]
}

// latest is the markdown of document's newest version.
func (issue *restoreIssue) latest(document string) string {
	issue.t.Helper()
	var markdown string
	if err := issue.database.Pool.QueryRow(context.Background(), `
		select markdown from artifact_versions where artifact_id = $1 order by number desc limit 1
	`, document).Scan(&markdown); err != nil {
		issue.t.Fatalf("read the newest version of %s: %v", document, err)
	}
	return markdown
}

// refKey is document's ref_key.
func (issue *restoreIssue) refKey(document string) string {
	issue.t.Helper()
	var refKey string
	if err := issue.database.Pool.QueryRow(context.Background(), `select ref_key from artifacts where id = $1`, document).Scan(&refKey); err != nil {
		issue.t.Fatalf("read the ref_key of %s: %v", document, err)
	}
	return refKey
}

// movedTo is each moved_to tuple the store states, from address to address, from document's
// blocks.
func (issue *restoreIssue) movedTo(document string) map[string]string {
	issue.t.Helper()
	rows, err := issue.database.Pool.Query(context.Background(), `
		select from_id, to_id from graph_tuples
		where kind = $1 and from_kind = $2 and starts_with(from_id, $3) and asserted
	`, graphstore.TupleMovedTo, refs.BlockNodeKind, issue.refKey(document)+"#")
	if err != nil {
		issue.t.Fatalf("read the moves: %v", err)
	}
	defer rows.Close()
	moves := map[string]string{}
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			issue.t.Fatal(err)
		}
		moves[from] = to
	}
	return moves
}

// askRow is the block and the document the row of ask names.
func (issue *restoreIssue) askRow(ask string) (string, string) {
	issue.t.Helper()
	var block, document string
	if err := issue.database.Pool.QueryRow(context.Background(), `
		select block_id, block_artifact_id::text from asks where id = $1
	`, ask).Scan(&block, &document); err != nil {
		issue.t.Fatalf("read the row of ask %s: %v", ask, err)
	}
	return block, document
}

// The conversion of a decisions document (dispatch://INFRA-1150 R9), after the restore of the
// answered asks its sections restate (R6): a section that cites an answered ask gives that ask's
// restored block its links as `shaped` and its body as a reading, whose author is the author of
// the section's newest version; one whose body is the ask's answer and nothing more, in other case
// and spacing, is dropped and writes no reading; one that cites no ask becomes a decision in the
// spec's `## Decisions` part, its `Unsure:` paragraph uncertainty="high" with the rest of the
// paragraph as uncertainty_reason; a section under `## Replaced` becomes a decision with
// replaced="true". The blocks stand in place of the part's pointer line. Each section leaves one
// moved_to tuple from its heading's address, and the document, holding no section any more, is
// retired.
func TestTheConversionTurnsEachSectionIntoANodeOrARuleDeletesIt(t *testing.T) {
	const pointer = "The decisions this spec rests on are in dispatch://DOC-1/artifact/decisions-md."
	const asked = "# Spec\n\n:::requirement{#r1}\nThe store holds the corpus.\n:::\n\n" +
		":::ask{#q-store urgency=\"high\" multiple=\"false\"}\nWhere do the readings come from?\n:::\n\n" +
		":::ask{#q-format urgency=\"med\" multiple=\"false\"}\nHow is a reading written?\n:::\n\n## Decisions\n\n" + pointer + "\n"
	issue := newRestoreIssue(t, asked)
	store := issue.answer(issue.spec, "q-store", "Read the seeded test database.")
	format := issue.answer(issue.spec, "q-format", "One line for each measurement.")
	issue.replace(issue.spec, "# Spec\n\n:::requirement{#r1}\nThe store holds the corpus.\n:::\n\n## Decisions\n\n"+pointer+"\n")
	decisions := issue.document("decisions-md", "decisions.md", false, "decisions", "# Decisions\n\n"+
		"## Readings come from the seeded database\n\nThe person ruled the test database, and we add that it stays small. "+
		"This shapes dispatch://DOC-1/spec#b-r1. Decided in dispatch://DOC-1/ask/"+store+".\n\n"+
		"## How a reading is written: dispatch://DOC-1/ask/"+format+"\n\none line  for each MEASUREMENT.\n\n"+
		"## Files stay out of the corpus\n\nWe write no file beside the database, which dispatch://DOC-1/spec#b-r1 depends on.\n\n"+
		"Unsure: a reader who wants a file export would change this.\n\n"+
		"## Replaced\n\n### Files held the corpus once\n\nWe first kept the corpus in files.\n")

	result := issue.run(true)
	if len(result.Restored) != 2 || result.Readings != 1 || result.Dropped != 1 || result.Decisions != 2 || result.Left != 0 || !result.Retired {
		t.Fatalf("the conversion did %+v, want two asks restored, one reading, one section dropped, two decisions, none left, the document retired", result)
	}
	spec := issue.latest(issue.spec)
	part := spec[strings.Index(spec, "## Decisions"):]
	for _, want := range []string{
		`#q-store urgency="high" multiple="false" shaped="dispatch://DOC-1/spec#b-r1"}`,
		"we add that it stays small",
		`#q-format urgency="med" multiple="false"}`,
		`uncertainty="high" uncertainty_reason="a reader who wants a file export would change this."`,
		"We write no file beside the database",
		`replaced="true"`,
		"Files held the corpus once",
	} {
		if !strings.Contains(part, want) {
			t.Errorf("the spec's Decisions part holds no %q:\n%s", want, part)
		}
	}
	for _, gone := range []string{pointer, "Unsure:", "one line  for each", `state="answered"`} {
		if strings.Contains(spec, gone) {
			t.Errorf("the converted spec still holds %q:\n%s", gone, spec)
		}
	}
	if strings.Index(part, "q-store") > strings.Index(part, "q-format") {
		t.Errorf("the restored asks stand out of answer-time order:\n%s", part)
	}
	for _, ask := range []string{store, format} {
		if _, document := issue.askRow(ask); document != issue.spec {
			t.Errorf("ask %s's row names document %s, want the spec %s", ask, document, issue.spec)
		}
	}
	if moves := issue.movedTo(decisions); len(moves) != 4 {
		t.Errorf("the conversion stated %d moves %v from the decisions document's headings, want one for each of its four sections", len(moves), moves)
	}
	var retired bool
	var role *string
	if err := issue.database.Pool.QueryRow(context.Background(), `select retired_at is not null, role from artifacts where id = $1`, decisions).Scan(&retired, &role); err != nil || !retired || role != nil {
		t.Errorf("the decisions document reads retired %v role %v (%v), want retired with no role", retired, role, err)
	}
	var authored int
	if err := issue.database.Pool.QueryRow(context.Background(), `
		select count(*) from graph_tuples
		where kind = $1 and from_kind = $2 and starts_with(from_id, $3) and to_kind = $4 and to_id = 'alice' and asserted
	`, graphstore.TupleAuthoredBy, refs.BlockNodeKind, issue.refKey(issue.spec)+"#", graphstore.KindPerson).Scan(&authored); err != nil || authored != 1 {
		t.Errorf("the spec's readings hold %d authored_by tuples to alice (%v), want the one reading's: the author of its section's newest version", authored, err)
	}
	if work, err := graphcensus.ReadRestoreWork(context.Background(), issue.database.Pool); err != nil || len(work) != 0 {
		t.Errorf("the restore leaves work %+v (%v), want none", work, err)
	}
}

// The collision of each shape of the restore (dispatch://INFRA-1150 R6): an answered ask whose
// block left the spec, where a callout of the spec now holds its block id, and an answered ask
// block of the decisions document whose id a callout of the spec holds too, each take a block id
// no document of the issue holds, recorded on the ask's row. The main shape writes no moved_to;
// the second writes one, from the block's address in the decisions document to its new one.
func TestTheRestoreGivesACollidingAskAFreshBlockIDInEachShape(t *testing.T) {
	const callouts = ":::callout{#q-left kind=\"note\" title=\"\"}\nThe readings name their database.\n:::\n\n" +
		":::callout{#q-doc kind=\"note\" title=\"\"}\nA moved block keeps a line.\n:::\n"
	issue := newRestoreIssue(t, "# Spec\n\n:::ask{#q-left urgency=\"high\" multiple=\"false\"}\nWhere do the readings come from?\n:::\n")
	left := issue.answer(issue.spec, "q-left", "The test database.")
	issue.replace(issue.spec, "# Spec\n\n"+callouts)
	decisions := issue.document("decisions-md", "decisions.md", false, "decisions",
		"# Decisions\n\n## Which line names a moved block\n\n:::ask{#q-doc urgency=\"low\" multiple=\"false\"}\nWhich line names a block that moved?\n:::\n")
	moved := issue.answer(decisions, "q-doc", "The census names each move.")

	result := issue.run(false)
	if len(result.Restored) != 2 {
		t.Fatalf("the restore returned %+v, want both asks", result.Restored)
	}
	for _, restored := range result.Restored {
		if !restored.NewID {
			t.Errorf("ask %s kept its block id though the spec holds it", restored.Ask)
		}
	}
	leftBlock, leftDocument := issue.askRow(left)
	movedBlock, movedDocument := issue.askRow(moved)
	if leftBlock == "q-left" || movedBlock == "q-doc" || leftDocument != issue.spec || movedDocument != issue.spec {
		t.Fatalf("the rows name %s of %s and %s of %s, want fresh ids in the spec %s", leftBlock, leftDocument, movedBlock, movedDocument, issue.spec)
	}
	spec := issue.latest(issue.spec)
	for _, want := range []string{"#q-left kind=", "#q-doc kind=", "#" + leftBlock + " urgency=", "#" + movedBlock + " urgency="} {
		if !strings.Contains(spec, want) {
			t.Errorf("the restored spec holds no %q:\n%s", want, spec)
		}
	}
	if moves := issue.movedTo(issue.spec); len(moves) != 0 {
		t.Errorf("the main shape stated moves %v, want none", moves)
	}
	want := map[string]string{issue.refKey(decisions) + "#q-doc": issue.refKey(issue.spec) + "#" + movedBlock}
	moves := issue.movedTo(decisions)
	if len(moves) != 2 || moves[issue.refKey(decisions)+"#q-doc"] != want[issue.refKey(decisions)+"#q-doc"] {
		t.Errorf("the second shape stated moves %v, want %v and the one of the section that held nothing but the block", moves, want)
	}
}

// A spec that holds no `## Decisions` part (dispatch://INFRA-1150 R6): the restore adds the
// heading, at the end of the spec, and the restored ask block under it.
func TestTheRestoreAddsADecisionsPartToASpecThatHoldsNone(t *testing.T) {
	issue := newRestoreIssue(t, "# Spec\n\n:::requirement{#r1}\nThe corpus is written once.\n:::\n\n:::ask{#q-b urgency=\"med\" multiple=\"false\"}\nWhich build writes the corpus?\n:::\n")
	issue.answer(issue.spec, "q-b", "The base release.")
	issue.replace(issue.spec, "# Spec\n\n:::requirement{#r1}\nThe corpus is written once.\n:::\n")
	result := issue.run(false)
	if len(result.Restored) != 1 || result.SpecVersion == 0 {
		t.Fatalf("the restore did %+v, want one ask restored in a version of the spec", result)
	}
	spec := issue.latest(issue.spec)
	heading := strings.Index(spec, "## Decisions\n")
	if heading < 0 || !strings.Contains(spec[heading:], "#q-b urgency=") || strings.Index(spec, "#r1") > heading {
		t.Fatalf("the restored spec holds no Decisions part after its requirement with the ask under it:\n%s", spec)
	}
}

// A closed issue's spec (dispatch://INFRA-1150 R6): the restore writes it as the version a
// settlement of a closed issue's document is written, through SnapshotVersion under the
// owner-verified mark, and the issue stays closed.
func TestTheRestoreWritesAClosedIssuesSpec(t *testing.T) {
	issue := newRestoreIssue(t, "# Spec\n\n:::ask{#q-c urgency=\"med\" multiple=\"false\"}\nWhich build writes the corpus?\n:::\n")
	issue.answer(issue.spec, "q-c", "The base release.")
	issue.replace(issue.spec, "# Spec\n\nThe corpus.\n")
	writeIssueClosed(t, issue.database, "DOC-1", true)
	result := issue.run(false)
	if len(result.Restored) != 1 || result.SpecVersion == 0 {
		t.Fatalf("the restore of the closed issue did %+v, want one ask restored in a version of the spec", result)
	}
	if spec := issue.latest(issue.spec); !strings.Contains(spec, "#q-c urgency=") {
		t.Fatalf("the closed issue's spec holds no restored ask:\n%s", spec)
	}
	var closed bool
	if err := issue.database.Pool.QueryRow(context.Background(), `select closed_at is not null from issues where key = 'DOC-1'`).Scan(&closed); err != nil || !closed {
		t.Fatalf("the issue reads closed %v (%v) after the restore, want it closed still", closed, err)
	}
}
