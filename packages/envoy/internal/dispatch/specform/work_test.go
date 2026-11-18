package specform

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cputest"
)

// workSamples is how many times each text is judged, the sizes in turn, so that a change of the
// machine's load reaches each. A busy machine can only add to the CPU time of a judgment, so the
// least of the samples is its work.
const workSamples = 3

// workScale is how many times the larger text of a row of the work table is the smaller one, and
// workBound how many times its work may grow: work in proportion to the text grows about 16 times,
// n log n work at most about 22 times for the smallest n of the table, and work for each pair of
// units about 256 times. So a walk for each pair breaks the bound once it is a fifteenth of the
// work of the smaller text. Measured on a machine of 288 cores, in 20 runs of each at a load
// average of 262 to 2,295 and in 11 more runs of the table, one under -race, at 158 to 1,058: the
// worst growth of this table was 24.0, and the least growth of a row whose walk was made a walk
// for each pair (each walk that a row is for, broken in turn) was 54.1.
const (
	workScale = 16
	workBound = 32
)

// A judgment does work in proportion to its text, for each rule of the form and each shape that
// grows the text: many forms of one rule on one line, one long line, many short lines, many links,
// addresses and titles, many terms and many texts, and each part of a document that many texts
// share. The one part of a document that the reader lets many texts share is a footnote: many
// statements, term lines, component rows and purpose lines that refer to one long footnote, to one
// footnote of many links, or to one chain of footnotes, many claims that refer to one long footnote
// of two verbs for one action, which is one finding of the footnote, one statement that refers to a
// chain of footnotes, and one statement that refers to many footnotes, also built with no store at
// a size that no store holds, where the count of its sentences stops once it passes two. The
// footnotes of the chains hold a verb of a synonym group, which Dispatch carries into each text
// that holds them, as it carries their sentences. The reader refuses a link definition, which many
// links could share, and a link and an image hold their own address and title. Each row judges a
// text of n units and one of workScale times n, and the work of the larger grows less than
// workBound times (Growth): the smaller text is judged workScale times in one window and the
// larger once, so that both windows last about as long. The work is the user CPU time of the
// thread that judges: the work of the judgment's own code. A wait for a core is not in it, and nor
// is the kernel's work for the thread, such as the reclaim of memory when the machine is short of
// it; work that allocates nothing is in it. The smaller window of each row lasts cputest.MinWindow
// or more, which Growth holds, so that the kernel's split of the thread's time into user and system
// time, the collector's work and a cold cache are a small part of it. A row whose judgment finds
// no finding of its rule at either size reads nothing, so it fails; a row of a rule "" is a shape
// that breaks no rule. A text of n units is 16 KB or more: Go's regexp reads a shorter text by a
// faster way, and the port's work for each byte grew 1.3 times from a text of 8 KB to one of 32 KB
// for that reason alone. Each row's n is large enough that the walk it is for, made a walk for each
// pair (ste-lint's count of a column from the start of the line, a scan of the addresses, the texts
// of a node, the footnote definitions read so far, the footnotes that hold a footnote's sentences
// and verbs, or the footnotes that a statement's count has passed), is most of the work of the
// larger text, so the row fails it. A walk for each pair whose step is a comparison of a few
// nanoseconds needs units that cost little else: so the footnotes of the rows "many footnotes" and
// "one statement that refers to many footnotes", and the statements of the first, hold a word or two
// each, and the row of purpose lines of one id times the reader alone (read), since the walk that
// it is for is the reader's, and the judgment of each line costs several times the reading of it.
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
	// read reads the document that markdown writes for n units, and judges none of its texts: a row
	// for a walk of the reader, which the judgment of each text, several times the work of its
	// reading, would hide. It fails unless the reader keeps the n texts as n texts of kind under one
	// node.
	read := func(node string, kind Kind, markdown func(n int) string) judgment {
		return func(t *testing.T, n int) func() []Finding {
			document := markdown(n)
			reading := func() Extracted {
				kept, err := ExtractDocument("spec.md", 1, document)
				if err != nil {
					t.Fatalf("read %d units: %v", n, err)
				}
				return kept
			}
			kept, last, want := reading(), "", fmt.Sprintf("%s (%d)", node, n)
			if len(kept.Texts) > 0 {
				last = kept.Texts[len(kept.Texts)-1].Node
			}
			if kept.Counts[kind] != n || len(kept.Texts) != n || last != want {
				t.Fatalf("the reader keeps %d texts of %d units, %d of kind %s, the last of node %q, want %d under one node, the last %q",
					len(kept.Texts), n, kept.Counts[kind], kind, last, n, want)
			}
			return func() []Finding {
				reading()
				return nil
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
	// chain is n footnotes, each of which refers to the next, and each of which holds a verb of a
	// synonym group.
	chain := func(n int) string {
		var markdown strings.Builder
		for index := 1; index <= n; index++ {
			fmt.Fprintf(&markdown, "[^%d]: Dispatch checks record %[1]d[^%d].\n\n", index, index+1)
		}
		return markdown.String()
	}
	// noted is one statement that refers to n footnotes of one short sentence each, which a caller
	// that reads no store builds, such as a check of the files of a tree: no limit of the store
	// holds n. The count of the statement's sentences stops once it passes maxSentences, so the
	// work of the count does not grow with the footnotes that it has passed.
	noted := func(_ *testing.T, n int) func() []Finding {
		notes := make([]*Footnote, n)
		for index := range notes {
			notes[index] = &Footnote{Label: fmt.Sprint(index + 1), Body: "It."}
		}
		in := Input{Texts: []Text{{Node: "s", Kind: KindStatement, Body: "Dispatch keeps it.", Where: where, Notes: notes}}}
		return func() []Finding { return Check(context, in) }
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
		{"many semicolons", "ste100-semicolon", 4_000, checked(KindClaim, "Dispatch keeps it", ";", " and waits.")},
		{"many phrasal verbs", "ste100-phrasal-verb", 3_000, checked(KindClaim, "Legion will ", "spin up ", "the job.")},
		{"many marketing adjectives", "ste100-marketing-adjective", 3_000, checked(KindClaim, "Dispatch keeps a ", "seamless ", "log.")},
		{"many nominalizations", "ste100-nominalization", 2_400, checked(KindClaim, "Dispatch ", "performs a validation ", "of each upload.")},
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
		{"many links", ruleDispatchLink, 4_000, extracted(units(requirement, " [it](dispatch://ACME-%d)", end))},
		{"many titles", ruleDispatchLink, 4_000, extracted(units(requirement, ` [it](https://example.com "ACME-%d")`, end))},
		{"many images", ruleDispatchLink, 4_000, extracted(units(requirement, " ![it](dispatch://ACME-%d)", end))},
		{"many terms", "ste100-semicolon", 500, extracted(units("# Terms\n\n", "- **term %d**: Dispatch keeps it; it waits.\n", ""))},
		{"many statements", "ste100-semicolon", 500, extracted(units("", ":::requirement{#s-%d}\nDispatch keeps it; it waits.\n:::\n\n", ""))},
		{"many purpose lines of one id", "", 2_000, read("x", KindPurpose, units("", "Purpose of x: Dispatch keeps record %d; it waits.\n\n", ""))},
		{"many footnotes", "ste100-semicolon", 2_000, extracted(units("", "[^%[1]d]: It; it.\n\n:::requirement{#s-%[1]d}\nIt[^%[1]d].\n:::\n\n", ""))},
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
				return chain(n) + units("", ":::requirement{#s-%d}\nDispatch keeps it[^1].\n:::\n\n", "")(n)
			})},
		{"many claims that refer to one long footnote of two verbs for one action", "ste100-synonym-rotation", 500,
			extracted(shared("Dispatch checks record %d and confirms it. ", "", ":::check{#c-%d}\nDispatch keeps record %[1]d[^shared].\n:::\n\n"))},
		{"one statement that refers to a chain of footnotes", ruleSentences, 500,
			extracted(func(n int) string { return ":::requirement{#s}\nDispatch keeps it[^1].\n:::\n\n" + chain(n) })},
		{"one statement that refers to many footnotes", ruleSentences, 1_000,
			extracted(func(n int) string {
				return units(":::requirement{#s}\nDispatch keeps it", "[^%d]", ".\n:::\n\n")(n) + units("", "[^%d]: It.\n\n", "")(n)
			})},
		{"one statement that refers to many footnotes, with no store", ruleSentences, 8_000, noted},
	} {
		t.Run(row.name, func(t *testing.T) {
			sizes := []int{row.n, workScale * row.n}
			judges := []func() []Finding{row.judge(t, sizes[0]), row.judge(t, sizes[1])}
			findings := make([][]Finding, len(sizes))
			growth, small, large, err := cputest.Growth(workSamples, workScale,
				cputest.Again(func() { findings[0] = judges[0]() }), cputest.Again(func() { findings[1] = judges[1]() }))
			if err != nil {
				t.Fatal(err)
			}
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
