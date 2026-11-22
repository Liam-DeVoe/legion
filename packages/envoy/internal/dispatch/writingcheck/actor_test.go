package writingcheck

import "testing"

func TestChecksFollowsTheActorRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, kind, id, act string
		checked             bool
		exemption           string
	}{
		{"agent session", "session", "01a1d1e8-0000-7000-8000-000000000001", "", true, ""},
		{"agent session with an act", "session", "01a1d1e8-0000-7000-8000-000000000001", "other", true, ""},
		{"personal token session", "session", "", "", true, ""},
		{"daemon session", "session", "legion-daemon:PROJ", "", false, ExemptDaemon},
		{"person at a browser", "user", "someone@example.com", "", false, ExemptPerson},
		{"agent acting for a person", "user", "someone@example.com", "01a1d1e8-0000-7000-8000-000000000002", true, ""},
		{"daemon acting for a person", "user", "someone@example.com", "legion-daemon:PROJ", false, ExemptDaemon},
		{"system", "system", "document-settlement", "", false, ExemptSystem},
		{"system with an act", "system", "importer", "01a1d1e8-0000-7000-8000-000000000003", false, ExemptSystem},
		{"no kind", "", "", "", false, ExemptSystem},
	} {
		exemption := Exemption(tc.kind, tc.id, tc.act)
		if checked := exemption == ""; checked != tc.checked || exemption != tc.exemption {
			t.Errorf("%s: Exemption(%q, %q, %q) = %q (checked %v), want %q (checked %v)",
				tc.name, tc.kind, tc.id, tc.act, exemption, checked, tc.exemption, tc.checked)
		}
	}
}
