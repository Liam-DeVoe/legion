package specform

import "fmt"

// fixedReads is how many times a judgment of the ways out (wayJudge) reads each sentence of its
// text for no finding: once to split the text into its sentences (newWholeText), once for the index
// of its words (wholeText.holding), once for its Spread, or for the findings of the text read whole
// (wholeText.load), and once in each of the two runs of the rules, for the places of the reference
// rules (sentencePlaces.of).
const fixedReads = 5

// ownReads is how many times at most a judgment reads one sentence for the findings that stand in
// it: fixedReads; once for what the sentence holds (wayJudge.unit); once for each count of the stops
// with no space after them that a way out leaves there (wayJudge.before), which is none or the
// stops of one of the sentence's first maxJudgedInSentence findings; and twice for each way out of
// each of those findings, once alone (wayJudge.passes) and once in the whole text that the way out
// writes (wholeText.passes). The bound of a sentence's findings (maxJudgedInSentence) holds those
// readings, and no other walk reads a sentence for the findings that stand in it.
const ownReads = fixedReads + 1 + (maxJudgedInSentence + 1) + 2*maxJudgedInSentence*maxWays

// maxReadsOfSentence is how many times at most a judgment reads one sentence of its text, for any
// purpose (sentenceReads): ownReads for the findings that stand in it, and as many again for the
// findings that do not, where the finding's words stand in it (wholeText.holding), or where the
// whole text that a way out writes runs through it (wholeText.changedRuns) or is read whole
// (wholeText.writesNoNewFinding). That is what the number guards: each reading of a sentence reads
// it a fixed number of times, so the work of a judgment is at most maxReadsOfSentence readings of
// each sentence, maxReadsOfSentence times the work of one reading of its text, however many findings
// read one sentence, as when one sentence holds in code font the names that many other sentences
// end with (the work table's rows of one sentence that holds the words of many findings). The half
// for the findings that stand elsewhere is the half for the sentence's own findings, so that those
// findings never lose a way out to the others: their readings are counted apart (own).
const maxReadsOfSentence = 2 * ownReads

// elsewhereNote is what the detail of a finding says whose way out would read a sentence past its
// charge (sentenceReads.charge), so the writer knows that Dispatch offers no way out of it, not that
// none exists.
var elsewhereNote = fmt.Sprintf("Dispatch reads one sentence at most %d times for the ways out of findings that stand elsewhere, and a way out of this one would read a sentence past that, so it offers none for this one, though one can exist",
	ownReads)

// loudReads is whether the charge stops the program at a reading of a sentence, for the findings
// that stand in it, that would pass ownReads: the bound of a sentence's findings holds those
// readings, so such a reading is a walk that breaks it. The tests of this package set it
// (reads_test.go), so that such a walk fails the suite at once. Where it is not set, the charge
// refuses such a reading, as it refuses any other past its charge.
var loudReads = false

// sentenceReads is the one place that charges each reading of a sentence in one judgment of the
// ways out (wayJudge) to that sentence: each walk of the judgment that reads a sentence, its words,
// its Spread or a text that a way out writes from it asks charge first, and reads only where charge
// says yes, so that a judgment reads one sentence at most maxReadsOfSentence times. A sentence is
// named by its unit (unitKey): the sentences of a text that hold the same words are one, and a
// reading of them reads each, so it is charged once. A test of the package (reads_test.go) fails
// when a function of the package reads the bytes of a sentence and does not charge them, unless it
// reads only what its caller charged, from a caller that the test names.
type sentenceReads struct {
	own, other map[unitKey]int
}

// charge charges one reading of unit to it, for a finding that stands in it (own) or for one that
// does not, and reports whether the reading may go ahead: whether the readings of unit for such
// findings stay within ownReads. A reading that would pass that is refused and charged nothing, so
// the finding gets no way out and says so (elsewhereNote); one for the findings that stand in unit
// stops the program where loudReads is set.
func (r *sentenceReads) charge(unit unitKey, own bool) bool {
	counts := &r.other
	if own {
		counts = &r.own
	}
	if *counts == nil {
		*counts = map[unitKey]int{}
	}
	if (*counts)[unit] >= ownReads {
		if own && loudReads {
			panic(fmt.Sprintf("specform: a judgment reads the sentence %.80q a %dth time for the findings that stand in it, past ownReads (%d): a walk breaks the bound of a sentence's findings",
				unit.text, ownReads+1, ownReads))
		}
		return false
	}
	(*counts)[unit]++
	return true
}

// chargeAll charges one reading, for no finding, to each unit of units, once each: a reading of
// each sentence of a text at once (wholeText.holding, wholeText.load). These readings are part of
// ownReads (fixedReads), so no bound that holds lets the charge refuse one.
func (r *sentenceReads) chargeAll(units map[unitKey][]int) {
	for unit := range units {
		r.charge(unit, true)
	}
}
