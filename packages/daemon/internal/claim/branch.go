package claim

import (
	"regexp"
	"slices"
	"strings"
)

// issueBranchPrefix begins the GitHub branch of every issue Legion works.
const issueBranchPrefix = "legion/"

// IssueBranch is the GitHub branch issue is worked on, legion/<KEY>: the jj bookmark of its
// workspace, the branch the daemon creates for it, and the head branch of its pull request.
func IssueBranch(issue string) string { return issueBranchPrefix + issue }

// BranchIssue is the issue whose branch branch is, and whether it is one: the issue's own branch
// (IssueBranch) and the branch of each task of its plan (BranchTask) are the issue's. A branch of
// any other shape is no issue's, and no pull request Legion records is on it, so a GitHub event
// about it changes nothing the daemon keeps.
func BranchIssue(branch string) (string, bool) {
	issue, _, ok := BranchTask(branch)
	return issue, ok
}

// BranchTask is the issue of a task's branch and the task's part of it, and whether branch is one.
// part is "" for the issue's own branch (IssueBranch), the task's slug for legion/<KEY>.<slug>, and
// the slug, a dot and a number for legion/<KEY>.<slug>.<n>, the branch of a later task of one
// generation of the plan that takes the same slug (laterGive). An issue key holds no dot
// (IsIssueKey), so the first dot after legion/<KEY> ends the key; a branch whose part after it is
// neither a slug (isTaskSlug) nor a later give of one is no task's.
func BranchTask(branch string) (issue, part string, ok bool) {
	name, ok := strings.CutPrefix(branch, issueBranchPrefix)
	if !ok {
		return "", "", false
	}
	key, part, dotted := strings.Cut(name, ".")
	if !IsIssueKey(key) || dotted && !isTaskPart(part) {
		return "", "", false
	}
	return key, part, true
}

// taskSlug is the shape of a task's slug in its branch: a lower-case letter or digit, then at most
// 39 lower-case letters, digits and hyphens. It holds no dot, so the first dot of a task's branch
// ends the issue key, and a slug is a component of a git ref and of a path. A word of that shape
// can still be no slug (notSlugs).
var taskSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// notSlugs are the words of a slug's shape (taskSlug) that no slug may be, each for its reason.
var notSlugs = []string{
	// git refuses a ref whose name ends in `.lock` (git check-ref-format), so legion/<KEY>.lock
	// is no branch GitHub or jj can hold.
	"lock",
	// internal/workspace renames a working copy it removes to its directory and `.removing`
	// (removingSuffix), so a task's working copy named for this slug beside the issue's own would be
	// the path the issue's working copy is deleted from.
	"removing",
	// `<branch>.join` is kept for the branch on which the work of a task's several needs is joined:
	// the base of that task's pull request, never a head, so a push there is a push to a base
	// (intake's BasePush) and legion/<KEY>.join is no task's branch.
	"join",
}

// laterGive is a task's part after the issue key when a later task of one generation of the plan
// takes a slug an earlier task of it took: the slug, a dot and n, the slug's n-th give in that
// generation, 2 or more, written with no leading zero. A slug holds no dot, so this part holds
// exactly one and a slug's own part none: no slug's own branch is a later give of another slug.
var laterGive = regexp.MustCompile(`^([^.]+)\.(?:[2-9]|[1-9][0-9]+)$`)

// isTaskSlug reports whether slug is a task's slug: of a slug's shape (taskSlug) and none of the
// words no slug may be (notSlugs).
func isTaskSlug(slug string) bool {
	return taskSlug.MatchString(slug) && !slices.Contains(notSlugs, slug)
}

// isTaskPart reports whether part, a task's branch after legion/<KEY>., is a slug (isTaskSlug) or
// a later give of one (laterGive).
func isTaskPart(part string) bool {
	if isTaskSlug(part) {
		return true
	}
	given := laterGive.FindStringSubmatch(part)
	return given != nil && isTaskSlug(given[1])
}
