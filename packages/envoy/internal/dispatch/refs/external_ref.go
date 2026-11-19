package refs

import (
	"regexp"
	"strings"
)

var (
	// externalRepoPattern is a repository of GitHub, `owner/name`: each half free of `/` and
	// whitespace, and the name free of `#`.
	externalRepoPattern = regexp.MustCompile(`^([^/\s]+)/([^/\s#]+)$`)
	// externalRefPattern is a reference to an issue of such a repository, `<repository>#number`:
	// the repository free of whitespace, which ParseExternalRepo reads, and the number after the
	// last `#`, since the repository's name holds none.
	externalRefPattern = regexp.MustCompile(`^(\S*)#([1-9][0-9]*)$`)
)

// ParseExternalRepo reads raw, an `owner/name` with no space in or around it, into the repository
// it names in the form Dispatch stores it (ExternalRepo), and false where raw has not that shape.
// It trims nothing: a caller that takes a value a person wrote trims it first, as each did when it
// read the value as the reference `<value>#1`. It is the one reading of a repository a person
// writes: the API reads an issue's repository, a source's, a mapping's path and the boot seed's
// mappings through it, and the repairs the CI reconcile's warning names (api's MappingRepairs)
// read a stored mapping's name back through it, so the request the warning names reaches the row
// the route will read.
func ParseExternalRepo(raw string) (repo string, ok bool) {
	match := externalRepoPattern.FindStringSubmatch(raw)
	if match == nil {
		return "", false
	}
	return ExternalRepo(match[1], match[2]), true
}

// ParseExternalRef reads ref, an `owner/name#number` reference with any space around it, into the
// repository it names (ParseExternalRepo) and its number, and false where ref has not that shape.
// It is the one reading of such a reference: the API reads an issue's reference through it (api's
// externalRef).
func ParseExternalRef(ref string) (repo, number string, ok bool) {
	match := externalRefPattern.FindStringSubmatch(strings.TrimSpace(ref))
	if match == nil {
		return "", "", false
	}
	if repo, ok = ParseExternalRepo(match[1]); !ok {
		return "", "", false
	}
	return repo, match[2], true
}

// ExternalRepo is the repository owner/repo in the form Dispatch stores one it reads from a
// reference or a path: each half trimmed of surrounding space and lower-cased, and one trailing
// `.git` trimmed off the name.
func ExternalRepo(owner, repo string) string {
	owner = strings.ToLower(strings.TrimSpace(owner))
	repo = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(repo)), ".git")
	return owner + "/" + repo
}
