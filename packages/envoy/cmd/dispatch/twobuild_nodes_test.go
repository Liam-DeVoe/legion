//go:build twobuild

// The node half of the two-build suite: what a document's node is left as when the build before
// this one, which writes no node, creates or removes a document beside this build during a roll
// (twobuild_harness_test.go holds the harness every half starts its builds with).

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A document's node is addressed by its ref_key, and the build before this one writes none, so
// the orders a roll can mix leave a node the census has to name, each with its cure:
//
//   - This build removes a document, stating its node gone, and the previous build makes a
//     document of the same issue and name, which takes the same address: the row stands behind a
//     gone node. The census names it gone, and envoy-dispatch backfill-graph-store makes the node
//     live from the row's own content.
//   - This build makes a document, writing its node, and the previous build removes it: the node
//     stands live behind no row. The census names it orphaned, with the command that removes it,
//     the backfill leaves it as it is, and that command, run as the census printed it, removes it.
//   - The previous build removes this build's document and this build makes it again: the create
//     finds the orphaned node at the free address and states it gone, then live, under its own
//     event, and the census names nothing.
//   - The previous build removes this build's document and makes it again itself: the live node is
//     the first row's, by its create event, so the census names the second row gone, and the
//     backfill states the node gone, then live, under the second row's own event.
//
// After each cure the census names no project row or node at all. Each order has its control. The
// first, second and fourth are this file run in the tree at the branch's head before these classes
// and their cures: there this build's removal states nothing gone and the census prints no gone or
// orphaned field, so the first order fails at the node's state and the second and fourth at their
// first census. The third's is the gone step taken out of this build's create (api's
// storeArtifact): the node then keeps the removed row's live statement, and the census names the
// new row gone.
func TestTwoBuildsLeaveADocumentsNodeTheCensusNamesAndItsCureClears(t *testing.T) {
	builds := newTwoBuilds(t)
	builds.startPrevious(t, nil)
	builds.startCurrent(t, nil)
	previous, current := builds.previous(), builds.current()
	t.Logf("the %s serves beside the %s", builds.previousName(), builds.currentName())
	project := uniqueProjectKey(t, "NODE")
	current.createProject(t, project, "Two builds' document nodes")
	issue, _ := createDocument(t, current, project, "Documents two builds remove and make", "The spec.\n")

	t.Run("this build removes a document and the previous build makes it again", func(t *testing.T) {
		removed := makeUntouchedDocument(t, current, issue, "Made again.md")
		removeUntouchedDocument(t, current, removed)
		requireDocumentNodeState(t, builds, removed.ref, "gone")
		again := makeUntouchedDocument(t, previous, issue, "Made again.md")
		if again.ref != removed.ref {
			t.Fatalf("the %s made the document at %s, want the removed one's address %s, so this row proves nothing",
				builds.previousName(), again.ref, removed.ref)
		}
		census := runStoreCensus(t, builds)
		requireProjectLine(t, census, "artifact", "gone=1", "orphaned=0")
		requireIncompleteLines(t, census, "artifact "+again.ref+": gone")
		runCurrentCommand(t, builds, []int{0}, "backfill-graph-store")
		requireDocumentNodeState(t, builds, again.ref, "live")
		requireIncompleteLines(t, runStoreCensus(t, builds))
	})

	t.Run("this build makes a document and the previous build removes it", func(t *testing.T) {
		document := makeUntouchedDocument(t, current, issue, "Removed by the previous build.md")
		requireDocumentNodeState(t, builds, document.ref, "live")
		removeUntouchedDocument(t, previous, document)
		erase := "envoy-dispatch erase --node artifact:" + document.ref
		census := runStoreCensus(t, builds)
		requireProjectLine(t, census, "artifact", "gone=0", "orphaned=1")
		if !requireIncompleteLines(t, census, "artifact "+document.ref+": orphaned; remove it with: "+erase) {
			t.FailNow()
		}
		runCurrentCommand(t, builds, []int{0}, "backfill-graph-store")
		requireDocumentNodeState(t, builds, document.ref, "live")
		census = runStoreCensus(t, builds)
		if !requireIncompleteLines(t, census, "artifact "+document.ref+": orphaned; remove it with: "+erase) {
			t.FailNow()
		}
		printed := strings.TrimPrefix(incompleteLines(census)[0], "artifact "+document.ref+": orphaned; remove it with: ")
		listing := runPrinted(t, builds, 1, printed)
		digest := regexp.MustCompile(`(?m)^  listing digest: ([0-9a-f]{64})$`).FindStringSubmatch(listing)
		if digest == nil {
			t.Fatalf("the printed command %q listed no digest:\n%s", printed, listing)
		}
		runPrinted(t, builds, 0, printed+" --confirm "+digest[1])
		requireDocumentNodeState(t, builds, document.ref, "(no node)")
		requireIncompleteLines(t, runStoreCensus(t, builds))
	})

	t.Run("the previous build removes this build's document and this build makes it again", func(t *testing.T) {
		document := makeUntouchedDocument(t, current, issue, "Made again here.md")
		removeUntouchedDocument(t, previous, document)
		again := makeUntouchedDocument(t, current, issue, "Made again here.md")
		if again.ref != document.ref {
			t.Fatalf("this build made the document at %s, want the removed one's address %s, so this row proves nothing", again.ref, document.ref)
		}
		requireDocumentNodeState(t, builds, again.ref, "live")
		requireIncompleteLines(t, runStoreCensus(t, builds))
	})

	t.Run("the previous build removes this build's document and makes it again", func(t *testing.T) {
		document := makeUntouchedDocument(t, current, issue, "Made again there.md")
		removeUntouchedDocument(t, previous, document)
		again := makeUntouchedDocument(t, previous, issue, "Made again there.md")
		if again.ref != document.ref {
			t.Fatalf("the %s made the document at %s, want the removed one's address %s, so this row proves nothing",
				builds.previousName(), again.ref, document.ref)
		}
		census := runStoreCensus(t, builds)
		requireProjectLine(t, census, "artifact", "gone=1", "orphaned=0")
		requireIncompleteLines(t, census, "artifact "+again.ref+": gone")
		runCurrentCommand(t, builds, []int{0}, "backfill-graph-store")
		requireDocumentNodeState(t, builds, again.ref, "live")
		requireIncompleteLines(t, runStoreCensus(t, builds))
	})
}

// untouchedDocument is a document made by typing a title and never written in, the one kind the
// removal route removes: its id and the address its node is written under.
type untouchedDocument struct{ id, ref string }

// makeUntouchedDocument makes one of the issue's documents through p by typing its title.
func makeUntouchedDocument(t *testing.T, p *dispatchProcess, issue, name string) untouchedDocument {
	t.Helper()
	made := p.call(t, http.MethodPost, "/api/v1/issues/"+issue+"/artifacts", jsonBody(t, map[string]string{"name": name}), http.StatusCreated)
	var answer struct {
		Artifact struct {
			ID     string `json:"id"`
			RefKey string `json:"ref_key"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(made, &answer); err != nil || answer.Artifact.ID == "" || answer.Artifact.RefKey == "" {
		t.Fatalf("make %q on %s: %v: %s", name, issue, err, made)
	}
	return untouchedDocument{id: answer.Artifact.ID, ref: answer.Artifact.RefKey}
}

// removeUntouchedDocument removes the document through p.
func removeUntouchedDocument(t *testing.T, p *dispatchProcess, document untouchedDocument) {
	t.Helper()
	p.call(t, http.MethodDelete, "/api/v1/artifacts/"+document.id, "", http.StatusNoContent)
}

// requireDocumentNodeState fails unless the store holds the document node at ref in state, or
// holds none when state is "(no node)".
func requireDocumentNodeState(t *testing.T, builds *twoBuilds, ref, state string) {
	t.Helper()
	var held string
	if err := builds.database.Pool.QueryRow(context.Background(), `
		select coalesce((select state from graph_nodes where kind = 'artifact' and id = $1), '(no node)')
	`, ref).Scan(&held); err != nil {
		t.Fatalf("read the node of %s: %v", ref, err)
	}
	if held != state {
		t.Errorf("the node of %s is %s, want %s", ref, held, state)
	}
}

// runStoreCensus runs this tree's `envoy-dispatch census --store` and answers what it printed. It
// takes exit 0, a census that passed, and exit 1, one that refused: on a database holding no role,
// page or person the store census refuses those kinds for having no row to check, which says
// nothing of a document's node. Exit 2 is a census that could not be taken and printed no report,
// and fails the test, as does a report holding no line for the document kind: a cure is read from
// the report alone.
func runStoreCensus(t *testing.T, builds *twoBuilds) string {
	t.Helper()
	census := runCurrentCommand(t, builds, []int{0, 1}, "census", "--store")
	if !strings.Contains(census, "\ncensus: project kind=artifact ") && !strings.HasPrefix(census, "census: project kind=artifact ") {
		t.Fatalf("the census printed no project line for documents, so it read no document node:\n%s", census)
	}
	return census
}

// runCurrentCommand runs this tree's envoy-dispatch with args against the pair's database and
// answers its output, failing the test unless it exits with one of wants.
func runCurrentCommand(t *testing.T, builds *twoBuilds, wants []int, args ...string) string {
	t.Helper()
	return runCommand(t, builds, wants, strings.Join(args, " "), exec.Command(builds.binary, args...))
}

// runPrinted runs a command line as the census printed it, in a POSIX shell whose PATH finds this
// tree's envoy-dispatch first, against the pair's database, failing the test unless it exits with
// want. The shell reads the line as an operator's does, its quoting included.
func runPrinted(t *testing.T, builds *twoBuilds, want int, line string) string {
	t.Helper()
	command := exec.Command("sh", "-c", line)
	command.Env = append(command.Environ(), "PATH="+filepath.Dir(builds.binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if filepath.Base(builds.binary) != "envoy-dispatch" {
		t.Fatalf("this tree's server is built as %s, so no shell finds it as envoy-dispatch", builds.binary)
	}
	return runCommand(t, builds, []int{want}, line, command)
}

func runCommand(t *testing.T, builds *twoBuilds, wants []int, shown string, command *exec.Cmd) string {
	t.Helper()
	command.Env = append(command.Environ(), "DATABASE_URL="+builds.databaseURL(t))
	output, err := command.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatalf("run %s: %v", shown, err)
	}
	if !slices.Contains(wants, code) {
		t.Fatalf("%s exited %d, want one of %v:\n%s", shown, code, wants, output)
	}
	return string(output)
}

// requireProjectLine fails unless the census printed a `census: project kind=<kind>` line holding
// every field of want.
func requireProjectLine(t *testing.T, census, kind string, want ...string) {
	t.Helper()
	for _, line := range strings.Split(census, "\n") {
		if !strings.HasPrefix(line, "census: project kind="+kind+" ") {
			continue
		}
		fields := strings.Fields(line)
		for _, field := range want {
			if !slices.Contains(fields, field) {
				t.Errorf("the census's %s line reads %q, want %s", kind, line, field)
			}
		}
		return
	}
	t.Errorf("the census printed no line for the project kind %s:\n%s", kind, census)
}

// incompleteLines is every `census: project incomplete` line of the census, without that head.
func incompleteLines(census string) []string {
	var lines []string
	for _, line := range strings.Split(census, "\n") {
		if rest, ok := strings.CutPrefix(line, "census: project incomplete "); ok {
			lines = append(lines, rest)
		}
	}
	return lines
}

// requireIncompleteLines fails the test, and answers false, unless the census named exactly want as
// incomplete, in that order.
func requireIncompleteLines(t *testing.T, census string, want ...string) bool {
	t.Helper()
	got := incompleteLines(census)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the census named %q as incomplete, want %q:\n%s", got, want, census)
		return false
	}
	return true
}
