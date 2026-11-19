package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// TestEveryGETRowHasOneReadEntry holds readClasses to the table it classes: every GET row of the
// table with its test hooks in (readOnlyTable, the table routesNotMounted walks too), read off
// the table rather than listed, so a GET row added later fails here until it has an entry. It
// fails for each GET row with no entry, each entry whose Writes is unset or no class at all, each
// readWritesState entry with no WriteNote, and each entry that names no GET row the table
// mounts. Every problem is collected first and the test fails once, naming each of them, so one
// run lists all that is missing.
func TestEveryGETRowHasOneReadEntry(t *testing.T) {
	gets := map[string]bool{}
	var problems []string
	for _, route := range readOnlyTable() {
		if route.Method != http.MethodGet {
			continue
		}
		name := route.Method + " " + route.Pattern
		gets[name] = true
		entry, classed := readClasses[name]
		switch {
		case !classed:
			problems = append(problems, name+": a GET row with no entry in readClasses")
		case entry.Writes == readWritesUnset:
			problems = append(problems, name+": its entry leaves Writes unset")
		case entry.Writes != readWritesNothing && entry.Writes != readWritesState:
			problems = append(problems, fmt.Sprintf("%s: its entry's Writes is %d, which is neither readWritesNothing nor readWritesState", name, entry.Writes))
		case entry.Writes == readWritesState && strings.TrimSpace(entry.WriteNote) == "":
			problems = append(problems, name+": a GET that writes (readWritesState) whose entry has no WriteNote naming what it writes")
		}
	}
	if len(gets) == 0 {
		t.Fatal("the table with its test hooks in holds no GET row, so the walk read nothing")
	}
	for name := range readClasses {
		if !gets[name] {
			problems = append(problems, name+": readClasses names it, and it is no GET row the table mounts (test hooks in)")
		}
	}
	sort.Strings(problems)
	t.Logf("walked %d GET rows of the table (test hooks in); readClasses holds %d entries", len(gets), len(readClasses))
	if len(problems) > 0 {
		t.Errorf("the read class (read_classes.go) has %d problems:\n%s", len(problems), strings.Join(problems, "\n"))
	}
}
