//go:build twobuild

// The transition half of the two-build suite: what this tree's page admission does while a
// process of the previous build is live beside this tree's, and what it does once that process
// has left the registry (twobuild_harness_test.go holds the harness the builds start with, and
// twobuild_documents_test.go the page parameters and the socket a page opens).

package main

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/reearth/ygo/crdt"

	"github.com/sjawhar/envoy/internal/dispatch/docs/docstest"
	"github.com/sjawhar/envoy/internal/dispatch/processes"
)

// fleetDepartureSlack is what the wait below allows a loaded runner on top of the registry's own
// window, and fleetPollInterval how often it reads the registry. The wait is never slept through:
// a process stopped as a deploy stops it deletes its row at the end of its HTTP drain, so the
// row is usually gone by the first read.
const (
	fleetDepartureSlack = 5 * time.Second
	fleetPollInterval   = 200 * time.Millisecond
)

// fleetDepartureBound is how long a stopped process's row may still count as live: a live window
// after its last heartbeat (processes.LiveWindow), which may itself have been one heartbeat
// before it stopped (processes.HeartbeatInterval), and the slack above. A process that
// deregisters leaves at once and is well inside it; one killed without deregistering spends it.
func fleetDepartureBound() time.Duration {
	return processes.LiveWindow + processes.HeartbeatInterval + fleetDepartureSlack
}

// readOnlyEditSettle is how long an edit sent through a read-only socket is given to appear
// anywhere before the row reads the document again: past the document service's own settle delay
// (two seconds), so an edit the server took would be in the text by then.
const readOnlyEditSettle = 4 * time.Second

// transitionSeed is the document the row seeds, one paragraph a replacement is made over
// (replaceText), and transitionRender what a build renders it as.
const (
	transitionSeed   = "before"
	transitionRender = transitionSeed + "\n"
)

// openPageEditor connects an editor to artifactID's room on server with page's parameters - the
// query a dashboard of that build opens its document socket with, schema_creatable included - and
// returns once the editor's copy renders want. It is dispatchProcess.openEditor
// (shutdown_test.go) for a page whose numbers are not the answering process's own.
func openPageEditor(t *testing.T, server *dispatchProcess, artifactID string, page pageParameters, want string) *docstest.Peer {
	t.Helper()
	address := "ws://127.0.0.1:" + strconv.Itoa(server.port) + "/ws/doc/" + artifactID + "?" + page.query.Encode()
	editor := docstest.Dial(t, address, http.Header{"X-Dispatch-User": {dispatchTestLogin}}, artifactID, crdt.New())
	deadline := time.After(10 * time.Second)
	for markdown(editor) != want {
		if err := editor.AskForDocument(); err != nil {
			t.Fatalf("ask the room for the document: %v", err)
		}
		select {
		case <-editor.Answers:
		case <-editor.Ended:
			t.Fatalf("the editor's connection closed before the room sent the document:\n%s", server.Output.String())
		case <-deadline:
			t.Fatalf("the room never sent the editor the document:\n%s", server.Output.String())
		}
	}
	return editor
}

// waitForThePreviousBuildToLeaveTheFleet reads the registry until no live process reads below what
// page creates (fleetNumbers.readsBelow): this build's process reads all of it, so that is the
// previous build's process gone. It returns how long that took, polls rather than sleeps, and
// gives up at fleetDepartureBound.
func waitForThePreviousBuildToLeaveTheFleet(t *testing.T, builds *twoBuilds, page pageParameters) time.Duration {
	t.Helper()
	started := time.Now()
	for deadline := started.Add(fleetDepartureBound()); ; time.Sleep(fleetPollInterval) {
		fleet := builds.fleet(t)
		if !fleet.readsBelow(page) {
			return time.Since(started)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the %s still counts as live %s after it stopped: the fleet's lowest schema version is still %d, below the %d the page (%s) creates up to, past a live window (%s), a heartbeat (%s) and %s of slack",
				builds.previousName(), time.Since(started), fleet.minVersion, page.creatable, page, processes.LiveWindow, processes.HeartbeatInterval, fleetDepartureSlack)
		}
	}
}

// requireDocumentUnchanged fails unless both builds still render the document as want, and names
// what landed where they do not.
func requireDocumentUnchanged(t *testing.T, builds *twoBuilds, artifactID, want, when string) {
	t.Helper()
	for _, side := range []*dispatchProcess{builds.current(), builds.previous()} {
		if got := side.documentText(t, artifactID); got != want {
			t.Fatalf("%s: the %s renders the document as\n%q\nwant it untouched:\n%q", when, builds.buildName(side), got, want)
		}
	}
}

// This tree's page admission is the whole fleet's: no socket of a page is admitted writable while a
// live process reads a lower schema version than the page creates up to (docs.readSchemaFleet,
// docs.schemaFleet.admits). That process may register its numbers, as every build from the fleet
// columns on does, or count as reading version 0, as a build before them does. A release that
// leaves the schema where a registering previous build has it holds none of its pages read-only
// beside that build, so this row drives the transition of a release that raises the schema: this
// tree built as that release (newTwoBuildsRaisingTheSchema), whose page creates up to a version
// the previous build's process does not read. With both builds live, a document socket of that
// page on this build's server is admitted read-only and an edit sent through it lands nowhere;
// once the previous build's process has stopped and its row has left the registry, the socket a
// reload opens - the same page, the same numbers - is admitted writable, and an edit through it
// lands on this build's server.
//
// The wait for the row to leave is bounded by the registry's own numbers (fleetDepartureBound)
// and polled: a process stopped as a deploy stops it deletes its row at the end of its HTTP
// drain, so it leaves at once, and only a process killed without deregistering spends the window.
func TestThisBuildsPageTurnsWritableOnceThePreviousBuildsProcessLeavesTheFleet(t *testing.T) {
	builds, page := newTwoBuildsRaisingTheSchema(t)
	builds.startPrevious(t, nil)
	current := builds.startCurrent(t, nil)

	fleet := builds.fleet(t)
	if !fleet.readsBelow(page) {
		t.Fatalf("with the %s live, the fleet's lowest schema version is %d, which reads everything this build's page (%s) creates, up to %d: no live process reads below the page, so the fleet cannot hold it read-only",
			builds.previousName(), fleet.minVersion, page, page.creatable)
	}
	t.Logf("with the %s live, the fleet's lowest schema version is %d and its highest creatable %d; this build's page (%s) creates up to %d",
		builds.previousName(), fleet.minVersion, fleet.maxCreatable, page, page.creatable)

	project := uniqueProjectKey(t, "XBT")
	current.createProject(t, project, "Two builds, one leaving")
	_, artifactID := createDocument(t, current, project, "The fleet changes under an open page", transitionRender)

	socket := openDocumentSocket(t, current, artifactID, page)
	if socket.scope != readOnlyScope {
		t.Fatalf("with the %s live, the current build admits a socket of its own page (%s) as %q, want %q: the fleet's lowest schema version is %d, below the %d the page creates up to",
			builds.previousName(), page, socket.scope, readOnlyScope, fleet.minVersion, page.creatable)
	}

	editor := openPageEditor(t, current, artifactID, page, transitionRender)
	const refused = "typed while the previous build was live"
	replaceText(t, editor, transitionSeed, refused)
	time.Sleep(readOnlyEditSettle)
	requireDocumentUnchanged(t, builds, artifactID, transitionRender, "an edit sent through the read-only socket")
	if socket.closed() {
		t.Errorf("the current build closed the socket it admitted read-only: %v", socket.endErr)
	}
	editor.Close()

	stop(t, builds.previous())
	waited := waitForThePreviousBuildToLeaveTheFleet(t, builds, page)
	t.Logf("the %s left the fleet %s after its stop, inside the bound of %s (a live window, a heartbeat and %s of slack)",
		builds.previousName(), waited.Round(time.Millisecond), fleetDepartureBound(), fleetDepartureSlack)

	reloaded := openDocumentSocket(t, current, artifactID, page)
	if reloaded.scope != writableScope {
		t.Fatalf("once the %s had left the fleet, the current build admits a socket of the same page (%s) as %q, want %q",
			builds.previousName(), page, reloaded.scope, writableScope)
	}

	writing := openPageEditor(t, current, artifactID, page, transitionRender)
	const landed = "typed once the previous build had gone"
	replaceText(t, writing, transitionSeed, landed)
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := current.documentText(t, artifactID)
		if got == landed+"\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the edit made through the writable socket never landed: the current build renders\n%q\nwant\n%q", got, landed+"\n")
		}
		time.Sleep(fleetPollInterval)
	}
	if reloaded.closed() {
		t.Errorf("the current build closed the socket it admitted writable: %v", reloaded.endErr)
	}
}
