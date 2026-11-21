package recordkey

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
)

// storedRow is one key of the fixture of what a merge-base build stored (testdata).
type storedRow struct {
	Column string `json:"column"`
	Key    string `json:"key"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Type   string `json:"type"`
}

// readStoredRows reads the fixture, failing when it holds no row, so a check over it cannot pass
// by reading nothing.
func readStoredRows(t *testing.T) []storedRow {
	t.Helper()
	raw, err := os.ReadFile("testdata/merge-base-keys.json")
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	var fixture struct {
		Rows []storedRow `json:"rows"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode the fixture: %v", err)
	}
	if len(fixture.Rows) == 0 {
		t.Fatal("UNKNOWN: the fixture holds no stored key")
	}
	return fixture.Rows
}

// misread is what is wrong with key as the store of row: the key this package builds for row's
// destination differs from it, or it does not parse back to row's kind and name (a session's or a
// role's), or it parses when it is an issue's, which Parse reads no kind of. Empty is right.
func misread(row storedRow, key string) string {
	var built string
	var kind Kind
	switch row.Kind {
	case "session":
		built, kind = Session(row.Name), SessionKind
	case "role":
		built, kind = Role(row.Name), RoleKind
	case "issue":
		built = Issue(row.Name, row.Type)
	default:
		return fmt.Sprintf("the fixture names kind %q", row.Kind)
	}
	if built != key {
		return fmt.Sprintf("this build keys %s %q as %q, not %q", row.Kind, row.Name, built, key)
	}
	parsed, err := Parse(key)
	if kind == 0 {
		if err == nil {
			return fmt.Sprintf("%q parses as %+v, want no session or role", key, parsed)
		}
		if issue, typ, ok := ParseIssue(key); !ok || issue != row.Name || typ != row.Type {
			return fmt.Sprintf("%q parses as issue %q type %q (%v), want %q type %q", key, issue, typ, ok, row.Name, row.Type)
		}
		return ""
	}
	if err != nil || parsed != (Destination{Kind: kind, Name: row.Name}) {
		return fmt.Sprintf("%q parses as %+v (%v), want %s %q", key, parsed, err, row.Kind, row.Name)
	}
	return ""
}

// TestAStoredKeyKeepsItsBytes: each key a merge-base build stored, read from a fixture of its rows
// (testdata/merge-base-keys.json), equals the key this package builds for the same destination
// today, and a session's or a role's parses back to its kind and name.
//
// Control, in the test: the new bus subject of each session and role row, the one that names a
// project, used as the stored key fails the check, naming it: a build that stored its bus subjects
// would leave every earlier row unread.
func TestAStoredKeyKeepsItsBytes(t *testing.T) {
	rows := readStoredRows(t)
	for _, row := range rows {
		if wrong := misread(row, row.Key); wrong != "" {
			t.Errorf("%s: %s", row.Column, wrong)
		}
	}
	controls := 0
	for _, row := range rows {
		var subject string
		switch row.Kind {
		case "session":
			subject = contracts.AgentSubject("PRIV", row.Name)
		case "role":
			subject = contracts.RoleSubject("PRIV", row.Name)
		case "issue":
			subject = contracts.DispatchIssueSubject("PRIV", row.Name, row.Type)
			if issue, _, ok := ParseIssue(subject); ok {
				t.Errorf("control: ParseIssue reads the bus subject %q as issue %q, want it refused", subject, issue)
			}
		default:
			continue
		}
		controls++
		if misread(row, subject) == "" {
			t.Errorf("control: the bus subject %q passed as the stored key of %s %q, want the check to fail naming it", subject, row.Kind, row.Name)
		}
	}
	if controls == 0 {
		t.Fatal("UNKNOWN: the control read no row")
	}
}
