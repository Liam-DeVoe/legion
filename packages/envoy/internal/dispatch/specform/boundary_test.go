package specform

import (
	"regexp"
	"slices"
	"strconv"
	"testing"
)

// quotedName is the first quoted string of a finding's detail: the form that the rule read.
var quotedName = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// referenceNames is "<rule> <form>" for each finding of the reference rules on body, in order.
// The text is a name, so no rule of a sentence reads it.
func referenceNames(t *testing.T, body string) []string {
	t.Helper()
	var names []string
	for _, finding := range Check(planted(), Input{Texts: []Text{{Node: "n", Kind: KindName, Body: body, Where: where}}}) {
		name, err := strconv.Unquote(quotedName.FindString(finding.Detail))
		if err != nil {
			t.Fatalf("the detail %q quotes no form: %v", finding.Detail, err)
		}
		names = append(names, string(finding.Rule)+" "+name)
	}
	return names
}

// Each form of a reference rule is a whole word: a letter, a digit or "_" right before it or right
// after it joins it to a longer word, which names nothing, and so do the characters that the form
// names below. For each form, the form alone, joined to a word on its left and joined to a word on
// its right. Where a letter is part of the form itself (the local part and the domain of an email
// address), the joined text is a longer form of the same kind.
func TestEachReferenceFormIsAWholeWord(t *testing.T) {
	const id = "0195f3c2-7b1d-7c3e-9a4f-2b6d8e1f0a37"
	for _, test := range []struct {
		form, body string
		want       []string
	}{
		{"a key", "ACME-12", []string{"dispatch-link ACME-12"}},
		{"a key", "xACME-12", nil},
		{"a key", "ACME-12x", nil},
		{"a key", "ACME-12.", []string{"dispatch-link ACME-12"}},
		{"an id", id, []string{"dispatch-link " + id}},
		{"an id", "x" + id, nil},
		{"an id", id + "x", nil},
		{"incident number", "incident number 12", []string{"incident incident number 12"}},
		{"incident number", "xincident number 12", nil},
		{"incident number", "incident number 12x", nil},
		{"incident #", "incident#12", []string{"incident incident#12"}},
		{"incident #", "incident #12x", nil},
		{"an incident's date", "incident 2026-11-17", []string{"incident incident 2026-11-17"}},
		{"an incident's date", "incident 2026-11-17x", nil},
		{"an incident's key", "incident sev-12", []string{"incident incident sev-12"}},
		{"an incident's key", "incident sev-12x", nil},
		{"INC-<n>", "INC-12", []string{"incident INC-12"}},
		{"INC-<n>", "xINC-12", nil},
		{"INC-<n>", "INC-12x", nil},
		{"pull request number", "PR number 12", []string{"pull-request PR number 12"}},
		{"pull request number", "xPR number 12", nil},
		{"pull request number", "PR number 12x", nil},
		{"pull request #", "PR#12", []string{"pull-request PR#12"}},
		{"pull request #", "pull request #12", []string{"pull-request pull request #12"}},
		{"pull request #", "PR #12x", nil},
		{"<owner>/<repo>#<n>", "acme/widgets#12", []string{"pull-request acme/widgets#12"}},
		{"<owner>/<repo>#<n>", "docs/acme/widgets#12", nil},
		{"<owner>/<repo>#<n>", "acme/widgets#12x", nil},
		{"#<n>", "#12", []string{"pull-request #12"}},
		{"#<n>", "(#12).", []string{"pull-request #12"}},
		{"#<n>", "x#12", nil},
		{"#<n>", "#12x", nil},
		{"#<n>, a colour of six digits and letters", "#12abcd", nil},
		{"#<n>, a colour of six digits and letters", "#123abc", nil},
		{"#<n>, the stated limit: a colour of three digits", "#123", []string{"pull-request #123"}},
		{"the address of a pull request", "github.com/o/r/pull/12", []string{"pull-request github.com/o/r/pull/12"}},
		{"the address of a pull request", "xgithub.com/o/r/pull/12", nil},
		{"the address of a pull request", "github.com/o/r/pull/12draft", nil},
		{"the address of a pull request", "github.com/o/r/pull/12/files", []string{"pull-request github.com/o/r/pull/12"}},
		{"an email address", "ada@example.com", []string{"person ada@example.com"}},
		{"an email address", "xada@example.com", []string{"person xada@example.com"}},
		{"an email address", "ada@example.comx", []string{"person ada@example.comx"}},
		{"an email address", "ada@example.com5", nil},
		{"a handle", "@legion", []string{"person @legion"}},
		{"a handle", "x@legion", nil},
		{"a handle", "@legion/core", nil},
		{"a handle, an npm scope", "@types/node", nil},
	} {
		if got := referenceNames(t, test.body); !slices.Equal(got, test.want) {
			t.Errorf("%s: %q gives %q, want %q", test.form, test.body, got, test.want)
		}
	}
}

// A bare key or id names a record of Dispatch and links to none: the finding says so. A reference
// links to the record.
func TestABareKeyOrIdNamesARecordAndLinksToNone(t *testing.T) {
	const id = "12345678-1234-1234-1234-123456789abc"
	for _, test := range []struct{ body, detail string }{
		{"Dispatch stores the value `" + id + "`.", `it names a record of Dispatch by its id: "` + id + `"`},
		{"Dispatch keeps ACME-3 for each reader.", `it names a record of Dispatch by its key: "ACME-3"`},
		{"Dispatch keeps dispatch://ACME-4 for each reader.", `it links to a record of Dispatch: "dispatch://ACME-4"`},
	} {
		text := statement(test.body)
		if got, want := Check(planted(), Input{Texts: []Text{text}}), []Finding{finding(text, "dispatch-link", test.detail)}; !sameFindings(got, want) {
			t.Errorf("Check(%q) = %#v, want %#v", test.body, got, want)
		}
	}
}
