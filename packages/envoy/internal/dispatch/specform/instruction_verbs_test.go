package specform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// The program that writes the list of the verbs of an instruction (instruction-verbs.txt) and its
// evidence (testdata/instruction-verbs.json), and the test that holds both to it. Run with
// -write-verbs, it reads the skills of the repository (skills/*/SKILL.md) at -verbs-commit and
// the procedures of the live Verification document of The Spec's root in -verbs-live (a
// `dispatch doc-read` of it), keeps those procedures in testdata/live-procedures.txt, and writes
// each word that opens an instruction sentence of them (openerVerb). Run with no flag, it fails
// when the list is not what the evidence gives, and, where the clone holds the source commit,
// when the evidence is not what the commit and the kept procedures give, so an edit of either file
// by hand fails. The command is the list's header.
var (
	writeVerbs  = flag.Bool("write-verbs", false, "write instruction-verbs.txt and its evidence from -verbs-commit and -verbs-live")
	verbsCommit = flag.String("verbs-commit", "", "the commit whose skills/*/SKILL.md the list of verbs is read from")
	verbsLive   = flag.String("verbs-live", "", "a `dispatch doc-read` of the Verification document whose procedures the list reads")
)

const (
	verbsListFile     = "instruction-verbs.txt"
	verbsEvidenceFile = "testdata/instruction-verbs.json"
	verbsLiveFile     = "testdata/live-procedures.txt"
	verbsRepository   = "../../../../.."
)

// verbEvidence is the list's evidence: the source commit and the command, each source with its
// digest and the sentences of it that open an instruction, and one sentence for each listed verb
// that a source also uses as a noun (after a determiner).
type verbEvidence struct {
	Commit  string        `json:"commit"`
	Command string        `json:"command"`
	Sources []verbSource  `json:"sources"`
	Verbs   []string      `json:"verbs"`
	Nouns   []verbOpening `json:"also_nouns"`
}

type verbSource struct {
	Path      string        `json:"path"`
	SHA256    string        `json:"sha256"`
	Sentences int           `json:"sentences"`
	Openers   []verbOpening `json:"openers"`
}

type verbOpening struct {
	Word     string `json:"word"`
	Line     int    `json:"line"`
	Sentence string `json:"sentence"`
}

// notVerbs are words that can open a sentence and stand before an object start and are no verb:
// the closed classes of English (articles, quantities, pronouns, prepositions, conjunctions,
// auxiliaries, numbers, adverbs and question words). The program reads a verb only where a sentence
// opens with a word outside them; the rule reads only the words the program lists.
var notVerbs = wordSet(
	"the", "a", "an", "each", "every", "all", "any", "both", "no", "some", "most", "many", "few", "more", "less", "other", "another",
	"its", "their", "his", "her", "our", "your", "my", "this", "these", "those", "that", "which", "who", "whom", "whose", "what",
	"it", "they", "them", "we", "us", "you", "he", "him", "she", "i", "me", "there", "here",
	"nobody", "anybody", "somebody", "everybody", "nothing", "anything", "something", "everything",
	"nowhere", "anywhere", "somewhere", "everywhere", "either", "neither", "otherwise",
	"of", "in", "on", "at", "to", "from", "for", "with", "by", "as", "into", "onto", "over", "under", "about", "against",
	"between", "through", "without", "within", "beside", "after", "before", "during", "since", "per", "via", "than", "past",
	"inside", "outside", "around", "across", "along", "among", "behind", "below", "above", "beyond", "near", "off", "out", "up",
	"down", "toward", "towards", "upon", "throughout", "except", "like", "unlike", "despite", "beneath",
	"and", "or", "but", "nor", "so", "if", "when", "where", "while", "once", "unless", "until", "because", "then",
	"whether", "how", "why", "whatever", "whichever", "wherever", "whenever",
	"is", "are", "was", "were", "be", "been", "am", "do", "does", "did", "has", "have", "had", "can", "could", "may", "might",
	"must", "shall", "should", "will", "would", "not", "also", "only", "too", "very", "just", "still", "even", "first",
	"never", "always", "often", "sometimes", "usually", "again", "now", "later", "soon", "rather", "instead", "else", "already",
	"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve")

// determiners stand before a noun, so a listed verb after one is a noun there.
var determiners = wordSet("the", "a", "an", "each", "every", "its", "their", "this", "these", "those", "any", "no", "your", "our")

// openerVerb is the verb that opens the sentence of raw (each word as the line writes it) as an
// instruction, when it does: its first word starts with a capital letter, is a word of letters
// alone with no mark after it, is in no closed class (notVerbs) and in no form of another person
// or time (-s, -ed, -ing), and stands before an object start that opens no clause of its own
// (contactClause), not inside brackets.
func openerVerb(raw []string) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	first := strings.Trim(raw[0], "*_")
	if first == "" || !unicode.IsUpper([]rune(first)[0]) || strings.ContainsFunc(first, func(r rune) bool { return !unicode.IsLetter(r) }) ||
		strings.HasPrefix(strings.TrimLeft(raw[1], "*_"), "(") {
		return "", false
	}
	trimmed := make([]string, len(raw))
	for index, word := range raw {
		trimmed[index] = strings.Trim(word, "*_")
	}
	words, commas := sentenceWords(trimmed)
	verb := words[0]
	if notVerbs[verb] || commas[0] || !objectStarts[words[1]] || contactClause(words, commas, 0) ||
		strings.HasSuffix(verb, "ing") || strings.HasSuffix(verb, "ed") ||
		(strings.HasSuffix(verb, "s") && !strings.HasSuffix(verb, "ss") && !strings.HasSuffix(verb, "us") && !strings.HasSuffix(verb, "is")) {
		return "", false
	}
	return verb, true
}

var (
	listMarker = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)]|#+|>)\s+`)
	fence      = regexp.MustCompile("^\\s*(```|~~~)")
)

// markdownSentences is each sentence of a Markdown file, with the line it starts on: the lines of
// a paragraph or of a list item are read as one, a heading, a list item, a quote and a table row
// each start one of their own, and a fenced block is read as no prose.
func markdownSentences(markdown string) []verbOpening {
	var found []verbOpening
	var unit strings.Builder
	start := 0
	flush := func() {
		coded, spans := codeAsWords(strings.TrimSpace(unit.String()))
		for _, sentence := range sentencesOf(coded) {
			found = append(found, verbOpening{Line: start, Sentence: spans.Replace(strings.Join(sentence, " "))})
		}
		unit.Reset()
	}
	fenced := false
	for number, line := range strings.Split(markdown, "\n") {
		if fence.MatchString(line) {
			flush()
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		blank := strings.TrimSpace(line) == ""
		marker := listMarker.MatchString(line) || strings.HasPrefix(strings.TrimSpace(line), "|")
		if blank || marker {
			flush()
		}
		if blank {
			continue
		}
		if unit.Len() == 0 {
			start = number + 1
		}
		for listMarker.MatchString(line) {
			line = listMarker.ReplaceAllString(line, "")
		}
		unit.WriteString(" " + strings.TrimSpace(line))
	}
	flush()
	return found
}

// sentenceVerb is the opener of one sentence as the evidence keeps it, its spans of code font
// written as one word again, as the program read it.
func sentenceVerb(sentence string) (string, bool) {
	coded, _ := codeAsWords(sentence)
	return openerVerb(strings.Fields(coded))
}

// readVerbSources reads every source at commit (and the kept live procedures) and gives the
// evidence and the list it writes.
func readVerbSources(t *testing.T, commit string) verbEvidence {
	t.Helper()
	git := func(args ...string) []byte {
		out, err := exec.Command("git", append([]string{"-C", verbsRepository}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return out
	}
	var paths []string
	for _, path := range strings.Fields(string(git("ls-tree", "-r", "--name-only", commit, "--", "skills"))) {
		if matched, _ := filepath.Match("skills/*/SKILL.md", path); matched {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		t.Fatalf("UNKNOWN: commit %s holds no skills/*/SKILL.md", commit)
	}
	texts := map[string][]byte{}
	for _, path := range paths {
		texts[path] = git("show", commit+":"+path)
	}
	live, err := os.ReadFile(verbsLiveFile)
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, verbsLiveFile)
	texts[verbsLiveFile] = live
	evidence := verbEvidence{Commit: commit, Command: verbsCommand(commit)}
	verbs := map[string]bool{}
	var every []verbOpening
	for _, path := range paths {
		digest := sha256.Sum256(texts[path])
		source := verbSource{Path: path, SHA256: hex.EncodeToString(digest[:]), Openers: []verbOpening{}}
		var sentences []verbOpening
		if path == verbsLiveFile {
			for number, procedure := range strings.Split(strings.TrimSpace(string(live)), "\n") {
				for _, sentence := range markdownSentences(procedure) {
					sentences = append(sentences, verbOpening{Line: number + 1, Sentence: sentence.Sentence})
				}
			}
		} else {
			sentences = markdownSentences(string(texts[path]))
		}
		source.Sentences = len(sentences)
		for _, sentence := range sentences {
			sentence.Word = path
			every = append(every, sentence)
			if verb, ok := sentenceVerb(sentence.Sentence); ok {
				source.Openers = append(source.Openers, verbOpening{Word: verb, Line: sentence.Line, Sentence: sentence.Sentence})
				verbs[verb] = true
			}
		}
		evidence.Sources = append(evidence.Sources, source)
	}
	for verb := range verbs {
		evidence.Verbs = append(evidence.Verbs, verb)
	}
	slices.Sort(evidence.Verbs)
	nouns := map[string]bool{}
	for _, sentence := range every {
		if word, ok := nounUse(sentence.Sentence, verbs); ok && !nouns[word] {
			nouns[word] = true
			evidence.Nouns = append(evidence.Nouns, verbOpening{Word: word, Line: sentence.Line, Sentence: sentence.Word + ": " + sentence.Sentence})
		}
	}
	slices.SortFunc(evidence.Nouns, func(a, b verbOpening) int { return strings.Compare(a.Word, b.Word) })
	return evidence
}

// nounUse is the first listed verb of sentence that stands right after a determiner, where it is a
// noun: "the record", "its name".
func nounUse(sentence string, verbs map[string]bool) (string, bool) {
	coded, _ := codeAsWords(sentence)
	words, _ := sentenceWords(strings.Fields(coded))
	for index := 1; index < len(words); index++ {
		if determiners[words[index-1]] && verbs[words[index]] {
			return words[index], true
		}
	}
	return "", false
}

func verbsCommand(commit string) string {
	return fmt.Sprintf("go test ./internal/dispatch/specform/ -run '^TestTheVerbListIsItsProgramsOutput$' -args -write-verbs -verbs-commit %s -verbs-live <a dispatch doc-read of the Verification document of The Spec's root>", commit)
}

// renderVerbs is the list file of evidence.
func renderVerbs(evidence verbEvidence) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "# The verbs of an instruction (R49.2): each word that opens an instruction sentence of the sources, one a line.\n")
	fmt.Fprintf(&out, "# Written by its program (instruction_verbs_test.go); do not edit. From packages/envoy:\n#   %s\n", evidence.Command)
	fmt.Fprintf(&out, "# Sources: skills/*/SKILL.md at %s, and the procedures of %s. Evidence: %s.\n", evidence.Commit, verbsLiveFile, verbsEvidenceFile)
	for _, verb := range evidence.Verbs {
		out.WriteString(verb + "\n")
	}
	return out.Bytes()
}

func renderEvidence(t *testing.T, evidence verbEvidence) []byte {
	t.Helper()
	out, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// writeLiveProcedures keeps the procedures of the live Verification document, one a line.
func writeLiveProcedures(t *testing.T, path string) {
	t.Helper()
	markdown, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ExtractDocument("verification.md", 1, string(markdown))
	if err != nil {
		t.Fatal(err)
	}
	var procedures []string
	for _, text := range read.Texts {
		if text.Kind == KindProcedure {
			procedures = append(procedures, strings.Join(strings.Fields(text.Body), " "))
		}
	}
	if len(procedures) == 0 {
		t.Fatalf("UNKNOWN: %s holds no procedure", path)
	}
	if err := os.WriteFile(verbsLiveFile, []byte(strings.Join(procedures, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheVerbListIsItsProgramsOutput(t *testing.T) {
	if *writeVerbs {
		if *verbsCommit == "" || *verbsLive == "" {
			t.Fatal("-write-verbs needs -verbs-commit and -verbs-live")
		}
		writeLiveProcedures(t, *verbsLive)
		evidence := readVerbSources(t, *verbsCommit)
		if err := os.WriteFile(verbsEvidenceFile, renderEvidence(t, evidence), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(verbsListFile, renderVerbs(evidence), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(verbsEvidenceFile)
	if err != nil {
		t.Fatal(err)
	}
	var evidence verbEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	// The list is what the evidence gives: each opener's sentence opens with its verb, and the
	// list is those verbs.
	verbs := map[string]bool{}
	for _, source := range evidence.Sources {
		for _, opener := range source.Openers {
			if verb, ok := sentenceVerb(opener.Sentence); !ok || verb != opener.Word {
				t.Errorf("%s:%d: %q opens with %q, %v, and the evidence says %q", source.Path, opener.Line, opener.Sentence, verb, ok, opener.Word)
			}
			verbs[opener.Word] = true
		}
	}
	if got := renderVerbs(evidence); !bytes.Equal([]byte(instructionVerbsFile), got) {
		t.Errorf("%s is not its program's output from %s: regenerate it with %s", verbsListFile, verbsEvidenceFile, evidence.Command)
	}
	if len(verbs) != len(evidence.Verbs) || len(instructionVerbs) != len(evidence.Verbs) {
		t.Errorf("the evidence's openers give %d verbs, its list %d, the rule reads %d", len(verbs), len(evidence.Verbs), len(instructionVerbs))
	}
	for _, noun := range evidence.Nouns {
		_, sentence, _ := strings.Cut(noun.Sentence, ": ")
		if word, ok := nounUse(sentence, map[string]bool{noun.Word: true}); !ok || word != noun.Word || !instructionVerbs[noun.Word] {
			t.Errorf("%q is no listed verb after a determiner in %q", noun.Word, noun.Sentence)
		}
	}
	// Where this clone holds the source commit, the evidence is what the commit and the kept live
	// procedures give, byte for byte.
	if exec.Command("git", "-C", verbsRepository, "cat-file", "-e", evidence.Commit+"^{commit}").Run() != nil {
		t.Logf("UNKNOWN: this clone does not hold %s, so the evidence is held to its own sentences only", evidence.Commit)
		return
	}
	if got := renderEvidence(t, readVerbSources(t, evidence.Commit)); !bytes.Equal(raw, got) {
		t.Errorf("%s is not its program's output from %s: regenerate it with %s", verbsEvidenceFile, evidence.Commit, evidence.Command)
	}
}
