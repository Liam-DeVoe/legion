package specform

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

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
// footnote that it holds already uses another verb of that group. The way out of a finding is
// judged where the finding first stands, so each place of one finding gives the same way out. A
// nil judge offers each way out unjudged: the judge's own reading of a sentence is one.
type wayJudge struct {
	c      Context
	people [][]string
	kind   Kind
	// count is how many sentences of the text and of its footnotes count for the rule sentences,
	// and verbs the verbs of the synonym groups that the text and its footnotes use (told).
	count int
	verbs ste.Verbs
	units map[unitKey]*judgedUnit
	given map[wayKey]offer
}

// unitKey is a sentence that the judge reads, or the address of a link or an image (address),
// which the reference rules read as an address and no form rule reads.
type unitKey struct {
	text    string
	address bool
}

// judgedUnit is a unit as the judge reads it alone: each finding by its rule and detail, with how
// many times it stands; the verbs of the synonym groups that it uses; how many sentences of the
// text stand outside it (others); and how many findings of it the judge has read the ways out of
// (judged).
type judgedUnit struct {
	findings map[findingKey]int
	verbs    ste.Verbs
	others   int
	judged   int
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
// text uses (own) and that its footnotes use (held).
func (j *wayJudge) told(count int, own ste.Verbs, held []ste.Verbs) {
	if j == nil {
		return
	}
	j.count, j.verbs = count, own
	for _, each := range held {
		j.verbs |= each
	}
}

// offered is the offer of ways, the ways out of a finding of rule whose ways out change from in
// unit, the sentence that holds it, or the address that holds it (address), with a zero wayOut in
// place of each that passes no reading (passes). A finding past maxJudgedInSentence of its
// sentence gives none, and its offer says so (past); so does one whose sentence is not known (unit
// ""), with no word of it. A nil judge offers ways as they are, and so does each judge a finding
// that gives none.
func (j *wayJudge) offered(unit string, address bool, rule Rule, from string, ways [maxWays]wayOut) offer {
	if j == nil || ways == ([maxWays]wayOut{}) {
		return offer{ways: ways}
	}
	key := wayKey{rule, from}
	if given, ok := j.given[key]; ok {
		return given
	}
	var given offer
	if unit != "" {
		read := unitKey{unit, address}
		state := j.unit(read)
		given.past = state.judged >= maxJudgedInSentence
		if !given.past {
			state.judged++
			for index, way := range ways {
				if way != (wayOut{}) && j.passes(state, read, rule, way) {
					given.ways[index] = way
				}
			}
		}
	}
	if j.given == nil {
		j.given = map[wayKey]offer{}
	}
	j.given[key] = given
	return given
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

// sentenceSpans finds the sentence of a line that holds a place of it, as the judge of the ways
// out reads one (wayJudge.offered): the words from one that starts the line or follows one that
// ends a sentence (endsSentence) to the next that ends one, in the order of the line, read once.
type sentenceSpans struct {
	line   string
	spans  [][2]int
	at     int
	cursor int
}

func spansOf(line string) *sentenceSpans {
	found := &sentenceSpans{line: line}
	start, word, end := -1, 0, 0
	for index, letter := range line {
		if unicode.IsSpace(letter) {
			if start >= 0 && end > word && endsSentence(line[word:end]) {
				found.spans = append(found.spans, [2]int{start, end})
				start = -1
			}
			continue
		}
		if start < 0 {
			start = index
		}
		if end < index {
			word = index
		}
		end = index + utf8.RuneLen(letter)
	}
	if start >= 0 {
		found.spans = append(found.spans, [2]int{start, len(line)})
	}
	return found
}

// of is the sentence that holds offset, a place of the line at or after the last place asked for,
// or "" for none.
func (s *sentenceSpans) of(offset int) string {
	for s.at < len(s.spans) && s.spans[s.at][1] <= offset {
		s.at++
	}
	if s.at == len(s.spans) || offset < s.spans[s.at][0] {
		return ""
	}
	return s.line[s.spans[s.at][0]:s.spans[s.at][1]]
}

// next is the sentence that holds word at its first place in the line after the word asked for
// before, or "" where the line does not hold it there.
func (s *sentenceSpans) next(word string) string {
	index := strings.Index(s.line[s.cursor:], word)
	if index < 0 {
		return ""
	}
	offset := s.cursor + index
	s.cursor = offset + len(word)
	return s.of(offset)
}
