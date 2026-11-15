package reviewer

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sjawhar/envoy/internal/dispatch/writingcheck"
)

// How much of each part of a record a model call reads. The text itself is never cut.
const (
	// aroundLimit is how much of a document the call reads on each side of the place the text
	// stands at, and from the start of a document the text does not stand in.
	aroundLimit = 8000
	// decisionsLimit is how much of the issue's decisions document the call reads, from its
	// start.
	decisionsLimit = 12000
	// entryLimit is how much of one text of a thread the call reads, and writerEntryLimit of
	// one text of the writer's.
	entryLimit       = 4000
	writerEntryLimit = 2000
)

// extra is what a call's user message holds besides the record.
type extra struct {
	// readerWords shows the reader's words: the reader_words rule set is on.
	readerWords bool
	// structure and detector are the findings of writingcheck's structure rules and of its
	// detector, shown where their rule set is on.
	structure, detector []writingcheck.Finding
	structureOn         bool
	detectorOn          bool
	// question is the side question Dispatch put to the writer session, and answer its answer;
	// shown where answer is not empty.
	question, answer string
	// words are the words a person asked to define.
	words []string
}

// input is the user message of a call about the record's text: the record's parts, each in a
// tag of its own, and the parts of extra that are set.
func (r Record) input(x extra) string {
	var b strings.Builder
	b.WriteString(textPart(r.Text))
	if len(r.Thread) > 0 {
		b.WriteString("\n<thread>\n")
		for _, entry := range r.Thread {
			b.WriteString(entryPart(entry, entryLimit))
		}
		b.WriteString("</thread>\n")
	}
	if r.IssueKey != "" {
		fmt.Fprintf(&b, "\n<issue key=%s>\n%s\n</issue>\n", strconv.Quote(r.IssueKey), r.IssueTitle)
	}
	if r.Project != "" {
		fmt.Fprintf(&b, "\n<project>\n%s\n</project>\n", r.Project)
	}
	if d := r.Document; d != nil && !(d.Holds && r.Text.Kind == KindDocument) {
		place := "primary document of the issue"
		if d.Holds {
			place = "document the text stands in"
		}
		fmt.Fprintf(&b, "\n<document name=%s role=%s>\n%s\n</document>\n", strconv.Quote(d.Name), strconv.Quote(place), d.around())
	}
	if r.Decisions != "" {
		fmt.Fprintf(&b, "\n<decisions>\n%s\n</decisions>\n", head(r.Decisions, decisionsLimit))
	}
	if len(r.WriterTexts) > 0 {
		b.WriteString("\n<writer_texts>\n")
		for _, entry := range r.WriterTexts {
			b.WriteString(entryPart(entry, writerEntryLimit))
		}
		b.WriteString("</writer_texts>\n")
	}
	if x.readerWords {
		b.WriteString("\n<reader_words>\n")
		if len(r.ReaderWords) == 0 {
			b.WriteString("The reader asked about no word before this text.\n")
		}
		for _, word := range r.ReaderWords {
			b.WriteString("- " + word + "\n")
		}
		b.WriteString("</reader_words>\n")
	}
	if x.structureOn {
		b.WriteString(findingsPart("structure_findings", "The structure rules found nothing.", x.structure))
	}
	if x.detectorOn {
		b.WriteString(findingsPart("detector_findings", "The phrase detector found nothing.", x.detector))
	}
	if x.answer != "" {
		fmt.Fprintf(&b, "\n<question_to_writer>\n%s\n</question_to_writer>\n\n<writer_answer>\n%s\n</writer_answer>\n", x.question, x.answer)
	}
	if len(x.words) > 0 {
		b.WriteString("\n<words>\n")
		for _, word := range x.words {
			b.WriteString("- " + word + "\n")
		}
		b.WriteString("</words>\n")
	}
	return b.String()
}

// textPart is the record's text in its tag: an ask's question and its options, as JSON so their
// labels read exactly, with no id (optionWords); an issue's title and spec; any other kind's body.
func textPart(text Text) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<text kind=%s", strconv.Quote(string(text.Kind)))
	switch {
	case text.Kind.asks():
		choice := "single"
		if text.Multiple {
			choice = "multiple"
		}
		options, _ := json.Marshal(optionWords(text.Options))
		fmt.Fprintf(&b, " choice=%s>\n<question>\n%s\n</question>\n<options>\n%s\n</options>\n", strconv.Quote(choice), text.Body, options)
	case text.Kind == KindIssue:
		fmt.Fprintf(&b, ">\n<title>\n%s\n</title>\n<spec>\n%s\n</spec>\n", text.Title, text.Body)
	default:
		fmt.Fprintf(&b, ">\n%s\n", text.Body)
	}
	b.WriteString("</text>\n")
	return b.String()
}

// entryPart is one text of a thread, or of the writer's, in its tag, cut at limit.
func entryPart(entry Entry, limit int) string {
	return fmt.Sprintf("<entry kind=%s author=%s at=%s>\n%s\n</entry>\n",
		strconv.Quote(string(entry.Kind)), strconv.Quote(entry.Author),
		strconv.Quote(entry.At.UTC().Format(time.RFC3339)), head(entry.Text, limit))
}

// findingsPart is the findings of writingcheck's structure rules or its detector in the tag
// named tag, or none where it found none.
func findingsPart(tag, none string, findings []writingcheck.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n<%s>\n", tag)
	if len(findings) == 0 {
		b.WriteString(none + "\n")
	}
	for _, f := range findings {
		fmt.Fprintf(&b, "- Rule %s. The sentence: %s. The fix: %s\n", f.Rule, strconv.Quote(f.Text), f.Rewrite)
	}
	fmt.Fprintf(&b, "</%s>\n", tag)
	return b.String()
}

// around is the part of the document a call reads: aroundLimit characters on each side of the
// place the text stands at (its decision block, else its quote), and from the start where the
// document does not hold the text or the place is not found.
func (d Document) around() string {
	at := -1
	if d.Holds {
		at = d.place()
	}
	if at < 0 {
		return head(d.Markdown, 2*aroundLimit)
	}
	return tail(d.Markdown[:at], aroundLimit) + head(d.Markdown[at:], aroundLimit)
}

// place is the byte offset in the document of the text's decision block, else of its quote, -1
// where it finds neither.
func (d Document) place() int {
	if d.BlockID != "" {
		for _, opening := range []string{":::ask{#" + d.BlockID + " ", ":::ask{#" + d.BlockID + "}"} {
			if at := strings.Index(d.Markdown, opening); at >= 0 {
				return at
			}
		}
	}
	if d.Quote != "" {
		return strings.Index(d.Markdown, d.Quote)
	}
	return -1
}

// cutMark stands where a part was cut.
const cutMark = "[…]"

// head is the first limit characters of text, and a mark where it cut text.
func head(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "\n" + cutMark
}

// tail is the last limit characters of text, and a mark where it cut text.
func tail(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return cutMark + "\n" + string(runes[len(runes)-limit:])
}
