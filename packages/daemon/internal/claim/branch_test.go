package claim

import (
	"strings"
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

// A task of an issue's plan works on a branch of its own: the issue's branch, a dot and the task's
// slug, and for a later task of one generation that takes the same slug that branch, a dot and the
// give's number, 2 or more. Each form is the issue's, so a pull request or a push there is the
// issue's as one on legion/<KEY> is, and BranchTask answers the task's part after the key. A branch
// whose part after the key is none of the forms is no task's and no issue's: an empty slug, a
// capital letter, a leading hyphen, a slash, a slug past 40 characters, a second word, a number
// below 2 or with a leading zero, and the words no slug may be (notSlugs).
func TestATaskBranchNamesItsIssueAndSlug(t *testing.T) {
	longest := strings.Repeat("d", 40)
	for _, c := range []struct {
		branch, issue, part string
		ok                  bool
	}{
		{"legion/ACME-1", "ACME-1", "", true},
		{"legion/ACME-1.d", "ACME-1", "d", true},
		{"legion/ACME-1.t-0123456789ab", "ACME-1", "t-0123456789ab", true},
		{"legion/ACME-1.d-2", "ACME-1", "d-2", true},
		{"legion/ACME-1.d.2", "ACME-1", "d.2", true},
		{"legion/ACME-1.d.10", "ACME-1", "d.10", true},
		{"legion/ACME-1." + longest, "ACME-1", longest, true},
		{"legion/ACME-1.", "", "", false},
		{"legion/ACME-1.D", "", "", false},
		{"legion/ACME-1.-d", "", "", false},
		{"legion/ACME-1.d/x", "", "", false},
		{"legion/ACME-1." + longest + "d", "", "", false},
		{"legion/ACME-1.d.e", "", "", false},
		{"legion/ACME-1.d.1", "", "", false},
		{"legion/ACME-1.d.02", "", "", false},
		{"legion/ACME-1.d.2.3", "", "", false},
		{"legion/ACME-1..2", "", "", false},
		{"legion/ACME-1.lock", "", "", false},
		{"legion/ACME-1.removing", "", "", false},
		{"legion/ACME-1.removing.2", "", "", false},
		{"legion/ACME-1.join", "", "", false},
		{"legion/ACME-1.join.2", "", "", false},
		{"legion/acme-1.d", "", "", false},
		{"legion/.d", "", "", false},
		{"release/legion/ACME-1.d", "", "", false},
	} {
		if issue, part, ok := BranchTask(c.branch); issue != c.issue || part != c.part || ok != c.ok {
			t.Errorf("BranchTask(%q) = %q, %q, %t; want %q, %q, %t", c.branch, issue, part, ok, c.issue, c.part, c.ok)
		}
		if issue, ok := BranchIssue(c.branch); issue != c.issue || ok != c.ok {
			t.Errorf("BranchIssue(%q) = %q, %t; want %q, %t", c.branch, issue, ok, c.issue, c.ok)
		}
	}
}
