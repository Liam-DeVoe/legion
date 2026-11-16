package harness

import (
	"cmp"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/sjawhar/envoy/agentloop"
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
	// HasQuestion, HasRecord and HasSession say the user message holds the person's question
	// (as context, where the judgement is not of an answer to it), Dispatch's record, and, at its
	// end, the writer's session part.
	HasQuestion, HasRecord, HasSession bool
	// WriterAnswer says the user message holds the writer's answer to a side question, and
	// HasOwnWords the words of the text that the reader wrote himself before it.
	WriterAnswer, HasOwnWords bool
	// Phrases is the table of the phrases skills/plain-words tells agents to stop.
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

// phrasesHeading heads the section of skills/plain-words that lists the phrases it tells agents to
// stop.
const phrasesHeading = "## The phrases to stop"

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
	// list is the field of a judgement whose answer is a list, and its two item fields; empty
	// for a judgement whose answer is a verdict. itemChecks are an item's true-or-false fields
	// before its note: an item with any of them true is not a finding, and the harness drops it.
	// inText says an item's quote is words of the text the person sees, so the harness drops an
	// item whose quote the text does not hold, and a second item of the same quote.
	list, itemQuote, itemNote string
	itemChecks                []string
	inText                    bool
	// sentences says the judgement is given sentence by sentence (phrases.go): its prompt judges
	// candidate sentences, and verdicts are the verdicts it gives, the first the one whose
	// sentences are findings.
	verdicts  []string
	sentences bool
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
	JAnswered:       {prompt: "grade-answered", verdict: "answers", notes: []string{"asked", "given"}, checks: answerChecks},
	JFirstParagraph: {prompt: "grade-first-paragraph", verdict: "answers", notes: []string{"asked", "given"}, checks: answerChecks},
	JLost: {prompt: "grade-lost", list: "lost", itemQuote: "quote", itemNote: "fact",
		itemChecks: []string{"states_no_fact", "in_new_text"}},
	JAdded: {prompt: "grade-added", list: "added", itemQuote: "quote", itemNote: "fact"},
	JPhrases: {prompt: "grade-phrases", list: "phrases", itemQuote: "quote", itemNote: "rewrite", sentences: true,
		verdicts: []string{"vacuous", "not_vacuous", "unresolved"}},
	JUndefined: {prompt: "grade-undefined", list: "undefined", itemQuote: "word", itemNote: "why", inText: true,
		itemChecks: []string{"defined_in_text", "defined_in_record", "common_meaning", "reference_only"}},
	JAnswerStart: {prompt: "grade-answer-start"},
	JHarder:      {prompt: "grade-harder", verdict: "harder"},
	JDefine: {prompt: "grade-define", verdict: "answers", notes: []string{"asked", "given"},
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

// GradeInput is what one judgement may read: its subject, and the case's context. The grader's
// input modes (GraderInputs) decide which context parts a call reads.
type GradeInput struct {
	Judgement Judgement
	// Reply says the text is the agent's reply to the person's question: an answer case.
	Reply bool
	// Question is the person's question, "" for a case with none.
	Question string
	// Answer is the fresh reader's answer: answered and first_paragraph.
	Answer string
	// Original is the text the agent wrote, and Text the text the person sees after the
	// change: lost, added and harder read both; phrases, undefined and answer_start read Text.
	Original, Text string
	// Definition is the definition the person sees: define.
	Definition string
	// WriterAnswer is the writer's answer to a side question, where a change asked one.
	WriterAnswer string
	// Record is Dispatch's record as Dataset.RecordPart gives it.
	Record string
	// Session is the writer's session part, where the case has one.
	Session claude.SessionText
	// OwnWords are the words of Text that the person wrote himself before the case's text
	// (score --vocabulary), which undefined reads in any input mode.
	OwnWords []string
}

// ownWords says the judgement reads the person's own words: undefined, where it has some.
func (in GradeInput) ownWords() bool { return in.Judgement == JUndefined && len(in.OwnWords) > 0 }

// tag is text in a tag of its own.
func tag(name, text string) string {
	return "<" + name + ">\n" + strings.TrimSpace(text) + "\n</" + name + ">\n"
}

// parts are the context a call reads: the parts c gives that the input has.
func (in GradeInput) parts(c contextParts) contextParts {
	return contextParts{
		Question:     c.Question && in.Question != "",
		Record:       c.Record && in.Record != "",
		Session:      c.Session && !in.Session.Empty(),
		WriterAnswer: c.WriterAnswer && in.WriterAnswer != "",
	}
}

// user is the judgement's user message, the session part aside: its subject, then the context
// parts p.
func (in GradeInput) user(p contextParts) string {
	var b strings.Builder
	if p.Question {
		b.WriteString(tag("question", in.Question))
	}
	switch in.Judgement {
	case JAnswered, JFirstParagraph:
		b.WriteString(tag("reader_answer", in.Answer))
	case JLost, JAdded, JHarder:
		b.WriteString(tag("original_text", in.Original))
		b.WriteString(tag("new_text", in.Text))
	case JPhrases, JUndefined, JAnswerStart:
		b.WriteString(tag("text", in.Text))
		if in.ownWords() {
			b.WriteString(tag("reader_own_words", "- "+strings.Join(in.OwnWords, "\n- ")))
		}
	case JDefine:
		b.WriteString(tag("definition", in.Definition))
	}
	b.WriteString(in.contextTags(p))
	return b.String()
}

// contextTags are the record and the writer's answer, as far as p reads them.
func (in GradeInput) contextTags(p contextParts) string {
	var b strings.Builder
	if p.Record {
		b.WriteString(in.Record)
	}
	if p.WriterAnswer {
		b.WriteString(tag("writer_answer", in.WriterAnswer))
	}
	return b.String()
}

// inputsFirst is lost's or added's user message under --added-grader inputs-first, the session
// part aside, and after, the part that follows the session part: the question, the record and
// the writer's answer, as far as p reads them; then, after the session part, the original text
// and, last, the new text.
func (in GradeInput) inputsFirst(p contextParts) (user, after string) {
	var b strings.Builder
	if p.Question {
		b.WriteString(tag("question", in.Question))
	}
	b.WriteString(in.contextTags(p))
	return b.String(), tag("original_text", in.Original) + tag("new_text", in.Text)
}

// Call is one model call as the outputs record it: its prompt tokens read at the input price
// (input_tokens), and those a provider reports written to its cache and read from it, priced at
// the model's prices in prices.yaml (cost_usd). SharedFrom names the model of a lockstep run
// (score --models) whose run made the call: the writer's answer to a side question that does
// not depend on the candidate, which this run took (sharedAnswers).
type Call struct {
	Model      string  `json:"model"`
	Served     string  `json:"served,omitempty"`
	Input      int     `json:"input_tokens"`
	Output     int     `json:"output_tokens"`
	CacheWrite int     `json:"cache_write_tokens"`
	CacheRead  int     `json:"cache_read_tokens"`
	CostUSD    float64 `json:"cost_usd"`
	Attempts   int     `json:"attempts"`
	Seconds    float64 `json:"seconds"`
	SharedFrom string  `json:"shared_from,omitempty"`
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
	// out, or that repeat a quote; NotInText the items whose quote the text does not hold.
	Failed    []string `json:"failed,omitempty"`
	Dropped   int      `json:"dropped,omitempty"`
	NotInText int      `json:"not_in_text,omitempty"`
	// Candidates and Unresolved are a sentence judgement's candidate sentences and those whose
	// verdict the grader could not settle.
	Candidates int `json:"candidates,omitempty"`
	Unresolved int `json:"unresolved,omitempty"`
	// ByConstruction marks a judgement the harness gives with no call: a change whose text is
	// the original loses and adds no fact.
	ByConstruction bool `json:"by_construction,omitempty"`
	// Inputs is the judgement's input mode, and Session says the call read the writer's
	// session part.
	Inputs  string `json:"inputs,omitempty"`
	Session bool   `json:"session,omitempty"`
	// Refused are the calls that ended on a refusal, the grader's first, where a fallback grader
	// then gave the grade (Call is the one that graded).
	Refused []Call `json:"refused,omitempty"`
	Call    *Call  `json:"call,omitempty"`
	Error   string `json:"error,omitempty"`
	// Reply is the grader's reply as it came, for an audit of the fields above.
	Reply json.RawMessage `json:"reply,omitempty"`
	// InputSHA256 is the SHA-256 of what the grade read (requestDigest): two grades of one
	// digest had the same inputs. Reused says the grade is another run's of the same inputs
	// (score --grades-from): its calls are that run's, and this run does not count them.
	InputSHA256 string `json:"input_sha256,omitempty"`
	Reused      bool   `json:"reused,omitempty"`
	// AddedGrader is the --added-grader variant that made the grade where it is not the old
	// request: lost and added under inputs-first, added under verify. Verify is verify's check
	// of an added grade's facts: Items are then the listed facts that the check kept.
	AddedGrader string        `json:"added_grader,omitempty"`
	Verify      *Verification `json:"verify,omitempty"`
}

// graderMaxTokens bounds one grading reply, the grader's reasoning included.
const graderMaxTokens = 8192

// Grader gives the judgements, with the grading model of candidates.yaml. Fallbacks, in order,
// grade a judgement that every model before them refuses on every try (llm's retries), and the
// grade names the model that graded it.
type Grader struct {
	Model     *claude.Client
	Fallbacks []*claude.Client
	Inputs    GraderInputs
	// Added is the added grader's variant (--added-grader): AddedOld, or "", sends the lost and
	// the added requests as they were.
	Added   string
	phrases string
}

// NewGrader is the grader of model and its fallbacks, reading the context inputs gives each
// judgement.
func NewGrader(model *claude.Client, fallbacks []*claude.Client, inputs GraderInputs) *Grader {
	return &Grader{Model: model, Fallbacks: fallbacks, Inputs: inputs, phrases: stoppedPhrases()}
}

// Grade gives one judgement. A call that fails is a Grade with its Error.
func (g *Grader) Grade(ctx context.Context, in GradeInput) Grade { return g.GradeOrReuse(ctx, in, nil) }

// GradeOrReuse gives one judgement, or, where one of done is a grade with no error whose inputs
// were the ones this grade reads (InputSHA256), that grade again, marked Reused, with no call.
// Under --added-grader verify, an added grade of done whose inputs were those of the first call
// is that call's grade (gradeVerified).
func (g *Grader) GradeOrReuse(ctx context.Context, in GradeInput, done []*Grade) Grade {
	spec, ok := judgementSpecs[in.Judgement]
	if !ok {
		return Grade{Error: fmt.Sprintf("no judgement %q", in.Judgement)}
	}
	r, err := g.request(spec, in)
	if err != nil {
		return Grade{Error: err.Error()}
	}
	variant := g.addedGrader(in.Judgement)
	digest := r.digest
	var check llm.Request
	if variant == AddedVerify {
		if check, err = checkRequest(r.data); err != nil {
			return Grade{Error: err.Error()}
		}
		digest = g.digest(r.req, r.session, "added_grader "+AddedVerify, check.System, string(check.Schema), strconv.Itoa(check.MaxTokens))
	}
	for _, d := range done {
		if d != nil && d.Error == "" && d.InputSHA256 == digest {
			again := *d
			again.Reused = true
			return again
		}
	}
	var grade Grade
	switch {
	case spec.sentences:
		grade = g.gradeSentences(ctx, spec, in, r.req)
	case variant == AddedVerify:
		grade = g.gradeVerified(ctx, spec, in, r, check, done)
	default:
		grade = g.gradeOnce(ctx, spec, in, r)
	}
	grade.InputSHA256 = digest
	if variant != AddedOld {
		grade.AddedGrader = variant
	}
	return grade
}

// addedGrader is the --added-grader variant that judgement j takes: the grader's for added, and
// for lost under inputs-first; AddedOld for every other judgement.
func (g *Grader) addedGrader(j Judgement) string {
	if (j == JAdded && g.Added != "") || (j == JLost && g.Added == AddedInputsFirst) {
		return g.Added
	}
	return AddedOld
}

// readsWriterAnswer is whether judgement j reads the writer's answer in its input mode, where the
// case has one. A sentence judgement reads its candidates alone.
func (g *Grader) readsWriterAnswer(j Judgement) bool {
	return !judgementSpecs[j].sentences && g.Inputs.context(j).WriterAnswer
}

// gradeRequest is one call of a judgement, ready to make: its request; the context parts it
// reads, and the data its system prompt was filled from; the session part it reads, empty for
// none; after, the part of the user message that follows the session part ("" where the session
// part ends it); and the SHA-256 of what it reads (requestDigest).
type gradeRequest struct {
	req     llm.Request
	parts   contextParts
	data    promptData
	session claude.SessionText
	after   string
	digest  string
}

// request is the judgement's call. A sentence judgement's User is the text, whose candidate
// sentences its calls read (gradeSentences).
func (g *Grader) request(spec judgementSpec, in GradeInput) (gradeRequest, error) {
	r := gradeRequest{req: llm.Request{Purpose: "grade:" + string(in.Judgement), MaxTokens: graderMaxTokens}}
	var err error
	if spec.sentences {
		r.req.User, r.req.Schema = in.Text, phraseSchema(spec.verdicts)
		r.req.System, err = render(spec.prompt, promptData{Phrases: g.phrases})
		r.digest = g.digest(r.req, r.session)
		return r, err
	}
	r.parts = in.parts(g.Inputs.context(in.Judgement))
	r.data = promptData{
		HasQuestion: r.parts.Question, HasRecord: r.parts.Record, HasSession: r.parts.Session, WriterAnswer: r.parts.WriterAnswer,
		HasOwnWords: in.ownWords(), Reply: in.Reply, Phrases: g.phrases,
	}
	if r.parts.Session {
		r.session = in.Session
	}
	r.req.System, err = render(spec.prompt, r.data)
	r.req.Schema = spec.schema()
	var extra []string
	if g.addedGrader(in.Judgement) == AddedInputsFirst {
		r.req.User, r.after = in.inputsFirst(r.parts)
		extra = []string{"added_grader " + AddedInputsFirst, r.after}
	} else {
		r.req.User = in.user(r.parts)
	}
	r.digest = g.digest(r.req, r.session, extra...)
	return r, err
}

// digest is the requestDigest of a call that the grader's models may make.
func (g *Grader) digest(req llm.Request, session claude.SessionText, extra ...string) string {
	return requestDigest(append([]*claude.Client{g.Model}, g.Fallbacks...), req, session, extra...)
}

// requestDigest is the SHA-256 of what a call reads: each model it may call, in order, with its
// effort; the request's system prompt, user message, schema and bound; extra, what else decides
// the call (an --added-grader variant's name and parts), none for the old request; and the
// session part, where the call reads one.
func requestDigest(models []*claude.Client, req llm.Request, session claude.SessionText, extra ...string) string {
	h := sha256.New()
	write := func(s string) { fmt.Fprintf(h, "%d:%s\n", len(s), s) }
	for _, m := range models {
		write(m.Name() + " " + m.Model().Effort)
	}
	for _, s := range append([]string{req.System, req.User, string(req.Schema), strconv.Itoa(req.MaxTokens)}, extra...) {
		write(s)
	}
	if !session.Empty() {
		d := session.Digest()
		h.Write(d[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// call makes r's call with the grader's model, then with each fallback while every model before
// it refuses, and answers the calls that ended on a refusal.
func (g *Grader) call(ctx context.Context, r gradeRequest) (llm.Response, []Call, error) {
	call := func(model *claude.Client) (llm.Response, error) {
		if !r.session.Empty() {
			return model.CallWithSession(ctx, r.req, r.session, r.after)
		}
		req := r.req
		req.User += r.after
		return model.Call(ctx, req)
	}
	resp, err := call(g.Model)
	var refused []Call
	for _, fallback := range g.Fallbacks {
		var callErr *llm.CallError
		if !errors.As(err, &callErr) || callErr.Fault != agentloop.FaultRefusal {
			break
		}
		refused = append(refused, callOf(resp))
		resp, err = call(fallback)
	}
	return resp, refused, err
}

// gradeOnce makes the judgement's call, then a fallback's while each model before it refuses.
func (g *Grader) gradeOnce(ctx context.Context, spec judgementSpec, in GradeInput, r gradeRequest) Grade {
	resp, refused, err := g.call(ctx, r)
	grade := Grade{Inputs: cmp.Or(g.Inputs[in.Judgement], InputsTrimmed), Session: r.parts.Session, Refused: refused}
	c := callOf(resp)
	grade.Call, grade.Reply = &c, resp.Value
	if err != nil {
		grade.Error = err.Error()
		return grade
	}
	if err := spec.read(resp.Value, &grade, in.Text); err != nil {
		grade.Error = fmt.Sprintf("the %s reply: %v", in.Judgement, err)
		return grade
	}
	return grade
}

// ownCalls are the grade's calls that its run made: none for a grade it took from another run
// (Reused); else the first call and its refused calls, unless its run took that call's grade
// (Verification.FirstReused), and the check's calls under --added-grader verify.
func (g *Grade) ownCalls() []Call {
	if g.Reused {
		return nil
	}
	var out []Call
	if g.Verify == nil || !g.Verify.FirstReused {
		out = append(out, g.Refused...)
		if g.Call != nil && g.Call.Model != "" {
			out = append(out, *g.Call)
		}
	}
	if v := g.Verify; v != nil {
		out = append(out, v.Refused...)
		if v.Call != nil && v.Call.Model != "" {
			out = append(out, *v.Call)
		}
	}
	return out
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
		seen := map[string]bool{}
		plain := claude.QuoteForm(text)
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
			if s.inText {
				q := claude.QuoteForm(item.Quote)
				switch {
				case q == "" || !strings.Contains(plain, q):
					grade.NotInText++
					continue items
				case seen[q]:
					grade.Dropped++
					continue items
				}
				seen[q] = true
			}
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
	// InputSHA256 is the SHA-256 of what the reader read (requestDigest). Reused says the
	// answer is another run's (score --readers-from, or --grades-from for the same inputs): its
	// call is that run's, and this run does not count it.
	InputSHA256 string `json:"input_sha256,omitempty"`
	Reused      bool   `json:"reused,omitempty"`
}

// readerCall is one call of the fresh reader, ready to make: its request, and the SHA-256 of what
// it reads (requestDigest), which its answer records as InputSHA256. err is a prompt that did
// not fill.
type readerCall struct {
	model  *claude.Client
	req    llm.Request
	digest string
	err    error
}

// call is the call that answers question from text alone. reply says text is the agent's reply
// to the question, and firstParagraph that text is only that reply's first paragraph.
func (r *Reader) call(text, question string, reply, firstParagraph bool) readerCall {
	system, err := render("reader", promptData{Reply: reply || firstParagraph, FirstParagraph: firstParagraph})
	if err != nil {
		return readerCall{err: err}
	}
	purpose := "reader"
	if firstParagraph {
		purpose = "reader:first_paragraph"
	}
	req := llm.Request{Purpose: purpose, System: system, User: tag("text", text) + tag("question", question), MaxTokens: 1024}
	return readerCall{model: r.Model, req: req, digest: requestDigest([]*claude.Client{r.Model}, req, claude.SessionText{})}
}

// reused is the answer of done with no error to the call's same inputs (InputSHA256), marked
// Reused, which a case takes in place of the call; nil where done holds none.
func (c readerCall) reused(done []*Answer) *Answer {
	if c.err != nil {
		return nil
	}
	for _, d := range done {
		if d != nil && d.Error == "" && d.InputSHA256 == c.digest {
			again := *d
			again.Reused = true
			return &again
		}
	}
	return nil
}

// answer makes the call.
func (c readerCall) answer(ctx context.Context) Answer {
	if c.err != nil {
		return Answer{Error: c.err.Error()}
	}
	resp, err := c.model.Call(ctx, c.req)
	a := Answer{Text: strings.TrimSpace(resp.Text), Call: callOf(resp), InputSHA256: c.digest}
	if err != nil {
		a.Error = err.Error()
	} else if a.Text == "" {
		a.Error = "the reader gave an empty answer"
	}
	return a
}
