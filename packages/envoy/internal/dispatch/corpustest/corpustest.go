// Package corpustest reads the corpus of the corpus tests of internal/dispatch: the directory of
// markdown documents a variable names, which CI fills with every tracked *.md file of the
// repository (`git ls-files -- '*.md'`), each under its path with each / as __. Every corpus test
// reads its corpus here, and so leaves a document out for one of three reasons alone, none of
// which asks the code under test for a correct answer:
//
//  1. the document is larger than the size limit the test passes (pmdoc.MaxDocumentBytes), by its
//     size alone;
//  2. the test's parse of the markdown, the step Dispatch runs to accept an upload, refuses it -
//     but for the refusals the test passes as failures (pmdoc.CorpusFailures: a rendering that reads
//     back otherwise, or that no reader reads), matched with errors.Is, and a panic, each of which
//     fails the test;
//  3. its text holds fewer candidates than the test needs, a candidate being a sentence or a phrase
//     that occurs exactly once in the text, counted with strings.Count and never with the quote
//     finder under test.
//
// Every error of a step after the parse, on a document the parse took (a write, a read, a find, a
// render), fails the test, and so does the quote finder's error on a candidate that occurs once.
// The log names each document left out with its reason and error, and the counts taken, left out,
// left out under each error and failed; a test that takes none of its documents fails.
//
// CORPUS_SHARD=<shard>/<shards> keeps the documents whose FNV-1a hash of the file name, modulo
// <shards>, is <shard> less one, so the legs of a CI job that each read one shard read every
// document once between them; unset (or empty), a test reads the whole corpus, and any other
// value fails the test. The package imports nothing of pmdoc, so pmdoc's own tests can import it.
package corpustest

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Rules are what a test reads its corpus under.
type Rules struct {
	// MaxBytes is the largest document the test reads; a larger one is left out by its size.
	MaxBytes int
	// Failures are the refusals of the test's parse that fail the test rather than leave the
	// document out, each matched with errors.Is.
	Failures []error
}

// Document is one document of the corpus.
type Document struct {
	// Name is the file's path below the corpus directory.
	Name     string
	Markdown string
	// Index is the document's place among every document of the directory in name order, the
	// same on every shard, so a seed drawn from it does not move with the shard count.
	Index int
}

// Quotes is what reason 3 reads of one document a test takes quotes from.
type Quotes struct {
	// Text is the document's text, in which each candidate is counted.
	Text string
	// Pick is the test's own choice of the candidates it takes. once reports whether a candidate
	// occurs exactly once in Text; Pick returns the once-occurring candidates it takes, in its
	// order, and how many its precondition needs.
	Pick func(once func(candidate string) bool) (picked []string, need int)
	// Find is the test's quote finder. Its error on a candidate Pick took fails the test.
	Find func(quote string) error
}

// Corpus is one test's reading of its corpus.
type Corpus struct {
	t         testing.TB
	variable  string
	directory string
	rules     Rules
	documents []Document
	shard     string
	// leftOut is each document left out, by name, and failed each whose reading failed the test.
	leftOut map[string]bool
	failed  map[string]bool
	// oversized and short count the documents left out for reasons 1 and 3; refused counts those
	// the parse refused under each kind of error (refusalKind).
	oversized, short int
	refused          map[string]int
}

// Open reads the corpus directory variable names, or skips the test where it is unset. The test
// reads the documents of its shard (Documents), runs its parse of each through Parse, judges each
// later step's error with Later and, where it takes quotes, each document's candidates with
// Quotes; when the test ends the log gives the counts, and the test fails if it took none.
func Open(t testing.TB, variable string, rules Rules) *Corpus {
	t.Helper()
	directory := os.Getenv(variable)
	if directory == "" {
		t.Skipf("%s is unset; point it at a directory of markdown documents", variable)
	}
	return read(t, variable, directory, rules)
}

// Optional is Open for a test that checks documents of its own beside a corpus: where variable is
// unset it reads no corpus, logs so, and returns nil, whose Documents are none.
func Optional(t testing.TB, variable string, rules Rules) *Corpus {
	t.Helper()
	directory := os.Getenv(variable)
	if directory == "" {
		t.Logf("%s is unset: no corpus documents are read", variable)
		return nil
	}
	return read(t, variable, directory, rules)
}

func read(t testing.TB, variable, directory string, rules Rules) *Corpus {
	t.Helper()
	var names []string
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		name, err := filepath.Rel(directory, path)
		names = append(names, filepath.ToSlash(name))
		return err
	})
	if err != nil {
		t.Fatalf("read %s=%s: %v", variable, directory, err)
	}
	if len(names) == 0 {
		t.Fatalf("%s=%s holds no *.md document", variable, directory)
	}
	c := &Corpus{
		t:         t,
		variable:  variable,
		directory: directory,
		rules:     rules,
		leftOut:   map[string]bool{},
		failed:    map[string]bool{},
		refused:   map[string]int{},
	}
	shard, shards := shardOf(t)
	if shards > 0 {
		c.shard = fmt.Sprintf("CORPUS_SHARD=%d/%d", shard, shards)
	}
	for index, name := range names {
		if shards > 0 && !inShard(name, shard, shards) {
			continue
		}
		markdown, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		document := Document{Name: name, Markdown: string(markdown), Index: index}
		c.documents = append(c.documents, document)
		if len(markdown) > rules.MaxBytes {
			c.oversized++
			c.leave(document, fmt.Sprintf("%d bytes, more than the %d a document may hold", len(markdown), rules.MaxBytes))
		}
	}
	if c.shard != "" {
		t.Logf("corpus %s: %s: %d of the %d documents in %s", variable, c.shard, len(c.documents), len(names), directory)
	}
	t.Cleanup(c.done)
	return c
}

// Documents are the documents of the test's shard that the size limit leaves to it, in name
// order. A nil Corpus, Optional's where its variable is unset, has none.
func (c *Corpus) Documents() []Document {
	if c == nil {
		return nil
	}
	documents := make([]Document, 0, len(c.documents))
	for _, document := range c.documents {
		if !c.leftOut[document.Name] {
			documents = append(documents, document)
		}
	}
	return documents
}

// Parse runs parse, the test's parse of document's markdown, the step Dispatch runs to accept an
// upload, and reports whether the test takes the document: nil takes it, a refusal leaves it out
// under that refusal, and a refusal that is one of Rules.Failures, or a panic, fails the test and
// the document is not taken.
func (c *Corpus) Parse(document Document, parse func() error) bool {
	c.t.Helper()
	err, panicked := run(parse)
	switch {
	case panicked != nil:
		c.fail(document, fmt.Sprintf("the parse panicked: %v", panicked))
		return false
	case err == nil:
		return true
	}
	for _, failure := range c.rules.Failures {
		if errors.Is(err, failure) {
			c.fail(document, fmt.Sprintf("the parse refused it with %v, a refusal that fails a corpus test", err))
			return false
		}
	}
	c.refused[refusalKind(err)]++
	c.leave(document, fmt.Sprintf("the parse refused it: %v", err))
	return false
}

// run calls parse, and answers its error or what it panicked with.
func run(parse func() error) (err error, panicked any) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err, panicked = nil, recovered
		}
	}()
	return parse(), nil
}

// Later judges the error of a step after the parse - a write, a read, a find, a render - on a
// document the parse took, and reports whether the test keeps the document: any error fails the
// test.
func (c *Corpus) Later(document Document, err error) bool {
	c.t.Helper()
	if err == nil {
		return true
	}
	c.fail(document, fmt.Sprintf("a step after the parse refused it: %v", err))
	return false
}

// Quotes judges document's candidates (reason 3) and reports whether the test takes it, with the
// candidates the test's Pick took: fewer than it needs leaves the document out, and the quote
// finder's error on any of them fails the test and the document is not taken.
func (c *Corpus) Quotes(document Document, quotes Quotes) ([]string, bool) {
	c.t.Helper()
	once := func(candidate string) bool { return Once(quotes.Text, candidate) }
	picked, need := quotes.Pick(once)
	if len(picked) < need {
		c.short++
		c.leave(document, fmt.Sprintf("%d once-occurring candidates, and the test needs %d", len(picked), need))
		return nil, false
	}
	for index, candidate := range picked {
		if !once(candidate) {
			c.fail(document, fmt.Sprintf("the test took candidate %d, %d bytes, which the text holds %d times, not once",
				index+1, len(candidate), strings.Count(quotes.Text, candidate)))
			return nil, false
		}
		if err := quotes.Find(candidate); err != nil {
			c.fail(document, fmt.Sprintf("the quote finder refused candidate %d, %d bytes at byte %d of the text, which holds it once: %v",
				index+1, len(candidate), strings.Index(quotes.Text, candidate), err))
			return nil, false
		}
	}
	return picked, true
}

// Once reports whether candidate occurs exactly once in text: reason 3's count of a candidate.
func Once(text, candidate string) bool {
	return candidate != "" && strings.Count(text, candidate) == 1
}

var digits = regexp.MustCompile(`[0-9]+`)

// refusalKind is the kind of error a parse refusal is counted under: its text before its first
// quote, each run of digits as N, so the refusals of one rule that name different lines, counts
// or elements count together.
func refusalKind(err error) string {
	kind, _, _ := strings.Cut(err.Error(), `"`)
	return strings.TrimRight(digits.ReplaceAllString(kind, "N"), " :,")
}

func (c *Corpus) leave(document Document, reason string) {
	c.t.Helper()
	c.leftOut[document.Name] = true
	c.t.Logf("corpus %s: left out %s: %s", c.variable, document.Name, reason)
}

func (c *Corpus) fail(document Document, reason string) {
	c.t.Helper()
	c.failed[document.Name] = true
	c.t.Errorf("corpus %s: %s: %s", c.variable, document.Name, reason)
}

// done logs the counts once the test has run, and fails the test where it took no document.
func (c *Corpus) done() {
	c.t.Helper()
	refused := 0
	for _, count := range c.refused {
		refused += count
	}
	taken := len(c.documents) - len(c.leftOut) - len(c.failed)
	var reasons []string
	for _, reason := range []struct {
		count int
		what  string
	}{{c.oversized, "over the size limit"}, {refused, "refused by the parse"}, {c.short, "short of candidates"}} {
		if reason.count > 0 {
			reasons = append(reasons, fmt.Sprintf("%d %s", reason.count, reason.what))
		}
	}
	leftOut := strconv.Itoa(len(c.leftOut)) + " left out"
	if len(reasons) > 0 {
		leftOut += " (" + strings.Join(reasons, ", ") + ")"
	}
	where := c.directory
	if c.shard != "" {
		where += ", " + c.shard
	}
	c.t.Logf("corpus %s (%s): %d taken, %s, %d failed", c.variable, where, taken, leftOut, len(c.failed))
	kinds := make([]string, 0, len(c.refused))
	for kind := range c.refused {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	for _, kind := range kinds {
		c.t.Logf("corpus %s: refused by the parse with %q: %d", c.variable, kind, c.refused[kind])
	}
	if taken == 0 {
		c.t.Errorf("corpus %s: the test took none of its %d documents", c.variable, len(c.documents))
	}
}

// InShard reports whether a document named name falls in the shard CORPUS_SHARD names, for a test
// that shards documents of its own as Open shards a corpus: true where it is unset.
func InShard(t testing.TB, name string) bool {
	t.Helper()
	shard, shards := shardOf(t)
	return shards == 0 || inShard(name, shard, shards)
}

var shardPattern = regexp.MustCompile(`^([1-9][0-9]*)/([1-9][0-9]*)$`)

// shardOf is the shard CORPUS_SHARD names and the number of shards, 0 of 0 where it is unset or
// empty; a value that is not <shard>/<shards> with 1 <= shard <= shards fails the test.
func shardOf(t testing.TB) (shard, shards int) {
	t.Helper()
	if value := os.Getenv("CORPUS_SHARD"); value != "" {
		match := shardPattern.FindStringSubmatch(value)
		if match != nil {
			shard, _ = strconv.Atoi(match[1])
			shards, _ = strconv.Atoi(match[2])
		}
		if match == nil || shard > shards {
			t.Fatalf("CORPUS_SHARD=%q is not <shard>/<shards>, two whole numbers with 1 <= shard <= shards", value)
		}
	}
	return shard, shards
}

// inShard reports whether name falls in shard of shards: its FNV-1a hash, modulo shards, is shard
// less one. Every name falls in exactly one shard of any count.
func inShard(name string, shard, shards int) bool {
	hash := fnv.New32a()
	hash.Write([]byte(name))
	return int(hash.Sum32()%uint32(shards)) == shard-1
}
