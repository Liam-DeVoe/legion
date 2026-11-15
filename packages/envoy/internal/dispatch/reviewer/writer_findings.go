package reviewer

import (
	"fmt"
	"strings"
	"time"
)

// FindingsFrame is the turn the writer session gets when a review of its text found problems
// (Decision 5): the body of the delivery frame Dispatch sends it, from prompts/writer-findings.md.
// The harness gives the writer of change 3 the same text.
type FindingsFrame struct {
	// ID is the text's id: the held text's, or the posted text's.
	ID string
	// Kind is the text's kind.
	Kind Kind
	// Findings are the review's findings, Rewrite its rewrite, nil for none, and NotApplied the
	// rule of what a rewrite keeps that Rewrite breaks, nil for none (Reviewed).
	Findings   []Finding
	Rewrite    *Text
	NotApplied *NotApplied
	// Held is whether Dispatch holds the text until the writer acts: the frame then names the
	// time left and the two commands. A text reviewed after its post is not held.
	Held bool
	// TimeLeft is the time the writer has to act on a held text
	// (DISPATCH_REVIEWER_WRITER_WINDOW, less what passed).
	TimeLeft time.Duration
	// PostsRewrite is whether the reviewer's rewrite posts when the writer does not act
	// (DISPATCH_REVIEWER_UNEDITED=rewrite). The text posts as written where it is false, and
	// where there is no rewrite or the rewrite is not applied.
	PostsRewrite bool
}

// frameData is what the frame's template is filled from.
type frameData struct {
	ID, Kind, Rewrite, NotApplied, TimeLeft, EditCommand string
	Findings                                             []Finding
	// Block is a held decision block, which shows in its document while its hold keeps it out of
	// its reader's Inbox.
	Held, Block, PostsRewrite bool
}

// editCommands are the dispatch commands that change a posted text of a kind.
var editCommands = map[Kind]string{
	KindAsk:           "dispatch edit-ask",
	KindIssue:         "dispatch issue-update",
	KindDocument:      "dispatch doc-edit",
	KindDecisionBlock: "dispatch doc-edit",
}

// Body is the frame's text.
func (f FindingsFrame) Body() (string, error) {
	data := frameData{
		ID: f.ID, Kind: f.Kind.spoken(), Findings: f.Findings, Held: f.Held, Block: f.Held && f.Kind == KindDecisionBlock,
		PostsRewrite: f.PostsRewrite && f.Rewrite != nil && f.NotApplied == nil,
		TimeLeft:     spokenDuration(f.TimeLeft), EditCommand: editCommands[f.Kind],
	}
	if f.Rewrite != nil {
		data.Rewrite = spokenText(*f.Rewrite)
	}
	if f.NotApplied != nil {
		data.NotApplied = f.NotApplied.Reason
	}
	return render("writer-findings", data)
}

// spokenText is a text as its reader sees it: an ask's question and its options, one line each;
// an issue's title above its spec; any other kind's body.
func spokenText(text Text) string {
	switch {
	case text.Kind.asks():
		return askText(text.Body, text.Options)
	case text.Kind == KindIssue:
		return "Title: " + text.Title + "\n\n" + text.Body
	default:
		return text.Body
	}
}

// spokenDuration is d as a person says it, to the second: "5 minutes", "1 minute 30 seconds".
// A time at or below zero is "no time".
func spokenDuration(d time.Duration) string {
	if d <= 0 {
		return "no time"
	}
	seconds := int((d + time.Second - 1) / time.Second)
	var parts []string
	for _, unit := range []struct {
		name string
		size int
	}{{"hour", 3600}, {"minute", 60}, {"second", 1}} {
		if n := seconds / unit.size; n > 0 {
			seconds -= n * unit.size
			name := unit.name
			if n != 1 {
				name += "s"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, name))
		}
	}
	return strings.Join(parts, " ")
}
