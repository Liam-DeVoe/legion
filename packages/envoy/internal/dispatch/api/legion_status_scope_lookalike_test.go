package api

import (
	"net/http"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestALabelTheDatabaseLowersToLegionFreesNoLegionIssue: R26 must not let a credential with
// neither scope free the status of an issue under a labelled issue, whatever the collation of
// the labels column.
//
// R26 reads "the legion label" in two ways. The removal rule reads it in Go
// (holdsLegionLabel, strings.EqualFold). The walk that says whether an issue is a Legion issue
// reads it in SQL (legionWalkSQL, lower(label collate "C") = 'legion'). The two agree because
// lower() in "C" changes only A-Z. In the column's own collation, lower() can change more:
// glibc's en_US.UTF-8 and C.UTF-8 lower U+0130 (İ) to i, and the Postgres of CI's envoy-go job
// is "initialized with locale en_US.utf8". A walk in such a collation reads LEGİON as the legion
// label, and the removal rule does not. Then a credential with neither scope frees the status in
// three calls:
//
//   - It moves the issue under a root that it made with the label LEGİON. The walk reads a
//     labelled issue above the issue after the move, so R26 takes the move.
//   - It takes LEGİON off that root. Go reads no legion label in the labels before the call, so
//     R26 takes the removal.
//   - It writes the issue's status. No labelled issue is above the issue now, so R26 takes the
//     write.
//
// The test puts the labels column of its database under C.utf8, glibc's C.UTF-8. So without the
// walk's collation "C" the test fails on every server that has glibc's C.UTF-8, a server whose
// database collation is ICU's included. With it, R26 refuses the move, takes the removal (the
// root is no Legion issue), and refuses the status write: the issue keeps its parent and its
// status. The shared token, a project's broker token and a personal token that holds no scope
// each make the three calls, in a subtest of their own.
func TestALabelTheDatabaseLowersToLegionFreesNoLegionIssue(t *testing.T) {
	rig := newLegionStatusRig(t, true)
	ctx := t.Context()
	if _, err := rig.database.Pool.Exec(ctx, `alter table issues alter column labels type text[] collate "C.utf8"`); err != nil {
		t.Fatalf("put the labels column under C.utf8: %v", err)
	}
	var lowered bool
	if err := rig.database.Pool.QueryRow(ctx, `select lower($1::text collate "C.utf8") = 'legion'`, "LEG\u0130ON").Scan(&lowered); err != nil {
		t.Fatalf("lower LEG\u0130ON under C.utf8: %v", err)
	}
	if !lowered {
		t.Fatalf("this server's C.utf8 does not lower U+0130 to i, so the test proves nothing here")
	}

	for _, name := range []string{legionSharedToken, legionProjectBrokerToken, legionUnscopedPersonalToken} {
		credential := rig.credential(t, name)
		t.Run(name, func(t *testing.T) {
			labelled := rig.issue(t, []string{"legion"}, "", "")
			issue := rig.issue(t, nil, labelled, "in_progress")
			made := credential.send(t, http.MethodPost, "/api/v1/issues", map[string]any{
				"project": brokerTestProject, "title": rig.title(), "force": true, "labels": []string{"LEG\u0130ON"},
			})
			if made.Code != http.StatusCreated {
				t.Fatalf("make a root labelled LEG\u0130ON: status=%d body=%s", made.Code, made.Body.String())
			}
			lookAlike := decodeBody[model.Issue](t, made).Key

			before := readLegionIssue(t, rig.database, issue)
			moved := credential.send(t, http.MethodPatch, "/api/v1/issues/"+issue, map[string]any{"parent": lookAlike})
			requireLegionStatusRefusal(t, moved)
			requireLegionIssueUnchanged(t, rig.database, issue, before)

			removed := credential.send(t, http.MethodPatch, "/api/v1/issues/"+lookAlike, map[string]any{"labels": []string{}})
			if removed.Code != http.StatusOK {
				t.Errorf("take LEG\u0130ON off %s, which is no Legion issue: status=%d body=%s, want 200",
					lookAlike, removed.Code, removed.Body.String())
			}

			written := credential.send(t, http.MethodPatch, "/api/v1/issues/"+issue, map[string]any{"status": "todo"})
			requireLegionStatusRefusal(t, written)
			requireLegionIssueUnchanged(t, rig.database, issue, before)
			if after := readLegionIssue(t, rig.database, issue); after.status != "in_progress" || after.parent == nil || *after.parent != labelled {
				t.Errorf("%s after the three calls: %s; want status in_progress under %s", issue, after, labelled)
			}
		})
	}
}
