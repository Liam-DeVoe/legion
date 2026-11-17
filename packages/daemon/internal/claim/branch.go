package claim

import (
	"regexp"
	"strconv"
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
// path but one: git refuses a ref whose name ends in `.lock`, so `lock` is no slug (IsTaskSlug).
var taskSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// laterGive is the part of a task's branch after the issue key for a later give of a slug in one
// generation (TaskBranchOfGive): the slug, a dot and the give's number, 2 or more, written with no
// leading zero. A slug holds no dot, so this part holds exactly one and a first give's part none:
// no first give of any slug is a later give of another.
var laterGive = regexp.MustCompile(`^([^.]+)\.([2-9]|[1-9][0-9]+)$`)

// IsTaskSlug reports whether slug is the shape a task's slug has (taskSlug).
func IsTaskSlug(slug string) bool { return slug != "lock" && taskSlug.MatchString(slug) }

// TaskBranch is the GitHub branch of a task of issue's plan: the issue's own branch (IssueBranch)
// for slug "", legion/<issue>.<slug> otherwise.
func TaskBranch(issue, slug string) string {
	if slug == "" {
		return IssueBranch(issue)
	}
	return IssueBranch(issue) + "." + slug
}

// TaskBranchOfGive is the branch of the give-th name slug takes in one generation of issue's plan:
// the slug's own branch (TaskBranch) for the first give, legion/<issue>.<slug>.<give> from the
// second on. A branch given to a task is never given to another task of its generation, so a task
// whose slug's branch went to a task that was then removed takes a later give.
func TaskBranchOfGive(issue, slug string, give int) string {
	if give <= 1 {
		return TaskBranch(issue, slug)
	}
	return TaskBranch(issue, slug) + "." + strconv.Itoa(give)
}

// BranchTask is the issue of a task's branch and the task's part of it, and whether branch is one:
// slug is "" for the issue's own branch, the slug for a slug's first give (TaskBranch) and the
// slug, a dot and the number for a later give (TaskBranchOfGive), so TaskBranch(issue, slug) is
// branch again. An issue key holds no dot, so the first dot after legion/<KEY> ends the key; a
// branch whose part after it is neither a slug (IsTaskSlug) nor a later give of one is no task's.
func BranchTask(branch string) (issue, slug string, ok bool) {
	name, ok := strings.CutPrefix(branch, issueBranchPrefix)
	if !ok {
		return "", "", false
	}
	key, slug, dotted := strings.Cut(name, ".")
	if !IsIssueKey(key) || dotted && !isTaskPart(slug) {
		return "", "", false
	}
	return key, slug, true
}

// isTaskPart reports whether part, a task's branch after legion/<KEY>., is a slug (IsTaskSlug) or
// a later give of one (laterGive).
func isTaskPart(part string) bool {
	if IsTaskSlug(part) {
		return true
	}
	given := laterGive.FindStringSubmatch(part)
	return given != nil && IsTaskSlug(given[1])
}
