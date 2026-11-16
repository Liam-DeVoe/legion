package specgraph

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// compareCall is a command line, as the shell reads it, that runs the comparison: the command
// spec-check, under any path ("./cmd/spec-check", "$RUNNER_TEMP/spec-check"), then white space
// and the word compare.
var compareCall = regexp.MustCompile(`spec-check\S*\s+compare\b`)

// CIFilesThatCompare is "<path>:<line>" for each line of files that runs the comparison of the
// spec files with the approved spec, sorted by path, then by line, each line once. No CI job
// compares the files (R2): Dispatch makes the comparison at each push. It reads each file two
// ways:
//
//   - As the shell reads its lines: a line that names the command spec-check and, in the next
//     word, the word compare. A quote is removed and a backslash escape reads as the character it
//     escapes, so spec-check "compare" is named. A line that ends in an odd number of backslashes
//     goes on in the next line, and the line named is the one that names spec-check. A comment
//     that names the command is named too: the text does not say that it is a comment.
//   - A file whose name ends in .yml or .yaml, as YAML reads it: the value of each run: key, in
//     every document, through an alias, read as the shell reads it. A plain, quoted or folded
//     value over two lines is one line, named at the line where the value starts; a literal block
//     keeps its lines, each named at its own line. A file that YAML cannot read is read as text
//     alone, since GitHub runs no step of a workflow or an action that it cannot read.
//
// The keys are the repository paths of the files that spec-check guard reads: every file under
// .github/workflows/, .github/actions/, .github/scripts/ and scripts/, which hold the workflows
// and their actions, and some of the scripts that their steps run. A step can also run the
// comparison from a file that is not there, such as a package script, a make target, a script in
// another directory or a link to one, or through a variable. This reader does not see those
// routes, and no list of directories closes them. spec-check compare covers each of them that
// passes the runner's environment on: it refuses to run when GITHUB_ACTIONS or CI is true. A
// route that drops that environment, such as sudo or a container, reaches the comparison past
// that refusal, and only a line this reader reads names it. A Go program that calls Compare is
// outside both; Dispatch itself calls Compare (R2), so no rule can refuse every Go caller.
func CIFilesThatCompare(files map[string][]byte) []string {
	type hit struct {
		path string
		line int
	}
	hits := map[hit]bool{}
	for path, content := range files {
		for _, n := range shellHits(string(content)) {
			hits[hit{path, n + 1}] = true
		}
		if strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml") {
			for _, line := range runValueHits(content) {
				hits[hit{path, line}] = true
			}
		}
	}
	sorted := slices.SortedFunc(maps.Keys(hits), func(a, b hit) int {
		return cmp.Or(cmp.Compare(a.path, b.path), cmp.Compare(a.line, b.line))
	})
	var named []string
	for _, h := range sorted {
		named = append(named, fmt.Sprintf("%s:%d", h.path, h.line))
	}
	return named
}

// shellHits is the index from 0 of each line of text that runs the comparison as the shell reads
// it, each line once.
func shellHits(text string) []int {
	lines := strings.Split(text, "\n")
	var hits []int
	for i := 0; i < len(lines); {
		// One command line: lines[i], and the line after each one that ends in an odd number of
		// backslashes, joined as a shell joins them, with that backslash and the line break
		// removed, and each line's words read as the shell reads them. starts holds where each
		// of its lines starts in text.
		first := i
		var text strings.Builder
		var starts []int
		for {
			line := strings.TrimSuffix(lines[i], "\r")
			starts = append(starts, text.Len())
			i++
			if continues(line) && i < len(lines) {
				text.WriteString(shellWords(line[:len(line)-1]))
				continue
			}
			text.WriteString(shellWords(line))
			break
		}
		last := -1 // the line of the last hit of this command line: one hit a line
		for _, match := range compareCall.FindAllStringIndex(text.String(), -1) {
			// The last line that starts at or before the match is the one that holds it.
			if n := first + sort.Search(len(starts), func(j int) bool { return starts[j] > match[0] }) - 1; n != last {
				last = n
				hits = append(hits, n)
			}
		}
	}
	return hits
}

// continues reports whether the shell reads the next line as part of line: line ends in an odd
// number of backslashes, so its last one escapes the line break.
func continues(line string) bool {
	return (len(line)-len(strings.TrimRight(line, `\`)))%2 == 1
}

// shellWords is line with each quote removed and each backslash escape read as the character it
// escapes, so "compare", 'compare', com""pare and \compare all read as compare. It reads more
// lines as the comparison than the shell does, never fewer: a quoted "spec-check compare" is
// one word to the shell and two here.
func shellWords(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '"' || c == '\'':
		case c == '\\' && i+1 < len(line):
			i++
			b.WriteByte(line[i])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// runValueHits is the line, from 1, of each run: value of the YAML documents of content that runs
// the comparison as the shell reads it: the line where the value starts, or for a literal block
// the line of the block that runs it. It is nil for content that YAML cannot read.
func runValueHits(content []byte) []int {
	var lines []int
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				key, value := n.Content[i], n.Content[i+1]
				if value.Kind == yaml.AliasNode {
					value = value.Alias
				}
				if key.Value != "run" || value == nil || value.Kind != yaml.ScalarNode {
					continue
				}
				for _, n := range shellHits(value.Value) {
					if value.Style&yaml.LiteralStyle != 0 {
						// A literal block starts on the line after its | and keeps each line.
						lines = append(lines, value.Line+1+n)
					} else {
						lines = append(lines, value.Line)
					}
				}
			}
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return lines
			}
			return nil
		}
		walk(&doc)
	}
}
