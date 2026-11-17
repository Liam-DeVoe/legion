package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/intake"
)

// An issue handed to Legion while its project maps no repository in Dispatch is told why the
// daemon did not admit it: the project has no repository mapped, and where in Dispatch one is
// mapped (api.NoRepositoryMapped), as the boot's ERROR line and `legion state` say. "Set its
// repository" is no remedy there: Dispatch refuses an issue repository its project does not map
// (400 REPO_NOT_MAPPED), so a note that names only the issue's own repository sends the person to a
// write that cannot succeed. A project that maps a repository but names no default keeps today's
// note, since setting the issue's repository is the remedy there.
func TestAnIssueHandedToLegionWhileTheProjectMapsNoRepositoryIsToldTheProjectMapsNone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mapping []dispatch.RepoProject
		want    string
		// remedy is what the note tells the person to do, and refused a remedy it must not give,
		// since Dispatch refuses it then; "" checks none.
		remedy, refused string
	}{
		{name: "no repository mapped", want: api.NoRepositoryMapped("LEGION"), remedy: "Map the project's repositories", refused: "Set its repository"},
		{
			name:    "a repository mapped, none the default",
			mapping: []dispatch.RepoProject{{Repo: "zeta/tools", Project: "LEGION"}},
			want:    "Legion did not admit this issue: its repository resolves to none. Set its repository (the issue header's Repository picker, or dispatch issue-update --repo owner/name) and move it to todo again.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			w, client, _, _, _ := mappingRigJS(t, &installations{}, func(w *workflowRuntime) {
				w.dispatch.(*mappingDispatch).set(tc.mapping...)
				w.recheckAfter = func(time.Duration) <-chan time.Time { return nil }
			})
			engine, admission := rigHandlers(w)
			issue := dispatch.Issue{Key: "LEGION-401", Status: "todo", Title: "handed to Legion", Rank: "A", HandedOver: true, LastSeq: 1}
			client.mu.Lock()
			client.issues = map[string]dispatch.Issue{issue.Key: issue}
			client.mu.Unlock()
			fact := intake.DispatchIssue{Key: issue.Key, Seq: issue.LastSeq, Status: issue.Status, Title: issue.Title, Rank: issue.Rank, HandedOver: issue.HandedOver}
			prepared, err := prepareAsDelivered(t, w, fact)
			if err != nil {
				t.Fatalf("prepare %s = %v", fact.Key, err)
			}
			if _, err := intake.ApplyFact(ctx, w.pool, "test", fmt.Sprintf("%s-%d", fact.Key, fact.Seq), prepared, engine, admission); err != nil {
				t.Fatal(err)
			}
			if got := recordedIssue(t, w, issue.Key); got != nil {
				t.Fatalf("%s recorded as %+v, want it not admitted", issue.Key, got)
			}
			notes := repositoryNotes(t, w, issue.Key)
			if len(notes) != 1 || !strings.Contains(notes[0], tc.want) {
				t.Fatalf("notes on %s = %q, want one saying %q", issue.Key, notes, tc.want)
			}
			if (tc.remedy != "" && !strings.Contains(notes[0], tc.remedy)) || (tc.refused != "" && strings.Contains(notes[0], tc.refused)) {
				t.Fatalf("the note on %s = %q, want it to say %q and not %q", issue.Key, notes[0], tc.remedy, tc.refused)
			}
		})
	}
}
