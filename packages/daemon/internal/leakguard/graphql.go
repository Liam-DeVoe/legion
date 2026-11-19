package leakguard

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// graphQLQuery is the field of a `gh api graphql` call that holds the query: a field of the argv
// (`-f query=…`), the field a file is read into (`-F query=@<path>`, GhFile.Field), or the member
// of a JSON body (`--input`). gh reads every other field of such a call but `operationName` as a
// variable, whose value is read as the field it is.
const graphQLQuery = "query"

// ghAPIGraphQL is whether args, a gh call's argv, is `gh api graphql`: the command `api`, whose
// endpoint, the first positional argument after it, is `graphql`. The options before it are read
// by ghAPIValueOptions and ghAPIShortValueOptions, so that the value of one (`-X POST`) is not
// taken for the endpoint, in a short cluster as pflag reads one (`-iX POST`), and past a bare
// `--` every argument is positional.
//
// It errs toward reading no query. A call it does not find is read as written, its query one
// field among the others, as every query was before literals were read; a call it found that
// was none would have a REST field named `query` read as a GraphQL document, which leaves out the
// text outside its literals.
func ghAPIGraphQL(args []string) bool {
	if len(args) == 0 || args[0] != "api" {
		return false
	}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return i+1 < len(args) && args[i+1] == "graphql"
		case strings.HasPrefix(arg, "--"):
			if ghAPIValueOptions[arg] {
				i++
			}
		case len(arg) > 1 && arg[0] == '-':
			// The first letter of a cluster that takes a value takes the rest of the argument
			// as it, or the next argument where it is the cluster's last letter.
			for j := 1; j < len(arg); j++ {
				if strings.IndexByte(ghAPIShortValueOptions, arg[j]) >= 0 {
					if j == len(arg)-1 {
						i++
					}
					break
				}
			}
		default:
			return arg == "graphql"
		}
	}
	return false
}

// ghAPIValueOptions are `gh api`'s long options that take a value written as the argument after
// them, and ghAPIShortValueOptions the letters of its short ones, in gh 2.98.0's help
// (cmd/legion/testdata/gh-help/api.txt). Its other options (`--include`, `--paginate`,
// `--silent`, `--slurp`, `--verbose`, `--allow-escape-sequences`, `--help`) take none, and a long
// option written `--name=value` holds its own.
var ghAPIValueOptions = map[string]bool{
	"--cache":     true,
	"--field":     true,
	"--header":    true,
	"--hostname":  true,
	"--input":     true,
	"--jq":        true,
	"--method":    true,
	"--preview":   true,
	"--raw-field": true,
	"--template":  true,
}

const ghAPIShortValueOptions = "FHXfpqt"

// graphQLRecords is record read as a GraphQL query: one record for each string literal of its
// text, decoded, labelled "<record>, GraphQL string <k>" with k counted from 1, and record itself
// where its text does not lex.
//
// A query is a document GitHub parses, as a JSON body is (jsonFields). GitHub stores the value of
// each literal its arguments carry, decoded: the title and the body of a `createIssue` mutation
// are two strings GitHub stores apart, and a `\u006c` in a literal is the letter l in the stored
// text. The rest of a query is the schema's own words — operation, field, argument and variable
// names, enum values, numbers and punctuation — which shape the request and are stored as no
// text of the caller's. So a literal is a record of its own, and the query text is read as no
// record: the raw text would join the title and the body on one line, and would show a rule the
// escape rather than the letter. The shared check reads what GitHub stores, one value at a time,
// for the same reason (dispatch://INFRA-4383, R1).
//
// A query with no literal lexes to no record, since GitHub stores nothing of it. A text that does
// not lex (graphQLStrings) is read as written, as every query was before literals were read: a
// guard that cannot tell where a literal ends cannot tell which part of the text GitHub would
// store.
func graphQLRecords(record Record) []Record {
	literals, ok := graphQLStrings(record.Text)
	if !ok {
		return []Record{record}
	}
	records := make([]Record, 0, len(literals))
	for k, literal := range literals {
		records = append(records, Record{Label: record.Label + ", GraphQL string " + strconv.Itoa(k+1), Text: literal})
	}
	return records
}

// graphQLStrings is the decoded value of each string literal of source, a GraphQL document, in
// the order the document writes them, and false where source does not lex: a literal with no
// end, a line break inside a `"…"` literal, or an escape the GraphQL specification does not
// have (its section 2.9.4, String Value). Both forms of literal are read:
//
//   - `"…"`, whose escapes are `\"`, `\\`, `\/`, `\b`, `\f`, `\n`, `\r`, `\t`, `\uXXXX` (two of
//     them for a code point past U+FFFF, as a surrogate pair) and `\u{…}`, any number of hex
//     digits naming one Unicode scalar value;
//   - `"""…"""`, a block string, whose one escape is `\"""` and whose value is its lines with
//     their common indentation and the blank lines around them taken off (graphQLBlockValue).
//
// A `#` outside a literal starts a comment, which runs to the end of its line and holds no
// literal: a `"` inside it opens nothing. Everything else outside a literal is skipped without a
// check of its own, since a character no GraphQL token has makes a document GitHub refuses, and a
// refused document stores no text.
func graphQLStrings(source string) ([]string, bool) {
	var literals []string
	for i := 0; i < len(source); {
		switch {
		case source[i] == '#':
			for i < len(source) && source[i] != '\n' && source[i] != '\r' {
				i++
			}
		case strings.HasPrefix(source[i:], `"""`):
			value, length, ok := graphQLBlockString(source[i+len(`"""`):])
			if !ok {
				return nil, false
			}
			literals = append(literals, value)
			i += len(`"""`) + length
		case source[i] == '"':
			value, length, ok := graphQLString(source[i+len(`"`):])
			if !ok {
				return nil, false
			}
			literals = append(literals, value)
			i += len(`"`) + length
		default:
			i++
		}
	}
	return literals, true
}

// graphQLString is the value of the `"…"` literal whose text, past its opening quote, begins s,
// and the length of that text with its closing quote. A literal with no escape is its own text,
// and one with escapes is its runs of plain text with each escape decoded between them.
func graphQLString(s string) (string, int, bool) {
	var value strings.Builder
	escaped := false
	// run is where the plain text not yet written to value starts.
	run := 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '"':
			if !escaped {
				return s[:i], i + 1, true
			}
			value.WriteString(s[run:i])
			return value.String(), i + 1, true
		case '\n', '\r':
			return "", 0, false
		case '\\':
			if i+1 == len(s) {
				return "", 0, false
			}
			value.WriteString(s[run:i])
			escaped = true
			switch one, single := graphQLEscapes[s[i+1]]; {
			case single:
				value.WriteByte(one)
				i += 2
			case s[i+1] == 'u':
				r, length, ok := graphQLUnicode(s[i+2:])
				if !ok {
					return "", 0, false
				}
				value.WriteRune(r)
				i += 2 + length
			default:
				return "", 0, false
			}
			run = i
		default:
			// The literal's own bytes, as written: a byte that is no UTF-8 is one GitHub's parser
			// refuses or stores as it stands, and either way the rules read what is there.
			i++
		}
	}
	return "", 0, false
}

// graphQLEscapes are the escapes of a `"…"` literal that stand for one character, by the letter
// after the backslash.
var graphQLEscapes = map[byte]byte{
	'"':  '"',
	'\\': '\\',
	'/':  '/',
	'b':  '\b',
	'f':  '\f',
	'n':  '\n',
	'r':  '\r',
	't':  '\t',
}

// graphQLUnicode is the code point of the `\u` escape whose text, past the `\u`, begins s, and
// the length of that text. It is `{…}` with one or more hex digits, or four hex digits; four that
// name a leading surrogate are followed by a second `\u` and four digits that name a trailing one,
// and the two are one code point. A surrogate anywhere else, and a value past U+10FFFF, is no
// Unicode scalar value and does not lex.
func graphQLUnicode(s string) (rune, int, bool) {
	if strings.HasPrefix(s, "{") {
		end := strings.IndexByte(s, '}')
		if end < 2 {
			return 0, 0, false
		}
		value, ok := graphQLHex(s[1:end])
		if !ok || !utf8.ValidRune(value) {
			return 0, 0, false
		}
		return value, end + 1, true
	}
	if len(s) < 4 {
		return 0, 0, false
	}
	leading, ok := graphQLHex(s[:4])
	if !ok {
		return 0, 0, false
	}
	if leading < 0xD800 || leading > 0xDFFF {
		return leading, 4, true
	}
	if leading > 0xDBFF || len(s) < 10 || s[4:6] != `\u` {
		return 0, 0, false
	}
	trailing, ok := graphQLHex(s[6:10])
	if !ok || trailing < 0xDC00 || trailing > 0xDFFF {
		return 0, 0, false
	}
	return (leading-0xD800)<<10 + (trailing - 0xDC00) + 0x10000, 10, true
}

// graphQLHex is the value of digits, hex digits of either case, and false for anything else or
// for a value past the last code point, which is read no further than the digit that passes it.
func graphQLHex(digits string) (rune, bool) {
	var value rune
	for _, digit := range []byte(digits) {
		switch {
		case '0' <= digit && digit <= '9':
			value = value<<4 | rune(digit-'0')
		case 'a' <= digit && digit <= 'f':
			value = value<<4 | rune(digit-'a'+10)
		case 'A' <= digit && digit <= 'F':
			value = value<<4 | rune(digit-'A'+10)
		default:
			return 0, false
		}
		if value > utf8.MaxRune {
			return 0, false
		}
	}
	return value, true
}

// graphQLBlockString is the value of the block string whose text, past its opening `"""`, begins
// s, and the length of that text with its closing `"""`. Its raw text is every character up to
// that close, with each `\"""` read as `"""`.
func graphQLBlockString(s string) (string, int, bool) {
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], `\"""`):
			i += len(`\"""`)
		case strings.HasPrefix(s[i:], `"""`):
			return graphQLBlockValue(strings.ReplaceAll(s[:i], `\"""`, `"""`)), i + len(`"""`), true
		default:
			i++
		}
	}
	return "", 0, false
}

// graphQLBlockValue is a block string's value, from its raw text, as the specification's
// BlockStringValue reads it: the lines, split at each line terminator; the least indentation of
// any line after the first that holds more than spaces and tabs, taken off each line after the
// first; then the leading and trailing lines of spaces and tabs alone dropped; and the lines left
// joined with line feeds.
func graphQLBlockValue(raw string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n"), "\n")
	common := -1
	for _, line := range lines[1:] {
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent < len(line) && (common < 0 || indent < common) {
			common = indent
		}
	}
	if common > 0 {
		for i := 1; i < len(lines); i++ {
			lines[i] = lines[i][min(common, len(lines[i])):]
		}
	}
	blank := func(line string) bool { return strings.Trim(line, " \t") == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
