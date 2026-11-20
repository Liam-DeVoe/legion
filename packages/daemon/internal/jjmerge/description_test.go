package jjmerge

import "testing"

// The Legion-Merge trailer joins the description's last paragraph when that paragraph is a block
// of trailers, as a pane's overlay writes one, and is a paragraph of its own otherwise. The
// subject is never a trailer block, though a conventional subject reads as `Key: value`: a
// trailer joined to it would become part of the subject. A line that starts with a space or a tab
// after a trailer continues that trailer, as git and jj read it, so the block stays one. A
// paragraph whose first line starts so is no block.
func TestWithTrailerPutsTheLineLast(t *testing.T) {
	const line = "Legion-Merge: 0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct{ description, want string }{
		{"Merge main\n\nLegion-Role: implementer\n", "Merge main\n\nLegion-Role: implementer\n" + line + "\n"},
		{"Merge main\n\nOmp-Session: s-1\nLegion-Role: tester\n", "Merge main\n\nOmp-Session: s-1\nLegion-Role: tester\n" + line + "\n"},
		{"Merge main\n", "Merge main\n\n" + line + "\n"},
		{"fix: merge main\n", "fix: merge main\n\n" + line + "\n"},
		{"Merge main\n\nThe body says why.\n", "Merge main\n\nThe body says why.\n\n" + line + "\n"},
		{"Merge main\n\nLegion-Role: implementer\nnot a trailer\n", "Merge main\n\nLegion-Role: implementer\nnot a trailer\n\n" + line + "\n"},
		{"Merge main\n\nKey: value\n  continued here\n", "Merge main\n\nKey: value\n  continued here\n" + line + "\n"},
		{"Merge main\n\nKey: value\n\tcontinued here\n  and here\n", "Merge main\n\nKey: value\n\tcontinued here\n  and here\n" + line + "\n"},
		{"Merge main\n\n  indented text\nKey: value\n", "Merge main\n\n  indented text\nKey: value\n\n" + line + "\n"},
	} {
		if got := withTrailer(tc.description, line); got != tc.want {
			t.Errorf("withTrailer(%q) = %q, want %q", tc.description, got, tc.want)
		}
	}
}

// The run's line leaves the description as jj would have written it with no such line: the
// trailers jj added beside it stay in their paragraph, and a paragraph that held the line alone,
// as jj writes it with no trailer configured, goes with it. Where the message ends in a block of
// trailers, jj puts the configured trailers in that block when it is given the message alone, so
// the trailers that were beside the line join that block. A trailer folded onto a next line that
// starts with a space or a tab keeps the block one. They join neither a body nor the subject,
// though a conventional subject reads as `Key: value`, nor a paragraph whose first line starts
// with a space. Any other paragraph stays as it is.
func TestWithoutLineTakesTheRunsLineOut(t *testing.T) {
	const line = "Legion-Merge-Run: legion-merge-default-0123456789abcdef-"
	for _, tc := range []struct{ description, want string }{
		{"Merge main\n\n" + line + "\nLegion-Role: implementer\n", "Merge main\n\nLegion-Role: implementer\n"},
		{"Merge main\n\n" + line + "\n", "Merge main\n"},
		{"Merge main\n\nThe body says why.\n\n" + line + "\n", "Merge main\n\nThe body says why.\n"},
		{"Merge main\n\nLegion-Role: implementer\n", "Merge main\n\nLegion-Role: implementer\n"},
		{"Merge main\n\nThe body says why.\n\nSigned-off-by: a <a@b>\n\n" + line + "\nLegion-Role: implementer\n", "Merge main\n\nThe body says why.\n\nSigned-off-by: a <a@b>\nLegion-Role: implementer\n"},
		{"Merge main\n\nSigned-off-by: a <a@b>\n\n" + line + "\n", "Merge main\n\nSigned-off-by: a <a@b>\n"},
		{"Merge main\n\nThe body says why.\n\n" + line + "\nLegion-Role: implementer\n", "Merge main\n\nThe body says why.\n\nLegion-Role: implementer\n"},
		{"fix: x\n\n" + line + "\nLegion-Role: implementer\n", "fix: x\n\nLegion-Role: implementer\n"},
		{"Merge main\n\nKey: value\n  continued here\n\n" + line + "\nLegion-Role: implementer\n", "Merge main\n\nKey: value\n  continued here\nLegion-Role: implementer\n"},
		{"Merge main\n\n  indented text\nKey: value\n\n" + line + "\nLegion-Role: implementer\n", "Merge main\n\n  indented text\nKey: value\n\nLegion-Role: implementer\n"},
	} {
		if got := withoutLine(tc.description, line); got != tc.want {
			t.Errorf("withoutLine(%q) = %q, want %q", tc.description, got, tc.want)
		}
	}
}
