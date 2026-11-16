package main

import (
	"bytes"
	"cmp"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// compareCall is a comparison in the words of readShell, each word ended by a NUL byte: a word
// that holds the command spec-check, under any path ("./cmd/spec-check",
// "$RUNNER_TEMP/spec-check"), then the word compare.
var compareCall = regexp.MustCompile(`spec-check[^\s\x00]*\x00compare\x00`)

// ciFilesThatCompare is "<path>:<line>" for each line of files that runs the comparison of the
// spec files with the approved spec, sorted by path, then by line, each line once. No CI job
// compares the files, and R2 gives that comparison at each push to Dispatch. It names each line
// whose words run spec-check, under any path, with compare as its first argument, as Bash reads
// the words: after quote removal and line continuation. A comment, from a # that starts a word to
// the end of its line, holds no words and continues no line, also after a backslash. A
// here-document body, up to its delimiter line, holds no words, so a quote or a backslash in it
// changes nothing after it. It also reads each quoted word, each here-document body and each
// comment as a script of its own, since a nested shell can run it, so it names a comparison there
// that no shell runs too. It expands nothing: for a form that needs a brace, a glob, a variable or
// a command substitution, the refusal of compare itself stops the comparison in a step that keeps
// GITHUB_ACTIONS. Of the lines that it reads, only a line that both writes the word in such a form
// and drops that variable passes both, and no program finds it. It reads each file two ways:
//
//   - Its text, as a script (shellHits), each comparison named at the line that holds its
//     spec-check.
//   - A file whose name ends in .yml or .yaml, as YAML reads it: the value of each run: key, in
//     each document up to the first one that YAML cannot read, through an alias, read as a script
//     too. A plain, quoted or folded value over two lines is one line, named at the line where the
//     value starts; a literal block keeps its lines, each named at its own line. From the first
//     document that YAML cannot read, the file is read as text alone, since GitHub runs no step of
//     a workflow or an action that it cannot read. In a folded or plain value, the guard can name a
//     line that Bash does not run, and it never misses one for that reason: YAML makes the line
//     break a space, which a backslash at the end of the line escapes, and the text reading reads
//     the backslash and the line break as a line continuation.
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

// shellHits is the index from 0 of each line of text that runs the comparison, sorted, each line
// once. It reads text in two layers: the words of its commands as Bash reads them (readShell),
// and each text that readShell keeps as inner, read in the same two layers as a script of its own,
// with no state from the text around it, since a nested shell can run it. An inner text is shorter
// than the text that holds it, so the reading ends.
func shellHits(text string) []int { return lineNumbers(text, comparisons(text)) }

// comparisons is the index in text of the spec-check of each comparison of shellHits.
func comparisons(text string) []int {
	read := readShell(text)
	at := read.commands()
	for _, inner := range read.inner {
		for _, n := range comparisons(inner.text) {
			at = append(at, inner.from[n])
		}
	}
	return at
}

// lineNumbers is the line, from 0, of each index of at in text, sorted, each line once.
func lineNumbers(text string, at []int) []int {
	slices.Sort(at)
	var lines []int
	line, counted := 0, 0
	for _, n := range at {
		line += strings.Count(text[counted:n], "\n")
		counted = n
		if len(lines) == 0 || lines[len(lines)-1] != line {
			lines = append(lines, line)
		}
	}
	return lines
}

// shellText is a text as readShell reads it.
type shellText struct {
	// words is each word of the commands of the text, ended by a NUL byte, as Bash gives it to the
	// command. A control operator and the line break that ends a command are words of their own,
	// and a redirection and its word are no words. from is, for each byte of words, the index in
	// the text of the byte that it reads.
	words []byte
	from  []int
	// inner is each text that the guard also reads as a script of its own: each comment, each
	// here-document body, and each word that holds a blank or a line break, which only a quote or an
	// escape puts in a word.
	inner []innerText
}

// innerText is a text of shellText.inner, and for each of its bytes the index in the outer text.
type innerText struct {
	text string
	from []int
}

// commands is the index in the text of the spec-check of each comparison in the words.
func (s shellText) commands() []int {
	var at []int
	for _, match := range compareCall.FindAllIndex(s.words, -1) {
		at = append(at, s.from[match[0]])
	}
	return at
}

// hereDocument is a here-document that a line opens: its delimiter after quote removal, whether a
// quote or an escape is in the delimiter's word, and whether it is <<-, which strips the tabs at
// the start of each line.
type hereDocument struct {
	delimiter    string
	quoted, tabs bool
}

// redirections are Bash's redirection operators, the longest first. The word after each is its
// file, its descriptor or its delimiter, and no argument.
var redirections = []string{"<<<", "<<-", "&>>", "<<", "<&", "<>", ">>", ">&", ">|", "&>", "<", ">"}

// readShell reads text one time, byte by byte, as Bash reads a script, and expands nothing:
//
//   - Outside quotes, a blank ends a word, and ; & | ( ) and a line break end one and are words of
//     their own. A redirection ends a word, and it, its word and the digits of a descriptor right
//     before it, as in 2>, are no words.
//   - A backslash and the line break after it go, outside '…' and $'…', before Bash reads the
//     bytes around them, and so does a backslash at the end of the text. Outside quotes, a
//     backslash keeps the next byte, a blank too, in the word. Inside "…" it does that before
//     $ ` " \ only, and stays before another byte.
//   - '…' is its text, $'…' is the text that its ANSI-C escapes decode to (ansiC), and "…" is its
//     text with the escapes above. $"…" is "…", since no message catalog holds the words of a
//     command. A quoted text over lines is one word.
//   - Outside quotes, a # that starts a word starts a comment, up to the line break, also after a
//     backslash. It holds no words.
//   - <<WORD and <<-WORD open a here-document, whose body starts after the line break that ends
//     the command (hereDocumentBody). It holds no words. Two on one line are read in order.
//   - $(…), <(…) and >(…) hold commands, and `…` holds words. ${…}, $[…], $((…)) and ((…)) hold no
//     comment and no redirection, so a # or a << in them is text. Inside "…" each of them starts
//     outside quotes again, as in Bash.
func readShell(text string) shellText {
	var (
		s shellText
		// nest is the open contexts, the innermost last, each named by a byte: " for "…", ) for
		// $(…), <(…), >(…) and a ( in them, a for $((…)), ((…)) and a ( in them, } for ${…}, ] for
		// $[…], and ` for `…`.
		nest    []byte
		inWord  bool
		start   int            // where the open word starts in s.words
		quoted  bool           // a quote or an escape is in the open word
		target  = -1           // where the word of the last redirection starts in s.words
		opens   byte           // '<' when that redirection is <<, '-' when it is <<-
		pending []hereDocument // the here-documents of the command
	)
	begin := func() {
		if !inWord {
			inWord, start, quoted = true, len(s.words), false
		}
	}
	write := func(c byte, at int) {
		begin()
		s.words, s.from = append(s.words, c), append(s.from, at)
	}
	end := func(at int) {
		if !inWord {
			return
		}
		inWord = false
		word := s.words[start:]
		if bytes.ContainsAny(word, " \t\n") {
			s.inner = append(s.inner, innerText{string(word), slices.Clone(s.from[start:])})
		}
		if start != target {
			s.words, s.from = append(s.words, 0), append(s.from, at)
			return
		}
		if opens != 0 {
			pending = append(pending, hereDocument{string(word), quoted, opens == '-'})
		}
		s.words, s.from, target, opens = s.words[:start], s.from[:start], -1, 0
	}
	token := func(c byte, at int) {
		end(at)
		s.words, s.from, target, opens = append(s.words, c, 0), append(s.from, at, at), -1, 0
	}
	inner := func(from, to int) {
		at := make([]int, to-from)
		for k := range at {
			at[k] = from + k
		}
		s.inner = append(s.inner, innerText{text[from:to], at})
	}
	// next is the index of the byte that Bash reads at i, past each line continuation, and byteAt
	// that byte, or 0 at the end of the text.
	next := func(i int) int {
		for i+1 < len(text) && text[i] == '\\' && text[i+1] == '\n' {
			i += 2
		}
		return i
	}
	byteAt := func(i int) byte {
		if i < len(text) {
			return text[i]
		}
		return 0
	}
	for i := 0; i < len(text); i++ {
		c, in := text[i], byte(0)
		if len(nest) > 0 {
			in = nest[len(nest)-1]
		}
		commands := in == 0 || in == ')' // where a comment and a here-document can start
		words := commands || in == '`'   // where an operator ends a word
		j := next(i + 1)
		switch {
		case c == 0: // Bash keeps no NUL in a word
		case c == '\\' && (i+1 == len(text) || text[i+1] == '\n'):
			i++
		case c == '\\' && in == '"' && !strings.ContainsRune("$`\"\\", rune(text[i+1])):
			write(c, i)
		case c == '\\':
			begin()
			quoted = true
			i++
			write(text[i], i)
		case c == '\'' && in != '"':
			begin()
			quoted = true
			to := strings.IndexByte(text[i+1:], '\'') + i + 1
			if to == i { // no quote closes it, so the text ends inside it
				to = len(text)
			}
			for k := i + 1; k < to; k++ {
				write(text[k], k)
			}
			i = to
		case c == '$' && byteAt(j) == '\'' && in != '"':
			begin()
			quoted = true
			decoded, from, to := ansiC(text, j+1)
			for k, b := range decoded {
				write(b, from[k])
			}
			i = to
		case c == '$' && byteAt(j) == '"' && in != '"':
			// $"…" is a text that Bash translates by the locale's message catalog, and no catalog
			// holds the words of a command, so it is "…", which the byte at j opens.
		case c == '"':
			begin()
			quoted = true
			if in == '"' {
				nest = nest[:len(nest)-1]
			} else {
				nest = append(nest, '"')
			}
		case in != 0 && in != '"' && (c == in || in == 'a' && c == ')'):
			nest = nest[:len(nest)-1]
			if in == ')' || in == '`' {
				end(i)
			}
			write(c, i)
		case c == '$' && byteAt(j) == '(' && byteAt(next(j+1)) == '(',
			c == '(' && byteAt(j) == '(' && commands && !inWord:
			// $((…)) and ((…)) are arithmetic, where << is a shift and # is text.
			write(c, i)
			if c == '$' {
				write('(', j)
				j = next(j + 1)
			}
			write('(', j)
			i = j
			nest = append(nest, 'a', 'a')
		case c == '$' && (byteAt(j) == '(' || byteAt(j) == '{' || byteAt(j) == '['),
			(c == '<' || c == '>') && byteAt(j) == '(' && words:
			write(c, i)
			write(text[j], j)
			nest = append(nest, ")}]"[strings.IndexByte("({[", text[j])])
			i = j
		case c == '(' && (in == ')' || in == 'a'), c == '{' && in == '}', c == '[' && in == ']':
			nest = append(nest, in)
			if in == ')' {
				token(c, i)
			} else {
				write(c, i)
			}
		case c == '`':
			write(c, i)
			nest = append(nest, '`')
		case c == '#' && commands && !inWord:
			eol := strings.IndexByte(text[i:], '\n') + i
			if eol < i {
				eol = len(text)
			}
			inner(i+1, eol)
			i = eol - 1
		case c == ' ' || c == '\t':
			if in == '"' {
				write(c, i)
			} else {
				end(i)
			}
		case c == '\n' && words:
			token(c, i)
			for _, h := range pending {
				var from, to int
				from, to, i = hereDocumentBody(text, i, h)
				inner(from, to)
			}
			pending = pending[:0]
		case c == '\n' && in != '"':
			end(i)
		case (c == '<' || c == '>' || c == '&' && byteAt(j) == '>') && words:
			if inWord && !quoted && len(bytes.TrimLeft(s.words[start:], "0123456789")) == 0 {
				s.words, s.from, inWord = s.words[:start], s.from[:start], false // a descriptor, as 2 in 2>
			}
			end(i)
			k := next(j + 1)
			read := string([]byte{c, byteAt(j), byteAt(k)})
			for _, op := range redirections {
				if strings.HasPrefix(read, op) {
					i = [3]int{i, j, k}[len(op)-1]
					if commands && strings.HasPrefix(op, "<<") && op != "<<<" {
						opens = op[len(op)-1]
					}
					break
				}
			}
			target = len(s.words)
		case strings.IndexByte(";&|()", c) >= 0 && words:
			token(c, i)
		default:
			write(c, i)
		}
	}
	end(len(text))
	return s
}

// hereDocumentBody reads the body of the here-document h, from the line after the line break at i
// of text, as Bash reads it: up to the first line that is its delimiter, each tab at the start of
// a line stripped for <<-, and with each backslash and the line break after it removed first when
// no quote or escape is in the delimiter's word. It answers where the body starts and ends, and the
// index of the line break after the delimiter line, or len(text) when the text ends first.
func hereDocumentBody(text string, i int, h hereDocument) (from, to, next int) {
	from = min(i+1, len(text))
	var line []byte
	for i < len(text) {
		start := i + 1
		line = line[:0]
		for i = start; i < len(text) && text[i] != '\n'; i++ {
			if text[i] == '\\' && !h.quoted && i+1 < len(text) {
				if i++; text[i] == '\n' {
					continue
				}
				line = append(line, '\\')
			}
			line = append(line, text[i])
		}
		if string(line) == h.delimiter || h.tabs && string(bytes.TrimLeft(line, "\t")) == h.delimiter {
			return from, start, i
		}
	}
	return from, len(text), len(text)
}

// ansiC is the text of the ANSI-C quote $'…' of line whose text starts at start, as Bash decodes
// it, for each byte of the text the index in line of the byte or the escape that it decodes, and
// the index of the closing quote, or the last index of line when line does not close it. The
// escapes are Bash's: \a \b \e \E \f \n \r \t \v, \\ \' \" \? for the character itself, \nnn in
// octal (one to three digits), \xHH (one or two hex digits) as a byte, \uHHHH and \UHHHHHHHH (up to
// four and eight hex digits) as a character in UTF-8, and \cx for control-x. Another escape, and
// \x, \u or \U with no digit after it, is its backslash and its letter, as Bash keeps them, so a
// backslash and a line break stay. The text ends at a NUL, since Bash keeps no NUL in a word.
func ansiC(line string, start int) (text []byte, from []int, end int) {
	i, at := start, start
	decoded := func() { // the bytes since the last call decode the byte or the escape at at
		for len(from) < len(text) {
			from = append(from, at)
		}
	}
	for ; i < len(line) && line[i] != '\''; i++ {
		decoded()
		at = i
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
	decoded()
	if nul := bytes.IndexByte(text, 0); nul >= 0 {
		text, from = text[:nul], from[:nul]
	}
	return text, from, min(i, len(line)-1)
}

// digits is the value of the octal (base 8) or hex (base 16) digits at the start of s, at most
// limit of them, and how many there are.
func digits(s string, base, limit int) (value, n int) {
	for ; n < limit && n < len(s); n++ {
		d := digitValue(s[n])
		if d >= base {
			break
		}
		value = value*base + d
	}
	return value, n
}

// digitValue is the value of the hex digit c, in either case, and 16 for a byte that is no hex
// digit.
func digitValue(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	}
	return 16
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
