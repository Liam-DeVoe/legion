package text

import (
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// headRunesReference is HeadRunes as its comment defines it, with none of its shortcuts: a value
// of at most limit runes is returned unchanged, and a longer one is written out rune by rune up
// to the limit. The decoder reads a byte that is not UTF-8 as a rune of its own, U+FFFD, so it
// is written as U+FFFD, and every other rune is written as its own bytes.
func headRunesReference(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	var head strings.Builder
	for ; limit > 0; limit-- {
		r, size := utf8.DecodeRuneInString(value)
		head.WriteRune(r)
		value = value[size:]
	}
	return head.String()
}

// shapeOf names a value as headRunesReference reads it: whether the limit cuts it, and where it
// holds a real U+FFFD or a byte that is not UTF-8: in the part kept, as the first rune left out,
// or further on.
func shapeOf(value string, limit int) string {
	if value == "" {
		return "an empty value"
	}
	shape := "a value at or under the limit"
	if utf8.RuneCountInString(value) > limit {
		shape = "a cut"
	}
	places := [...]string{"kept", "right after the cut", "further on"}
	kinds := [...]string{"a real U+FFFD", "a byte that is not UTF-8"}
	var held [len(places)][len(kinds)]bool
	for n := 0; value != ""; n++ {
		r, size := utf8.DecodeRuneInString(value)
		value = value[size:]
		if r != utf8.RuneError {
			continue
		}
		place, kind := 0, 0
		if n == limit {
			place = 1
		} else if n > limit {
			place = 2
		}
		if size == 1 {
			kind = 1
		}
		held[place][kind] = true
	}
	var holds []string
	for place := range places {
		for kind := range kinds {
			if held[place][kind] {
				holds = append(holds, kinds[kind]+" "+places[place])
			}
		}
	}
	if len(holds) == 0 {
		return shape + " with no U+FFFD and no byte that is not UTF-8"
	}
	return shape + " with " + strings.Join(holds, ", ")
}

// headRunesAlphabet holds each byte that decides a cut: a letter; EF BF BD, the three bytes of
// U+FFFD, which alone or as a pair are bytes that are not UTF-8; FF, which is never UTF-8;
// E7 95 8C, the three bytes of 界; F0 9F, which with 95 8C are the four bytes of U+1F54C, a
// character outside the BMP; and C3 A9, the two bytes of é, each of which alone is not UTF-8.
const headRunesAlphabet = "a\xef\xbf\xbd\xff\xe7\x95\x8c\xf0\x9f\xc3\xa9"

// headRunesCharacters are the five characters that the alphabet's comment names: a, é, 界,
// U+FFFD and U+1F54C. The twelve bytes make 259 characters of one to four bytes; these five are
// the ones a seeded value is built of, with single bytes of the alphabet, so it holds runs of
// whole characters as well as bytes that are not UTF-8.
var headRunesCharacters = []string{"a", "é", "界", "\uFFFD", "\U0001F54C"}

// sink keeps each answer of an allocation row, so an answer that the cut copied lives on the heap
// and is counted.
var sink string

// pair is a value and the limit it is cut to.
type pair struct {
	value string
	limit int
}

// copiesNothing fails t when HeadRunes makes an allocation for any of pairs, each of which has an
// answer that is, by the reference, a slice of its value. It counts all of them at once, then one
// at a time to name the pair that copied.
func copiesNothing(t *testing.T, pairs []pair) {
	t.Helper()
	if testing.AllocsPerRun(10, func() {
		for _, p := range pairs {
			sink = HeadRunes(p.value, p.limit)
		}
	}) == 0 {
		return
	}
	for _, p := range pairs {
		if allocations := testing.AllocsPerRun(10, func() { sink = HeadRunes(p.value, p.limit) }); allocations != 0 {
			t.Fatalf("HeadRunes(%.70q, %d) made %v allocations, want a slice of the value and none (the value is %d bytes)", p.value, p.limit, allocations, len(p.value))
		}
	}
	t.Fatalf("the %d pairs made allocations together and none alone", len(pairs))
}

// HeadRunes counts a rune of any script as one, and a document edit's read-back cuts its last
// block with it. The first row compares it with headRunesReference on every value of 0 to 5 bytes
// of the alphabet, to each limit from 0 to 6: every pair, no sample. Five bytes is the shortest
// value on which a guard that reads the start of the value, not the place of the rune, changes
// what HeadRunes does: it copies the cut of a letter, U+FFFD and a letter, and it keeps as it is a
// byte that is not UTF-8 after a U+FFFD that starts the value.
//
// The second row compares the two on seeded values of 6 to 64 bytes, to each limit from 0 to 70.
// The seed is fixed, so a failure repeats. The third puts a byte that is not UTF-8 at each place
// of a value of 2,001 characters, and cuts the value at each limit from one character before that
// place to two after it, and at 2,000. A guard that reads only the first N runes or the first N
// bytes of the value fails the third row for each N under 2,000; the second row fails the first
// guard only for N up to 24, and the second for N up to 61. Both rows also want no allocation for
// each of their pairs whose answer is, by the reference, the value's own first bytes, a value
// returned unchanged among them, as the shape rows below want for the pairs of the first row.
//
// The fourth row cuts before each code point but the surrogates, after each of seven characters:
// a letter, the same code point, and five that a rule of grapheme clusters joins with what
// follows (an emoji that takes a skin-tone modifier, that emoji with a zero-width joiner, a
// regional indicator, a Hangul leading consonant, and a Devanagari consonant with its virama). So
// a count that keeps a code point with one of these seven before it fails the row, whatever the
// code point and its length: a character is one code point, not a grapheme cluster.
//
// An answer that is, by the reference, the value's own first bytes is a slice of the value, and
// HeadRunes copies nothing for it. One row for each shape of such a pair (shapeOf) cuts every
// pair of that shape and wants no allocation, so a copy of any one pair fails its row. The last
// rows hold values longer than five bytes, each cut to a slice with no copy. Among them, a cut
// after characters outside the BMP fails a count of UTF-16 units, and a cut before a combining
// mark fails a count that keeps a mark with its letter.
func TestHeadRunesCutsAtARuneAndCopiesNothing(t *testing.T) {
	const longest, limits = 5, 6
	var values []string
	for length := 0; length <= longest; length++ {
		count := 1
		for range length {
			count *= len(headRunesAlphabet)
		}
		for code := range count {
			value := make([]byte, length)
			for i, digits := 0, code; i < length; i, digits = i+1, digits/len(headRunesAlphabet) {
				value[i] = headRunesAlphabet[digits%len(headRunesAlphabet)]
			}
			values = append(values, string(value))
		}
	}

	t.Run("every pair agrees with the reference", func(t *testing.T) {
		differ := 0
		for _, value := range values {
			for limit := 0; limit <= limits; limit++ {
				got, want := HeadRunes(value, limit), headRunesReference(value, limit)
				if got == want {
					continue
				}
				if differ < 10 {
					t.Errorf("HeadRunes(%q, %d) = %q, want %q", value, limit, got, want)
				}
				differ++
			}
		}
		if differ > 0 {
			t.Errorf("%d of %d pairs differ from the reference", differ, len(values)*(limits+1))
		}
	})

	t.Run("every seeded value of 6 to 64 bytes agrees with the reference, and copies nothing where its answer is a slice", func(t *testing.T) {
		const seeded, longLimits = 20000, 70
		for _, character := range headRunesCharacters {
			for i := range len(character) {
				if strings.IndexByte(headRunesAlphabet, character[i]) < 0 {
					t.Fatalf("%q holds the byte %#x, which the alphabet does not", character, character[i])
				}
			}
		}
		random := rand.New(rand.NewPCG(1, 6))
		differ := 0
		var sliced []pair
		for range seeded {
			length := 6 + random.IntN(59)
			built := make([]byte, 0, length+3)
			for len(built) < length {
				if random.IntN(4) == 0 {
					built = append(built, headRunesAlphabet[random.IntN(len(headRunesAlphabet))])
				} else {
					built = append(built, headRunesCharacters[random.IntN(len(headRunesCharacters))]...)
				}
			}
			value := string(built[:length])
			for limit := 0; limit <= longLimits; limit++ {
				got, want := HeadRunes(value, limit), headRunesReference(value, limit)
				if strings.HasPrefix(value, want) {
					sliced = append(sliced, pair{value, limit})
				}
				if got == want {
					continue
				}
				if differ < 10 {
					t.Errorf("HeadRunes(%q, %d) = %q, want %q", value, limit, got, want)
				}
				differ++
			}
		}
		if differ > 0 {
			t.Errorf("%d of %d seeded pairs differ from the reference", differ, seeded*(longLimits+1))
		}
		copiesNothing(t, sliced)
	})

	t.Run("a byte that is not UTF-8 at each place of 2,001 characters agrees with the reference, and copies nothing where its answer is a slice", func(t *testing.T) {
		const characters = 2001
		differ, cuts := 0, 0
		var sliced []pair
		for place := range characters {
			value := strings.Repeat("a", place) + "\xff" + strings.Repeat("a", characters-1-place)
			around := []int{place - 1, place, place + 1, place + 2, characters - 1}
			for i, limit := range around {
				if limit < 0 || slices.Contains(around[:i], limit) {
					continue
				}
				cuts++
				got, want := HeadRunes(value, limit), headRunesReference(value, limit)
				if strings.HasPrefix(value, want) {
					sliced = append(sliced, pair{value, limit})
				}
				if got == want {
					continue
				}
				if differ < 10 {
					from := min(max(0, place-5), len(got), len(want))
					t.Errorf("a byte that is not UTF-8 at character %d, cut to %d: HeadRunes kept %d bytes, %.20q... from byte %d, want %d bytes, %.20q...", place, limit, len(got), got[from:], from, len(want), want[from:])
				}
				differ++
			}
		}
		if differ > 0 {
			t.Errorf("%d of %d cuts differ from the reference", differ, cuts)
		}
		copiesNothing(t, sliced)
	})

	t.Run("a cut before each code point leaves it out, after each of seven characters", func(t *testing.T) {
		// Six of the seven characters; the seventh is the code point itself, which the first cut
		// of each code point puts before it.
		befores := []string{"a", "\U0001F44D", "\U0001F44D\u200D", "\U0001F1E6", "\u1100", "\u0915\u094D"}
		differ, cuts := 0, 0
		cut := func(value string, limit int) {
			cuts++
			if got, want := HeadRunes(value, limit), headRunesReference(value, limit); got != want {
				if differ < 10 {
					t.Errorf("HeadRunes(%+q, %d) = %+q, want %+q", value, limit, got, want)
				}
				differ++
			}
		}
		for r := rune(0); r <= utf8.MaxRune; r++ {
			if !utf8.ValidRune(r) {
				continue
			}
			character := string(r)
			cut(character+character+character, 2)
			for _, before := range befores {
				cut(before+character+"a", utf8.RuneCountInString(before))
			}
		}
		if differ > 0 {
			t.Errorf("%d of %d cuts before a code point differ from the reference", differ, cuts)
		}
	})

	shapes := map[string][]pair{}
	for _, value := range values {
		for limit := 0; limit <= limits; limit++ {
			if strings.HasPrefix(value, headRunesReference(value, limit)) {
				shape := shapeOf(value, limit)
				shapes[shape] = append(shapes[shape], pair{value, limit})
			}
		}
	}
	for _, shape := range slices.Sorted(maps.Keys(shapes)) {
		pairs := shapes[shape]
		t.Run(shape+" copies nothing", func(t *testing.T) { copiesNothing(t, pairs) })
	}

	for _, row := range []struct {
		name, value string
		limit       int
		want        string
	}{
		{name: "characters outside the BMP are cut at the limit", value: strings.Repeat("𠀀", 10), limit: 4, want: strings.Repeat("𠀀", 4)},
		{name: "2,001 Chinese characters are cut at 1,967", value: strings.Repeat("界", 2001), limit: 1967, want: strings.Repeat("界", 1967)},
		{name: "100 pairs of a letter and U+FFFD are cut at 50", value: strings.Repeat("a\uFFFD", 100), limit: 50, want: strings.Repeat("a\uFFFD", 25)},
		{name: "a byte that is not UTF-8 past the cut is left out", value: strings.Repeat("界", 10) + "\xff", limit: 5, want: strings.Repeat("界", 5)},
		{name: "a combining mark after the cut is left out", value: strings.Repeat("a\u0301", 10), limit: 3, want: "a\u0301a"},
	} {
		t.Run(row.name, func(t *testing.T) {
			allocations := testing.AllocsPerRun(100, func() { sink = HeadRunes(row.value, row.limit) })
			if sink != row.want {
				t.Fatalf("HeadRunes to %d kept %d bytes, %.40q, want %d bytes, %.40q", row.limit, len(sink), sink, len(row.want), row.want)
			}
			if allocations != 0 {
				t.Fatalf("HeadRunes to %d made %v allocations, want a slice of the value and none", row.limit, allocations)
			}
		})
	}
}
