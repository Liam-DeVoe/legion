package specstore

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/specchange"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// A level is read from its two documents: each statement with its text and the parent a
// `### Under <id>` heading gives it, a heading of another form giving none; each component and
// interface of its table, a component's parent and an interface's joins from the last column, and
// `none` naming none; and each statement's kind, about and supports from its notes, as written.
func TestParseLevelReadsTheLevelAndItsNotes(t *testing.T) {
	t.Parallel()
	level, err := ParseLevel("# Level\n\n## Statements\n\n### Billing\n\n"+
		":::requirement{#a-bill-is-sent}\nA bill is sent.\n:::\n\n"+
		"### Under a-bill-is-sent\n\n:::requirement{#a-bill-is-sent-once}\nA bill is sent once.\n:::\n\n"+
		"### Ledger\n\n:::requirement{#a-ledger-holds-each-bill}\nA ledger holds each bill.\n:::\n\n"+
		"## Components\n\n| Id | Kind | Name | Purpose | Parent or joins |\n| --- | --- | --- | --- | --- |\n"+
		"| billing | component | Billing | It bills. | none |\n| ledger | component | Ledger | It records. | billing |\n"+
		"| billing-ledger | interface | Billing to ledger | Billing writes the ledger. | billing, ledger |\n",
		"# Notes\n\n### a-bill-is-sent\n\n- Kind: behavior\n- About: billing\n- Supports: none\n\n"+
			"### a-bill-is-sent-once\n\n- Kind: rule of structure\n- About: billing, ledger\n- Supports: a-bill-is-sent\n")
	if err != nil {
		t.Fatalf("parse the level: %v", err)
	}
	want := Level{
		Statements: []LevelStatement{
			{ID: "a-bill-is-sent", Text: "A bill is sent.", Kind: "behavior", About: []string{"billing"}, Noted: true},
			{ID: "a-bill-is-sent-once", Text: "A bill is sent once.", Parent: "a-bill-is-sent", Kind: "rule of structure",
				About: []string{"billing", "ledger"}, Supports: []string{"a-bill-is-sent"}, Noted: true},
			{ID: "a-ledger-holds-each-bill", Text: "A ledger holds each bill."},
		},
		Components: []specgraph.Component{
			{ID: "billing", Name: "Billing", Purpose: "It bills."},
			{ID: "ledger", Name: "Ledger", Purpose: "It records.", Parent: "billing"},
			{ID: "billing-ledger", Name: "Billing to ledger", Purpose: "Billing writes the ledger.", Interface: true, Joins: []string{"billing", "ledger"}},
		},
	}
	if !reflect.DeepEqual(level, want) {
		t.Errorf("the level reads\n%+v\nwant\n%+v", level, want)
	}
}

// A level is read as a change request's versions are read (specchange.FromVersions), from the
// blocks of the parsed document: a requirement block is a statement wherever the document holds it,
// a quote's included, with the block's own text, and a directive, a heading, a table row or a note
// that a code example shows is none of them. Each case also requires that the change request's
// reader reads the same statements, with the same texts, from the same level.
func TestParseLevelReadsNoCodeExampleAsAStatementHeadingRowOrNote(t *testing.T) {
	t.Parallel()
	const directive = ":::requirement{#a-bill-is-sent}\nA bill is sent.\n:::\n"
	const notes = "# Notes\n\n### a-bill-is-sent\n\n- Kind: behavior\n- About: billing\n- Supports: none\n"
	sent := LevelStatement{ID: "a-bill-is-sent", Text: "A bill is sent.", Kind: "behavior", About: []string{"billing"}, Noted: true}
	for _, c := range []struct {
		name, level, notes string
		want               Level
	}{
		{name: "a directive", level: "# Level\n\n## Statements\n\n" + directive, notes: notes,
			want: Level{Statements: []LevelStatement{sent}}},
		{name: "the same directive in a code example", level: "# Level\n\n## Examples\n\n```markdown\n" + directive + "```\n", notes: notes},
		{name: "the same directive in a quote", notes: notes,
			level: "# Level\n\n## Statements\n\n> :::requirement{#a-bill-is-sent}\n> A bill is sent.\n> :::\n",
			want:  Level{Statements: []LevelStatement{sent}}},
		{name: "a heading in a code example", notes: notes,
			level: "# Level\n\n## Statements\n\n```markdown\n### Under a-parent\n```\n\n" + directive,
			want:  Level{Statements: []LevelStatement{sent}}},
		{name: "a components table in a code example", notes: notes,
			level: "# Level\n\n## Statements\n\n" + directive + "\n```markdown\n## Components\n\n" +
				"| Id | Kind | Name | Purpose | Parent or joins |\n| --- | --- | --- | --- | --- |\n| billing | component | Billing | It bills. | none |\n```\n",
			want: Level{Statements: []LevelStatement{sent}}},
		{name: "a note in a code example", level: "# Level\n\n## Statements\n\n" + directive,
			notes: "# Notes\n\n```markdown\n### a-bill-is-sent\n\n- Kind: behavior\n- About: billing\n- Supports: none\n```\n",
			want:  Level{Statements: []LevelStatement{{ID: "a-bill-is-sent", Text: "A bill is sent."}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			level, err := ParseLevel(c.level, c.notes)
			if err != nil {
				t.Fatalf("parse the level: %v", err)
			}
			if !reflect.DeepEqual(level, c.want) {
				t.Errorf("the level reads\n%+v\nwant\n%+v", level, c.want)
			}
			request, err := specchange.FromVersions("level", specchange.VersionText{Document: "level", Version: 1, Markdown: c.level}, nil)
			if err != nil {
				t.Fatalf("read the level as a change request: %v", err)
			}
			shared := map[string]string{}
			for _, proposal := range request.Proposals {
				if statement, ok := proposal.Node.(specgraph.Statement); ok {
					shared[proposal.ID] = statement.Text
				}
			}
			read := map[string]string{}
			for _, statement := range level.Statements {
				read[statement.ID] = statement.Text
			}
			if !reflect.DeepEqual(read, shared) {
				t.Errorf("the level's reader reads the statements %q, and the change request's reader %q", read, shared)
			}
		})
	}
}

// componentsTable is a level whose `## Components` table holds rows, under the five columns of
// level one's table.
func componentsTable(rows string) string {
	return "# Level\n\n## Components\n\n| Id | Kind | Name | Purpose | Parent or joins |\n| --- | --- | --- | --- | --- |\n" + rows
}

// Each component of a level is read from the cells the document parser gives its row, so a cell
// keeps every character it holds: an escaped pipe stays in its cell and the purpose reads with a
// literal `|`, a code span in the purpose keeps its backticks, and an id in code font is the id.
func TestParseLevelReadsEachComponentFromItsParsedCells(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, row string
		want      specgraph.Component
	}{
		{name: "a purpose of plain words", row: "| billing | component | Billing | It bills. | none |\n",
			want: specgraph.Component{ID: "billing", Name: "Billing", Purpose: "It bills."}},
		{name: "a purpose holding an escaped pipe", row: "| billing | component | Billing | It bills \\| pays. | none |\n",
			want: specgraph.Component{ID: "billing", Name: "Billing", Purpose: "It bills | pays."}},
		{name: "a purpose holding a pipe in code", row: "| billing | component | Billing | It reads `a\\|b`. | none |\n",
			want: specgraph.Component{ID: "billing", Name: "Billing", Purpose: "It reads `a|b`."}},
		{name: "an id and a parent in code font", row: "| `ledger` | component | Ledger | It records. | `billing` |\n",
			want: specgraph.Component{ID: "ledger", Name: "Ledger", Purpose: "It records.", Parent: "billing"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			level, err := ParseLevel(componentsTable(c.row), "")
			if err != nil {
				t.Fatalf("parse the level: %v", err)
			}
			if want := []specgraph.Component{c.want}; !reflect.DeepEqual(level.Components, want) {
				t.Errorf("the level reads the components %+v, want %+v", level.Components, want)
			}
		})
	}
}

// The tables of the Components part are read wherever the part holds them - at its top, in a quote,
// in a list item - as a statement is read wherever the level holds its block, so no table of the
// part is left out.
func TestParseLevelReadsAComponentsTableWhereverThePartHoldsIt(t *testing.T) {
	t.Parallel()
	rows := []string{"| Id | Kind | Name | Purpose | Parent or joins |", "| --- | --- | --- | --- | --- |", "| billing | component | Billing | It bills. | none |"}
	table, quoted, listed := "", "", "- The table:\n\n"
	for _, row := range rows {
		table += row + "\n"
		quoted += "> " + row + "\n"
		listed += "  " + row + "\n"
	}
	want := []specgraph.Component{{ID: "billing", Name: "Billing", Purpose: "It bills."}}
	for _, c := range []struct{ name, part string }{
		{name: "at the top of the part", part: table},
		{name: "in a quote", part: quoted},
		{name: "in a list item", part: listed},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			level, err := ParseLevel("# Level\n\n## Components\n\n"+c.part, "")
			if err != nil {
				t.Fatalf("parse the level: %v", err)
			}
			if !reflect.DeepEqual(level.Components, want) {
				t.Errorf("the level reads the components %+v, want %+v", level.Components, want)
			}
		})
	}
}

// A row of a Components table that reads as no component or interface - a table of another number
// of columns, a kind that is neither `component` nor `interface`, an empty id - is refused as a
// node the approved spec cannot hold (SPEC_NODE_INVALID), naming the row, and never left out.
func TestParseLevelRefusesAComponentRowItCannotRead(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, level, node string
	}{
		{name: "a table of four columns", node: "billing",
			level: "# Level\n\n## Components\n\n| Id | Kind | Name | Purpose |\n| --- | --- | --- | --- |\n| billing | component | Billing | It bills. |\n"},
		{name: "a kind of no component", node: "billing", level: componentsTable("| billing | service | Billing | It bills. | none |\n")},
		{name: "an empty id", node: "-", level: componentsTable("|  | component | Billing | It bills. | none |\n")},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			level, err := ParseLevel(c.level, "")
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("the level reads %+v with the error %v, want a refusal of the row", level, err)
			}
			if refusal.Code != RefusalNodeInvalid || len(refusal.Problems) != 1 || refusal.Problems[0].Node != c.node || refusal.Problems[0].Rule != "bad-component" {
				t.Errorf("the refusal is %s %+v, want %s naming the row of %s under bad-component", refusal.Code, refusal.Problems, RefusalNodeInvalid, c.node)
			}
		})
	}
}

// notedLevel is a level of one statement, a-bill-is-kept, read with notes.
func notedLevel(t *testing.T, notes string) LevelStatement {
	t.Helper()
	level, err := ParseLevel("# Level\n\n## Statements\n\n:::requirement{#a-bill-is-kept}\nA bill is kept.\n:::\n", notes)
	if err != nil {
		t.Fatalf("parse the level: %v", err)
	}
	if len(level.Statements) != 1 {
		t.Fatalf("the level reads the statements %+v, want a-bill-is-kept alone", level.Statements)
	}
	return level.Statements[0]
}

// A note's fields are read from the list items under its heading at every depth - an item of a
// list in an item, an item of a numbered list - and from notes the same id has under two headings.
func TestParseLevelReadsEachNoteFieldAtEveryDepth(t *testing.T) {
	t.Parallel()
	kept := LevelStatement{ID: "a-bill-is-kept", Text: "A bill is kept.", Kind: "behavior", About: []string{"billing"},
		Supports: []string{"a-bill-is-sent"}, Noted: true}
	for _, c := range []struct{ name, notes string }{
		{name: "three items of one list", notes: "# Notes\n\n### a-bill-is-kept\n\n- Kind: behavior\n- About: billing\n- Supports: a-bill-is-sent\n"},
		{name: "about and supports in a list under the kind", notes: "# Notes\n\n### a-bill-is-kept\n\n- Kind: behavior\n  - About: billing\n  - Supports: a-bill-is-sent\n"},
		{name: "a numbered list", notes: "# Notes\n\n### a-bill-is-kept\n\n1. Kind: behavior\n2. About: billing\n3. Supports: a-bill-is-sent\n"},
		{name: "two headings of the id", notes: "# Notes\n\n### a-bill-is-kept\n\n- Kind: behavior\n- About: billing\n\n### a-bill-is-kept\n\n- Supports: a-bill-is-sent\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if statement := notedLevel(t, c.notes); !reflect.DeepEqual(statement, kept) {
				t.Errorf("the statement reads\n%+v\nwant\n%+v", statement, kept)
			}
		})
	}
}

// A field a note writes where no list item of the note holds it - a paragraph of its own, a second
// paragraph of an item, an item in a quote - or writes twice, is one the reader cannot place: the
// statement reads with that field unread and its value empty, so the import holds it for that field
// rather than writing it without the field.
func TestParseLevelMarksANoteFieldItCannotPlaceAsUnread(t *testing.T) {
	t.Parallel()
	unread := LevelStatement{ID: "a-bill-is-kept", Text: "A bill is kept.", Kind: "behavior", Supports: []string{"a-bill-is-sent"},
		Noted: true, Unread: []string{"about"}}
	for _, c := range []struct{ name, notes string }{
		{name: "a paragraph of its own", notes: "# Notes\n\n### a-bill-is-kept\n\n- Kind: behavior\n- Supports: a-bill-is-sent\n\nAbout: billing\n"},
		{name: "a second paragraph of an item", notes: "# Notes\n\n### a-bill-is-kept\n\n- Kind: behavior\n\n  About: billing\n- Supports: a-bill-is-sent\n"},
		{name: "an item in a quote", notes: "# Notes\n\n### a-bill-is-kept\n\n- Kind: behavior\n- Supports: a-bill-is-sent\n\n> - About: billing\n"},
		{name: "the field twice", notes: "# Notes\n\n### a-bill-is-kept\n\n- Kind: behavior\n- About: billing\n- Supports: a-bill-is-sent\n  - About: ledger\n"},
		{name: "the field under two headings", notes: "# Notes\n\n### a-bill-is-kept\n\n- Kind: behavior\n- About: billing\n\n### a-bill-is-kept\n\n- About: billing\n- Supports: a-bill-is-sent\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if statement := notedLevel(t, c.notes); !reflect.DeepEqual(statement, unread) {
				t.Errorf("the statement reads\n%+v\nwant\n%+v", statement, unread)
			}
		})
	}
}

// An import writes each node of the level at an address of its id, so each id the level holds must
// name one node and be one the approved spec can hold: an id that two nodes of the level hold - two
// components, two statements, a statement and a component - an id that is no id, and an id longer
// than an address of the store refuse the level (SPEC_NODE_INVALID), naming the id, whether its node
// would be written or held, and the import writes nothing. A level whose ids each name one node is
// written and held as before.
func TestImportRefusesALevelIDTheStoreCannotHold(t *testing.T) {
	sent := LevelStatement{ID: "a-bill-is-sent", Text: "A bill is sent.", Kind: "behavior", About: []string{"billing"}, Noted: true}
	kept := LevelStatement{ID: "a-bill-is-kept", Text: "A bill is kept."}
	billing := specgraph.Component{ID: "billing", Name: "Billing", Purpose: "It bills each order."}
	ledger := specgraph.Component{ID: "ledger", Name: "Ledger", Purpose: "It records each bill.", Parent: "billing"}
	held := func(id string) LevelStatement { return LevelStatement{ID: id, Text: "A bill is kept."} }
	long := strings.Repeat("a", graphstore.MaxAddressBytes)

	t.Run("each id names one node", func(t *testing.T) {
		database := storetest.Open(t)
		imported, err := importLevel(database, issue(1), Level{Statements: []LevelStatement{sent, kept}, Components: []specgraph.Component{billing, ledger}})
		if err != nil {
			t.Fatalf("import the level: %v", err)
		}
		var written []string
		for _, node := range imported.Written {
			written = append(written, node.ID)
		}
		if slices.Sort(written); !slices.Equal(written, []string{"a-bill-is-sent", "billing", "ledger"}) {
			t.Errorf("the import writes %v, want a-bill-is-sent, billing and ledger", written)
		}
		if holds := heldIDs(t, database); !slices.Equal(holds, []string{"a-bill-is-kept"}) {
			t.Errorf("the import holds %v, want a-bill-is-kept", holds)
		}
	})

	for _, c := range []struct {
		name       string
		level      Level
		node, rule string
	}{
		{name: "two components of one id", node: "billing", rule: "doubled-id", level: Level{Statements: []LevelStatement{sent, kept},
			Components: []specgraph.Component{billing, {ID: "billing", Name: "Billing again", Purpose: "It bills each refund."}}}},
		{name: "two statements of one id", node: "a-bill-is-sent", rule: "doubled-id", level: Level{
			Statements: []LevelStatement{sent, kept, {ID: "a-bill-is-sent", Text: "A bill is sent twice.", Kind: "behavior", Noted: true}},
			Components: []specgraph.Component{billing}}},
		{name: "a statement and a component of one id", node: "billing", rule: "doubled-id", level: Level{
			Statements: []LevelStatement{sent, {ID: "billing", Text: "Billing bills.", Kind: "behavior", Noted: true}},
			Components: []specgraph.Component{billing}}},
		{name: "two held statements of one id", node: "a-bill-is-kept", rule: "doubled-id", level: Level{
			Statements: []LevelStatement{sent, kept, held("a-bill-is-kept")}, Components: []specgraph.Component{billing}}},
		{name: "a held statement whose id is no id", node: "step-3", rule: "bad-id", level: Level{
			Statements: []LevelStatement{sent, held("step-3")}, Components: []specgraph.Component{billing}}},
		{name: "a held statement whose id is longer than an address", node: long, rule: "bad-id", level: Level{
			Statements: []LevelStatement{sent, held(long)}, Components: []specgraph.Component{billing}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			database := storetest.Open(t)
			imported, err := importLevel(database, issue(1), c.level)
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("the import answers %+v with the error %v, want a refusal of the level naming %.40s", imported, err, c.node)
			}
			if refusal.Code != RefusalNodeInvalid || len(refusal.Problems) != 1 || refusal.Problems[0].Node != c.node || refusal.Problems[0].Rule != c.rule {
				t.Errorf("the refusal is %s %.300v, want %s naming %.40s under %s", refusal.Code, refusal.Problems, RefusalNodeInvalid, c.node, c.rule)
			}
			state, err := Read(context.Background(), database.Pool, "CORE", "acme/app")
			if err != nil {
				t.Fatalf("read the approved spec: %v", err)
			}
			if len(state.Nodes) != 0 {
				t.Errorf("the refused import wrote the nodes %.300v", slices.Sorted(maps.Keys(state.Nodes)))
			}
			if holds := heldIDs(t, database); len(holds) != 0 {
				t.Errorf("the refused import holds %v", holds)
			}
		})
	}
}
