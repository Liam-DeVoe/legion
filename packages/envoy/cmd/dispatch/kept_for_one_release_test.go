package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// keptListPath is the list of what dispatch://INFRA-1150 keeps for one release, relative to the
// repository's root: one line for each kept thing, its id, the thing and what holds it, which the
// root's own pull request deletes one release later.
const keptListPath = "packages/dispatch/e2e/acceptance/root-deletes.txt"

// keptMarker is the comment that marks a place serving a kept thing: the words, a colon and the
// id, the id the last thing on its line. A sentence that only begins with the same words, such as
// one that goes on after a word, is no marker. It is built in two pieces so this file holds none.
var keptMarker = regexp.MustCompile("kept for one release" + `: ([a-z0-9][a-z0-9-]*)[ \t]*\r?$`)

// keptID is the shape of an id the list names.
var keptID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// The handoff's list of what this child keeps for one release is complete and names nothing that
// is gone (dispatch://INFRA-1150 R11, the plan's T12): every comment that marks a place serving a
// kept thing names an id the list holds, and every id the list holds is named by at least one such
// comment, in any file of the repository - Go, SQL, TypeScript, YAML or prose alike. A marker whose
// id the list does not hold is a kept thing nobody would delete; a listed id no marker names is a
// thing the root's pull request would look for and not find.
func TestEveryThingKeptForOneReleaseIsListed(t *testing.T) {
	root := repositoryRoot(t)
	listed := readKeptList(t, filepath.Join(root, keptListPath))
	if len(listed) == 0 {
		t.Fatalf("%s lists no kept thing", keptListPath)
	}
	marked := scanKeptMarkers(t, root)

	ids := make([]string, 0, len(marked))
	for id := range marked {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !listed[id] {
			t.Errorf("a marker names the id %q, which %s does not list: %s", id, keptListPath, strings.Join(marked[id], ", "))
		}
	}
	listedIDs := make([]string, 0, len(listed))
	for id := range listed {
		listedIDs = append(listedIDs, id)
	}
	sort.Strings(listedIDs)
	for _, id := range listedIDs {
		places := marked[id]
		if len(places) == 0 {
			t.Errorf("%s lists %q, and no marker in the repository names it", keptListPath, id)
			continue
		}
		t.Logf("%s: %s", id, strings.Join(places, ", "))
	}
}

// repositoryRoot is the root of the checkout this test runs in.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("find the repository's root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// readKeptList reads the ids of the list at path: each line that is neither blank nor a `#`
// comment is an id, a tab, the thing and a tab, then what holds it. A line of another shape, an id
// of another shape and an id listed twice each fail the test.
func readKeptList(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", keptListPath, err)
	}
	listed := map[string]bool{}
	for number, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || strings.TrimSpace(fields[1]) == "" || strings.TrimSpace(fields[2]) == "" {
			t.Errorf("%s:%d is not an id, the thing and what holds it, each after a tab: %q", keptListPath, number+1, line)
			continue
		}
		id := fields[0]
		if !keptID.MatchString(id) {
			t.Errorf("%s:%d names the id %q, which is no id", keptListPath, number+1, id)
			continue
		}
		if listed[id] {
			t.Errorf("%s:%d lists %q a second time", keptListPath, number+1, id)
		}
		listed[id] = true
	}
	return listed
}

// scanKeptMarkers is each id a marker names in a file of the checkout, tracked or new and not
// ignored, with each place that names it as path:line. A file holding a NUL byte is no text and is
// not read, and neither is the list itself.
func scanKeptMarkers(t *testing.T, root string) map[string][]string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("list the files of the checkout: %v", err)
	}
	marked := map[string][]string{}
	for _, name := range strings.Split(string(out), "\x00") {
		if name == "" || name == keptListPath {
			continue
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			// A file the index names and the working copy no longer holds is no place.
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", name, err)
		}
		// A symbolic link names a place the scan reads under its own path, so it is not read twice.
		if !info.Mode().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if bytes.IndexByte(raw, 0) >= 0 {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 0, 64*1024), len(raw)+1)
		for number := 1; scanner.Scan(); number++ {
			if match := keptMarker.FindStringSubmatch(scanner.Text()); match != nil {
				marked[match[1]] = append(marked[match[1]], name+":"+strconv.Itoa(number))
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
	}
	return marked
}
