package specgraph

import (
	"slices"
	"testing"
)

// CodeNames finds each of the five signs of code in a text (level_check.py's code_signs and the
// level format's "How the checker finds code"): a span in backticks, a word with a file ending, a
// word with a slash between two letters, a word that ends with "()", and a word with an underscore
// between letters or digits. A plain product name and ordinary prose name no code.
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

// A go selector is a Go import path and a top-level test name, separated by one space.
func TestParseSelector(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		selector, path, name string
		ok                   bool
	}{
		{"github.com/sjawhar/envoy/cmd/listener TestARestartLosesNoMessage", "github.com/sjawhar/envoy/cmd/listener", "TestARestartLosesNoMessage", true},
		{"example.com/p Test", "example.com/p", "Test", true},
		{"example.com/p Test_x", "example.com/p", "Test_x", true},
		{"example.com/p Test2", "example.com/p", "Test2", true},
		{"TestX", "", "", false},
		{"example.com/p", "", "", false},
		{"example.com/p  TestA", "", "", false},
		{"example.com/p TestA extra", "", "", false},
		{"example.com/p Testa", "", "", false},
		{"example.com/p TestA/sub", "", "", false},
		{"example.com/p BenchmarkA", "", "", false},
		{"example.com/../p TestA", "", "", false},
		{"/abs/p TestA", "", "", false},
		{"./p TestA", "", "", false},
		{"example.com/p/ TestA", "", "", false},
		{"example.com//p TestA", "", "", false},
		{" example.com/p TestA", "", "", false},
		{"", "", "", false},
	} {
		path, name, err := ParseSelector(row.selector)
		if ok := err == nil; ok != row.ok || path != row.path || name != row.name {
			t.Errorf("ParseSelector(%q) = %q, %q, %v; want %q, %q, ok %t", row.selector, path, name, err, row.path, row.name, row.ok)
		}
	}
}
