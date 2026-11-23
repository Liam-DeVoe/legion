package main

import (
	"regexp"
	"strconv"
	"strings"
)

// ghShown is value, a value a person gave legion gh — an argument, an option's value, a selector,
// an environment value, a search query — as a legion gh refusal may print it: quoted where its slot
// expects one kind of identifier (form) and value has that kind's strict form, and otherwise place,
// the words that name its kind and where it stands ("the 2nd argument", "a -R value", "the search
// query's 3rd word"). The form is the slot's, never whichever one value happens to match: a --body
// value or a search word that reads as a login is named by its place all the same, since its slot
// takes no login, and a slot that takes no identifier passes no form.
//
// It is the one place a refusal of legion gh prints such a value: no refusal formats one itself,
// and each call names its place with a literal of its own, by which TestNoRefusalPathPrintsACanary
// finds every call in the source and drives each with a canary in every slot.
func ghShown(value, place string, form ...ghForm) string {
	for _, admits := range form {
		if admits(value) {
			return strconv.Quote(value)
		}
	}
	return place
}

// ghForm is the strict form of the one kind of identifier a slot takes (ghShown): a GitHub login
// for an owner's slot, OWNER/REPO for a repository's, a subcommand gh's own help lists for a
// subcommand's, exactly github.com for a host's.
type ghForm func(value string) bool

var (
	// ghLoginShape is a GitHub login: a letter or digit, then up to 38 letters, digits or "-".
	ghLoginShape = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	// ghRepositoryNameShape is the name of a GitHub repository: letters, digits, ".", "_" and "-".
	ghRepositoryNameShape = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// ghLogin is an owner's slot's form: a GitHub login.
func ghLogin(value string) bool { return ghLoginShape.MatchString(value) }

// ghRepository is a repository's slot's form: OWNER/REPO, the owner a login and the name of the
// characters GitHub takes in one.
func ghRepository(value string) bool {
	owner, name, ok := strings.Cut(value, "/")
	return ok && ghLogin(owner) && ghRepositoryNameShape.MatchString(name)
}

// ghGitHubHost is a host's slot's form: github.com exactly.
func ghGitHubHost(value string) bool { return value == "github.com" }

// ghListedIn is a subcommand's slot's form: a word that gh's own help of the command before it,
// read at run time, lists. A help that could not be read lists none.
func ghListedIn(listed map[string]bool) ghForm {
	return func(value string) bool { return listed[value] }
}
