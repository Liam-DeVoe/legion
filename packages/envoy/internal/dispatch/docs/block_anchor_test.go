package docs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// A batch prepared from an earlier listing names ids another session's whole-document upload has
// minted again, and the precondition refuses it. The refusal says how to get back: read the
// blocks again, name a block by its text, and, where the batch's own text names one block, the id
// that block carries now.
func TestAPreconditionRefusedForARemovedBlockNamesTheWayBack(t *testing.T) {
	tree, err := parseInput("# Title\n\nIntro, as another session reworded it.\n\nTail.\n")
	if err != nil {
		t.Fatal(err)
	}
	start := newBatchTree(tree)
	current := blockIDOfText(t, tree, "Intro, as another session reworded it.")
	gone := "b9d7a3f0-0000-4000-8000-000000000000"
	refused := &ErrPreconditionFailed{Mismatches: []model.EditPreconditionMismatch{
		{Scope: "block", BlockID: gone, Expected: "sha256:stale"},
	}}
	ops := []model.EditOp{{Op: "replace", Find: "Intro, as another session reworded it.", With: "again"}}
	err = adviseRemovedPreconditionBlock(start, ops, refused)
	if !strings.Contains(err.Error(), "took a fresh id") ||
		!strings.Contains(err.Error(), "now has the id "+strconv.Quote(current)) {
		t.Fatalf("the refusal = %q, want the way back and the current id %q", err, current)
	}
	// Text two blocks hold names neither, since neither is the block the caller meant.
	twice, err := parseInput("Same words.\n\nSame words.\n")
	if err != nil {
		t.Fatal(err)
	}
	ambiguous := newBatchTree(twice)
	err = adviseRemovedPreconditionBlock(ambiguous,
		[]model.EditOp{{Op: "replace", Find: "Same words.", With: "again"}},
		&ErrPreconditionFailed{Mismatches: []model.EditPreconditionMismatch{
			{Scope: "block", BlockID: gone, Expected: "sha256:stale"},
		}})
	if strings.Contains(err.Error(), "now has the id") {
		t.Fatalf("the refusal = %q, want no id named where two blocks hold the text", err)
	}
	// A token that changed, rather than a block the document no longer holds, takes no advice.
	token := "sha256:current"
	unchanged := &ErrPreconditionFailed{Mismatches: []model.EditPreconditionMismatch{
		{Scope: "block", BlockID: current, Expected: "sha256:stale", Current: &token},
	}}
	if err := adviseRemovedPreconditionBlock(start, ops, unchanged); strings.Contains(err.Error(), "fresh id") {
		t.Fatalf("a changed block's refusal = %q, want no advice about a removed one", err)
	}
}

// A block guard covers an insert or a move whose anchor is a block id or a quote: the batch names
// the anchor block, and a move names the block it relocates as well.
func TestPreconditionBlocksCoverInsertAndMove(t *testing.T) {
	tree, err := parseInput("# Title\n\nIntro.\n\nTail.\n")
	if err != nil {
		t.Fatal(err)
	}
	intro := blockIDOfText(t, tree, "Intro.")
	tail := blockIDOfText(t, tree, "Tail.")
	for _, test := range []struct {
		name    string
		ops     []model.EditOp
		covered []string
		missing string
	}{
		{
			name:    "an insert on a block anchor",
			ops:     []model.EditOp{{Op: "insert", Markdown: "More.", After: "block:" + intro}},
			covered: []string{intro},
		},
		{
			name:    "an insert on a quote anchor",
			ops:     []model.EditOp{{Op: "insert", Markdown: "More.", Before: "Intro."}},
			covered: []string{intro},
		},
		{
			name:    "an insert whose anchor block is not covered",
			ops:     []model.EditOp{{Op: "insert", Markdown: "More.", After: "block:" + intro}},
			missing: intro,
		},
		{
			name:    "a move, which names the moved block too",
			ops:     []model.EditOp{{Op: "move", Block: tail, After: "Intro."}},
			covered: []string{intro, tail},
		},
		{
			name:    "a move whose moved block is not covered",
			ops:     []model.EditOp{{Op: "move", Block: tail, After: "Intro."}},
			covered: []string{intro},
			missing: tail,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := preconditionCoverage(tree, test.ops, test.covered...)
			if test.missing == "" {
				if err != nil {
					t.Fatalf("coverage of %s = %v, want it accepted", test.covered, err)
				}
				return
			}
			var invalid *ErrInvalidPrecondition
			if !errors.As(err, &invalid) || !strings.Contains(invalid.Reason, test.missing) {
				t.Fatalf("coverage error = %v, want the uncovered block %q named", err, test.missing)
			}
		})
	}
}

// An operation that removes or replaces a block holding others is covered only by a guard on the
// whole of what it takes, so a change another writer made anywhere inside refuses the batch
// (INFRA-341). The guard names the outermost node the operation takes as the listing names it:
// the list or quote a removal empties; for a list item a find removes, the item's paragraph and
// the nested list after it, since the listing names no list item; the typed block a replace's
// with rewrites under its own id. A delete that removes the very block it names asks for that
// block, as it always has. Each case prints the refusal it got.
func TestPreconditionBlocksCoverTheWholeOfWhatAnOperationTakes(t *testing.T) {
	const (
		nested = "- Parent\n  - Child\n- Other\n"
		only   = "Intro.\n\n- Only\n  - Child\n"
		lone   = "Intro.\n\n- Only\n"
		quoted = "> :::callout{#c1 kind=\"note\" title=\"T\"}\n> Alpha.\n> :::\n\nAfter.\n"
		typed  = "Intro.\n\n:::callout{#c1 kind=\"note\" title=\"T\"}\nAlpha.\n:::\n"
	)
	for _, test := range []struct {
		name, markdown string
		op             func(ids map[string]string) model.EditOp
		// covered and missing name blocks as blockIDsByName does; missing is "" where the
		// guard covers the operation.
		covered []string
		missing string
	}{
		{
			name:     "a delete whose list marker selects an item, under the item's paragraph alone",
			markdown: nested,
			op:       func(map[string]string) model.EditOp { return model.EditOp{Op: "delete", Find: "- Parent"} },
			covered:  []string{"Parent"},
			missing:  "bullet_list 1",
		},
		{
			name:     "a delete whose list marker selects an item, under its paragraph and its nested list",
			markdown: nested,
			op:       func(map[string]string) model.EditOp { return model.EditOp{Op: "delete", Find: "- Parent"} },
			covered:  []string{"Parent", "bullet_list 1"},
		},
		{
			name:     "a delete whose list marker selects a list's only item, under all the item holds",
			markdown: only,
			op:       func(map[string]string) model.EditOp { return model.EditOp{Op: "delete", Find: "- Only"} },
			covered:  []string{"Only", "bullet_list 1"},
			missing:  "bullet_list 0",
		},
		{
			name:     "a delete whose list marker selects a list's only item, under the list",
			markdown: only,
			op:       func(map[string]string) model.EditOp { return model.EditOp{Op: "delete", Find: "- Only"} },
			covered:  []string{"bullet_list 0"},
		},
		{
			name:     "a delete of an item's whole text, which hoists its nested list, under the item's paragraph",
			markdown: nested,
			op:       func(map[string]string) model.EditOp { return model.EditOp{Op: "delete", Find: "Parent"} },
			covered:  []string{"Parent"},
			missing:  "bullet_list 1",
		},
		{
			name:     "a delete of the whole text of a list's only item, under the item's paragraph",
			markdown: lone,
			op:       func(map[string]string) model.EditOp { return model.EditOp{Op: "delete", Find: "Only"} },
			covered:  []string{"Only"},
			missing:  "bullet_list 0",
		},
		{
			name:     "a delete of the paragraph a list's only item holds, under that paragraph",
			markdown: lone,
			op:       func(ids map[string]string) model.EditOp { return model.EditOp{Op: "delete", Block: ids["Only"]} },
			covered:  []string{"Only"},
			missing:  "bullet_list 0",
		},
		{
			name:     "a delete of the typed block a quote holds alone, under the typed block",
			markdown: quoted,
			op:       func(ids map[string]string) model.EditOp { return model.EditOp{Op: "delete", Block: ids["callout 0"]} },
			covered:  []string{"callout 0"},
			missing:  "blockquote 0",
		},
		{
			name:     "a delete of the list item it names, under that item",
			markdown: nested,
			op:       func(ids map[string]string) model.EditOp { return model.EditOp{Op: "delete", Block: ids["list_item 0"]} },
			covered:  []string{"list_item 0"},
		},
		{
			name:     "a move of a list's only item, under the anchor and the item's paragraph",
			markdown: lone,
			op: func(ids map[string]string) model.EditOp {
				return model.EditOp{Op: "move", Block: ids["Only"], After: "Intro."}
			},
			covered: []string{"Intro.", "Only"},
			missing: "bullet_list 0",
		},
		{
			name:     "a move of a list's only item, under the anchor and the list",
			markdown: lone,
			op: func(ids map[string]string) model.EditOp {
				return model.EditOp{Op: "move", Block: ids["Only"], After: "Intro."}
			},
			covered: []string{"Intro.", "bullet_list 0"},
		},
		{
			name:     "a replace whose with rewrites the typed block it lands in, under the matched paragraph",
			markdown: typed,
			op: func(map[string]string) model.EditOp {
				return model.EditOp{Op: "replace", Find: "Alpha.", With: ":::callout{#c1 kind=\"warning\" title=\"T\"}\nNew.\n:::"}
			},
			covered: []string{"Alpha."},
			missing: "callout 0",
		},
		{
			name:     "a replace whose with rewrites the typed block it lands in, under that block",
			markdown: typed,
			op: func(map[string]string) model.EditOp {
				return model.EditOp{Op: "replace", Find: "Alpha.", With: ":::callout{#c1 kind=\"warning\" title=\"T\"}\nNew.\n:::"}
			},
			covered: []string{"callout 0"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tree, err := parseInput(test.markdown)
			if err != nil {
				t.Fatal(err)
			}
			ids := blockIDsByName(tree)
			covered := make([]string, 0, len(test.covered))
			for _, name := range test.covered {
				if ids[name] == "" {
					t.Fatalf("no block %q in %v", name, ids)
				}
				covered = append(covered, ids[name])
			}
			err = preconditionCoverage(tree, []model.EditOp{test.op(ids)}, covered...)
			if test.missing == "" {
				if err != nil {
					t.Fatalf("coverage of %v = %v, want it accepted", test.covered, err)
				}
				return
			}
			var invalid *ErrInvalidPrecondition
			if want := "blocks must include tokens for resolved blocks: " + ids[test.missing]; !errors.As(err, &invalid) || invalid.Reason != want {
				t.Fatalf("coverage of %v = %v, want the reason %q, naming %s", test.covered, err, want, test.missing)
			}
			t.Logf("under %v: %v", test.covered, err)
		})
	}
}

// blockIDsByName names tree's blocks for a test: a paragraph by its text, and every other block by
// its type and its place among the blocks of that type in document order ("bullet_list 0" is the
// first list, the outermost where lists nest).
func blockIDsByName(tree *pmdoc.Node) map[string]string {
	ids := map[string]string{}
	counts := map[string]int{}
	pmdoc.Walk(tree, func(node *pmdoc.Node) bool {
		switch id := blockID(node); {
		case id == "" || node.Type == "doc":
		case node.Type == "paragraph":
			ids[pmdoc.TextContent(node)] = id
		default:
			ids[fmt.Sprintf("%s %d", node.Type, counts[node.Type])] = id
			counts[node.Type]++
		}
		return true
	})
	return ids
}

// An operation whose anchor names no block of its own - the document's edges, a heading - is the
// one a block guard cannot cover, and its refusal says what guards it instead.
func TestPreconditionBlocksRefuseAnAnchorThatNamesNoBlock(t *testing.T) {
	tree, err := parseInput("# Title\n\nIntro.\n")
	if err != nil {
		t.Fatal(err)
	}
	for anchor, op := range map[string]model.EditOp{
		"start":         {Op: "insert", Markdown: "More.", Before: "start"},
		"end":           {Op: "insert", Markdown: "More.", After: "end"},
		"heading:Title": {Op: "insert", Markdown: "More.", After: "heading:Title"},
	} {
		err := preconditionCoverage(tree, []model.EditOp{op})
		var invalid *ErrInvalidPrecondition
		want := "insert anchored on " + strconv.Quote(anchor) + " names no block to guard. The document token guards that operation."
		if !errors.As(err, &invalid) || invalid.Reason != want {
			t.Fatalf("coverage of an insert on %q = %v, want the reason %q", anchor, err, want)
		}
	}
}

// A block id an operation wrote that the document does not carry is refused by the quote form of
// the field it stood in, since the words are what the caller still has.
func TestBlockAnchorNotFoundNamesTheQuoteForm(t *testing.T) {
	tree, err := parseInput("# Title\n\nIntro.\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		op   model.EditOp
		want string
	}{
		{
			name: "an insert anchor",
			op:   model.EditOp{Op: "insert", Markdown: "More.", After: "block:gone"},
			want: `block "gone" is not in the document. Anchor by quote instead: write the text of that block in after, in place of "block:gone".`,
		},
		{
			name: "a move anchor",
			op:   model.EditOp{Op: "move", Block: blockIDOfText(t, tree, "Intro."), Before: "block:gone"},
			want: `block "gone" is not in the document. Anchor by quote instead: write the text of that block in before, in place of "block:gone".`,
		},
		{
			name: "a delete by block",
			op:   model.EditOp{Op: "delete", Block: "gone"},
			want: `block "gone" is not in the document. Name the block by its text instead: write that text in find.`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := applyOperations(tree, []model.EditOp{test.op})
			var missing *ErrBlockNotFound
			if !errors.As(err, &missing) {
				t.Fatalf("error = %v, want an ErrBlockNotFound", err)
			}
			if !errors.Is(err, pmdoc.ErrTargetNotFound) {
				t.Fatalf("error %v does not read as a target miss, which answers TARGET_NOT_FOUND", err)
			}
			if want := "operation 0: " + test.want; err.Error() != want {
				t.Fatalf("error = %q, want %q", err.Error(), want)
			}
		})
	}
}

// Where the operation carries text that identifies a block - a replace's find, a delete's quote -
// the refusal of its block id names the id that block carries now, so the caller can write the
// operation again without reading the document. Text two blocks hold names neither.
func TestBlockNotFoundNamesTheBlockHoldingTheQuotedText(t *testing.T) {
	tree, err := parseInput("# Title\n\nIntro.\n\nIntro.\n")
	if err != nil {
		t.Fatal(err)
	}
	title := blockIDOfText(t, tree, "Title")
	err = preconditionCoverage(tree, []model.EditOp{{Op: "delete", Block: "gone", Find: "Title"}}, title)
	var missing *ErrBlockNotFound
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want an ErrBlockNotFound", err)
	}
	if missing.Current != title {
		t.Fatalf("the refusal names block %q, want the block %q holding the quoted text", missing.Current, title)
	}
	if want := `The block whose text is "Title" now has the id "` + title + `".`; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to hold %q", err.Error(), want)
	}

	// Two blocks hold "Intro.", so neither is the block the caller meant.
	err = preconditionCoverage(tree, []model.EditOp{{Op: "delete", Block: "gone", Find: "Intro."}}, title)
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want an ErrBlockNotFound", err)
	}
	if missing.Current != "" {
		t.Fatalf("the refusal names block %q, want none named for text two blocks hold", missing.Current)
	}
}

// preconditionCoverage is what a block guard covering ids answers for ops, as a conditional edit
// asks it before applying the batch (requirePreconditionCoverage).
func preconditionCoverage(tree *pmdoc.Node, ops []model.EditOp, ids ...string) error {
	blocks := make([]model.EditBlockPrecondition, 0, len(ids))
	for _, id := range ids {
		blocks = append(blocks, model.EditBlockPrecondition{ID: id, Token: "token"})
	}
	return requirePreconditionCoverage(newBatchTree(tree), ops, model.EditPrecondition{Blocks: blocks}, "")
}
