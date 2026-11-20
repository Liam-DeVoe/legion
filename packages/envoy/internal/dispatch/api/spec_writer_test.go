// spec_writer_test.go holds the behavior tests of R1 of INFRA-4764: on an issue Legion runs, a
// version of the spec or the Verification document from a writer who is no person and no
// architect of the issue, or of an issue above it, is refused with 403
// DOCUMENT_WRITER_NOT_ARCHITECT on every route that writes one (spec_writer.go). Its rig
// (specWriterServer, specWriterRig) builds a project of its own for each run, and uses only routes
// the stack's base serves, so the inventory runs there as the negative control of the checks it
// holds.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/testwait"
)

// specWriterRefusalCode is the code of the check's refusal.
const specWriterRefusalCode = "DOCUMENT_WRITER_NOT_ARCHITECT"

// TestSpecWriteRefusesAWorkerOnEveryRoute is the behavioral inventory of the routes that write a
// version of an issue's spec or Verification document (plan, "The inventory of routes"): each row
// of specWriteInventory runs once for each writer and each document its columns name, each run on
// a rig of its own (specWriterServer.rig), and each refused write is held to the check's 403
// naming the issue, the document, the writer's session and the architect sessions recorded on the
// issue's chain, and to a project that holds the same issues, document versions and issue events
// as before it (specWriterRig.requireRefused), with each row's own checks after it.
func TestSpecWriteRefusesAWorkerOnEveryRoute(t *testing.T) {
	server := newSpecWriterServer(t, testServerOptions{})
	for _, row := range specWriteInventory() {
		for _, writer := range row.writers {
			for _, document := range row.documents {
				t.Run(row.name+"/"+writer.name+"/"+document, func(t *testing.T) {
					rig := server.rig(t, row.shape)
					row.run(t, specWriteAt{rig: rig, writer: writer.session(rig), issue: writer.issue(rig), document: document})
				})
			}
		}
	}
}

// specWriteRow is one row of the inventory: the route it writes on, run for each of its writers on
// each of its documents, on a rig of shape.
type specWriteRow struct {
	name      string
	documents []string
	writers   []specWriteWriter
	shape     specWriterRigShape
	run       func(t *testing.T, at specWriteAt)
}

// specWriteWriter is who writes in a run, and on which issue of the rig.
type specWriteWriter struct {
	name    string
	session func(rig *specWriterRig) string
	issue   func(rig *specWriterRig) specWriterIssue
}

// The writers the inventory names. "Own" is C's tester writing C's document, the issue's own
// worker; "child's" is C's tester writing R's, a worker of a child of R, which holds no role on R;
// "parent's" is R's implementer writing C's, as check 5b1d7e42 runs it.
var (
	specWriterOwn = specWriteWriter{"own", func(rig *specWriterRig) string { return rig.cTester },
		func(rig *specWriterRig) specWriterIssue { return rig.c }}
	specWriterChilds = specWriteWriter{"child's", func(rig *specWriterRig) string { return rig.cTester },
		func(rig *specWriterRig) specWriterIssue { return rig.r }}
	specWriterParents = specWriteWriter{"parent's", func(rig *specWriterRig) string { return rig.rImplementer },
		func(rig *specWriterRig) specWriterIssue { return rig.c }}
	specWriterTesterOnSibling = specWriteWriter{"C's tester on D", func(rig *specWriterRig) string { return rig.cTester },
		func(rig *specWriterRig) specWriterIssue { return rig.d }}
	specWriterChildArchitect = specWriteWriter{"C's architect on R", func(rig *specWriterRig) string { return rig.cArchitect },
		func(rig *specWriterRig) specWriterIssue { return rig.r }}
)

// specWriterBoth is the two documents the check holds.
var specWriterBoth = []string{"spec.md", "verification.md"}

// specWriteInventory is the rows of the plan's inventory table, in its order.
func specWriteInventory() []specWriteRow {
	spec := []string{"spec.md"}
	sibling := specWriterRigShape{sibling: true}
	return []specWriteRow{
		{name: "upload a new version", documents: specWriterBoth, writers: []specWriteWriter{specWriterOwn, specWriterChilds, specWriterParents},
			run: func(t *testing.T, at specWriteAt) {
				at.rig.requireRefused(t, specWriteAttempt{
					what: fmt.Sprintf("%s's upload of a new version of %s's %s", at.writer, at.issue.key, at.document),
					send: func(t *testing.T) *httptest.ResponseRecorder {
						return at.rig.upload(t, at.writer, at.issue, at.document, documentMarkdown(t, at.rig.handler, at.artifact())+"\nA worker's paragraph.\n")
					},
					refusal: at.refusal(),
				})
			}},
		{name: "first upload", documents: []string{"verification.md"}, writers: []specWriteWriter{specWriterOwn, specWriterChilds},
			shape: specWriterRigShape{noVerification: true},
			run: func(t *testing.T, at specWriteAt) {
				at.rig.requireRefused(t, specWriteAttempt{
					what: fmt.Sprintf("%s's first upload of %s's %s", at.writer, at.issue.key, at.document),
					send: func(t *testing.T) *httptest.ResponseRecorder {
						return at.rig.upload(t, at.writer, at.issue, at.document, "A worker's verification.\n")
					},
					refusal: at.refusal(),
				})
			}},
		{name: "edit by id", documents: specWriterBoth, writers: []specWriteWriter{specWriterOwn, specWriterChilds, specWriterParents},
			run: func(t *testing.T, at specWriteAt) {
				at.rig.requireRefused(t, at.rig.editAttempt(at.writer, at.issue, at.document))
			}},
		{name: "edit by slug", documents: specWriterBoth, writers: []specWriteWriter{specWriterOwn, specWriterChilds},
			run: func(t *testing.T, at specWriteAt) {
				at.rig.requireRefused(t, specWriteAttempt{
					what: fmt.Sprintf("%s's edit of %s's %s by its slug", at.writer, at.issue.key, at.document),
					send: func(t *testing.T) *httptest.ResponseRecorder {
						return at.rig.send(t, at.writer, http.MethodPost, "/api/v1/issues/"+at.issue.key+"/artifacts/"+at.issue.documents[at.document].slug+"/edits",
							map[string]any{"ops": specWriterAppend})
					},
					refusal: at.refusal(),
				})
			}},
		{name: "edit that changes nothing", documents: spec, writers: []specWriteWriter{specWriterOwn},
			run: func(t *testing.T, at specWriteAt) {
				first := specWriterFirstLine(at.issue.name, at.document)
				at.rig.requireRefused(t, specWriteAttempt{
					what: fmt.Sprintf("%s's edit of %s's %s that replaces a line with itself", at.writer, at.issue.key, at.document),
					send: func(t *testing.T) *httptest.ResponseRecorder {
						return at.rig.send(t, at.writer, http.MethodPost, "/api/v1/artifacts/"+at.artifact()+"/edits",
							map[string]any{"ops": []map[string]string{{"op": "replace", "find": first, "with": first}}})
					},
					refusal: at.refusal(),
				})
			}},
		{name: "upload that changes nothing", documents: spec, writers: []specWriteWriter{specWriterOwn},
			run: func(t *testing.T, at specWriteAt) {
				at.rig.requireRefused(t, specWriteAttempt{
					what: fmt.Sprintf("%s's upload of %s's %s with its own text", at.writer, at.issue.key, at.document),
					send: func(t *testing.T) *httptest.ResponseRecorder {
						return at.rig.upload(t, at.writer, at.issue, at.document, documentMarkdown(t, at.rig.handler, at.artifact()))
					},
					refusal: at.refusal(),
				})
			}},
		{name: "edit with a summary", documents: spec, writers: []specWriteWriter{specWriterOwn, specWriterChilds},
			run: func(t *testing.T, at specWriteAt) {
				at.rig.requireRefused(t, specWriteAttempt{
					what: fmt.Sprintf("%s's edit of %s's %s with a summary", at.writer, at.issue.key, at.document),
					send: func(t *testing.T) *httptest.ResponseRecorder {
						return at.rig.send(t, at.writer, http.MethodPost, "/api/v1/artifacts/"+at.artifact()+"/edits",
							map[string]any{"ops": specWriterAppend, "summary": "A worker's named change"})
					},
					refusal: at.refusal(),
				})
			}},
		{name: "named version by id", documents: specWriterBoth, writers: []specWriteWriter{specWriterOwn, specWriterChilds},
			run: func(t *testing.T, at specWriteAt) {
				at.rig.requireRefused(t, at.rig.namedVersionAttempt(at.writer, at.issue, at.document, "/api/v1/artifacts/"+at.artifact()+"/versions"))
			}},
		{name: "named version by slug", documents: specWriterBoth, writers: []specWriteWriter{specWriterOwn, specWriterChilds},
			run: func(t *testing.T, at specWriteAt) {
				at.rig.requireRefused(t, at.rig.namedVersionAttempt(at.writer, at.issue, at.document,
					"/api/v1/issues/"+at.issue.key+"/artifacts/"+at.issue.documents[at.document].slug+"/versions"))
			}},
		{name: "named version that changes nothing", documents: spec, writers: []specWriteWriter{specWriterOwn},
			run: func(t *testing.T, at specWriteAt) {
				// A person names a version first, so the worker's is sent right after the last
				// version with no edit between.
				named := dispatchRequest(t, at.rig.handler, http.MethodPost, "/api/v1/artifacts/"+at.artifact()+"/versions",
					map[string]any{"summary": "A person's named version"}, "alice")
				if named.Code != http.StatusCreated {
					t.Fatalf("rig: a person names a version of %s's %s: status=%d body=%s", at.issue.key, at.document, named.Code, named.Body.String())
				}
				at.rig.quiet(t)
				at.rig.requireRefused(t, at.rig.namedVersionAttempt(at.writer, at.issue, at.document, "/api/v1/artifacts/"+at.artifact()+"/versions"))
			}},
		{name: "resolution", documents: specWriterBoth, writers: []specWriteWriter{specWriterOwn, specWriterChilds},
			run: func(t *testing.T, at specWriteAt) { at.requireAskCloseRefused(t, "resolved") }},
		{name: "retraction", documents: specWriterBoth, writers: []specWriteWriter{specWriterOwn, specWriterChilds},
			run: func(t *testing.T, at specWriteAt) { at.requireAskCloseRefused(t, "retracted") }},
		{name: "ask edit", documents: specWriterBoth, writers: []specWriteWriter{specWriterOwn, specWriterChilds},
			run: func(t *testing.T, at specWriteAt) {
				ask := at.rig.askIn(t, at.issue, at.document, "tester")
				at.rig.requireRefused(t, specWriteAttempt{
					what: fmt.Sprintf("%s's edit of its own block ask %s in %s's %s", at.writer, ask, at.issue.key, at.document),
					send: func(t *testing.T) *httptest.ResponseRecorder {
						return at.rig.send(t, at.writer, http.MethodPatch, "/api/v1/asks/"+ask, map[string]any{"question": "Which way does the worker go now?"})
					},
					refusal: at.refusal(),
				})
			}},
		{name: "create under a Legion-run parent", documents: spec, writers: []specWriteWriter{
			{"own: C's tester under C", specWriterOwn.session, specWriterOwn.issue},
			{"child's: C's tester under R", specWriterChilds.session, specWriterChilds.issue},
		}, run: func(t *testing.T, at specWriteAt) {
			title := "A worker's issue under " + at.issue.key
			at.rig.requireRefused(t, specWriteAttempt{
				what: fmt.Sprintf("%s's create of an issue under %s", at.writer, at.issue.key),
				send: func(t *testing.T) *httptest.ResponseRecorder {
					return at.rig.send(t, at.writer, http.MethodPost, "/api/v1/issues", map[string]any{
						"project": at.rig.project, "title": title, "parent": at.issue.key, "force": true,
					})
				},
				refusal: at.refusal(),
			})
			if keys := at.rig.issuesTitled(t, title); len(keys) > 0 {
				t.Errorf("the refused create left the issues %v titled %q, want none", keys, title)
			}
		}},
		{name: "move under a Legion-run parent", documents: spec, writers: []specWriteWriter{specWriterOwn},
			run: func(t *testing.T, at specWriteAt) {
				created := at.rig.send(t, at.writer, http.MethodPost, "/api/v1/issues", map[string]any{
					"project": at.rig.project, "title": "A worker's root", "labels": []string{"legion"}, "force": true,
				})
				if created.Code != http.StatusCreated {
					t.Fatalf("rig: %s creates a root with the label legion: status=%d body=%s", at.writer, created.Code, created.Body.String())
				}
				moved := decodeBody[struct {
					Key string `json:"key"`
				}](t, created).Key
				at.rig.quiet(t)
				refusal := at.refusal()
				refusal.issues = []string{moved, at.issue.key}
				at.rig.requireRefused(t, specWriteAttempt{
					what: fmt.Sprintf("%s's move of its root %s under %s", at.writer, moved, at.issue.key),
					send: func(t *testing.T) *httptest.ResponseRecorder {
						return at.rig.send(t, at.writer, http.MethodPatch, "/api/v1/issues/"+moved, map[string]any{"parent": at.issue.key})
					},
					refusal: refusal,
				})
				if parent := at.rig.issueRow(t, moved).parent; parent != nil {
					t.Errorf("the refused move left %s under %s, want it still a root", moved, *parent)
				}
			}},
		{name: "close", documents: spec, writers: []specWriteWriter{
			{"own: C's tester closes C", specWriterOwn.session, specWriterOwn.issue},
			{"child's: C's tester closes R", specWriterChilds.session, specWriterChilds.issue},
		}, run: func(t *testing.T, at specWriteAt) {
			at.requireCloseRefused(t, func(t *testing.T) *httptest.ResponseRecorder {
				return at.rig.send(t, at.writer, http.MethodPatch, "/api/v1/issues/"+at.issue.key, map[string]any{"status": "done"})
			})
		}},
		{name: "reopen", documents: spec, writers: []specWriteWriter{
			{"own: C's tester reopens C", specWriterOwn.session, specWriterOwn.issue},
			{"child's: C's tester reopens R", specWriterChilds.session, specWriterChilds.issue},
		}, run: func(t *testing.T, at specWriteAt) {
			at.requireReopenRefused(t, func(t *testing.T) *httptest.ResponseRecorder {
				return at.rig.send(t, at.writer, http.MethodPatch, "/api/v1/issues/"+at.issue.key, map[string]any{"status": "in_progress"})
			})
		}},
		{name: "label removal", documents: spec, writers: []specWriteWriter{{"child's: C's tester on R", specWriterChilds.session, specWriterChilds.issue}},
			shape: sibling,
			run: func(t *testing.T, at specWriteAt) {
				at.rig.requireRefused(t, specWriteAttempt{
					what: fmt.Sprintf("%s's removal of the label legion from %s", at.writer, at.issue.key),
					send: func(t *testing.T) *httptest.ResponseRecorder {
						return at.rig.send(t, at.writer, http.MethodPatch, "/api/v1/issues/"+at.issue.key, map[string]any{"labels": []string{}})
					},
					refusal: at.refusal(),
				}, at.rig.editAttempt(at.writer, at.rig.d, "spec.md"))
				if labels := at.rig.issueRow(t, at.issue.key).labels; !slices.Contains(labels, "legion") {
					t.Errorf("the refused label removal left %s with the labels %v, want legion among them", at.issue.key, labels)
				}
			}},
		{name: "parent clear", documents: spec, writers: []specWriteWriter{specWriterTesterOnSibling}, shape: sibling,
			run: func(t *testing.T, at specWriteAt) { at.requireParentWriteRefused(t, nil) }},
		{name: "parent clear to an unlabelled issue", documents: spec, writers: []specWriteWriter{specWriterTesterOnSibling},
			shape: specWriterRigShape{sibling: true, unlabelled: true},
			run:   func(t *testing.T, at specWriteAt) { at.requireParentWriteRefused(t, &at.rig.u2.key) }},
		{name: "status-change close", documents: spec, writers: []specWriteWriter{specWriterOwn},
			run: func(t *testing.T, at specWriteAt) {
				at.requireCloseRefused(t, func(t *testing.T) *httptest.ResponseRecorder {
					return at.rig.send(t, at.writer, http.MethodPost, "/api/v1/issues/"+at.issue.key+"/status-change", map[string]any{"status": "done"})
				})
			}},
		{name: "status-change reopen", documents: spec, writers: []specWriteWriter{specWriterOwn},
			run: func(t *testing.T, at specWriteAt) {
				at.requireReopenRefused(t, func(t *testing.T) *httptest.ResponseRecorder {
					return at.rig.send(t, at.writer, http.MethodPost, "/api/v1/issues/"+at.issue.key+"/status-change", map[string]any{"status": "in_progress"})
				})
			}},
		{name: "status-change reopen with include_children", documents: spec, writers: []specWriteWriter{specWriterOwn},
			run: func(t *testing.T, at specWriteAt) {
				at.requireReopenRefused(t, func(t *testing.T) *httptest.ResponseRecorder {
					return at.rig.send(t, at.writer, http.MethodPost, "/api/v1/issues/"+at.issue.key+"/status-change",
						map[string]any{"status": "in_progress", "include_children": true})
				})
			}},
		{name: "status-change unlink", documents: spec, writers: []specWriteWriter{{"C's tester on R", specWriterChilds.session, specWriterChilds.issue}},
			shape: sibling,
			run: func(t *testing.T, at specWriteAt) {
				// The unlink takes C and D out of R's tree, each a step of the call: the refusal
				// names the step it refuses.
				refusal := at.refusal()
				refusal.issues = []string{at.rig.c.key, at.rig.d.key}
				at.rig.requireRefused(t, specWriteAttempt{
					what: fmt.Sprintf("%s's status change of %s with unlink_children", at.writer, at.issue.key),
					send: func(t *testing.T) *httptest.ResponseRecorder {
						return at.rig.send(t, at.writer, http.MethodPost, "/api/v1/issues/"+at.issue.key+"/status-change",
							map[string]any{"status": "in_progress", "unlink_children": true})
					},
					refusal: refusal,
				}, at.rig.editAttempt(at.writer, at.rig.d, "spec.md"))
				for _, child := range []string{at.rig.c.key, at.rig.d.key} {
					if parent := at.rig.issueRow(t, child).parent; parent == nil || *parent != at.issue.key {
						t.Errorf("the refused unlink left %s under %s, want it still under %s", child, specWriterParentName(parent), at.issue.key)
					}
				}
			}},
		{name: "child's architect on the parent", documents: spec, writers: []specWriteWriter{specWriterChildArchitect},
			run: func(t *testing.T, at specWriteAt) {
				at.rig.requireRefused(t, at.rig.editAttempt(at.writer, at.issue, at.document))
			}},
	}
}

// specWriteAt is one run of a row: its rig, the writer's session, the issue whose document it
// writes, and the document's name.
type specWriteAt struct {
	rig      *specWriterRig
	writer   string
	issue    specWriterIssue
	document string
}

// artifact is the id of the run's document.
func (at specWriteAt) artifact() string {
	return at.issue.documents[at.document].id
}

// refusal is what the check's refusal of the run's write names.
func (at specWriteAt) refusal() specWriteRefusal {
	return at.rig.refusalOn(at.issue, at.document, at.writer)
}

// requireAskCloseRefused holds the resolution route (kind resolved or retracted) of the
// architect's block ask in the run's document to the check, and the ask to still being open.
func (at specWriteAt) requireAskCloseRefused(t *testing.T, kind string) {
	t.Helper()
	ask := at.rig.askIn(t, at.issue, at.document, "architect")
	at.rig.requireRefused(t, specWriteAttempt{
		what: fmt.Sprintf("%s's resolution (%s) of the architect's block ask %s in %s's %s", at.writer, kind, ask, at.issue.key, at.document),
		send: func(t *testing.T) *httptest.ResponseRecorder {
			return at.rig.send(t, at.writer, http.MethodPost, "/api/v1/asks/"+ask+"/resolve", map[string]any{"kind": kind, "reason": "A worker settles it."})
		},
		refusal: at.refusal(),
	})
	if state := at.rig.askState(t, ask); state != "open" {
		t.Errorf("the refused resolution left the ask %s %s, want it open", ask, state)
	}
}

// requireCloseRefused holds a close of the run's issue, while its spec holds the architect's open
// block ask, to the check: the issue is still open and no ask of it was withdrawn.
func (at specWriteAt) requireCloseRefused(t *testing.T, send func(t *testing.T) *httptest.ResponseRecorder) {
	t.Helper()
	if state := at.rig.askState(t, at.rig.askIn(t, at.issue, "spec.md", "architect")); state != "open" {
		t.Fatalf("rig: the architect's block ask in %s's spec is %s, want it open before the close", at.issue.key, state)
	}
	status := at.rig.issueRow(t, at.issue.key).status
	withdrawn := at.rig.eventCount(t, at.issue.key, "ask.withdrawn")
	at.rig.requireRefused(t, specWriteAttempt{
		what:    fmt.Sprintf("%s's close of %s", at.writer, at.issue.key),
		send:    send,
		refusal: at.refusal(),
	})
	if after := at.rig.issueRow(t, at.issue.key).status; after != status {
		t.Errorf("the refused close left %s %s, want it still %s", at.issue.key, after, status)
	}
	if after := at.rig.eventCount(t, at.issue.key, "ask.withdrawn"); after != withdrawn {
		t.Errorf("the refused close appended %d ask.withdrawn events on %s, want none", after-withdrawn, at.issue.key)
	}
}

// requireReopenRefused holds a reopen of the run's issue, after the issue's architect closed it, to
// the check: the issue is still closed and no ask event was appended.
func (at specWriteAt) requireReopenRefused(t *testing.T, send func(t *testing.T) *httptest.ResponseRecorder) {
	t.Helper()
	architect := at.rig.architectsOf(at.issue)[0]
	closed := at.rig.send(t, architect, http.MethodPatch, "/api/v1/issues/"+at.issue.key, map[string]any{"status": "done"})
	if closed.Code != http.StatusOK {
		t.Fatalf("rig: the architect %s closes %s: status=%d body=%s", architect, at.issue.key, closed.Code, closed.Body.String())
	}
	at.rig.quiet(t)
	asks := at.rig.eventCount(t, at.issue.key, "ask.%")
	at.rig.requireRefused(t, specWriteAttempt{
		what:    fmt.Sprintf("%s's reopen of %s", at.writer, at.issue.key),
		send:    send,
		refusal: at.refusal(),
	})
	if status := at.rig.issueRow(t, at.issue.key).status; status != "done" {
		t.Errorf("the refused reopen left %s %s, want it still done", at.issue.key, status)
	}
	if after := at.rig.eventCount(t, at.issue.key, "ask.%"); after != asks {
		t.Errorf("the refused reopen appended %d ask events on %s, want none", after-asks, at.issue.key)
	}
}

// requireParentWriteRefused holds a write of the run's issue's parent to parent (nil for none, or
// an issue no labelled issue is above), which takes the issue out of Legion, to the check, then the
// writer's edit of the issue's spec after it too, and the issue to still being under the parent it
// had.
func (at specWriteAt) requireParentWriteRefused(t *testing.T, parent *string) {
	t.Helper()
	before := at.rig.issueRow(t, at.issue.key).parent
	if before == nil {
		t.Fatalf("rig: %s has no parent before the write", at.issue.key)
	}
	at.rig.requireRefused(t, specWriteAttempt{
		what: fmt.Sprintf("%s's write of %s's parent to %s", at.writer, at.issue.key, specWriterParentName(parent)),
		send: func(t *testing.T) *httptest.ResponseRecorder {
			return at.rig.send(t, at.writer, http.MethodPatch, "/api/v1/issues/"+at.issue.key, map[string]any{"parent": parent})
		},
		refusal: at.refusal(),
	}, at.rig.editAttempt(at.writer, at.issue, "spec.md"))
	if after := at.rig.issueRow(t, at.issue.key).parent; after == nil || *after != *before {
		t.Errorf("the refused parent write left %s under %s, want it still under %s", at.issue.key, specWriterParentName(after), *before)
	}
}

// specWriterParentName is a stored parent as a failure line names it.
func specWriterParentName(parent *string) string {
	if parent == nil {
		return "no parent"
	}
	return *parent
}

// specWriterServer is the server a spec-writer test shares between its runs. Each run builds its
// rig in a project of its own (rig), so no run's write changes another's.
type specWriterServer struct {
	handler  http.Handler
	database *store.Store
	served   *server
	// projects is how many rigs the server has built; it numbers the next rig's project.
	projects int
}

func newSpecWriterServer(t *testing.T, options testServerOptions) *specWriterServer {
	t.Helper()
	handler, database, _, served := newRegisteredTestServer(t, options)
	return &specWriterServer{handler: handler, database: database, served: served}
}

// specWriterRigShape is what a rig holds beside R and C.
type specWriterRigShape struct {
	// noVerification leaves R and C without a Verification document, for a first upload of one.
	noVerification bool
	// sibling adds D, a child of R with no label and no role holder of its own: Legion runs it
	// only through R.
	sibling bool
	// unlabelled adds U2, a root of the project with no label.
	unlabelled bool
}

// specWriterIssue is one issue of a rig: the name its texts and block ids are written after
// (root, child, sibling, unlabelled), its key, and its documents by name (spec.md,
// verification.md).
type specWriterIssue struct {
	name      string
	key       string
	documents map[string]specWriterDocument
}

// specWriterDocument is one document of a rig's issue.
type specWriterDocument struct {
	id, slug string
}

// specWriterRig is one project P of its own on a specWriterServer: a root R with the label legion
// and a child C under it, each with a spec.md and, unless the shape says otherwise, a
// verification.md that a person wrote (dispatchRequest as alice), each document holding a block ask
// the person wrote - the architect's - and a block ask of C's tester's own, written by an edit
// under its session before the first role-holder publication; and then the role holders the
// daemon of P published on the shared token: R's architect and implementer, C's architect and
// tester. Each role holder is a session whose id names its role and the project, which a write on
// the shared token names as its actor.
type specWriterRig struct {
	*specWriterServer
	project                                       string
	r, c, d, u2                                   specWriterIssue
	rArchitect, rImplementer, cArchitect, cTester string
	// asks are the ids of the block asks the rig wrote, by block id (specWriterBlockID).
	asks map[string]string
}

// rig builds a rig of shape in a project of its own, and waits until nothing it wrote is still
// settling, so the run's first snapshot reads only what the run writes.
func (s *specWriterServer) rig(t *testing.T, shape specWriterRigShape) *specWriterRig {
	t.Helper()
	s.projects++
	project := fmt.Sprintf("SW%d", s.projects)
	createAdviceProject(t, s.handler, project)
	session := func(role string) string { return "ses-" + role + "-" + strings.ToLower(project) }
	rig := &specWriterRig{
		specWriterServer: s, project: project, asks: map[string]string{},
		rArchitect: session("r-architect"), rImplementer: session("r-implementer"),
		cArchitect: session("c-architect"), cTester: session("c-tester"),
	}
	rig.r = rig.file(t, "root", "", []string{"legion"})
	rig.c = rig.file(t, "child", rig.r.key, nil)
	if shape.sibling {
		rig.d = rig.file(t, "sibling", rig.r.key, nil)
	}
	if shape.unlabelled {
		rig.u2 = rig.file(t, "unlabelled", "", nil)
	}
	asked := map[*specWriterIssue][]string{}
	for _, issue := range []*specWriterIssue{&rig.r, &rig.c} {
		asked[issue] = append(asked[issue], specWriterBlockID(issue.name, "spec.md", "architect"))
		if !shape.noVerification {
			uploaded := uploadDocument(t, s.handler, "/api/v1/issues/"+issue.key, "verification.md", specWriterText(issue.name, "verification.md"))
			issue.documents["verification.md"] = specWriterDocument{id: uploaded.Artifact.ID, slug: uploaded.Artifact.Slug}
			asked[issue] = append(asked[issue], specWriterBlockID(issue.name, "verification.md", "architect"))
		}
	}
	// C's tester's own block asks, written while the project is not marked yet, so the ask-edit
	// rows have an ask of the writer's own to edit.
	for _, issue := range []*specWriterIssue{&rig.r, &rig.c} {
		for _, document := range slices.Sorted(maps.Keys(issue.documents)) {
			block := specWriterBlockID(issue.name, document, "tester")
			written := rig.send(t, rig.cTester, http.MethodPost, "/api/v1/artifacts/"+issue.documents[document].id+"/edits", map[string]any{
				"ops": []map[string]string{{"op": "insert", "after": "end", "markdown": specWriterAsk(block)}},
			})
			if written.Code != http.StatusOK {
				t.Fatalf("rig: C's tester writes its block ask into %s's %s: status=%d body=%s", issue.key, document, written.Code, written.Body.String())
			}
			asked[issue] = append(asked[issue], block)
		}
	}
	for _, issue := range []*specWriterIssue{&rig.r, &rig.c} {
		rig.awaitAsks(t, *issue, asked[issue])
	}
	rig.publish(t, rig.r.key, []issueRoleHolder{{Role: "architect", Session: rig.rArchitect}, {Role: "implementer", Session: rig.rImplementer}})
	rig.publish(t, rig.c.key, []issueRoleHolder{{Role: "architect", Session: rig.cArchitect}, {Role: "tester", Session: rig.cTester}})
	rig.quiet(t)
	return rig
}

// file creates an issue of the rig's project as a person, under parent when it is not empty, with
// labels, and with a spec of specWriterText's: R's and C's hold the architect's block ask.
func (rig *specWriterRig) file(t *testing.T, name, parent string, labels []string) specWriterIssue {
	t.Helper()
	body := map[string]any{"project": rig.project, "title": "The " + name + " issue", "spec": specWriterText(name, "spec.md"), "force": true}
	if parent != "" {
		body["parent"] = parent
	}
	if labels != nil {
		body["labels"] = labels
	}
	created := dispatchRequest(t, rig.handler, http.MethodPost, "/api/v1/issues", body, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("rig: a person creates the %s issue: status=%d body=%s", name, created.Code, created.Body.String())
	}
	issue := decodeBody[struct {
		Key               string `json:"key"`
		PrimaryArtifactID string `json:"primary_artifact_id"`
	}](t, created)
	var slug string
	if err := rig.database.Pool.QueryRow(context.Background(), `select slug from artifacts where id = $1`, issue.PrimaryArtifactID).Scan(&slug); err != nil {
		t.Fatalf("rig: read the slug of %s's spec: %v", issue.Key, err)
	}
	return specWriterIssue{name: name, key: issue.Key, documents: map[string]specWriterDocument{
		"spec.md": {id: issue.PrimaryArtifactID, slug: slug},
	}}
}

// specWriterText is the text a person writes into an issue's document: its first line
// (specWriterFirstLine), and, in R's and C's, the architect's block ask.
func specWriterText(issue, document string) string {
	text := specWriterFirstLine(issue, document) + "\n"
	if issue == "root" || issue == "child" {
		text += "\n" + specWriterAsk(specWriterBlockID(issue, document, "architect"))
	}
	return text
}

// specWriterFirstLine is the first line of an issue's document, which no other line repeats.
func specWriterFirstLine(issue, document string) string {
	return "The " + strings.TrimSuffix(document, ".md") + " of the " + issue + " issue."
}

// specWriterBlockID is the id of the block ask asker wrote (architect, tester) in an issue's
// document, unique on the issue so no ask is read as another's copy.
func specWriterBlockID(issue, document, asker string) string {
	return issue + "-" + strings.TrimSuffix(document, ".md") + "-" + asker
}

// specWriterAsk is a block ask with its own question.
func specWriterAsk(block string) string {
	return ":::ask{#" + block + " urgency=\"med\" multiple=\"false\"}\nWhich way does " + block + " go?\n\n" +
		"- One: The first way.\n- Two: The second way.\n:::\n"
}

// specWriterAppend is the edit a worker sends: a paragraph of its own at the end.
var specWriterAppend = []map[string]string{{"op": "insert", "after": "end", "markdown": "A worker's paragraph.\n"}}

// awaitAsks waits until settlement has indexed each of blocks on issue as an open ask, keeps each
// ask's id, and fails the rig when a tester's block ask is not credited to the tester, which the
// ask edit's NOT_AUTHOR would then refuse first.
func (rig *specWriterRig) awaitAsks(t *testing.T, issue specWriterIssue, blocks []string) {
	t.Helper()
	var asks []model.Ask
	indexed := func() bool {
		listed := dispatchRequest(t, rig.handler, http.MethodGet, "/api/v1/issues/"+issue.key+"/asks?state=all", nil, "alice")
		if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &asks) != nil {
			return false
		}
		for _, block := range blocks {
			if !slices.ContainsFunc(asks, func(ask model.Ask) bool { return ask.BlockID != nil && *ask.BlockID == block && ask.State == "open" }) {
				return false
			}
		}
		return true
	}
	testwait.Eventually(t, "the block asks "+strings.Join(blocks, ", ")+" of "+issue.key+" to be indexed open", indexed)
	for _, ask := range asks {
		if ask.BlockID == nil || !slices.Contains(blocks, *ask.BlockID) {
			continue
		}
		if strings.HasSuffix(*ask.BlockID, "-tester") && ask.Author.ID != rig.cTester {
			t.Fatalf("rig: the block ask %s of %s is credited to %#v, want C's tester %s", *ask.BlockID, issue.key, ask.Author, rig.cTester)
		}
		rig.asks[*ask.BlockID] = ask.ID
	}
}

// askIn is the id of the block ask asker wrote in issue's document.
func (rig *specWriterRig) askIn(t *testing.T, issue specWriterIssue, document, asker string) string {
	t.Helper()
	block := specWriterBlockID(issue.name, document, asker)
	id, written := rig.asks[block]
	if !written {
		t.Fatalf("rig: no block ask %s was written", block)
	}
	return id
}

// publish publishes issue's whole set of role holders as the daemon of the rig's project does.
func (rig *specWriterRig) publish(t *testing.T, issue string, holders []issueRoleHolder) {
	t.Helper()
	response := putRoleHoldersAs(t, rig.handler, issue, map[string]any{"holders": holders, "actor": daemonSessionOf(rig.project)})
	if response.Code != http.StatusOK {
		t.Fatalf("rig: the daemon of %s publishes %v on %s: status=%d body=%s", rig.project, holders, issue, response.Code, response.Body.String())
	}
}

// quiet waits until no document owes a settlement and no write the server left running past its
// answer is still running.
func (rig *specWriterRig) quiet(t *testing.T) {
	t.Helper()
	awaitNoSettlementOwed(t, rig.database)
	awaitBackgroundWrites(t, rig.served)
}

// send sends body on the shared token as session, which the body names as its actor.
func (rig *specWriterRig) send(t *testing.T, session, method, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	named := maps.Clone(body)
	if named == nil {
		named = map[string]any{}
	}
	named["actor"] = map[string]any{"kind": "session", "id": session}
	return sessionRequest(t, rig.handler, method, path, named)
}

// upload uploads content as issue's document name as session, naming the token of the version it
// replaces where the issue holds the document already.
func (rig *specWriterRig) upload(t *testing.T, session string, issue specWriterIssue, name, content string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"name": name, "content": content}
	if document, held := issue.documents[name]; held {
		body["precondition"] = uploadBase(t, rig.handler, document.id)
	}
	return rig.send(t, session, http.MethodPost, "/api/v1/issues/"+issue.key+"/artifacts", body)
}

// editAttempt is session's edit of issue's document by id (specWriterAppend), which the check
// refuses naming the issue, the document, the session and the issue's architects.
func (rig *specWriterRig) editAttempt(session string, issue specWriterIssue, document string) specWriteAttempt {
	return specWriteAttempt{
		what: fmt.Sprintf("%s's edit of %s's %s", session, issue.key, document),
		send: func(t *testing.T) *httptest.ResponseRecorder {
			return rig.send(t, session, http.MethodPost, "/api/v1/artifacts/"+issue.documents[document].id+"/edits", map[string]any{"ops": specWriterAppend})
		},
		refusal: rig.refusalOn(issue, document, session),
	}
}

// namedVersionAttempt is session's named version of issue's document on path.
func (rig *specWriterRig) namedVersionAttempt(session string, issue specWriterIssue, document, path string) specWriteAttempt {
	return specWriteAttempt{
		what: fmt.Sprintf("%s's named version of %s's %s (%s)", session, issue.key, document, path),
		send: func(t *testing.T) *httptest.ResponseRecorder {
			return rig.send(t, session, http.MethodPost, path, map[string]any{"summary": "A worker's named version"})
		},
		refusal: rig.refusalOn(issue, document, session),
	}
}

// architectsOf is the architect sessions the rig records on issue's chain, nearest first: C's
// architect and R's on C, and R's alone on R and on D, which has none of its own.
func (rig *specWriterRig) architectsOf(issue specWriterIssue) []string {
	if issue.key == rig.c.key {
		return []string{rig.cArchitect, rig.rArchitect}
	}
	return []string{rig.rArchitect}
}

// refusalOn is what the check's refusal of session's write of issue's document names.
func (rig *specWriterRig) refusalOn(issue specWriterIssue, document, session string) specWriteRefusal {
	return specWriteRefusal{issues: []string{issue.key}, document: document, writer: session, architects: rig.architectsOf(issue)}
}

// specWriteRefusal is what the check's refusal of one write names: one of issues (the issue the
// write is on), the document, the writer's session, and each architect session recorded on the
// issue's chain.
type specWriteRefusal struct {
	issues     []string
	document   string
	writer     string
	architects []string
}

// specWriteAttempt is one write of a run, and what its refusal names.
type specWriteAttempt struct {
	what    string
	send    func(t *testing.T) *httptest.ResponseRecorder
	refusal specWriteRefusal
}

// requireRefused sends each attempt in order, then fails t unless each was refused with 403
// DOCUMENT_WRITER_NOT_ARCHITECT naming what its refusal names, and the project holds the issues,
// the document versions and the issue events it held before the first.
func (rig *specWriterRig) requireRefused(t *testing.T, attempts ...specWriteAttempt) {
	t.Helper()
	before := rig.snapshot(t)
	responses := make([]*httptest.ResponseRecorder, len(attempts))
	for index, attempt := range attempts {
		responses[index] = attempt.send(t)
	}
	rig.quiet(t)
	for index, attempt := range attempts {
		requireSpecWriteRefused(t, attempt.what, responses[index], attempt.refusal)
	}
	rig.requireUnchanged(t, attempts[0].what, before)
}

// requireSpecWriteRefused fails t unless response is the check's refusal naming want.
func requireSpecWriteRefused(t *testing.T, what string, response *httptest.ResponseRecorder, want specWriteRefusal) {
	t.Helper()
	var refusal struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &refusal)
	if response.Code != http.StatusForbidden || refusal.Code != specWriterRefusalCode {
		t.Errorf("%s: status=%d body=%s, want 403 %s", what, response.Code, clippedBody(response), specWriterRefusalCode)
		return
	}
	if !slices.ContainsFunc(want.issues, func(key string) bool { return specWriterNames(refusal.Error, key) }) {
		t.Errorf("%s: the refusal %q names none of %v, the issue it writes", what, refusal.Error, want.issues)
	}
	for _, name := range append([]string{want.document, want.writer}, want.architects...) {
		if !specWriterNames(refusal.Error, name) {
			t.Errorf("%s: the refusal %q does not name %s", what, refusal.Error, name)
		}
	}
}

// specWriterNames reports whether message names word as a word of its own, so SW1-1 is not read
// in SW1-12 nor a session in a longer one.
func specWriterNames(message, word string) bool {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(word) + `($|[^A-Za-z0-9_-])`).MatchString(message)
}

// clippedBody is a response's body cut to a length a failure line can carry.
func clippedBody(response *httptest.ResponseRecorder) string {
	body := response.Body.String()
	if len(body) > 300 {
		return body[:300] + "..."
	}
	return body
}

// specWriterSnapshot is what a refused write leaves as it found it in the rig's project: its
// issues, each issue's events, and each document's versions, by "<issue>/<name>".
type specWriterSnapshot struct {
	issues   []string
	events   map[string]int
	versions map[string]int
}

func (rig *specWriterRig) snapshot(t *testing.T) specWriterSnapshot {
	t.Helper()
	ctx := context.Background()
	snapshot := specWriterSnapshot{events: map[string]int{}, versions: map[string]int{}}
	read := func(query string, each func(key string, count int)) {
		rows, err := rig.database.Pool.Query(ctx, query, rig.project)
		if err != nil {
			t.Fatalf("rig: read the project %s: %v", rig.project, err)
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var count int
			if err := rows.Scan(&key, &count); err != nil {
				t.Fatalf("rig: read the project %s: %v", rig.project, err)
			}
			each(key, count)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rig: read the project %s: %v", rig.project, err)
		}
	}
	read(`
		select i.key, (select count(*) from events e where e.issue_key = i.key)
		from issues i where i.project_key = $1 order by i.key
	`, func(key string, count int) {
		snapshot.issues = append(snapshot.issues, key)
		snapshot.events[key] = count
	})
	read(`
		select a.issue_key || '/' || a.name, (select count(*) from artifact_versions v where v.artifact_id = a.id)
		from artifacts a join issues i on i.key = a.issue_key where i.project_key = $1
	`, func(key string, count int) { snapshot.versions[key] = count })
	return snapshot
}

// requireUnchanged fails t for each issue, document, version count and event count of the rig's
// project that differs from before.
func (rig *specWriterRig) requireUnchanged(t *testing.T, what string, before specWriterSnapshot) {
	t.Helper()
	after := rig.snapshot(t)
	if !slices.Equal(after.issues, before.issues) {
		t.Errorf("%s: the project holds the issues %v, want the %v it held before", what, after.issues, before.issues)
	}
	for _, counts := range []struct {
		of            string
		before, after map[string]int
	}{{"events", before.events, after.events}, {"versions", before.versions, after.versions}} {
		keys := maps.Clone(counts.before)
		maps.Copy(keys, counts.after)
		for _, key := range slices.Sorted(maps.Keys(keys)) {
			was, wasHeld := counts.before[key]
			is, isHeld := counts.after[key]
			switch {
			case !wasHeld:
				t.Errorf("%s: %s is new, holding %d %s", what, key, is, counts.of)
			case !isHeld:
				t.Errorf("%s: %s is gone, which held %d %s", what, key, was, counts.of)
			case was != is:
				t.Errorf("%s: %s holds %d %s, want the %d it held before", what, key, is, counts.of, was)
			}
		}
	}
}

// specWriterIssueRow is an issue's row as it is stored.
type specWriterIssueRow struct {
	status string
	parent *string
	labels []string
}

func (rig *specWriterRig) issueRow(t *testing.T, key string) specWriterIssueRow {
	t.Helper()
	var row specWriterIssueRow
	if err := rig.database.Pool.QueryRow(context.Background(), `select status, parent_key, labels from issues where key = $1`, key).
		Scan(&row.status, &row.parent, &row.labels); err != nil {
		t.Fatalf("rig: read the issue %s: %v", key, err)
	}
	return row
}

// issuesTitled is the keys of the rig's project's issues titled title.
func (rig *specWriterRig) issuesTitled(t *testing.T, title string) []string {
	t.Helper()
	rows, err := rig.database.Pool.Query(context.Background(), `select key from issues where project_key = $1 and title = $2`, rig.project, title)
	if err != nil {
		t.Fatalf("rig: read the issues titled %q: %v", title, err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("rig: read the issues titled %q: %v", title, err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rig: read the issues titled %q: %v", title, err)
	}
	return keys
}

// askState is an ask's state as it is stored.
func (rig *specWriterRig) askState(t *testing.T, id string) string {
	t.Helper()
	var state string
	if err := rig.database.Pool.QueryRow(context.Background(), `select state from asks where id = $1`, id).Scan(&state); err != nil {
		t.Fatalf("rig: read the ask %s: %v", id, err)
	}
	return state
}

// eventCount is how many events of issue have a type like pattern (a SQL like pattern).
func (rig *specWriterRig) eventCount(t *testing.T, issue, pattern string) int {
	t.Helper()
	var count int
	if err := rig.database.Pool.QueryRow(context.Background(), `select count(*) from events where issue_key = $1 and type like $2`, issue, pattern).
		Scan(&count); err != nil {
		t.Fatalf("rig: count the %s events of %s: %v", pattern, issue, err)
	}
	return count
}
