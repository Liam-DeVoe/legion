package specform

import (
	"sort"
	"strings"
	"unicode"

	"github.com/sjawhar/envoy/internal/dispatch/specform/ste"
)

// wholeText is a text as the judge of the ways out reads it for the rules that read more than one
// of its sentences, so that a way out passes only where the whole text that it writes gives no
// finding of those rules that the text does not give, word for word (passes). The rules that read
// more than one sentence are:
//   - ste100-synonym-rotation, in three places: each member of a synonym group after the first one
//     to stand in the text, at its first place (ste.LintVerbs, read from the Hits of each
//     sentence); the verbs of a text with those of its footnotes (rotationBreaks in formBreaks,
//     with held); and the verbs of a footnote beside those of the footnotes that it refers to and
//     of each text that holds it (footnoteJudge.rotations and held, with elsewhere);
//   - ste100-long-sentence, whose piece runs on past a stop that a closing quote or bracket
//     follows, where a sentence of the form ends (ste.Spread.Open), so that one piece can take the
//     words of several sentences;
//   - ste100-dangling-conjunction, which reads a list item from its mark to its last word, and
//     quotes it whole;
//   - sentences, the count of a statement, which the judge reads in the sentence of the way out
//     with the other sentences of the text (wayJudge.told) and of the statement that holds a
//     footnote (wayJudge.heldIn), so it is no part of wholeText.
//
// Each other rule reads one sentence or one word: stop-no-space, sentence-words and instructions
// read a sentence (sentencesOf); the reference rules read a word, or a name whose parts only white
// space parts (personNames), and no form of theirs holds the stop that ends a sentence; and the
// rules semicolon, phrasal-verb, marketing-adjective and nominalization of ste-lint match words
// with no stop between them. The rules term-line and unreadable read a line under Terms and a
// version, which no way out of a text writes.
//
// A way out writes the sentences that hold its finding (wayJudge.places) and the sentences where
// only its words stand (wayJudge.alsoAt), so the judge reads only those again, beside what it read
// once of each other sentence: the work of a way out grows with those sentences and the pieces of
// ste-lint that run through them, not with the text. A text that ste-lint reads as more than one
// line, as it ends a line at a line break such as \v or U+2028 that the one line of a text keeps
// (ste.Lines), the judge reads whole for each way out instead (lines), since ste-lint ends a list
// item and a piece at such a break. Each such reading reads the whole text, so the judge reads such
// a text for the ways out of its first maxJudgedInSentence findings only, as it reads a sentence
// for those of the first maxJudgedInSentence findings of the sentence (the work table's row of many
// lines of one text). A finding after them is still refused and named, and its detail says that
// Dispatch offers no way out of it (wholeNote). Each reading of a sentence, its words or its Spread
// asks the sentence's charge first (sentenceReads, reads), as every reading of the judge does, so
// the work of a judgment stays in proportion to its text however many ways out read one sentence;
// a way out that would read a sentence past its charge is refused unread (elsewhereNote).
type wholeText struct {
	line string
	// spans is the bytes of each sentence of the line, from the end of the one before it, so that
	// they write the line whole (sentencesOf); keys is the unit of each sentence, its text as the
	// judge names a place (unitKey, sentence.in), and at the sentences of each unit; and opened is
	// whether a backtick that nothing closes stands before each sentence, an odd count of backticks
	// in the line before it: ste-lint cuts code font from a backtick to the next one, so a backtick
	// that a way out writes in that sentence would close it, and code font would take the words of
	// each sentence between.
	spans  []string
	keys   []unitKey
	at     map[unitKey][]int
	opened []bool
	// bare is each word of the line, with the sentence that it stands in, by its bare form
	// (bareWord), read the first time that a finding asks where its words stand (holding).
	bare map[string][]wordAt
	// reads is the charge of each reading of a sentence of the judgment (sentenceReads).
	reads *sentenceReads
	// held is the verbs of each footnote that the text holds (footnotesHeld.verbs). elsewhere, for
	// the text of a footnote, reports whether a way out that changes the verbs of its words from
	// one set to another gives a finding of the rule synonym-rotation of the footnotes and the
	// texts that hold the footnote that the judgment does not give (footnoteJudge.rotationCanChange).
	// lines is whether the judge reads the text whole for each way out, and readFor each finding
	// whose ways out it read so.
	held      []ste.Verbs
	elsewhere func(before, after ste.Verbs) bool
	lines     bool
	readFor   map[wayKey]bool
	// The fields below are read once (load): the Spread of each sentence; the sentences that each
	// member of a synonym group stands in, in order, with its first place in each; the words of the
	// piece of ste-lint that runs on into each sentence from the ones before it (left), and that
	// runs from the start of each sentence to the end of its piece (runs, one more than the
	// sentences); the findings of the rules that read more than one sentence, the text's verbs;
	// whether its first sentence starts a list item (listItem), whether its last sentence ends in
	// "and" or "or" (endsInConjunction), and so whether it is a list item that ends so (dangles).
	read              bool
	spreads           []ste.Spread
	stands            map[[2]int][]sentenceHit
	left              []int
	runs              []int
	before            map[findingKey]int
	verbs             ste.Verbs
	listItem          bool
	endsInConjunction bool
	dangles           bool
	passed            map[wholeWay]bool
}

// wordAt is a word of a line, as strings.Fields cuts it, and the sentence that it stands in.
type wordAt struct {
	sentence int
	word     string
}

// sentenceHit is the first place of a member of a synonym group in one sentence of a text.
type sentenceHit struct {
	sentence int
	hit      ste.Hit
}

// writtenSentence is a sentence of a text as a way out writes it: its bytes, and their Spread.
type writtenSentence struct {
	text   string
	spread ste.Spread
}

// wholeWay is a way out of a finding as the whole text reads it once: the finding by its rule and
// its words, and the way out.
type wholeWay struct {
	finding wayKey
	way     wayOut
}

// newWholeText is line as the judge reads it whole, with held, the verbs of its footnotes, and
// elsewhere for the text of a footnote (nil for a text), its readings charged to reads. Its split of
// the line into sentences reads each sentence once, the first of the judgment's readings of it.
func newWholeText(line string, held []ste.Verbs, elsewhere func(before, after ste.Verbs) bool, reads *sentenceReads) *wholeText {
	w := &wholeText{line: line, held: held, elsewhere: elsewhere, reads: reads, at: map[unitKey][]int{}, lines: ste.Lines(line) > 1}
	ticks := 0
	for index, each := range sentencesOf(line) {
		span, key := line[each.start:each.end], unitKey{text: each.in(line)}
		if _, ok := w.at[key]; !ok {
			reads.charge(key, true)
		}
		w.spans = append(w.spans, span)
		w.keys = append(w.keys, key)
		w.at[key] = append(w.at[key], index)
		w.opened = append(w.opened, ticks%2 == 1)
		ticks += strings.Count(span, "`")
	}
	return w
}

// bareWord is word without the characters at its two ends that are no letter or digit: its marks,
// stops, colons and code font.
func bareWord(word string) string {
	return strings.TrimFunc(word, func(letter rune) bool { return !isLetterOrDigit(letter) })
}

// holding is each sentence of the line, as the judge names a place (unitKey), where a writer who
// changes words at each place where they stand (strings.ReplaceAll) changes them and no place of
// places stands, each once; and whether the charge of each sentence that it read let it read it
// (true), or refused a reading (false). words is a word with no white space, the words of a stop
// with no space after it or of an email address, which stands in a word of the line whose bare form
// (bareWord) is its own: "Web.Config" stands in "Web.Config.", "`Web.Config`" and "(Web.Config)",
// and "ada@example.com" in "ada@example.com:.". The words of the line are read once, with the
// sentence of each, by their bare forms (bare), so each finding's look reads the words that have
// its bare form, not the line, and tests each such word, not its sentence. A test reads a word of a
// sentence where the finding does not stand, so it is charged to that sentence, once for each
// sentence of the look (sentenceReads). Words with white space in them, the sentence of a finding
// of instructions or a person's name, stand in no word, and so in no sentence that holding names; a
// sentence that holds the same text is one of places already (at).
func (w *wholeText) holding(words string, places []unitKey) ([]unitKey, bool) {
	if words == "" || strings.ContainsFunc(words, unicode.IsSpace) {
		return nil, true
	}
	if w.bare == nil {
		w.reads.chargeAll(w.at)
		w.bare = map[string][]wordAt{}
		for index, span := range w.spans {
			for _, word := range strings.Fields(span) {
				key := bareWord(word)
				w.bare[key] = append(w.bare[key], wordAt{index, word})
			}
		}
	}
	read := map[int]bool{}
	for _, place := range places {
		if !place.address && !place.title {
			for _, index := range w.at[place] {
				read[index] = true
			}
		}
	}
	tested := map[unitKey]bool{}
	var units []unitKey
	for _, at := range w.bare[bareWord(words)] {
		key := w.keys[at.sentence]
		if read[at.sentence] {
			continue
		}
		if !tested[key] {
			if !w.reads.charge(key, false) {
				return nil, false
			}
			tested[key] = true
		}
		if !strings.Contains(at.word, words) {
			continue
		}
		for _, index := range w.at[key] {
			read[index] = true
		}
		units = append(units, key)
	}
	return units, true
}

// load reads each sentence of the text once, the first time that a way out asks: its Spread, the
// first place of each member of a synonym group, the words of the pieces that run across its two
// ends, and the findings of the whole text.
func (w *wholeText) load() {
	if w.read {
		return
	}
	w.read = true
	w.reads.chargeAll(w.at)
	if w.lines {
		w.before, w.verbs = lintedWhole(w.line, w.held)
		return
	}
	count := len(w.spans)
	w.spreads = make([]ste.Spread, count)
	w.stands = map[[2]int][]sentenceHit{}
	for index, span := range w.spans {
		w.spreads[index] = ste.SpreadOf(span)
		for _, hit := range w.spreads[index].Hits {
			member := [2]int{hit.Group, hit.Member}
			w.stands[member] = append(w.stands[member], sentenceHit{index, hit})
		}
	}
	w.left = make([]int, count)
	for index := 1; index < count; index++ {
		before := w.spreads[index-1]
		switch last := len(before.Pieces) - 1; {
		case !before.Open:
		case last == 0:
			w.left[index] = w.left[index-1] + before.Pieces[0]
		default:
			w.left[index] = before.Pieces[last]
		}
	}
	w.runs = make([]int, count+1)
	for index := count - 1; index >= 0; index-- {
		spread := w.spreads[index]
		w.runs[index] = spread.Pieces[0]
		if len(spread.Pieces) == 1 && spread.Open {
			w.runs[index] += w.runs[index+1]
		}
	}
	w.before, w.verbs = w.rotations(nil)
	if count > 0 {
		w.pieces(0, count-1, nil, false, func(words int) { addLong(w.before, words, 1) })
		w.listItem, w.endsInConjunction = ste.ListItem(w.spans[0]), ste.EndsInConjunction(w.spans[count-1])
	}
	w.dangles = w.listItem && w.endsInConjunction
}

// passes reports whether way, a way out of finding written at each place of its words in the
// sentences of the text that places and also name, writes a text that gives no finding of the rules
// that read more than one sentence that the text does not give as many times (wholeText); and what
// the finding says where the judge did not read it (note): wholeNote where the judge reads the text
// whole, which it does for the ways out of the first maxJudgedInSentence findings that ask and no
// more, and elsewhereNote where a reading of a sentence would pass its charge (sentenceReads). The
// sentences of places hold the finding, and those of also only its words (wayJudge.alsoAt). A place
// that is no sentence of the text, an address or a sentence of a title, changes none of its words.
// A place of the text that is no sentence of the line, which this reading cannot write, refuses the
// way out, and so does a way out that writes a backtick in a sentence after a backtick that nothing
// closes (opened), which would take the sentences between into code font: neither passes unread.
// No way out that a rule writes holds a line break that ste-lint reads (ste.Lines): its words are a
// word of ste-lint, an address or a sentence without the white space at its ends, in a text that
// ste-lint reads as one line or that this reading reads whole (lines).
func (w *wholeText) passes(finding wayKey, way wayOut, places, also []unitKey) (passed bool, note string) {
	key := wholeWay{finding, way}
	if passed, ok := w.passed[key]; ok {
		return passed, ""
	}
	ticks := strings.Count(way.to, "`") != strings.Count(way.from, "`")
	changed := map[int]writtenSentence{}
	passed = true
	// write writes the way out at each place of units, the finding's own (own) or not, where the
	// charge of its sentence lets it, until one refuses the way out.
	write := func(units []unitKey, own bool) bool {
		for _, place := range units {
			if !passed {
				return true
			}
			if place.address || place.title {
				continue
			}
			indexes, ok := w.at[place]
			if !ok {
				passed = false
				return true
			}
			if !w.reads.charge(place, own) {
				return false
			}
			for _, index := range indexes {
				passed = passed && !(ticks && w.opened[index])
				text := strings.ReplaceAll(w.spans[index], way.from, way.to)
				changed[index] = writtenSentence{text, ste.SpreadOf(text)}
			}
		}
		return true
	}
	if !write(places, true) || !write(also, false) {
		return false, elsewhereNote
	}
	if passed && len(changed) > 0 {
		if w.lines && !w.readFor[finding] {
			if len(w.readFor) >= maxJudgedInSentence {
				return false, wholeNote
			}
			if w.readFor == nil {
				w.readFor = map[wayKey]bool{}
			}
			w.readFor[finding] = true
		}
		w.load()
		var charged bool
		if passed, charged = w.writesNoNewFinding(changed); !charged {
			return false, elsewhereNote
		}
	}
	if w.passed == nil {
		w.passed = map[wholeWay]bool{}
	}
	w.passed[key] = passed
	return passed, ""
}

// writesNoNewFinding reports whether the text with the sentences of changed in place of its own
// gives no finding of the rules that read more than one sentence that the text does not give as
// many times (passed), and whether the charge of each sentence that it reads beside those of
// changed let it read it (charged): each other sentence of a text that it reads whole, and each
// sentence between two of changed that a piece of ste-lint runs through (changedRuns). Those are
// sentences where the finding does not stand, as changed holds each place of the finding.
func (w *wholeText) writesNoNewFinding(changed map[int]writtenSentence) (passed, charged bool) {
	var after map[findingKey]int
	var verbs ste.Verbs
	if w.lines {
		var written strings.Builder
		read := map[unitKey]bool{}
		for index, span := range w.spans {
			if each, ok := changed[index]; ok {
				span = each.text
			} else if key := w.keys[index]; !read[key] {
				if !w.reads.charge(key, false) {
					return false, false
				}
				read[key] = true
			}
			written.WriteString(span)
		}
		after, verbs = lintedWhole(written.String(), w.held)
	} else {
		after, verbs = w.rotations(changed)
	}
	if w.elsewhere != nil && verbs != w.verbs && w.elsewhere(w.verbs, verbs) {
		return false, true
	}
	if w.lines {
		return noneNew(after, w.before), true
	}
	for key, count := range w.before {
		if key.rule == "ste100-long-sentence" {
			after[key] += count
		}
	}
	runs, charged := w.changedRuns(changed)
	if !charged {
		return false, false
	}
	for _, run := range runs {
		// The piece that starts the sentence after the run is part of what both readings count
		// where either runs on into it (next), so that the count after the way out holds it as a
		// piece of its own where the way out ends the piece that ran on into it, and the count
		// before it where the way out makes a piece run on into it.
		next := w.spreads[run[1]].Open || w.spread(changed, run[1]).Open
		w.pieces(run[0], run[1], nil, next, func(words int) { addLong(after, words, -1) })
		w.pieces(run[0], run[1], changed, next, func(words int) { addLong(after, words, 1) })
	}
	if !noneNew(after, w.before) {
		return false, true
	}
	if !w.dangling(changed) {
		return true, true
	}
	// The finding of dangling-conjunction quotes the item whole, so it stays the text's own only
	// where each changed sentence reads to the rule as it did.
	if !w.dangles {
		return false, true
	}
	for index, written := range changed {
		if ste.Neutral(written.text) != ste.Neutral(w.spans[index]) {
			return false, true
		}
	}
	return true, true
}

// lintedWhole is the findings of the rules that read more than one sentence (wholeText) in line,
// read whole by ste-lint, with the verbs of its footnotes, held, and its verbs: the reading of a
// text that ste-lint reads as more than one line, and the one that the sentences of any other text
// add up to.
func lintedWhole(line string, held []ste.Verbs) (map[findingKey]int, ste.Verbs) {
	lint, verbs := ste.LintVerbs(line)
	found := map[findingKey]int{}
	for _, finding := range lint {
		switch finding.Rule {
		case "synonym-rotation", "long-sentence", "dangling-conjunction":
			found[findingKeyOf(steBreak(finding))]++
		}
	}
	for _, each := range rotationBreaks(ste.Rotations(verbs, held), "its footnotes") {
		found[findingKeyOf(each)]++
	}
	return found, verbs
}

// noneNew reports whether after holds no finding more times than before.
func noneNew(after, before map[findingKey]int) bool {
	for key, count := range after {
		if count > before[key] {
			return false
		}
	}
	return true
}

// rotations is the findings of the rule synonym-rotation of the text with the sentences of changed
// in place of its own, and its verbs: of its own words, each member of a group after the first one
// to stand in it, at its first place, in the order of ste.LintVerbs; and of its words with those of
// its footnotes (ste.Rotations, held).
func (w *wholeText) rotations(changed map[int]writtenSentence) (map[findingKey]int, ste.Verbs) {
	firsts := map[[2]int]sentenceHit{}
	for member, stands := range w.stands {
		for _, stand := range stands {
			if _, ok := changed[stand.sentence]; !ok {
				firsts[member] = stand
				break
			}
		}
	}
	for index, written := range changed {
		for _, hit := range written.spread.Hits {
			member := [2]int{hit.Group, hit.Member}
			if first, ok := firsts[member]; !ok || index < first.sentence {
				firsts[member] = sentenceHit{index, hit}
			}
		}
	}
	groups := map[int][]sentenceHit{}
	var verbs ste.Verbs
	for _, first := range firsts {
		groups[first.hit.Group] = append(groups[first.hit.Group], first)
		verbs |= first.hit.Verb()
	}
	found := map[findingKey]int{}
	for _, present := range groups {
		// The sentences in order, and the places of one sentence in ste-lint's order (Hit.Less).
		sort.Slice(present, func(a, b int) bool {
			x, y := present[a], present[b]
			if x.sentence != y.sentence {
				return x.sentence < y.sentence
			}
			return x.hit.Less(y.hit)
		})
		for _, each := range present[1:] {
			found[findingKeyOf(steBreak(each.hit.Rotation()))]++
		}
	}
	for _, each := range rotationBreaks(ste.Rotations(verbs, w.held), "its footnotes") {
		found[findingKeyOf(each)]++
	}
	return found, verbs
}

// spread is the Spread of the sentence index of the text, or of the sentence that changed writes in
// its place.
func (w *wholeText) spread(changed map[int]writtenSentence, index int) ste.Spread {
	if written, ok := changed[index]; ok {
		return written.spread
	}
	return w.spreads[index]
}

// changedRuns is the sentences of changed as runs, each from the first to the last of the sentences
// of changed that the pieces of ste-lint join: a piece runs on, in the text or as changed writes
// it, from each sentence of a run to the next. So no piece runs from a run into a sentence of
// changed outside it, before the way out or after it. It reads the Spread of each sentence between
// two of changed until a piece stops there, and pieces reads the runs again, so each such sentence
// is charged once (sentenceReads); it reports false where a charge refuses one.
func (w *wholeText) changedRuns(changed map[int]writtenSentence) ([][2]int, bool) {
	indexes := make([]int, 0, len(changed))
	for index := range changed {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	read := map[unitKey]bool{}
	open := func(index int) (bool, bool) {
		if _, ok := changed[index]; !ok && !read[w.keys[index]] {
			if !w.reads.charge(w.keys[index], false) {
				return false, false
			}
			read[w.keys[index]] = true
		}
		return w.spreads[index].Open || w.spread(changed, index).Open, true
	}
	var runs [][2]int
	for _, index := range indexes {
		if count := len(runs); count > 0 {
			joined, charged := true, true
			for at := runs[count-1][1]; at < index && joined; at++ {
				if joined, charged = open(at); !charged {
					return nil, false
				}
			}
			if joined {
				runs[count-1][1] = index
				continue
			}
		}
		runs = append(runs, [2]int{index, index})
	}
	return runs, true
}

// pieces calls emit with the words of each piece of ste-lint that the sentences from to through
// take part in, each sentence as changed writes it, with the words of the piece that runs on into
// from from the sentences before it (left), and from through into the sentences after it (runs).
// Where next asks for the piece that starts the sentence after through and through, as changed
// writes it, does not run on into it, that piece is emitted alone, as ste-lint reads it then.
func (w *wholeText) pieces(from, through int, changed map[int]writtenSentence, next bool, emit func(int)) {
	open := w.left[from]
	for index := from; index <= through; index++ {
		each := w.spread(changed, index)
		last := len(each.Pieces) - 1
		if last == 0 {
			open += each.Pieces[0]
		} else {
			emit(open + each.Pieces[0])
			for _, words := range each.Pieces[1:last] {
				emit(words)
			}
			open = each.Pieces[last]
		}
		if !each.Open {
			emit(open)
			open = 0
		}
	}
	switch {
	case w.spread(changed, through).Open:
		emit(open + w.runs[through+1])
	case next:
		emit(w.runs[through+1])
	}
}

// dangling reports whether the text with the sentences of changed in place of its own is a list
// item that ends in "and" or "or", as the rule dangling-conjunction reads it: its mark stands in
// its first sentence, and its last word in its last. It reads only the sentences of changed, and
// for the others what load read of them once (listItem, endsInConjunction).
func (w *wholeText) dangling(changed map[int]writtenSentence) bool {
	count := len(w.keys)
	if count == 0 {
		return false
	}
	first, last := w.listItem, w.endsInConjunction
	if each, ok := changed[0]; ok {
		first = ste.ListItem(each.text)
	}
	if each, ok := changed[count-1]; ok && first {
		last = ste.EndsInConjunction(each.text)
	}
	return first && last
}

// addLong adds by to the count in found of the finding of the rule long-sentence of a piece of
// words, where a piece of so many words gives one.
func addLong(found map[findingKey]int, words, by int) {
	if finding, long := ste.LongSentence(words); long {
		found[findingKeyOf(steBreak(finding))] += by
	}
}

// findingKeyOf is a finding as the judge compares two readings: its rule and its detail.
func findingKeyOf(each broken) findingKey {
	return findingKey{each.rule, each.detail}
}
