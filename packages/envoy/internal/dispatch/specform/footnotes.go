package specform

import (
	"fmt"
	"slices"

	"github.com/sjawhar/envoy/internal/dispatch/specform/ste"
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
// that a statement holds count among the statement's (held). Two verbs for one action are the one
// finding of the text or the footnote that holds them both with no one footnote that it refers to
// holding them all (held, rotations).
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
	// until their sentences pass maxSentences, and verbs the verbs of the synonym groups that it
	// and the footnotes that it holds use (carry). gives is the verbs of each synonym group that a
	// footnote rotates with the footnotes that it refers to (rotations).
	counted map[*Footnote]*sentenceCount
	verbs   map[*Footnote]ste.Verbs
	gives   map[*Footnote]ste.Verbs
}

// footnoteJudgment is one footnote judged: the findings of the form rules, the findings of the
// reference rules, the count of its sentences and the verbs of the synonym groups that it uses.
type footnoteJudgment struct {
	form, references []broken
	sentences        int
	verbs            ste.Verbs
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

// footnotesHeld is what the footnotes that a text holds add to it: for a statement, each footnote
// whose sentences count among its own, with how many it adds, their sum (count) and whether that
// sum is only the least that they hold (atLeast), because one footnote holds more than Dispatch
// counted; for a text of prose, the verbs of the synonym groups that each footnote that it refers
// to uses with the footnotes that it holds.
type footnotesHeld struct {
	adds    []sentencesAdded
	count   int
	atLeast bool
	verbs   []ste.Verbs
}

// sentencesAdded is a footnote, by its label, and how many sentences it adds to a statement.
type sentencesAdded struct {
	label     string
	sentences int
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
	judge := newWayJudge(j.c, j.people, kindFootnote)
	form, sentences, verbs := formBreaks(kindFootnote, oneLine(note.Body), footnotesHeld{}, judge)
	judged := &footnoteJudgment{form: form, references: heldBreaks(j.c, j.people, note.Body, note.Held, judge), sentences: sentences, verbs: verbs}
	j.judged[note] = judged
	return judged
}

// owned is the findings of each footnote that text is the first text to hold, for each of the two
// kinds of rule that judge text: the form rules for a text of prose, then the reference rules.
func (j *footnoteJudge) owned(text Text) []broken {
	var found []broken
	if prose(text.Kind) {
		j.walk(text, j.formGiven, func(via, note *Footnote) {
			found = append(found, j.placed(via, note, j.form(note))...)
		})
	}
	j.walk(text, j.referencesGiven, func(via, note *Footnote) {
		found = append(found, j.placed(via, note, j.judgment(note).references)...)
	})
	return found
}

// form is the findings of the form rules of note: those of its words (judgment), then each synonym
// group that it rotates with the footnotes that it refers to (rotations).
func (j *footnoteJudge) form(note *Footnote) []broken {
	if j.counted == nil {
		j.carry()
	}
	form := j.judgment(note).form
	if gives := j.gives[note]; gives != 0 {
		return append(slices.Clip(form), rotationBreaks(gives, "the footnotes that it refers to")...)
	}
	return form
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

// held is what the footnotes that text holds add to it (footnotesHeld): for a statement, the
// footnotes with sentences of each footnote that it refers to (carry), each once; for a text of
// prose, the verbs that each footnote that it refers to holds (carry).
func (j *footnoteJudge) held(text Text) footnotesHeld {
	var noted footnotesHeld
	if !prose(text.Kind) || len(text.Notes) == 0 {
		return noted
	}
	if j.counted == nil {
		j.carry()
	}
	for _, note := range text.Notes {
		if verbs := j.verbs[note]; verbs != 0 {
			noted.verbs = append(noted.verbs, verbs)
		}
	}
	if text.Kind != KindStatement {
		return noted
	}
	var held []*Footnote
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
				sentences := j.judgment(each).sentences
				noted.count += sentences
				noted.adds = append(noted.adds, sentencesAdded{label: each.Label, sentences: sentences})
			}
		}
	}
	return noted
}

// carry counts, for each footnote that a text holds, the footnotes with sentences that it holds
// (sentenceCount), and the verbs that it holds. Each footnote with sentences is added to itself and
// to each footnote that holds it, walking back along the references, and the walk stops at a
// footnote that has it already or whose count is full. A footnote whose count is full holds more
// than maxSentences sentences, and so does each footnote that refers to it. So each footnote takes
// at most maxSentences+1 footnotes. The verbs of each footnote are added to itself and to each
// footnote that holds it in the same way, and the walk stops at a footnote that holds them all
// already, so each footnote takes each verb once. So the work is in proportion to the footnotes
// and their references. Then it finds the rotations that each footnote gives (rotations).
func (j *footnoteJudge) carry() {
	j.counted, j.verbs = map[*Footnote]*sentenceCount{}, map[*Footnote]ste.Verbs{}
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
		judged := j.judgment(source)
		if verbs := judged.verbs; verbs != 0 {
			walk := []*Footnote{source}
			for len(walk) > 0 {
				note := walk[len(walk)-1]
				walk = walk[:len(walk)-1]
				if j.verbs[note]&verbs == verbs {
					continue
				}
				j.verbs[note] |= verbs
				walk = append(walk, referrers[note]...)
			}
		}
		sentences := judged.sentences
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
	j.rotations(order, referrers)
}

// rotations finds, for each footnote that a text holds, the verbs of each synonym group that it
// breaks the rule synonym-rotation in with the footnotes that it refers to (gives), once carry has
// carried the verbs: two verbs or more of the group that it and the footnotes that it holds use
// together, where neither its own words, which are Lint's finding of it, nor one footnote that it
// refers to holds them all (ste.Rotations). Where one footnote that it refers to holds them all,
// that footnote gives the finding or rotates them in its own words, or a footnote that it holds in
// turn does: so each group that a footnote gives or rotates in its own words is given for each
// footnote that holds that footnote with the same verbs of the group (given), walking back along
// the references. Footnotes that refer to each other in a loop hold the same verbs, so a group that
// they rotate together and that none of them gives is given by the first footnote of the loop in
// the order of the texts. Each footnote takes each group once, so the work is in proportion to the
// footnotes and their references.
func (j *footnoteJudge) rotations(order []*Footnote, referrers map[*Footnote][]*Footnote) {
	j.gives = map[*Footnote]ste.Verbs{}
	given := map[*Footnote]ste.Verbs{}
	// spread adds groups, each verb of each group, to given of source, and of each footnote that
	// holds it with the same verbs of the group.
	spread := func(source *Footnote, groups ste.Verbs) {
		type step struct {
			note   *Footnote
			groups ste.Verbs
		}
		walk := []step{{source, groups}}
		for len(walk) > 0 {
			each := walk[len(walk)-1]
			walk = walk[:len(walk)-1]
			added := each.groups &^ given[each.note]
			if added == 0 {
				continue
			}
			given[each.note] |= added
			for _, referrer := range referrers[each.note] {
				walk = append(walk, step{referrer, added &^ (j.verbs[referrer] ^ j.verbs[each.note]).Groups()})
			}
		}
	}
	var held []ste.Verbs
	for _, note := range order {
		own, all := j.judgment(note).verbs, j.verbs[note]
		if all == 0 {
			continue
		}
		held = held[:0]
		for _, child := range note.Notes {
			held = append(held, j.verbs[child])
		}
		j.gives[note] = ste.Rotations(own, held)
		spread(note, (j.gives[note] | own.Rotated()&^(own^all).Groups()).Groups())
	}
	for _, note := range order {
		if lost := j.verbs[note].Rotated() &^ given[note]; lost != 0 {
			j.gives[note] |= lost
			spread(note, lost.Groups())
		}
	}
}
