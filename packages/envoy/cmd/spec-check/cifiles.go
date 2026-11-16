package main

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"slices"
)

// compareCall is the comparison in the text that textHits keeps: a word that holds the command
// spec-check, under any path ("./cmd/spec-check", "$RUNNER_TEMP/spec-check"), then white space (a
// line break too), commas and opening brackets, as a list of arguments in a script holds them,
// and the word compare.
var compareCall = regexp.MustCompile(`spec-check[^\s,\[(]*[\s,\[(]+compare\b`)

// ciFilesThatCompare is "<path>:<line>" for each place of files where the comparison of the spec
// files with the approved spec is written, sorted by path, then by line, each line once. No CI job
// compares the files, and R2 gives that comparison at each push to Dispatch. The guard names each
// place of a CI file where the command's name and then the word compare follow each other with
// only white space, commas or opening brackets between them, and with quote characters (also a $
// before one), backslashes, and line breaks with the indentation after them ignored. So it also
// names a comment or a quoted text that no shell runs, and the author rewords that line. It does
// not find a command that an expansion or an escape builds, or one with another word between the
// two, and it is no defence against an author who hides the command: compare itself refuses to
// run in a CI job. It reads each file as text (textHits), and knows nothing of shell or of YAML.
//
// The keys are the repository paths of the files that guard reads (ciDirs): every file under
// .github/workflows/, .github/actions/, .github/scripts/ and scripts/, which hold the workflows
// and their actions, and some of the scripts that their steps run. A step can also run the
// comparison from a file that is not there, such as a package script, a make target, a script in
// another directory or a link to one, or through a variable. This reader does not see those
// routes, and no list of directories closes them. compare covers each of them that passes the
// runner's environment on: it refuses to run when GITHUB_ACTIONS is true. A route that drops that
// environment, such as sudo or a container, reaches the comparison past that refusal, and only a
// line this reader reads names it. A Go program that calls specgraph.Compare is outside both, and
// the Dispatch server is to call specgraph.Compare (R2), so no rule can refuse every Go caller.
func ciFilesThatCompare(files map[string][]byte) []string {
	var named []string
	for _, path := range slices.Sorted(maps.Keys(files)) {
		for _, line := range textHits(files[path]) {
			named = append(named, fmt.Sprintf("%s:%d", path, line))
		}
	}
	return named
}

// textHits is the line, from 1, of each place of content where compareCall matches, sorted, each
// line once. It reads content two ways and names a place that either finds: a backslash and the
// line break after it go, and in the second reading the blanks that start the next line go too,
// so a word that a run: | block splits is one word again (in a shell script the blanks there
// separate two words, which the first reading keeps).
func textHits(content []byte) []int {
	hits := append(placesIn(content, false), placesIn(content, true)...)
	slices.Sort(hits)
	return slices.Compact(hits)
}

// placesIn is the line, from 1, of each match of compareCall in content as one reading of
// textHits keeps it, in order. It reads content one time: each backslash with the line break after
// it (a CR and LF is a line break too) goes, with the blanks that start the next line when
// indentation is true (joined). Then each quote character, ' and ", each other backslash, and each
// $ that stands right before a quote character once those backslashes are gone (quoteAfter) go.
// Each byte that is left keeps the line of content that it came from, so a place is named at the
// line where the command's name stands.
func placesIn(content []byte, indentation bool) []int {
	kept := make([]byte, 0, len(content))
	lines := make([]int32, 0, len(content))
	line := int32(1)
	for i := 0; i < len(content); {
		if j := joined(content, i, indentation); j > i {
			line += int32(bytes.Count(content[i:j], []byte{'\n'}))
			i = j
			continue
		}
		c := content[i]
		i++
		switch {
		case c == '\\' || c == '\'' || c == '"':
		case c == '$' && quoteAfter(content, i, indentation):
		default:
			kept, lines = append(kept, c), append(lines, line)
			if c == '\n' {
				line++
			}
		}
	}
	var hits []int
	for _, match := range compareCall.FindAllIndex(kept, -1) {
		hits = append(hits, int(lines[match[0]]))
	}
	return hits
}

// quoteAfter reports whether a quote character, ' or ", is at i of content once the backslashes
// there, and the line breaks after them that joined removes, are gone.
func quoteAfter(content []byte, i int, indentation bool) bool {
	for i = joined(content, i, indentation); i < len(content) && content[i] == '\\'; i = joined(content, i+1, indentation) {
	}
	return i < len(content) && (content[i] == '\'' || content[i] == '"')
}

// joined is the index in content after each backslash at i that a line break follows, with that
// line break, and with the spaces and tabs that start the next line when indentation is true. It
// is i when no such backslash is at i.
func joined(content []byte, i int, indentation bool) int {
	for i < len(content) && content[i] == '\\' {
		j := i + 1
		if j < len(content) && content[j] == '\r' {
			j++
		}
		if j == len(content) || content[j] != '\n' {
			break
		}
		for i = j + 1; indentation && i < len(content) && (content[i] == ' ' || content[i] == '\t'); i++ {
		}
	}
	return i
}
