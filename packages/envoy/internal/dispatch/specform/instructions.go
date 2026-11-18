package specform

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// maxProcedureWords is R49's bound on a sentence of the procedure of a check, which ASD-STE100
// writes as an instruction: an instruction has at most 20 words, where a description has 25.
const maxProcedureWords = 20

// instructionQuote is how many words of the second instruction a finding of the rule instructions
// quotes, as the rule sentence-words quotes the first words of its sentence.
const instructionQuote = 8

// codeWord starts and ends the word that stands for a span in code font among the words that the
// rule instructions reads, so that a command in code font is the object of an instruction, as in
// "Run `go test` and read the log" (codeAsWords).
const codeWord = "\uE000"

// wordSet is a set of words in lower case.
func wordSet(words ...string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[word] = true
	}
	return set
}

var (
	// conditions open a condition at the start of a sentence, which a comma ends: the instruction
	// starts after that comma.
	conditions = wordSet("if", "when", "after", "before", "once", "unless", "until", "while")
	// objectStarts start the object of an instruction: an article, a word of quantity, a
	// possessive, a demonstrative, a pronoun, or a span in code font.
	objectStarts = wordSet("the", "a", "an", "each", "every", "all", "any", "both", "no", "its", "their",
		"this", "these", "those", "it", "them", codeWord)
	// relatives open a relative clause, whose verbs give no instruction.
	relatives = wordSet("that", "which", "who", "whom", "whose")
	// joiners join a second instruction to the first, as a comma does.
	joiners = wordSet("and", "or", "then")
	// closedWords are no verb: articles, words of quantity, pronouns, prepositions, conjunctions,
	// auxiliary verbs, numbers, adverbs and question words.
	closedWords = wordSet(
		"the", "a", "an", "each", "every", "all", "any", "both", "no", "some", "most", "many", "few", "more", "less", "other", "another",
		"its", "their", "his", "her", "our", "your", "my", "this", "these", "those", "that", "which", "who", "whom", "whose", "what",
		"it", "they", "them", "we", "us", "you", "he", "him", "she", "i", "me", "there", "here",
		"of", "in", "on", "at", "to", "from", "for", "with", "by", "as", "into", "onto", "over", "under", "about", "against",
		"between", "through", "without", "within", "beside", "after", "before", "during", "since", "per", "via", "than",
		"inside", "outside", "around", "across", "along", "among", "behind", "below", "above", "beyond", "near", "off", "out", "up",
		"down", "toward", "towards", "upon", "throughout", "except", "like", "unlike", "despite", "beneath",
		"and", "or", "but", "nor", "so", "if", "when", "where", "while", "once", "unless", "until", "because", "then",
		"whether", "how", "why", "whatever", "whichever", "wherever", "whenever",
		"is", "are", "was", "were", "be", "been", "am", "do", "does", "did", "has", "have", "had", "can", "could", "may", "might",
		"must", "shall", "should", "will", "would", "not", "also", "only", "too", "very", "just", "still", "even", "first",
		"never", "always", "often", "sometimes", "usually", "again", "now", "later", "soon", "rather", "instead", "else", "already",
		"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve")
)

// instructionBreaks is a finding of the rule instructions for each sentence of a procedure that
// gives two instructions (secondInstruction). A span in code font counts as one word of the
// sentence, where the rules that count words read none.
func instructionBreaks(line string) []broken {
	var found []broken
	coded, spans := codeAsWords(line)
	for _, sentence := range sentencesOf(coded) {
		second, ok := secondInstruction(sentence)
		if !ok {
			continue
		}
		quoted := strings.Join(sentence[second:min(len(sentence), second+instructionQuote)], " ")
		found = append(found, broken{rule: ruleInstructions, detail: fmt.Sprintf(
			"a sentence gives two instructions, and a sentence of a procedure gives one: %q. Write each instruction in a sentence of its own",
			strings.TrimRight(spans.Replace(quoted), ".!?"))})
	}
	return found
}

// codeAsWords is line with each span in code font, as ste-lint cuts one ("`" to the next "`"),
// written as one word that codeWord starts and ends, and the replacer that writes each such word
// back as its span.
func codeAsWords(line string) (string, *strings.Replacer) {
	var written strings.Builder
	var spans []string
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			break
		}
		end := strings.IndexByte(line[start+1:], '`')
		if end < 0 {
			break
		}
		word := fmt.Sprintf("%s%d%s", codeWord, len(spans)/2, codeWord)
		spans = append(spans, word, line[start:start+1+end+1])
		written.WriteString(line[:start])
		written.WriteString(word)
		line = line[start+1+end+1:]
	}
	written.WriteString(line)
	return written.String(), strings.NewReplacer(spans...)
}

// secondInstruction is where the second instruction of sentence starts, when it gives two. A
// sentence gives an instruction when it starts with a verb in its base form, with no comma after
// it, and the first word of the verb's object (objectStarts); after a condition at its start
// (conditions), the instruction starts after the comma that ends the condition. A second
// instruction is the same pair after a word that joins it (joiners) or a comma, once the first
// instruction's object stands between them. So two verbs of one object, as in "Push and hold the
// switch", are one instruction, as ASD-STE100 lets two actions at the same time be. A verb after a
// relative clause is no instruction: in "the requests that open and close the same day" the verbs
// are the clause's.
func secondInstruction(sentence []string) (int, bool) {
	words := make([]string, len(sentence))
	commas := make([]bool, len(sentence))
	for index, word := range sentence {
		words[index], commas[index] = wordCore(word)
	}
	start := 0
	if len(words) > 0 && conditions[words[0]] {
		start = slices.Index(commas, true) + 1
		if start == 0 {
			return 0, false
		}
	}
	instruction := func(at int) bool {
		return at+1 < len(words) && baseForm(words[at]) && !commas[at] && objectStarts[words[at+1]]
	}
	if !instruction(start) {
		return 0, false
	}
	for at := start + 2; at+1 < len(words); at++ {
		if relatives[words[at-1]] {
			return 0, false
		}
		if (commas[at-1] || joiners[words[at-1]]) && instruction(at) {
			return at, true
		}
	}
	return 0, false
}

// wordCore is word in lower case without the marks that open or close it and the stops and
// commas after it, and whether a comma ends it. A span in code font is codeWord.
func wordCore(word string) (string, bool) {
	word = strings.TrimLeft(word, openingMarks)
	word = strings.TrimRight(word, closingMarks+".!?:;")
	comma := strings.HasSuffix(word, ",")
	word = strings.TrimRight(word, closingMarks+",")
	if strings.HasPrefix(word, codeWord) {
		return codeWord, comma
	}
	return strings.ToLower(word), comma
}

// baseForm reports whether word, in lower case, can be a verb in its base form: a word of letters
// alone, no word of closedWords, and no ending of another form, -s, -ed or -ing, but for a word
// that ends in -ss, -us or -is, such as "process" or "focus".
func baseForm(word string) bool {
	if len(word) < 2 || closedWords[word] || strings.ContainsFunc(word, func(r rune) bool { return !unicode.IsLetter(r) }) {
		return false
	}
	switch {
	case strings.HasSuffix(word, "ing"), strings.HasSuffix(word, "ed"):
		return false
	case strings.HasSuffix(word, "s"):
		return strings.HasSuffix(word, "ss") || strings.HasSuffix(word, "us") || strings.HasSuffix(word, "is")
	}
	return true
}
