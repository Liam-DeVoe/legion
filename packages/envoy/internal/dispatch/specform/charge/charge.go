// Package charge holds the texts that one judgment of the ways out of a text's findings reads (the
// judge of package specform), and it charges each reading of a text to that text. A text is a
// sentence of the judged text, a sentence of a title or the address of a link or an image, and the
// judgment names it by its Unit, an index, everywhere: in each map, each key and each walk. Its
// bytes, and what the judgment keeps of each place of it in the judged text, are in Texts, outside
// the judge's package, and the one way to read them is Read, which charges the reading first and
// gives the text only where the charge lets it: the judge's code has no field and no key that holds
// a text, so it cannot read one outside the charge. So the judgment reads each text a bounded number
// of times, however many findings read it, and its work stays in proportion to the judged text.
//
// What Read gives can be kept in a variable of the code that read it, as any value of Go can; a
// copy of a text in a field of the judge's own, read again later, would be read outside the charge.
// The judge keeps no such copy, and a review of a new field reads that.
package charge

import "fmt"

// Answer is whether a charge let a reading go ahead (Read), or whether each charge that a walk asked
// let it: a walk of the judge that reads through Read hands its answer on as an Answer. A value of
// this type is never dropped: each call whose results hold one binds it to a name, or hands it on
// whole as its own results, which the tests of package specform hold for each call of that package
// by the type alone (TestEachReadingOfATextKeepsTheAnswerOfItsCharge). So no code reads on past a
// charge that refused it as though the charge had said yes.
type Answer bool

// Unit names one text of a judgment: a sentence of the judged text, a sentence of a title of it
// (title), or the address of a link or an image (address), as Texts.Name gives it, or no text (a
// place that the judge did not find, None). A Unit holds no byte of its text.
type Unit struct {
	// index is one more than the place of the text in its Texts, so that the zero Unit names none.
	index          int32
	address, title bool
}

// None is the unit of a place that names no text, in a title where title.
func None(title bool) Unit {
	return Unit{title: title}
}

// Named reports whether u names a text.
func (u Unit) Named() bool {
	return u.index != 0
}

// Address reports whether u names the address of a link or an image.
func (u Unit) Address() bool {
	return u.address
}

// Title reports whether u names a sentence of a title, or no text in a title.
func (u Unit) Title() bool {
	return u.title
}

// Texts is the texts of one judgment, each under its Unit, with K for each place where it stands in
// the judged text (Keep), and the charge of each: how many times the judgment has read each text,
// for a finding that stands in it (own) and for one that does not, apart. Each count stays within
// limit: a reading past it is refused (Read), and one for a finding that stands in the text stops
// the program where loud is set, as a walk that breaks the bounds that the limit is derived from.
type Texts[K any] struct {
	limit   int
	loud    bool
	entries []entry[K]
	named   map[name]int32
}

type entry[K any] struct {
	text       string
	kept       []K
	own, other int
}

// name is a text as Name knows it: two sentences of the same words are one text, so that a
// judgment reads them once, and a text of a title or an address is another text than a sentence of
// the same words.
type name struct {
	text           string
	address, title bool
}

// New is the texts of one judgment, whose readings of each text stay within limit (Texts).
func New[K any](limit int, loud bool) *Texts[K] {
	return &Texts[K]{limit: limit, loud: loud, named: map[name]int32{}}
}

// Name is the unit of text, a sentence of the judged text, of a title (title) or an address of a
// link or an image (address): the same unit for each sentence of the same words and kind. It reads
// text once, to know it, as a caller names each place of a text once (the judge's split of a text,
// of a title, and each address and each sentence of a procedure once in each run of the rules), and
// charges nothing.
func (t *Texts[K]) Name(text string, address, title bool) Unit {
	key := name{text, address, title}
	index, ok := t.named[key]
	if !ok {
		t.entries = append(t.entries, entry[K]{text: text})
		index = int32(len(t.entries))
		t.named[key] = index
	}
	return Unit{index: index, address: address, title: title}
}

// Keep adds kept, what the judgment keeps of one place of u in the judged text, to the places of u,
// and is its place among them, the index of kept in what Read gives. It charges nothing.
func (t *Texts[K]) Keep(u Unit, kept K) int {
	at := &t.entries[u.index-1]
	at.kept = append(at.kept, kept)
	return len(at.kept) - 1
}

// Read is one reading of the text that u names, for a finding that stands in it (own) or for one
// that does not: where the text's count of such readings is under its limit, it counts the reading
// and gives the text and what the judgment keeps of each of its places, in the order of Keep, with
// the answer yes (ok true); elsewhere it counts nothing and gives nothing, with the answer no, so the
// reader reads nothing of it. A reading past the limit for a finding that stands in the text stops
// the program where loud is set. A unit that names no text gives nothing. The kept values may be
// changed in place, as the judge's one reading of each text for no finding fills in what it reads
// of each place.
func (t *Texts[K]) Read(u Unit, own bool) (text string, kept []K, ok Answer) {
	if !u.Named() {
		return "", nil, false
	}
	at := &t.entries[u.index-1]
	count := &at.other
	if own {
		count = &at.own
	}
	if *count >= t.limit {
		if own && t.loud {
			panic(fmt.Sprintf("charge: a judgment reads the text %.80q a %dth time for the findings that stand in it, past its limit of %d: a walk breaks the bounds that the limit is derived from",
				at.text, t.limit+1, t.limit))
		}
		return "", nil, false
	}
	*count++
	return at.text, at.kept, true
}

// Index is values under a text key, such as each word of the judged text under its bare form: one
// reads only the values of a key that it holds (Get), and no walk can read the keys. So a lookup
// reads its own key, not the texts that the keys came from.
type Index[V any] struct {
	values map[string][]V
}

// Put adds value under key.
func (x *Index[V]) Put(key string, value V) {
	if x.values == nil {
		x.values = map[string][]V{}
	}
	x.values[key] = append(x.values[key], value)
}

// Get is the values under key, in the order of Put.
func (x *Index[V]) Get(key string) []V {
	return x.values[key]
}
