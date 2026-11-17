package main

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"slices"
)

// commandName is the name of the command that compares the files, which starts each match of
// compareCall.
var commandName = []byte("spec-check")

// compareCall is the comparison in the text that textHits keeps: the command's name, spec-check,
// under any path ("./cmd/spec-check", "$RUNNER_TEMP/spec-check"), with no suffix or one that
// starts with another character than a letter, a digit, _, - or a separator (".exe", "@v1", the
// ").arg" of a builder), then white space (a line break too), commas and opening brackets, as a
// list of arguments in a script holds them, and the word compare. So spec-checker, spec-check-linux
// and spec-check's are other words. nameStart holds the name's other end: no letter, digit, _ or -
// stands right before it in the file, so envoy-spec-check and myspec-check are other words too.
var compareCall = regexp.MustCompile(regexp.QuoteMeta(string(commandName)) + `(?:[^\w\s,\[(-][^\s,\[(]*)?[\s,\[(]+compare\b`)

// ciFilesThatCompare is "<path>:<line>" for each place of files where the comparison of the spec
// files with the approved spec is written, sorted by path, then by line, each line once. No CI job
// compares the files, and R2 gives that comparison at each push to Dispatch. The guard names each
// place of a CI file where the command's name and then the word compare follow each other with
// only white space, commas or opening brackets between them, and with quote characters (also a $
// before one), backslashes, and line breaks with the indentation after them ignored. The name is
// spec-check, under any path, as a word of its own: a word that only holds it, such as
// envoy-spec-check or spec-checker, is another name. So it also names a comment or a quoted text
// that no shell runs, and the author rewords that line. It does not find a command that an
// expansion, an escape or an alias builds, or one with another word between the two, and it is no
// defence against an author who hides the command: compare itself refuses to run in a CI job. It
// reads each file as text (textHits), and knows nothing of shell or of YAML.
//
// The keys are the repository paths of the files that guard reads (ciDirs): every file under
// .github/workflows/, .github/actions/, .github/scripts/ and scripts/, which hold the workflows
// and their actions, and some of the scripts that their steps run. A step can also run the
// comparison from a file that is not there, such as a package script, a make target, a script in
// another directory or a link to one, or through a variable. This reader does not see those
// routes, and no list of directories closes them. compare covers each of them that passes the
// runner's environment on: it refuses to run when GITHUB_ACTIONS is true. A route that drops that
// environment, such as sudo or a container, reaches the comparison past that refusal, and only a
// line this reader reads names it, when the command's name and compare stand next to each other
// there. A Go program that calls specgraph.Compare is outside both, and the Dispatch server is to
// call specgraph.Compare (R2), so no rule can refuse every Go caller.
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
// separate two words, which the first reading keeps). Content with no backslash before a line
// break reads the same both ways, so it is read once.
func textHits(content []byte) []int {
	hits := placesIn(content, false)
	if bytes.Contains(content, []byte("\\\n")) || bytes.Contains(content, []byte("\\\r\n")) {
		hits = append(hits, placesIn(content, true)...)
	}
	slices.Sort(hits)
	return slices.Compact(hits)
}

// placesIn is the line, from 1, of each match of compareCall in content as one reading of
// textHits keeps it, in order. It reads content one time: each backslash with the line break after
// it (a CR and LF is a line break too) goes, with the blanks that start the next line when
// indentation is true (joined). Then each quote character, ' and ", each other backslash, and each
// $ that stands right before a quote character once those backslashes are gone (quoteAfter) go.
// Each byte that is left keeps its index in content, so a place is named at the line where the
// command's name stands, and the byte right before the name is the file's (nameStart): a line that
// a backslash and a line break join onto the word before it, such as a here-document's delimiter
// END\, keeps the line break before spec-check.
func placesIn(content []byte, indentation bool) []int {
	kept := make([]byte, 0, len(content))
	at := make([]int32, 0, len(content)) // the index in content of each byte of kept
	for i := 0; i < len(content); {
		if j := joined(content, i, indentation); j > i {
			i = j
			continue
		}
		switch c := content[i]; {
		case c == '\\' || c == '\'' || c == '"':
		case c == '$' && quoteAfter(content, i+1, indentation):
		default:
			kept, at = append(kept, c), append(at, int32(i))
		}
		i++
	}
	var hits []int
	line, counted := 1, 0 // the line of content at the index counted
	for _, match := range compareCall.FindAllIndex(kept, -1) {
		start := nameStart(content, kept, at, match)
		if start < 0 {
			continue
		}
		from := int(at[start])
		line += bytes.Count(content[counted:from], []byte{'\n'})
		hits, counted = append(hits, line), from
	}
	return hits
}

// nameStart is the index in kept of the command's name that starts the comparison in match, a
// match of compareCall in kept, or -1 when no name does. The name at the start of the match is
// another word when a letter, a digit, _ or - stands right before it in content, such as the
// envoy- of envoy-spec-check. A later spec-check inside the match can then start the comparison, as
// the name of a file in a directory named envoy-spec-check does: it lies in the suffix of the first
// name, which holds no separator, so it runs on to the match's end whenever the byte after it is
// no letter, digit, _ or -, as compareCall holds for the name that starts the match.
func nameStart(content, kept []byte, at []int32, match []int) int {
	for start := match[0]; ; {
		if from := at[start]; (from == 0 || !wordByte(content[from-1])) && (start == match[0] || !wordByte(kept[start+len(commandName)])) {
			return start
		}
		next := bytes.Index(kept[start+1:match[1]], commandName)
		if next < 0 {
			return -1
		}
		start += 1 + next
	}
}

// wordByte reports whether c is an ASCII letter, a digit, _ or -: a byte of a word that holds the
// command's name, such as envoy-spec-check or spec-checker. compareCall's [^\w\s,\[(-] is the same
// set, beside the separators.
func wordByte(c byte) bool {
	return c == '_' || c == '-' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
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
