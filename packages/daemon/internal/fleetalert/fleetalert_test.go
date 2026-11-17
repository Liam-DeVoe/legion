package fleetalert

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/testlog"
)

// writer is a Writer over a JSON log the test reads, as the daemon's own log is JSON: the alert
// policy reads the line out of `jsonPayload.msg`, so the test reads the same field.
func writer(t *testing.T) (*Writer, *testlog.Buffer) {
	t.Helper()
	buf := &testlog.Buffer{}
	return New(slog.New(slog.NewJSONHandler(buf, nil)), "ACME"), buf
}

// lines is every log entry the buffer holds, read as the policy reads them.
func lines(t *testing.T, buf *testlog.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %v (%s)", err, line)
		}
		out = append(out, entry)
	}
	return out
}

// The fixed text is the contract between this repository and the deployment repository, whose
// `legion/` program matches it in jsonPayload.msg. Neither side changes it alone.
func TestTheFixedTextIsTheContractWithTheAlertPolicy(t *testing.T) {
	if Prefix != "LEGION FLEET ALERT" {
		t.Fatalf("the fixed text is %q; the alert policy of the deployment repository's legion/ program matches %q in jsonPayload.msg, and the two move together", Prefix, "LEGION FLEET ALERT")
	}
}

func TestALineStartsWithTheFixedTextAndNamesItsCaseAndProject(t *testing.T) {
	w, buf := writer(t)

	w.Open(NoLiveSession, "", "sessions", 0)

	got := lines(t, buf)
	if len(got) != 1 {
		t.Fatalf("one open wrote %d lines, want 1: %s", len(got), buf.String())
	}
	msg, _ := got[0]["msg"].(string)
	if !strings.HasPrefix(msg, Prefix+": ") {
		t.Errorf("msg = %q, want it to start with %q", msg, Prefix+": ")
	}
	if !strings.Contains(msg, NoLiveSession.Sentence) {
		t.Errorf("msg = %q, want it to name the case %q", msg, NoLiveSession.Sentence)
	}
	if got[0]["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR: the policy reads the daemon's error lines", got[0]["level"])
	}
	if got[0]["case"] != NoLiveSession.Word {
		t.Errorf("case = %v, want %q", got[0]["case"], NoLiveSession.Word)
	}
	if got[0]["project"] != "ACME" {
		t.Errorf("project = %v, want ACME", got[0]["project"])
	}
	if _, named := got[0]["claim"]; named {
		t.Errorf("a case with no claim named one: %v", got[0]["claim"])
	}
	if got[0]["sessions"] != float64(0) {
		t.Errorf("the attributes the caller gave are not on the line: %v", got[0])
	}
}

func TestALineIsWrittenOnceUntilItsCaseEnds(t *testing.T) {
	w, buf := writer(t)

	if wrote := w.Open(ControllerUnreachable, "claim-1"); !wrote {
		t.Fatal("the first open wrote nothing")
	}
	if wrote := w.Open(ControllerUnreachable, "claim-1"); wrote {
		t.Error("the second open of an open case wrote a line")
	}
	if len(lines(t, buf)) != 1 {
		t.Fatalf("an open case wrote more than once: %s", buf.String())
	}

	w.Close(ControllerUnreachable, "claim-1")
	if wrote := w.Open(ControllerUnreachable, "claim-1"); !wrote {
		t.Error("the case came back and wrote nothing")
	}
	if len(lines(t, buf)) != 2 {
		t.Fatalf("the case that came back wrote %d lines, want 2: %s", len(lines(t, buf)), buf.String())
	}
}

func TestEachClaimOfOneCaseHasItsOwnLine(t *testing.T) {
	w, buf := writer(t)

	w.Open(NewerProcessRefused, "claim-1", "generation", 3)
	w.Open(NewerProcessRefused, "claim-2", "generation", 7)
	w.Open(NewerProcessRefused, "claim-1", "generation", 3)

	got := lines(t, buf)
	if len(got) != 2 {
		t.Fatalf("two claims of one case wrote %d lines, want 2: %s", len(got), buf.String())
	}
	if got[0]["claim"] != "claim-1" || got[1]["claim"] != "claim-2" {
		t.Errorf("the lines name %v and %v, want claim-1 and claim-2", got[0]["claim"], got[1]["claim"])
	}
}

// A closed case the writer never opened changes nothing: a caller that ends a case it never
// raised (a daemon that booted with the case already over) writes no line and clears nothing else.
func TestClosingACaseThatIsNotOpenChangesNothing(t *testing.T) {
	w, buf := writer(t)

	w.Open(SessionListUnreadable, "")
	w.Close(NoLiveSession, "")
	w.Open(SessionListUnreadable, "")

	if got := lines(t, buf); len(got) != 1 {
		t.Fatalf("closing another case let the open one write again: %s", buf.String())
	}
}

// Every case this package names is written out, so a case added without a word or a sentence,
// or with a word another case already uses, fails here rather than reaching the policy.
func TestEveryCaseHasItsOwnWordAndASentence(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Cases() {
		if c.Word == "" || c.Sentence == "" {
			t.Errorf("case %+v has no word or no sentence", c)
		}
		if seen[c.Word] {
			t.Errorf("two cases share the word %q", c.Word)
		}
		seen[c.Word] = true
	}
	if len(seen) != 5 {
		t.Errorf("Cases() names %d cases, want the 5 of R6 and R3", len(seen))
	}
}
