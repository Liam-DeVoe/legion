package pmdoc

import (
	"errors"
	"strings"
	"testing"

	"github.com/reearth/ygo/crdt"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc/heavybodies"
)

// The bodies a reader's browser tab froze on (sjawhar/legion#2519) are read here through the
// parser a caller's write takes, each at the length heavybodies grows it to - the longest one
// block of a body may be, but for the marker chain at 31,999 characters and the two runs of
// brackets at 30,000: a plain list of 8,000 items; a quote and a list item each followed by
// lines that carry no marker of their own; a run of brackets; 49 items nested by indentation and
// then blank lines, and 100 items nested the same way; 49 markers on one line and then blank
// lines; a chain of list markers on one line; and a typed fence whose label opens a run of
// brackets. Each is stored or refused here as it is written to be, and six
// of the nine are the very same bytes the browser suite draws in a page (e2e/heavy-bodies.ts,
// held to these by heavybodies/testdata/shared-bodies.json), so the two read one body, not two.
//
// The shapes themselves are heavybodies.Shapes (pmdoc/heavybodies), not this file: the API's own
// test of the verdict each write gets reads the same bodies from there, and a _test.go file of
// this package cannot be imported (INFRA-1404).
//
// Each shape is asserted at an eighth of what it grows to and at the whole of it: the verdict a
// write of it gets, and the parse's work, counted by Go's coverage counters in pmdoc and every
// package it imports outside the standard library (counted_work_test.go), held linear in the
// body's length; and, for every stored shape, the work of the parse and of the write of the
// document a write makes of the body together, held the same way.
//
// The Playwright spec that wrote eight of these nine shapes through the API held each write to a
// second and to nothing about its growth: a ratio between two wall times a shared CI runner took,
// a doubling of the body apart, cannot separate a regression from the runner's own noise, which
// is what made that bound fail at random (INFRA-985). The growth claim is made here instead, in
// counted work at an eightfold step, and the verdict each write gets is gated in
// internal/dispatch/api (TestHeavyDocumentWriteVerdicts).
//
// A second is the browser's own bound - the longest a card's draw may hold the page's main thread
// (longestHoldMs, packages/dispatch/e2e/main-thread-hold.ts) - and six of the nine bodies here are
// the very bytes those rows draw, so those six keep their second there. This file held every
// shape to a second of this process's CPU time too, until a loaded two-processor run spent that
// second on the runner's load and its collections rather than the parse (INFRA-3149). It then held
// the steps the parse counted of itself (ParseWork), which counted 17 steps for the 30,000
// characters of the run of brackets, whose parse is goldmark's link parser and imageNesting.Parse:
// a quadratic walk planted there passed (dispatch://INFRA-3149, the parsework-brackets-finding
// artifact). It holds the work the coverage counters count now, which no load moves and no loop of
// a counted package escapes, beside the bytes the parse allocates, which a regression that built a
// string or a list again for each line or block in the standard library would make grow with the
// square of the body.

// Every shape at the block bound is stored or refused as it is, at both sizes, and the parse of it
// counts work linear in its characters (countedWorkIsLinear): its growth from an eighth of the
// body to the whole, a floor a character past the eighth and a ceiling a character at each size,
// each bound set from the shape's reading in heavyBodyParseReadings.
func TestHeavyBodiesAtTheBlockCapAreStoredOrRefusedWithinParseStepsPerCharacter(t *testing.T) {
	// The shapes spell their refusals with heavybodies.MaxNesting, which is this package's own
	// maxNesting copied: heavybodies may not import pmdoc, since this test is in-package, so the
	// two are held to each other here rather than by the compiler.
	if heavybodies.MaxNesting != maxNesting {
		t.Fatalf("heavybodies.MaxNesting = %d, want pmdoc's maxNesting %d", heavybodies.MaxNesting, maxNesting)
	}
	row := t.Name()
	for _, shape := range heavyBodyParseShapes() {
		t.Run(shape.name, func(t *testing.T) {
			countedWorkIsLinear(t, row, shape)
		})
	}
}

// heavyBodyParseShapes are the heavy bodies' parses, each at an eighth of what it grows to and at
// the whole of it, each run checking the verdict the write gets. A shape's units are the characters
// it repeats (heavybodies.Shape's Grown), so the fixed opening a nested shape indents before its
// blank lines is no unit of its growth.
func heavyBodyParseShapes() []countedShape {
	var shapes []countedShape
	for _, shape := range heavybodies.Shapes() {
		shapes = append(shapes, countedShape{
			name:    shape.Name,
			sizes:   [2]int{shape.Grown / growthStep, shape.Grown},
			unit:    "repeated character",
			reading: heavyBodyParseReadings[shape.ID],
			stage:   searchedBytes,
			prepare: func(t *testing.T, size int) (int, func()) {
				body := heavyBody(t, shape, size)
				return size, func() {
					_, err := ParseForWrite(body, nil)
					requireHeavyVerdict(t, shape, len(body), err)
				}
			},
		})
	}
	return shapes
}

// heavyBodyParseReadings are the readings (countedReading) of each heavy body's parse, by its ID,
// taken on 2026-11-20 in INFRA-3149's pod (Go 1.27.3) at an eighth of each shape's Grown and at
// all of it, counting every package pmdoc imports outside the standard library: past is the
// statements each repeated character past the smaller size cost, most the statements a repeated
// character cost at whichever size cost more. Each shape's floor is a quarter of past and its
// ceiling four times most (countedMargin). The run of brackets counted 7,679,344 statements over
// 3,750 characters and 74,564,344 over 30,000, 9.71 times over 8 times the characters, linear
// with a fixed term below zero: each character past 3,750 cost 2,548.0, as each past 7,500 and
// past 15,000 did, and 70.8 million of the larger count were goldmark's. goldmark refuses a
// closer outright while more than 998 labels stand open, so of a run of n brackets only the last
// 998 closers copy their label (text/reader.go's Value) before refusing it for its length, which
// is 998 copies of up to n characters less the 998² the shorter ones save. ParseWork counted 17
// steps for that parse. The typed fence is refused at its line before any inline parse, at 1.0
// statements a character.
var heavyBodyParseReadings = map[string]countedReading{
	"plain-list":                        {past: 1532.3, most: 1533.3},
	"lazy-quote":                        {past: 572.0, most: 573.8},
	"lazy-list-item":                    {past: 610.5, most: 612.9},
	"bracket-run":                       {past: 2548.0, most: 2485.5},
	"nested-49-items-then-blank-lines":  {past: 9533.0, most: 10240.6},
	"markers-49-then-blank-lines":       {past: 9533.0, most: 9618.5},
	"nested-100-items-then-blank-lines": {past: 8173.0, most: 10385.5},
	"marker-chain":                      {past: 673.0, most: 679.0},
	"callout-bracket-run":               {past: 1.0, most: 1.7},
}

// heavyBody is shape at size, which may be no longer than the block cap.
func heavyBody(t *testing.T, shape heavybodies.Shape, size int) string {
	t.Helper()
	body := shape.Body(size)
	if len(body) > heavybodies.BlockCap {
		t.Fatalf("%s is %d characters, past the %d one block may hold", shape.Name, len(body), heavybodies.BlockCap)
	}
	return body
}

// requireHeavyVerdict fails t unless err is the verdict a write of shape gets: stored, or refused
// as it is written to be.
func requireHeavyVerdict(t *testing.T, shape heavybodies.Shape, characters int, err error) {
	t.Helper()
	if shape.Refusal == "" {
		if err != nil {
			t.Fatalf("%s at %d characters: %v, want it stored", shape.Name, characters, err)
		}
	} else if !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), shape.Refusal) {
		t.Fatalf("%s at %d characters: %v, want ErrSchema saying %q", shape.Name, characters, err, shape.Refusal)
	}
}

// Every shape a write stores is parsed and written into a document in work linear in its length,
// at the sizes the parse alone is counted at above: this is the whole of what a document write
// does with a body before it reaches Postgres - the parse of the markdown a caller sent and the
// write of that tree into a fresh document, encoded as the document's first durable update
// (parseReplacing and encodeDocumentTree, internal/dispatch/docs/tree.go). The parse is the part
// that has grown quadratically before (linear_test.go holds the shapes that did), and the write and
// the encode are counted beside it because they are the rest of what an API write does with a
// body, which the end-to-end bound this test replaced used to cover (INFRA-985). The write and the
// encode are ygo's as much as this package's, and the counters count ygo's packages too, so an
// encode or an insert that walked the document again for each item it wrote grows the count with
// the square of the document; an encode that copied its buffer again for each item, through the
// standard library's copy, grows the bytes the write allocates instead, which the row judges beside
// the count. A write that encodes no update fails.
//
// Each size gets a document of its own, made before the count starts, since a write changes the
// document it writes into.
func TestHeavyBodiesAtTheBodyCapParseAndEncodeInWorkProportionalToTheirLength(t *testing.T) {
	row := t.Name()
	for _, shape := range heavyBodyWriteShapes() {
		t.Run(shape.name, func(t *testing.T) {
			countedWorkIsLinear(t, row, shape)
		})
	}
}

// heavyBodyWriteShapes are the stored heavy bodies' parses, each followed by the write of its tree
// into a fresh document and the encode of that document's update, in the units heavyBodyParseShapes
// counts.
func heavyBodyWriteShapes() []countedShape {
	var shapes []countedShape
	for _, shape := range heavybodies.Shapes() {
		if shape.Refusal != "" {
			continue
		}
		shapes = append(shapes, countedShape{
			name:    shape.Name,
			sizes:   [2]int{shape.Grown / growthStep, shape.Grown},
			unit:    "repeated character",
			reading: heavyBodyWriteReadings[shape.ID],
			stage:   searchedBytes,
			prepare: func(t *testing.T, size int) (int, func()) {
				body := heavyBody(t, shape, size)
				// "prosemirror" is the fragment the document service writes a document's blocks
				// into (fragmentName, internal/dispatch/docs/tree.go).
				doc := crdt.New()
				fragment := doc.GetXmlFragment("prosemirror")
				return size, func() {
					tree, err := ParseForWrite(body, nil)
					if err != nil {
						t.Fatalf("parsing %d characters of %s: %v", len(body), shape.Name, err)
					}
					if err := doc.TransactE(func(txn *crdt.Transaction) error { return Update(txn, fragment, tree) }); err != nil {
						t.Fatalf("writing %d characters of %s: %v", len(body), shape.Name, err)
					}
					if update := crdt.EncodeStateAsUpdateV1(doc, nil); len(update) == 0 {
						t.Fatalf("writing %d characters of %s encoded no update", len(body), shape.Name)
					}
				}
			},
		})
	}
	return shapes
}

// heavyBodyWriteReadings are the readings (countedReading) of each stored heavy body's parse,
// write and encode, by its ID, taken as heavyBodyParseReadings were.
var heavyBodyWriteReadings = map[string]countedReading{
	"plain-list":                       {past: 2045.5, most: 2045.1},
	"lazy-quote":                       {past: 574.0, most: 576.2},
	"lazy-list-item":                   {past: 612.5, most: 615.7},
	"bracket-run":                      {past: 2550.0, most: 2487.5},
	"nested-49-items-then-blank-lines": {past: 9533.0, most: 10276.6},
	"markers-49-then-blank-lines":      {past: 9533.0, most: 9645.4},
}
