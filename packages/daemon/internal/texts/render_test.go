package texts

import (
	"strings"
	"testing"
	"time"
)

// RenderHead is exported, as an embedded struct of a record must be.
type RenderHead struct {
	Kind  Kind   `json:"kind"`
	Issue string `json:"issue"`
}

type renderKept struct {
	Issue  string `json:"issue"`
	Ref    string `json:"ref"`
	Answer *Words `json:"answer"`
}

type renderRecord struct {
	RenderHead
	Look      int          `json:"look"`
	Merged    bool         `json:"merged"`
	Draft     bool         `json:"draft,omitempty"`
	Version   int          `json:"version,omitempty"`
	LastLook  time.Time    `json:"last_look"`
	Since     time.Time    `json:"since,omitzero"`
	Skipped   []string     `json:"skipped"`
	Open      []string     `json:"open"`
	Decisions []renderKept `json:"kept"`
	Reason    Words        `json:"reason"`
	Message   *Words       `json:"message"`
	Note      *Words       `json:"note"`
	Untagged  string
	Hidden    string `json:"-"`
}

func (renderRecord) Lead() string {
	return "Production check of INFRA-3686: Dispatch: reduce verbosity."
}

func (renderRecord) Kept() []string {
	return []string{"this resume is your design-approved wake"}
}

func TestRenderWritesTheLeadTheFieldsTheKeptSentencesAndTheBlocks(t *testing.T) {
	at := time.Date(2026, 11, 14, 22, 44, 10, 0, time.UTC)
	record := renderRecord{
		RenderHead: RenderHead{Kind: "look", Issue: "INFRA-3686"},
		Look:       1,
		LastLook:   time.Date(2026, 11, 30, 23, 0, 0, 500_000_000, time.FixedZone("x", 3600)),
		Skipped:    []string{"docs", "ci-filters"},
		Decisions: []renderKept{
			{Issue: "INFRA-1", Ref: "dispatch://INFRA-1/ask/a", Answer: &Words{Text: "Yes.", By: "sami", At: at}},
			{Issue: "INFRA-2", Ref: "dispatch://INFRA-2/ask/b", Answer: &Words{Text: "No, and </answer here.", Rest: 7}},
		},
		Reason:   Words{Text: "Two lines\nof reason.", By: "tester"},
		Message:  &Words{Text: "", More: true},
		Untagged: "not a field",
		Hidden:   "not a field",
	}
	want := strings.Join([]string{
		"Production check of INFRA-3686: Dispatch: reduce verbosity.",
		"kind: look",
		"issue: INFRA-3686",
		"look: 1",
		"merged: false",
		`last_look: "2026-11-30T22:00:00.5Z"`,
		"skipped[2]: docs,ci-filters",
		"open: []",
		"kept[2]{issue,ref}:",
		`  INFRA-1,"dispatch://INFRA-1/ask/a"`,
		`  INFRA-2,"dispatch://INFRA-2/ask/b"`,
		"this resume is your design-approved wake",
		`<answer by="sami" at="2026-11-14T22:44:10Z" field="kept[0].answer">`,
		"Yes.",
		"</answer>",
		`<answer-1 field="kept[1].answer" rest="7">`,
		"No, and </answer here.",
		"</answer-1>",
		`<reason by="tester">`,
		"Two lines",
		"of reason.",
		"</reason>",
		`<message more="true">`,
		"",
		"</message>",
	}, "\n")
	for _, given := range []any{record, &record} {
		got, err := Render(given)
		if err != nil {
			t.Fatalf("render %T: %v", given, err)
		}
		if got != want {
			t.Errorf("render %T:\ngot\n%s\nwant\n%s", given, got, want)
		}
	}

	if _, err := Render("text"); err == nil {
		t.Error("a string rendered; a record is a struct")
	}
	if _, err := Render(struct {
		Score float64 `json:"score"`
	}{1.5}); err == nil {
		t.Error("a float rendered; the encoder writes integers alone")
	}
	type head struct {
		Issue string `json:"issue"`
	}
	if _, err := Render(struct{ head }{head{Issue: "INFRA-1"}}); err == nil {
		t.Error("a record that embeds an unexported struct rendered; its fields cannot be read whole")
	}
}
