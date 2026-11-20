package jjmerge

import (
	"regexp"
	"slices"
	"strings"
)

// withTrailer is description with line added as its last trailer: one more line of its last
// paragraph when that paragraph is a block of trailers and not the subject, or a paragraph of its
// own after a blank line.
func withTrailer(description, line string) string {
	text := strings.TrimRight(description, "\n")
	paragraphs := strings.Split(text, "\n\n")
	if last := paragraphs[len(paragraphs)-1]; len(paragraphs) > 1 && trailerBlock(last) {
		return text + "\n" + line + "\n"
	}
	return text + "\n\n" + line + "\n"
}

// withoutLine is description without each line that is line, and without a paragraph that held
// such lines alone, so the run's line leaves no empty paragraph where jj wrote it with no
// trailer beside it. jj writes the run's line, a second message, as a paragraph of its own and
// puts the configured trailers in that paragraph after it. When that paragraph holds other lines
// once the line is out, and the paragraph before it is a block of trailers and not the subject,
// the two become one paragraph, where jj puts the configured trailers when it is given the
// message alone: git reads only the last paragraph as trailers. Every other paragraph stays as it
// is.
func withoutLine(description, line string) string {
	var paragraphs []string
	for _, paragraph := range strings.Split(strings.TrimRight(description, "\n"), "\n\n") {
		lines := strings.Split(paragraph, "\n")
		count := len(lines)
		rest := slices.DeleteFunc(lines, func(l string) bool { return l == line })
		switch {
		case len(rest) == 0:
			// The paragraph held the run's line alone.
		case len(rest) < count && len(paragraphs) > 1 && trailerBlock(paragraphs[len(paragraphs)-1]):
			paragraphs[len(paragraphs)-1] += "\n" + strings.Join(rest, "\n")
		default:
			paragraphs = append(paragraphs, strings.Join(rest, "\n"))
		}
	}
	return strings.Join(paragraphs, "\n\n") + "\n"
}

// trailerLine is one `Key: value` line of a trailer block.
var trailerLine = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*: `)

// trailerBlock is whether paragraph is a block of trailers: its first line is a trailer, and each
// other line is a trailer or starts with a space or a tab. git and jj read such a line as the
// continuation of the trailer above it, so a folded trailer keeps the block one.
func trailerBlock(paragraph string) bool {
	for i, line := range strings.Split(paragraph, "\n") {
		folded := i > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t"))
		if !folded && !trailerLine.MatchString(line) {
			return false
		}
	}
	return true
}
