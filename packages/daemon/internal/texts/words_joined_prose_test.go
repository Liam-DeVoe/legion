package texts

import (
	"regexp"
	"strings"
	"testing"
)

// A start's own text that an earlier release queued is prose, not a record Render drew, and a
// person's words in it can hold lines of a block's form, such as a `<details>` section of a review
// body. Joined (the daemon's followedBy) draws every block of every part again against the rest of
// the task, so such a section is read as a block, and where a news record after it holds its tag,
// or two sections share one, its tag lines take another tag. What Joined may change is those tag
// lines alone: every other line of the prose and of each record, and the words between each pair of
// tag lines, stay as they were. And a prose start whose sections no other text holds the tag of,
// and that holds no two of one name, comes back as it was.
func TestJoinedChangesNoWordsOfAProseStartOnlyTheTagLinesOfItsSections(t *testing.T) {
	news, err := Render(DesignNewsRecord{TaskHead: TaskHead{Kind: DesignNews, Issue: "LEGION-208"}, Tree: "LEGION-200",
		Audience: NewsToHolder, Phase: "implementing",
		Items:    []DesignNewsItem{{Step: "spec", Document: "dispatch://LEGION-200/spec", Artifact: "artifact-spec", Version: 5, Taken: new(4), Changes: NewsChangesListed}},
		Sections: []DesignNewsSection{{Item: 0, Unit: NewsUnitSection, Nth: 1, Change: "changed", Heading: &Words{Text: "The `</details>` line"}}}})
	if err != nil {
		t.Fatal(err)
	}
	tagLine := regexp.MustCompile(`^</?[A-Za-z_][A-Za-z0-9_]*(?:-[1-9][0-9]*)?(?: [a-z]+="[^"<>]*")*>$`)
	words := func(text string) string {
		var kept []string
		for _, line := range strings.Split(text, "\n") {
			if !tagLine.MatchString(line) {
				kept = append(kept, line)
			}
		}
		return strings.Join(kept, "\n")
	}
	for _, c := range []struct {
		name, start string
		changed     bool
	}{
		{name: "a section whose tag the news holds",
			start: "Answer the review. Issue: LEGION-208.\n<details>\nthe log of the run\n</details>\nPush when it passes.", changed: true},
		{name: "two sections of one name",
			start: "Answer the review.\n<details>\nfirst run\n</details>\n<details>\nsecond run\n</details>", changed: true},
		{name: "a section no other text holds the tag of",
			start: "Answer the review.\n<summary>\nthe tests pass\n</summary>\nPush it.", changed: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			joined := Joined(c.start, news)
			t.Logf("joined:\n%s", joined)
			if got, want := words(joined), words(c.start+"\n\n"+news); got != want {
				t.Errorf("the joined task changes more than tag lines:\n%s\nwant, tag lines aside:\n%s", got, want)
			}
			if prose := strings.SplitN(joined, "\n\nkind: design-news\n", 2)[0]; (prose != c.start) != c.changed {
				t.Errorf("the start comes back as\n%s\nwant it changed %v", prose, c.changed)
			}
		})
	}
}
