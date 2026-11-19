package refs

import "testing"

// A repository is read as Dispatch stores it: lower-cased, and one trailing `.git` trimmed off the
// name. Each half is free of `/` and whitespace and the name free of `#`, so `acme/widgets#5`,
// which names an issue, is no repository, and neither is a name with a space in or around it or a
// third segment: a caller that takes a value a person wrote trims it first, so each caller keeps
// the reading it had when it read the value as a reference.
//
// The negative controls are a reading that lets `#` into the name, which takes `acme/widgets#5`,
// and one that trims the space around the value, which takes `acme/x ` and ` acme/x`.
func TestParseExternalRepoReadsARepositoryAsDispatchStoresIt(t *testing.T) {
	for _, row := range []struct {
		raw, want string
	}{
		{raw: "acme/widgets", want: "acme/widgets"},
		{raw: "Acme/Widgets.git", want: "acme/widgets"},
		{raw: " acme/x"},
		{raw: "acme/x "},
		{raw: "acme/x\t"},
		{raw: "acme/x.git.git", want: "acme/x.git"},
		{raw: "mona-cat_octo/x", want: "mona-cat_octo/x"},
		{raw: "acme/widgets#5"},
		{raw: "acme/wid gets"},
		{raw: "acme/widgets/extra"},
		{raw: "acme/"},
		{raw: "/widgets"},
		{raw: "acme"},
		{raw: ""},
	} {
		got, ok := ParseExternalRepo(row.raw)
		if ok != (row.want != "") || got != row.want {
			t.Errorf("ParseExternalRepo(%q) = %q, %v; want %q, %v", row.raw, got, ok, row.want, row.want != "")
		}
	}
}

// An issue reference is a repository ParseExternalRepo reads and a number after its last `#`,
// with the space around the whole reference trimmed and none inside it.
func TestParseExternalRefReadsItsRepositoryAsParseExternalRepoDoes(t *testing.T) {
	for _, row := range []struct {
		ref, repo, number string
	}{
		{ref: "Acme/Widgets.git#12", repo: "acme/widgets", number: "12"},
		{ref: " acme/widgets#3\n", repo: "acme/widgets", number: "3"},
		{ref: "acme/widgets #3"},
		{ref: "acme/widgets#0"},
		{ref: "acme/widgets#1#2"},
		{ref: "acme/widgets"},
		{ref: "#1"},
	} {
		repo, number, ok := ParseExternalRef(row.ref)
		if ok != (row.repo != "") || repo != row.repo || number != row.number {
			t.Errorf("ParseExternalRef(%q) = %q, %q, %v; want %q, %q, %v", row.ref, repo, number, ok, row.repo, row.number, row.repo != "")
		}
	}
}
