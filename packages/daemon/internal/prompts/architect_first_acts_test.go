package prompts

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// Each architect's prompt gives its first acts as one numbered list at the top of its role part,
// and no other sentence of the composed prompt names another act as the first. The root reads the
// skills, then its root's claim, then the start task and the catch-up. A child reads the skills,
// then its catch-up, then decides its children before it extends its spec: the agent of its first
// step may already be working, since Engine.gateOpened queues the child's architect start just
// before that agent's and holds the agent for nothing, and the children decide what the spec
// covers. The retired phrases are the scattered "first" sentences the list replaced.
func TestEachArchitectPromptGivesItsFirstActsAsOneOrderedList(t *testing.T) {
	composer, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	retired := []string{"before anything else", "decide first", "claim first", "take up first", "before you carry on", "before taking lifecycle action"}
	numbered := regexp.MustCompile(`(?m)^\d+\. `)
	for _, tc := range []struct {
		name   string
		isRoot bool
		part   string
		items  []string
		also   []string
	}{
		{
			name: "root", isRoot: true, part: "architect-root.md",
			items: []string{
				"1. Read the `<skills>` list in your system prompt and `skill://legion-architect`.",
				"2. Read your root's claim, and claim the root where it shows no claim or your own session, before any write.",
				"3. Read the start task and the `catch-up`. Act on them in this order: each reference a `decided` names, each issue waiting in `accepting`, then the gates and the tree.",
			},
		},
		{
			name: "child", isRoot: false, part: "architect.md",
			items: []string{
				"1. Read the `<skills>` list in your system prompt and `skill://legion-architect`.",
				"2. Read the `catch-up` of your issue and every issue under it.",
				"3. Decide whether your child needs children of its own",
				"4. Extend the issue's primary document in place as the specification of the part that stays",
			},
			also: []string{
				"Take step 3 before step 4.",
				"The agent of your child's first step may already be working: the daemon queues its start just after yours and never holds it for you.",
				"The children you file also set what your spec covers.",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(numbered.FindAllString(rolePart(t, tc.part), -1)); got != len(tc.items) {
				t.Errorf("roles/%s holds %d numbered items, want its one list of %d", tc.part, got, len(tc.items))
			}
			text := foldWhitespace(composedText(t, composer, claim.RoleArchitect, tc.isRoot))
			if at := strings.Index(text, tc.items[0]); at < 0 || at > 300 {
				t.Errorf("the composed %s architect prompt opens its list of first acts at byte %d, want it within the first 300", tc.name, at)
			}
			rest := text
			for _, item := range tc.items {
				_, after, found := strings.Cut(rest, item)
				if !found {
					t.Errorf("the composed %s architect prompt lacks %q after the item before it", tc.name, item)
					break
				}
				rest = after
			}
			for _, phrase := range tc.also {
				if !strings.Contains(text, phrase) {
					t.Errorf("the composed %s architect prompt omits %q", tc.name, phrase)
				}
			}
			lower := strings.ToLower(text)
			for _, phrase := range retired {
				if strings.Contains(lower, phrase) {
					t.Errorf("the composed %s architect prompt still says %q", tc.name, phrase)
				}
			}
		})
	}
}

// Only the root's architect asks a person with `dispatch ask`. A child's architect sends each
// question for a person to the architect above it (roles/architect.md), as the daemon's own
// children-first notice says it (workflow.ChildrenFirstNotice). go/architect-common.md, which both
// architects' prompts hold, says so once, and every other sentence of it that names `dispatch ask`
// is one only the root's architect acts on: `close_root`, whose CLOSE_REASON_DECISION_BLOCK sends a
// question to a decision block or an ask, is the root's architect's alone (api's ROOT_REQUIRED),
// and a sub-architect writes no decision block either.
func TestOnlyTheRootsArchitectIsToldToOpenADispatchAsk(t *testing.T) {
	common := goPart(t, "architect-common.md")
	folded := foldWhitespace(common)
	for _, want := range []string{
		"Wherever this part tells you to ask a person, the root's architect asks with `dispatch ask` on the root. A child's architect sends the question to the architect above it with `envoy_publish`, and that architect asks it",
		"Only the root's architect calls `close_root` (`ROOT_REQUIRED`). Ask that question in the root's own document, as a decision block there, or with `dispatch ask`, and close again with the decision stated.",
	} {
		if !strings.Contains(folded, want) {
			t.Errorf("go/architect-common.md omits %q", want)
		}
	}
	for _, clause := range clauses(common) {
		if strings.Contains(clause, "`dispatch ask`") && !strings.Contains(clause, "the root's architect") && !strings.Contains(clause, "the root's own document") {
			t.Errorf("go/architect-common.md tells every architect to open a dispatch ask: %q", clause)
		}
	}
	if child := rolePart(t, "architect.md"); strings.Contains(child, "whatever another part of this prompt says") {
		t.Error("roles/architect.md still allows for another part that tells a child's architect to open a dispatch ask")
	}
}

// A child's spec has no gate, so no person approves a version of it (api's GATE_ROOT_ONLY): a
// person's later request goes into a child's spec at a version the architect above reviews
// (roles/architect.md). A child is released only once its filed version has the architect's
// approving review and its plan, so no text tells the architect to release before the review
// (skill://legion-architect/references/decomposition.md, "Reviewing a child's spec"). And a child
// has no architect before its first phase starts it (Engine.gateOpened), so the review that comes
// before the release tells that architect once it runs.
func TestTheArchitectTextsGiveAChildsSpecItsReviewBeforeItsRelease(t *testing.T) {
	common := foldWhitespace(goPart(t, "architect-common.md"))
	child := foldWhitespace(rolePart(t, "architect.md"))
	for _, text := range []struct {
		name, body string
		want, gone []string
	}{
		{
			name: "go/architect-common.md", body: common,
			want: []string{
				"A requirement is in the approved spec or it is not one: a person's later request belongs there. On a root that is a version they approve, and on a child a version the architect above reviews.",
				"tell the child's architect with `envoy_publish` once it runs",
				"before the child's work goes on with that version",
			},
			gone: []string{"a person's later request belongs there, in a version they approve."},
		},
		{
			name: "roles/architect.md", body: child,
			want: []string{"Release every child with `release_children` as soon as its filed version has your approving spec review and you have given it its plan with `register_plan`"},
			gone: []string{"as soon as it is filed and you have given it its plan"},
		},
	} {
		for _, phrase := range text.want {
			if !strings.Contains(text.body, phrase) {
				t.Errorf("%s omits %q", text.name, phrase)
			}
		}
		for _, phrase := range text.gone {
			if strings.Contains(text.body, phrase) {
				t.Errorf("%s still says %q", text.name, phrase)
			}
		}
	}
}
