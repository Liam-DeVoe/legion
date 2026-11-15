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
	"strconv"
	"strings"
	"text/template"

	"github.com/sjawhar/envoy/cmd/writing-check/harness/claude"
	"github.com/sjawhar/envoy/cmd/writing-check/harness/llm"
	"github.com/sjawhar/envoy/internal/dispatch/writingcheck/plainwords"
)

// promptFiles are the reader's and the grader's prompts.
//
//go:embed prompts/*.md
var promptFiles embed.FS

var prompts = template.Must(template.New("prompts").ParseFS(promptFiles, "prompts/*.md"))

// promptData is what a system prompt is filled from.
type promptData struct {
	// FirstParagraph says the reader read only the text's first paragraph.
	FirstParagraph bool
	// Reply says the text is the agent's reply to the person's question (an answer case), not
	// a text the person asked a question about.
	Reply bool
	// HasSession says the user message ends with the writer's session part.
	HasSession bool
	// WriterAnswer says the user message holds the writer's answer to a side question.
	WriterAnswer bool
	// Phrases is the table of the phrases the owner asked agents to stop.
	Phrases string
}

// render is the template name filled from data.
func render(name string, data promptData) (string, error) {
	var b strings.Builder
	if err := prompts.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("fill the %s prompt: %w", name, err)
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}

// PromptsDigest is the SHA-256 of every prompt file, by name, with the candidate list and the
// price table: what a run's figures were made with.
func PromptsDigest() string {
	h := sha256.New()
	names, _ := fs.Glob(promptFiles, "prompts/*.md")
	slices.Sort(names)
	for _, name := range names {
		data, _ := promptFiles.ReadFile(name)
		fmt.Fprintf(h, "%s %d\n", name, len(data))
		h.Write(data)
	}
	for _, f := range [][]byte{candidatesFile, pricesFile} {
		fmt.Fprintf(h, "%d\n", len(f))
		h.Write(f)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// phrasesHeading heads the section of skills/plain-words that lists the phrases the owner asked
// agents to stop.
const phrasesHeading = "## The phrases the owner asked to stop"

// stoppedPhrases is that section's table, from the skill as package plainwords holds it.
func stoppedPhrases() string {
	_, rest, found := strings.Cut(plainwords.Skill, phrasesHeading+"\n")
	if !found {
		return ""
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	return strings.TrimSpace(section)
}

// Judgement is one judgement of R2 the grader gives, each by a prompt of its own.
type Judgement string

// The judgements.
const (
	// JAnswered is whether the fresh reader's answer answers the person's question.
	JAnswered Judgement = "answered"
	// JFirstParagraph is whether the answer of a fresh reader who read only the first paragraph
	// of an answer case's text answers the question.
	JFirstParagraph Judgement = "first_paragraph"
	// JLost counts the facts of the original text that the new text lost.
	JLost Judgement = "lost"
	// JAdded counts the facts of the new text that its inputs do not hold.
	JAdded Judgement = "added"
	// JPhrases counts the phrases of the text that state no fact.
	JPhrases Judgement = "phrases"
	// JUndefined counts the words and names that neither the text nor the record defines.
	JUndefined Judgement = "undefined"
	// JAnswerStart finds where an answer case's text first answers the question.
	JAnswerStart Judgement = "answer_start"
	// JHarder is whether the new text is harder to read than the original, for a plain case.
	JHarder Judgement = "harder"
	// JDefine is whether a definition answers the question, for Define.
	JDefine Judgement = "define"
)

// Judgements is every judgement, in the order the reports give them.
var Judgements = []Judgement{JAnswered, JFirstParagraph, JLost, JAdded, JPhrases, JUndefined, JAnswerStart, JHarder, JDefine}

// judgementSpec is how one judgement is asked and read. Each reply's fields come in the order the
// prompt asks for them, so the grader states what it found before its verdict.
type judgementSpec struct {
	// prompt is the template of its system prompt.
	prompt string
	// session says the call reads the writer's session part, where the case has one.
	session bool
	// list is the field of a judgement whose answer is a list, and its two item fields; empty
	// for a judgement whose answer is a verdict. itemChecks are an item's true-or-false fields
	// before its note: an item with any of them true is not a finding, and the harness drops it.
	list, itemQuote, itemNote string
	itemChecks                []string
	// verdict is the field of a judgement whose answer is true or false. notes are the
	// sentences the reply gives first, and checks its true-or-false fields after them: a check
	// that is true makes the verdict false, whatever the verdict field says.
	verdict string
	notes   []string
	checks  []string
}

// The checks of an answer: any of them true, and the answer does not answer.
var answerChecks = []string{"restates_question", "points_elsewhere", "other_question", "contradicts_record"}

var judgementSpecs = map[Judgement]judgementSpec{
	JAnswered:       {prompt: "grade-answered", session: true, verdict: "answers", notes: []string{"asked", "given"}, checks: answerChecks},
	JFirstParagraph: {prompt: "grade-first-paragraph", session: true, verdict: "answers", notes: []string{"asked", "given"}, checks: answerChecks},
	JLost:           {prompt: "grade-lost", list: "lost", itemQuote: "quote", itemNote: "fact"},
	JAdded:          {prompt: "grade-added", session: true, list: "added", itemQuote: "quote", itemNote: "fact"},
	JPhrases:        {prompt: "grade-phrases", list: "phrases", itemQuote: "quote", itemNote: "why"},
	JUndefined: {prompt: "grade-undefined", list: "undefined", itemQuote: "word", itemNote: "why",
		itemChecks: []string{"defined_in_text", "defined_in_record", "common_meaning"}},
	JAnswerStart: {prompt: "grade-answer-start"},
	JHarder:      {prompt: "grade-harder", verdict: "harder"},
	JDefine: {prompt: "grade-define", session: true, verdict: "answers", notes: []string{"asked", "given"},
		checks: []string{"restates_words", "points_elsewhere", "other_words", "contradicts_record"}},
}

// property is one property of an object schema.
type property struct {
	name   string
	schema string
}

const (
	stringSchema  = `{"type":"string"}`
	booleanSchema = `{"type":"boolean"}`
)

// object is the JSON schema of an object with properties, in their order, all required, and no
// other property.
func object(props ...property) string {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	names := make([]string, len(props))
	for i, p := range props {
		if i > 0 {
			b.WriteByte(',')
		}
		names[i] = p.name
		fmt.Fprintf(&b, "%q:%s", p.name, p.schema)
	}
	required, _ := json.Marshal(names)
	fmt.Fprintf(&b, `},"required":%s,"additionalProperties":false}`, required)
	return b.String()
}

// The provider writes an object's fields in the order of their names, whatever order a schema
// gives them in: with a verdict named "answers" before "reason", the grader gave its verdict
// first and its reason after, and the two did not agree (`harness grader-test`, 2026-11-15). So
// each field's name starts with its place in the order the prompt asks for: "1_asked", ...,
// "8_answers".

// numbered is name as the field at place i (from 0) of an object.
func numbered(i int, name string) string { return strconv.Itoa(i+1) + "_" + name }

// key is the numbered name of name among fields.
func key(fields []string, name string) string { return numbered(slices.Index(fields, name), name) }

// itemFields are the fields of a list judgement's item, in order; verdictFields those of a
// verdict judgement's reply; startFields those of answer_start's reply.
func (s judgementSpec) itemFields() []string {
	return append(append([]string{s.itemQuote}, s.itemChecks...), s.itemNote)
}

func (s judgementSpec) verdictFields() []string {
	return append(append(slices.Clone(s.notes), s.checks...), "reason", s.verdict)
}

var startFields = []string{"quote", "found"}

// schema is the JSON schema of the judgement's reply.
func (s judgementSpec) schema() json.RawMessage {
	props := func(fields []string, schema func(string) string) []property {
		out := make([]property, len(fields))
		for i, f := range fields {
			out[i] = property{numbered(i, f), schema(f)}
		}
		return out
	}
	switch {
	case s.list != "":
		item := props(s.itemFields(), func(f string) string {
			if slices.Contains(s.itemChecks, f) {
				return booleanSchema
			}
			return stringSchema
		})
		return json.RawMessage(object(property{s.list, `{"type":"array","items":` + object(item...) + `}`}))
	case s.verdict != "":
		return json.RawMessage(object(props(s.verdictFields(), func(f string) string {
			if slices.Contains(s.checks, f) || f == s.verdict {
				return booleanSchema
			}
			return stringSchema
		})...))
	}
	return json.RawMessage(object(props(startFields, func(f string) string {
		if f == "found" {
			return booleanSchema
		}
		return stringSchema
	})...))
}

// GradeInput is what one judgement reads. Each judgement reads its own fields of it.
type GradeInput struct {
	Judgement Judgement
	// Reply says the text is the agent's reply to the person's question: an answer case.
	Reply bool
	// Question is the person's question: answered, first_paragraph, answer_start and define.
	Question string
	// Answer is the fresh reader's answer: answered and first_paragraph.
	Answer string
	// Original is the text the agent wrote, and Text the text the person sees after the
	// change: lost, added and harder read both; phrases, undefined and answer_start read Text.
	Original, Text string
	// Definition is the definition the person sees: define.
	Definition string
	// WriterAnswer is the writer's answer to a side question, where a change asked one: added
	// and define.
	WriterAnswer string
	// Record is Dispatch's record as Dataset.RecordPart gives it: answered, first_paragraph,
	// added, undefined and define.
	Record string
	// Session is the writer's session part: answered, first_paragraph, added and define, where
	// the case has one.
	Session claude.SessionText
}

// tag is text in a tag of its own.
func tag(name, text string) string {
	return "<" + name + ">\n" + strings.TrimSpace(text) + "\n</" + name + ">\n"
}

// user is the judgement's user message, the session part aside.
func (in GradeInput) user() string {
	var b strings.Builder
	switch in.Judgement {
	case JAnswered, JFirstParagraph:
		b.WriteString(tag("question", in.Question))
		b.WriteString(tag("reader_answer", in.Answer))
		b.WriteString(in.Record)
	case JLost, JHarder:
		b.WriteString(tag("original_text", in.Original))
		b.WriteString(tag("new_text", in.Text))
	case JAdded:
		b.WriteString(tag("original_text", in.Original))
		b.WriteString(tag("new_text", in.Text))
		b.WriteString(in.Record)
		if in.WriterAnswer != "" {
			b.WriteString(tag("writer_answer", in.WriterAnswer))
		}
	case JPhrases:
		b.WriteString(tag("text", in.Text))
	case JUndefined:
		b.WriteString(tag("text", in.Text))
		b.WriteString(in.Record)
	case JAnswerStart:
		b.WriteString(tag("question", in.Question))
		b.WriteString(tag("text", in.Text))
	case JDefine:
		b.WriteString(tag("question", in.Question))
		b.WriteString(tag("definition", in.Definition))
		b.WriteString(in.Record)
		if in.WriterAnswer != "" {
			b.WriteString(tag("writer_answer", in.WriterAnswer))
		}
	}
	return b.String()
}

// Call is one model call as the outputs record it.
type Call struct {
	Model      string  `json:"model"`
	Served     string  `json:"served,omitempty"`
	Input      int     `json:"input_tokens"`
	Output     int     `json:"output_tokens"`
	CacheWrite int     `json:"cache_write_tokens,omitempty"`
	CacheRead  int     `json:"cache_read_tokens,omitempty"`
	CostUSD    float64 `json:"cost_usd"`
	Attempts   int     `json:"attempts"`
	Seconds    float64 `json:"seconds"`
}

func callOf(r llm.Response) Call {
	return Call{
		Model: r.Requested, Served: r.Served, Input: r.Usage.Input, Output: r.Usage.Output,
		CacheWrite: r.Usage.CacheWrite, CacheRead: r.Usage.CacheRead, CostUSD: r.Usage.CostUSD,
		Attempts: r.Attempts, Seconds: r.Elapsed.Seconds(),
	}
}

// Item is one item of a list judgement: its quote (a fact's words, a phrase, an undefined word)
// and its note (the fact, or why).
type Item struct {
	Quote string `json:"quote"`
	Note  string `json:"note"`
}

// Grade is one judgement of one text.
type Grade struct {
	// Verdict is answered, first_paragraph, harder and define's answer.
	Verdict *bool `json:"verdict,omitempty"`
	// Items and Count are a list judgement's answer.
	Items []Item `json:"items,omitempty"`
	Count int    `json:"count"`
	// Found, Quote and WordsBefore are answer_start's answer: whether a sentence answers, its
	// first words, and the words of the text before them (-1 where the quote is not found in
	// the text).
	Found       *bool  `json:"found,omitempty"`
	Quote       string `json:"quote,omitempty"`
	WordsBefore *int   `json:"words_before,omitempty"`
	// Reason is a verdict's reason, and Notes the sentences the reply gave before it (what the
	// question asks, what the answer gives).
	Reason string            `json:"reason,omitempty"`
	Notes  map[string]string `json:"notes,omitempty"`
	// Failed are the checks of a verdict judgement that the reply marked true, which make the
	// verdict false; Dropped counts the items of a list judgement the reply's own checks rule
	// out.
	Failed  []string `json:"failed,omitempty"`
	Dropped int      `json:"dropped,omitempty"`
	// ByConstruction marks a judgement the harness gives with no call: a change whose text is
	// the original loses and adds no fact.
	ByConstruction bool `json:"by_construction,omitempty"`
	// Session says the call read the writer's session part.
	Session bool   `json:"session,omitempty"`
	Call    *Call  `json:"call,omitempty"`
	Error   string `json:"error,omitempty"`
	// Reply is the grader's reply as it came, for an audit of the fields above.
	Reply json.RawMessage `json:"reply,omitempty"`
}

// graderMaxTokens bounds one grading reply, the grader's reasoning included.
const graderMaxTokens = 8192

// Grader gives the judgements, with the grading model of candidates.yaml.
type Grader struct {
	Model   *claude.Client
	phrases string
}

// NewGrader is the grader of model.
func NewGrader(model *claude.Client) *Grader {
	return &Grader{Model: model, phrases: stoppedPhrases()}
}

// Grade gives one judgement. A call that fails is a Grade with its Error.
func (g *Grader) Grade(ctx context.Context, in GradeInput) Grade {
	spec, ok := judgementSpecs[in.Judgement]
	if !ok {
		return Grade{Error: fmt.Sprintf("no judgement %q", in.Judgement)}
	}
	withSession := spec.session && !in.Session.Empty()
	system, err := render(spec.prompt, promptData{HasSession: withSession, Reply: in.Reply, WriterAnswer: in.WriterAnswer != "", Phrases: g.phrases})
	if err != nil {
		return Grade{Error: err.Error()}
	}
	req := llm.Request{Purpose: "grade:" + string(in.Judgement), System: system, User: in.user(), Schema: spec.schema(), MaxTokens: graderMaxTokens}
	var resp llm.Response
	if withSession {
		resp, err = g.Model.CallWithSession(ctx, req, in.Session)
	} else {
		resp, err = g.Model.Call(ctx, req)
	}
	call := callOf(resp)
	grade := Grade{Session: withSession, Call: &call, Reply: resp.Value}
	if err != nil {
		grade.Error = err.Error()
		return grade
	}
	if err := spec.read(resp.Value, &grade, in.Text); err != nil {
		grade.Error = fmt.Sprintf("the %s reply: %v", in.Judgement, err)
	}
	return grade
}

// read fills grade from a reply of the judgement's schema.
func (s judgementSpec) read(value json.RawMessage, grade *Grade, text string) error {
	var reply map[string]json.RawMessage
	if err := json.Unmarshal(value, &reply); err != nil {
		return err
	}
	switch {
	case s.list != "":
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(reply[s.list], &items); err != nil {
			return fmt.Errorf("%s: %w", s.list, err)
		}
		fields := s.itemFields()
	items:
		for _, it := range items {
			for _, check := range s.itemChecks {
				if b, _ := boolField(it, key(fields, check)); b {
					grade.Dropped++
					continue items
				}
			}
			var item Item
			_ = json.Unmarshal(it[key(fields, s.itemQuote)], &item.Quote)
			_ = json.Unmarshal(it[key(fields, s.itemNote)], &item.Note)
			grade.Items = append(grade.Items, item)
		}
		grade.Count = len(grade.Items)
	case s.verdict != "":
		fields := s.verdictFields()
		verdict, err := boolField(reply, key(fields, s.verdict))
		if err != nil {
			return err
		}
		for _, check := range s.checks {
			failed, err := boolField(reply, key(fields, check))
			if err != nil {
				return err
			}
			if failed {
				grade.Failed = append(grade.Failed, check)
				verdict = false
			}
		}
		for _, note := range s.notes {
			var text string
			if json.Unmarshal(reply[key(fields, note)], &text) == nil && text != "" {
				if grade.Notes == nil {
					grade.Notes = map[string]string{}
				}
				grade.Notes[note] = text
			}
		}
		_ = json.Unmarshal(reply[key(fields, "reason")], &grade.Reason)
		grade.Verdict = &verdict
	default:
		found, err := boolField(reply, key(startFields, "found"))
		if err != nil {
			return err
		}
		_ = json.Unmarshal(reply[key(startFields, "quote")], &grade.Quote)
		grade.Found = &found
		before := -1
		if found {
			before = WordsBefore(text, grade.Quote)
		}
		grade.WordsBefore = &before
	}
	return nil
}

// boolField is the true-or-false field name of a reply.
func boolField(reply map[string]json.RawMessage, name string) (bool, error) {
	var b bool
	if err := json.Unmarshal(reply[name], &b); err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	return b, nil
}

// Reader is the fresh reader of candidates.yaml: it reads only the text a person sees and the
// person's question. It never reads session text.
type Reader struct {
	Model *claude.Client
}

// Answer is the fresh reader's answer.
type Answer struct {
	Text  string `json:"text"`
	Call  Call   `json:"call"`
	Error string `json:"error,omitempty"`
}

// Read answers question from text alone. reply says text is the agent's reply to the question,
// and firstParagraph that text is only that reply's first paragraph.
func (r *Reader) Read(ctx context.Context, text, question string, reply, firstParagraph bool) Answer {
	system, err := render("reader", promptData{Reply: reply || firstParagraph, FirstParagraph: firstParagraph})
	if err != nil {
		return Answer{Error: err.Error()}
	}
	purpose := "reader"
	if firstParagraph {
		purpose = "reader:first_paragraph"
	}
	resp, err := r.Model.Call(ctx, llm.Request{Purpose: purpose, System: system, User: tag("text", text) + tag("question", question), MaxTokens: 1024})
	a := Answer{Text: strings.TrimSpace(resp.Text), Call: callOf(resp)}
	if err != nil {
		a.Error = err.Error()
	} else if a.Text == "" {
		a.Error = "the reader gave an empty answer"
	}
	return a
}
