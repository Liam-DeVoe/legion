package reviewer

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The user message shows each part of the record in its own tag, and the reader's words only
// where reader_words is on.
func TestTheInputShowsEachPartOfTheRecord(t *testing.T) {
	input := askRecord().input(extra{})
	for _, want := range []string{
		"<text kind=\"ask\" choice=\"single\">\n<question>\nFinding from tester\n",
		`[{"label":"Ship","description":"Release it today"},{"label":"Hold","description":"Wait for the store"}]`,
		"<thread>\n<entry kind=\"comment\" author=\"person sami\" at=\"2026-11-14T09:00:00Z\">\nWhat is a store?\n</entry>\n</thread>",
		"<issue key=\"PRJ-1\">\nShip the reviewer\n</issue>",
		"<document name=\"spec\" role=\"document the text stands in\">\n# Spec",
		"<decisions>\n1. We keep one store.\n</decisions>",
		"<writer_texts>\n<entry kind=\"comment\" author=\"the writer agent\" at=\"2026-11-14T08:00:00Z\">\nI moved the store.\n</entry>",
	} {
		if !strings.Contains(input, want) {
			t.Errorf("the input does not hold %q:\n%s", want, input)
		}
	}
	for _, absent := range []string{"<reader_words>", "<project>", "<structure_findings>", "<writer_answer>", "<words>"} {
		if strings.Contains(input, absent) {
			t.Errorf("the input holds %s", absent)
		}
	}
	// An option's id is the server's: a text whose options carry the ids an ask's read shows
	// gives the model no id.
	read := askRecord()
	read.Text.Options = []model.AskOption{{ID: "b-2", Label: "Ship"}, {ID: "b-3", Label: "Hold"}}
	if input := read.input(extra{}); !strings.Contains(input, `[{"label":"Ship"},{"label":"Hold"}]`) || strings.Contains(input, "b-2") {
		t.Errorf("the input shows an option's id:\n%s", input)
	}
	if input := (Record{Text: Text{Kind: KindMessage, Body: "Hi."}}).input(extra{readerWords: true}); !strings.Contains(input, "<reader_words>\nThe reader asked about no word before this text.\n</reader_words>") {
		t.Errorf("a record with no reader words, reader_words on:\n%s", input)
	}
	issue := Record{Text: Text{Kind: KindIssue, Title: "Land it", Body: "The spec."}, Project: "Project One"}.input(extra{})
	if !strings.Contains(issue, "<text kind=\"issue\">\n<title>\nLand it\n</title>\n<spec>\nThe spec.\n</spec>\n</text>") || !strings.Contains(issue, "<project>\nProject One\n</project>") {
		t.Errorf("an issue of a project:\n%s", issue)
	}
}

// The reader's words leave out the function words (readerStopWords), in any case and with the
// space around them, and keep every other word and phrase in its order. A record of function
// words alone shows no word.
func TestTheReaderWordsLeaveOutFunctionWords(t *testing.T) {
	record := Record{Text: Text{Kind: KindMessage, Body: "Hi."}, ReaderWords: []string{"it", "store", " You ", "There", "the node", "is", "lane", "THAT", "eval"}}
	if input := record.input(extra{readerWords: true}); !strings.Contains(input, "<reader_words>\n- store\n- the node\n- lane\n- eval\n</reader_words>") {
		t.Errorf("the reader's words with function words among them:\n%s", input)
	}
	record.ReaderWords = []string{"we", "This", "a", "an", "are"}
	if input := record.input(extra{readerWords: true}); !strings.Contains(input, "<reader_words>\nThe reader asked about no word before this text.\n</reader_words>") {
		t.Errorf("the reader's words of function words alone:\n%s", input)
	}
}

// A document the text stands in shows the part around the text's place, and one it does not
// stand in shows its start; a document's own text is not shown twice.
func TestTheDocumentShowsThePartAroundTheText(t *testing.T) {
	before, after := strings.Repeat("b", 3*aroundLimit), strings.Repeat("a", 3*aroundLimit)
	block := ":::ask{#blk urgency=\"med\" multiple=\"false\"}\nWhich store?\n:::\n"
	document := Document{Name: "plan", Markdown: before + block + after, Holds: true, BlockID: "blk"}
	around := document.around()
	if !strings.Contains(around, cutMark+"\n"+strings.Repeat("b", aroundLimit)+block) || !strings.HasSuffix(around, cutMark) {
		t.Errorf("the part around a decision block does not hold the block with %d characters before it", aroundLimit)
	}
	if n := utf8.RuneCountInString(around); n > 2*aroundLimit+2*len(cutMark)+2 {
		t.Errorf("the part around the block is %d characters, want at most %d", n, 2*aroundLimit)
	}
	quoted := Document{Markdown: before + "the quiet one" + after, Holds: true, Quote: "the quiet one"}
	if !strings.Contains(quoted.around(), "bbb"+"the quiet one"+"aaa") {
		t.Error("the part around a quote does not hold the quote")
	}
	primary := Document{Markdown: "# Spec\n" + after}
	if got := primary.around(); !strings.HasPrefix(got, "# Spec\n") || utf8.RuneCountInString(got) > 2*aroundLimit+len(cutMark)+1 {
		t.Errorf("the primary document does not show its start, cut at %d characters", 2*aroundLimit)
	}
	own := Record{Text: Text{Kind: KindDocument, Body: "# Plan\n"}, Document: &Document{Name: "plan", Markdown: "# Plan\n", Holds: true}}
	if strings.Contains(own.input(extra{}), "<document") {
		t.Error("a document's own text is shown twice")
	}
}

// A thread's text and the writer's text are cut at their limits.
func TestEntriesAreCut(t *testing.T) {
	long := strings.Repeat("x", entryLimit+10)
	record := Record{Text: Text{Kind: KindComment, Body: "Hi."}, Thread: []Entry{{Kind: KindComment, Author: "person sami", At: time.Unix(0, 0), Text: long}}}
	if input := record.input(extra{}); strings.Contains(input, long) || !strings.Contains(input, strings.Repeat("x", entryLimit)+"\n"+cutMark) {
		t.Error("a thread's text is not cut at its limit")
	}
}
