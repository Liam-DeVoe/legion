package specform

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/specform/charge"
	"github.com/sjawhar/envoy/internal/dispatch/specform/ste"
)

// wholeTestLines is the rows of the tests of the whole text, then each line of ste-lint's corpus.
// The rows hold what joins or splits a piece of ste-lint across two sentences, and what moves the
// first place of a verb: a stop before a closing quote or bracket, an initialism, code font that
// holds a stop and a space, a verb of a group in code font and in a name, and list items.
func wholeTestLines(t *testing.T) []string {
	t.Helper()
	lines := []string{
		`The guide says "Run it." The job copies the report to deploy@build.example.com: each night after the tests of the merge pass on main.`,
		`The guide says (run it.) Then the job copies each report of the night to the host of the build and keeps a copy of it for a week or two.`,
		`Dispatch keeps it in the U.S. Then the job copies each report of the night to the host of the build and keeps a copy for a week or two at most.`,
		"Dispatch reads `one. two` and Web.Config at boot. It logs each read of the file and each write of the file for the team of the night build today.",
		"Dispatch checks Foo.Validate at boot. It validated the file.",
		"Dispatch checks `Foo.Validate` at boot. It validated the file. It confirms each record and verifies it.",
		"- Dispatch reads Web.Config. It keeps the record and",
		"1. Dispatch keeps the record. It checks it or",
		`He said "Stop." "Go." "Wait." Then each of the twenty words of this long piece runs on and on past every closing quote of the line today.`,
		`He said "Stop." "Go." Then the job copies each report of the night build to ada@example.com and keeps a copy for a week.`,
		`The job copies each report of the night build to ada@example.com and says "Done." "Ok." Then it stops for the rest of the night.`,
		`He said "Send it to ada@example.com." Then the job copies the log to ada@example.com each night after the build ends.`,
	}
	corpus, err := os.Open("ste/testdata/corpus.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer corpus.Close()
	scanner := bufio.NewScanner(corpus)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var line string
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// The judge reads the rules that read more than one sentence from what it reads once of each
// sentence (wholeText), and that reading is ste-lint's reading of the whole line (lintedWhole): for
// each line of ste-lint's corpus and of the rows (wholeTestLines) that ste-lint reads as one line,
// the findings of synonym-rotation and long-sentence that wholeText reads, its verbs and whether it
// is a list item that ends in "and" or "or" are ste-lint's. A line that ste-lint reads as more than
// one, as the corpus's lines with \v or U+2028 are, the judge reads whole (wholeText.lines).
func TestTheWholeTextReadsAsSteLintReadsTheLine(t *testing.T) {
	lines := wholeTestLines(t)
	read, joined, several := 0, 0, 0
	for _, text := range lines {
		line := oneLine(text)
		whole := newWholeText(line, nil, nil, newTexts())
		if len(whole.units) == 0 {
			continue
		}
		if whole.lines {
			several++
			continue
		}
		if !whole.load() {
			t.Fatalf("%q: the charge refuses the judgment's one reading of each sentence", line)
		}
		read++
		want, verbs := lintedWhole(line, ste.Held{})
		dangles := false
		for key := range want {
			if key.rule == "ste100-dangling-conjunction" {
				dangles = true
				delete(want, key)
			}
		}
		if !maps.Equal(whole.before, want) || whole.verbs != verbs || whole.dangles != dangles {
			t.Errorf("%q: wholeText reads %v, verbs %b and dangles %v; ste-lint reads %v, verbs %b and dangles %v",
				line, whole.before, whole.verbs, whole.dangles, want, verbs, dangles)
		}
		sentences := sentencesOf(line)
		for _, each := range sentences[:len(sentences)-1] {
			if ste.SpreadOf(line[each.start:each.end]).Open {
				joined++
			}
		}
	}
	t.Logf("read %d lines by their sentences, with %d sentences that a piece runs on from, and %d whole", read, joined, several)
	if read < 700 || joined == 0 || several == 0 {
		t.Errorf("read %d lines by their sentences, %d sentences that a piece runs on from and %d lines whole: the test reads too little", read, joined, several)
	}
}

// The judge reads a change of one sentence in the whole text that it writes as ste-lint reads that
// text (wholeText.passes against lintedWhole of the written line), both ways: no change passes
// that ste-lint gives a new finding of synonym-rotation, long-sentence or dangling-conjunction, and
// none is refused that ste-lint gives none. The changes are of one word of a sentence, each at
// every place of the word in that sentence: the word in code font, six words after it, another
// verb of a synonym group, and, at a stop, a closing quote after it, the stop taken away or the
// closing marks taken away. They end a piece of ste-lint where it ran on into the next sentence,
// make one run on where it ended, and move a verb's first place, so the count of each piece after
// the change, from the sentences before it (left) and into those after it (runs), is read for each
// way a piece can cross the changed sentence. Each line is one that the judge reads by its
// sentences, with no backtick that nothing closes before a sentence (opened), which the judge
// refuses a backtick after on purpose. Each change is read by a reading of its own, as a judgment
// reads one finding's ways out, so the charge of a sentence (charge.Texts.Read) holds each. The
// text that ste-lint reads is written from the line's own split (sentencesOf), not from what the
// judge keeps.
func TestTheWholeTextReadsAChangeOfASentenceAsSteLintReadsTheTextItWrites(t *testing.T) {
	verbs := []string{"checks", "verifies", "validated", "confirm", "deletes", "removed", "starts", "begin", "halted", "display"}
	texts, ways, opened, closed, refused := 0, 0, 0, 0, 0
	for _, text := range wholeTestLines(t) {
		line := oneLine(text)
		sentences := sentencesOf(line)
		if len(sentences) == 0 || ste.Lines(line) > 1 || strings.Count(line, "`")%2 == 1 {
			continue
		}
		texts++
		before, _ := lintedWhole(line, ste.Held{})
		for index, each := range sentences {
			place := each.in(line)
			pieces := ste.Words(place)
			if len(pieces) == 0 {
				continue
			}
			seen := map[wayOut]bool{}
			words := pieces[0]
			for at, word := range words {
				var candidates []wayOut
				if at < 5 || at == len(words)-1 {
					candidates = append(candidates, wayOut{word.Text, "`" + word.Text + "`"}, wayOut{word.Text, word.Text + " x x x x x x"},
						wayOut{word.Text, verbs[(len(word.Text)+at)%len(verbs)]})
				}
				if trimmed := strings.TrimRight(word.Text, closingMarks); trimmed != word.Text {
					candidates = append(candidates, wayOut{word.Text, trimmed})
				} else if strings.HasSuffix(word.Text, ".") {
					candidates = append(candidates, wayOut{word.Text, word.Text + `"`}, wayOut{word.Text, strings.TrimSuffix(word.Text, ".")})
				}
				for _, way := range candidates {
					if seen[way] || strings.Contains(way.from, "`") {
						continue
					}
					seen[way] = true
					// The judge keeps its reading of a way out by its finding and the way out, so the
					// finding names the sentence: the same change in two sentences is two readings.
					finding := wayKey{rule: Rule(fmt.Sprintf("probe of sentence %d", index)), from: way.from}
					judgment := newTexts()
					judged := newWholeText(line, nil, nil, judgment)
					passed, note := judged.passes(finding, way, []charge.Unit{judgment.Name(place, false, false)}, nil)
					if note != "" {
						t.Fatalf("%q: the judge did not read %q to %q: %s", line, way.from, way.to, note)
					}
					var written strings.Builder
					for _, other := range sentences {
						span := line[other.start:other.end]
						if ste.Strip(span) == place {
							span = strings.ReplaceAll(span, way.from, way.to)
						}
						written.WriteString(span)
					}
					after, _ := lintedWhole(written.String(), ste.Held{})
					if full := noneNew(after, before); passed != full {
						t.Errorf("%q: %q to %q in sentence %d: the judge says %v, and ste-lint over the written text says %v (%v, was %v)",
							line, way.from, way.to, index, passed, full, after, before)
					}
					ways++
					if !passed {
						refused++
					}
					span := line[each.start:each.end]
					was, now := ste.SpreadOf(span).Open, ste.SpreadOf(strings.ReplaceAll(span, way.from, way.to)).Open
					switch {
					case was && !now:
						closed++
					case !was && now:
						opened++
					}
				}
			}
		}
	}
	t.Logf("%d lines, %d changes, %d refused, %d that end a piece that ran on, %d that make one run on", texts, ways, refused, closed, opened)
	if texts < 700 || refused == 0 || refused == ways || closed == 0 || opened == 0 {
		t.Errorf("%d lines, %d changes, %d refused, %d and %d that end and make a piece that runs on: the test reads too little", texts, ways, refused, closed, opened)
	}
}
