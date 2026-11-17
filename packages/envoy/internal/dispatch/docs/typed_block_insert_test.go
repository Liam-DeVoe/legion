package docs

import (
	"errors"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// typedInsertFixture holds a requirement, an ask with its question and two options, a check, a
// callout holding a list, and a paragraph at the document's own level.
const typedInsertFixture = ":::requirement{#r1}\nThe read succeeds.\n:::\n\n" +
	":::ask{#a1 urgency=\"med\" multiple=\"false\" state=\"open\"}\nWhich transport?\n\n- NATS\n- HTTP\n:::\n\n" +
	":::check{#k1 serves=\"dispatch://CORE-1/spec#b-r1\" surface=\"cli\" run=\"go test ./...\"}\nThe read returns 200.\n:::\n\n" +
	":::callout{#c1 kind=\"note\"}\n- one\n- two\n:::\n\n" +
	"After.\n"

// An insert anchored on a block inside a typed block lands beside that block, inside the typed
// block, whether the anchor names the block or quotes its text, after it or before it: the
// typed block's children are where the anchor names (dispatch://INFRA-3142). Below the typed
// block the block is its child holding the anchor, so a paragraph anchored on an item of a list
// in a callout lands after the list, in the callout, as one anchored on a list at the document's
// level lands after that list; list content anchored on an ask's option joins the options. An
// anchor at the document's own level, or on a typed block's own block, lands where it always did.
func TestAnInsertLandsInsideTheTypedBlockHoldingItsAnchor(t *testing.T) {
	for _, test := range []struct {
		name string
		op   func(t *testing.T, tree *pmdoc.Node) model.EditOp
		// old and new are the stretch of the fixture's rendering the insert rewrites.
		old, new string
	}{
		{
			name: "after a requirement's paragraph, by its block",
			op: func(t *testing.T, tree *pmdoc.Node) model.EditOp {
				return model.EditOp{Op: "insert", After: "block:" + blockIDOfText(t, tree, "The read succeeds."), Markdown: "Within a second."}
			},
			old: "The read succeeds.\n:::\n", new: "The read succeeds.\n\nWithin a second.\n:::\n",
		},
		{
			name: "before a requirement's paragraph, by its block",
			op: func(t *testing.T, tree *pmdoc.Node) model.EditOp {
				return model.EditOp{Op: "insert", Before: "block:" + blockIDOfText(t, tree, "The read succeeds."), Markdown: "First."}
			},
			old: "{#r1}\nThe read succeeds.\n", new: "{#r1}\nFirst.\n\nThe read succeeds.\n",
		},
		{
			name: "after a requirement's paragraph, by a quote of it",
			op: func(*testing.T, *pmdoc.Node) model.EditOp {
				return model.EditOp{Op: "insert", After: "read succeeds", Markdown: "Within a second."}
			},
			old: "The read succeeds.\n:::\n", new: "The read succeeds.\n\nWithin a second.\n:::\n",
		},
		{
			name: "after an ask's question",
			op: func(t *testing.T, tree *pmdoc.Node) model.EditOp {
				return model.EditOp{Op: "insert", After: "block:" + blockIDOfText(t, tree, "Which transport?"), Markdown: "The one the daemon holds."}
			},
			old: "Which transport?\n\n- NATS", new: "Which transport?\n\nThe one the daemon holds.\n\n- NATS",
		},
		{
			name: "an option after an ask's option",
			op: func(t *testing.T, tree *pmdoc.Node) model.EditOp {
				return model.EditOp{Op: "insert", After: "block:" + blockIDOfText(t, tree, "NATS"), Markdown: "- gRPC"}
			},
			old: "- NATS\n- HTTP\n", new: "- NATS\n- gRPC\n- HTTP\n",
		},
		{
			name: "after a check's paragraph",
			op: func(t *testing.T, tree *pmdoc.Node) model.EditOp {
				return model.EditOp{Op: "insert", After: "block:" + blockIDOfText(t, tree, "The read returns 200."), Markdown: "Within a second."}
			},
			old: "The read returns 200.\n:::\n", new: "The read returns 200.\n\nWithin a second.\n:::\n",
		},
		{
			name: "a paragraph after an item of a callout's list",
			op: func(t *testing.T, tree *pmdoc.Node) model.EditOp {
				return model.EditOp{Op: "insert", After: "block:" + blockIDOfText(t, tree, "one"), Markdown: "Noted."}
			},
			old: "- one\n- two\n:::\n", new: "- one\n- two\n\nNoted.\n:::\n",
		},
		{
			name: "after a paragraph at the document's level",
			op: func(t *testing.T, tree *pmdoc.Node) model.EditOp {
				return model.EditOp{Op: "insert", After: "block:" + blockIDOfText(t, tree, "After."), Markdown: "Closing."}
			},
			old: "After.\n", new: "After.\n\nClosing.\n",
		},
		{
			name: "after a requirement's own block",
			op: func(*testing.T, *pmdoc.Node) model.EditOp {
				return model.EditOp{Op: "insert", After: "block:r1", Markdown: "Beside it."}
			},
			old: "The read succeeds.\n:::\n\n", new: "The read succeeds.\n:::\n\nBeside it.\n\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tree, err := parseInput(typedInsertFixture)
			if err != nil {
				t.Fatal(err)
			}
			before, err := renderTree(tree)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(before, test.old) {
				t.Fatalf("the fixture renders %q, which holds no %q", before, test.old)
			}
			out, err := applyAlone(tree, test.op(t, tree))
			if err != nil {
				t.Fatal(err)
			}
			markdown, err := renderTree(out)
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.Replace(before, test.old, test.new, 1); markdown != want {
				t.Fatalf("after the insert = %q, want %q", markdown, want)
			}
		})
	}
}

// An insert anchored inside a typed block whose content rule cannot hold its markdown there is
// refused, naming the anchor, the typed block, its rule and what breaks it, and the anchor that
// writes the markdown beside the typed block instead.
func TestAnInsertATypedBlocksRuleCannotHoldIsRefused(t *testing.T) {
	for _, test := range []struct {
		name, text, markdown, holder string
		before                       bool
		rule                         pmdoc.BlockContentRule
		broken, says                 string
	}{
		{
			name: "a heading in a requirement", text: "The read succeeds.", markdown: "## Latency",
			holder: "r1", rule: pmdoc.BlockContentParagraphs, broken: "a heading",
			says: `lands inside the requirement block "r1", whose content rule, paragraph+, cannot hold a heading there; write what that rule holds there, or anchor the insert on the requirement block itself (after: "block:r1") to write the markdown beside it`,
		},
		{
			name: "a list in a check", text: "The read returns 200.", markdown: "- one more", before: true,
			holder: "k1", rule: pmdoc.BlockContentParagraphs, broken: "a bullet list",
			says: `(before: "block:k1")`,
		},
		{
			name: "a paragraph after an ask's options", text: "HTTP", markdown: "Pick one by Friday.",
			holder: "a1", rule: pmdoc.BlockContentParagraphsOptionalBulletList, broken: "a paragraph after its options",
			says: `cannot hold a paragraph after its options there`,
		},
		{
			name: "a second option list after an ask's question", text: "Which transport?", markdown: "- gRPC",
			holder: "a1", rule: pmdoc.BlockContentParagraphsOptionalBulletList, broken: "a second bullet list",
			says: `cannot hold a second bullet list there`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tree, err := parseInput(typedInsertFixture)
			if err != nil {
				t.Fatal(err)
			}
			anchor := "block:" + blockIDOfText(t, tree, test.text)
			op := model.EditOp{Op: "insert", After: anchor, Markdown: test.markdown}
			field := "after"
			if test.before {
				op, field = model.EditOp{Op: "insert", Before: anchor, Markdown: test.markdown}, "before"
			}
			_, err = applyAlone(tree, op)
			var refusal *ErrInsertDoesNotFit
			if !errors.As(err, &refusal) {
				t.Fatalf("insert of %q at %q: error = %v, want ErrInsertDoesNotFit", test.markdown, test.text, err)
			}
			if refusal.Field != field || refusal.Anchor != anchor || refusal.HolderID != test.holder || refusal.Rule != test.rule || refusal.Broken != test.broken {
				t.Fatalf("refusal = %+v, want %s %q in %q under %s, broken by %s", refusal, field, anchor, test.holder, test.rule, test.broken)
			}
			if !isEditRefusal(err) {
				t.Fatalf("isEditRefusal(%v) = false: the route would bury the refusal", err)
			}
			if !strings.Contains(err.Error(), test.says) || !strings.Contains(err.Error(), "insert "+field+" "+`"`+anchor+`"`) {
				t.Fatalf("refusal = %q, want it to name the anchor and say %q", err.Error(), test.says)
			}
		})
	}
}
