package api

import "github.com/sjawhar/envoy/internal/dispatch/model"

// group is one of the three groups that R24 puts each issue status in. The rule holds two
// things between a parent and each of its children: a parent is done only when each child is
// done, and while a child is in work, its parent is in work. The zero group is no group:
// statusGroup gives it for a string that is not a status.
type group string

const (
	// groupNotStarted holds the statuses of work that nobody started: triage, icebox and
	// backlog.
	groupNotStarted group = "not started"
	// groupInWork holds the statuses of work that is planned or that runs: todo, in_progress,
	// testing, needs_review and retro.
	groupInWork group = "in work"
	// groupDone holds done alone.
	groupDone group = "done"
)

// statusGroup is the group of an issue status of model.IssueStatuses.
func statusGroup(status string) group {
	switch status {
	case model.IssueStatusTriage, "icebox", "backlog":
		return groupNotStarted
	case "todo", "in_progress", "testing", "needs_review", "retro":
		return groupInWork
	case "done":
		return groupDone
	}
	return ""
}
