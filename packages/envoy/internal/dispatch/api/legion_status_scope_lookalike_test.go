package api

import (
	"net/http"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestALabelTheDatabaseLowersToLegionFreesNoLegionIssue: R26 must not let a credential with
// neither scope free the status of an issue under a labelled issue, whatever the collation of
// the database that Dispatch runs on.
//
// R26 reads "the legion label" in two ways. The removal rule reads it in Go
// (holdsLegionLabel, strings.EqualFold). The walk that says whether an issue is a Legion issue
// reads it in SQL (legionWalkSQL, lower(label) = 'legion'), and lower() follows the database's
// collation. glibc's en_US.UTF-8 and C.UTF-8 lower U+0130 (İ) to i, so on such a database, as
// on the Postgres of CI's envoy-go job ("initialized with locale en_US.utf8"), the walk reads
// LEGİON as the legion label and the removal rule does not. The shared token then frees the
// status in three calls:
//
//   - It moves the issue under a root that the shared token made with the label LEGİON. The walk
//     reads a labelled issue above the issue after the move, so R26 takes the move.
//   - It takes LEGİON off that root. Go reads no legion label in the labels before the call, so
//     R26 takes the removal.
//   - It writes the issue's status. No labelled issue is above the issue now, so R26 takes the
//     write.
//
// On a database whose lower() keeps İ as İ (an ICU en-US collation), R26 refuses the move, and
// the status write after it.
func TestALabelTheDatabaseLowersToLegionFreesNoLegionIssue(t *testing.T) {
	rig := newLegionStatusRig(t, true)
	shared := rig.credential(t, legionSharedToken)
	labelled := rig.issue(t, []string{"legion"}, "", "")
	issue := rig.issue(t, nil, labelled, "in_progress")

	made := shared.send(t, http.MethodPost, "/api/v1/issues", map[string]any{
		"project": brokerTestProject, "title": rig.title(), "force": true, "labels": []string{"LEG\u0130ON"},
	})
	if made.Code != http.StatusCreated {
		t.Fatalf("the shared token makes a root labelled LEG\u0130ON: status=%d body=%s", made.Code, made.Body.String())
	}
	lookAlike := decodeBody[model.Issue](t, made).Key

	moved := shared.send(t, http.MethodPatch, "/api/v1/issues/"+issue, map[string]any{"parent": lookAlike})
	t.Logf("the shared token moves %s under %s: %d %s", issue, lookAlike, moved.Code, moved.Body.String())
	removed := shared.send(t, http.MethodPatch, "/api/v1/issues/"+lookAlike, map[string]any{"labels": []string{}})
	t.Logf("the shared token takes LEG\u0130ON off %s: %d %s", lookAlike, removed.Code, removed.Body.String())

	before := readLegionIssue(t, rig.database, issue)
	written := shared.send(t, http.MethodPatch, "/api/v1/issues/"+issue, map[string]any{"status": "todo"})
	requireLegionStatusRefusal(t, written)
	requireLegionIssueUnchanged(t, rig.database, issue, before)
	if after := readLegionIssue(t, rig.database, issue); after.status != "in_progress" {
		t.Errorf("%s after the three calls of the shared token: %s; want status in_progress", issue, after)
	}
}
