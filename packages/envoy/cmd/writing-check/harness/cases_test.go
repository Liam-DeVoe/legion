package harness

import (
	"strings"
	"testing"
)

func TestFirstParagraphAndWordsBefore(t *testing.T) {
	text := "\n\nThe work is on track.\nIt is half done.\n\nThe blocker is the review of PR 12.\n"
	if got := FirstParagraph(text); got != "The work is on track.\nIt is half done." {
		t.Errorf("first paragraph = %q", got)
	}
	if got := WordsBefore(text, "The blocker is the review"); got != 9 {
		t.Errorf("words before = %d, want 9", got)
	}
	// Case and the punctuation at a word's ends do not matter, and a quote the grader made
	// longer than the text is matched by its first words.
	if got := WordsBefore(text, "the BLOCKER is: the review of PR 12, which waits"); got != 9 {
		t.Errorf("words before a looser quote = %d, want 9", got)
	}
	if got := WordsBefore(text, "no such words here"); got != -1 {
		t.Errorf("words before a quote the text does not hold = %d, want -1", got)
	}
	if got := Words("- one two, three -- four"); got != 4 {
		t.Errorf("words = %d, want 4: a bullet is no word", got)
	}
}

func TestPersonQuestion(t *testing.T) {
	cases := fixtureCases()
	q := cases[0]
	q.OwnerReply.Selected = []string{"zork train"}
	if got := q.PersonQuestion(); !strings.HasPrefix(got, "What is the zork train?") || !strings.Contains(got, "- zork train") {
		t.Errorf("a question case's question: %q", got)
	}
	if got := cases[1].PersonQuestion(); got != "What is the blocker?" {
		t.Errorf("an answer case's question: %q", got)
	}
	if got := cases[2].PersonQuestion(); got != "" {
		t.Errorf("a plain case has no question: %q", got)
	}
}

func TestRecordPart(t *testing.T) {
	c := fixtureCases()[0]
	c.Record.Documents = append(c.Record.Documents,
		CaseDocument{ArtifactID: "art-2", Name: "notes.md", Version: 3},
		CaseDocument{ArtifactID: "art-3", Name: "decisions.md", Version: 2},
		CaseDocument{ArtifactID: "art-4", Name: "extra.md", Version: 1})
	ds := &Dataset{docs: map[docKey]string{
		{"art-1", 1}: "SPEC", {"art-2", 3}: "NOTES", {"art-3", 2}: "DECISIONS", {"art-4", 1}: strings.Repeat("y", docLimit+50),
	}}
	got := ds.RecordPart(&c)
	spec, decisions, notes := strings.Index(got, "SPEC"), strings.Index(got, "DECISIONS"), strings.Index(got, "NOTES")
	if spec < 0 || decisions < spec || notes < decisions {
		t.Errorf("the documents are shown spec, decisions, then the others:\n%s", got)
	}
	if strings.Contains(got, "yyyy") || !strings.Contains(got, "<other_documents>\n- extra.md\n") {
		t.Errorf("a document past the %d shown is listed by its name alone:\n%s", docsShown, got)
	}
	if !strings.Contains(got, `author="person owner@example.com"`) || !strings.Contains(got, `<issue key="TEST-1">`) {
		t.Errorf("the thread and the issue:\n%s", got)
	}
}
