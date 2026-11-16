package textcheck

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Two doc tests hold regions of docs/kubernetes.md to the code those regions document.
// internal/daemon's workspace_copy_doc_test.go holds the copy's cadence paragraphs and the
// paragraph on what each kind of pod end loses and keeps to the running copy's constants;
// internal/runtime/sandbox's holds the extended-run-time annotation's bullet, the paragraphs on an
// end from outside the pod and the two-kinds and AWS paragraphs of the copy to the runtime's
// annotations and pod-end reasons. That comparison is in two halves, because a test that reads the
// guide puts the guide in its job's filter — a declared read applies to every compiled source file
// of a run — and a documentation-only edit would then start the whole daemon Go lane. The product
// halves read workspace_copy_doc_extract.txt, a copy of their regions committed beside each of the
// two packages, through internal/testdoc; this half holds each region of both copies to the guide
// itself. Neither half alone is the guarantee: together they are the one the two tests used to
// give when they read the guide.
//
// What it proves: each region of either copy is the run of the guide's lines, byte for byte, that
// the region's own two markers bound, and those are the markers its product test reads it by,
// since internal/testdoc fails a test whose markers name no region of its copy. What it does not:
// that a region states a fact or a constant, which is the product halves', and anything of a
// region neither copy carries.
const (
	// The committed copies, each read from the package whose test reads it, and the guide, read
	// under ../../../.. by shippedText.
	daemonWorkspaceCopyExtract  = "../daemon/workspace_copy_doc_extract.txt"
	sandboxWorkspaceCopyExtract = "../runtime/sandbox/workspace_copy_doc_extract.txt"
	workspaceCopyGuide          = "docs/kubernetes.md"
	// The two lines that open a region of either copy, as the files' own headers describe them:
	// `from: ` and the start marker, then `to: ` and the end marker on the line under it.
	workspaceCopyRegionFrom = "from: "
	workspaceCopyRegionTo   = "to: "
)

// workspaceCopyRegion is one region of a committed copy: the two markers its opening lines name,
// and the guide's lines it copies under them.
type workspaceCopyRegion struct {
	start, end string
	lines      []string
}

// Each region of the two committed copies is the guide's own lines between the region's two
// markers. An edit to either side alone fails here: a sentence reworded in the guide and not in a
// copy would otherwise never reach the product test, which reads the copy and not the guide; and a
// region copied from another place, or a marker the guide no longer bounds that region by, would
// hold that test's facts to lines the guide does not carry where an operator reads them. The
// failure prints the guide's lines, so the fix is a paste.
func TestTheWorkspaceCopyDocExtractsAreTheGuidesOwnLines(t *testing.T) {
	guide := strings.Split(shippedText(t, workspaceCopyGuide), "\n")
	flat, lineOf := flattenKeepingLines(guide)
	for _, extract := range []string{daemonWorkspaceCopyExtract, sandboxWorkspaceCopyExtract} {
		for _, region := range workspaceCopyExtractRegions(t, extract) {
			first, last, ok := workspaceCopyGuideRun(t, flat, lineOf, extract, region)
			if !ok {
				continue
			}
			bounded := guide[first : last+1]
			if slices.Equal(region.lines, bounded) {
				continue
			}
			paste := strings.Join(bounded, "\n")
			if at := workspaceCopyRunAt(guide, region.lines); at >= 0 {
				t.Errorf("%s's region from %q to %q is lines %d-%d of %s, but the guide's lines between those "+
					"two markers are %d-%d: the doc test beside the copy reads the region by those markers, so "+
					"its facts would be held to lines the guide carries elsewhere. Replace the region with the "+
					"lines the markers bound:\n\n%s",
					extract, region.start, region.end, at+1, at+len(region.lines), workspaceCopyGuide,
					first+1, last+1, paste)
				continue
			}
			t.Errorf("%s carries no run of lines that is %s's region from %q to %q, byte for byte; %s. The doc "+
				"test beside the copy reads the region and not the guide, so the two answer for the guide only "+
				"while they are equal. Repaste the region from the guide's lines %d-%d, in the same commit as "+
				"the edit that changed them:\n\n%s",
				workspaceCopyGuide, extract, region.start, region.end,
				workspaceCopyFirstDifference(region.lines, bounded), first+1, last+1, paste)
		}
	}
}

// workspaceCopyExtractRegions reads a committed copy into its regions, as its header describes
// them: above the first `from: ` line, the `#` header and blank lines; a `from: ` line opens a
// region and the `to: ` line under it names its end marker; every line after those two, to the next
// `from: ` line, is the region's own but for the blank lines that close it. The parse is a split and
// a switch and is deliberately not internal/testdoc's, which the product halves read the same files
// with: a copy of a document held to it by the same reader that reads the copy can drift with it and
// still agree.
//
// Every failure here stops the test by name, because a copy whose shape cannot be read is a copy
// nothing is being held to, and an absence reads exactly like a pass.
func workspaceCopyExtractRegions(t *testing.T, extract string) []workspaceCopyRegion {
	t.Helper()
	var regions []workspaceCopyRegion
	for i, line := range strings.Split(readExtract(t, extract), "\n") {
		open := len(regions) - 1
		switch {
		case strings.HasPrefix(line, workspaceCopyRegionFrom):
			start := strings.TrimPrefix(line, workspaceCopyRegionFrom)
			if start == "" {
				t.Fatalf("%s:%d: %q names no start marker, so the guide's lines it copies cannot be found", extract, i+1, line)
			}
			regions = append(regions, workspaceCopyRegion{start: start})
		case open < 0:
			if line != "" && !strings.HasPrefix(line, "#") {
				t.Fatalf("%s:%d: %q stands above the first %q line, where only the `#` header is, so which "+
					"region of the guide it copies cannot be read", extract, i+1, line, workspaceCopyRegionFrom)
			}
		case regions[open].end == "":
			end, named := strings.CutPrefix(line, workspaceCopyRegionTo)
			if !named || end == "" {
				t.Fatalf("%s:%d: the region from %q has no %q line naming its end marker under its %q line, so "+
					"where the guide's lines it copies end cannot be read", extract, i+1, regions[open].start,
					workspaceCopyRegionTo, workspaceCopyRegionFrom)
			}
			regions[open].end = end
		default:
			regions[open].lines = append(regions[open].lines, line)
		}
	}
	// An empty copy is this check failing open: the file is still there, the parse matched nothing,
	// and the product halves would then be held to regions nobody is reading.
	if len(regions) == 0 {
		t.Fatalf("%s holds no region: its copy of the guide is gone, and the doc test beside it is held to "+
			"nothing", extract)
	}
	for i := range regions {
		lines := regions[i].lines
		for len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if regions[i].end == "" || len(lines) == 0 {
			t.Fatalf("%s's region from %q has no %q line or no line of the guide under it, so it copies "+
				"nothing the guide could be held to", extract, regions[i].start, workspaceCopyRegionTo)
		}
		regions[i].lines = lines
	}
	return regions
}

// workspaceCopyGuideRun is the run of the guide's lines that region's two markers bound, as the
// indexes of its first and last lines: from the line the start marker opens on through the line
// the end marker finishes on, the bound internal/testdoc reads the region by in the copy. The
// markers are prose and the guide is wrapped, so they are found in the flattened guide
// (flattenKeepingLines, reclaim_doc_test.go) and carried back to its lines. A marker the guide does
// not carry, or carries where it no longer bounds one run, fails naming it and answers false, so no
// region is compared with lines nobody chose.
func workspaceCopyGuideRun(t *testing.T, flat string, lineOf []int, extract string, region workspaceCopyRegion) (first, last int, ok bool) {
	t.Helper()
	startOffset := strings.Index(flat, region.start)
	if startOffset < 0 {
		t.Errorf("%s has no %q, the start marker of %s's region: the guide's paragraph was reworded or moved, "+
			"and the doc test beside the copy reads words the guide no longer ships. Repaste the region and "+
			"its markers from the guide as it reads now, and change that test's marker with them",
			workspaceCopyGuide, region.start, extract)
		return 0, 0, false
	}
	if strings.Contains(flat[startOffset+len(region.start):], region.start) {
		t.Errorf("%s has %q more than once, so it no longer says where %s's region starts: the region could "+
			"be held to whichever comes first. Choose a start marker the guide carries once, in the copy and "+
			"in the doc test beside it", workspaceCopyGuide, region.start, extract)
		return 0, 0, false
	}
	endOffset := strings.Index(flat[startOffset:], region.end)
	if endOffset < 0 {
		t.Errorf("%s has no %q after %q, so the end of %s's region moved: an unbounded region would hold the "+
			"doc test's facts to whatever the guide carries after it", workspaceCopyGuide, region.end, region.start,
			extract)
		return 0, 0, false
	}
	return lineOf[startOffset], lineOf[startOffset+endOffset+len(region.end)-1], true
}

// workspaceCopyRunAt is the index of the first guide line of a run equal to lines, or -1 when the
// guide carries no such run: where a region that is not the one its markers bound was copied from.
func workspaceCopyRunAt(guide, lines []string) int {
	for at := 0; at+len(lines) <= len(guide); at++ {
		if slices.Equal(guide[at:at+len(lines)], lines) {
			return at
		}
	}
	return -1
}

// workspaceCopyFirstDifference names the first line a region and the guide's run disagree on, so a
// failing run points at the edit rather than at two blocks to compare by eye.
func workspaceCopyFirstDifference(region, guide []string) string {
	line := func(lines []string, i int) string {
		if i < len(lines) {
			return fmt.Sprintf("%q", lines[i])
		}
		return "no line"
	}
	for i := range max(len(region), len(guide)) {
		if i >= len(region) || i >= len(guide) || region[i] != guide[i] {
			return fmt.Sprintf("its line %d is %s in the copy and %s in the guide", i+1, line(region, i), line(guide, i))
		}
	}
	return "the two are equal"
}
