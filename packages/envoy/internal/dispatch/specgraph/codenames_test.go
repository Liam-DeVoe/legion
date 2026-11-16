package specgraph

import (
	"slices"
	"testing"
)

// CodeNames finds each sign of code in a text (level_check.py's code_signs and the level format's
// "How the checker finds code", and the name of a Go test): a span in backticks, a word with a
// file ending, a word with a slash between two letters, a word that ends with "()", a word with an
// underscore between letters or digits, and a word that has the form of a Go test's name. A plain
// product name and ordinary prose name no code.
func TestCodeNames(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		text string
		want []string
	}{
		{text: "a span in `backticks`", want: []string{"`backticks`"}},
		{text: "reads settings.go", want: []string{"settings.go"}},
		{text: "reads SETTINGS.YAML,", want: []string{"SETTINGS.YAML"}},
		{text: "in cmd/listener", want: []string{"cmd/listener"}},
		{text: "calls Read()", want: []string{"Read()"}},
		{text: "calls (Read()).", want: []string{"Read())"}},
		{text: "the max_bytes value", want: []string{"max_bytes"}},
		{text: "the daemon's \"store.json\" file's lines", want: []string{"store.json"}},
		{text: "the parser's main.go's rule", want: []string{"main.go"}},
		{text: "GitHub and PostgreSQL and Node.js", want: nil},
		{text: "the 25 words, at most.", want: nil},
		{text: "3/4 of a_ value", want: nil},
		{text: "a `code` span and lib/x.ts", want: []string{"`code`", "lib/x.ts"}},
		{text: "a\u202fspan\u202fin\u202f`go`", want: []string{"`go`"}},
		// v-check-shape: "a program check whose procedure names a test". A Go test is named in Go's
		// own form, with no underscore; the word Test alone, and Tests, name no test.
		{text: "runs TestARestartLosesNoMessage, then TestB.", want: []string{"TestARestartLosesNoMessage", "TestB"}},
		{text: "the TestARestart_LosesNoClaim test", want: []string{"TestARestart_LosesNoClaim"}},
		{text: "Test results. Tests of the daemon, a Testament and Test's end", want: nil},
		// R4: a symbol with an underscore, in lower camel case, or as a package and its symbol.
		{text: "the read_set value", want: []string{"read_set"}},
		{text: "readSet gives each message an owner, as isValid says", want: []string{"readSet", "isValid"}},
		{text: "pmdoc.ReadSet gives each message an owner, and so does notes.ReadSet's set.", want: []string{"pmdoc.ReadSet", "notes.ReadSet"}},
		{text: "ReadSet() gives each message an owner", want: []string{"ReadSet()"}},
		// The reviewer's, not the program's: a word with a capital at the start of each part and a
		// bare package word have the shape of a product name and a plain word.
		{text: "ReadSet and the pmdoc package, GitHub and JetStream", want: nil},
		{text: "e.g. one message, i.e. the first, at 9 a.m. on example.com", want: nil},
		{text: "gRPC on macOS, iOS, tvOS and the iPhone, at 5 kHz and 64 kB, through eBPF, vCPU and jQuery", want: nil},
		// Each character that is no letter, no digit and no underscore ends a name, in each script
		// of quotes and brackets; the bare word Test stays a plain word.
		{text: "runs \u201cTestMessagesSurvive\u201d. Then \u00abTestB\u00bb, \u2018TestC\u2019, \u300cTestD\u300d and TestE\u2019s end", want: []string{"TestMessagesSurvive", "TestB", "TestC", "TestD", "TestE"}},
		{text: "TestF\u2026 and TestG\u2014then", want: []string{"TestF", "TestG"}},
		{text: "the \u201creadSet\u201d and \u201epmdoc.ReadSet\u201c names", want: []string{"readSet", "pmdoc.ReadSet"}},
		{text: "\u201cTest\u201d results of \u201cGitHub\u201d", want: nil},
		{text: "", want: nil},
	} {
		if got := CodeNames(row.text); !slices.Equal(got, row.want) {
			t.Errorf("CodeNames(%q) = %q, want %q", row.text, got, row.want)
		}
	}
}

// An id is lower-case words of letters and digits joined by single hyphens, with no word that is
// digits alone or one letter and digits, which state a position.
func TestIsID(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		id   string
		want bool
	}{
		{"work-unit", true}, {"oauth2-login", true}, {"ipv6", true}, {"a-claim-survives-a-restart", true},
		{"step-3", false}, {"s3-bucket", false}, {"r12", false}, {"Work-Unit", false}, {"a--b", false},
		{"-a", false}, {"a-", false}, {"a_b", false}, {"", false}, {"café", false},
	} {
		if got := IsID(row.id); got != row.want {
			t.Errorf("IsID(%q) = %t, want %t", row.id, got, row.want)
		}
	}
}
