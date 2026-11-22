package specstore

import (
	"reflect"
	"testing"

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
