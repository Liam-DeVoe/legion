package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// An operator's resume that a release's take of the claim's issue pod made wait answers 202 with
// the claim, still suspended, and the owed resume's sentence (api.OperatorClaim.ResumeOwed). The
// claim's line reads suspended, so the sentence is a line of its own under it: without it an
// operator reads a resume that did nothing, and asks again. The control is the same answer with no
// sentence, which prints the claim's line alone.
func TestAResumeAReleaseMadeWaitPrintsItsSentenceUnderTheClaim(t *testing.T) {
	suspended := api.OperatorClaim{Token: "the-architect", Tree: "the-tree", Issue: "the-issue", Role: "architect",
		Generation: 2, State: "suspended", SuspendedBy: "operator", PluginContract: api.DaemonAPIVersion}
	owed := (&supervise.ResumeOwedError{Issue: "the-issue", Release: 7, Sandbox: "the-issue-pod"}).Error()
	printed := func(t *testing.T, c api.OperatorClaim) string {
		t.Helper()
		answer, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal the claim the route answers: %v", err)
		}
		var out bytes.Buffer
		if err := printClaim(&out, answer); err != nil {
			t.Fatalf("print the claim: %v", err)
		}
		return out.String()
	}

	line := printed(t, suspended)
	if strings.Count(line, "\n") != 1 || !strings.HasPrefix(line, "the-architect architect the-issue ") {
		t.Fatalf("control: the answer with no sentence printed %q, want the claim's one line", line)
	}
	waiting := suspended
	waiting.ResumeOwed = owed
	if got, want := printed(t, waiting), line+owed+"\n"; got != want {
		t.Fatalf("printed %q, want %q", got, want)
	}
}
