package harness

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"text/template"

	"github.com/sjawhar/envoy/cmd/writing-check/harness/claude"
	"github.com/sjawhar/envoy/cmd/writing-check/harness/llm"
)

// writerFiles are the prompts of the writer the harness plays (changes 2, 3, 5, 6 and 7a), apart
// from the reader's and the grader's prompts/, so change 1's prompt digest does not move with
// them.
//
//go:embed writer/*.md
var writerFiles embed.FS

var writerPrompts = template.Must(template.New("writer").ParseFS(writerFiles, "writer/*.md"))

// WriterDigest is the SHA-256 of every writer prompt, by name: what a run that plays the writer
// was made with.
func WriterDigest() string {
	h := sha256.New()
	names, _ := fs.Glob(writerFiles, "writer/*.md")
	slices.Sort(names)
	for _, name := range names {
		data, _ := writerFiles.ReadFile(name)
		fmt.Fprintf(h, "%s %d\n", name, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writerData is what a writer prompt is filled from.
type writerData struct {
	Kind       string
	Ask        bool
	HasSession bool
}

// writerMaxTokens bounds the writer's reply: a document written again. llm asks again with twice
// the bound for a reply cut at it.
const writerMaxTokens = 16000

// The writer's prompts (writer/writer.md): its text written again, its action on a review's
// findings, its answer to a side question.
const (
	promptRewrite  = "writer-rewrite"
	promptFindings = "writer-findings"
	promptAnswer   = "writer-answer"
)

// The orders of the writer's user turn (score --writer-order). writerSessionLast, the default, is
// the order the harness has always sent: Dispatch's record, the writer's text, Dispatch's
// message, then the session part. writerSessionFirst sends the record and the session part, then
// the text and the message: the order of the harness's first cache layout, kept so that a change
// can be scored with each order to see how much its effect depends on where its words sit.
const (
	writerSessionLast  = "session-last"
	writerSessionFirst = "session-first"
)

// Writer is the writer agent the harness plays (plan Decision 20): the Claude model of
// candidates.yaml's writer role, given Dispatch's record of the case, its own text, the message
// Dispatch sent it, and its session up to the text where the case has one. Only a claude.Client
// takes the session (claude.SessionText). Calls are the most calls of each prompt that one lane's
// change makes for one case (changePlan.writerCalls), which decide the cache mark (marked).
// Order is the order of its user turn, writerSessionLast where it is "".
type Writer struct {
	Model *claude.Client
	Calls map[string]int
	Order string
}

// marked says the calls of prompt for a case carry the cache mark: where lanes lanes that make
// the change of the case can call it two or more times in all. They send the same prefix (the
// prompt, its schema and the record, and in the order writerSessionFirst the session part), and
// they go one after another (scorer.scoreCase), so each after the first reads the prefix from the
// provider's cache. A call that one lane alone can make once goes unmarked.
func (w *Writer) marked(prompt string, lanes int) bool {
	return w.Calls[prompt]*max(lanes, 1) >= 2
}

// writerTurn is one message Dispatch sends the writer about a case's text. lanes is the number of
// the process's lanes whose changes make this turn's calls for the case.
type writerTurn struct {
	kind    string
	text    string
	record  string
	session claude.SessionText
	message string
	lanes   int
}

// call makes the writer's call for prompt with the turn's inputs and its session, if any, in the
// writer's order. The record, which every call of a case sends the same, is the first block, as
// Shared; the writer's text and Dispatch's message, which change from turn to turn, go after it.
// The session part ends the turn, or, in the order writerSessionFirst, ends the record's block.
// A marked call marks the end of that block, so a later call of the case with the same prompt
// (change 3's next round, or another model's lane) reads it from the provider's cache.
func (w *Writer) call(ctx context.Context, prompt, purpose string, turn writerTurn, schema json.RawMessage) (llm.Response, error) {
	var b strings.Builder
	if err := writerPrompts.ExecuteTemplate(&b, prompt, writerData{
		Kind: turn.kind, Ask: turn.kind == "ask", HasSession: !turn.session.Empty(),
	}); err != nil {
		return llm.Response{}, fmt.Errorf("fill the %s prompt: %w", prompt, err)
	}
	req := llm.Request{
		Purpose: purpose, System: strings.TrimSpace(b.String()) + "\n",
		Shared: []string{strings.TrimSpace(turn.record) + "\n"}, Mark: w.marked(prompt, turn.lanes),
		User:   tag("your_text", turn.text) + tag("dispatch_message", turn.message),
		Schema: schema, MaxTokens: writerMaxTokens,
	}
	place := claude.SessionEndsUser
	if w.Order == writerSessionFirst {
		place = claude.SessionEndsShared
	}
	return w.Model.CallWithSession(ctx, req, turn.session, place)
}

// rewriteSchema is the schema of the writer's text written again.
var rewriteSchema = json.RawMessage(object(property{"text", stringSchema}))

// Rewrite is the writer's text written again after message: the written rules' refusal (change
// 2) or the writing check's refusal (change 7a).
func (w *Writer) Rewrite(ctx context.Context, purpose string, turn writerTurn) (string, Call, error) {
	resp, err := w.call(ctx, promptRewrite, purpose, turn, rewriteSchema)
	call := callOf(resp)
	if err != nil {
		return "", call, err
	}
	var reply struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(resp.Value, &reply); err != nil || strings.TrimSpace(reply.Text) == "" {
		return "", call, fmt.Errorf("the writer's %s reply holds no text: %s", purpose, resp.Value)
	}
	return strings.TrimSpace(reply.Text), call, nil
}

// The writer's actions on a review's findings (change 3).
const (
	actionEdit = "edit"
	actionPost = "post"
)

// findingsSchema is the schema of the writer's action on a review's findings.
var findingsSchema = json.RawMessage(object(
	property{numbered(0, "action"), `{"type":"string","enum":["edit","post"]}`},
	property{numbered(1, "text"), stringSchema},
	property{numbered(2, "reason"), stringSchema},
))

// FindingsAction is what the writer did with a review's findings: edited its text, or posted it as
// written with a reason.
type FindingsAction struct {
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ActOnFindings is the writer's action on the frame of a review's findings (reviewer.FindingsFrame).
func (w *Writer) ActOnFindings(ctx context.Context, turn writerTurn) (FindingsAction, Call, error) {
	resp, err := w.call(ctx, promptFindings, "writer:findings", turn, findingsSchema)
	call := callOf(resp)
	if err != nil {
		return FindingsAction{}, call, err
	}
	var reply map[string]string
	if err := json.Unmarshal(resp.Value, &reply); err != nil {
		return FindingsAction{}, call, fmt.Errorf("the writer's findings reply: %w", err)
	}
	act := FindingsAction{Action: reply[numbered(0, "action")], Text: strings.TrimSpace(reply[numbered(1, "text")]), Reason: strings.TrimSpace(reply[numbered(2, "reason")])}
	switch {
	case act.Action == actionEdit && act.Text == "":
		return act, call, fmt.Errorf("the writer edited its text to nothing: %s", resp.Value)
	case act.Action != actionEdit && act.Action != actionPost:
		return act, call, fmt.Errorf("the writer's action %q is neither edit nor post", act.Action)
	}
	return act, call, nil
}

// Answer is the writer's answer to Dispatch's side question about its text (changes 5 and 6):
// text the agent writes for the reader, which a model of any family may then read.
func (w *Writer) Answer(ctx context.Context, turn writerTurn) (string, Call, error) {
	resp, err := w.call(ctx, promptAnswer, "writer:answer", turn, nil)
	call := callOf(resp)
	if err != nil {
		return "", call, err
	}
	answer := strings.TrimSpace(resp.Text)
	if answer == "" {
		return "", call, fmt.Errorf("the writer gave an empty answer")
	}
	return answer, call, nil
}
