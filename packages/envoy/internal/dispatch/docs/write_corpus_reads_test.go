package docs

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/corpustest"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// On every spec of the write corpus (runWriteCorpus), warm and cold, each anchored MarkQuote reads
// its fork twice, before and after its write, and each read joined to the write - every
// SnapshotVersion after a write, and the suggestion's SuggestionKind - and each ProjectMark's
// margin write take the write's rendering in place of reading the fork, and every rendering the
// write hands on is a fresh read and render of its fork (checkRendering). The specs are the
// documents of DISPATCH_TOKEN_CORPUS (the repository's markdown in CI) read through corpustest:
// each is parsed as its seed parses it (pmdoc.ParseWritten), the step whose refusal leaves it out,
// and the writes' three quotes are counted in its text (writeCorpusPicks, writeCorpusText), so a
// spec with too few is left out and FindQuote's refusal of one fails the test, as every error of
// the writes does. The test skips unless DISPATCH_TOKEN_CORPUS names the directory.
func TestWriteCorpusReadsOnce(t *testing.T) {
	corpus := corpustest.Open(t, "DISPATCH_TOKEN_CORPUS", corpustest.Rules{MaxBytes: pmdoc.MaxDocumentBytes, Failures: pmdoc.CorpusFailures})
	markQuote := forkReads{read: 2}
	reused := forkReads{renderingReused: 1}
	want := map[string]forkReads{
		"comment/MarkQuote":          markQuote,
		"comment/SnapshotVersion":    reused,
		"comment/ProjectMark":        reused,
		"suggestion/MarkQuote":       markQuote,
		"suggestion/SnapshotVersion": reused,
		"suggestion/SuggestionKind":  reused,
		"suggestion/ProjectMark":     reused,
		"edit/SnapshotVersion":       reused,
	}
	for _, document := range corpus.Documents() {
		var tree *pmdoc.Node
		if !corpus.Parse(document, func() (err error) {
			tree, _, err = pmdoc.ParseWritten(document.Markdown, nil)
			return err
		}) {
			continue
		}
		if _, ok := corpus.Quotes(document, corpustest.Quotes{
			Text: writeCorpusText(tree),
			Pick: func(once func(string) bool) ([]string, int) { return writeCorpusPicks(tree, once), 3 },
			Find: func(quote string) error {
				_, err := pmdoc.FindQuote(tree, quote, nil, nil)
				return err
			},
		}); !ok {
			continue
		}
		t.Run(document.Name, func(t *testing.T) {
			service := newWriteCorpusService(t)
			checks := checkRenderings(service)
			// Each step's reads are the counts' move since the call before it, which for a
			// transaction's first operation is its begin.
			got := map[string]forkReads{}
			last := readForkReads(service)
			observe := func(step string) {
				now := readForkReads(service)
				if !strings.HasSuffix(step, "/begin") {
					got[step] = now.minus(last)
				}
				last = now
			}
			runWriteCorpus(t, service, document.Name, document.Markdown, observe)
			checks.requireNoRenderingFailures(t)
			for _, mode := range writeCorpusModes {
				for _, step := range slices.Sorted(maps.Keys(want)) {
					reads := want[step]
					observed, ok := got[mode+"/"+step]
					if !ok {
						t.Errorf("%s/%s did not run", mode, step)
						continue
					}
					requireForkReads(t, mode+"/"+step, observed, reads)
				}
			}
		})
	}
}
