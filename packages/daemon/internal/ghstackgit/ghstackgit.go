// Package ghstackgit is what the git gh-stack runs under `legion gh stack` admits: the argument
// lists the pinned gh-stack release starts git with (shapes, generated from its source by
// generate.sh), and Check, which cmd/gh-stack-git, the worker image's
// /opt/legion/gh-stack-bin/git, runs on every git call gh-stack makes before it runs the
// image's git.
//
// A shape is a list of words: a literal, a literal prefix of an argument gh-stack builds as an
// option with a value appended (`--format=…`), a format gh-stack fills with names
// (`refs/heads/%s:refs/heads/%s`), or a value, an argument gh-stack computes (a branch, a commit,
// a remote, a path, a message), with the optional, repeated and alternative parts gh-stack's code
// builds. A value never starts with `-` and is never `.` or `..`: gh-stack passes names it read
// from its stack files, from GitHub and from the repository as values, so a name that starts with
// `-` would reach git as an option of its own. A name in a format holds no `:` or space either,
// so it cannot add a refspec of its own. Check admits a call only where it fits a shape, so such a
// name, or any argument list gh-stack does not build, never reaches git; Args is what runs.
package ghstackgit

import (
	"fmt"
	"slices"
	"strings"
)

// ShimDir is the image directory that holds cmd/gh-stack-git as `git`, first on the PATH
// `legion gh stack` runs gh-stack with.
const ShimDir = "/opt/legion/gh-stack-bin"

// GitPath is the rest of that PATH, and the PATH cmd/gh-stack-git runs the image's git with, so
// the git it starts, its hooks and helpers among them, never finds cmd/gh-stack-git as `git`.
// A test build of cmd/gh-stack-git sets it, and RealGit, with -ldflags -X.
var GitPath = "/opt/legion/bin:/usr/bin"

// RealGit is the git cmd/gh-stack-git runs: the image's git wrapper, which puts the leak guard's
// pre-push hook before every push.
var RealGit = "/opt/legion/bin/git"

// word is one parsed shape word: a literal, a prefix, a format, a value, literal alternatives, or
// an optional, repeated or alternative group.
type word struct {
	kind    byte
	s       string
	texts   []string
	numbers []byte
	alts    []string
	group   []word
	seqs    [][]word
}

// parsed is shapes, parsed once.
var parsed = func() [][]word {
	out := make([][]word, 0, len(shapes))
	for _, shape := range shapes {
		words, _, closer := parse(shape)
		if closer != "" {
			panic(fmt.Sprintf("ghstackgit: shape %q has an unmatched %q", shape, closer))
		}
		out = append(out, words)
	}
	return out
}()

// parse reads encoded words up to a closing word, and answers the words, what follows the
// closing word, and the closing word ("" at the end).
func parse(encoded []string) ([]word, []string, string) {
	var words []word
	for len(encoded) > 0 {
		w := encoded[0]
		encoded = encoded[1:]
		switch {
		case w == "]" || w == "}" || w == ")" || w == "/":
			return words, encoded, w
		case w == "[" || w == "{":
			group, rest, closer := parse(encoded)
			kind, want := byte('O'), "]"
			if w == "{" {
				kind, want = 'R', "}"
			}
			if closer != want {
				panic(fmt.Sprintf("ghstackgit: a %q group closes with %q", w, closer))
			}
			words, encoded = append(words, word{kind: kind, group: group}), rest
		case w == "(":
			var seqs [][]word
			for {
				seq, rest, closer := parse(encoded)
				seqs, encoded = append(seqs, seq), rest
				if closer == ")" {
					break
				}
				if closer != "/" {
					panic(fmt.Sprintf("ghstackgit: a ( group closes with %q", closer))
				}
			}
			words = append(words, word{kind: 'X', seqs: seqs})
		case w == "$":
			words = append(words, word{kind: 'V'})
		case w[0] == '%':
			texts, numbers := formatParts(w[1:])
			words = append(words, word{kind: 'F', s: w[1:], texts: texts, numbers: numbers})
		case w[0] == '=':
			words = append(words, word{kind: 'L', s: w[1:]})
		case w[0] == '^':
			words = append(words, word{kind: 'P', s: w[1:]})
		case w[0] == '|':
			words = append(words, word{kind: 'A', alts: strings.Split(w[1:], "|")})
		default:
			panic(fmt.Sprintf("ghstackgit: unknown shape word %q", w))
		}
	}
	return words, nil, ""
}

// formatParts reads a format word into the literal text around its pieces and the number of the
// argument each piece is: `+refs/heads/%1:refs/remotes/%2/%1` is the texts `+refs/heads/`, `:`,
// `refs/remotes/`… with pieces 1, 2, 1.
func formatParts(format string) ([]string, []byte) {
	var texts []string
	var numbers []byte
	rest := format
	for {
		i := strings.IndexByte(rest, '%')
		if i < 0 {
			return append(texts, rest), numbers
		}
		if i+1 >= len(rest) || rest[i+1] < '1' || rest[i+1] > '9' {
			panic(fmt.Sprintf("ghstackgit: format %q holds a verb other than %%1 to %%9", format))
		}
		texts, numbers = append(texts, rest[:i]), append(numbers, rest[i+1])
		rest = rest[i+2:]
	}
}

// pieceStops are the bytes no piece holds: `:` and space, which would split or end a refspec; the
// glob and revision bytes `*?[\^~`, which would make one refspec stand for many refs or a ref
// another than the name; and every control byte. A branch name gh-stack takes from its stack or
// from GitHub holds none of them (`git check-ref-format` refuses each).
func pieceStop(b byte) bool {
	return b < 0x20 || b == 0x7f || strings.IndexByte(": *?[\\^~", b) >= 0
}

// piece says whether p may stand for a format's piece: a name or a number gh-stack computes, a
// value (no leading `-`, not `.` or `..`) with no pieceStop byte, no `..` and no `@{`, which git
// reads as a range or a reflog entry rather than a name.
func piece(p string) bool {
	return p != "" && value(p) && !strings.Contains(p, "..") && !strings.Contains(p, "@{") &&
		!strings.ContainsFunc(p, func(r rune) bool { return r < 0x80 && pieceStop(byte(r)) })
}

// fits says whether a is texts filled with pieces, the same number the same piece, trying every
// way to split a, since a branch name holds `/` as the texts around it do. A piece ends at the
// first pieceStop byte, so the splits tried are those before it, and the work is linear in a.
func fits(texts []string, numbers []byte, a string, bound map[byte]string) bool {
	if !strings.HasPrefix(a, texts[0]) {
		return false
	}
	a = a[len(texts[0]):]
	if len(numbers) == 0 {
		return a == ""
	}
	n := numbers[0]
	if p, ok := bound[n]; ok {
		return strings.HasPrefix(a, p) && fits(texts[1:], numbers[1:], a[len(p):], bound)
	}
	// Every piece is a prefix of a[:limit], which holds no pieceStop byte; a prefix holds `..` or
	// `@{` from the first one's end on, and the leading `-` and the whole `.`/`..` read the prefix's
	// own bytes, so each split is checked in constant time.
	limit := len(a)
	if i := strings.IndexFunc(a, func(r rune) bool { return r < 0x80 && pieceStop(byte(r)) }); i >= 0 {
		limit = i
	}
	if i := strings.Index(a[:limit], ".."); i >= 0 {
		limit = min(limit, i+1)
	}
	if i := strings.Index(a[:limit], "@{"); i >= 0 {
		limit = min(limit, i+1)
	}
	if strings.HasPrefix(a, "-") {
		return false
	}
	next := texts[1]
	for end := 1; end <= limit; end++ {
		// The next text must follow the piece, so only an end where it starts is worth trying.
		if !strings.HasPrefix(a[end:], next) {
			continue
		}
		if p := a[:end]; p != "." {
			bound[n] = p
			if fits(texts[1:], numbers[1:], a[end:], bound) {
				return true
			}
			delete(bound, n)
		}
	}
	return false
}

// value says whether a is a value gh-stack may hand git: no leading `-`, which git reads as an
// option, and neither `.` nor `..`, which git reads as this repository, the work tree's root or
// its parent wherever a name stands (`git checkout .` restores every file).
func value(a string) bool {
	return !strings.HasPrefix(a, "-") && a != "." && a != ".."
}

// match says whether args[i:] fits words, then k.
func match(words []word, args []string, i int, k func(int) bool) bool {
	if len(words) == 0 {
		return k(i)
	}
	w, rest := words[0], words[1:]
	next := func(j int) bool { return match(rest, args, j, k) }
	one := func(ok func(string) bool) bool { return i < len(args) && ok(args[i]) && next(i+1) }
	switch w.kind {
	case 'L':
		return one(func(a string) bool { return a == w.s })
	case 'P':
		return one(func(a string) bool { return strings.HasPrefix(a, w.s) })
	case 'F':
		return one(func(a string) bool { return fits(w.texts, w.numbers, a, map[byte]string{}) })
	case 'V':
		return one(value)
	case 'A':
		return one(func(a string) bool { return slices.Contains(w.alts, a) })
	case 'O':
		return match(w.group, args, i, next) || next(i)
	case 'R':
		// Once or more: each loop gh-stack builds an argument list with runs over a list it has
		// checked is not empty, and a git call with none of its refspecs, branches or commits is
		// one gh-stack does not make (`git push origin` alone pushes what push.default says).
		var again func(int) bool
		again = func(j int) bool {
			return match(w.group, args, j, func(after int) bool { return after > j && (next(after) || again(after)) })
		}
		return again(i)
	case 'X':
		return slices.ContainsFunc(w.seqs, func(seq []word) bool { return match(seq, args, i, next) })
	}
	return false
}

// Check answers nil where args is an argument list gh-stack Version starts git with, every
// value of it starting with no `-`; else an error naming the call, and the first argument that
// starts with `-` and is no option gh-stack passes, where there is one.
func Check(args []string) error {
	for _, shape := range parsed {
		if match(shape, args, 0, func(end int) bool { return end == len(args) }) {
			return nil
		}
	}
	call := fmt.Sprintf("`git %s` is no git call gh-stack %s makes, so git does not run", strings.Join(args, " "), Version)
	if i := slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, "-") && !option(a) }); i >= 0 {
		return fmt.Errorf("%s: %q starts with `-` and is no option gh-stack passes to git, and a name gh-stack hands to git cannot start with `-`", call, args[i])
	}
	if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "-") || a == "." || a == ".." }) {
		return fmt.Errorf("%s; a name gh-stack hands to git can neither start with `-`, even where it is an option gh-stack passes elsewhere, nor be `.` or `..`", call)
	}
	return fmt.Errorf("%s", call)
}

// Args is the argument list cmd/gh-stack-git runs the image's git with for args, or the reason it
// runs none (Check). A `git checkout <name>` gets `--` after the name, so git reads the name as a
// branch or a commit and never as a path: a name that is no branch but is a tracked path would
// otherwise restore that path and throw away its uncommitted change. gh-stack checks out only
// branches it has made or fetched, which `checkout <name> --` still switches to, and creates a
// local branch from a remote one as before.
func Args(args []string) ([]string, error) {
	if err := Check(args); err != nil {
		return nil, err
	}
	command := args
	if len(command) >= 2 && command[0] == "-C" {
		command = command[2:]
	}
	if len(command) == 2 && command[0] == "checkout" {
		return append(slices.Clone(args), "--"), nil
	}
	return args, nil
}

// option says whether arg is a literal of some shape, starts with one of its prefixes, or starts
// with the literal start of a format of one that starts with `-`.
func option(arg string) bool {
	var in func(words []word) bool
	in = func(words []word) bool {
		return slices.ContainsFunc(words, func(w word) bool {
			switch w.kind {
			case 'L':
				return w.s == arg
			case 'P':
				return strings.HasPrefix(arg, w.s)
			case 'F':
				start, _, _ := strings.Cut(w.s, "%")
				return strings.HasPrefix(w.s, "-") && strings.HasPrefix(arg, start)
			case 'A':
				return slices.Contains(w.alts, arg)
			case 'O', 'R':
				return in(w.group)
			case 'X':
				return slices.ContainsFunc(w.seqs, in)
			}
			return false
		})
	}
	return slices.ContainsFunc(parsed, in)
}
