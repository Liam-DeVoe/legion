package specgraph

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// compareCall is a command line that runs the comparison: the command spec-check, under any path
// or quoting ("./cmd/spec-check", "$RUNNER_TEMP/spec-check"), then white space and the word
// compare.
var compareCall = regexp.MustCompile(`spec-check\S*\s+compare\b`)

// CIFilesThatCompare is "<path>:<line>" for each line of files that runs the comparison of the
// spec files with the approved spec, sorted by path, then by line: a line that names the command
// spec-check and, later on the same line, the word compare. A line that ends in a backslash goes
// on in the next line, as a shell reads it, and the line named is the one that names spec-check.
// No CI job compares the files (R2): Dispatch makes the comparison at each push.
//
// The keys are repository paths under .github/workflows/, .github/actions/, .github/scripts/ and
// scripts/, the places where the steps of a workflow and the actions and scripts that they run
// live. The instrument reads text, not what a step does when it runs: a command held in a
// variable, a line that a YAML folded scalar joins, and a Go program that calls Compare are
// outside it. Dispatch itself calls Compare (R2), so no rule can refuse every Go caller.
func CIFilesThatCompare(files map[string][]byte) []string {
	type hit struct {
		path string
		line int
	}
	var hits []hit
	for path, content := range files {
		lines := strings.Split(string(content), "\n")
		for i := 0; i < len(lines); {
			// One command line: lines[i], and the line after each one that ends in a backslash,
			// joined as a shell joins them, with the backslash and the line break removed. starts
			// holds where each of its lines starts in text.
			first := i
			var text strings.Builder
			var starts []int
			for {
				line := strings.TrimSuffix(lines[i], "\r")
				starts = append(starts, text.Len())
				i++
				if strings.HasSuffix(line, `\`) && i < len(lines) {
					text.WriteString(line[:len(line)-1])
					continue
				}
				text.WriteString(line)
				break
			}
			last := -1 // the line of the last hit of this command line: one hit a line
			for _, match := range compareCall.FindAllStringIndex(text.String(), -1) {
				// The last line that starts at or before the match is the one that holds it.
				if n := first + sort.Search(len(starts), func(j int) bool { return starts[j] > match[0] }); n != last {
					last = n
					hits = append(hits, hit{path, n})
				}
			}
		}
	}
	slices.SortFunc(hits, func(a, b hit) int { return cmp.Or(cmp.Compare(a.path, b.path), cmp.Compare(a.line, b.line)) })
	var named []string
	for _, h := range hits {
		named = append(named, fmt.Sprintf("%s:%d", h.path, h.line))
	}
	return named
}
