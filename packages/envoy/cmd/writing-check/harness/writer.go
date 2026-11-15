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

// Writer is the writer agent the harness plays (plan Decision 20): the Claude model of
// candidates.yaml's writer role, given Dispatch's record of the case, its own text, the message
// Dispatch sent it, and its session up to the text where the case has one. Only a claude.Client
// takes the session (claude.SessionText).
type Writer struct {
	Model *claude.Client
}

// writerTurn is one message Dispatch sends the writer about a case's text.
type writerTurn struct {
	kind    string
	text    string
	record  string
	session claude.SessionText
	message string
}

// call makes the writer's call for prompt with the turn's inputs and its session, if any.
func (w *Writer) call(ctx context.Context, prompt, purpose string, turn writerTurn, schema json.RawMessage) (llm.Response, error) {
	var b strings.Builder
	if err := writerPrompts.ExecuteTemplate(&b, prompt, writerData{
		Kind: turn.kind, Ask: turn.kind == "ask", HasSession: !turn.session.Empty(),
	}); err != nil {
		return llm.Response{}, fmt.Errorf("fill the %s prompt: %w", prompt, err)
	}
	user := strings.TrimSpace(turn.record) + "\n" + tag("your_text", turn.text) + tag("dispatch_message", turn.message)
	req := llm.Request{Purpose: purpose, System: strings.TrimSpace(b.String()) + "\n", User: user, Schema: schema, MaxTokens: writerMaxTokens}
	return w.Model.CallWithSession(ctx, req, turn.session)
}

// rewriteSchema is the schema of the writer's text written again.
var rewriteSchema = json.RawMessage(object(property{"text", stringSchema}))

// Rewrite is the writer's text written again after message: the written rules' refusal (change
// 2) or the writing check's refusal (change 7a).
func (w *Writer) Rewrite(ctx context.Context, purpose string, turn writerTurn) (string, Call, error) {
	resp, err := w.call(ctx, "writer-rewrite", purpose, turn, rewriteSchema)
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
	resp, err := w.call(ctx, "writer-findings", "writer:findings", turn, findingsSchema)
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
	resp, err := w.call(ctx, "writer-answer", "writer:answer", turn, nil)
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
