package claim

import (
	"fmt"
	"testing"
)

// An issue's branch and the issue of a branch are one rule read in both directions: every branch
// IssueBranch builds names its issue again, and a branch of any other shape is no issue's, so the
// daemon neither records nor acts on a pull request or a push there.
func TestIssueBranchAndBranchIssueAreOneRule(t *testing.T) {
	for _, issue := range []string{"ACME-1", "ACME-208", "A1-9000"} {
		branch := IssueBranch(issue)
		if branch != "legion/"+issue {
			t.Fatalf("IssueBranch(%q) = %q, want legion/%s", issue, branch, issue)
		}
		if got, ok := BranchIssue(branch); !ok || got != issue {
			t.Fatalf("BranchIssue(%q) = %q, %t; want %q, true", branch, got, ok, issue)
		}
	}
	for _, branch := range []string{
		"main",
		"legion",
		"legion/",
		"legion/main",
		"legion/acme-1",
		"legion/ACME-1/x",
		"legion/ACME-",
		"legion/-1",
		"release/legion/ACME-1",
		"",
	} {
		if issue, ok := BranchIssue(branch); ok {
			t.Fatalf("BranchIssue(%q) = %q, true; want no issue", branch, issue)
		}
	}
}

// A task of an issue's plan has a branch of its own: the issue's branch for no slug, the issue's
// branch, a dot and the slug otherwise. Both forms name their issue, so each GitHub event on a
// task's branch reaches its issue, and a branch whose part after the dot no slug can be is no
// task's and no issue's.
func TestATaskBranchNamesItsIssueAndSlug(t *testing.T) {
	if got := TaskBranch("INFRA-1", ""); got != "legion/INFRA-1" {
		t.Fatalf("TaskBranch(no slug) = %q", got)
	}
	if got := TaskBranch("INFRA-1", "d"); got != "legion/INFRA-1.d" {
		t.Fatalf("TaskBranch(d) = %q", got)
	}
	for branch, want := range map[string][3]string{
		"legion/INFRA-1":                {"INFRA-1", "", "true"},
		"legion/INFRA-1.d":              {"INFRA-1", "d", "true"},
		"legion/INFRA-1.t-0123456789ab": {"INFRA-1", "t-0123456789ab", "true"},
		"legion/INFRA-1.":               {"", "", "false"},
		"legion/INFRA-1.D":              {"", "", "false"},
		"legion/INFRA-1.d.e":            {"", "", "false"},
		"main":                          {"", "", "false"},
	} {
		issue, slug, ok := BranchTask(branch)
		if issue != want[0] || slug != want[1] || fmt.Sprint(ok) != want[2] {
			t.Errorf("BranchTask(%q) = %q, %q, %v; want %v", branch, issue, slug, ok, want)
		}
		key, isIssue := BranchIssue(branch)
		if isIssue != (want[2] == "true") || key != want[0] {
			t.Errorf("BranchIssue(%q) = %q, %v", branch, key, isIssue)
		}
	}
}
