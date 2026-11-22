// Package ghstackgit is what the git gh-stack runs under `legion gh stack` admits: the argument
// lists the pinned gh-stack release starts git with (shapes, generated from its source by
// generate.sh), and Check, which cmd/gh-stack-git, the worker image's
// /opt/legion/gh-stack-bin/git, runs on every git call gh-stack makes before it runs the
// image's git.
//
// A shape is a list of words: a literal, a literal prefix of an argument gh-stack builds as an
// option with a value appended (`--format=…`), or a value, an argument gh-stack computes (a
// branch, a commit, a remote, a path, a message), with the optional, repeated and alternative
// parts gh-stack's code builds. A value never starts with `-`: gh-stack passes names it read from
// its stack files, from GitHub and from the repository as values, so a name that starts with `-`
// would reach git as an option of its own. Check admits a call only where it fits a shape, so
// such a name, or any argument list gh-stack does not build, never reaches git.
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

// word is one parsed shape word: a literal, a prefix, a value, literal alternatives, or an
// optional, repeated or alternative group.
type word struct {
	kind  byte
	s     string
	alts  []string
	group []word
	seqs  [][]word
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
	case 'V':
		return one(func(a string) bool { return !strings.HasPrefix(a, "-") })
	case 'A':
		return one(func(a string) bool { return slices.Contains(w.alts, a) })
	case 'O':
		return match(w.group, args, i, next) || next(i)
	case 'R':
		var again func(int) bool
		again = func(j int) bool {
			return next(j) || match(w.group, args, j, func(after int) bool { return after > j && again(after) })
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
	return fmt.Errorf("%s; a name gh-stack hands to git cannot start with `-`, even where it is an option gh-stack passes elsewhere", call)
}

// option says whether arg is a literal of some shape, or starts with one of its prefixes.
func option(arg string) bool {
	var in func(words []word) bool
	in = func(words []word) bool {
		return slices.ContainsFunc(words, func(w word) bool {
			switch w.kind {
			case 'L':
				return w.s == arg
			case 'P':
				return strings.HasPrefix(arg, w.s)
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
