package specform

// Red test of the acceptance round at cbb63e3ef0 (place it at
// packages/envoy/internal/dispatch/specform/work_footnote_red_test.go). Rows that the work table
// (TestTheWorkOfAJudgmentGrowsInProportionToItsText) lacks: many texts that refer to one long
// footnote. Each text holds the footnote's words, so the judgment reads the footnote once for each
// text, and its work grows with the square of the document. The measure and the bound are the
// table's (cputest.OfCall, the least of workSamples samples, workGrowth). At cbb63e3ef0 each row
// grows about 19 times when the document grows 4 times. The control row, one statement that refers
// to a chain of footnotes, grows about 5 times.

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cputest"
)

func TestTheWorkOfAJudgmentOfTextsThatShareAFootnoteGrowsInProportionToItsDocument(t *testing.T) {
	context := planted("ada.lovelace@example.com")
	long := func(n int) string {
		return "[^long]: " + strings.Repeat("Dispatch keeps the record of each answer for one year. ", n) + "\n\n"
	}
	for _, row := range []struct {
		name string
		n    int
		md   func(n int) string
	}{
		{"many statements that refer to one long footnote", 100, func(n int) string {
			var b strings.Builder
			b.WriteString(long(n))
			for i := range n {
				fmt.Fprintf(&b, ":::requirement{#s-%d}\nDispatch keeps it[^long].\n:::\n\n", i)
			}
			return b.String()
		}},
		{"many terms that refer to one long footnote", 100, func(n int) string {
			var b strings.Builder
			b.WriteString(long(n) + "# Terms\n\n")
			for i := range n {
				fmt.Fprintf(&b, "- **term %d**: One record of a claim[^long].\n", i)
			}
			return b.String()
		}},
		{"control: one statement that refers to a chain of footnotes", 1000, func(n int) string {
			var b strings.Builder
			b.WriteString(":::requirement{#s}\nDispatch keeps it[^1].\n:::\n\n")
			for i := 1; i <= n; i++ {
				fmt.Fprintf(&b, "[^%d]: Dispatch keeps record %d[^%d].\n\n", i, i, i+1)
			}
			return b.String()
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			docs := []string{row.md(row.n), row.md(4 * row.n)}
			least := make([]time.Duration, 2)
			for sample := range workSamples {
				for index, md := range docs {
					runtime.GC()
					took, _ := cputest.Measure(func(runs int) (time.Duration, error) {
						return cputest.OfCall(func() {
							for range runs {
								read, err := ExtractDocument("spec.md", 1, md)
								if err != nil {
									t.Fatalf("read: %v", err)
								}
								Check(context, Input{Texts: read.Texts, Proposed: read.Terms})
							}
						}), nil
					})
					if sample == 0 || took < least[index] {
						least[index] = took
					}
				}
			}
			grows := float64(least[1]) / float64(least[0])
			t.Logf("%d bytes: %v; %d bytes: %v; %.1f times", len(docs[0]), least[0], len(docs[1]), least[1], grows)
			if grows >= workGrowth {
				t.Errorf("the work grew %.1f times, from %v for %d bytes to %v for %d bytes, want less than %d times",
					grows, least[0], len(docs[0]), least[1], len(docs[1]), workGrowth)
			}
		})
	}
}
