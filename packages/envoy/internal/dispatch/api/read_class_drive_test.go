package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"
)

// rowDrive is how a drive sends one GET row of the table on the seeded store: the request it
// makes.
type rowDrive struct {
	// target is the path and query the drive sends, every wildcard of the row's pattern filled.
	// The row's own pattern must match it (sendsItsRow), so a drive keyed by one row never sends
	// another.
	target string
}

// readRowDrives is the drive of every read row of the table (readWritesNothing), keyed
// "GET <pattern>" as readClasses is: the request that shows the row writes nothing a read may
// not write.
var readRowDrives = map[string]rowDrive{}

// getWriteDrives is the drive of every GET row of the table that writes (readWritesState), keyed
// as readRowDrives is: the request that makes the write the row's WriteNote names.
var getWriteDrives = map[string]rowDrive{}

// TestEveryGETRowHasADrive holds the two registries of drives to the read class: every GET row of
// the table with its test hooks in (readOnlyTable), read off the table, so a GET row added later
// fails here until it is driven. It fails for each read row with no drive in readRowDrives, each
// GET that writes with no drive in getWriteDrives, each GET row readClasses has not classed that
// has a drive in neither, and each drive that names no GET row of its class or whose target its
// row's pattern does not match. Every problem is collected first and the test fails once, naming
// each of them, so one run lists all that is missing.
func TestEveryGETRowHasADrive(t *testing.T) {
	patterns := map[string]string{}
	var problems []string
	for _, route := range readOnlyTable() {
		if route.Method != http.MethodGet {
			continue
		}
		name := route.Method + " " + route.Pattern
		patterns[name] = route.Pattern
		_, read := readRowDrives[name]
		_, write := getWriteDrives[name]
		switch readClasses[name].Writes {
		case readWritesNothing:
			if !read {
				problems = append(problems, name+": a read (readWritesNothing) with no drive in readRowDrives")
			}
		case readWritesState:
			if !write {
				problems = append(problems, name+": a GET that writes (readWritesState) with no drive in getWriteDrives")
			}
		default:
			if !read && !write {
				problems = append(problems, name+": a GET row readClasses has not classed, with no drive in readRowDrives or getWriteDrives")
			}
		}
	}
	if len(patterns) == 0 {
		t.Fatal("the table with its test hooks in holds no GET row, so the walk read nothing")
	}
	for _, registry := range []struct {
		name   string
		class  readWrites
		rows   string
		drives map[string]rowDrive
	}{
		{"readRowDrives", readWritesNothing, "read row (readWritesNothing)", readRowDrives},
		{"getWriteDrives", readWritesState, "GET that writes (readWritesState)", getWriteDrives},
	} {
		for name, drive := range registry.drives {
			pattern, mounted := patterns[name]
			switch {
			case !mounted || readClasses[name].Writes != registry.class:
				problems = append(problems, name+": "+registry.name+" drives it, and it is no "+registry.rows+" of the table")
			case !sendsItsRow(pattern, drive.target):
				problems = append(problems, name+": "+registry.name+" drives it at "+drive.target+", which its pattern does not match")
			}
		}
	}
	sort.Strings(problems)
	t.Logf("walked %d GET rows of the table (test hooks in); readRowDrives holds %d drives and getWriteDrives %d",
		len(patterns), len(readRowDrives), len(getWriteDrives))
	if len(problems) > 0 {
		t.Errorf("the drives (read_class_drive_test.go) have %d problems:\n%s", len(problems), strings.Join(problems, "\n"))
	}
}

// sendsItsRow reports whether a GET of target reaches the row of pattern: a mux that holds that
// row alone routes the request to it.
func sendsItsRow(pattern, target string) bool {
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	mux := http.NewServeMux()
	mux.HandleFunc(http.MethodGet+" "+pattern, func(http.ResponseWriter, *http.Request) {})
	_, matched := mux.Handler(request)
	return matched == http.MethodGet+" "+pattern
}
