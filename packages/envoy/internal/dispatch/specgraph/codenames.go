package specgraph

import (
	"regexp"
	"slices"
	"strings"
)

// The five signs of code in a text ported from level_check.py's code_signs, and the form of a Go
// test's name below (R4: no text of The Spec names a file, a package or a symbol, but a check's
// test and its job).
var (
	backtickSpan        = regexp.MustCompile("`[^`\n]+`")
	fileEnding          = regexp.MustCompile(`(?i)\.(?:go|ts|tsx|js|jsx|mjs|cjs|py|md|yaml|yml|json|jsonl|toml|sh|bash|sql|rs|lean|v|txt|html|css|proto|lock|mod|cfg|ini|env)$`)
	slashBetweenLetters = regexp.MustCompile(`[A-Za-z]/[A-Za-z]`)
	endsWithCall        = regexp.MustCompile(`[\p{L}\p{N}_]\(\)`)
	underscoreBetween   = regexp.MustCompile(`[A-Za-z0-9]_[A-Za-z0-9]`)
)

// productNames are plain names of products that are not code where a sign matches them.
var productNames = []string{"GitHub", "PostgreSQL", "OpenAI", "JavaScript", "TypeScript", "Kubernetes", "Node.js", "Next.js", "Vue.js"}

// CodeNames is each name of code that text holds, in the order found: first each span in
// backticks, backticks included, then each word of the rest with a file ending, a slash between two
// letters, an ending "()", or an underscore between two letters or digits, and each word that has
// the form of a Go test's name (goTestName). Punctuation around a word and a possessive "'s" are
// not part of it, and a plain product name such as GitHub or Node.js is no code.
func CodeNames(text string) []string {
	found := backtickSpan.FindAllString(text, -1)
	for _, token := range strings.Fields(backtickSpan.ReplaceAllString(text, " ")) {
		word := strings.TrimLeft(token, `"'([{<`)
		if endsWithCall.MatchString(word) {
			found = append(found, strings.TrimRight(word, `.,;:!?"'`))
			continue
		}
		word = strings.TrimRight(word, `"')]}>.,;:!?`)
		word = strings.TrimSuffix(word, "'s")
		if word == "" || slices.Contains(productNames, word) {
			continue
		}
		if fileEnding.MatchString(word) || slashBetweenLetters.MatchString(word) || underscoreBetween.MatchString(word) || goTestName(word) {
			found = append(found, word)
		}
	}
	return found
}

// goTestName reports whether word has the form of the name of a top-level Go test, the one form of
// a test's name that the checker knows, since go is the one runner whose selector it reads
// (ParseSelector): Test, then a capital letter, a digit or an underscore, then letters, digits and
// underscores. The word Test alone is a plain word, and so are Tests and Testament.
func goTestName(word string) bool {
	return word != "Test" && topLevelTestName.MatchString(word)
}

// slug is an id's form: lower-case letters and digits, in words joined by single hyphens.
var slug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// positionWord is a word that states a position: digits alone ("step-3") or one letter and digits
// ("s3", "r12"). A word such as "oauth2" or "ipv6" does not.
var positionWord = regexp.MustCompile(`^(?:[0-9]+|[a-z][0-9]+)$`)

// IsID reports whether s is a valid id of a node: lower-case words of letters and digits joined by
// single hyphens, with no word that states a position, since an id never changes and a position
// does.
func IsID(s string) bool {
	if !slug.MatchString(s) {
		return false
	}
	for _, word := range strings.Split(s, "-") {
		if positionWord.MatchString(word) {
			return false
		}
	}
	return true
}
