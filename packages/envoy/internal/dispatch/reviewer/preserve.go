package reviewer

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// Keep is one rule of what a rewrite keeps (INFRA-3231's R5, INFRA-3552's R1).
type Keep string

// The rules of what a rewrite keeps.
const (
	// KeepMostWords: a third of the text's words or more, for every kind. A rewrite of fewer
	// words is a stub or a summary in place of the text ("placeholder", "(no change)", an empty
	// body), which the reader would read in place of the whole text.
	KeepMostWords Keep = "most_words"
	// KeepOptionCount: an ask's number of options.
	KeepOptionCount Keep = "option_count"
	// KeepOptionOrder: the order of an ask's options.
	KeepOptionOrder Keep = "option_order"
	// KeepOptionLabels: each option's label, byte for byte. An option's id is the server's, which
	// an edit that keeps the labels and their order keeps (optionWords), so a rewrite has none.
	KeepOptionLabels Keep = "option_labels"
	// KeepChoice: an ask's single or multiple choice.
	KeepChoice Keep = "choice"
	// KeepFirstLine: an ask's first line, byte for byte, where the question has more than one
	// line and that line has 12 words or fewer. Legion's controller finds a finding's ask by it.
	KeepFirstLine Keep = "first_line"
	// KeepRefLine: each line of an ask that starts with "Ref: ", byte for byte, wherever it
	// stands.
	KeepRefLine Keep = "ref_line"
	// KeepPictureLines: each line of an ask that holds only a picture, byte for byte.
	KeepPictureLines Keep = "picture_lines"
	// KeepLinks: each link of an ask's question, somewhere in its text.
	KeepLinks Keep = "links"
	// KeepOptionLines: each option line of a decision block that the ask-edit route does not
	// rewrite, because it holds more than its plain text (docs.LostOptionWords).
	KeepOptionLines Keep = "option_lines"
	// KeepCommentFirstLine: a comment's first line, byte for byte, where the comment has more
	// than one line and that line has 12 words or fewer.
	KeepCommentFirstLine Keep = "comment_first_line"
)

// firstLineWords is the most words a first line a rewrite keeps has.
const firstLineWords = 12

// NotApplied is a rewrite that breaks a rule of what a rewrite keeps, which is therefore not
// applied: the rule and the reason, which the card shows as the failed rewrite's tooltip.
type NotApplied struct {
	Rule   Keep
	Reason string
}

func (n *NotApplied) Error() string {
	return "the rewrite is not applied: " + n.Reason
}

// Preserve weighs rewrite against the text of record by the rules of what a rewrite keeps, in
// the order of Keep's constants, and answers the first rule it breaks; nil where it breaks none.
// Every kind keeps a third of its words or more. An ask and a decision block keep their
// options, their choice, their first line, their `Ref: ` line, their picture lines and their
// links; a decision block also keeps the option lines its document's ask-edit route does not
// rewrite, which Preserve weighs where the record holds the document and the block's id; a
// comment keeps its first line. Every other kind keeps nothing more.
func Preserve(record Record, rewrite Text) *NotApplied {
	original := record.Text
	if broken := keepMostWords(original, rewrite); broken != nil {
		return broken
	}
	switch {
	case original.Kind.asks():
		if broken := keepOptions(original, rewrite); broken != nil {
			return broken
		}
		if broken := keepLines(original.Body, rewrite.Body, KeepFirstLine); broken != nil {
			return broken
		}
		if original.Kind == KindDecisionBlock {
			return keepOptionLines(record, rewrite)
		}
	case original.Kind == KindComment:
		return keepFirstLine(original.Body, rewrite.Body, KeepCommentFirstLine)
	}
	return nil
}

// keepMostWords weighs a rewrite's words against the text's: the rewrite has a third of them or
// more, counted over every field a reader sees (the title, the body and each option).
func keepMostWords(original, rewrite Text) *NotApplied {
	was, now := wordCount(original), wordCount(rewrite)
	if 3*now >= was {
		return nil
	}
	words := fmt.Sprintf("%d words", now)
	if now == 1 {
		words = "1 word"
	}
	return &NotApplied{KeepMostWords, fmt.Sprintf("the rewrite has %s, fewer than a third of the text's %d", words, was)}
}

// wordCount is the number of words of text's title, body and options, words being the runs of
// characters between white space.
func wordCount(text Text) int {
	n := len(strings.Fields(text.Title)) + len(strings.Fields(text.Body))
	for _, option := range text.Options {
		n += len(strings.Fields(option.Label)) + len(strings.Fields(option.Description))
	}
	return n
}

// keepOptions weighs a rewrite's options and choice against the ask's: their number, their
// order and each label. Their ids are the server's (optionWords), so neither side's are weighed.
func keepOptions(original, rewrite Text) *NotApplied {
	was, now := original.Options, rewrite.Options
	if len(now) != len(was) {
		return &NotApplied{KeepOptionCount, fmt.Sprintf("the rewrite has %s, and the ask has %d", optionCount(len(now)), len(was))}
	}
	if !slices.EqualFunc(was, now, sameLabel) && sameLabels(was, now) {
		return &NotApplied{KeepOptionOrder, fmt.Sprintf("the rewrite puts the options in the order %s, and the ask has them in the order %s", labels(now), labels(was))}
	}
	for i := range was {
		if now[i].Label != was[i].Label {
			return &NotApplied{KeepOptionLabels, fmt.Sprintf("option %d has the label %s in the rewrite, and %s in the ask", i+1, strconv.Quote(now[i].Label), strconv.Quote(was[i].Label))}
		}
	}
	if rewrite.Multiple != original.Multiple {
		choice := map[bool]string{false: "single-choice", true: "multiple-choice"}
		return &NotApplied{KeepChoice, fmt.Sprintf("the rewrite makes the %s ask a %s ask", choice[original.Multiple], choice[rewrite.Multiple])}
	}
	return nil
}

// sameLabel reports whether two options have the same label.
func sameLabel(a, b model.AskOption) bool { return a.Label == b.Label }

// sameLabels reports whether two lists hold the same labels, in any order.
func sameLabels(a, b []model.AskOption) bool {
	left, right := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		left[i] = a[i].Label
	}
	for i := range b {
		right[i] = b[i].Label
	}
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// labels is each option's label, quoted, joined.
func labels(options []model.AskOption) string {
	quoted := make([]string, len(options))
	for i, option := range options {
		quoted[i] = strconv.Quote(option.Label)
	}
	return strings.Join(quoted, ", ")
}

// optionCount is n options, as a sentence says it.
func optionCount(n int) string {
	if n == 1 {
		return "1 option"
	}
	return fmt.Sprintf("%d options", n)
}

// keepLines weighs a rewrite's question against the ask's: its first line (under the rule
// first), each `Ref: ` line, each picture line and each link.
func keepLines(was, now string, first Keep) *NotApplied {
	if broken := keepFirstLine(was, now, first); broken != nil {
		return broken
	}
	held := lines(now)
	for _, line := range lines(was) {
		if strings.HasPrefix(line, "Ref: ") && !slices.Contains(held, line) {
			return &NotApplied{KeepRefLine, "the rewrite drops or changes the line " + strconv.Quote(line)}
		}
	}
	for _, line := range lines(was) {
		if pictureLine.MatchString(line) && !slices.Contains(held, line) {
			return &NotApplied{KeepPictureLines, "the rewrite drops or changes the picture line " + strconv.Quote(line)}
		}
	}
	for _, link := range links(was) {
		if !strings.Contains(now, link) {
			return &NotApplied{KeepLinks, "the rewrite drops the link " + strconv.Quote(link)}
		}
	}
	return nil
}

// keepFirstLine weighs a rewrite's first line against the text's, which it keeps where the text
// has more than one line and its first has firstLineWords words or fewer.
func keepFirstLine(was, now string, rule Keep) *NotApplied {
	original := lines(was)
	if len(original) < 2 || len(strings.Fields(original[0])) > firstLineWords {
		return nil
	}
	if rewritten := lines(now); len(rewritten) == 0 || rewritten[0] != original[0] {
		return &NotApplied{rule, fmt.Sprintf("the rewrite changes the first line %s, which has %d words or fewer", strconv.Quote(original[0]), firstLineWords)}
	}
	return nil
}

// keepOptionLines weighs a decision block's rewritten options by its document's ask-edit route
// (docs.LostOptionWords). A record without the block's document weighs nothing here; the write
// of the rewrite weighs it again.
func keepOptionLines(record Record, rewrite Text) *NotApplied {
	document := record.Document
	if document == nil || !document.Holds || document.BlockID == "" {
		return nil
	}
	err := docs.LostOptionWords(document.Markdown, document.BlockID, rewrite.Options)
	if err == nil {
		return nil
	}
	var refused *docs.ErrAskBlockUnrepresentable
	if errors.As(err, &refused) && refused.Field == "options" {
		return &NotApplied{KeepOptionLines, refused.Reason}
	}
	return &NotApplied{KeepOptionLines, "the decision block cannot be read in its document: " + err.Error()}
}

// lines is text's lines, a final line break ending the last rather than starting another.
func lines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

var (
	// pictureLine is a line that holds only a picture, or pictures: Markdown images and the
	// space around them.
	pictureLine = regexp.MustCompile(`^\s*(?:!\[[^\]]*\]\([^)]*\)\s*)+$`)
	// linkTarget is a Markdown link's or picture's address, and bareLink an address written in
	// the text as it is.
	linkTarget = regexp.MustCompile(`\]\(\s*<?([^)\s>]+)`)
	bareLink   = regexp.MustCompile(`(?:https?|dispatch)://[^\s<>()\[\]"'` + "`" + `]+`)
)

// links is each link of text once: each Markdown link's address and each address written bare,
// without the punctuation that ends a sentence after it.
func links(text string) []string {
	var found []string
	add := func(link string) {
		if link = strings.TrimRight(link, ".,;:!?"); link != "" && !slices.Contains(found, link) {
			found = append(found, link)
		}
	}
	for _, match := range linkTarget.FindAllStringSubmatch(text, -1) {
		add(match[1])
	}
	for _, match := range bareLink.FindAllString(text, -1) {
		add(match)
	}
	return found
}
