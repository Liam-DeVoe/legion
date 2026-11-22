package specstore

import (
	"reflect"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/specchange"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
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
