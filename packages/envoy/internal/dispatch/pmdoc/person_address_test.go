package pmdoc

import (
	"strings"
	"testing"
)

// A person's address is `dispatch://person/<login>`, and a login is often an email address, so
// its local part can hold a dot, a hyphen or a plus (INFRA-1702 R1, R5, R8). The address is one
// reference, read whole from the text: neither reader links an email it finds inside it. The
// GFM email literal begins after any punctuation, so `john.doe@example.com` behind
// `dispatch://person/` must not become a link to `mailto:doe@example.com`, which would cut the
// address in two and leave a reader nothing whole to name the person by. Each row is a paragraph
// holding one such address; the stored reading is the paragraph's text as written, with no link
// in it, in Go and in the browser editor's engine alike.
func TestABarePersonAddressIsOneTextAndNoMailLink(t *testing.T) {
	rows := []string{
		"Ask dispatch://person/john.doe@example.com now.\n",
		"Ask dispatch://person/person-2@example.com now.\n",
		"Ask dispatch://person/a.b-c+d@x-y.test now.\n",
		// No punctuation in the local part: the `/` before it already keeps it whole.
		"Ask dispatch://person/sami@theorem.dev now.\n",
	}
	docs := make([]*Node, len(rows))
	for index, markdown := range rows {
		doc, err := Parse(markdown)
		if err != nil {
			t.Fatalf("Parse(%q) = %v", markdown, err)
		}
		docs[index] = doc
		want := strings.TrimSuffix(markdown, "\n")
		if got := inlineSummaries(doc); got != `text "`+want+`"` {
			t.Errorf("%q: Go reads %s, want the paragraph as one text with no link", markdown, got)
		}
	}
	readings := engineReadings(t, rows)
	for index, markdown := range rows {
		if reading := readings[index]; reading == nil || !docs[index].Equal(reading) {
			t.Errorf("%q: Go reads %s, the engine %s", markdown, inlineSummaries(docs[index]), inlineSummaries(reading))
		}
	}
}
