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
	"sync"
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
	// (as context, where the judgement is not of an answer to it), Dispatch's record, and the
	// writer's session part.
	HasQuestion, HasRecord, HasSession bool
	// WriterAnswer says the user message holds the writer's answer to a side question, and
	// HasOwnWords the words of the text that the reader wrote himself before it.
	WriterAnswer, HasOwnWords bool
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

// shared is the start of a judgement's user turn: the context parts p, in the order the user
// message has always held them (the question, the record, the writer's answer), as one block, or
// none where p holds none of them. The session part, where the call reads one, ends that block
// (claude.SessionEndsShared), and the subject comes after it (subject). So the calls of one
// judgement of one case send the same prefix whatever text they grade, such as the calls of a
// lockstep's models, and where two or more of them do (Grader.Plan), each call after the first
// reads it from the provider's cache. Before the cache marks, the subject came after the
// question; it moved to the end of the turn so that the prefix can hold the session part.
func (in GradeInput) shared(p contextParts) []string {
	var b strings.Builder
	if p.Question {
		b.WriteString(tag("question", in.Question))
	}
	if p.Record {
		b.WriteString(in.Record)
	}
	if p.WriterAnswer {
		b.WriteString(tag("writer_answer", in.WriterAnswer))
	}
	if b.Len() == 0 {
		return nil
	}
	return []string{b.String()}
}

// subject is what the judgement grades, which ends its call's user turn: the fresh reader's
// answer, the original and the new text, the text with the person's own words, or the
// definition.
func (in GradeInput) subject() string {
	var b strings.Builder
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
	return b.String()
}

// Call is one model call as the outputs record it: its prompt tokens read at the input price
// (input_tokens), written to the provider's cache and read from it, priced at the model's
// prices in prices.yaml (cost_usd). Marked and Unmarked count the model calls it holds that the
// harness sent with the cache mark and with none (llm.Request's Mark): one for most calls, one
// for each chunk of a phrases judgement. A candidate call, whose marks the local Dispatch, the
// reviewer component or the detector's reader set, is in neither. SharedFrom names the model of
// a lockstep run (score --models) whose run made the call: the writer's answer to a side
// question that does not depend on the candidate, which this run took (sharedAnswers).
type Call struct {
	Model      string  `json:"model"`
	Served     string  `json:"served,omitempty"`
	Input      int     `json:"input_tokens"`
	Output     int     `json:"output_tokens"`
	CacheWrite int     `json:"cache_write_tokens"`
	CacheRead  int     `json:"cache_read_tokens"`
	CostUSD    float64 `json:"cost_usd"`
	Attempts   int     `json:"attempts"`
	Marked     int     `json:"marked_calls,omitempty"`
	Unmarked   int     `json:"unmarked_calls,omitempty"`
	Seconds    float64 `json:"seconds"`
	SharedFrom string  `json:"shared_from,omitempty"`
}

func callOf(r llm.Response) Call {
	c := Call{
		Model: r.Requested, Served: r.Served, Input: r.Usage.Input, Output: r.Usage.Output,
		CacheWrite: r.Usage.CacheWrite, CacheRead: r.Usage.CacheRead, CostUSD: r.Usage.CostUSD,
		Attempts: r.Attempts, Seconds: r.Elapsed.Seconds(),
	}
	if r.Marked {
		c.Marked = 1
	} else {
		c.Unmarked = 1
	}
	return c
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
	phrases   string
	// planned counts, for each prefix (prefixKey), the calls of Model that the run's plans
	// hold that send it (Plan). mu guards it.
	mu      sync.Mutex
	planned map[string]int
	// prefixes holds, for each marked prefix a call of Model sent, a channel that closes when
	// the first call that sent it ends.
	prefixes sync.Map
}

// NewGrader is the grader of model and its fallbacks, reading the context inputs gives each
// judgement.
func NewGrader(model *claude.Client, fallbacks []*claude.Client, inputs GraderInputs) *Grader {
	return &Grader{Model: model, Fallbacks: fallbacks, Inputs: inputs, phrases: stoppedPhrases(), planned: map[string]int{}}
}

// Grade gives one judgement. A call that fails is a Grade with its Error.
func (g *Grader) Grade(ctx context.Context, in GradeInput) Grade { return g.GradeOrReuse(ctx, in, nil) }

// gradeCall is a judgement's call as the grader makes it: the judgement's spec, its request, the
// context parts it reads, and the session part where it reads one.
type gradeCall struct {
	spec    judgementSpec
	req     llm.Request
	parts   contextParts
	session claude.SessionText
}

// prepare is the call of the judgement in.
func (g *Grader) prepare(in GradeInput) (gradeCall, error) {
	spec, ok := judgementSpecs[in.Judgement]
	if !ok {
		return gradeCall{}, fmt.Errorf("no judgement %q", in.Judgement)
	}
	req, p, err := g.request(spec, in)
	if err != nil {
		return gradeCall{}, err
	}
	c := gradeCall{spec: spec, req: req, parts: p}
	if p.Session {
		c.session = in.Session
	}
	return c, nil
}

// digest is the SHA-256 of what the call reads (requestDigest), with Model and its fallbacks.
func (g *Grader) digest(c gradeCall) string {
	return requestDigest(append([]*claude.Client{g.Model}, g.Fallbacks...), c.req, c.session)
}

// reuse is the grade of done with no error whose inputs were the ones of digest (InputSHA256),
// marked Reused; nil where done holds none.
func reuse(done []*Grade, digest string) *Grade {
	for _, d := range done {
		if d != nil && d.Error == "" && d.InputSHA256 == digest {
			again := *d
			again.Reused = true
			return &again
		}
	}
	return nil
}

// GradeOrReuse gives one judgement, or, where one of done is a grade with no error whose inputs
// were the ones this grade reads (InputSHA256), that grade again, marked Reused, with no call.
func (g *Grader) GradeOrReuse(ctx context.Context, in GradeInput, done []*Grade) Grade {
	c, err := g.prepare(in)
	if err != nil {
		return Grade{Error: err.Error()}
	}
	digest := g.digest(c)
	if again := reuse(done, digest); again != nil {
		return *again
	}
	var grade Grade
	if c.spec.sentences {
		grade = g.gradeSentences(ctx, c.spec, in, c.req)
	} else {
		grade = g.gradeOnce(ctx, c, in)
	}
	grade.InputSHA256 = digest
	return grade
}

// Plan adds to the run's plan the calls of Model that the judgements ins will make: one for each
// judgement, one for each chunk of a phrases judgement's candidates (phraseCalls), and none for
// a judgement whose grade one of done gives again (GradeOrReuse). A judgement of a fresh
// reader's answer whose Answer is "", since the reader answers later in the run, counts as one
// call: only that answer could match a grade of done. A caller plans the judgements it knows of
// before the first of them is sent: score each case's, grader-test and phrases-labels all of
// theirs. A call of Model carries the cache mark only where the plan holds two or more calls of
// its prefix when it is sent (mark), so a prefix that one call alone sends pays the input price
// and not the cache write.
func (g *Grader) Plan(ins []GradeInput, done map[Judgement][]*Grade) {
	type count struct {
		key   string
		calls int
	}
	var counts []count
	for _, in := range ins {
		c, err := g.prepare(in)
		if err != nil {
			continue
		}
		// A grade of done is taken again only for the same digest, which a judgement of an
		// answer still to come cannot have, and which needs no hashing where done holds none.
		answerLater := in.Answer == "" && (in.Judgement == JAnswered || in.Judgement == JFirstParagraph)
		if prior := done[in.Judgement]; !answerLater && len(prior) > 0 && reuse(prior, g.digest(c)) != nil {
			continue
		}
		calls := 1
		if c.spec.sentences {
			calls = phraseCalls(len(phraseCandidates(in.Text)))
		}
		if key := prefixKey(g.Model, c.req, c.session); key != "" && calls > 0 {
			counts = append(counts, count{key, calls})
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range counts {
		g.planned[c.key] += c.calls
	}
}

// request is the judgement's request and the context parts it reads: the judgement's own system
// prompt and reply schema, and a user turn of the context parts (GradeInput.shared), to which the
// call adds the session part where it reads one (gradeOnce), then the subject
// (GradeInput.subject). A sentence judgement's system prompt is its prompt, which is the same for
// every text, and its Shared opens the tag of its candidates (candidatesOpen); its User is the
// text, whose candidate sentences its calls send in its place (gradeSentences).
func (g *Grader) request(spec judgementSpec, in GradeInput) (llm.Request, contextParts, error) {
	req := llm.Request{Purpose: "grade:" + string(in.Judgement), MaxTokens: graderMaxTokens}
	var p contextParts
	var err error
	if spec.sentences {
		req.Shared, req.User, req.Schema = []string{candidatesOpen}, in.Text, phraseSchema(spec.verdicts)
		req.System, err = render(spec.prompt, promptData{Phrases: g.phrases})
		return req, p, err
	}
	p = in.parts(g.Inputs.context(in.Judgement))
	data := promptData{
		HasQuestion: p.Question, HasRecord: p.Record, HasSession: p.Session, WriterAnswer: p.WriterAnswer,
		HasOwnWords: in.ownWords(), Reply: in.Reply, Phrases: g.phrases,
	}
	req.System, err = render(spec.prompt, data)
	req.Shared, req.User, req.Schema = in.shared(p), in.subject(), spec.schema()
	return req, p, err
}

// requestDigest is the SHA-256 of what a call reads: each model it may call, in order, with its
// effort; the request's system prompt, Shared blocks, user message, schema and bound; and the
// session part, where the call reads one. The request's Mark is not in it: the mark changes
// whether the provider caches the prefix, and not what the model reads.
func requestDigest(models []*claude.Client, req llm.Request, session claude.SessionText) string {
	h := sha256.New()
	write := func(s string) { fmt.Fprintf(h, "%d:%s\n", len(s), s) }
	for _, m := range models {
		write(m.Name() + " " + m.Model().Effort)
	}
	write(req.System)
	write(strconv.Itoa(len(req.Shared)))
	for _, s := range req.Shared {
		write(s)
	}
	for _, s := range []string{req.User, string(req.Schema), strconv.Itoa(req.MaxTokens)} {
		write(s)
	}
	if !session.Empty() {
		d := session.Digest()
		h.Write(d[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// prefixKey is the SHA-256 of the part of model's request that the provider caches: the model
// with its effort, the system prompt, the schema (the provider puts it before the messages), the
// Shared blocks and the session part. It is "" for a call that sends neither Shared nor a
// session part, which has no prefix to mark.
func prefixKey(model *claude.Client, req llm.Request, session claude.SessionText) string {
	if len(req.Shared) == 0 && session.Empty() {
		return ""
	}
	h := sha256.New()
	write := func(s string) { fmt.Fprintf(h, "%d:%s\n", len(s), s) }
	write(model.Name() + " " + model.Model().Effort)
	write(req.System)
	write(string(req.Schema))
	for _, s := range req.Shared {
		write(s)
	}
	if !session.Empty() {
		d := session.Digest()
		h.Write(d[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// mark sets the cache mark of req, a call of Model with the prefix key, where the run's plan
// holds two or more calls of that prefix (Plan). It orders the marked calls of one prefix: the
// first goes at once, and each other waits until it has ended, so it reads the prefix from the
// provider's cache instead of writing it again. The provider can serve a cached prefix only once
// a call that wrote it has begun its reply, so calls that start together would each write it.
// mark answers the function that the call calls when it has ended. An unmarked call waits for
// none: it writes nothing to the cache and reads nothing from it.
func (g *Grader) mark(ctx context.Context, req *llm.Request, key string) (done func()) {
	if key == "" {
		return func() {}
	}
	g.mu.Lock()
	req.Mark = g.planned[key] >= 2
	g.mu.Unlock()
	if !req.Mark {
		return func() {}
	}
	mine := make(chan struct{})
	prior, loaded := g.prefixes.LoadOrStore(key, mine)
	if !loaded {
		return func() { close(mine) }
	}
	select {
	case <-prior.(chan struct{}):
	case <-ctx.Done():
	}
	return func() {}
}

// gradeOnce makes the judgement's call, then a fallback's while each model before it refuses. A
// fallback's call carries no cache mark: no plan counts it.
func (g *Grader) gradeOnce(ctx context.Context, c gradeCall, in GradeInput) Grade {
	call := func(model *claude.Client, req llm.Request) (llm.Response, error) {
		if c.parts.Session {
			return model.CallWithSession(ctx, req, in.Session, claude.SessionEndsShared)
		}
		return model.Call(ctx, req)
	}
	req := c.req
	done := g.mark(ctx, &req, prefixKey(g.Model, c.req, c.session))
	resp, err := call(g.Model, req)
	done()
	grade := Grade{Inputs: cmp.Or(g.Inputs[in.Judgement], InputsTrimmed), Session: c.parts.Session}
	for _, fallback := range g.Fallbacks {
		var callErr *llm.CallError
		if !errors.As(err, &callErr) || callErr.Fault != agentloop.FaultRefusal {
			break
		}
		grade.Refused = append(grade.Refused, callOf(resp))
		resp, err = call(fallback, c.req)
	}
	made := callOf(resp)
	grade.Call, grade.Reply = &made, resp.Value
	if err != nil {
		grade.Error = err.Error()
		return grade
	}
	if err := c.spec.read(resp.Value, &grade, in.Text); err != nil {
		grade.Error = fmt.Sprintf("the %s reply: %v", in.Judgement, err)
		return grade
	}
	return grade
}

// quoteForm is text as a quote is looked up in it: lowercased, without Markdown's emphasis and
// code marks, with typographic quotes made plain and each run of white space one space.
func quoteForm(text string) string {
	text = strings.NewReplacer("*", "", "`", "", "\u2019", "'", "\u2018", "'", "\u201c", "\"", "\u201d", "\"").Replace(strings.ToLower(text))
	return strings.Join(strings.Fields(text), " ")
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
		plain := quoteForm(text)
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
				q := quoteForm(item.Quote)
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
