package specform

import (
	"fmt"
	"slices"
)

// maxSentences is how many sentences a statement has at most (R49).
const maxSentences = 2

// holdsWords marks each footnote of notes that holds words, or refers to a footnote that does,
// directly or in turn (Footnote.words). Each footnote that holds words marks the footnotes that
// refer to it, and a marked one stops the walk, so each footnote is marked once.
func holdsWords(notes []*Footnote) {
	referrers := map[*Footnote][]*Footnote{}
	for _, note := range notes {
		for _, child := range note.Notes {
			referrers[child] = append(referrers[child], note)
		}
	}
	var marked []*Footnote
	for _, note := range notes {
		if note.Body != "" {
			marked = append(marked, note)
		}
	}
	for len(marked) > 0 {
		note := marked[len(marked)-1]
		marked = marked[:len(marked)-1]
		if note.words {
			continue
		}
		note.words = true
		marked = append(marked, referrers[note]...)
	}
}

// footnoteJudge judges the footnotes of the texts of one Check. It judges each footnote once
// (judgment), and gives each of its findings once: the findings of the form rules to the first text
// that holds the footnote and whose kind those rules judge (prose), and the findings of the
// reference rules to the first text that holds it, so that the work and the findings of a judgment
// grow with the texts and their footnotes, not with their product. A text holds a footnote that it
// refers to, and each footnote that such a footnote refers to in turn. The words of each finding
// name the footnote and how many other texts refer to it (place). The sentences of each footnote
// that a statement holds count among the statement's (sentences).
type footnoteJudge struct {
	c      Context
	people [][]string
	texts  []Text
	judged map[*Footnote]*footnoteJudgment
	// formGiven and referencesGiven are each footnote whose findings of the form rules, and of the
	// reference rules, a text already holds.
	formGiven, referencesGiven map[*Footnote]bool
	// referrers is how many of the texts refer to each footnote directly.
	referrers map[*Footnote]int
	// counted is, for each footnote that a text holds, the footnotes with sentences that it holds,
	// until their sentences pass maxSentences (countSentences).
	counted map[*Footnote]*sentenceCount
}

// footnoteJudgment is one footnote judged: the findings of the form rules, the findings of the
// reference rules and the count of its sentences.
type footnoteJudgment struct {
	form, references []broken
	sentences        int
}

// sentenceCount is the footnotes with sentences that one footnote holds, itself among them, and the
// sum of their sentences, which stops growing once it passes maxSentences: a statement that holds
// it then has more than maxSentences sentences, whatever the rest.
type sentenceCount struct {
	notes []*Footnote
	sum   int
}

func (count *sentenceCount) full() bool {
	return count.sum > maxSentences
}

// footnoteSentences is the sentences of the footnotes that a statement holds: their count, and
// whether the count is a least one (atLeast), because one footnote holds more than it counted.
type footnoteSentences struct {
	count   int
	atLeast bool
}

func newFootnoteJudge(c Context, people [][]string, texts []Text) *footnoteJudge {
	judge := &footnoteJudge{
		c: c, people: people, texts: texts,
		judged:          map[*Footnote]*footnoteJudgment{},
		formGiven:       map[*Footnote]bool{},
		referencesGiven: map[*Footnote]bool{},
		referrers:       map[*Footnote]int{},
	}
	for _, text := range texts {
		for _, note := range text.Notes {
			judge.referrers[note]++
		}
	}
	return judge
}

// judgment is note judged, the first time that a text asks for it.
func (j *footnoteJudge) judgment(note *Footnote) *footnoteJudgment {
	if judged, ok := j.judged[note]; ok {
		return judged
	}
	form, sentences := formBreaks(kindFootnote, oneLine(note.Body), footnoteSentences{})
	judged := &footnoteJudgment{form: form, references: heldBreaks(j.c, j.people, note.Body, note.Held), sentences: sentences}
	j.judged[note] = judged
	return judged
}

// owned is the findings of each footnote that text is the first text to hold, for each of the two
// kinds of rule that judge text: the form rules for a text of prose, then the reference rules.
func (j *footnoteJudge) owned(text Text) []broken {
	var found []broken
	if prose(text.Kind) {
		j.walk(text, j.formGiven, func(via, note *Footnote) {
			found = append(found, j.placed(via, note, j.judgment(note).form)...)
		})
	}
	j.walk(text, j.referencesGiven, func(via, note *Footnote) {
		found = append(found, j.placed(via, note, j.judgment(note).references)...)
	})
	return found
}

// walk calls visit for each footnote that text holds and given does not, in the order of the first
// reference, with the footnote that text refers to directly through which it holds it (via), and
// adds it to given. A footnote in given was walked whole by an earlier text, so the walk stops at
// it, and each footnote is walked once for each set.
func (j *footnoteJudge) walk(text Text, given map[*Footnote]bool, visit func(via, note *Footnote)) {
	type step struct{ via, note *Footnote }
	var queue []step
	for _, note := range text.Notes {
		queue = append(queue, step{note, note})
	}
	for index := 0; index < len(queue); index++ {
		each := queue[index]
		if given[each.note] {
			continue
		}
		given[each.note] = true
		visit(each.via, each.note)
		for _, child := range each.note.Notes {
			if !given[child] {
				queue = append(queue, step{each.via, child})
			}
		}
	}
}

// placed is found, the findings of note, with the place in front of each detail: the footnote, the
// footnote that the text refers to when it holds note through it, and how many other texts refer
// to each.
func (j *footnoteJudge) placed(via, note *Footnote, found []broken) []broken {
	if len(found) == 0 {
		return nil
	}
	place := fmt.Sprintf("in its footnote [^%s]%s", note.Label, others(j.referrers[note]-1, "it"))
	if via != note {
		place = fmt.Sprintf("in the footnote [^%s], which its footnote [^%s] refers to%s", note.Label, via.Label, others(j.referrers[via]-1, "[^"+via.Label+"]"))
		if direct := j.referrers[note]; direct > 0 {
			place += fmt.Sprintf(", and %s to [^%s]", textCount(direct), note.Label)
		}
	}
	placed := make([]broken, len(found))
	for index, each := range found {
		each.detail = place + ": " + each.detail
		placed[index] = each
	}
	return placed
}

// others says how many other texts also refer to what, when any do.
func others(count int, what string) string {
	switch {
	case count <= 0:
		return ""
	case count == 1:
		return fmt.Sprintf(" (1 other text also refers to %s)", what)
	}
	return fmt.Sprintf(" (%d other texts also refer to %s)", count, what)
}

// textCount is count texts, in words.
func textCount(count int) string {
	if count == 1 {
		return "1 text refers"
	}
	return fmt.Sprintf("%d texts refer", count)
}

// sentences is the sentences of the footnotes that text holds, which count among its own: the
// footnotes with sentences of each footnote that it refers to (countSentences), each once.
func (j *footnoteJudge) sentences(text Text) footnoteSentences {
	if text.Kind != KindStatement || len(text.Notes) == 0 {
		return footnoteSentences{}
	}
	if j.counted == nil {
		j.countSentences()
	}
	var held []*Footnote
	var noted footnoteSentences
	for _, note := range text.Notes {
		count := j.counted[note]
		noted.atLeast = noted.atLeast || count.full()
		for _, each := range count.notes {
			if noted.count > maxSentences {
				noted.atLeast = true
				return noted
			}
			if !slices.Contains(held, each) {
				held = append(held, each)
				noted.count += j.judgment(each).sentences
			}
		}
	}
	return noted
}

// countSentences counts, for each footnote that a text holds, the footnotes with sentences that it
// holds (sentenceCount). Each footnote with sentences is added to itself and to each footnote that
// holds it, walking back along the references, and the walk stops at a footnote that has it
// already or whose count is full. A footnote whose count is full holds more than maxSentences
// sentences, and so does each footnote that refers to it. So each footnote takes at most
// maxSentences+1 footnotes, and the work is in proportion to the footnotes and their references.
func (j *footnoteJudge) countSentences() {
	j.counted = map[*Footnote]*sentenceCount{}
	var held, order []*Footnote
	for _, text := range j.texts {
		held = append(held, text.Notes...)
	}
	for index := 0; index < len(held); index++ {
		note := held[index]
		if j.counted[note] != nil {
			continue
		}
		j.counted[note] = &sentenceCount{}
		order = append(order, note)
		held = append(held, note.Notes...)
	}
	referrers := map[*Footnote][]*Footnote{}
	for _, note := range order {
		for _, child := range note.Notes {
			referrers[child] = append(referrers[child], note)
		}
	}
	for _, source := range order {
		sentences := j.judgment(source).sentences
		if sentences == 0 {
			continue
		}
		walk := []*Footnote{source}
		for len(walk) > 0 {
			note := walk[len(walk)-1]
			walk = walk[:len(walk)-1]
			count := j.counted[note]
			if count.full() || slices.Contains(count.notes, source) {
				continue
			}
			count.notes, count.sum = append(count.notes, source), count.sum+sentences
			walk = append(walk, referrers[note]...)
		}
	}
}
