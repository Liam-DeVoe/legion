package leakguard

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// The fixture rules compile, and they are the four leakguardtest.Rules holds: the host, the pair,
// the word in a text and the word in a path. A rule file that did not compile would be a panic
// inside the process that read it rather than a refusal, which is why LoadRules recovers one; this
// is the test that says the fixture every other test runs needs no recovery.
func TestTheFixtureRulesCompile(t *testing.T) {
	rules := fixtureRules(t)
	ids := make([]string, 0, len(rules.detector.Config.Rules))
	for id := range rules.detector.Config.Rules {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if want := []string{"host_rule", "pair_rule", "word_path_rule", "word_rule"}; !slices.Equal(ids, want) {
		t.Errorf("the rules are %q, want %q", ids, want)
	}
	if rules.Count() != len(ids) {
		t.Errorf("the set counts %d rules, want %d", rules.Count(), len(ids))
	}
}

// Each fixture rule refuses what it is written for and nothing beside it: the host and not the
// example machine, the pair on one line and not on two, the word as a word and not inside another,
// and the word in a path by the path rule.
func TestTheFixtureRulesMatchWhatTheyAreWrittenFor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record Record
		// want is the report, "<record>: <class>" a line, and empty where the record passes.
		want []string
	}{
		{
			name:   "a machine of the invented tailnet",
			record: Record{Label: "the title", Text: "a machine on " + leakguardtest.RulesHost},
			want:   []string{"the title: " + leakguardtest.RulesHostClass},
		},
		{
			name:   "the example machine",
			record: Record{Label: "the title", Text: "a machine on " + leakguardtest.AllowedHost},
		},
		{
			name:   "the pair on one line",
			record: Record{Label: "the body", Text: strings.ToUpper(leakguardtest.PairName) + " keeps the " + leakguardtest.PairWord + " ledger"},
			want:   []string{"the body: " + leakguardtest.PairClass},
		},
		{
			name:   "the pair on two lines",
			record: Record{Label: "the body", Text: leakguardtest.PairName + " keeps\nthe " + leakguardtest.PairWord + " ledger"},
		},
		{
			name:   "the pair's name alone",
			record: Record{Label: "the body", Text: "a note on " + leakguardtest.PairName},
		},
		{
			name:   "the word as a word",
			record: Record{Label: "the body", Text: "the " + strings.ToUpper(leakguardtest.Word) + " runbook"},
			want:   []string{"the body: " + leakguardtest.WordClass},
		},
		{
			name:   "the word inside another word",
			record: Record{Label: "the body", Text: "a " + leakguardtest.Word + "s ledger"},
		},
		{
			name:   "the word in a path",
			record: Record{Label: "docs/" + leakguardtest.Word + "-notes.md", Text: "docs/" + leakguardtest.Word + "-notes.md"},
			want: []string{
				"docs/" + leakguardtest.Word + "-notes.md: " + leakguardtest.WordClass,
				"docs/" + leakguardtest.Word + "-notes.md: " + leakguardtest.WordPathClass,
			},
		},
		{
			name:   "ordinary prose",
			record: Record{Label: "docs/testing.md", Text: "a line of ordinary prose"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines, err := fixtureRules(t).scan(context.Background(), []Record{tc.record})
			if err != nil {
				t.Fatal(err)
			}
			reports(t, lines, tc.want...)
		})
	}
}

// A path-scoped allowlist clears its one path and no other: the word rule ignores the vendored
// file's own use of the word, and the same line in a file beside it is refused.
func TestAPathScopedAllowlistClearsItsPathAlone(t *testing.T) {
	const line = "var " + leakguardtest.Word + " = require('./lantern')"
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	if err := guard.Rules(bound(t), Record{Label: leakguardtest.AllowedPath, Text: line}); err != nil {
		t.Fatalf("the vendored file's own line: %v", err)
	}
	const beside = "vendor/glasswing/other.js"
	reports(t, refusal(t, guard.Rules(bound(t), Record{Label: beside, Text: line})).Lines, beside+": "+leakguardtest.WordClass)
}

// Each record is scanned on its own: a record that names something private is reported against
// itself and against no record after it. Detect of one fragment answers that fragment's
// findings, where DetectSource would hand the detector's whole run back and report the first
// record's finding against every later one.
func TestARecordIsReportedAgainstItselfAlone(t *testing.T) {
	lines, err := fixtureRules(t).scan(context.Background(), []Record{
		{Label: "the first record", Text: "a host at " + leakguardtest.RulesHost},
		{Label: "the second record", Text: "nothing of ours"},
		{Label: "the third record", Text: "nothing of ours either"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reports(t, lines, "the first record: "+leakguardtest.RulesHostClass)
}

// A context that has run out is a setup failure, never a record read as clean: the detector's own
// scan loop stops at a context's end and answers no finding, which would otherwise read as a
// record holding nothing.
func TestARuleScanThatRanOutOfTimeRefuses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixtureRules(t).scan(ctx, []Record{{Label: "the title", Text: "a host at " + leakguardtest.RulesHost}}); err == nil {
		t.Fatal("a scan under a context that ended answered no error")
	}
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	if err := guard.Rules(ctx, Record{Label: "the title", Text: "nothing of ours"}); err == nil {
		t.Fatal("Rules under a context that ended passed the write")
	} else {
		setupFailure(t, err)
	}
}

// A remote URL names the repository the push would reach, in the shapes a remote is written in;
// one that names no repository on github.com names none, and the guard scans such a write as if
// it were public rather than skipping it. A URL whose two names are no repository GitHub serves
// names none either: the guard refuses a slug its exemption could not cover (Guard.scanThrough),
// so a URL that answered one would refuse the push over the spelling of its remote.
func TestRepoFromURL(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"https://github.com/invented-org/invented-repo.git", "invented-org/invented-repo"},
		{"https://github.com/invented-org/invented-repo", "invented-org/invented-repo"},
		{"https://x-access-token:secret@github.com/invented-org/invented-repo.git", "invented-org/invented-repo"},
		{"ssh://git@github.com/invented-org/invented-repo.git", "invented-org/invented-repo"},
		{"git@github.com:invented-org/invented-repo.git", "invented-org/invented-repo"},
		{"https://github.com/invented-org/invented-repo/", "invented-org/invented-repo"},
		{"https://example.invalid/invented-org/invented-repo.git", ""},
		{"/a/local/path.git", ""},
		{"https://github.com/invented-org", ""},
		{"https://github.com/-invented-org/invented-repo", ""},
		{"https://github.com/invented-org/../invented-repo", ""},
	} {
		if got := RepoFromURL(tc.url); got != tc.want {
			t.Errorf("RepoFromURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// The engine's own inline escape is inert. gitleaks drops every finding of a line that holds a
// `gitleaks:allow` comment, and .github/scripts/check-private-names.sh, the script the rules are
// ported from, honours no such comment: a write that carries one is refused exactly as a write
// that does not. The comment is spelled in two pieces, as the hosts here are, so that no line of
// this file carries an escape of its own.
func TestTheInlineAllowCommentIsInert(t *testing.T) {
	const escape = "gitleaks" + ":allow"
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	err := guard.Rules(bound(t), Record{
		Label: "commit 1a2b3c4 message",
		Text:  "moved the run to " + leakguardtest.RulesHost + " # " + escape,
	})
	reports(t, refusal(t, err).Lines, "commit 1a2b3c4 message: "+leakguardtest.RulesHostClass)
}
