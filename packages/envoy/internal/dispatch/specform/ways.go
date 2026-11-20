package specform

import (
	"fmt"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/specform/ste"
)

// maxJudgedInSentence is how many findings of one sentence the judge of the ways out reads the
// ways out of (wayJudge.offered). A stop with no space after it and the address of a person can
// stand many times in one sentence, and each reading of a way out reads the whole sentence, so the
// judge reads one sentence at most this many times for each way out a finding can give, and the
// work of a judgment stays in proportion to its text (the work table's rows of many stops and many
// addresses of a person in one sentence). A finding past this many of one sentence is still
// refused and named, and its detail says that Dispatch offers no way out of it (pastNote).
const maxJudgedInSentence = 8

// pastNote is what the detail of a finding past the first maxJudgedInSentence findings of its
// sentence says, so the writer knows that Dispatch offers no way out of it, not that none exists.
var pastNote = fmt.Sprintf("Dispatch offers ways out for the first %d findings of a sentence, so it offers none for this one, though one can exist",
	maxJudgedInSentence)

// offer is the ways out that a judge offers a finding (ways), and whether the finding stands past
// the first maxJudgedInSentence findings of its sentence, whose ways out the judge did not read
// (past).
type offer struct {
	ways [maxWays]wayOut
	past bool
}

// said is detail, the detail of the finding of the offer, with pastNote after it for a finding
// past the findings whose ways out the judge reads.
func (o offer) said(detail string) string {
	if o.past {
		return detail + ". " + pastNote
	}
	return detail
}

// wayJudge offers a way out of a finding only where the text that it writes gives no finding that
// the text does not give, word for word, and one finding fewer of the finding's own rule
// (offered). So no refusal suggests a form that another rule refuses: "that" before a clause can
// make an instruction of 21 words, and a space after a stop can make a statement of three
// sentences. A way out changes one sentence, so the judge reads that sentence alone, by each rule
// that judges a text of its kind: the form rules, with the other sentences of the text and of its
// footnotes in the count of a statement, and the reference rules. The rule synonym-rotation reads
// the text whole, so a way out that adds a verb of a synonym group is refused where the text or a
// footnote that it holds already uses another verb of that group. One finding stands for each
// place of its words, and the writer changes the words at each place, so the judge offers a way
// out only where it passes in the sentence of each place: it first takes each place (survey), then
// judges. A nil judge offers each way out unjudged: the judge's own reading of a sentence is one.
type wayJudge struct {
	c      Context
	people [][]string
	kind   Kind
	// count is how many sentences of the text and of its footnotes count for the rule sentences,
	// and verbs the verbs of the synonym groups that the text and its footnotes use (told). For a
	// footnote that a statement holds, count is that statement's (heldIn), and stays.
	count       int
	verbs       ste.Verbs
	inStatement bool
	// surveying is whether the judge takes places (survey); places is each unit that holds a
	// finding, once and in the order of the text, rank where a finding stands among the
	// findings of each unit that holds it, and ranked how many findings each unit holds.
	surveying bool
	places    map[wayKey][]unitKey
	rank      map[place]int
	ranked    map[unitKey]int
	units     map[unitKey]*judgedUnit
	given     map[wayKey]offer
}

// unitKey is a sentence that the judge reads, or the address of a link or an image (address),
// which the reference rules read as an address and no form rule reads.
type unitKey struct {
	text    string
	address bool
}

// place is a finding, by the words that its ways out change, at one unit that holds it.
type place struct {
	way  wayKey
	unit unitKey
}

// judgedUnit is a unit as the judge reads it alone: each finding by its rule and detail, with how
// many times it stands; the verbs of the synonym groups that it uses; and how many sentences of the
// text stand outside it (others).
type judgedUnit struct {
	findings map[findingKey]int
	verbs    ste.Verbs
	others   int
}

// findingKey is a finding as the judge compares two readings of a unit: its rule and its detail,
// word for word.
type findingKey struct {
	rule   Rule
	detail string
}

// wayKey is a finding as the judge keeps the ways out that it offered: its rule and the words that
// its ways out change.
type wayKey struct {
	rule Rule
	from string
}

func newWayJudge(c Context, people [][]string, kind Kind) *wayJudge {
	return &wayJudge{c: c, people: people, kind: kind}
}

// told gives the judge what the form rules read of the whole text: how many sentences count for
// the rule sentences, with those of its footnotes, and the verbs of the synonym groups that the
// text uses (own) and that its footnotes use (held). The count of the judge of a footnote that a
// statement holds stays the statement's (heldIn).
func (j *wayJudge) told(count int, own ste.Verbs, held []ste.Verbs) {
	if j == nil {
		return
	}
	if !j.inStatement {
		j.count = count
	}
	j.verbs = own
	for _, each := range held {
		j.verbs |= each
	}
}

// heldIn tells the judge of a footnote that a statement holds it, a statement of count sentences
// with those of its footnotes at most (footnoteJudge.statements). The footnote's sentences count in
// the statement's, so the judge reads each sentence of the footnote as a sentence of that statement,
// by the rule sentences too: a way out that adds a sentence to a footnote is judged as the refusal
// counts the statement.
func (j *wayJudge) heldIn(count int) {
	j.kind, j.count, j.inStatement = KindStatement, count, true
}

// survey runs find with the judge taking each place of each finding that find asks an offer of,
// and offering no way out (offered), so that the judge offers a run of find after it each way out
// judged at every place of its finding. find asks the same offers each time it runs, and only the
// findings of the run after survey stand. A nil judge takes nothing.
func (j *wayJudge) survey(find func()) {
	if j == nil {
		return
	}
	j.surveying = true
	find()
	j.surveying = false
}

// offered is the offer of ways, the ways out of a finding of rule whose ways out change from in
// unit, the sentence that holds it, or the address that holds it (address), with a zero wayOut in
// place of each that passes no reading (passes) at some place of the finding (survey). A finding
// past maxJudgedInSentence of the findings of a unit that holds it gives none, and its offer says
// so (past); so does one whose sentence is not known at some place (unit ""), with no word of it,
// and one that survey did not see. A nil judge offers ways as they are, and so does each judge a
// finding that gives none.
func (j *wayJudge) offered(unit string, address bool, rule Rule, from string, ways [maxWays]wayOut) offer {
	if j == nil || ways == ([maxWays]wayOut{}) {
		return offer{ways: ways}
	}
	key := wayKey{rule, from}
	if j.surveying {
		j.take(place{key, unitKey{unit, address}})
		return offer{}
	}
	if given, ok := j.given[key]; ok {
		return given
	}
	given := offer{ways: ways}
	places := j.places[key]
	for _, at := range places {
		if j.rank[place{key, at}] >= maxJudgedInSentence {
			given.past = true
		}
		if at.text == "" || given.past {
			given.ways = [maxWays]wayOut{}
		}
	}
	if len(places) == 0 {
		given.ways = [maxWays]wayOut{}
	}
	for _, at := range places {
		if given.ways == ([maxWays]wayOut{}) {
			break
		}
		state := j.unit(at)
		for index, way := range given.ways {
			if way != (wayOut{}) && !j.passes(state, at, rule, way) {
				given.ways[index] = wayOut{}
			}
		}
	}
	if j.given == nil {
		j.given = map[wayKey]offer{}
	}
	j.given[key] = given
	return given
}

// take keeps at, a place of a finding, once: the unit among the places of its finding, and the
// finding's rank among the findings of the unit.
func (j *wayJudge) take(at place) {
	if _, ok := j.rank[at]; ok {
		return
	}
	if j.rank == nil {
		j.places, j.rank, j.ranked = map[wayKey][]unitKey{}, map[place]int{}, map[unitKey]int{}
	}
	j.rank[at] = j.ranked[at.unit]
	j.ranked[at.unit]++
	j.places[at.way] = append(j.places[at.way], at.unit)
}

// unit is the judge's reading of unit, read once.
func (j *wayJudge) unit(unit unitKey) *judgedUnit {
	if state, ok := j.units[unit]; ok {
		return state
	}
	others := 0
	if prose(j.kind) && !unit.address {
		others = max(0, j.count-len(sentencesOf(unit.text)))
	}
	findings, verbs := j.read(unit, others)
	state := &judgedUnit{findings: findings, verbs: verbs, others: others}
	if j.units == nil {
		j.units = map[unitKey]*judgedUnit{}
	}
	j.units[unit] = state
	return state
}

// read is each finding of unit, with how many times it stands, and the verbs of the synonym groups
// that it uses: for a sentence of prose, the form rules, as a sentence of a text of the judge's
// kind that others sentences of the text stand beside; then the reference rules, for an address as
// an address. It judges no way out.
func (j *wayJudge) read(unit unitKey, others int) (map[findingKey]int, ste.Verbs) {
	findings := map[findingKey]int{}
	var verbs ste.Verbs
	if prose(j.kind) && !unit.address {
		var form []broken
		form, _, verbs = formBreaks(j.kind, unit.text, footnotesHeld{count: others}, nil)
		for _, each := range form {
			findings[findingKey{each.rule, each.detail}]++
		}
	}
	for _, each := range referenceBreaks(j.c, j.people, unit.text, unit.address, nil) {
		findings[findingKey{each.rule, each.detail}]++
	}
	return findings, verbs
}

// passes reports whether way, a way out of a finding of rule in the unit that state reads, writes
// a unit that gives no finding that the unit does not give as many times, and fewer findings of
// rule, and that adds no verb of a synonym group of which the text uses another verb.
func (j *wayJudge) passes(state *judgedUnit, unit unitKey, rule Rule, way wayOut) bool {
	changed := unitKey{strings.Replace(unit.text, way.from, way.to, 1), unit.address}
	if changed.text == unit.text {
		return false
	}
	after, verbs := j.read(changed, state.others)
	own, ownAfter := 0, 0
	for key, count := range after {
		if count > state.findings[key] {
			return false
		}
		if key.rule == rule {
			ownAfter += count
		}
	}
	for key, count := range state.findings {
		if key.rule == rule {
			own += count
		}
	}
	added := verbs &^ state.verbs
	return ownAfter < own && added.Groups()&^added&j.verbs == 0
}

// sentencePlaces finds the sentence of a line that holds a place of it, for the judge of the ways
// out (wayJudge.offered): the sentences of the line as the rules read them (sentencesOf), which
// take each byte of the line once, and a cursor that walks them once for places asked in the order
// of the line.
type sentencePlaces struct {
	line      string
	sentences []sentence
	at        int
}

// of is the sentence that holds offset, a place of the line at or after the last place asked for,
// or "" where the line has no sentence.
func (p *sentencePlaces) of(offset int) string {
	for p.at < len(p.sentences) && p.sentences[p.at].end <= offset {
		p.at++
	}
	if p.at == len(p.sentences) {
		return ""
	}
	return p.sentences[p.at].in(p.line)
}
