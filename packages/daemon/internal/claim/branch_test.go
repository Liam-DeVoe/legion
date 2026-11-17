package claim

import (
	"regexp"
	"testing"
)

// anyIssueKey is a Dispatch key of any project in any letter case, the widest reading of a key in
// a branch name: Dispatch's own attribution upper-cases a head branch before it looks for keys in
// it, and a project key is a letter followed by up to nine letters or digits.
var anyIssueKey = regexp.MustCompile(`(?i)[A-Z][A-Z0-9]{0,9}-[0-9]+`)

// A new issue branch names no issue: the prefix and 16 lowercase hex digits, with no hyphen, so no
// key of any project shape is in it in any letter case, and two draws differ. The control is a
// backfilled name, legion/<KEY>, which the same pattern does find a key in.
func TestANewIssueBranchCarriesNoIssueKey(t *testing.T) {
	newShape := regexp.MustCompile(`^legion/[0-9a-f]{16}$`)
	seen := map[string]bool{}
	for range 10000 {
		branch, err := NewIssueBranch()
		if err != nil {
			t.Fatalf("NewIssueBranch: %v", err)
		}
		if !newShape.MatchString(branch) {
			t.Fatalf("NewIssueBranch() = %q, want legion/<16 lowercase hex>", branch)
		}
		if key := anyIssueKey.FindString(branch); key != "" {
			t.Fatalf("NewIssueBranch() = %q carries the key shape %q", branch, key)
		}
		if !IsIssueBranch(branch) {
			t.Fatalf("IsIssueBranch(%q) = false for a name NewIssueBranch drew", branch)
		}
		if seen[branch] {
			t.Fatalf("NewIssueBranch() drew %q twice", branch)
		}
		seen[branch] = true
	}
	if key := anyIssueKey.FindString("legion/ACME-208"); key != "ACME-208" {
		t.Fatalf("the key pattern found %q in legion/ACME-208, want ACME-208: it cannot see a key at all", key)
	}
}

// The shape test accepts both names the store keeps — a new one and a backfilled legion/<KEY> —
// and nothing a revset quote or a second path segment could break, and nothing outside legion/.
func TestIsIssueBranchAcceptsTheTwoStoredShapesOnly(t *testing.T) {
	for _, branch := range []string{"legion/0123456789abcdef", "legion/ACME-1", "legion/A1-9000"} {
		if !IsIssueBranch(branch) {
			t.Fatalf("IsIssueBranch(%q) = false, want true", branch)
		}
	}
	for _, branch := range []string{
		"main",
		"legion",
		"legion/",
		"legion/-1",
		"legion/ACME-1/x",
		`legion/ACME-1"`,
		"legion/a b",
		"release/legion/ACME-1",
		"",
	} {
		if IsIssueBranch(branch) {
			t.Fatalf("IsIssueBranch(%q) = true, want false", branch)
		}
	}
}
