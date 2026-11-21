package main

import (
	"fmt"
	"strings"
	"testing"
)

// The step gives the push a revision argument's commits as their ids (jjExplicitIDs) only where
// the word that holds them, with what stands in front of the argument in it, is under jjWordBound
// bytes: a word one byte under the bound holds every id after its prefix, and a word of the
// bound's length, or one byte over it, is no word at all.
func TestTheIDsOfARevisionArgumentStayUnderTheBound(t *testing.T) {
	t.Parallel()
	ids := make([]string, jjWordBound/len(fmt.Sprintf("%040x | ", 0)))
	for i := range ids {
		ids[i] = fmt.Sprintf("%040x", i)
	}
	joined := strings.Join(ids, " | ")
	for _, row := range []struct {
		name string
		size int
		ok   bool
	}{
		{name: "one byte under the bound", size: jjWordBound - 1, ok: true},
		{name: "the bound", size: jjWordBound},
		{name: "one byte over the bound", size: jjWordBound + 1},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			prefix := strings.Repeat("n", row.size-len(joined)-1) + "="
			word, ok := jjExplicitIDs(prefix, ids)
			if ok != row.ok {
				t.Fatalf("jjExplicitIDs of %d ids behind a prefix of %d bytes, %d bytes in all, answered ok %v, want %v", len(ids), len(prefix), row.size, ok, row.ok)
			}
			if want := prefix + joined; ok && word != want || !ok && word != "" {
				t.Errorf("jjExplicitIDs answered a word of %d bytes, ok %v; want the prefix and every id where ok, and no word where not", len(word), ok)
			}
		})
	}
}

// A list of ids that one revset of a read would hold over jjWordBound bytes goes to as many
// revsets as keep each under the bound (jjWords): each starts with the prefix and ends with the
// suffix, and they hold every term once, in order. A term too long for a revset under the bound is
// a revset alone, and a list that fits is one revset.
func TestTheStepsReadsOfAListOfIDsStayUnderTheBound(t *testing.T) {
	t.Parallel()
	const prefix, suffix = "(::(", ") ~ ::remote_bookmarks()) & mutable()"
	terms := make([]string, 3001)
	for i := range terms {
		terms[i] = fmt.Sprintf("commit_id(%040x)", i)
	}
	long := strings.Repeat("x", jjWordBound)
	for _, row := range []struct {
		name  string
		terms []string
		words int
	}{
		{name: "3,001 commit ids", terms: terms, words: 2},
		{name: "one commit id", terms: terms[:1], words: 1},
		{name: "a term over the bound between two commit ids", terms: []string{terms[0], long, terms[1]}, words: 3},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			words := jjWords(prefix, row.terms, suffix)
			if len(words) != row.words {
				t.Errorf("jjWords made %d revsets, want %d", len(words), row.words)
			}
			var got []string
			for _, word := range words {
				if len(word) >= jjWordBound && !strings.Contains(word, long) {
					t.Errorf("jjWords made a revset of %d bytes, want under %d", len(word), jjWordBound)
				}
				inner, ok := strings.CutPrefix(word, prefix)
				if inner, ok = strings.CutSuffix(inner, suffix); !ok {
					t.Fatalf("jjWords made %.80q…, want each revset %q, the terms, then %q", word, prefix, suffix)
				}
				got = append(got, strings.Split(inner, " | ")...)
			}
			if strings.Join(got, "\n") != strings.Join(row.terms, "\n") {
				t.Errorf("jjWords' revsets hold %d terms, want the %d given, each once, in order", len(got), len(row.terms))
			}
		})
	}
}
