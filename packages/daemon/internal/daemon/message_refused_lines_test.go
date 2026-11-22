package daemon

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/texts"
)

// A message-refused notice quotes the first line of what the asker wrote as the asker wrote it, in
// a block of its own "which nothing else shares" (dispatch://INFRA-3686 R1). A line ends at a line
// feed, or at a carriage return and a line feed, and neither is part of it: begins held the
// carriage return of a first line that ended with both. Main's notice said that a message whose
// first line is empty begins "", and the branch's gave no begins and no word of that line at all,
// so it says so (first_line_empty). The line endings that end the words leave no words out, and
// the words object counted a final line feed as one unit of rest. And the cut at 200 UTF-16 units
// could part a letter from the combining marks after it, which quotes a letter the asker did not
// write: the cut backs off to the start of that letter. Only where one letter's marks alone run
// past the bound does the cut stay where the bound put it, since backing off would quote nothing.
func TestTheBeginsBlockQuotesTheFirstLineAsTheAskerWroteIt(t *testing.T) {
	moved := func(words string) record.StatusWrite {
		return record.OrdinaryStatusWrite("in_progress", "testing", moveSentence(phase.Testing, phase.Implementing)+": "+words)
	}
	status := func(write record.StatusWrite) texts.MessageRefusedRecord {
		return statusWriteRefused("LEGION-2", write, cause, statusWrites{status: true})
	}
	message := func(body string) texts.MessageRefusedRecord {
		return messagePostRefused("LEGION-2", record.MessagePost{Body: body}, cause)
	}
	accented := strings.Repeat("a", 199) + "e\u0301\u0323 hangs on input"
	marks := "a" + strings.Repeat("\u0301", 250)
	for _, tc := range []struct {
		name   string
		rec    texts.MessageRefusedRecord
		begins string
		by     string
		rest   int
	}{
		{name: "a worker's first line ended by a carriage return and a line feed", rec: status(moved("The fuzz suite hangs\r\nsecond line")),
			begins: "The fuzz suite hangs", by: "tester", rest: texts.Units("\r\nsecond line")},
		{name: "the daemon's own first line ended by a carriage return and a line feed", rec: message("READY #42 at head\r\n\r\nthe rest"),
			begins: "READY #42 at head", by: texts.ByDaemon, rest: texts.Units("\r\n\r\nthe rest")},
		{name: "a message whose first line is empty", rec: message("\nREADY #42 at head")},
		{name: "a message whose empty first line ends with a carriage return and a line feed", rec: message("\r\nREADY #42 at head")},
		{name: "a close whose reason opens with an empty line", rec: status(record.StatusWrite{Status: "done", Reason: intake.CloseRoot{Reason: "\r\nthe human decided no change."}.Message()}),
			begins: intake.CloseRootLead, by: texts.ByDaemon, rest: texts.Units("\r\nthe human decided no change.")},
		{name: "words that end with a line feed", rec: message("READY #42 at head\n"), begins: "READY #42 at head", by: texts.ByDaemon},
		{name: "words that end with a carriage return and a line feed", rec: message("READY #42 at head\r\n"), begins: "READY #42 at head", by: texts.ByDaemon},
		{name: "words that end with empty lines", rec: message("READY #42 at head\n\r\n\n"), begins: "READY #42 at head", by: texts.ByDaemon},
		{name: "words of two lines that end with a line feed", rec: message("READY #42 at head\nthe rest\n"), begins: "READY #42 at head", by: texts.ByDaemon,
			rest: texts.Units("\nthe rest")},
		{name: "a carriage return no line feed follows", rec: message("READY #42 at head\r"), begins: "READY #42 at head\r", by: texts.ByDaemon},
		{name: "the daemon's line cut where a letter's combining marks would be parted from it", rec: message(accented),
			begins: strings.Repeat("a", 199), by: texts.ByDaemon, rest: texts.Units(accented) - 199},
		{name: "a worker's line cut where a letter's combining marks would be parted from it", rec: status(moved(accented)),
			begins: strings.Repeat("a", 199), by: "tester", rest: texts.Units(accented) - 199},
		{name: "a letter whose marks alone run past the bound", rec: message(marks),
			begins: "a" + strings.Repeat("\u0301", 199), by: texts.ByDaemon, rest: 51},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notice, err := record.NoticeOf(tc.rec)
			if err != nil {
				t.Fatal(err)
			}
			got := refused(t, tc.rec)
			if got.Begins != tc.begins {
				t.Errorf("begins is %q, want %q", got.Begins, tc.begins)
			}
			if tc.begins == "" {
				if !notice.FirstLineEmpty || len(got.Words) != 0 {
					t.Errorf("a first line that is empty gives first_line_empty %t and the words object %+v, want true and none: main said such a message begins \"\"",
						notice.FirstLineEmpty, got.Words)
				}
				return
			}
			if notice.FirstLineEmpty {
				t.Errorf("a first line that begins %q is named empty", got.Begins)
			}
			if meta := got.Words["begins"]; meta.By != tc.by || meta.Rest != tc.rest || meta.More {
				t.Errorf("the words object names begins by %q with rest %d (more %t), want by %q with rest %d", meta.By, meta.Rest, meta.More, tc.by, tc.rest)
			}
		})
	}
}
