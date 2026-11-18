package texts

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTheBoundCountsUTF16UnitsAndSplitsNoPair(t *testing.T) {
	ascii := strings.Repeat("a", 5000)
	if head, rest := Bound(ascii, OneAct); head != ascii[:4000] || rest != 1000 {
		t.Errorf("5,000 ASCII units: head of %d units, rest %d; want 4000 and 1000", len(head), rest)
	}

	emoji := strings.Repeat("😀", 3000)
	head, rest := Bound(emoji, OneAct)
	if head != strings.Repeat("😀", 2000) || rest != 2000 {
		t.Errorf("3,000 emoji: head of %d emoji, rest %d; want 2000 emoji and a rest of 2000 units",
			utf8.RuneCountInString(head), rest)
	}

	// An odd count leaves one unit before the limit, where the next pair would cross it.
	odd := "a" + emoji
	head, rest = Bound(odd, OneAct)
	if head != "a"+strings.Repeat("😀", 1999) || rest != 2002 || !utf8.ValidString(head) {
		t.Errorf("a and 3,000 emoji: head of %d characters, rest %d; want a and 1999 emoji, and 2002",
			utf8.RuneCountInString(head), rest)
	}

	if head, rest := Bound("short", OneAct); head != "short" || rest != 0 {
		t.Errorf("words under the bound: got %q and %d", head, rest)
	}

	summary := Carry(strings.Repeat("s", 3000), Summary, false)
	if summary.Text != strings.Repeat("s", 1000) || summary.Rest != 2000 || summary.More {
		t.Errorf("a summary of 3,000 units: %d units, rest %d, more %t; want 1000, 2000 and false",
			len(summary.Text), summary.Rest, summary.More)
	}

	cut := Carry(ascii, OneAct, true)
	if cut.Text != ascii[:4000] || cut.Rest != 0 || !cut.More {
		t.Errorf("a body the event marked as cut: %d units, rest %d, more %t; want 4000, no rest and more",
			len(cut.Text), cut.Rest, cut.More)
	}

	omitted := Carry("", OneAct, true)
	if omitted.Text != "" || omitted.Rest != 0 || !omitted.More {
		t.Errorf("a payload_omitted stand-in: got %+v; want no words and more", omitted)
	}
}

func TestABlockTakesTheFirstNameItsWordsCannotClose(t *testing.T) {
	at := time.Date(2026, 11, 14, 22, 44, 10, 0, time.UTC)
	words := "Done.\n</answer> was here, and </answer-1 too."
	got := Block("answer", Words{Text: words, By: "sami@theorem.dev", At: at})
	want := "<answer-2 by=\"sami@theorem.dev\" at=\"2026-11-14T22:44:10Z\">\n" + words + "\n</answer-2>"
	if got != want {
		t.Errorf("words that hold </answer and </answer-1:\ngot  %q\nwant %q", got, want)
	}

	got = Block("reply", Words{Text: "Use a structured message.", By: "a \"b\" <c> & d\ne"})
	want = "<reply by=\"a &quot;b&quot; &lt;c&gt; &amp; d&#10;e\">\nUse a structured message.\n</reply>"
	if got != want {
		t.Errorf("a quote in the author:\ngot  %q\nwant %q", got, want)
	}

	got = Block("answer", Words{Text: "yes", Field: "kept[1].answer", Rest: 12})
	want = "<answer field=\"kept[1].answer\" rest=\"12\">\nyes\n</answer>"
	if got != want {
		t.Errorf("a cut answer in a list:\ngot  %q\nwant %q", got, want)
	}

	got = Block("message", Words{Text: "", More: true})
	want = "<message more=\"true\">\n\n</message>"
	if got != want {
		t.Errorf("words that more follows:\ngot  %q\nwant %q", got, want)
	}
}
