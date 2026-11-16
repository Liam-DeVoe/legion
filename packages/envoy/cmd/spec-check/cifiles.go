package main

import (
	"bytes"
	"cmp"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// compareCall is a command line, its literal words read as shellWords reads them, that runs the
// comparison: the command spec-check, under any path ("./cmd/spec-check",
// "$RUNNER_TEMP/spec-check"), then white space and the word compare.
var compareCall = regexp.MustCompile(`spec-check\S*\s+compare\b`)

// ciFilesThatCompare is "<path>:<line>" for each line of files that runs the comparison of the
// spec files with the approved spec, sorted by path, then by line, each line once. No CI job
// compares the files, and R2 gives that comparison at each push to Dispatch. It reads each
// literal word of a line, quoted or not. It does not expand a brace, a glob, a variable or a
// command substitution, and the reviewer of a change to the CI files reads those. It reads each
// file two ways:
//
//   - Its lines: a line that names the command spec-check and, in the next word, the word
//     compare. A quote is removed, a backslash escape reads as the character it escapes, and
//     Bash's $'…' and $"…" read as Bash reads them, so spec-check "compare" and spec-check
//     $'com\x70are' are named. A line that ends in an odd number of backslashes goes on in the
//     next line, and the line named is the one that names spec-check. A comment that names the
//     command is named too: the text does not say that it is a comment.
//   - A file whose name ends in .yml or .yaml, as YAML reads it: the value of each run: key, in
//     each document up to the first one that YAML cannot read, through an alias, its words read
//     as a line's are. A plain, quoted or folded value over two lines is one line, named at the
//     line where the value starts; a literal block keeps its lines, each named at its own line.
//     From the first document that YAML cannot read, the file is read as text alone, since GitHub
//     runs no step of a workflow or an action that it cannot read.
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

// shellHits is the index from 0 of each line of text that runs the comparison, its literal words
// read as shellWords reads them, each line once.
func shellHits(text string) []int {
	lines := strings.Split(text, "\n")
	var hits []int
	for i := 0; i < len(lines); {
		// One command line: lines[i], and the line after each one that ends in an odd number of
		// backslashes, joined as a shell joins them, with that backslash and the line break
		// removed, and each line's words read by shellWords. starts holds where each
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
// escapes, so "compare", 'compare', com""pare and \compare all read as compare, and with Bash's
// literal quoting read as Bash reads it: $'…' is the text that its ANSI-C escapes decode to
// (ansiC), so $'com\x70are' reads as compare, and $"…" is the quoted text. Of the literal words of
// a line it reads more as the comparison than the shell does, never fewer: a quoted "spec-check
// compare" is one word to the shell and two here. It expands no brace, glob, variable or command
// substitution.
func shellWords(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '$' && i+1 < len(line) && line[i+1] == '\'':
			i = ansiC(&b, line, i+2)
		case c == '$' && i+1 < len(line) && line[i+1] == '"':
			// $"…" is a text that Bash translates by the locale's message catalog, and no catalog
			// holds the words of this command, so it is the quoted text.
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

// ansiC writes to b the text of the ANSI-C quote $'…' of line whose text starts at start, as Bash
// decodes it, and answers the index of its closing quote, or the last index of line when the line
// does not close it. The escapes are Bash's: \a \b \e \E \f \n \r \t \v, \\ \' \" \? for the
// character itself, \nnn in octal (one to three digits), \xHH (one or two hex digits) as a byte,
// \uHHHH and \UHHHHHHHH (up to four and eight hex digits) as a character in UTF-8, and \cx for
// control-x. Another escape, and \x, \u or \U with no digit after it, is its backslash and its
// letter, as Bash keeps them. The text ends at a NUL, since Bash keeps no NUL in a word.
func ansiC(b *strings.Builder, line string, start int) int {
	var text []byte
	i := start
	for ; i < len(line) && line[i] != '\''; i++ {
		if line[i] != '\\' || i+1 == len(line) {
			text = append(text, line[i])
			continue
		}
		i++
		switch e := line[i]; e {
		case 'a':
			text = append(text, '\a')
		case 'b':
			text = append(text, '\b')
		case 'e', 'E':
			text = append(text, 0x1b)
		case 'f':
			text = append(text, '\f')
		case 'n':
			text = append(text, '\n')
		case 'r':
			text = append(text, '\r')
		case 't':
			text = append(text, '\t')
		case 'v':
			text = append(text, '\v')
		case '\\', '\'', '"', '?':
			text = append(text, e)
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v, n := digits(line[i:], 8, 3)
			text = append(text, byte(v)) // Bash keeps the low eight bits of \400 to \777
			i += n - 1
		case 'x', 'u', 'U':
			limit := 2 // \x takes two hex digits, \u four and \U eight
			if e == 'u' {
				limit = 4
			} else if e == 'U' {
				limit = 8
			}
			v, n := digits(line[i+1:], 16, limit)
			switch {
			case n == 0:
				text = append(text, '\\', e)
			case e == 'x':
				text = append(text, byte(v))
			default:
				text = utf8.AppendRune(text, rune(v))
			}
			i += n
		case 'c':
			if i+1 == len(line) || line[i+1] == '\'' {
				text = append(text, '\\', 'c')
				break
			}
			i++
			switch {
			case line[i] == '?':
				text = append(text, 0x7f)
			case line[i] == '\\' && i+1 < len(line) && line[i+1] == '\\':
				i++ // \c\\ is control-\, as Bash reads it
				fallthrough
			default:
				text = append(text, line[i]&0x1f) // control-x, the same for x in either case
			}
		default:
			text = append(text, '\\', e)
		}
	}
	if nul := bytes.IndexByte(text, 0); nul >= 0 {
		text = text[:nul]
	}
	b.Write(text)
	return min(i, len(line)-1)
}

// digits is the value of the octal (base 8) or hex (base 16) digits at the start of s, at most
// limit of them, and how many there are.
func digits(s string, base, limit int) (value, n int) {
	set := "01234567"
	if base == 16 {
		set = "0123456789abcdefABCDEF"
	}
	for n < limit && n < len(s) && strings.IndexByte(set, s[n]) >= 0 {
		n++
	}
	// At most eight digits of base, so ParseUint fails only on no digit, which is 0.
	v, _ := strconv.ParseUint(s[:n], base, 64)
	return int(v), n
}

// runValueHits is the line, from 1, of each run: value of the YAML documents of content that runs
// the comparison as shellHits reads it: the line where the value starts, or for a literal block
// the line of the block that runs it. It reads the documents up to the first one that YAML cannot
// read, and keeps the lines of the documents before it.
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
			// The end of content, or a document that YAML cannot read, ends the reading, since the
			// decoder cannot go on past such a document. The lines of the documents before it stay.
			return lines
		}
		walk(&doc)
	}
}
