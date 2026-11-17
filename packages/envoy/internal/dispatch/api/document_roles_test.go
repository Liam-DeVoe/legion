package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/files/filestest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// issueRoleDocuments is what an issue read names by role: its spec, and its document of each
// role, "" where it holds none.
type issueRoleDocuments struct {
	PrimaryArtifactID      string `json:"primary_artifact_id"`
	VerificationArtifactID string `json:"verification_artifact_id"`
	PlanArtifactID         string `json:"plan_artifact_id"`
	DecisionsArtifactID    string `json:"decisions_artifact_id"`
}

func readIssueRoleDocuments(t *testing.T, handler http.Handler, key string) issueRoleDocuments {
	t.Helper()
	read := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+key, nil, "alice")
	if read.Code != http.StatusOK {
		t.Fatalf("read the issue %s: status=%d body=%s", key, read.Code, read.Body.String())
	}
	return decodeBody[issueRoleDocuments](t, read)
}

// A document an issue holds under one of the reserved names plays that role from the moment
// it is created, the issue read names both documents by it, and nothing else on the issue takes a
// role. The role is the document's for life: a second upload of the same name is its next
// version, under the row that already holds the role.
func TestIssueDocumentsTakeTheirRoleFromTheirName(t *testing.T) {
	handler, _, _ := newTestServer(t, testServerOptions{})
	issue := createInteractionIssue(t, handler, "ROLE", "Roles", "A spec\n")
	owner := "/api/v1/issues/" + issue.Key

	verification := uploadDocument(t, handler, owner, "verification.md", "# Verification\n\nOne.\n")
	plan := uploadDocument(t, handler, owner, "plan.md", "# Plan\n\nOne.\n")
	notes := uploadDocument(t, handler, owner, "notes.md", "# Notes\n\nOne.\n")
	if verification.Artifact.Role != "verification" || plan.Artifact.Role != "plan" || notes.Artifact.Role != "" {
		t.Fatalf("roles = %q, %q, %q; want verification, plan and none",
			verification.Artifact.Role, plan.Artifact.Role, notes.Artifact.Role)
	}
	if again := uploadDocument(t, handler, owner, "plan.md", "# Plan\n\nTwo.\n"); again.Artifact.ID != plan.Artifact.ID || again.Artifact.Role != "plan" || again.Version.Number != 2 {
		t.Fatalf("second upload of plan.md = %#v version %d, want version 2 of the plan", again.Artifact, again.Version.Number)
	}

	// The spec keeps is_primary and takes no role.
	spec := dispatchRequest(t, handler, http.MethodGet, "/api/v1/artifacts/"+issue.PrimaryArtifactID, nil, "alice")
	if spec.Code != http.StatusOK {
		t.Fatalf("read the spec: status=%d body=%s", spec.Code, spec.Body.String())
	}
	if read := decodeBody[model.Artifact](t, spec); !read.Primary || read.Role != "" {
		t.Fatalf("spec = primary %v role %q, want the primary document with no role", read.Primary, read.Role)
	}

	read := dispatchRequest(t, handler, http.MethodGet, owner, nil, "alice")
	if read.Code != http.StatusOK {
		t.Fatalf("read the issue: status=%d body=%s", read.Code, read.Body.String())
	}
	detail := decodeBody[struct {
		PrimaryArtifactID      string `json:"primary_artifact_id"`
		VerificationArtifactID string `json:"verification_artifact_id"`
		PlanArtifactID         string `json:"plan_artifact_id"`
	}](t, read)
	if detail.PrimaryArtifactID != issue.PrimaryArtifactID ||
		detail.VerificationArtifactID != verification.Artifact.ID ||
		detail.PlanArtifactID != plan.Artifact.ID {
		t.Fatalf("issue read = %#v, want the spec, the Verification document and the plan", detail)
	}

	// Another issue holds its own pair: the role is unique within an issue, not across the project.
	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]string{
		"project": "ROLE", "title": "A second issue", "spec": "Another spec\n",
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("create a second issue: status=%d body=%s", created.Code, created.Body.String())
	}
	other := decodeBody[struct {
		Key string `json:"key"`
	}](t, created)
	if second := uploadDocument(t, handler, "/api/v1/issues/"+other.Key, "plan.md", "# Plan\n\nOne.\n"); second.Artifact.Role != "plan" {
		t.Fatalf("the second issue's plan = %#v, want its own plan role", second.Artifact)
	}

	// A project document takes no role: a role belongs to an issue.
	if project := uploadDocument(t, handler, "/api/v1/projects/ROLE", "plan.md", "# Plan\n\nOne.\n"); project.Artifact.Role != "" {
		t.Fatalf("project document = %#v, want no role", project.Artifact)
	}
}

// The decisions document is a role of its own, which the design gate's `decisions` step binds
// to: an issue's decisions.md plays it from the moment it is created, the issue read names it,
// a person reviews it as he reviews any document, and a row stored before Dispatch knew the role
// is still found by its slug until its next version repairs it.
func TestDecisionsDocumentTakesItsRoleFromItsName(t *testing.T) {
	handler, database, _ := newTestServer(t, testServerOptions{})
	issue := createInteractionIssue(t, handler, "DEC", "Decisions", "A spec\n")
	owner := "/api/v1/issues/" + issue.Key

	decisions := uploadDocument(t, handler, owner, "decisions.md", "# Decisions\n\n## Transport\n\nHTTP it is.\n")
	notes := uploadDocument(t, handler, owner, "notes.md", "# Notes\n\nOne.\n")
	if decisions.Artifact.Role != model.ArtifactRoleDecisions || notes.Artifact.Role != "" {
		t.Fatalf("roles = %q, %q; want decisions and none", decisions.Artifact.Role, notes.Artifact.Role)
	}
	named := readIssueRoleDocuments(t, handler, issue.Key)
	if named.DecisionsArtifactID != decisions.Artifact.ID {
		t.Fatalf("issue read = %#v, want it to name the decisions document %s", named, decisions.Artifact.ID)
	}
	// notes.md reaches no role, so no field of the read names it, and the issue holds neither of
	// the other two documents.
	if named.VerificationArtifactID != "" || named.PlanArtifactID != "" || named.PrimaryArtifactID == notes.Artifact.ID {
		t.Fatalf("issue read = %#v, want notes.md named nowhere and no Verification or Plan document", named)
	}

	// A person reviews the decisions document through an approval request, as he reviews a spec:
	// his answer records the review the gate's `decisions` step reads.
	requested := sessionRequest(t, handler, http.MethodPost,
		"/api/v1/artifacts/"+decisions.Artifact.ID+"/approval-requests", map[string]any{"actor": sessionActor()})
	if requested.Code != http.StatusCreated {
		t.Fatalf("request approval of the decisions document: status=%d body=%s", requested.Code, requested.Body.String())
	}
	request := decodeBody[approvalRequestAsk](t, requested)
	if request.Ask == nil || request.Ask.Approval.ArtifactID != decisions.Artifact.ID {
		t.Fatalf("approval ask = %#v, want one naming the decisions document", request.Ask)
	}
	if answered := dispatchRequest(t, handler, http.MethodPost, "/api/v1/asks/"+request.Ask.ID+"/answer",
		map[string]any{"selected": []string{"Approve"}}, "alice"); answered.Code != http.StatusOK {
		t.Fatalf("approve the decisions document: status=%d body=%s", answered.Code, answered.Body.String())
	}
	reviews := listReviews(t, handler, decisions.Artifact.ID)
	if len(reviews) != 1 || reviews[0].State != "approved" || reviews[0].Version != 1 ||
		reviews[0].Kind != "person" || reviews[0].Actor.ID != "alice" {
		t.Fatalf("reviews of the decisions document = %#v, want one person's approval of version 1", reviews)
	}
	if read := readApproval(t, handler, decisions.Artifact.ID); read.Approval == nil || read.Approval.State != "approved" {
		t.Fatalf("approval of the decisions document = %#v, want approved", read.Approval)
	}

	// A decisions.md stored before Dispatch knew the role carries none. Every reader of the role
	// finds it by its slug, which is what lets Dispatch and the daemon deploy in either order,
	// and the next version written under that row sets the role from the same slug.
	created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]string{
		"project": "DEC", "title": "Decided before the role", "spec": "Another spec\n",
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("create a second issue: status=%d body=%s", created.Code, created.Body.String())
	}
	other := decodeBody[struct {
		Key string `json:"key"`
	}](t, created)
	stored := uploadDocument(t, handler, "/api/v1/issues/"+other.Key, "decisions.md", "# Decisions\n\nOne.\n")
	if _, err := database.Pool.Exec(context.Background(),
		`update artifacts set role = null where id = $1`, stored.Artifact.ID); err != nil {
		t.Fatalf("clear the stored role: %v", err)
	}
	if roleless := readIssueRoleDocuments(t, handler, other.Key); roleless.DecisionsArtifactID != stored.Artifact.ID {
		t.Fatalf("issue read of a row with no role = %#v, want the decisions document %s found by its slug",
			roleless, stored.Artifact.ID)
	}
	repaired := uploadDocument(t, handler, "/api/v1/issues/"+other.Key, "decisions.md", "# Decisions\n\nTwo.\n")
	if repaired.Artifact.ID != stored.Artifact.ID || repaired.Artifact.Role != model.ArtifactRoleDecisions ||
		repaired.Version.Number != 2 {
		t.Fatalf("second upload of decisions.md = %#v version %d, want version 2 of the same row, now in the decisions role",
			repaired.Artifact, repaired.Version.Number)
	}
}

// Both readers of a document's role - documentRolePredicate, which every query that finds an
// issue's document by its role holds a row to, and documentPlays, which the routes ask of a row
// already read - give a role to an issue's markdown document alone. A role-less row falls back to
// its slug only when it is a `doc`, the row a rolling deploy leaves, so a file uploaded as plan.md,
// which carries no role and whose slug is plan-md, plays no role in either reader, nor does a
// picture stored under that slug. A row whose role is set plays that role. Both readers are asked
// about the same rows and answer the same about each one, which is the role it plays or none.
func TestBothRoleReadersGiveAFileNamedLikeARoleDocumentNoRole(t *testing.T) {
	handler, database, _ := newTestServer(t, testServerOptions{files: filestest.NewMemory()})
	ctx := context.Background()
	uploads := createInteractionIssue(t, handler, "ROLE", "Files named like role documents", "A spec\n").Key
	createIssue := func(title string) string {
		t.Helper()
		created := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]string{
			"project": "ROLE", "title": title, "spec": "A spec\n",
		}, "alice")
		if created.Code != http.StatusCreated {
			t.Fatalf("create the issue %q: status=%d body=%s", title, created.Code, created.Body.String())
		}
		return decodeBody[struct {
			Key string `json:"key"`
		}](t, created).Key
	}
	rolling := createIssue("Documents a rolling deploy left with no role")
	stamped := createIssue("Documents that carry their role")
	pictures := createIssue("Pictures named like role documents")

	// The upload route stores bytes that are not markdown, under the name plan.md, as a file with
	// no role and the slug plan-md.
	uploaded := multipartRequest(t, handler, "/api/v1/issues/"+uploads+"/artifacts", map[string]string{"name": "plan.md"},
		"plan.md", "application/octet-stream", []byte("\x00\x01 not markdown \x02"), "alice")
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("upload a file named plan.md: status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	file := decodeBody[uploadedArtifact](t, uploaded).Artifact
	if file.Kind != "file" || file.Role != "" || file.Slug != "plan-md" {
		t.Fatalf("the upload named plan.md = kind %q role %q slug %q, want a file with no role and the slug plan-md",
			file.Kind, file.Role, file.Slug)
	}

	// The rows the release before this one stores, written by SQL: no role, whatever the slug.
	insertRoleless := func(issueKey, name, slug, kind string) string {
		t.Helper()
		var id string
		if err := database.Pool.QueryRow(ctx, `
			insert into artifacts (issue_key, project_key, slug, name, kind, created_by)
			values ($1, 'ROLE', $2, $3, $4, '{"kind":"user","id":"alice"}')
			returning id::text
		`, issueKey, slug, name, kind).Scan(&id); err != nil {
			t.Fatalf("store the %s %s on %s: %v", kind, slug, issueKey, err)
		}
		return id
	}

	rollingPlan := insertRoleless(rolling, "plan.md", "plan-md", "doc")
	rollingVerification := insertRoleless(rolling, "verification.md", "verification-md", "doc")
	rollingDecisions := insertRoleless(rolling, "decisions.md", "decisions-md", "doc")

	rows := []struct {
		what  string
		id    string
		plays string
	}{
		{"a role-less file uploaded as plan.md", file.ID, ""},
		{"a role-less file stored as verification.md", insertRoleless(uploads, "verification.md", "verification-md", "file"), ""},
		{"a role-less file stored as decisions.md", insertRoleless(uploads, "decisions.md", "decisions-md", "file"), ""},
		{"a role-less picture stored under the slug plan-md", insertRoleless(pictures, "plan.md", "plan-md", "image"), ""},
		{"a role-less doc with the slug plan-md", rollingPlan, model.ArtifactRolePlan},
		{"a role-less doc with the slug verification-md", rollingVerification, model.ArtifactRoleVerification},
		{"a role-less doc with the slug decisions-md", rollingDecisions, model.ArtifactRoleDecisions},
		{"a role-less doc whose slug names no role", insertRoleless(rolling, "notes.md", "notes-md", "doc"), ""},
		{"a plan whose role is set", uploadDocument(t, handler, "/api/v1/issues/"+stamped, "plan.md", "# Plan\n\nOne.\n").Artifact.ID, model.ArtifactRolePlan},
		{"a Verification document whose role is set", uploadDocument(t, handler, "/api/v1/issues/"+stamped, "verification.md", "# Verification\n\nOne.\n").Artifact.ID, model.ArtifactRoleVerification},
		{"a decisions document whose role is set", uploadDocument(t, handler, "/api/v1/issues/"+stamped, "decisions.md", "# Decisions\n\nOne.\n").Artifact.ID, model.ArtifactRoleDecisions},
		{"a project's document named plan.md", uploadDocument(t, handler, "/api/v1/projects/ROLE", "plan.md", "# Plan\n\nOne.\n").Artifact.ID, ""},
	}
	for _, row := range rows {
		artifact, err := loadArtifactRowByID(ctx, database.Pool, row.id)
		if err != nil {
			t.Fatalf("read %s: %v", row.what, err)
		}
		for role := range roleSlugs {
			want := role == row.plays
			// The predicate is asked as every query asks it, in a where clause, aliased and not.
			for _, alias := range []string{"a", ""} {
				table := "artifacts"
				if alias != "" {
					table += " " + alias
				}
				var held bool
				if err := database.Pool.QueryRow(ctx,
					`select exists (select 1 from `+table+` where id = $1 and `+documentRolePredicate(alias, role)+`)`,
					row.id).Scan(&held); err != nil {
					t.Fatalf("ask documentRolePredicate(%q, %q) of %s: %v", alias, role, row.what, err)
				}
				if held != want {
					t.Errorf("documentRolePredicate(%q, %q) holds %s (kind %q role %q slug %q) = %v, want %v",
						alias, role, row.what, artifact.Kind, artifact.Role, artifact.Slug, held, want)
				}
			}
			if plays := documentPlays(artifact, role); plays != want {
				t.Errorf("documentPlays(%s (kind %q role %q slug %q), %q) = %v, want %v",
					row.what, artifact.Kind, artifact.Role, artifact.Slug, role, plays, want)
			}
		}
		if role := documentRoleOf(artifact); role != row.plays {
			t.Errorf("documentRoleOf(%s) = %q, want %q", row.what, role, row.plays)
		}
	}

	// The issue read finds each document by the predicate: it names none of the files, and it
	// names the rolling deploy's rows by their slug.
	if named := readIssueRoleDocuments(t, handler, uploads); named.PlanArtifactID != "" ||
		named.VerificationArtifactID != "" || named.DecisionsArtifactID != "" {
		t.Errorf("issue read of the issue holding files named like role documents = %#v, want no role document named", named)
	}
	if named := readIssueRoleDocuments(t, handler, rolling); named.PlanArtifactID != rollingPlan ||
		named.VerificationArtifactID != rollingVerification || named.DecisionsArtifactID != rollingDecisions {
		t.Errorf("issue read of the issue holding role-less docs = %#v, want plan %s, verification %s and decisions %s",
			named, rollingPlan, rollingVerification, rollingDecisions)
	}
}
