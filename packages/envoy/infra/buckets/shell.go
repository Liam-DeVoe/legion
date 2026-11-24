package buckets

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// readShell reads a launcher's words as a POSIX shell (dash) would run them, as far as the proof can
// without running them: it answers each text a word can take, a hole (shellHole) standing for text
// the proof cannot know, so that a writer's name in any of them, or one a hole could complete beside
// the text it joins, blocks (launch).
//
// The script after -c is read statement by statement. Each expansion of a name reads the set of every
// value the name can hold there (values): the pod's value (lookup, and the launcher's own NAME=value
// words before the script), or, for a name the pod does not set and the image's environment may,
// a hole; and the literal values the script assigns it. A top-level statement of assignments alone,
// unchained (no &&, || or |), outside any if, case, loop, group, subshell or function, replaces the
// name's values from there on, as the shell does; so do assignments before a special builtin such as
// exec or :, and those of export, readonly, local, declare and typeset. Any other assignment - in a
// branch, a loop or a function, after && or ||, or the ${NAME:=word} default - adds to the values and
// replaces none, and a word in a nested body reads every value the name gets anywhere in the script
// (the union the prescan builds). An assignment before an ordinary command (PROGRAM=/bin/true
// /opt/init.sh) is that command's alone and adds nothing after it. The words of a statement are read
// before its assignments take effect, so ROLE=dispatch exec envoy-$ROLE runs the pod's ROLE.
//
// The expansions it evaluates: $NAME and ${NAME}, $0 to $9 (the words after the -c script; a hole in a
// function's body), $@ and $*, ${NAME:-word}, ${NAME-word}, ${NAME:=word}, ${NAME=word}, ${NAME:+word},
// ${NAME+word} and ${NAME:?word}, and ${NAME#pattern}, ${NAME##pattern}, ${NAME%pattern} and
// ${NAME%%pattern} with a shell-pattern matcher (globMatch). ${#NAME} and $((...)) read as a number;
// a command's output ($(...), `...`), a loop's or read's variable, an indirect ${!NAME}, and an unquoted
// glob read as a hole, and the text of a command substitution is read as a script too. Quotes and
// backslashes are taken out, and a backslash before a line break joins the two lines. A bash-only
// operator on a name (a substring ${N:o:l}, ${N/p/r}, ${N^^}, ${N,,}, ${N@Q}) leaves the reading
// undecided. A value that holds shell text is read again (reread), as eval or a nested shell would.
//
// Every bound - candidates of a name or of a word, a candidate's length, the bytes read again, the
// rounds of the prescan, the depth of rereading - makes the reading bounded, which decides 'writer',
// never 'not a writer'.
func readShell(words []string, lookup func(name string) variable, imageMayVary bool) shellRead {
	r := &shellReader{lookup: lookup, imageMayVary: imageMayVary, prefix: map[string]string{}, walked: map[string]bool{}}
	script, scriptAt := "", -1
	for i, word := range words {
		if name, value, ok := assignmentWord(word); ok && scriptAt < 0 && i < len(words)-1 {
			r.prefix[name] = value
		}
		if commandOption.MatchString(word) && i+1 < len(words) && scriptAt < 0 {
			script, scriptAt = words[i+1], i+1
			r.positional = slices.Clone(words[i+2:])
		}
	}
	r.union = r.prescan(words)
	for i, word := range words {
		ctx := &evalCtx{reader: r, collect: true, positional: true}
		if i == scriptAt {
			r.walk(ctx, script, 0)
			continue
		}
		ctx.mode = modeLiteral
		for _, candidate := range r.evalWord(ctx, word).list {
			r.add(candidate)
		}
	}
	return r.result
}

// shellHole stands, in a candidate text, for text the proof cannot know: a command's output, a loop's
// variable, a value the image's environment may set.
const shellHole = "\x00"

// The bounds of readShell, each of which makes the reading bounded when reached.
const (
	maxNameCandidates  = 4096
	maxWordCandidates  = 4096
	maxCandidateLength = 4096
	maxRereadBytes     = 1 << 20
	maxPrescanRounds   = 8
	maxRereadDepth     = 4
)

// shellRead is what readShell answers.
type shellRead struct {
	// unread is the first expansion of a variable the pod's env sets but does not show, and
	// undecided the first bash-only operator the reader does not evaluate: either may start any
	// writer.
	unread, undecided string
	// bounded names the bound that stopped the reading before it finished, "" when none did.
	bounded string
	// candidates is every text a word of the launcher, or of a script in it, can take.
	candidates []string
}

type shellReader struct {
	lookup       func(name string) variable
	imageMayVary bool
	// prefix is the launcher's own NAME=value words before its script (env NAME=value sh -c ...),
	// which set the name for the shell as the pod's env does.
	prefix     map[string]string
	positional []string
	// union is each name's every value anywhere in the words, which a nested body reads.
	union map[string]values
	// walked is each text read again, so none is read twice.
	walked      map[string]bool
	rereadBytes int
	result      shellRead
}

// add records a candidate text of a word, once.
func (r *shellReader) add(candidate string) {
	if !slices.Contains(r.result.candidates, candidate) {
		r.result.candidates = append(r.result.candidates, candidate)
	}
}

// bound records what stopped the reading, the first bound reached.
func (r *shellReader) bound(what string) {
	if r.result.bounded == "" {
		r.result.bounded = what
	}
}

// values is the set of values a name can hold at a point of the script.
type values struct {
	list []string
	// unset says the name may be unset there, and unread that it may hold a value the pod sets but
	// does not show.
	unset, unread bool
}

func (v *values) addValue(r *shellReader, value string) {
	value = squashHoles(value)
	if slices.Contains(v.list, value) {
		return
	}
	if len(v.list) >= maxNameCandidates || len(value) > maxCandidateLength {
		r.bound("a name's values")
		return
	}
	v.list = append(v.list, value)
}

// squashHoles is text in a canonical form that names exactly the writers text names (markerRank):
// each run of holes and separators (holeSeparator, bytes that cannot be part of a writer's name and
// have no meaning to the shell) that holds a hole is written by its ends alone - a hole end as a
// hole, a separator end as a blank - with one hole between two blank ends. A marker cannot cross a
// separator, so only the run's ends meet the text beside it, and the form keeps each end as it is:
// a hole touching the text beside it, or a separator cutting it off. The values a loop builds by
// joining unknown words, " $a/$b/$c" among them, so stay few.
func squashHoles(text string) string {
	if !strings.Contains(text, shellHole) {
		return text
	}
	var out strings.Builder
	for i := 0; i < len(text); {
		if !holeSeparator(text[i]) && text[i] != shellHole[0] {
			out.WriteByte(text[i])
			i++
			continue
		}
		j := i
		holes := false
		for j < len(text) && (holeSeparator(text[j]) || text[j] == shellHole[0]) {
			holes = holes || text[j] == shellHole[0]
			j++
		}
		run := text[i:j]
		i = j
		if !holes {
			out.WriteString(run)
			continue
		}
		end := func(c byte) string {
			if c == shellHole[0] {
				return shellHole
			}
			return " "
		}
		first, last := end(run[0]), end(run[len(run)-1])
		switch {
		case len(run) == 1:
			out.WriteString(shellHole)
		case first == shellHole && last == shellHole:
			out.WriteString(shellHole)
		case first == shellHole || last == shellHole:
			out.WriteString(first + last)
		default:
			out.WriteString(" " + shellHole + " ")
		}
	}
	return out.String()
}

// holeSeparator reports whether c is a separator squashHoles may fold beside a hole: blank space or
// punctuation that is neither part of a writer's name (markerByte) nor of the shell's syntax.
func holeSeparator(c byte) bool {
	return strings.IndexByte(" \t\n/.:,=@+%~#!?*^[]", c) >= 0
}

func (v *values) merge(r *shellReader, other values) {
	for _, value := range other.list {
		v.addValue(r, value)
	}
	v.unset = v.unset || other.unset
	v.unread = v.unread || other.unread
}

func (v values) clone() values {
	v.list = slices.Clone(v.list)
	return v
}

func (v values) equal(other values) bool {
	return v.unset == other.unset && v.unread == other.unread && slices.Equal(v.list, other.list)
}

// base is the value a name has before the script runs: the launcher's own NAME=value before its
// script, else the pod's (unread when the pod does not show it), else unset - and a hole too when the
// image's environment may set it.
func (r *shellReader) base(name string) values {
	if value, ok := r.prefix[name]; ok {
		return values{list: []string{value}}
	}
	if v := r.lookup(name); v.set {
		if !v.read {
			return values{unread: true}
		}
		return values{list: []string{v.value}}
	}
	if r.imageMayVary {
		return values{list: []string{shellHole}, unset: true}
	}
	return values{unset: true}
}

// evalCtx is how a word is read: the mode of its quoting, and where it is in the script.
type evalCtx struct {
	reader *shellReader
	mode   evalMode
	// union says a name reads every value it gets anywhere (a nested body, a reread, a command's
	// text), and positional that $1 to $9 read the words after the -c script (not inside a function).
	union, positional bool
	depth             int
	// live is each name's values at this point of a top-level walk, nil where union decides.
	live map[string]values
	// self is the name a prescan round assigns, which reads as a hole after the first round.
	self   string
	rounds int
	// collect says the reading's findings (candidates, unread) count; a prescan round's do not.
	collect bool
	// unread says the word read a value the pod does not show, and substituted that it read a
	// value holding shell text ($ or `), which readShell reads again.
	unread, substituted bool
}

type evalMode int

const (
	modeWord    evalMode = iota // a shell word: quotes and backslashes taken out, an unquoted glob a hole
	modeLiteral                 // a launcher's word, which no shell splits: only $ expands
	modePattern                 // the pattern of a trim: glob characters kept, quoted ones escaped
)

// values is the set of values name holds at this point (base, live, union).
func (ctx *evalCtx) values(name string) values {
	r := ctx.reader
	if index, err := strconv.Atoi(name); err == nil {
		if !ctx.positional {
			return values{list: []string{shellHole}}
		}
		if index < len(r.positional) {
			return values{list: []string{r.positional[index]}}
		}
		return values{list: []string{""}}
	}
	if name == ctx.self && ctx.rounds > 0 {
		return values{list: []string{shellHole}}
	}
	var v values
	if live, ok := ctx.live[name]; ok && ctx.live != nil {
		v = live.clone()
	} else {
		v = r.base(name)
	}
	if ctx.union || ctx.live == nil {
		if u, ok := r.union[name]; ok {
			v.merge(r, u)
		}
	}
	if slices.ContainsFunc(v.list, func(value string) bool { return strings.ContainsAny(value, "$`") }) {
		ctx.substituted = true
	}
	return v
}

// cands is a word's candidate texts.
type cands struct{ list []string }

func lit(text string) cands { return cands{list: []string{text}} }

// cat joins every candidate of c with every candidate of next.
func (c cands) cat(r *shellReader, next cands) cands {
	var out []string
	for _, a := range c.list {
		for _, b := range next.list {
			joined := squashHoles(a + b)
			if len(joined) > maxCandidateLength {
				r.bound("a candidate's length")
				continue
			}
			if !slices.Contains(out, joined) {
				if len(out) >= maxWordCandidates {
					r.bound("a word's candidates")
					break
				}
				out = append(out, joined)
			}
		}
	}
	return cands{list: out}
}

// union adds next's candidates to c's.
func (c cands) union(r *shellReader, next cands) cands {
	out := slices.Clone(c.list)
	for _, b := range next.list {
		if !slices.Contains(out, b) {
			if len(out) >= maxWordCandidates {
				r.bound("a word's candidates")
				break
			}
			out = append(out, b)
		}
	}
	return cands{list: out}
}

// evalWord is the candidates of the word text in ctx's mode.
func (r *shellReader) evalWord(ctx *evalCtx, text string) cands {
	out := lit("")
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = out.cat(r, lit(buf.String()))
			buf.Reset()
		}
	}
	double := false
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '\\' && ctx.mode != modeLiteral:
			if i+1 >= len(text) {
				i++
				continue
			}
			next := text[i+1]
			switch {
			case next == '\n':
			case double && !strings.ContainsRune("$`\"\\", rune(next)):
				buf.WriteByte('\\')
				buf.WriteString(ctx.escaped(next, true))
			default:
				buf.WriteString(ctx.escaped(next, true))
			}
			i += 2
		case c == '\'' && !double && ctx.mode != modeLiteral:
			end := strings.IndexByte(text[i+1:], '\'')
			if end < 0 {
				end = len(text) - i - 1
			}
			for _, b := range []byte(text[i+1 : i+1+end]) {
				buf.WriteString(ctx.escaped(b, true))
			}
			i += end + 2
		case c == '"' && ctx.mode != modeLiteral:
			double = !double
			i++
		case c == '$':
			flush()
			expanded, used := r.dollar(ctx, text[i:])
			out = out.cat(r, expanded)
			i += used
		case c == '`' && ctx.mode != modeLiteral:
			flush()
			end := i + 1
			for end < len(text) && text[end] != '`' {
				if text[end] == '\\' {
					end++
				}
				end++
			}
			inner := text[i+1 : min(end, len(text))]
			r.substitution(ctx, inner)
			out = out.cat(r, lit(shellHole))
			i = end + 1
		case strings.IndexByte("*?[", c) >= 0 && !double && ctx.mode == modeWord:
			flush()
			out = out.cat(r, lit(shellHole))
			i++
		default:
			buf.WriteString(ctx.escaped(c, double))
			i++
		}
	}
	flush()
	return out
}

// escaped is the byte c of a word as it is matched: in a trim's pattern, a quoted glob character is
// escaped, as the shell matches it literally.
func (ctx *evalCtx) escaped(c byte, quoted bool) string {
	if ctx.mode == modePattern && quoted && strings.IndexByte("*?[]\\", c) >= 0 {
		return "\\" + string(c)
	}
	return string(c)
}

// substitution reads the text of a command substitution as a script, in a subshell: a writer it
// runs blocks as one the script runs does.
func (r *shellReader) substitution(ctx *evalCtx, inner string) {
	if ctx.collect {
		r.walk(&evalCtx{reader: r, union: true, positional: ctx.positional, collect: true, depth: ctx.depth + 1}, inner, ctx.depth+1)
	}
}

// dollar is the candidates of the expansion text starts with, and the bytes it takes.
func (r *shellReader) dollar(ctx *evalCtx, text string) (cands, int) {
	if len(text) == 1 {
		return lit("$"), 1
	}
	switch c := text[1]; {
	case strings.HasPrefix(text, "$(("):
		end := spanEnd(text, 1)
		if end < 0 {
			return lit("0"), len(text)
		}
		return lit("0"), end
	case c == '(':
		end := spanEnd(text, 1)
		if end < 0 {
			r.substitution(ctx, text[2:])
			return lit(shellHole), len(text)
		}
		r.substitution(ctx, text[2:end-1])
		return lit(shellHole), end
	case c == '{':
		end := spanEnd(text, 1)
		if end < 0 {
			return lit(shellHole), len(text)
		}
		return r.braced(ctx, text[2:end-1], text[:end]), end
	case isNameStart(c):
		j := nameEnd(text, 1)
		return r.named(ctx, text[1:j], text[:j]), j
	case c >= '0' && c <= '9':
		return r.named(ctx, text[1:2], text[:2]), 2
	case c == '@' || c == '*':
		return r.arguments(ctx), 2
	case strings.IndexByte("?$#!-", c) >= 0:
		return lit("0"), 2
	default:
		return lit("$"), 1
	}
}

// arguments is $@ and $*: the words after $0, joined.
func (r *shellReader) arguments(ctx *evalCtx) cands {
	if !ctx.positional {
		return lit(shellHole)
	}
	if len(r.positional) > 1 {
		return lit(strings.Join(r.positional[1:], " "))
	}
	return lit("")
}

// named is the candidates of the plain expansion of name, written as display.
func (r *shellReader) named(ctx *evalCtx, name, display string) cands {
	v := ctx.values(name)
	if v.unread {
		ctx.unread = true
		if ctx.collect && r.result.unread == "" {
			r.result.unread = display
		}
	}
	out := cands{list: slices.Clone(v.list)}
	if v.unset || v.unread && len(v.list) == 0 {
		out = out.union(r, lit(""))
	}
	return out
}

// parameterOperators are the POSIX operators of ${NAME<operator>word}, longest first.
var parameterOperators = []string{":-", ":=", ":?", ":+", "##", "%%", "-", "=", "?", "+", "#", "%"}

// braced is the candidates of ${inner}, written as whole.
func (r *shellReader) braced(ctx *evalCtx, inner, whole string) cands {
	switch {
	case strings.HasPrefix(inner, "!") && len(inner) > 1:
		return lit(shellHole)
	case strings.HasPrefix(inner, "#") && len(inner) > 1 && paramEnd(inner, 1) == len(inner):
		return lit("0")
	}
	end := paramEnd(inner, 0)
	if end == 0 {
		return lit(shellHole)
	}
	name, rest := inner[:end], inner[end:]
	switch name {
	case "@", "*":
		if rest == "" {
			return r.arguments(ctx)
		}
		return lit(shellHole)
	case "?", "$", "#", "!", "-":
		return lit("0")
	}
	display := "${" + name + "}"
	if rest == "" {
		return r.named(ctx, name, display)
	}
	operator := ""
	for _, op := range parameterOperators {
		if strings.HasPrefix(rest, op) {
			operator = op
			break
		}
	}
	if operator == "" {
		if ctx.collect && r.result.undecided == "" {
			r.result.undecided = whole
		}
		return lit(shellHole)
	}
	wordText := rest[len(operator):]
	v := ctx.values(name)
	if v.unread {
		ctx.unread = true
		if ctx.collect && r.result.unread == "" {
			r.result.unread = display
		}
	}
	valueCands := cands{list: slices.Clone(v.list)}
	emptyOrUnset := v.unset || v.unread || slices.Contains(v.list, "") || slices.Contains(v.list, shellHole)
	switch operator {
	case ":-", ":=", "-", "=":
		takesWord := v.unset || v.unread || operator[0] == ':' && emptyOrUnset
		out := cands{}
		for _, value := range v.list {
			if value != "" || operator[0] != ':' {
				out = out.union(r, lit(value))
			}
		}
		if takesWord {
			word := r.evalWord(&evalCtx{reader: r, mode: modeWord, union: ctx.union, positional: ctx.positional, depth: ctx.depth, live: ctx.live, self: ctx.self, rounds: ctx.rounds, collect: ctx.collect}, wordText)
			out = out.union(r, word)
			if (operator == ":=" || operator == "=") && ctx.live != nil {
				assigned := ctx.live[name]
				if _, ok := ctx.live[name]; !ok {
					assigned = r.base(name)
				}
				for _, value := range word.list {
					assigned.addValue(r, value)
				}
				ctx.live[name] = assigned
			}
		}
		if len(out.list) == 0 {
			out = lit("")
		}
		return out
	case ":+", "+":
		word := r.evalWord(&evalCtx{reader: r, mode: modeWord, union: ctx.union, positional: ctx.positional, depth: ctx.depth, live: ctx.live, self: ctx.self, rounds: ctx.rounds, collect: ctx.collect}, wordText)
		out := cands{}
		set := len(v.list) > 0
		if set && (operator == "+" || slices.ContainsFunc(v.list, func(value string) bool { return value != "" })) {
			out = out.union(r, word)
		}
		if v.unset || operator == ":+" && slices.Contains(v.list, "") || slices.Contains(v.list, shellHole) {
			out = out.union(r, lit(""))
		}
		if len(out.list) == 0 {
			out = lit("")
		}
		return out
	case ":?", "?":
		if v.unset {
			valueCands = valueCands.union(r, lit(""))
		}
		return valueCands
	default: // # ## % %%
		pattern := r.evalWord(&evalCtx{reader: r, mode: modePattern, union: ctx.union, positional: ctx.positional, depth: ctx.depth, live: ctx.live, self: ctx.self, rounds: ctx.rounds, collect: ctx.collect}, wordText)
		out := cands{}
		if v.unset {
			out = out.union(r, lit(""))
		}
		for _, value := range v.list {
			if strings.Contains(value, shellHole) || slices.ContainsFunc(pattern.list, func(p string) bool { return strings.Contains(p, shellHole) }) {
				out = out.union(r, lit(shellHole+value+shellHole))
				continue
			}
			for _, p := range pattern.list {
				out = out.union(r, lit(trim(value, operator, p)))
			}
		}
		if len(out.list) == 0 {
			out = lit("")
		}
		return out
	}
}

// paramEnd is the end of the parameter name that starts at i of text: a name, digits, or one special
// character.
func paramEnd(text string, i int) int {
	if i >= len(text) {
		return i
	}
	switch c := text[i]; {
	case isNameStart(c):
		return nameEnd(text, i)
	case c >= '0' && c <= '9':
		j := i
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		return j
	case strings.IndexByte("@*?$#!-", c) >= 0:
		return i + 1
	}
	return i
}

// nameEnd is the end of the shell name that starts at i of text.
func nameEnd(text string, i int) int {
	j := i + 1
	for j < len(text) && isNameChar(text[j]) {
		j++
	}
	return j
}

func isNameStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isNameChar(c byte) bool { return isNameStart(c) || c >= '0' && c <= '9' }

// assignmentWord reports whether word is NAME=value, and its parts.
func assignmentWord(word string) (name, value string, ok bool) {
	if word == "" || !isNameStart(word[0]) {
		return "", "", false
	}
	j := nameEnd(word, 0)
	if j < len(word) && word[j] == '=' {
		return word[:j], word[j+1:], true
	}
	return "", "", false
}

// commandOption is a shell's option word that holds -c, such as -c, -ec or -lc: the word after it
// is the script, and those after that its $0, $1 and on.
var commandOption = regexp.MustCompile(`^-[a-zA-Z]*c[a-zA-Z]*$`)

// trim is value with operator's prefix or suffix matching pattern removed: # the shortest prefix,
// ## the longest, % the shortest suffix, %% the longest.
func trim(value, operator, pattern string) string {
	switch operator {
	case "#":
		for i := 0; i <= len(value); i++ {
			if globMatch(pattern, value[:i]) {
				return value[i:]
			}
		}
	case "##":
		for i := len(value); i >= 0; i-- {
			if globMatch(pattern, value[:i]) {
				return value[i:]
			}
		}
	case "%":
		for i := len(value); i >= 0; i-- {
			if globMatch(pattern, value[i:]) {
				return value[:i]
			}
		}
	case "%%":
		for i := 0; i <= len(value); i++ {
			if globMatch(pattern, value[i:]) {
				return value[:i]
			}
		}
	}
	return value
}

// globMatch reports whether s matches the shell pattern p: * any text, ? one byte, [...] a class
// ([!...] or [^...] negated, a-b a range), a backslash the next byte literally.
func globMatch(p, s string) bool {
	pi, si := 0, 0
	star, from := -1, 0
	for si < len(s) {
		if pi < len(p) && p[pi] == '*' {
			star, from = pi, si
			pi++
			continue
		}
		if pi < len(p) {
			if width, ok := matchOne(p[pi:], s[si]); ok {
				pi += width
				si++
				continue
			}
		}
		if star < 0 {
			return false
		}
		pi, from = star+1, from+1
		si = from
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// matchOne reports whether the pattern element at the start of p matches c, and its width.
func matchOne(p string, c byte) (int, bool) {
	switch p[0] {
	case '?':
		return 1, true
	case '\\':
		if len(p) > 1 {
			return 2, p[1] == c
		}
		return 1, c == '\\'
	case '[':
		i, negate := 1, false
		if i < len(p) && (p[i] == '!' || p[i] == '^') {
			negate, i = true, i+1
		}
		matched, first := false, true
		for i < len(p) && (p[i] != ']' || first) {
			first = false
			lo := p[i]
			if lo == '\\' && i+1 < len(p) {
				i++
				lo = p[i]
			}
			hi := lo
			if i+2 < len(p) && p[i+1] == '-' && p[i+2] != ']' {
				hi = p[i+2]
				i += 2
			}
			if lo <= c && c <= hi {
				matched = true
			}
			i++
		}
		if i >= len(p) {
			return 1, c == '['
		}
		return i + 1, matched != negate
	}
	return 1, p[0] == c
}

// shellStmt is one simple command of a script: its words, and where it is.
type shellStmt struct {
	words []shellToken
	// chained says && , || or | joins it to the command before or after it, or & runs it apart.
	chained bool
	// depth is the number of groups, subshells, branches, loops and functions it is inside, and
	// braced says one of them is a { group, a function's body among them.
	depth  int
	braced bool
	// lead is the reserved words before its command (!, if, while, ...).
	lead int
}

type shellToken struct {
	text     string
	redirect bool
}

// walk reads the script text: each statement's words (evalWord), with its assignments applied to
// the values that follow (live), as readShell says.
func (r *shellReader) walk(outer *evalCtx, text string, depth int) {
	statements, bad := splitStatements(text)
	live := map[string]values{}
	if outer.live != nil {
		for name, v := range outer.live {
			live[name] = v.clone()
		}
	}
	for _, s := range statements {
		nested := s.depth > 0 || depth > 0
		cls := classify(s)
		runs := runsScript(s)
		for _, token := range s.words {
			ctx := &evalCtx{reader: r, mode: modeWord, union: nested || outer.union, positional: outer.positional && !s.braced, depth: depth, live: live, collect: outer.collect}
			candidates := r.evalWord(ctx, token.text)
			if !outer.collect {
				continue
			}
			for _, candidate := range candidates.list {
				r.add(candidate)
				// A value brought in that holds shell text is read again as a script, as eval or a
				// nested shell would, and so is every word of a command that runs its words as one.
				if ctx.substituted || runs {
					r.reread(candidate, depth)
				}
			}
		}
		replaces := cls.kills && !bad && !nested && !s.chained
		for _, a := range cls.assignments {
			ctx := &evalCtx{reader: r, mode: modeWord, union: nested || outer.union, positional: outer.positional && !s.braced, depth: depth, live: live, collect: outer.collect}
			assigned := values{}
			switch {
			case a.hole:
				assigned.addValue(r, shellHole)
			case a.append:
				// NAME+=value joins value to each value NAME holds here.
				current, ok := live[a.name]
				if !ok {
					current = r.base(a.name)
				}
				prefixes := slices.Clone(current.list)
				if current.unset || len(prefixes) == 0 {
					prefixes = append(prefixes, "")
				}
				for _, value := range r.evalWord(ctx, a.value).list {
					for _, prefix := range prefixes {
						assigned.addValue(r, prefix+value)
					}
				}
				assigned.unread = ctx.unread || current.unread
			default:
				for _, value := range r.evalWord(ctx, a.value).list {
					assigned.addValue(r, value)
				}
				assigned.unread = ctx.unread
			}
			switch {
			case replaces && !a.hole:
				live[a.name] = assigned
			case cls.regularPrefix && !nested && !s.chained && !a.hole:
				// an ordinary command's prefix assignment is its own, and lasts no longer
			default:
				current, ok := live[a.name]
				if !ok {
					current = r.base(a.name)
				}
				current.merge(r, assigned)
				live[a.name] = current
			}
		}
	}
}

// shellAssignment is an assignment a statement makes: NAME=value, NAME+=value, or a name a for loop,
// read or getopts sets to a value the proof cannot know (hole).
type shellAssignment struct {
	name, value  string
	append, hole bool
}

type statementClass struct {
	assignments []shellAssignment
	// kills says the statement's assignments last past it and replace the name's values: assignments
	// alone, or before a special builtin, or those of a declaration builtin.
	kills bool
	// regularPrefix says they are an ordinary command's, for that command alone.
	regularPrefix bool
}

// specialBuiltins are dash's special builtins, after which a prefix assignment lasts.
var specialBuiltins = map[string]bool{":": true, ".": true, "break": true, "continue": true, "eval": true, "exec": true, "exit": true, "export": true, "readonly": true, "return": true, "set": true, "shift": true, "times": true, "trap": true, "unset": true}

// declarationBuiltins assign their NAME=value words.
var declarationBuiltins = map[string]bool{"export": true, "readonly": true, "local": true, "declare": true, "typeset": true}

// leadWords are the reserved words a command may follow in a statement.
var leadWords = map[string]bool{"!": true, "if": true, "elif": true, "while": true, "until": true, "then": true, "do": true, "else": true, "time": true}

// classify reads a statement's assignments and whether they replace the names' values.
func classify(s shellStmt) statementClass {
	var cls statementClass
	words := slices.DeleteFunc(slices.Clone(s.words), func(t shellToken) bool { return t.redirect })
	i := 0
	for i < len(words) && leadWords[words[i].text] {
		i++
	}
	lead := i
	if i < len(words) && (words[i].text == "for" || words[i].text == "select") {
		if i+1 < len(words) && words[i+1].text != "" && isNameStart(words[i+1].text[0]) && nameEnd(words[i+1].text, 0) == len(words[i+1].text) {
			cls.assignments = append(cls.assignments, shellAssignment{name: words[i+1].text, hole: true})
		}
		return cls
	}
	var prefix []shellAssignment
	for i < len(words) {
		name, value, ok := assignmentWord(words[i].text)
		appendTo := false
		if !ok {
			if n, v, isAppend := appendWord(words[i].text); isAppend {
				name, value, ok, appendTo = n, v, true, true
			}
		}
		if !ok {
			break
		}
		prefix = append(prefix, shellAssignment{name: name, value: value, append: appendTo})
		i++
	}
	command := ""
	if i < len(words) {
		command = words[i].text
	}
	cls.assignments = prefix
	switch {
	case command == "read" || command == "getopts":
		for _, w := range words[i+1:] {
			if w.text != "" && isNameStart(w.text[0]) && nameEnd(w.text, 0) == len(w.text) {
				cls.assignments = append(cls.assignments, shellAssignment{name: w.text, hole: true})
			}
		}
	case declarationBuiltins[command]:
		for _, w := range words[i+1:] {
			if name, value, ok := assignmentWord(w.text); ok {
				cls.assignments = append(cls.assignments, shellAssignment{name: name, value: value})
			}
		}
	}
	cls.kills = lead == 0 && len(cls.assignments) > 0 && (command == "" || specialBuiltins[command] || declarationBuiltins[command])
	cls.regularPrefix = len(prefix) > 0 && command != "" && !specialBuiltins[command] && !declarationBuiltins[command]
	return cls
}

// appendWord reports whether word is NAME+=value, and its parts.
func appendWord(word string) (name, value string, ok bool) {
	if word == "" || !isNameStart(word[0]) {
		return "", "", false
	}
	j := nameEnd(word, 0)
	if strings.HasPrefix(word[j:], "+=") {
		return word[:j], word[j+2:], true
	}
	return "", "", false
}

// splitStatements reads text into simple commands, each with its depth. bad says the reserved words
// and brackets did not balance, so no assignment may replace a name's values.
func splitStatements(text string) (statements []shellStmt, bad bool) {
	type frame struct {
		kind       byte // ( { i(f) l(oop) c(ase)
		expectsPat bool
	}
	var frames []frame
	var cur shellStmt
	chainedNext := false
	pendingFunction := false
	braced := func() bool {
		return slices.ContainsFunc(frames, func(f frame) bool { return f.kind == '{' })
	}
	flush := func(chainAfter bool) {
		if len(cur.words) > 0 {
			cur.chained = cur.chained || chainAfter
			statements = append(statements, cur)
		}
		cur = shellStmt{depth: len(frames), braced: braced(), chained: chainedNext}
		chainedNext = false
	}
	cur = shellStmt{}
	pop := func(kind byte) {
		if len(frames) == 0 || frames[len(frames)-1].kind != kind {
			bad = true
			if len(frames) == 0 {
				return
			}
		}
		frames = frames[:len(frames)-1]
	}
	commandStart := func() bool {
		for _, w := range cur.words[cur.lead:] {
			if !w.redirect {
				return false
			}
		}
		return true
	}
	for _, t := range tokenize(text) {
		switch t.op {
		case ";", "\n":
			flush(false)
		case "&":
			flush(true)
		case ";;":
			flush(false)
			if len(frames) > 0 && frames[len(frames)-1].kind == 'c' {
				frames[len(frames)-1].expectsPat = true
			}
		case "&&", "||", "|", "|&":
			flush(true)
			chainedNext = true
			cur.chained = true
		case "(":
			if commandStart() {
				flush(false)
				frames = append(frames, frame{kind: '('})
				cur.depth, cur.braced = len(frames), braced()
			} else {
				pendingFunction = true
			}
		case ")":
			switch {
			case pendingFunction:
				// the () of a function's name: its body is the group that follows
				pendingFunction = false
				flush(false)
			case len(frames) > 0 && frames[len(frames)-1].kind == 'c' && frames[len(frames)-1].expectsPat:
				frames[len(frames)-1].expectsPat = false
				flush(false)
			default:
				flush(false)
				pop('(')
				cur.depth, cur.braced = len(frames), braced()
			}
		case "":
			word := t.text
			if t.redirect {
				cur.words = append(cur.words, shellToken{text: word, redirect: true})
				continue
			}
			inCase := len(frames) > 0 && frames[len(frames)-1].kind == 'c'
			if inCase && frames[len(frames)-1].expectsPat && word == "esac" {
				flush(false)
				pop('c')
				cur.depth, cur.braced = len(frames), braced()
				continue
			}
			if inCase && frames[len(frames)-1].expectsPat {
				cur.words = append(cur.words, shellToken{text: word})
				continue
			}
			if !commandStart() {
				if word == "in" && len(cur.words) > 0 && cur.words[0].text == "case" && inCase {
					frames[len(frames)-1].expectsPat = true
				}
				cur.words = append(cur.words, shellToken{text: word})
				continue
			}
			switch word {
			case "{":
				flush(false)
				frames = append(frames, frame{kind: '{'})
				cur.depth, cur.braced = len(frames), braced()
			case "}":
				flush(false)
				pop('{')
				cur.depth, cur.braced = len(frames), braced()
			case "if", "while", "until":
				flush(false)
				frames = append(frames, frame{kind: map[string]byte{"if": 'i', "while": 'l', "until": 'l'}[word]})
				cur.depth, cur.braced = len(frames), braced()
				cur.words = append(cur.words, shellToken{text: word})
				cur.lead = 1
			case "for", "select":
				flush(false)
				frames = append(frames, frame{kind: 'l'})
				cur.depth, cur.braced = len(frames), braced()
				cur.words = append(cur.words, shellToken{text: word})
			case "case":
				flush(false)
				frames = append(frames, frame{kind: 'c'})
				cur.depth, cur.braced = len(frames), braced()
				cur.words = append(cur.words, shellToken{text: word})
			case "then", "do", "else", "elif":
				flush(false)
				cur.words = append(cur.words, shellToken{text: word})
				cur.lead = 1
			case "fi":
				flush(false)
				pop('i')
				cur.depth, cur.braced = len(frames), braced()
			case "done":
				flush(false)
				pop('l')
				cur.depth, cur.braced = len(frames), braced()
			case "esac":
				flush(false)
				pop('c')
				cur.depth, cur.braced = len(frames), braced()
			case "!", "time":
				cur.words = append(cur.words, shellToken{text: word})
				cur.lead++
			default:
				cur.words = append(cur.words, shellToken{text: word})
			}
		}
	}
	flush(false)
	return statements, bad || len(frames) > 0
}

type rawToken struct {
	op, text string
	redirect bool
}

// tokenize reads text into words and operators: a word runs to blank space or an operator, quotes,
// backslashes, $(...), ${...} and `...` kept whole in it; a backslash before a line break joins the
// lines; a # starting a word starts a comment.
func tokenize(text string) []rawToken {
	var tokens []rawToken
	redirectNext := false
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\\' && i+1 < len(text) && text[i+1] == '\n':
			i += 2
		case c == '#':
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case c == '\n':
			tokens = append(tokens, rawToken{op: "\n"})
			i++
		case c == ';':
			if strings.HasPrefix(text[i:], ";;") {
				tokens = append(tokens, rawToken{op: ";;"})
				i += 2
			} else {
				tokens = append(tokens, rawToken{op: ";"})
				i++
			}
		case c == '&' || c == '|':
			op := string(c)
			if i+1 < len(text) && (text[i+1] == c || c == '|' && text[i+1] == '&') {
				op = text[i : i+2]
			}
			tokens = append(tokens, rawToken{op: op})
			i += len(op)
		case c == '(' || c == ')':
			tokens = append(tokens, rawToken{op: string(c)})
			i++
		case c == '<' || c == '>':
			j := i + 1
			for j < len(text) && strings.IndexByte("<>&|-", text[j]) >= 0 {
				j++
			}
			i = j
			redirectNext = true
		default:
			end := wordEnd(text, i)
			tokens = append(tokens, rawToken{text: text[i:end], redirect: redirectNext})
			redirectNext = false
			i = end
		}
	}
	return tokens
}

// wordEnd is the end of the word that starts at i of text.
func wordEnd(text string, i int) int {
	for i < len(text) {
		c := text[i]
		switch {
		case c == '\\':
			i += 2
		case c == '\'':
			end := strings.IndexByte(text[i+1:], '\'')
			if end < 0 {
				return len(text)
			}
			i += end + 2
		case c == '"':
			i = quoteEnd(text, i)
		case c == '$' && i+1 < len(text) && (text[i+1] == '(' || text[i+1] == '{'):
			end := spanEnd(text, i+1)
			if end < 0 {
				return len(text)
			}
			i = end
		case c == '`':
			j := i + 1
			for j < len(text) && text[j] != '`' {
				if text[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1
		case strings.IndexByte(" \t\r\n;&|()<>", c) >= 0:
			return i
		default:
			i++
		}
	}
	return min(i, len(text))
}

// quoteEnd is the index after the double quote that closes the one at i of text.
func quoteEnd(text string, i int) int {
	for j := i + 1; j < len(text); j++ {
		switch text[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		case '$':
			if j+1 < len(text) && (text[j+1] == '(' || text[j+1] == '{') {
				if end := spanEnd(text, j+1); end > 0 {
					j = end - 1
				}
			}
		case '`':
			for j++; j < len(text) && text[j] != '`'; j++ {
				if text[j] == '\\' {
					j++
				}
			}
		}
	}
	return len(text)
}

// spanEnd is the index after the bracket that closes the ( or { at open in text, or -1: brackets
// nest, and quotes and backslashes are skipped.
func spanEnd(text string, open int) int {
	closer := map[byte]byte{'(': ')', '{': '}'}[text[open]]
	opener := text[open]
	depth := 0
	for i := open; i < len(text); i++ {
		switch c := text[i]; c {
		case '\\':
			i++
		case '\'':
			end := strings.IndexByte(text[i+1:], '\'')
			if end < 0 {
				return -1
			}
			i += end + 1
		case '"':
			i = quoteEnd(text, i) - 1
		case opener:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// prescan builds the union: each name's every value anywhere in the words and the pod values they
// read as scripts, from the literal assignments (NAME=value, NAME+=value, ${NAME:=word}) and the
// names a for loop, read or getopts sets (a hole). The assignments are read again until no value
// changes, a name's own value reading as a hole after the first round (so PROGRAM=${PROGRAM}dispatch
// ends); if values still change after maxPrescanRounds the reading is bounded.
func (r *shellReader) prescan(words []string) map[string]values {
	var assignments []shellAssignment
	for _, text := range r.prescanTexts(words) {
		assignments = append(assignments, scanAssignments(text)...)
	}
	union := map[string]values{}
	if len(assignments) == 0 {
		return union
	}
	for round := 0; ; round++ {
		next := map[string]values{}
		for _, a := range assignments {
			if _, ok := next[a.name]; !ok {
				next[a.name] = r.base(a.name)
			}
		}
		for _, a := range assignments {
			v := next[a.name]
			if a.hole {
				v.addValue(r, shellHole)
			} else {
				ctx := &evalCtx{reader: r, mode: modeWord, union: true, positional: true, self: a.name, rounds: round}
				saved := r.union
				r.union = union
				for _, value := range r.evalWord(ctx, a.value).list {
					if a.append {
						value = shellHole + value
					}
					v.addValue(r, value)
				}
				r.union = saved
				v.unread = v.unread || ctx.unread
			}
			next[a.name] = v
		}
		changed := len(next) != len(union)
		for name, v := range next {
			if !v.equal(union[name]) {
				changed = true
			}
		}
		union = next
		if !changed {
			return union
		}
		if round+1 >= maxPrescanRounds {
			r.bound("the prescan's rounds")
			return union
		}
	}
}

// prescanTexts are the texts readShell may read as a script: the launcher's words, and each value of
// the pod's env they reference that holds shell text, and so on.
func (r *shellReader) prescanTexts(words []string) []string {
	texts := slices.Clone(words)
	seen := map[string]bool{}
	for i := 0; i < len(texts) && i < 64; i++ {
		for _, match := range referencePattern.FindAllStringSubmatch(texts[i], -1) {
			name := match[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			value, ok := r.prefix[name]
			if !ok {
				v := r.lookup(name)
				value, ok = v.value, v.set && v.read
			}
			if ok && strings.ContainsAny(value, "$'\"\\=`") {
				texts = append(texts, value)
			}
		}
	}
	return texts
}

// referencePattern is a reference to a name, $NAME or ${NAME.
var referencePattern = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// scanAssignments is every literal assignment in text, wherever it is: NAME=value or NAME+=value
// after a boundary, ${NAME:=word} and ${NAME=word}, and the names for, read and getopts set.
func scanAssignments(text string) []shellAssignment {
	var out []shellAssignment
	boundary := func(i int) bool {
		return i == 0 || strings.IndexByte(" \t\n;&|(){}'\"`", text[i-1]) >= 0
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '\\' && i+1 < len(text) && text[i+1] == '\n' {
			i++
			continue
		}
		if c == '$' && i+1 < len(text) && text[i+1] == '{' {
			if end := spanEnd(text, i+1); end > 0 {
				inner := text[i+2 : end-1]
				if j := paramEnd(inner, 0); j > 0 && isNameStart(inner[0]) {
					rest := inner[j:]
					for _, op := range []string{":=", "="} {
						if word, ok := strings.CutPrefix(rest, op); ok {
							out = append(out, shellAssignment{name: inner[:j], value: word})
							break
						}
					}
				}
			}
			continue
		}
		if !isNameStart(c) || !boundary(i) {
			continue
		}
		j := nameEnd(text, i)
		name := text[i:j]
		switch {
		case strings.HasPrefix(text[j:], "="):
			end := wordEnd(text, j+1)
			out = append(out, shellAssignment{name: name, value: strings.ReplaceAll(text[j+1:end], "\\\n", "")})
		case strings.HasPrefix(text[j:], "+="):
			end := wordEnd(text, j+2)
			out = append(out, shellAssignment{name: name, value: text[j+2 : end], append: true})
		case (name == "read" || name == "getopts" || name == "for" || name == "select") && j < len(text) && (text[j] == ' ' || text[j] == '\t'):
			out = append(out, holeNames(text[j:], name == "for" || name == "select")...)
		}
		i = j - 1
	}
	return out
}

// holeNames is the names a for loop (first only) or read and getopts set, read from text, the rest
// of their command.
func holeNames(text string, first bool) []shellAssignment {
	var out []shellAssignment
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case strings.IndexByte(";&|\n<>", c) >= 0:
			return out
		default:
			end := wordEnd(text, i)
			word := text[i:end]
			if word != "" && isNameStart(word[0]) && nameEnd(word, 0) == len(word) {
				out = append(out, shellAssignment{name: word, hole: true})
				if first {
					return out
				}
			}
			i = end
		}
	}
	return out
}

// reread reads candidate again as a script, as eval or a nested shell would: a text a word took from
// a value holding shell text, or a word of a command that runs its words as a script (runsScript).
// A text already read again is not read again, so a value that expands to itself ends; deeper than
// maxRereadDepth, a text that still holds an expansion leaves the reading bounded.
func (r *shellReader) reread(candidate string, depth int) {
	if !strings.ContainsAny(candidate, "$`\\'\"") || r.walked[candidate] {
		return
	}
	r.walked[candidate] = true
	r.rereadBytes += len(candidate)
	if r.rereadBytes > maxRereadBytes {
		r.bound("the bytes read again")
		return
	}
	if depth+1 > maxRereadDepth {
		if strings.Contains(candidate, "$") {
			r.bound("the depth of reading again")
		}
		return
	}
	r.walk(&evalCtx{reader: r, union: true, positional: true, collect: true}, candidate, depth+1)
}

// scriptRunners are the commands that run their words as a script.
var scriptRunners = map[string]bool{"eval": true, "sh": true, "bash": true, "dash": true, "ash": true, "ksh": true, "zsh": true, ".": true, "source": true}

// runsScript reports whether a word of s is a command that runs its words as a script (eval, a
// shell, source), by its base name.
func runsScript(s shellStmt) bool {
	return slices.ContainsFunc(s.words, func(t shellToken) bool {
		word := strings.Trim(t.text, `"'`)
		if k := strings.LastIndexByte(word, '/'); k >= 0 {
			word = word[k+1:]
		}
		return scriptRunners[word]
	})
}

// shellNames is what a shell reading names, for a test: the writer the words name (launchMarkers),
// listenerBinary before dispatchBinary; undecided says the reading is unread, undecided or bounded,
// which decides 'writer'.
func shellNames(words []string, lookup func(name string) variable, imageMayVary bool) (binary string, undecided bool) {
	read := readShell(words, lookup, imageMayVary)
	if read.unread != "" || read.undecided != "" || read.bounded != "" {
		return "", true
	}
	best := 4
	for _, text := range append([]string{strings.Join(words, "\n")}, read.candidates...) {
		if rank, name := markerRank(text); rank < best {
			best, binary = rank, name
		}
	}
	return binary, false
}

// markerRank ranks the writer names a text holds: 0 envoy-listener (or ENVOY_MACHINE_ID,
// ENVOY_WEBHOOKS), 2 envoy-dispatch, 4 none. A hole stands for text the pod does not show, which the
// proof does not read as part of a name: a program a launcher picks through one is best effort
// (bestEffortWarning).
func markerRank(text string) (int, string) {
	best, name := 4, ""
	for _, marker := range launchMarkers {
		if !strings.Contains(text, marker.marker) {
			continue
		}
		rank := 2
		if marker.binary == listenerBinary {
			rank = 0
		}
		if rank < best {
			best, name = rank, marker.binary
		}
	}
	return best, name
}
