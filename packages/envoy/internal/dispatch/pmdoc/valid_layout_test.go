package pmdoc

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/corpustest"
)

// TestValidDocumentRenderWithLayoutIsTheValidatingRender holds the render a write's fork takes
// (readFork's validated tree, ValidDocument.RenderWithLayout, which does not validate again) to
// RenderWithLayout's, which validates first: the same markdown, block offsets and layout, byte for
// byte, over every fixture, every corpus file, a generated document of 400 KB holding every kind
// of block and, where DISPATCH_RENDER_CORPUS names a directory, every document of it the test
// takes, read through corpustest, which judges each refusal of Parse and fails the test on a
// render's refusal of a document Parse took.
func TestValidDocumentRenderWithLayoutIsTheValidatingRender(t *testing.T) {
	rendered := 0
	// check compares the two renders of doc and answers the render's refusal where both refuse
	// it, which a corpus document hands to corpustest as a step after the parse.
	check := func(name string, doc *Node) error {
		want, wantOffsets, wantLayout, wantErr := RenderWithLayout(doc)
		valid, err := ValidateDocument(doc, "RenderWithLayout")
		if err != nil {
			if wantErr == nil {
				t.Errorf("%s: Validate refuses the tree (%v) and RenderWithLayout renders it", name, err)
			}
			return wantErr
		}
		got, gotOffsets, gotLayout, gotErr := valid.RenderWithLayout()
		if (gotErr == nil) != (wantErr == nil) {
			t.Errorf("%s: the validated render's error is %v, the validating render's %v", name, gotErr, wantErr)
			return nil
		}
		if wantErr != nil {
			return wantErr
		}
		if got != want {
			t.Errorf("%s: the validated render's markdown (%d bytes) is not the validating render's (%d bytes)", name, len(got), len(want))
		}
		if !reflect.DeepEqual(gotOffsets, wantOffsets) {
			t.Errorf("%s: the validated render's %d block offsets are not the validating render's %d", name, len(gotOffsets), len(wantOffsets))
		}
		if gotLayout == nil || !reflect.DeepEqual(gotLayout, wantLayout) {
			t.Errorf("%s: the validated render's layout is not the validating render's", name)
		}
		rendered++
		return nil
	}

	docs := map[string]*Node{"generated": generatedDocument(t, 400<<10)}
	for _, fx := range loadFixtures(t) {
		doc, err := FromJSON(fx.PMJSON)
		if err != nil {
			t.Fatalf("%s: %v", fx.Name, err)
		}
		docs["fixture "+fx.Name] = doc
	}
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", "*.md"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("testdata/corpus: %d .md files, %v", len(paths), err)
	}
	for _, path := range paths {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := Parse(string(source))
		if err != nil {
			continue
		}
		docs[path] = doc
	}
	for _, name := range slices.Sorted(maps.Keys(docs)) {
		check(name, docs[name])
	}
	// Each corpus document is checked as it is parsed, so the test holds one corpus tree at a time.
	corpus := corpustest.Optional(t, "DISPATCH_RENDER_CORPUS", corpusRules)
	for _, document := range corpus.Documents() {
		var doc *Node
		if corpus.Parse(document, func() (err error) {
			doc, err = Parse(document.Markdown)
			return err
		}) {
			// The render is a step after the parse: its refusal fails the test.
			corpus.Later(document, check("corpus "+document.Name, doc))
		}
	}
	if rendered == 0 {
		t.Fatal("no document rendered")
	}
	t.Logf("%d documents rendered, each identical", rendered)

	if _, _, _, err := (ValidDocument{}).RenderWithLayout(); !errors.Is(err, ErrSchema) {
		t.Fatalf("the zero ValidDocument's RenderWithLayout = %v, want ErrSchema", err)
	}
}
