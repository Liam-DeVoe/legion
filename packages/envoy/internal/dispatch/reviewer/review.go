package reviewer

import (
	"context"

	"github.com/sjawhar/envoy/internal/dispatch/writingcheck"
)

// Finding is one place where a text breaks a rule: the rule set, the words of the text, why they
// break it, and the words to write in their place.
type Finding struct {
	RuleSet RuleSet `json:"rule_set"`
	Quote   string  `json:"quote"`
	Why     string  `json:"why"`
	Fix     string  `json:"fix"`
}

// Reviewed is what one review answered.
type Reviewed struct {
	// Findings are the places where the text breaks a rule; none where the text passed.
	Findings []Finding
	// Rewrite is the text with each finding fixed, nil where the review found nothing or the
	// model wrote none.
	Rewrite *Text
	// NotApplied is the rule of what a rewrite keeps that Rewrite breaks (Preserve), nil where
	// it keeps them all. A rewrite that breaks one is not applied, so it is not posted as
	// rewritten; the writer still gets it with the findings.
	NotApplied *NotApplied
	// Checks are the findings of writingcheck's structure rules and detector, which the model
	// read as inputs (the structure and detector rule sets).
	Checks []writingcheck.Finding
	// Calls are the model calls the review made, the detector's first.
	Calls []Call
}

// reviewReply is a review's reply as its schema has it.
type reviewReply struct {
	Findings []Finding `json:"findings"`
	Rewrite  *Text     `json:"rewrite"`
}

// Review reviews record's text by rules (DISPATCH_REVIEWER_RULES): one model call that returns
// the findings and a rewrite, after the structure rules and the detector where they are on. A
// text no rule set applies to passes with no call. A model call that gives no usable answer is a
// *ModelError, and the Reviewed then still carries the calls made.
func Review(ctx context.Context, m Model, rules []RuleSet, record Record) (Reviewed, error) {
	p, err := prepare(ctx, m, "review", rules, record, extra{})
	if err != nil {
		return Reviewed{}, err
	}
	reviewed := Reviewed{Checks: p.checks, Calls: p.calls}
	if len(p.on) == 0 {
		return reviewed, nil
	}
	var reply reviewReply
	call, err := m.call(ctx, PurposeReview, p.system, p.input, reviewSchema(record.Text.Kind, p.on), &reply)
	reviewed.Calls = append(reviewed.Calls, call)
	if err != nil {
		return reviewed, err
	}
	reviewed.Findings = reply.Findings
	if len(reply.Findings) > 0 && reply.Rewrite != nil {
		rewrite := ofKind(*reply.Rewrite, record.Text.Kind)
		reviewed.Rewrite = &rewrite
		reviewed.NotApplied = Preserve(record, rewrite)
	}
	return reviewed, nil
}

// ofKind is a reply's text as a text of kind: its kind set, and only the fields the kind has.
func ofKind(text Text, kind Kind) Text {
	text.Kind = kind
	if kind != KindIssue {
		text.Title = ""
	}
	if !kind.asks() {
		text.Options, text.Multiple = nil, false
	}
	return text
}
