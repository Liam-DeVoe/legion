package text

import (
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
	"unsafe"
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

// sink keeps each answer of a copy row, so an answer that the cut copied lives on the heap and is
// counted.
var sink string

// pair is a value, the limit it is cut to, and want, the number of bytes of the value that the cut
// keeps by the reference, for a pair whose answer is, by the reference, the value's own first bytes.
type pair struct {
	value       string
	limit, want int
}

// isSlice reports whether got is the value's own first want bytes: want bytes long and, unless it
// is empty, at the value's address. A copy has another address, whatever call made it.
func isSlice(got, value string, want int) bool {
	return len(got) == want && (want == 0 || unsafe.StringData(got) == unsafe.StringData(value))
}

// answerScan cuts each of pairs once in each of its runs, keeps each answer in sink, and counts the
// answers that are not the value's own first want bytes, which is, by the reference, each pair's
// answer, keeping the first of them. The two copy helpers below run it on their own schedules and
// report through it, so a bad answer is read and named in one place.
type answerScan struct {
	pairs                                      []pair
	runs, copies, first, firstRun, firstLength int
	firstAt                                    string
}

func (s *answerScan) run() {
	s.runs++
	for i, p := range s.pairs {
		got := HeadRunes(p.value, p.limit)
		if !isSlice(got, p.value, p.want) {
			if s.copies == 0 {
				s.first, s.firstRun, s.firstLength, s.firstAt = i, s.runs, len(got), "another address"
				if len(got) > 0 && unsafe.StringData(got) == unsafe.StringData(p.value) {
					s.firstAt = "the value's address"
				}
			}
			s.copies++
		}
		sink = got
	}
}

// check fails t when any answer of any run was not the value's own first bytes, naming the first.
func (s *answerScan) check(t *testing.T) {
	t.Helper()
	if s.copies > 0 {
		p := s.pairs[s.first]
		t.Fatalf("%d of the %d answers are not the value's own first bytes; the first, in run %d of %d: HeadRunes(%.70q, %d) answered %d bytes at %s, want the value's own first %d bytes (the value is %d bytes)", s.copies, s.runs*len(s.pairs), s.firstRun, s.runs, p.value, p.limit, s.firstLength, s.firstAt, p.want, len(p.value))
	}
}

// copiesNothing fails t when HeadRunes answers any of pairs with anything but the value's own first
// want bytes, which is, by the reference, each pair's answer. testing.AllocsPerRun cuts all the
// pairs 22 times, the first run uncounted, and each answer of each run is checked: so a copy fails
// on whichever of a pair's first 22 calls makes it, on each call, on the first only, the second
// only, each eleventh, or kept and returned on a later call. A row that cuts a pair before it calls
// copiesNothing checks that answer too. The count of the 21 counted runs adds what an address
// cannot show, an allocation that HeadRunes makes and does not return (a []rune of the value, say):
// it fails the row when the pairs make 21 or more of them over the 21 runs, since the mean, a whole
// number, leaves out fewer than 21 allocations of something else. Fewer such allocations, and a
// copy made only on a pair's 23rd call or later, can pass. It then counts the pairs one at a time
// to name the pair that allocated.
func copiesNothing(t *testing.T, pairs []pair) {
	t.Helper()
	scan := answerScan{pairs: pairs}
	allocations := testing.AllocsPerRun(21, scan.run)
	scan.check(t)
	if allocations == 0 {
		return
	}
	for _, p := range pairs {
		if allocations := testing.AllocsPerRun(21, func() { sink = HeadRunes(p.value, p.limit) }); allocations != 0 {
			t.Fatalf("HeadRunes(%.70q, %d) made %v allocations on each call, though each answer is the value's own first bytes: it allocates what it does not return (the value is %d bytes)", p.value, p.limit, allocations, len(p.value))
		}
	}
	t.Fatalf("the %d pairs made %v allocations in each run together and none alone, though each answer is the value's own first bytes", len(pairs), allocations)
}

// answersSlices fails t when HeadRunes answers any of pairs, on one call of each, with anything but
// the value's own first want bytes, which is, by the reference, each pair's answer. That one call
// catches a copy keyed on what the value holds, on each call or on the first. A copy keyed on how
// many times a pair was cut is copiesNothing's to catch, over fewer pairs, and so is an allocation
// that HeadRunes makes and does not return.
func answersSlices(t *testing.T, pairs []pair) {
	t.Helper()
	scan := answerScan{pairs: pairs}
	scan.run()
	scan.check(t)
}

// keptCuts cuts values that are UTF-8, each kept+rest, to the code points of kept, and counts the
// cuts that keep something else: by the definition, that cut of a value that is UTF-8 is kept.
type keptCuts struct {
	t            *testing.T
	cuts, differ int
}

func (c *keptCuts) cut(kept, rest string) {
	c.cuts++
	value, limit := kept+rest, utf8.RuneCountInString(kept)
	if got := HeadRunes(value, limit); got != kept {
		if c.differ < 10 {
			c.t.Errorf("HeadRunes(%+q, %d) = %+q, want %+q", value, limit, got, kept)
		}
		c.differ++
	}
}

// check fails the row when a cut kept something else, or when the row made no cut at all.
func (c *keptCuts) check(where string) {
	if c.cuts == 0 {
		c.t.Fatalf("no cut %s was made", where)
	}
	if c.differ > 0 {
		c.t.Errorf("%d of %d cuts %s differ from the characters before it", c.differ, c.cuts, where)
	}
}

// spanRunes lists the code points of each range of bounds, given as its first and its last.
func spanRunes(bounds ...rune) []rune {
	var runes []rune
	for i := 0; i < len(bounds); i += 2 {
		for r := bounds[i]; r <= bounds[i+1]; r++ {
			runes = append(runes, r)
		}
	}
	return runes
}

// tableRunes lists the code points of a table of Go's unicode package, in order.
func tableRunes(table *unicode.RangeTable) []rune {
	var runes []rune
	for _, r := range table.R16 {
		for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) {
			runes = append(runes, c)
		}
	}
	for _, r := range table.R32 {
		for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) {
			runes = append(runes, c)
		}
	}
	return runes
}

// HeadRunes counts a rune of any script as one, and a document edit's read-back cuts its last
// block with it. The first row compares it with headRunesReference on every value of 0 to 5 bytes
// of the alphabet, to each limit from 0 to 6: every pair, no sample. Five bytes is the shortest
// value on which a guard that reads the start of the value, not the place of the rune, changes
// what HeadRunes does: it copies the cut of a letter, U+FFFD and a letter, and it keeps as it is a
// byte that is not UTF-8 after a U+FFFD that starts the value.
//
// The second row compares the two on seeded values of 6 to 64 bytes, to each limit from 0 to 70.
// The seed is fixed, so a failure repeats. The third puts each of six kinds of character at each
// place of a value of 2,001 characters (a byte that is not UTF-8, U+FFFD, é, 界, U+1F54C and a
// combining mark), and cuts the value at each limit from one character before that place to two
// after it, and at 2,000. A guard that reads only the first N runes or the first N bytes of the
// value fails the third row for each N under 2,000; the second row fails the first guard only for
// N up to 24, and the second for N up to 61. Both rows also want each of their pairs whose answer
// is, by the reference, the value's own first bytes, a value returned unchanged among them, to
// answer that slice of the value itself, as the first row and the shape rows below want for the
// pairs of the first row.
//
// The fourth row cuts before each code point but the surrogates, after the same code point and
// after each of 62 strings of one or two code points, and wants the cut to leave the code point
// out. The strings are a letter, an emoji that takes a skin-tone modifier, and the left side of
// each rule of Unicode Standard Annex #29 that keeps two code points in one grapheme cluster: CR
// (GB3); the first Hangul L, V, T, LV and LVT, and the first Kirat Rai V (GB6 to GB8); each of the
// 27 Prepends (GB9b), since GB9b keeps any code point but a control after a Prepend; each of the
// 23 linkers, after the first consonant of its script or, for the two Vedic signs, whose script
// Common has no consonant, after Devanagari's (GB9c); two pictographs, each with a zero-width
// joiner (GB11); and the first regional indicator (GB12 and GB13). The fifth row cuts between the
// two code points of each pair that GB6 to GB8, GB12 and GB13 keep together, every one of them;
// before each letter of the script of a linker's consonant, after each letter of that script and
// the linker, and after the consonant, the linker and each code point the sixth row cuts before,
// since GB9c, as Unicode 18.0 writes it, keeps a consonant after a linker and any InCB=Extend, and
// those code points hold each InCB=Extend of Go's tables; and before the first pictograph, after
// each code point and a joiner. A linker that makes no cut fails the fifth row, so tables with no
// script for its consonant cannot make the row hold less unseen. The sixth cuts before each
// combining mark and each of 107 more code points, which GB9 and GB9a keep after any other but
// for 31 spacing marks of Myanmar, Tai Tham and Ahom, after each letter of its own script, and
// after each letter among its 128 code points (those that share all but its last seven bits); it
// cuts each of these of the script Inherited, which takes the script of the letter before it,
// after the first letter of each script, each mark of SignWriting, which has no letter, after each
// SignWriting base, and each emoji modifier after each emoji modifier base. So a count that keeps
// a code point with one of these before it fails a row, whatever its length: a character is one
// code point, not a grapheme cluster. A count keyed on code points that no row cuts together can
// pass, such as a mark after a code point that is no letter of its script and not among its 128
// code points (U+0301 after e, or a tag after U+1F3F4), an Inherited mark after a letter that is
// not the first of its script, a consonant that GB9c keeps after a linker in any case but the three
// the rows cut (each letter of the script of the linker's consonant after each letter of that
// script and the linker, or after the consonant, the linker and one code point of joined, and any
// code point after the consonant and the linker), so one of another script, one after a code point
// before the linker that is no letter of that script (U+093C, the nukta), one after a letter other
// than the consonant, the linker and an InCB=Extend, and one after two InCB=Extend, or a pictograph
// after another and a joiner, where the first is neither U+00A9 nor U+1F44D and the second is not
// U+00A9.
//
// The code points that a rule names are written here from the data of Unicode 18.0, since Go's
// unicode package gives no grapheme property. The letters, the marks and their scripts are read
// from Go's own tables (unicode.Version, 17.0.0 with the module's toolchain), since they are too
// many to write here: a letter or a mark that is new in 18.0 is neither there, and only the fourth
// row cuts before it.
//
// An answer that is, by the reference, the value's own first bytes is a slice of the value, and
// HeadRunes copies nothing for it. A copy has another address, so each row that holds such pairs
// wants each answer at the value's address and of the reference's length. The rows that call
// copiesNothing cut each pair 22 times, so a copy fails on whichever of a pair's first 22 calls
// makes it, and count the allocations of those calls; one of them for each shape of such a pair
// (shapeOf) cuts every pair of that shape. Two rows cut values at each length from 0 to 8,001
// bytes, at every 32nd from 8,033 to 32,001 and at every 199th from 32,001 to 128,001, to their
// own length and to one character short, since an embedding input of any script reaches each of
// those lengths: one of letters and, for each of é, 界 and U+1F54C, characters of two, three and
// four bytes, values of letters and one run of the character or the character once (lengthPairs
// lists them): a run first or last with up to 66 letters or half the length on its other side, a
// run between one letter and up to four or between two quarters of the length, two of the
// character between two halves, the run alone, the character once first, last, in the middle, or
// with up to 63 letters on one side and the rest on the other, and the character once and a run
// first or last at one more place for each length, which moves along the value as the lengths
// run. They cut each pair once (answersSlices), which catches a copy keyed on what the value
// holds; a third cuts the values of each kind they hold 22 times, at each edge of the ranges of
// lengths the rows hold and one byte each side of it, which catches a copy keyed on how many times
// a pair was cut. A copy keyed on a value with two runs of wider characters or more, such as one
// at each end, or with characters of two widths besides letters, can pass, and so can a copy keyed
// on a run between two stretches of letters (say, ten letters, a run of three, then 5,000
// letters), or on a count of letters beside one run or the character once at lengths where the
// rows do not put it (say, the character once after exactly 100 letters, in values of 5,000 bytes
// only), and a copy keyed on how many times a pair was cut or an allocation HeadRunes does not
// return, each keyed too on lengths inside a range, away from its edges (from 69 to 1,999 bytes,
// say). The last rows hold values longer than five bytes, each cut to a slice with no copy, up to
// 32,000 characters, the most an embedding input keeps. Among them, a cut after characters outside
// the BMP fails a count of UTF-16 units, and a cut before a combining mark fails a count that keeps
// a mark with its letter.
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

	t.Run("every pair agrees with the reference, and is a slice of its value where the reference is one", func(t *testing.T) {
		differ := 0
		for _, value := range values {
			for limit := 0; limit <= limits; limit++ {
				got, want := HeadRunes(value, limit), headRunesReference(value, limit)
				if got == want && (!strings.HasPrefix(value, want) || isSlice(got, value, len(want))) {
					continue
				}
				if differ < 10 && got == want {
					t.Errorf("HeadRunes(%q, %d) = %q at another address, want the value's own first %d bytes", value, limit, got, len(want))
				} else if differ < 10 {
					t.Errorf("HeadRunes(%q, %d) = %q, want %q", value, limit, got, want)
				}
				differ++
			}
		}
		if differ > 0 {
			t.Errorf("%d of %d pairs differ from the reference, or are a copy where it is a slice", differ, len(values)*(limits+1))
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
				slice := strings.HasPrefix(value, want)
				if slice {
					sliced = append(sliced, pair{value, limit, len(want)})
				}
				if got == want && (!slice || isSlice(got, value, len(want))) {
					continue
				}
				if differ < 10 && got == want {
					t.Errorf("HeadRunes(%q, %d) = %q at another address, want the value's own first %d bytes", value, limit, got, len(want))
				} else if differ < 10 {
					t.Errorf("HeadRunes(%q, %d) = %q, want %q", value, limit, got, want)
				}
				differ++
			}
		}
		if differ > 0 {
			t.Errorf("%d of %d seeded pairs differ from the reference, or are a copy where it is a slice", differ, seeded*(longLimits+1))
		}
		copiesNothing(t, sliced)
	})

	t.Run("each of six kinds of character at each place of 2,001 characters agrees with the reference, and copies nothing where its answer is a slice", func(t *testing.T) {
		const characters = 2001
		differ, cuts := 0, 0
		var sliced []pair
		for _, kind := range []string{"\xff", "\uFFFD", "é", "界", "\U0001F54C", "\u0301"} {
			for place := range characters {
				value := strings.Repeat("a", place) + kind + strings.Repeat("a", characters-1-place)
				around := []int{place - 1, place, place + 1, place + 2, characters - 1}
				for i, limit := range around {
					if limit < 0 || slices.Contains(around[:i], limit) {
						continue
					}
					cuts++
					got, want := HeadRunes(value, limit), headRunesReference(value, limit)
					slice := strings.HasPrefix(value, want)
					if slice {
						sliced = append(sliced, pair{value, limit, len(want)})
					}
					if got == want && (!slice || isSlice(got, value, len(want))) {
						continue
					}
					if differ < 10 && got == want {
						t.Errorf("%+q at character %d, cut to %d: HeadRunes kept the right %d bytes at another address, want the value's own first bytes", kind, place, limit, len(got))
					} else if differ < 10 {
						from := min(max(0, place-5), len(got), len(want))
						t.Errorf("%+q at character %d, cut to %d: HeadRunes kept %d bytes, %.20q... from byte %d, want %d bytes, %.20q...", kind, place, limit, len(got), got[from:], from, len(want), want[from:])
					}
					differ++
				}
			}
		}
		if differ > 0 {
			t.Errorf("%d of %d cuts differ from the reference, or are a copy where it is a slice", differ, cuts)
		}
		copiesNothing(t, sliced)
	})

	// linkers are the 23 linkers of Unicode 18.0 (GB9c), each after the first consonant of its
	// script, Devanagari's for the two Vedic signs of Common.
	linkers := []string{
		// Devanagari, Bengali, Gujarati, Oriya, Telugu, Malayalam, Myanmar, Khmer, Tai Tham,
		// Balinese, Sundanese, Common (two Vedic signs), Javanese, Meetei Mayek, Kharoshthi, Chakma,
		// Tulu-Tigalari, Dives Akuru, Zanabazar Square (two), Soyombo and Kawi.
		"\u0915\u094D", "\u0995\u09CD", "\u0A95\u0ACD", "\u0B15\u0B4D", "\u0C15\u0C4D", "\u0D15\u0D4D", "\u1000\u1039",
		"\u1780\u17D2", "\u1A20\u1A60", "\u1B0B\u1B44", "\u1B83\u1BAB", "\u0915\u1CF5", "\u0915\u1CF6", "\uA989\uA9C0",
		"\uAAE0\uAAF6", "\U00010A00\U00010A3F", "\U00011103\U00011133", "\U00011380\U000113D0", "\U00011900\U0001193E",
		"\U00011A00\U00011A3A", "\U00011A00\U00011A47", "\U00011A50\U00011A99", "\U00011F04\U00011F42",
	}
	// letters holds the letters of each script of Go's tables, under the script's name.
	letters := map[string][]rune{}
	for name, table := range unicode.Scripts {
		for _, r := range tableRunes(table) {
			if unicode.IsLetter(r) {
				letters[name] = append(letters[name], r)
			}
		}
	}
	// joins holds each combining mark of Go's tables and, of Unicode 18.0
	// (GraphemeBreakProperty.txt), these 107 code points that are no mark in Go's tables: the
	// spacing marks of Thai and Lao that are letters, the two joiners, the halfwidth sound marks,
	// the emoji modifiers and the tags. GB9 and GB9a keep each of them after any code point but 31
	// spacing marks of Myanmar, Tai Tham and Ahom, whose Grapheme_Cluster_Break in 18.0 is Other;
	// a cut before those leaves them out too, as before any code point. joined lists them in order.
	others := map[rune]bool{}
	for _, r := range spanRunes(0x0E33, 0x0E33, 0x0EB3, 0x0EB3, 0x200C, 0x200D, 0xFF9E, 0xFF9F, 0x1F3FB, 0x1F3FF, 0xE0020, 0xE007F) {
		others[r] = true
	}
	// joined below holds the marks of Go's tables too, so its check alone cannot see others empty or
	// short; the fifth and sixth rows would then cut before fewer of the 107 and still pass.
	if len(others) != 107 {
		t.Fatalf("others holds %d code points, not the 107 listed above, so the fifth and sixth rows would cut before fewer of them", len(others))
	}
	joins := func(r rune) bool { return unicode.Is(unicode.M, r) || others[r] }
	var joined []rune
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if joins(r) {
			joined = append(joined, r)
		}
	}
	// The fifth and sixth rows cut before the code points of joined; with none, both would hold no
	// mark and still pass, since each of them makes other cuts too.
	if len(joined) == 0 {
		t.Fatal("joins holds no code point, so the fifth and sixth rows would cut before no mark")
	}

	t.Run("a cut before each code point leaves it out, after the code point itself and after each of 62 strings of one or two code points", func(t *testing.T) {
		befores := append([]string{
			// Any character (GB9, GB9a), and an emoji that takes a skin-tone modifier.
			"a", "\U0001F44D",
			// GB3: CR. GB6 to GB8: the first L, V, LV, T and LVT of Hangul, and the first V of Kirat Rai.
			"\r", "\u1100", "\u1160", "\U00016D63", "\uAC00", "\u11A8", "\uAC01",
			// GB9b: each Prepend, of Arabic, Common, Syriac, Malayalam, Kaithi, Sharada, Tulu-Tigalari,
			// Dives Akuru, Soyombo, Masaram Gondi and Kawi.
			"\u0600", "\u0601", "\u0602", "\u0603", "\u0604", "\u0605", "\u06DD", "\u070F", "\u0890", "\u0891", "\u08E2",
			"\u0D4E", "\U000110BD", "\U000110CD", "\U000111C2", "\U000111C3", "\U000113D1", "\U0001193F", "\U00011941",
			"\U00011A84", "\U00011A85", "\U00011A86", "\U00011A87", "\U00011A88", "\U00011A89", "\U00011D46", "\U00011F02",
			// GB11: a pictograph and a zero-width joiner. GB12 and GB13: a regional indicator.
			"\u00A9\u200D", "\U0001F44D\u200D", "\U0001F1E6",
		}, linkers...)
		cuts := keptCuts{t: t}
		for r := rune(0); r <= utf8.MaxRune; r++ {
			if !utf8.ValidRune(r) {
				continue
			}
			character := string(r)
			rest := character + "a"
			cuts.cut(character+character, character)
			for _, before := range befores {
				cuts.cut(before, rest)
			}
		}
		cuts.check("before a code point")
	})

	t.Run("a cut before a code point that a grapheme rule joins to each code point of its left side leaves it out", func(t *testing.T) {
		// GB6 to GB8: the Hangul L, V and T of Unicode 18.0 (GraphemeBreakProperty.txt), with the
		// Kirat Rai V among the V, and the syllables U+AC00 to U+D7A3, of which LV are those whose
		// distance from U+AC00 is a multiple of 28, and LVT the rest. GB12 and GB13: the regional
		// indicators, RI.
		leading := spanRunes(0x1100, 0x115F, 0xA960, 0xA97C)
		vowels := spanRunes(0x1160, 0x11A7, 0xD7B0, 0xD7C6, 0x16D63, 0x16D63, 0x16D67, 0x16D6A)
		trailing := spanRunes(0x11A8, 0x11FF, 0xD7CB, 0xD7FB)
		regional := spanRunes(0x1F1E6, 0x1F1FF)
		var lv, lvt []rune
		for syllable := rune(0xAC00); syllable <= 0xD7A3; syllable++ {
			if (syllable-0xAC00)%28 == 0 {
				lv = append(lv, syllable)
			} else {
				lvt = append(lvt, syllable)
			}
		}
		// Each rule names the sets of its left side and of its right, and cuts each code point of
		// each right set after each of each left set. A pair of sets that makes no cut, as when one
		// of them holds no code point, fails the row by name, as a linker does below: the other
		// pairs' cuts would still pass it.
		sets := map[string][]rune{"L": leading, "V": vowels, "T": trailing, "LV": lv, "LVT": lvt, "RI": regional}
		cuts := keptCuts{t: t}
		for _, rule := range []struct{ left, right []string }{
			{[]string{"L"}, []string{"L", "V", "LV", "LVT"}},
			{[]string{"LV", "V"}, []string{"V", "T"}},
			{[]string{"LVT", "T"}, []string{"T"}},
			{[]string{"RI"}, []string{"RI"}},
		} {
			for _, leftSet := range rule.left {
				for _, rightSet := range rule.right {
					made := cuts.cuts
					for _, left := range sets[leftSet] {
						for _, right := range sets[rightSet] {
							cuts.cut(string(left), string(right)+"a")
						}
					}
					if cuts.cuts == made {
						t.Errorf("no cut was made before a code point of %s after one of %s: one of the two holds no code point", rightSet, leftSet)
					}
				}
			}
		}
		// GB9c, as Unicode 18.0 writes it, keeps a consonant after a linker and any InCB=Extend:
		// each letter of the script of a linker's consonant, the linker, then each letter of that
		// script, which holds each consonant of the script that Go's tables call a letter; and the
		// consonant, the linker and each code point of joined, which holds each InCB=Extend of Go's
		// tables, then each letter of that script. A linker that makes no cut here, since Go's tables
		// give its consonant no script with a letter, fails the row by name.
		for _, linked := range linkers {
			consonant, size := utf8.DecodeRuneInString(linked)
			made := cuts.cuts
			for name, table := range unicode.Scripts {
				if unicode.Is(table, consonant) {
					for _, letter := range letters[name] {
						for _, next := range letters[name] {
							cuts.cut(string(letter)+linked[size:], string(next)+"a")
						}
					}
					for _, extend := range joined {
						for _, next := range letters[name] {
							cuts.cut(linked+string(extend), string(next)+"a")
						}
					}
				}
			}
			if cuts.cuts == made {
				t.Errorf("no cut was made after the linker %+q: Go's tables give its consonant %+q no script with a letter", linked[size:], consonant)
			}
		}
		// GB11: each code point and a joiner, then the first pictograph.
		for r := rune(0); r <= utf8.MaxRune; r++ {
			if utf8.ValidRune(r) {
				cuts.cut(string(r)+"\u200D", "\u00A9a")
			}
		}
		cuts.check("before a code point that a grapheme rule joins to the one before it")
	})

	t.Run("a cut before each mark that a grapheme rule keeps after any code point, after each letter of its own script or of its 128 code points, an Inherited mark after the first letter of each script, a SignWriting mark after each SignWriting base and an emoji modifier after each emoji it modifies, leaves it out", func(t *testing.T) {
		// GB9 and GB9a keep each code point of joins after any code point but 31 (above). Each loop
		// below cuts over input sets of its own, and the row stops, naming them, when a loop makes no
		// cut, as when one of its sets holds no code point: the other loops' cuts would still pass it.
		cuts := keptCuts{t: t}
		made := 0
		madeCuts := func(sets string) {
			t.Helper()
			if cuts.cuts == made {
				t.Fatalf("no cut was made over %s: one of them holds no code point", sets)
			}
			made = cuts.cuts
		}
		for _, name := range slices.Sorted(maps.Keys(unicode.Scripts)) {
			for _, mark := range tableRunes(unicode.Scripts[name]) {
				if joins(mark) {
					for _, letter := range letters[name] {
						cuts.cut(string(letter), string(mark)+"a")
					}
				}
			}
		}
		madeCuts("unicode.Scripts, the marks of joins in each script and the letters of each script (letters)")
		for _, mark := range joined {
			for letter := mark &^ 0x7F; letter < mark&^0x7F+0x80; letter++ {
				if unicode.IsLetter(letter) {
					cuts.cut(string(letter), string(mark)+"a")
				}
			}
		}
		madeCuts("joined and the letters among each mark's 128 code points")
		// A mark of the script Inherited takes the script of the letter before it, and Inherited holds
		// no letter: each of its code points of joins after the first letter of each script, which
		// stands for its script. SignWriting holds no letter either: each of its marks after each of
		// its code points that joins nothing, its bases. And each emoji modifier after each emoji
		// modifier base of Unicode 18.0 (emoji-data.txt), which Go's tables do not name.
		inherited := tableRunes(unicode.Inherited)
		for _, name := range slices.Sorted(maps.Keys(letters)) {
			for _, mark := range inherited {
				if joins(mark) {
					cuts.cut(string(letters[name][0]), string(mark)+"a")
				}
			}
		}
		madeCuts("inherited, the code points of joins in it, and the first letter of each script (letters)")
		signWriting := tableRunes(unicode.SignWriting)
		for _, mark := range signWriting {
			if joins(mark) {
				for _, base := range signWriting {
					if !joins(base) {
						cuts.cut(string(base), string(mark)+"a")
					}
				}
			}
		}
		madeCuts("signWriting, its marks (those of joins) and its bases (those not of joins)")
		modifierBases := spanRunes(0x261D, 0x261D, 0x26F9, 0x26F9, 0x270A, 0x270D, 0x1F385, 0x1F385, 0x1F3C2, 0x1F3C4,
			0x1F3C7, 0x1F3C7, 0x1F3CA, 0x1F3CC, 0x1F442, 0x1F443, 0x1F446, 0x1F450, 0x1F466, 0x1F478, 0x1F47C, 0x1F47C,
			0x1F481, 0x1F483, 0x1F485, 0x1F487, 0x1F48F, 0x1F48F, 0x1F491, 0x1F491, 0x1F4AA, 0x1F4AA, 0x1F574, 0x1F575,
			0x1F57A, 0x1F57A, 0x1F590, 0x1F590, 0x1F595, 0x1F596, 0x1F645, 0x1F647, 0x1F64B, 0x1F64F, 0x1F6A3, 0x1F6A3,
			0x1F6B4, 0x1F6B6, 0x1F6C0, 0x1F6C0, 0x1F6CC, 0x1F6CC, 0x1F90C, 0x1F90C, 0x1F90F, 0x1F90F, 0x1F918, 0x1F91F,
			0x1F926, 0x1F926, 0x1F930, 0x1F939, 0x1F93C, 0x1F93E, 0x1F977, 0x1F977, 0x1F9B5, 0x1F9B6, 0x1F9B8, 0x1F9B9,
			0x1F9BB, 0x1F9BB, 0x1F9CD, 0x1F9CF, 0x1F9D1, 0x1F9DD, 0x1FAC3, 0x1FAC5, 0x1FAF0, 0x1FAFA)
		for _, base := range modifierBases {
			for modifier := rune(0x1F3FB); modifier <= 0x1F3FF; modifier++ {
				cuts.cut(string(base), string(modifier)+"a")
			}
		}
		madeCuts("modifierBases and the five emoji modifiers")
		cuts.check("before a mark")
	})

	// lengthPairs gives values at each of lengths, in bytes, each cut to its own length in characters
	// and to one short, each pair once. One is of letters. For each of é, 界 and U+1F54C, characters
	// of two, three and four bytes, the others hold letters and one run of the character, or the
	// character once. Below, few is the remainder of the length's place in lengths divided by 64, so
	// over each 64 lengths running it takes each count from 0 to 63, and spread is that place times
	// 4,093, a prime, modulo the places the character has in a value of the length, so it moves
	// along the whole value as the lengths run.
	//   - A run first, with as many letters after it as the length leaves over, and, where it leaves
	//     some, the run alone, as long as the length rounded down to the character's width. A run
	//     last, after one to four letters, as many as the length needs, and a run between one letter
	//     and one to four.
	//   - A run last, after few letters or up to three more, as many as the length needs, and a run
	//     first, before as many: a run with 0 to 66 letters on one side and none on the other.
	//   - A run of half the length last, after the other half in letters, or first, before them; a
	//     run of half the length between two quarters in letters; and two of the character between
	//     two halves of letters: a run with letters beside it up to half the length.
	//   - The character once first, last, in the middle, and after or before few letters, with the
	//     rest of the letters on its other side.
	//   - The character once after spread letters, and a run last after about as many, or first
	//     before as many, as the length's width allows.
	// Each value but the two runs between letters is a slice of a string the row makes once for its
	// character, so for each length the row makes only those two values of its own.
	lengthPairs := func(lengths []int) []pair {
		var pairs []pair
		seen := map[pair]bool{}
		add := func(value string) {
			characters := utf8.RuneCountInString(value)
			_, last := utf8.DecodeLastRuneInString(value)
			for _, p := range [...]pair{{value, characters, len(value)}, {value, max(characters-1, 0), len(value) - last}} {
				if !seen[p] {
					seen[p] = true
					pairs = append(pairs, p)
				}
			}
		}
		to := slices.Max(lengths)
		letters := strings.Repeat("a", to)
		for _, n := range lengths {
			add(letters[:n])
		}
		for _, character := range []string{"é", "界", "\U0001F54C"} {
			width := len(character)
			run := strings.Repeat(character, to/width)
			// first[over] is the run then over letters, last[over] is over+1 letters then the run;
			// once is the character between two runs of letters, lettersRun and runLetters the run
			// after and before letters, and twice two of the character between two runs of letters.
			first, last := make([]string, width), make([]string, width)
			for over := range width {
				first[over] = run + letters[:over]
				last[over] = letters[:over+1] + run
			}
			once := letters + character + letters
			lettersRun, runLetters, twice := letters+run, run+letters, letters+run[:2*width]+letters
			for i, n := range lengths {
				end := first[n%width]
				add(end[len(end)-n:])
				if n < width+2 {
					continue
				}
				add(last[(n-1)%width][:n])
				after := (n-2)%width + 1
				add("a" + run[:n-1-after] + letters[:after])
				if n%width != 0 {
					add(run[:n-n%width])
				}
				add(once[to : to+n])
				add(once[to+width-n : to+width])
				before := (n - width) / 2
				add(once[to-before : to-before+n])
				few := i % 64
				if few <= n-width {
					add(once[to-few : to-few+n])
					add(once[to+width+few-n : to+width+few])
				}
				if few += (n - few) % width; few <= n-2*width {
					add(lettersRun[to-few : to-few+n])
					add(runLetters[len(run)-(n-few) : len(run)-(n-few)+n])
				}
				// spread moves over each place of the length as the lengths run, so the character once
				// and a run at either end stand at places away from the ends and the middle too.
				spread := i * 4093 % (n - width + 1)
				add(once[to-spread : to-spread+n])
				if spread += (n - spread) % width; spread <= n-2*width {
					add(lettersRun[to-spread : to-spread+n])
					add(runLetters[len(run)-(n-spread) : len(run)-(n-spread)+n])
				}
				if n >= 2*width+2 {
					half := n / 2 / width * width
					add(lettersRun[to-(n-half) : to+half])
					add(runLetters[len(run)-half : len(run)-half+n])
					quarter := n / 4
					add(letters[:quarter] + run[:half] + letters[:n-quarter-half])
					around := (n - 2*width) / 2
					add(twice[to-around : to-around+n])
				}
			}
		}
		return pairs
	}
	var short, long []int
	for n := 0; n <= 8001; n++ {
		short = append(short, n)
	}
	// The eighth row starts one step past the seventh's last length, so no pair is in both.
	for n := 8001 + 32; n <= 32001; n += 32 {
		long = append(long, n)
	}
	for n := 32001 + 199; n <= 128001; n += 199 {
		long = append(long, n)
	}
	t.Run("a value of letters, or of a run or one of a character of two, three or four bytes first, last or between letters or alone, of each length up to 8,001 bytes, unchanged or cut one short, copies nothing", func(t *testing.T) {
		answersSlices(t, lengthPairs(short))
	})
	t.Run("a value of letters, or of a run or one of a character of two, three or four bytes first, last or between letters or alone, of every 32nd length from 8,033 to 32,001 bytes and of every 199th to 128,001, unchanged or cut one short, copies nothing", func(t *testing.T) {
		answersSlices(t, lengthPairs(long))
	})
	// The two rows above cut each pair once. This one cuts the values of each kind they hold 22
	// times, at each edge of the ranges of lengths that the rows of this test hold, and one byte each
	// side of it: 5 and 6, where the shape rows end and the seeded row starts (4 to 7); 64 and 65,
	// where the seeded row ends (63 to 66, with 67 and 68 for each remainder by two, three and four);
	// 2,001 and 2,004, the place row's (2,000 to 2,005); 8,001, where the seventh row ends (8,000 to
	// 8,002); 8,033, where the eighth starts (8,032 to 8,034); 32,001, where its step changes (32,000
	// to 32,002); and 127,919, its last (127,918 to 127,920). So a copy keyed on how many times a pair
	// was cut fails it, with or without a key on what the value holds, when its key reaches one of
	// these lengths, and so does an allocation HeadRunes does not return.
	t.Run("each kind of value of the two rows above, at each edge of the lengths the rows hold and one byte each side of it (4 to 7, 63 to 68, 2,000 to 2,005, 8,000 to 8,002, 8,032 to 8,034, 32,000 to 32,002 and 127,918 to 127,920 bytes), unchanged or cut one short, copies nothing on each of 22 calls", func(t *testing.T) {
		copiesNothing(t, lengthPairs([]int{4, 5, 6, 7, 63, 64, 65, 66, 67, 68, 2000, 2001, 2002, 2003, 2004, 2005, 8000, 8001, 8002, 8032, 8033, 8034, 32000, 32001, 32002, 127918, 127919, 127920}))
	})

	shapes := map[string][]pair{}
	for _, value := range values {
		for limit := 0; limit <= limits; limit++ {
			if want := headRunesReference(value, limit); strings.HasPrefix(value, want) {
				shape := shapeOf(value, limit)
				shapes[shape] = append(shapes[shape], pair{value, limit, len(want)})
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
		{name: "2,001 Chinese characters are returned unchanged", value: strings.Repeat("界", 2001), limit: 2001, want: strings.Repeat("界", 2001)},
		{name: "32,000 letters are returned unchanged", value: strings.Repeat("a", 32000), limit: 32000, want: strings.Repeat("a", 32000)},
		{name: "32,000 Chinese characters are returned unchanged", value: strings.Repeat("界", 32000), limit: 32000, want: strings.Repeat("界", 32000)},
		{name: "32,001 letters are cut at 32,000", value: strings.Repeat("a", 32001), limit: 32000, want: strings.Repeat("a", 32000)},
		{name: "32,001 Chinese characters are cut at 32,000", value: strings.Repeat("界", 32001), limit: 32000, want: strings.Repeat("界", 32000)},
	} {
		t.Run(row.name, func(t *testing.T) {
			if !strings.HasPrefix(row.value, row.want) {
				t.Fatalf("the row wants %.40q, which is not the first bytes of its value", row.want)
			}
			copiesNothing(t, []pair{{row.value, row.limit, len(row.want)}})
		})
	}
}
