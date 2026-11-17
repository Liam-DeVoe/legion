package specform

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cputest"
)

// workGrowth is how many times the work of a judgment may grow when its text grows four times, the
// bound of a comparison of a text of n units and one of 4n (TestTheWorkOfAJudgmentOfTextsThatShare
// AFootnoteGrowsInProportionToItsDocument): work in proportion to the text grows about four times,
// and work for each pair of forms, links, texts or characters about sixteen.
const workGrowth = 6

// workSamples is how many times each text is judged, the sizes in turn, so that a change of the
// machine's load reaches each. A busy machine can only add to the CPU time of a judgment, so the
// least of the samples is its work.
const workSamples = 3

// workScale is how many times the larger text of a row of the work table is the smaller one, and
// workBound how many times its work may grow: work in proportion to the text grows about 16 times,
// n log n work at most about 22 times for the smallest n of the table, and work for each pair of
// units about 256 times. So a walk for each pair breaks the bound once it is a fifteenth of the
// work of the smaller text.
const (
	workScale = 16
	workBound = 32
)

// A judgment does work in proportion to its text, for each rule of the form and each shape that
// grows the text: many forms of one rule on one line, one long line, many short lines, many links,
// addresses and titles, many terms and many texts, and each part of a document that many texts
// share. The one part of a document that the reader lets many texts share is a footnote: many
// statements, term lines, component rows and purpose lines that refer to one long footnote, to one
// footnote of many links, or to one chain of footnotes, and one statement that refers to many
// footnotes. The reader refuses a link definition, which many links could share, and a link and an
// image hold their own address and title. Each row judges a text of n units and one of workScale
// times n, and the work of the larger grows less than workBound times (cputest.Growth): the
// smaller text is judged workScale times in one window and the larger once, so that both windows
// last about as long. The work is the CPU time of the thread that judges (cputest.OfCall): a wait
// for a core is not in it, and work that allocates nothing is. The window of each row lasts tens
// of milliseconds or more, so that the collector's work and a cold cache are a small part of it. A
// row whose judgment finds no finding of its rule at either size reads nothing, so it fails; a row
// of a rule "" is a shape that breaks no rule. A text of n units is 16 KB or more: Go's regexp
// reads a shorter text by a faster way, and the port's work for each byte grew 1.3 times from a
// text of 8 KB to one of 32 KB for that reason alone.
func TestTheWorkOfAJudgmentGrowsInProportionToItsText(t *testing.T) {
	context := planted("ada.lovelace@example.com")
	const id = "0195f3c2-7b1d-7c3e-9a4f-2b6d8e1f0a37"
	type judgment func(t *testing.T, n int) func() []Finding
	// checked judges one text of kind: before, n units, then after.
	checked := func(kind Kind, before, unit, after string) judgment {
		return func(_ *testing.T, n int) func() []Finding {
			in := Input{Texts: []Text{{Node: "n", Kind: kind, Body: before + strings.Repeat(unit, n) + after, Where: where}}}
			return func() []Finding { return Check(context, in) }
		}
	}
	// extracted reads the document that markdown writes for n units, and judges it.
	extracted := func(markdown func(n int) string) judgment {
		return func(t *testing.T, n int) func() []Finding {
			document := markdown(n)
			return func() []Finding {
				read, err := ExtractDocument("spec.md", 1, document)
				if err != nil {
					t.Fatalf("read %d units: %v", n, err)
				}
				return Check(context, Input{Texts: read.Texts, Proposed: read.Terms})
			}
		}
	}
	// units is before, n units, each written with its number, and after.
	units := func(before, unit, after string) func(n int) string {
		return func(n int) string {
			var markdown strings.Builder
			markdown.WriteString(before)
			for index := range n {
				fmt.Fprintf(&markdown, unit, index+1)
			}
			markdown.WriteString(after)
			return markdown.String()
		}
	}
	// shared is one footnote of n units, each written with its number, and n units that refer to
	// it, each written with its number, after head.
	shared := func(note, head, unit string) func(n int) string {
		return func(n int) string {
			return units("[^shared]: ", note, "\n\n")(n) + units(head, unit, "")(n)
		}
	}
	const requirement, end = ":::requirement{#r}\nDispatch keeps", "\n:::\n"
	const statementUnit = ":::requirement{#s-%d}\nDispatch keeps record %[1]d[^shared].\n:::\n\n"
	for _, row := range []struct {
		name  string
		rule  Rule
		n     int
		judge judgment
	}{
		{"many sentences on one line", ruleSentences, 1_000, checked(KindStatement, "", "Dispatch keeps it. ", "")},
		{"many short lines", ruleSentences, 1_000, checked(KindStatement, "", "Dispatch keeps it.\n", "")},
		{"one long sentence", ruleSentenceWords, 4_000, checked(KindStatement, "Dispatch keeps ", "word ", "it.")},
		{"many stops with no space after them", ruleStopNoSpace, 4_000, checked(KindClaim, "Dispatch reads ", "a.B ", "now.")},
		{"one word of many stops", "", 16_000, checked(KindClaim, "Dispatch reads ", ".", "A now.")},
		{"many semicolons", "ste100-semicolon", 1_000, checked(KindClaim, "Dispatch keeps it", ";", " and waits.")},
		{"many phrasal verbs", "ste100-phrasal-verb", 500, checked(KindClaim, "Legion will ", "spin up ", "the job.")},
		{"many marketing adjectives", "ste100-marketing-adjective", 500, checked(KindClaim, "Dispatch keeps a ", "seamless ", "log.")},
		{"many nominalizations", "ste100-nominalization", 400, checked(KindClaim, "Dispatch ", "performs a validation ", "of each upload.")},
		{"many synonyms of one verb", "ste100-synonym-rotation", 400, checked(KindClaim, "", "Check the config file. Then verify the output. ", "")},
		{"one list item that ends with and", "ste100-dangling-conjunction", 2_000, checked(KindClaim, "- Dispatch keeps ", "the record ", "and")},
		{"many references", ruleDispatchLink, 4_000, checked(KindName, "", "dispatch://ACME-1 ", "")},
		{"many keys", ruleDispatchLink, 8_000, checked(KindName, "", "ACME-1 ", "")},
		{"many ids", ruleDispatchLink, 2_000, checked(KindName, "", id+" ", "")},
		{"one reference before many closing brackets", ruleDispatchLink, 64_000, checked(KindName, "dispatch://ACME-1", ")", "")},
		{"many incidents", ruleIncident, 8_000, checked(KindName, "", "INC-1 ", "")},
		{"many numbers of a pull request", rulePullRequest, 8_000, checked(KindName, "", "#1 ", "")},
		{"many pull requests of a repository", rulePullRequest, 4_000, checked(KindName, "", "acme/widgets#1 ", "")},
		{"many addresses of a pull request", rulePullRequest, 2_000, checked(KindName, "", "github.com/acme/widgets/pull/1 ", "")},
		{"many email addresses", rulePerson, 4_000, checked(KindName, "", "ada@example.com ", "")},
		{"many handles", rulePerson, 8_000, checked(KindName, "", "@ada ", "")},
		{"many names of a person", rulePerson, 4_000, checked(KindName, "", "Ada Lovelace ", "")},
		{"many links", ruleDispatchLink, 2_000, extracted(units(requirement, " [it](dispatch://ACME-%d)", end))},
		{"many titles", ruleDispatchLink, 2_000, extracted(units(requirement, ` [it](https://example.com "ACME-%d")`, end))},
		{"many images", ruleDispatchLink, 2_000, extracted(units(requirement, " ![it](dispatch://ACME-%d)", end))},
		{"many terms", "ste100-semicolon", 500, extracted(units("# Terms\n\n", "- **term %d**: Dispatch keeps it; it waits.\n", ""))},
		{"many statements", "ste100-semicolon", 500, extracted(units("", ":::requirement{#s-%d}\nDispatch keeps it; it waits.\n:::\n\n", ""))},
		{"many purpose lines of one id", "ste100-semicolon", 2_000, extracted(units("", "Purpose of x: Dispatch keeps record %d; it waits.\n\n", ""))},
		{"many footnotes", "ste100-semicolon", 500, extracted(units("", "[^%[1]d]: Dispatch keeps it; it waits.\n\n:::requirement{#s-%[1]d}\nDispatch keeps it[^%[1]d].\n:::\n\n", ""))},
		{"many statements that refer to one long footnote", "ste100-semicolon", 500,
			extracted(shared("Dispatch keeps record %d; it waits. ", "", statementUnit))},
		{"many term lines that refer to one long footnote", "", 500,
			extracted(shared("Dispatch keeps the record of answer %d for one year. ", "# Terms\n\n", "- **term %d**: One record of a claim[^shared].\n"))},
		{"many component rows that refer to one long footnote", "", 500,
			extracted(shared("Dispatch keeps the record of answer %d for one year. ", "| Id | Kind | Name | Purpose |\n| --- | --- | --- | --- |\n",
				"| `c%d` | component | Part %[1]d | It keeps one record[^shared]. |\n"))},
		{"many purpose lines that refer to one long footnote", "", 500,
			extracted(shared("Dispatch keeps the record of answer %d for one year. ", "", "Purpose of c%d: It keeps one record[^shared].\n\n"))},
		{"many statements that refer to one footnote of many links", ruleDispatchLink, 500,
			extracted(shared("[it](dispatch://ACME-%d) ", "", statementUnit))},
		{"many statements that refer to one chain of footnotes", ruleSentences, 500,
			extracted(func(n int) string {
				var chain strings.Builder
				for index := 1; index <= n; index++ {
					fmt.Fprintf(&chain, "[^%d]: Dispatch keeps record %[1]d[^%d].\n\n", index, index+1)
				}
				return chain.String() + units("", ":::requirement{#s-%d}\nDispatch keeps it[^1].\n:::\n\n", "")(n)
			})},
		{"one statement that refers to many footnotes", ruleSentences, 1_000,
			extracted(func(n int) string {
				return units(":::requirement{#s}\nDispatch keeps it", "[^%d]", ".\n:::\n\n")(n) + units("", "[^%d]: Dispatch keeps record %[1]d.\n\n", "")(n)
			})},
	} {
		t.Run(row.name, func(t *testing.T) {
			sizes := []int{row.n, workScale * row.n}
			judges := []func() []Finding{row.judge(t, sizes[0]), row.judge(t, sizes[1])}
			findings := make([][]Finding, len(sizes))
			growth, small, large := cputest.Growth(workSamples, workScale,
				func() { findings[0] = judges[0]() }, func() { findings[1] = judges[1]() })
			for index, found := range findings {
				if has := slices.ContainsFunc(found, func(finding Finding) bool { return finding.Rule == row.rule }); row.rule != "" && !has {
					t.Fatalf("the judgment of %d units finds no finding of %s, so this row reads nothing: %.300v", sizes[index], row.rule, found)
				} else if row.rule == "" && len(found) > 0 {
					t.Fatalf("the judgment of %d units finds %.300v, want none", sizes[index], found)
				}
			}
			t.Logf("%d units %d times: %v; %d units once: %v; %.1f times", sizes[0], workScale, small, sizes[1], large, growth)
			if growth >= workBound {
				t.Errorf("the work grew %.1f times, from %v for %d runs of %d units to %v for one run of %d, want less than %d times",
					growth, small, workScale, sizes[0], large, sizes[1], workBound)
			}
		})
	}
}
