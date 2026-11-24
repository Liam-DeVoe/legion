package pmdoc

import (
	"errors"
	"strings"
	"testing"
)

// An `ask` attribute names one ask of an issue, written whole as every writer writes its address,
// and never a part of one: `from_ask` points a decision at the ask it settles, which a part of the
// ask's text is not.
func TestAnAskAttributeNamesOneAskOfAnIssue(t *testing.T) {
	attr := BlockAttributeSchema{Kind: BlockAttributeAsk}
	const ask = "dispatch://CORE-1/ask/0b6f4c0e-3d1a-4f5e-9a7b-2c8d1e0f3a4b"
	if err := validateAttributeValue(attr, ask); err != nil {
		t.Fatalf("an ask of an issue %q is refused: %v", ask, err)
	}
	for value, reason := range map[string]string{
		ask + "#b-q1":                       "is a part of an ask",
		ask + "#b-q1@v2":                    "is a part of an ask",
		"dispatch://CORE-1/spec#b-x":        "is not an ask of an issue",
		"dispatch://CORE-1":                 "is not an ask of an issue",
		"dispatch://CORE/artifact/h/ask/a1": "is not an ask of an issue",
		ask + ".":                           "is not an ask of an issue",
		"see " + ask:                        "is not an ask of an issue",
		"":                                  "is not an ask of an issue",
	} {
		err := validateAttributeValue(attr, value)
		if err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("the ask attribute value %q = %v, want it refused as one that %s", value, err, reason)
		}
	}
	if err := validateAttributeValue(attr, true); err == nil {
		t.Errorf("an ask attribute holding a bool is accepted")
	}
}

// An attribute whose `markdown` field is false lives on the block's node alone: the renderer
// never writes it, and markdown that names it is read as if it did not. The rule is the field's,
// not a name's: the renderer used to leave out `invalid` by its name.
func TestAnAttributeMarkdownNeverCarriesIsNeitherWrittenNorRead(t *testing.T) {
	parsed, err := ParseForWrite(":::ask{#a1 invalid=\"forged\"}\nWhich day?\n:::\n", nil)
	if err != nil {
		t.Fatalf("markdown naming attributes markdown never carries is refused: %v", err)
	}
	if value, held := parsed.Children[0].Attrs["invalid"]; held {
		t.Errorf("markdown claiming invalid gives the ask invalid = %#v, want none", value)
	}

	value := blockPointerTestSchema(BlockAttributes{
		{Name: "invalid", BlockAttributeSchema: BlockAttributeSchema{Kind: BlockAttributeString, Server: true}},
		{Name: "note", BlockAttributeSchema: BlockAttributeSchema{Kind: BlockAttributeString, Markdown: new(false)}},
	})
	useTestBlockSchema(t, value)
	claim, err := Parse(":::claim{#c1 invalid=\"kept\" note=\"dropped\"}\nA claim.\n:::\n")
	if err != nil {
		t.Fatalf("parse a claim: %v", err)
	}
	if got, held := claim.Children[0].Attrs["note"]; held {
		t.Errorf("the claim's note = %#v, want it dropped since markdown never carries it", got)
	}
	claim.Children[0].Attrs["note"] = "on the node"
	rendered, err := Render(claim)
	if err != nil {
		t.Fatalf("render a claim: %v", err)
	}
	if !strings.Contains(rendered, `invalid="kept"`) || strings.Contains(rendered, "note") {
		t.Errorf("the claim renders as %q, want invalid written, since markdown carries it, and note left out", rendered)
	}
}

// A live document keeps a decision and an ask whatever order their blocks and readings stand in,
// as the browser editor builds them (proof-editor's relaxAskContentCheck): a person's paragraph
// added at the end of a decision and an agent's first reading added there at once land in an
// order the shared document decides, [paragraph, reading, paragraph] among them, and a tree the
// server refused would leave the document unreadable while every browser keeps it. Its rendering
// reads back as the tree it was rendered from. Markdown a caller writes in whole is still held to
// the decision's rule, which names the reading, and a reading anywhere a parent does not admit
// one is refused as before.
func TestALiveDecisionKeepsItsReadingWhateverOrderItsBlocksStandIn(t *testing.T) {
	paragraph := func(id, text string) *Node {
		return &Node{Type: "paragraph", Attrs: Attrs{BlockIDAttr: id}, Children: []*Node{{Type: "text", Text: text}}}
	}
	reading := func() *Node {
		return &Node{Type: "reading", Attrs: Attrs{BlockIDAttr: "r1"}, Children: []*Node{paragraph("rp", "The agent reads it as Friday.")}}
	}
	decision := &Node{Type: "decision", Attrs: Attrs{BlockIDAttr: "d1", "replaced": false}, Children: []*Node{
		paragraph("p1", "We ship on Friday."), reading(), paragraph("p2", "And we say so."),
	}}
	doc := &Node{Type: "doc", Children: []*Node{decision}}
	if err := doc.Validate(); err != nil {
		t.Fatalf("a decision holding a paragraph after its reading is refused: %v", err)
	}
	list := &Node{Type: "bullet_list", Children: []*Node{{Type: "list_item", Children: []*Node{paragraph("o1", "Friday")}}}}
	ask := &Node{Type: "ask", Attrs: Attrs{BlockIDAttr: "a1"}, Children: []*Node{
		paragraph("q1", "Which day?"), list, reading(), paragraph("p3", "An afterthought."),
	}}
	if err := (&Node{Type: "doc", Children: []*Node{ask}}).Validate(); err != nil {
		t.Fatalf("an ask holding a paragraph after its reading is refused: %v", err)
	}

	rendered, err := Render(doc)
	if err != nil {
		t.Fatalf("render the decision: %v", err)
	}
	back, err := ParseRendering(rendered)
	if err != nil {
		t.Fatalf("the decision's rendering %q does not read back: %v", rendered, err)
	}
	// A paragraph's id is not written in the markdown, so the read-back is compared without block
	// ids, and the typed blocks' own ids, which are, one by one.
	held := back.Children[0]
	if !back.Equal(doc) || held.Attrs[BlockIDAttr] != "d1" || len(held.Children) != 3 || held.Children[1].Attrs[BlockIDAttr] != "r1" {
		t.Fatalf("the decision's rendering %q reads back as %s, want the tree it was rendered from", rendered, BlockNames(held.Children))
	}
	_, err = ParseForWrite(rendered, nil)
	if want := `typed block "reading" "r1" stands only at the end of a decision or an ask, after that block's own content`; !errors.Is(err, ErrSchema) || err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("markdown written in whole holding a paragraph after a decision's reading = %v, want it refused with %q", err, want)
	}

	callout := &Node{Type: "doc", Children: []*Node{{Type: "callout", Attrs: Attrs{BlockIDAttr: "k1", "kind": "note", "title": ""}, Children: []*Node{
		paragraph("p4", "A note."), reading(),
	}}}}
	if err := callout.Validate(); err == nil || !strings.Contains(err.Error(), `typed block "reading" "r1" stands only`) {
		t.Fatalf("a callout holding a reading = %v, want the reading refused", err)
	}
}
