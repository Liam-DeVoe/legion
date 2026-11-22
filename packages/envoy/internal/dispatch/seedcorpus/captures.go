package seedcorpus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// copiedFiles are the captures of the two real issues that carry decisions, and premortemFile the
// capture of the graph pre-mortem, each committed under packages/dispatch/e2e/fixtures by
// dispatch://INFRA-1098's T1 and copied here unchanged.
var copiedFiles = []string{"acme-1115.json", "acme-1042.json"}

const premortemFile = "acme-1098-premortem.json"

// captures are the three files read.
type captures struct {
	copied    []copiedCapture
	premortem premortemCapture
}

// copiedCapture is one copied issue as its capture holds it: the issue's key and title where it
// was captured, the markdown of its spec and of its decisions document, and one row per ask.
type copiedCapture struct {
	file      string
	Issue     string `json:"issue"`
	Title     string `json:"title"`
	Documents struct {
		Spec      capturedDocument `json:"spec"`
		Decisions capturedDocument `json:"decisions"`
	} `json:"documents"`
	Asks []capturedAsk `json:"asks"`
}

// capturedDocument is one document of a capture: its name and its markdown.
type capturedDocument struct {
	Name     string `json:"name"`
	Markdown string `json:"markdown"`
}

// capturedAsk is one ask of a copied issue: its kind (question or approval), the state it was
// captured in (open, answered or resolved), its question and its options, and the answer where it
// holds one.
type capturedAsk struct {
	ID       string           `json:"id"`
	BlockID  *string          `json:"blockId"`
	Kind     string           `json:"kind"`
	State    string           `json:"state"`
	Question string           `json:"question"`
	Options  []capturedOption `json:"options"`
	Answer   *capturedAnswer  `json:"answer"`
}

// capturedOption is one option of a captured ask.
type capturedOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// capturedAnswer is a captured ask's answer: who answered, the options selected by label, and the
// words written.
type capturedAnswer struct {
	By       string   `json:"by"`
	Selected []string `json:"selected"`
	Text     string   `json:"text"`
}

// premortemCapture is the pre-mortem's capture: its document's name and markdown and the eleven
// risks its own capture read, by index, which the conversion's report is held to.
type premortemCapture struct {
	Document capturedDocument `json:"document"`
	Risks    []struct {
		Index int    `json:"index"`
		Title string `json:"title"`
	} `json:"risks"`
}

// answerers are the logins that answered one of the capture's asks, each once, which the loader
// records as people who have signed in so each answer is theirs.
func (c copiedCapture) answerers() []string {
	var logins []string
	for _, ask := range c.Asks {
		if ask.Answer != nil && ask.Answer.By != "" && !slices.Contains(logins, ask.Answer.By) {
			logins = append(logins, ask.Answer.By)
		}
	}
	return logins
}

// readCaptures reads the three captures out of directory.
func readCaptures(directory string) (captures, error) {
	var read captures
	if directory == "" {
		return read, fmt.Errorf("no directory of the captures was named (Options.Fixtures)")
	}
	for _, file := range copiedFiles {
		var capture copiedCapture
		if err := readJSON(filepath.Join(directory, file), &capture); err != nil {
			return read, err
		}
		if capture.Issue == "" || capture.Documents.Spec.Markdown == "" || len(capture.Asks) == 0 {
			return read, fmt.Errorf("%s holds no issue, spec or ask", file)
		}
		capture.file = file
		read.copied = append(read.copied, capture)
	}
	if err := readJSON(filepath.Join(directory, premortemFile), &read.premortem); err != nil {
		return read, err
	}
	if read.premortem.Document.Markdown == "" || len(read.premortem.Risks) != premortemRisks {
		return read, fmt.Errorf("%s holds %d risks, want %d", premortemFile, len(read.premortem.Risks), premortemRisks)
	}
	return read, nil
}

// premortemRisks is how many risks the captured pre-mortem holds: risk 0 and the ten numbered.
const premortemRisks = 11

func readJSON(path string, into any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read the capture %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("decode the capture %s: %w", path, err)
	}
	return nil
}
