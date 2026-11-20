package docs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/logtest"
)

// misreadBefore is a paragraph of an emphasised `(` and then a `y` both emphasised and strong,
// which reads back as written. misreadReplace writes a strong `x` between the two: the smallest
// shape found by reducing, its letters written as x and its digits as 0, the paragraph of a real
// document an edit stored reading back otherwise. The renderer writes the run that leaves as
// `*(***x*y***`, where the run of three asterisks after the `(` cannot close the emphasis, so the
// text reads back otherwise. It reads back as another run the renderer cannot write either, so an
// upload of that text is refused too, as an upload of the real document's stored text was.
const misreadBefore = "*(**y***\n"

var misreadReplace = model.EditOp{Op: "replace", Find: "y", With: "**x*y***"}

// storedTextEditor is the session the tests below edit as.
var storedTextEditor = model.Actor{Kind: "session", ID: "session-0123456789abcdef"}

// The text an edit batch stores is judged as an upload of that text is: a replace's own checks
// read back only the shape of the block it changed, so an edit could store a text an upload of it
// is refused for. An edit whose text would read back otherwise is refused with an upload's words,
// on the operation that wrote there, and leaves the document as it was. A renderer that writes the
// run in a spelling that reads back may let the edit be stored, but only with a text an upload of
// it is not refused for.
func TestAnEditWhoseTextWouldReadBackOtherwiseIsRefusedAsAnUploadOfItIs(t *testing.T) {
	service, artifactID := newTestService(t)
	service.settle = time.Hour
	seedServiceText(t, service, artifactID, misreadBefore)
	before, err := service.Text(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("read the document before the edit: %v", err)
	}
	if _, err := pmdoc.ParseForWrite(before, nil); err != nil {
		t.Fatalf("the document before the edit, %q, is refused as an upload: %v", before, err)
	}

	_, err = joinedApplyOps(service, artifactID, []model.EditOp{misreadReplace}, storedTextEditor, nil)
	if err == nil {
		stored, readErr := service.Text(context.Background(), artifactID)
		if readErr != nil {
			t.Fatalf("read the document the edit stored: %v", readErr)
		}
		if _, uploadErr := pmdoc.ParseForWrite(stored, nil); uploadErr != nil {
			t.Fatalf("the edit was stored as %q, which an upload of it is refused for: %v", stored, uploadErr)
		}
		return
	}
	var invalid *ErrInvalidOp
	if !errors.As(err, &invalid) || invalid.Field != "with" || !strings.HasPrefix(err.Error(), "operation 0: ") ||
		!strings.Contains(err.Error(), "reads back otherwise") {
		t.Fatalf("the edit was refused with %v, want operation 0's with refused as reading back otherwise", err)
	}
	after, err := service.Text(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("read the document after the refused edit: %v", err)
	}
	if after != before {
		t.Fatalf("the refused edit left the document %q, want it as it was, %q", after, before)
	}
}

// A document whose text already reads back otherwise in one block - a browser edit can leave one -
// still takes an edit elsewhere: the difference the text held before the edit, in the same block
// and the same characters, is not the edit's. It is stored all the same, so it is logged, by block
// id and the kind of difference alone, since a log line carries no document text.
func TestAnEditBesideABlockThatAlreadyReadsBackOtherwiseIsStoredAndLogsTheBlock(t *testing.T) {
	logs := &logtest.Recorder{}
	logtest.SetDefault(t, logs)
	service, artifactID := newTestService(t)
	service.settle = time.Hour
	seedServiceText(t, service, artifactID, "Plain words.\n\n"+misreadBefore)
	settleCurrentGeneration(t, service, artifactID)
	// A browser writes the second block into the shape the renderer has no spelling of that reads
	// back, the very tree misreadReplace writes: the text the room stores for it reads back
	// otherwise from here on, and an upload of that text is refused.
	editLiveTree(t, service, artifactID, func(tree *pmdoc.Node) *pmdoc.Node {
		paragraph := tree.Children[1]
		if len(paragraph.Children) != 2 || pmdoc.TextContent(paragraph) != "(y" {
			t.Fatalf("the second block holds %d runs, want the two misreadBefore writes", len(paragraph.Children))
		}
		var strong pmdoc.Mark
		for _, mark := range paragraph.Children[1].Marks {
			if mark.Type == "strong" {
				strong = mark
			}
		}
		x := &pmdoc.Node{Type: "text", Text: "x", Marks: []pmdoc.Mark{strong}}
		paragraph.Children = []*pmdoc.Node{paragraph.Children[0], x, paragraph.Children[1]}
		return tree
	})
	misread := blockID(liveTree(t, service, artifactID).Children[1])
	stored, err := service.Text(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("read the document the browser left: %v", err)
	}
	if _, err := pmdoc.ParseForWrite(stored, nil); !errors.Is(err, pmdoc.ErrMisreadRendering) {
		t.Fatalf("the document the browser left, %q, is not refused as an upload (%v): the edit below would prove nothing", stored, err)
	}

	if _, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: "Plain words.", With: "Plain words, edited."}}, storedTextEditor, nil); err != nil {
		t.Fatalf("the edit of the first block was refused: %v, want it stored", err)
	}
	var excused []slog.Record
	for _, record := range logs.Records() {
		if record.Message == "dispatch: a write stores a block that reads back otherwise, as the text it replaces already did" {
			excused = append(excused, record)
		}
	}
	if len(excused) != 1 {
		t.Fatalf("the edit logged %d blocks it stored reading back otherwise, want the one the browser left:\n%s", len(excused), recordList(logs.Records()))
	}
	attributes := map[string]string{}
	excused[0].Attrs(func(attribute slog.Attr) bool {
		attributes[attribute.Key] = attribute.Value.String()
		return true
	})
	if attributes["room"] != artifactID || attributes["block"] != misread || attributes["difference"] != "paragraph text" || len(attributes) != 3 {
		t.Fatalf("the edit logged %v, want room %s, block %s and the difference paragraph text, and nothing else", attributes, artifactID, misread)
	}
}

// recordList names every logged record by its message and attributes, for a failure to read.
func recordList(records []slog.Record) string {
	lines := make([]string, 0, len(records))
	for _, record := range records {
		line := "\t" + record.Message
		record.Attrs(func(attribute slog.Attr) bool {
			line += " " + attribute.String()
			return true
		})
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// countBlockReadBacks counts every read-back of a document-level block the package makes from here
// to the end of the test (blockReadBacksCounted), as countRenders records its renders. One test
// counts at a time, and the count is taken away in t.Cleanup.
func countBlockReadBacks(t *testing.T) *atomic.Int64 {
	t.Helper()
	count := &atomic.Int64{}
	if !blockReadBacksCounted.CompareAndSwap(nil, count) {
		t.Fatal("another test is counting the block read-backs already")
	}
	t.Cleanup(func() { blockReadBacksCounted.Store(nil) })
	return count
}

// A batch of replaces in one large block reads that block back once for each replace and once for
// the block it started from, since each replace starts from the very block the one before it left
// and the batch shares one read-back of each, and it renders the document once: the read-back it
// answers with is cut from the rendering it stores. A conditional batch runs its operations a
// second time to resolve the blocks its precondition must name, and judges nothing in that run, so
// it costs the same. Read-backs held for one operation each would read the block twice for every
// replace, and twice more in the conditional run, and a read-back rendered apart from the text the
// edit stores would render the document twice.
func TestReplacesInOneBlockReadItBackOnceEachAndRenderTheDocumentOnce(t *testing.T) {
	const replaces = 4
	words := make([]string, 2000)
	for index := range words {
		words[index] = fmt.Sprintf("w%04d", index)
	}
	large := strings.Join(words, " ") + "."
	ops := make([]model.EditOp, replaces)
	for index := range ops {
		word := words[100*(index+1)]
		ops[index] = model.EditOp{Op: "replace", Find: word, With: "v" + word[1:]}
	}
	for _, conditional := range []bool{false, true} {
		t.Run(fmt.Sprintf("conditional=%v", conditional), func(t *testing.T) {
			service, artifactID := newTestService(t)
			service.settle = time.Hour
			seedServiceText(t, service, artifactID, "Intro.\n\n"+large+"\n")
			settleCurrentGeneration(t, service, artifactID)
			// The edit before the measured one leaves the room's replica holding the state it
			// wrote, so the measured edit meets a warm room, as an edit of a document someone
			// reads does, and the rendering of the document before it is the replica's.
			if _, err := joinedApplyOps(service, artifactID, []model.EditOp{{Op: "replace", Find: "Intro.", With: "Intro, warmed."}}, storedTextEditor, nil); err != nil {
				t.Fatalf("warm the room with a first edit: %v", err)
			}
			var precondition *model.EditPrecondition
			if conditional {
				block := liveTree(t, service, artifactID).Children[1]
				token, err := nodeToken(block)
				if err != nil {
					t.Fatal(err)
				}
				precondition = &model.EditPrecondition{Blocks: []model.EditBlockPrecondition{{ID: blockID(block), Token: token}}}
			}

			readBacks := countBlockReadBacks(t)
			renders := countRenders(t)
			outcome, err := joinedApplyOps(service, artifactID, ops, storedTextEditor, precondition)
			rendered := renders()
			if err != nil {
				t.Fatalf("apply %d replaces in one block: %v", replaces, err)
			}
			if !outcome.Changed {
				t.Fatal("the replaces changed nothing")
			}
			if got := readBacks.Load(); got > replaces+1 {
				t.Errorf("%d replaces in one block read it back %d times, want at most %d", replaces, got, replaces+1)
			}
			stored, err := service.Text(context.Background(), artifactID)
			if err != nil {
				t.Fatalf("read the edited document: %v", err)
			}
			if len(rendered) != 1 || rendersOf(rendered, stored) != 1 {
				t.Errorf("the edit rendered %d documents, %d of them the text it stored, want that one alone", len(rendered), rendersOf(rendered, stored))
			}
		})
	}
}
