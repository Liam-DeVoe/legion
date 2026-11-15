// Package wordrecords is INFRA-3231's R8 records of the words people ask agents about. A job of
// the server (Job) reads each text a person writes from the event log, after the write has
// committed, and asks a model whether the person asks what a word of an agent's text means, says
// they cannot understand the text, or complains about its words: the selection prompt
// (select.md). The dataset command (cmd/writing-check dataset classify) gives the dataset's texts
// their verdicts with the same prompt and the same user message (Input), so one definition of a
// question about words serves R1's dataset and R8's records.
package wordrecords

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// SelectPrompt is the selection prompt, the system prompt of every call. dataset/DATASET in
// cmd/writing-check records its SHA-256, which a change to the file breaks.
//
//go:embed select.md
var SelectPrompt string

// The verdicts of the selection prompt.
const (
	WordMeaning      = "asks_word_meaning"
	CannotUnderstand = "cannot_understand"
	JargonComplaint  = "jargon_complaint"
	AgentQuestion    = "asks_agent_question"
	None             = "none"
)

// Verdicts is every verdict, in the prompt's order.
var Verdicts = []string{WordMeaning, CannotUnderstand, JargonComplaint, AgentQuestion, None}

// AboutWords is whether a verdict is one of the three about words: the person says the words of
// the agent's text are the problem.
func AboutWords(verdict string) bool {
	return verdict == WordMeaning || verdict == CannotUnderstand || verdict == JargonComplaint
}

// VerdictSchema is the JSON schema of a reply: one verdict, and the words it is about.
var VerdictSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"verdict": {"type": "string", "enum": ["asks_word_meaning", "cannot_understand", "jargon_complaint", "asks_agent_question", "none"]},
		"words": {"type": "array", "items": {"type": "string"}}
	},
	"required": ["verdict", "words"],
	"additionalProperties": false
}`)

// How much of each part of a text the user message holds, in bytes.
const (
	AgentChars  = 12_000
	PersonChars = 8_000
	QuoteChars  = 2_000
)

// Text is one text of the user message: its kind (comment, message, ask, document, answer,
// resolution, review) and its words.
type Text struct {
	Kind string
	Text string
}

// Input is the user message of one call: the agent text the person's text replies to, left out
// where its words are empty; the part of a document the person's text points to, left out where it
// is empty; and the person's text. Each is cut at its bound (Clip).
func Input(agent Text, quote string, person Text) string {
	var b strings.Builder
	if strings.TrimSpace(agent.Text) != "" {
		fmt.Fprintf(&b, "<agent_text kind=%q>\n%s\n</agent_text>\n\n", agent.Kind, Clip(agent.Text, AgentChars))
	}
	if quote != "" {
		fmt.Fprintf(&b, "<quoted_part>\n%s\n</quoted_part>\n\n", Clip(quote, QuoteChars))
	}
	fmt.Fprintf(&b, "<person_text kind=%q>\n%s\n</person_text>\n", person.Kind, Clip(person.Text, PersonChars))
	return b.String()
}

// Clip is s cut to at most n bytes at a rune boundary, with a line that says how much was cut.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n[cut here: %d more characters]", utf8.RuneCountInString(s[cut:]))
}

// ParseVerdict reads a reply of VerdictSchema: its verdict, and its words, an empty list where it
// gives none. A reply that is not one verdict of the schema is an error that quotes it.
func ParseVerdict(value json.RawMessage) (verdict string, words []string, err error) {
	var reply struct {
		Verdict string   `json:"verdict"`
		Words   []string `json:"words"`
	}
	if err := json.Unmarshal(value, &reply); err != nil || !slices.Contains(Verdicts, reply.Verdict) {
		return "", nil, fmt.Errorf("the reply is not one verdict of the schema: %.300s", value)
	}
	if reply.Words == nil {
		reply.Words = []string{}
	}
	return reply.Verdict, reply.Words, nil
}
