package specform

import (
	"os"
	"reflect"
	"slices"
	"testing"
)

// everyForm holds each form of a text of The Spec that ExtractDocument reads, and a term-shaped
// line outside Terms.
const everyForm = "# Fixture\n" +
	"\n" +
	":::requirement{#ship-at-head}\n" +
	"Legion lands a change at the head it read.\n" +
	"It refuses another head.\n" +
	":::\n" +
	"\n" +
	"- **Dispatch keeps each answer.** `keeps-answers` · under: `dispatch` · check: `none`\n" +
	"\n" +
	":::check{#v-one serves=\"dispatch://ACME-1/spec#b-ship-at-head\" run=\"Run the merge test.\" negative_control=\"The merger lands any head.\"}\n" +
	"The merger lands only the head it read.\n" +
	":::\n" +
	"\n" +
	"### Records (component `dispatch-records`)\n" +
	"\n" +
	"It holds each record of Dispatch.\n" +
	"\n" +
	"Purpose of dispatch-search: It finds relevant records.\n" +
	"\n" +
	"| Id | Kind | Name | Purpose | Parent or joins |\n" +
	"| --- | --- | --- | --- | --- |\n" +
	"| `dispatch-records` | component | Records | It holds each record of Dispatch. | dispatch |\n" +
	"| dispatch-inbox | component | Inbox | It shows a person what waits on that person. | dispatch |\n" +
	"\n" +
	"## Terms\n" +
	"\n" +
	"- **session**: One conversation of an agent.\n" +
	"* **work unit**: One piece of work that one owner runs.\n" +
	"\n" +
	"## Notes\n" +
	"\n" +
	"- **stray**: This line is no term.\n"

const fixtureWhere = "spec.md version 4"

func TestExtractReadsEachFormOfText(t *testing.T) {
	got, err := ExtractDocument("spec.md", 4, everyForm)
	if err != nil {
		t.Fatal(err)
	}
	want := []Text{
		{Node: "ship-at-head", Kind: KindStatement, Body: "Legion lands a change at the head it read.\nIt refuses another head.", Where: fixtureWhere},
		{Node: "keeps-answers", Kind: KindStatement, Body: "Dispatch keeps each answer.", Where: fixtureWhere},
		{Node: "v-one", Kind: KindClaim, Body: "The merger lands only the head it read.", Where: fixtureWhere},
		{Node: "v-one", Kind: KindProcedure, Body: "Run the merge test.", Where: fixtureWhere},
		{Node: "v-one", Kind: KindFailingExample, Body: "The merger lands any head.", Where: fixtureWhere},
		{Node: "dispatch-records", Kind: KindName, Body: "Records", Where: fixtureWhere},
		{Node: "dispatch-records", Kind: KindPurpose, Body: "It holds each record of Dispatch.", Where: fixtureWhere},
		{Node: "dispatch-search", Kind: KindPurpose, Body: "It finds relevant records.", Where: fixtureWhere},
		{Node: "dispatch-inbox", Kind: KindName, Body: "Inbox", Where: fixtureWhere},
		{Node: "dispatch-inbox", Kind: KindPurpose, Body: "It shows a person what waits on that person.", Where: fixtureWhere},
	}
	if !reflect.DeepEqual(got.Texts, want) {
		t.Errorf("Texts =\n%#v\nwant\n%#v", got.Texts, want)
	}
	terms := []Term{
		{Name: "session", Definition: "One conversation of an agent.", Where: fixtureWhere},
		{Name: "work unit", Definition: "One piece of work that one owner runs.", Where: fixtureWhere},
	}
	if !reflect.DeepEqual(got.Terms, terms) {
		t.Errorf("Terms = %#v, want %#v", got.Terms, terms)
	}
}

func TestExtractCountsWhatItRead(t *testing.T) {
	got, err := ExtractDocument("spec.md", 4, everyForm)
	if err != nil {
		t.Fatal(err)
	}
	want := map[Kind]int{KindStatement: 2, KindClaim: 1, KindProcedure: 1, KindFailingExample: 1, KindName: 2, KindPurpose: 3, KindDefinition: 2}
	if !reflect.DeepEqual(got.Counts, want) {
		t.Errorf("Counts = %v, want %v", got.Counts, want)
	}
	none, err := ExtractDocument("notes.md", 1, "# Notes\n\nProse with no text of The Spec.\n")
	if err != nil {
		t.Fatal(err)
	}
	zero := map[Kind]int{KindStatement: 0, KindClaim: 0, KindProcedure: 0, KindFailingExample: 0, KindName: 0, KindPurpose: 0, KindDefinition: 0}
	if !reflect.DeepEqual(none.Counts, zero) || len(none.Texts) != 0 || len(none.Terms) != 0 {
		t.Errorf("a document with no text: %#v, want every count 0 and nothing read", none)
	}
}

// TestExtractReadsLevelOneShape reads level one's 43 system, component and interface headings
// and its table of components: the 40 that stand under a heading of their own and in the table
// are one text each.
func TestExtractReadsLevelOneShape(t *testing.T) {
	markdown, err := os.ReadFile("testdata/level-one-components.md")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ExtractDocument("spec.md", 10, string(markdown))
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts[KindName] != 43 || got.Counts[KindPurpose] != 43 {
		t.Errorf("level one gives %d names and %d purposes, want 43 and 43", got.Counts[KindName], got.Counts[KindPurpose])
	}
	var purposes []string
	for _, text := range got.Texts {
		if text.Kind == KindPurpose {
			purposes = append(purposes, text.Node)
		}
	}
	for _, node := range []string{"legion", "dispatch", "envoy"} {
		if !slices.Contains(purposes, node) {
			t.Errorf("no purpose of %s among %v", node, purposes)
		}
	}
}

func TestExtractNamesABlockWithNoID(t *testing.T) {
	got, err := ExtractDocument("spec.md", 1, ":::requirement{}\nLegion lands a change.\n:::\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Texts) != 1 || got.Texts[0].Node != "requirement 1" {
		t.Fatalf("Texts = %#v, want one text of Node requirement 1", got.Texts)
	}
}

func TestExtractNamesARepeatedID(t *testing.T) {
	got, err := ExtractDocument("spec.md", 1, ":::requirement{#a}\nLegion lands a change.\n:::\n\n:::requirement{#a}\nLegion lands another change.\n:::\n")
	if err != nil {
		t.Fatal(err)
	}
	var nodes []string
	for _, text := range got.Texts {
		nodes = append(nodes, text.Node)
	}
	if !reflect.DeepEqual(nodes, []string{"a", "a (2)"}) {
		t.Fatalf("Nodes = %v, want [a a (2)]", nodes)
	}
	if findings := Check(planted(), Input{Texts: got.Texts}); len(findings) != 0 {
		t.Fatalf("Check = %#v, want none", findings)
	}
	markdown := ":::requirement{#a}\nLegion lands a change.\n:::\n\n:::requirement{#a}\nOne. Two. Three.\n:::\n"
	broken, err := ExtractDocument("spec.md", 1, markdown)
	if err != nil {
		t.Fatal(err)
	}
	if findings := Check(Context{}, Input{Texts: broken.Texts}); len(findings) != 1 || findings[0].Node != "a (2)" {
		t.Fatalf("Check = %#v, want one finding of Node a (2)", findings)
	}
}

// TestExtractSkipsCodeFences holds the extractor to the document's own texts: an example in a
// code fence, as a how-to writes one, is no text of The Spec.
func TestExtractSkipsCodeFences(t *testing.T) {
	markdown := "```\n:::requirement{#example}\nOne. Two. Three.\n:::\n- **<statement>** `<id>` · under: `<parent id>` · check: `none`\n```\n\n~~~\nPurpose of x: One. Two.\n~~~\n"
	got, err := ExtractDocument("the-spec.md", 2, markdown)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Texts) != 0 || len(got.Terms) != 0 {
		t.Fatalf("ExtractDocument read %#v from code fences, want nothing", got)
	}
}
