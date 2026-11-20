package jjmerge

import "testing"

// The Legion-Merge trailer joins the description's last paragraph when that paragraph is a block
// of trailers, as a pane's overlay writes one, and is a paragraph of its own otherwise. The
// subject is never a trailer block, though a conventional subject reads as `Key: value`: a
// trailer joined to it would become part of the subject.
func TestWithTrailerPutsTheLineLast(t *testing.T) {
	const line = "Legion-Merge: 0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct{ description, want string }{
		{"Merge main\n\nLegion-Role: implementer\n", "Merge main\n\nLegion-Role: implementer\n" + line + "\n"},
		{"Merge main\n\nOmp-Session: s-1\nLegion-Role: tester\n", "Merge main\n\nOmp-Session: s-1\nLegion-Role: tester\n" + line + "\n"},
		{"Merge main\n", "Merge main\n\n" + line + "\n"},
		{"fix: merge main\n", "fix: merge main\n\n" + line + "\n"},
		{"Merge main\n\nThe body says why.\n", "Merge main\n\nThe body says why.\n\n" + line + "\n"},
		{"Merge main\n\nLegion-Role: implementer\nnot a trailer\n", "Merge main\n\nLegion-Role: implementer\nnot a trailer\n\n" + line + "\n"},
	} {
		if got := withTrailer(tc.description, line); got != tc.want {
			t.Errorf("withTrailer(%q) = %q, want %q", tc.description, got, tc.want)
		}
	}
}

// A path is one file to jj whatever it holds: its quotes and backslashes are escaped inside the
// fileset's string, and nothing else of it is read as fileset syntax.
func TestRootFileNamesOnePath(t *testing.T) {
	for path, want := range map[string]string{
		"f":                `root-file:"f"`,
		"-odd (path) x":    `root-file:"-odd (path) x"`,
		`quote " and \ it`: `root-file:"quote \" and \\ it"`,
	} {
		if got := RootFile(path); got != want {
			t.Errorf("RootFile(%q) = %s, want %s", path, got, want)
		}
	}
}
