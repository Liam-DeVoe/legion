package corpustest_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/corpustest"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// corpusVariable is the variable these cases hand the helper their directory in. It is built at
// run time, so no reader of the tree takes it for a gate of these tests.
var corpusVariable = strings.Join([]string{"CORPUSTEST", "CASE", "DIRECTORY"}, "_")

// rules are the rules of a corpus test whose parse reads the rendering back (docs'
// TestWriteCorpusReadsOnce): pmdoc's size limit, and the two refusals of that parse that fail the
// test, a rendering that reads back otherwise and one no reader reads.
var rules = corpustest.Rules{MaxBytes: pmdoc.MaxDocumentBytes, Failures: []error{pmdoc.ErrMisreadRendering, pmdoc.ErrRenderingRefused}}

// recorder is the testing.TB a case hands the helper: it keeps what the helper logs, reports and
// registers, and ends the case's goroutine where FailNow or SkipNow would end a test's. A method
// the helper is not expected to call reaches the nil TB and panics.
type recorder struct {
	testing.TB
	mu       sync.Mutex
	logs     []string
	errors   []string
	fatal    string
	skipped  string
	cleanups []func()
}

func (r *recorder) Helper() {}

func (r *recorder) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.mu.Lock()
	r.fatal = fmt.Sprintf(format, args...)
	r.mu.Unlock()
	runtime.Goexit()
}

func (r *recorder) Skipf(format string, args ...any) {
	r.mu.Lock()
	r.skipped = fmt.Sprintf(format, args...)
	r.mu.Unlock()
	runtime.Goexit()
}

func (r *recorder) Cleanup(cleanup func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanups = append(r.cleanups, cleanup)
}

func (r *recorder) failed() bool { return len(r.errors) > 0 || r.fatal != "" }

func (r *recorder) log() string { return strings.Join(r.logs, "\n") }

// run runs body as a test function against a recorder, then its cleanups, last registered first,
// each on a goroutine of its own as a test's are, and returns the recorder.
func run(body func(t testing.TB)) *recorder {
	r := &recorder{}
	inGoroutine := func(f func()) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			f()
		}()
		<-done
	}
	inGoroutine(func() { body(r) })
	for index := len(r.cleanups) - 1; index >= 0; index-- {
		inGoroutine(r.cleanups[index])
	}
	return r
}

// corpus writes documents, file name to text, into a new directory and points corpusVariable at it.
func corpus(t *testing.T, documents map[string]string) string {
	t.Helper()
	directory := t.TempDir()
	for name, text := range documents {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(corpusVariable, directory)
	return directory
}

// parseEach hands every document of the corpus to its parse: the error parse holds for a
// document's name is the error the test's parse returns for it.
func parseEach(c *corpustest.Corpus, parse map[string]error) []string {
	var taken []string
	for _, document := range c.Documents() {
		if c.Parse(document, func() error { return parse[document.Name] }) {
			taken = append(taken, document.Name)
		}
	}
	return taken
}

func requirePassed(t *testing.T, r *recorder) {
	t.Helper()
	if r.failed() || r.skipped != "" {
		t.Fatalf("the corpus test failed or skipped: errors %q, fatal %q, skipped %q; log:\n%s", r.errors, r.fatal, r.skipped, r.log())
	}
}

func requireFailed(t *testing.T, r *recorder, want string) {
	t.Helper()
	got := strings.Join(append(slices.Clone(r.errors), r.fatal), "\n")
	if !r.failed() || !strings.Contains(got, want) {
		t.Fatalf("the corpus test's failures are %q, want one naming %q; log:\n%s", got, want, r.log())
	}
}

func requireLogged(t *testing.T, r *recorder, want string) {
	t.Helper()
	if !strings.Contains(r.log(), want) {
		t.Fatalf("the log does not say %q:\n%s", want, r.log())
	}
}

// A document of pmdoc.MaxDocumentBytes plus one byte is left out by its size alone: the test's
// parse call never sees it, and the corpus passes on the documents it takes.
func TestCorpusLeavesOutADocumentOverTheSizeLimit(t *testing.T) {
	corpus(t, map[string]string{
		"large.md": strings.Repeat("a", pmdoc.MaxDocumentBytes+1),
		"limit.md": strings.Repeat("b", pmdoc.MaxDocumentBytes),
		"small.md": "# A small document\n",
	})
	var taken []string
	r := run(func(t testing.TB) {
		taken = parseEach(corpustest.Open(t, corpusVariable, rules), map[string]error{"large.md": errors.New("parsed a document over the size limit")})
	})
	requirePassed(t, r)
	if want := []string{"limit.md", "small.md"}; !slices.Equal(taken, want) {
		t.Fatalf("took %v, want %v", taken, want)
	}
	requireLogged(t, r, fmt.Sprintf("left out large.md: %d bytes, more than the %d a document may hold", pmdoc.MaxDocumentBytes+1, pmdoc.MaxDocumentBytes))
	requireLogged(t, r, "2 taken, 1 left out (1 over the size limit), 0 failed")
}

// A document the parse refuses is left out under its refusal, whatever the refusal is - pmdoc's
// named bounds, an element this build does not know, any other refusal - but the two that still
// fail; the log names each document with its error, and counts the refusals of one kind together,
// a kind being the error's text before its first quote with its digits read as N.
func TestCorpusLeavesOutADocumentItsParseRefuses(t *testing.T) {
	corpus(t, map[string]string{
		"elements.md": "many elements",
		"padding.md":  "| a | b |\n| - | - |\n| c |",
		"html.md":     "<div>block</div>",
		"html-12.md":  "<section>block</section>",
		"unknown.md":  ":::mystery\nText.\n:::",
		"other.md":    "Text no rule takes.",
		"plain.md":    "Plain text.",
	})
	parse := map[string]error{
		"elements.md": pmdoc.ErrTooManyElements,
		"padding.md":  fmt.Errorf("line 3: %w", pmdoc.ErrTablePadding),
		"html.md":     fmt.Errorf("%w: line 1, %q", pmdoc.ErrBlockHTML, "<div>block</div>"),
		"html-12.md":  fmt.Errorf("%w: line 12, %q", pmdoc.ErrBlockHTML, "<section>block</section>"),
		"unknown.md":  fmt.Errorf("%w: unknown typed block %q", pmdoc.ErrUnknownElement, "mystery"),
		"other.md":    errors.New("a refusal no rule names"),
	}
	var taken []string
	r := run(func(t testing.TB) { taken = parseEach(corpustest.Open(t, corpusVariable, rules), parse) })
	requirePassed(t, r)
	if want := []string{"plain.md"}; !slices.Equal(taken, want) {
		t.Fatalf("took %v, want %v", taken, want)
	}
	for name, err := range parse {
		requireLogged(t, r, fmt.Sprintf("left out %s: the parse refused it: %v", name, err))
	}
	requireLogged(t, r, "1 taken, 6 left out (6 refused by the parse), 0 failed")
	for kind, count := range map[string]int{
		pmdoc.ErrTooManyElements.Error():                          1,
		"line N: " + pmdoc.ErrTablePadding.Error():                1,
		pmdoc.ErrBlockHTML.Error() + ": line N":                   2,
		pmdoc.ErrUnknownElement.Error() + ": unknown typed block": 1,
		"a refusal no rule names":                                 1,
	} {
		requireLogged(t, r, fmt.Sprintf("refused by the parse with %q: %d", kind, count))
	}
}

// The parse's refusals that still fail a corpus test - a rendering that reads back as another
// document, one no reader reads - and a panic of the parse fail it, and the document is not taken;
// the other documents still are.
func TestCorpusFailsOnTheParsesFailuresAndOnAPanic(t *testing.T) {
	for name, parse := range map[string]func() error{
		"misread":   func() error { return fmt.Errorf("store: %w", pmdoc.ErrMisreadRendering) },
		"refused":   func() error { return pmdoc.ErrRenderingRefused },
		"panicking": func() error { panic("index out of range") },
	} {
		t.Run(name, func(t *testing.T) {
			corpus(t, map[string]string{"refused.md": "Refused.", "taken.md": "Taken."})
			var taken []string
			r := run(func(t testing.TB) {
				c := corpustest.Open(t, corpusVariable, rules)
				for _, document := range c.Documents() {
					step := func() error { return nil }
					if document.Name == "refused.md" {
						step = parse
					}
					if c.Parse(document, step) {
						taken = append(taken, document.Name)
					}
				}
			})
			want := "refused.md: the parse refused it with "
			if name == "panicking" {
				want = "refused.md: the parse panicked: index out of range"
			}
			requireFailed(t, r, want)
			if want := []string{"taken.md"}; !slices.Equal(taken, want) {
				t.Fatalf("took %v, want %v", taken, want)
			}
			requireLogged(t, r, "1 taken, 0 left out, 1 failed")
		})
	}
}

// An error of a step after the parse - a write, a read, a render - on a document the parse took
// fails the test, whatever it is, even one the parse may leave a document out for; no error keeps
// the document.
func TestCorpusFailsOnAnErrorOfALaterStep(t *testing.T) {
	corpus(t, map[string]string{"written.md": "Written.", "kept.md": "Kept."})
	var kept []string
	r := run(func(t testing.TB) {
		c := corpustest.Open(t, corpusVariable, rules)
		for _, document := range c.Documents() {
			if !c.Parse(document, func() error { return nil }) {
				continue
			}
			var err error
			if document.Name == "written.md" {
				err = fmt.Errorf("write the document: %w", pmdoc.ErrTooManyElements)
			}
			if c.Later(document, err) {
				kept = append(kept, document.Name)
			}
		}
	})
	requireFailed(t, r, "written.md: a step after the parse refused it: write the document: "+pmdoc.ErrTooManyElements.Error())
	if want := []string{"kept.md"}; !slices.Equal(kept, want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	requireLogged(t, r, "1 taken, 0 left out, 1 failed")
}

// sentences is count sentences of the text a test takes quotes from, each one there once.
func sentences(count int) []string {
	out := make([]string, count)
	for index := range out {
		out[index] = fmt.Sprintf("Sentence number %d of the document says one thing only.", index)
	}
	return out
}

// onceOf is a test's choice of quotes: every candidate the helper finds once in the text, with
// need, how many the test needs.
func onceOf(candidates []string, need int) func(once func(string) bool) ([]string, int) {
	return func(once func(string) bool) ([]string, int) {
		var picked []string
		for _, candidate := range candidates {
			if once(candidate) {
				picked = append(picked, candidate)
			}
		}
		return picked, need
	}
}

// A document whose text holds 97 sentences once and one twice, for a test that needs 98 once, is
// left out, counted in the text and not by the quote finder, which it never calls; one more
// sentence once and the document is taken, each of its 98 candidates found once.
func TestCorpusLeavesOutADocumentShortOfOnceOccurringCandidates(t *testing.T) {
	short := sentences(97)
	twice := "This sentence stands in the document twice and is no candidate."
	shortText := strings.Join(append(slices.Clone(short), twice, twice), "\n")
	enough := sentences(98)
	enoughText := strings.Join(enough, "\n")
	corpus(t, map[string]string{"short.md": shortText, "enough.md": enoughText})
	found := map[string]int{}
	var taken []string
	r := run(func(t testing.TB) {
		c := corpustest.Open(t, corpusVariable, rules)
		for _, document := range c.Documents() {
			candidates := append(slices.Clone(short), twice)
			if document.Name == "enough.md" {
				candidates = enough
			}
			once, ok := c.Quotes(document, corpustest.Quotes{
				Text: document.Markdown,
				Pick: onceOf(candidates, 98),
				Find: func(quote string) error {
					found[document.Name]++
					return nil
				},
			})
			if ok {
				taken = append(taken, document.Name)
				if len(once) != 98 {
					t.Errorf("%s: %d once-occurring candidates handed back, want 98", document.Name, len(once))
				}
			}
		}
	})
	requirePassed(t, r)
	if want := []string{"enough.md"}; !slices.Equal(taken, want) {
		t.Fatalf("took %v, want %v", taken, want)
	}
	if found["short.md"] != 0 || found["enough.md"] != 98 {
		t.Fatalf("the quote finder ran %d times on short.md and %d on enough.md, want 0 and 98", found["short.md"], found["enough.md"])
	}
	requireLogged(t, r, "left out short.md: 97 once-occurring candidates, and the test needs 98")
	requireLogged(t, r, "1 taken, 1 left out (1 short of candidates), 0 failed")
}

// A quote finder's error on a candidate that occurs once in the text fails the test, naming where
// the candidate stands; an error on a candidate that occurs twice is never asked for.
func TestCorpusFailsOnAQuoteFinderErrorOnAOnceOccurringCandidate(t *testing.T) {
	candidates := sentences(4)
	twice := "This sentence stands in the document twice and is no candidate."
	text := strings.Join(append(slices.Clone(candidates), twice, twice), "\n")
	corpus(t, map[string]string{"spec.md": text})
	missed := errors.New("target not found")
	var asked []string
	r := run(func(t testing.TB) {
		c := corpustest.Open(t, corpusVariable, rules)
		for _, document := range c.Documents() {
			if _, ok := c.Quotes(document, corpustest.Quotes{
				Text: text,
				Pick: onceOf(append(slices.Clone(candidates), twice), 3),
				Find: func(quote string) error {
					asked = append(asked, quote)
					if quote == candidates[2] || quote == twice {
						return missed
					}
					return nil
				},
			}); ok {
				t.Errorf("took %s, whose candidate the finder missed", document.Name)
			}
		}
	})
	at := strings.Index(text, candidates[2])
	requireFailed(t, r, fmt.Sprintf("spec.md: the quote finder refused candidate 3, %d bytes at byte %d of the text, which holds it once: target not found", len(candidates[2]), at))
	if slices.Contains(asked, twice) {
		t.Fatalf("the finder was asked for a candidate the text holds twice")
	}
	requireLogged(t, r, "0 taken, 0 left out, 1 failed")
}

// A corpus test fails when it takes no document: a directory with no markdown in it, and one
// whose every document is left out.
func TestCorpusFailsWhenItTakesNoDocument(t *testing.T) {
	t.Run("no markdown", func(t *testing.T) {
		directory := corpus(t, map[string]string{"notes.txt": "Not markdown."})
		r := run(func(t testing.TB) { parseEach(corpustest.Open(t, corpusVariable, rules), nil) })
		requireFailed(t, r, fmt.Sprintf("%s=%s holds no *.md document", corpusVariable, directory))
	})
	t.Run("every document left out", func(t *testing.T) {
		corpus(t, map[string]string{"html.md": "<div>x</div>", "large.md": strings.Repeat("a", pmdoc.MaxDocumentBytes+1)})
		r := run(func(t testing.TB) {
			parseEach(corpustest.Open(t, corpusVariable, rules), map[string]error{"html.md": pmdoc.ErrBlockHTML})
		})
		requireFailed(t, r, "took none of its 2 documents")
	})
}

// shardNames are file names as CI writes them, each path's / as __.
func shardNames() []string {
	var names []string
	for _, directory := range []string{"docs", "docs__site", "packages__envoy", "skills__legion-worker", ""} {
		for index := range 12 {
			name := fmt.Sprintf("guide-%02d.md", index)
			if directory != "" {
				name = directory + "__" + name
			}
			names = append(names, name)
		}
	}
	return names
}

// For each count of shards from 1 to 8, every document falls in exactly one shard: the shards'
// documents together are the corpus, none twice, each with the index it has in the whole corpus,
// and InShard answers each name as the shards do.
func TestCorpusShardsHoldEachDocumentOnce(t *testing.T) {
	names := shardNames()
	documents := map[string]string{}
	for _, name := range names {
		documents[name] = "Text of " + name
	}
	corpus(t, documents)
	slices.Sort(names)
	for shards := 1; shards <= 8; shards++ {
		seen := map[string]int{}
		for shard := 1; shard <= shards; shard++ {
			t.Setenv("CORPUS_SHARD", fmt.Sprintf("%d/%d", shard, shards))
			var inShard []string
			r := run(func(t testing.TB) {
				for _, name := range names {
					if corpustest.InShard(t, name) {
						inShard = append(inShard, name)
					}
				}
			})
			requirePassed(t, r)
			var held []string
			r = run(func(t testing.TB) {
				c := corpustest.Open(t, corpusVariable, rules)
				for _, document := range c.Documents() {
					held = append(held, document.Name)
					seen[document.Name]++
					if names[document.Index] != document.Name {
						t.Errorf("%s has index %d, which is %s in the whole corpus", document.Name, document.Index, names[document.Index])
					}
				}
			})
			if len(held) > 0 {
				requirePassed(t, r)
				requireLogged(t, r, fmt.Sprintf("CORPUS_SHARD=%d/%d: %d of the %d documents", shard, shards, len(held), len(names)))
			} else {
				requireFailed(t, r, "took none of its 0 documents")
			}
			if !slices.Equal(held, inShard) {
				t.Fatalf("shard %d/%d holds %v, and InShard keeps %v", shard, shards, held, inShard)
			}
		}
		for _, name := range names {
			if seen[name] != 1 {
				t.Fatalf("with %d shards %s falls in %d of them, want exactly one", shards, name, seen[name])
			}
		}
	}
}

// A CORPUS_SHARD that is not <i>/<n>, with 1 <= i <= n, fails the test that reads it.
func TestCorpusFailsOnAMalformedShard(t *testing.T) {
	corpus(t, map[string]string{"spec.md": "Text."})
	for _, value := range []string{"0/3", "4/3", "3", "1/0", "a/3", "1/3x", "-1/3", " 1/3", "1/ 3", "2/3/4"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CORPUS_SHARD", value)
			r := run(func(t testing.TB) { parseEach(corpustest.Open(t, corpusVariable, rules), nil) })
			requireFailed(t, r, fmt.Sprintf("CORPUS_SHARD=%q is not <shard>/<shards>", value))
			r = run(func(t testing.TB) { corpustest.InShard(t, "generated") })
			requireFailed(t, r, fmt.Sprintf("CORPUS_SHARD=%q is not <shard>/<shards>", value))
		})
	}
}

// Where the variable is unset, Open skips the test, naming the variable, and Optional reads no
// corpus and fails nothing.
func TestCorpusUnsetSkipsOrReadsNothing(t *testing.T) {
	t.Setenv(corpusVariable, "")
	r := run(func(t testing.TB) { corpustest.Open(t, corpusVariable, rules) })
	if r.failed() || !strings.Contains(r.skipped, corpusVariable+" is unset") {
		t.Fatalf("Open with the variable unset: errors %q, fatal %q, skipped %q", r.errors, r.fatal, r.skipped)
	}
	var documents []corpustest.Document
	r = run(func(t testing.TB) {
		c := corpustest.Optional(t, corpusVariable, rules)
		documents = c.Documents()
	})
	requirePassed(t, r)
	if len(documents) != 0 {
		t.Fatalf("Optional with the variable unset read %d documents", len(documents))
	}
}
