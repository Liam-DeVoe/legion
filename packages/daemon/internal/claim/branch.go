package claim

import (
	"regexp"
	"strings"
)

// issueBranchPrefix begins the GitHub branch of every issue Legion works.
const issueBranchPrefix = "legion/"

// IssueBranch is the GitHub branch issue is worked on, legion/<KEY>: the jj bookmark of its
// workspace, the branch the daemon creates for it, and the head branch of its pull request.
func IssueBranch(issue string) string { return issueBranchPrefix + issue }

// BranchIssue is the issue whose branch branch is, and whether it is one: the issue's own branch
// (IssueBranch) and the branch of each task of its plan (TaskBranch) are the issue's. A branch of
// any other shape (BranchTask) is no issue's, and no pull request Legion records is on it, so a
// GitHub event about it changes nothing the daemon keeps.
func BranchIssue(branch string) (string, bool) {
	issue, _, ok := BranchTask(branch)
	return issue, ok
}

// taskSlug is the shape of a task's slug in its branch and its directory: a lower-case letter or
// digit, then at most 39 lower-case letters, digits and hyphens. It holds no dot, so the first dot
// of a task's branch ends the issue key, and every slug is a valid component of a git ref and of a
// path.
var taskSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// IsTaskSlug reports whether slug is the shape a task's slug has (taskSlug).
func IsTaskSlug(slug string) bool { return taskSlug.MatchString(slug) }

// TaskBranch is the GitHub branch of a task of issue's plan: the issue's own branch (IssueBranch)
// for slug "", legion/<issue>.<slug> otherwise.
func TaskBranch(issue, slug string) string {
	if slug == "" {
		return IssueBranch(issue)
	}
	return IssueBranch(issue) + "." + slug
}

// BranchTask is the issue and the slug of a task's branch (TaskBranch), and whether branch is one:
// slug is "" for the issue's own branch. An issue key holds no dot, so the first dot after
// legion/<KEY> ends the key; a branch whose part after it is no slug (IsTaskSlug) is no task's.
func BranchTask(branch string) (issue, slug string, ok bool) {
	name, ok := strings.CutPrefix(branch, issueBranchPrefix)
	if !ok {
		return "", "", false
	}
	key, slug, dotted := strings.Cut(name, ".")
	if !IsIssueKey(key) || dotted && !IsTaskSlug(slug) {
		return "", "", false
	}
	return key, slug, true
}
