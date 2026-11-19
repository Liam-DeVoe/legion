package refs

import (
	"regexp"
	"strings"
)

// externalRefPattern is a reference to an issue of a GitHub repository, `owner/name#number`: each
// half free of `/` and whitespace, and the name free of `#`.
var externalRefPattern = regexp.MustCompile(`^([^/\s]+)/([^/\s#]+)#([1-9][0-9]*)$`)

// ParseExternalRef reads ref, an `owner/name#number` reference with any space around it, into the
// repository it names in the form Dispatch stores it (ExternalRepo) and its number, and false
// where ref has not that shape. It is the one reading of such a reference: the API reads an
// issue's reference and a settings route's path through it (api's externalRef, and through it
// storedRepoProjectPath), and the CI reconcile reads a stored mapping's name back through it, so
// the request its warning names reaches the row the route will read.
func ParseExternalRef(ref string) (repo, number string, ok bool) {
	match := externalRefPattern.FindStringSubmatch(strings.TrimSpace(ref))
	if match == nil {
		return "", "", false
	}
	return ExternalRepo(match[1], match[2]), match[3], true
}

// ExternalRepo is the repository owner/repo in the form Dispatch stores one it reads from a
// reference or a path: each half trimmed of surrounding space and lower-cased, and one trailing
// `.git` trimmed off the name.
func ExternalRepo(owner, repo string) string {
	owner = strings.ToLower(strings.TrimSpace(owner))
	repo = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(repo)), ".git")
	return owner + "/" + repo
}
