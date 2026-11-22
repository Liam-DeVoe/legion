//go:build basebuild

// The basebuild tag, and how a test of it is run: store_census_test.go. This file's test reads the
// base release built from BASELINE_SHA at $TMPDIR/base-dispatch (baseRelease), and is run as
//
//	BASELINE_SHA=<the commit this branch starts from> go test -tags basebuild -timeout 30m ./cmd/dispatch -run '^TestAnApprovalOfTheBaseStandsUnderASpecQueryThatTakesComponents$' -count=1 -v

package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// An issue a person approved before this change keeps its approval, under the gate it started with
// (R24), now that the spec query takes the component and interface nodes an issue proposes (R22).
// The base release writes an issue with a memo and a requirement and the person approves its spec;
// the head, over the same database once its migrations ran, reads the spec query's record of the
// approved version at the revision it was read at and finds the digest the base release pinned on
// the review; the approval reads as standing; and a version the head writes that changes only prose
// outside the memo carries the approval, since its record is the approved one byte for byte.
func TestAnApprovalOfTheBaseStandsUnderASpecQueryThatTakesComponents(t *testing.T) {
	ctx := context.Background()
	databaseURL := censusDatabase(t)
	base := baseRelease(t)
	head := cmdtest.BuildStamped(t, "envoy-dispatch", headBuildStamp)
	database, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to the run's database: %v", err)
	}
	defer database.Close(ctx)

	previous := startDispatchProcessWith(t, databaseURL, dispatchOptions{Binary: base, SigningKey: previousBuildSigningKey, SettleDelay: oneAnswerSettleDelay})
	previous.waitHealthy(t)
	baseCommit := announcedBaseCommit(t, ctx, database)
	previous.call(t, http.MethodPost, "/api/v1/projects", `{"key":"STD","name":"The standing approval"}`, http.StatusCreated)
	issue, spec := createCensusIssue(t, previous, "STD", "Approved on the base",
		":::callout{#memo}\nWe keep each approval. A person approved it once. It stands after the change.\n:::\n\n"+
			"Prose outside the memo.\n\n:::requirement{#r-stands}\nAn approval of the base stands.\n:::\n")
	approved := approveAndCapture(t, ctx, previous, database, issue, spec, "the spec approved on the base")
	var pinned string
	if err := database.QueryRow(ctx, `
		select content_sha256 from artifact_reviews where artifact_id = $1::uuid and version = $2 and state = 'approved'
		order by created_at desc limit 1`, spec, approved.version).Scan(&pinned); err != nil {
		t.Fatalf("read the digest the base release pinned on the approval: %v", err)
	}
	t.Logf("the base release %s approved version %d of %s's spec, pinning %s", baseCommit, approved.version, issue, pinned)
	stopProcess(t, previous)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		var live int
		if err := database.QueryRow(ctx, `select count(*) from server_processes where heartbeat_at > now() - interval '15 seconds'`).Scan(&live); err != nil {
			t.Fatalf("read the live processes: %v", err)
		}
		if live == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d processes still read live after the base release stopped", live)
		}
	}

	migrated, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open the run's database: %v", err)
	}
	defer migrated.Pool.Close()
	if err := migrated.Migrate(ctx); err != nil {
		t.Fatalf("apply this build's migrations: %v", err)
	}
	// No version of the base records the revision of the tree it was read at: version_renders has
	// no writer in the base release, nor in this build. So the digest the approval pinned is the
	// digest of the version's stored bytes, and the head, its migrations run - the block schema's
	// version 7 and the spec query that takes components among them - answers the same digest for
	// the approved version (artifact_version_text_sha256), the one an approval is judged by.
	var rendered int
	if err := database.QueryRow(ctx, `select count(*) from version_renders where artifact_id = $1::uuid`, spec).Scan(&rendered); err != nil {
		t.Fatalf("count the versions of the spec rendered from the tree: %v", err)
	}
	if rendered != 0 {
		t.Fatalf("%d versions of the spec hold a version_renders row, want none: the base release renders no version from the tree", rendered)
	}
	var digest string
	if err := database.QueryRow(ctx, `select artifact_version_text_sha256($1::uuid, $2)`, spec, approved.version).Scan(&digest); err != nil {
		t.Fatalf("read the digest the head answers for version %d of the spec: %v", approved.version, err)
	}
	if digest != pinned {
		t.Fatalf("the head answers %s for the approved version, and the base release pinned %s: the approval would read as of other text", digest, pinned)
	}

	current := startDispatchProcessWith(t, databaseURL, dispatchOptions{Binary: head, SigningKey: previousBuildSigningKey, SettleDelay: oneAnswerSettleDelay})
	current.waitHealthy(t)
	requireApprovedNow(t, current, spec, "the spec the base release approved, read by the head")
	agentEdits(t, current, spec, map[string]string{"op": "replace", "find": "Prose outside the memo.", "with": "Other prose outside the memo."})
	settledDocument(t, ctx, current, database, spec)
	requireApprovedNow(t, current, spec, "the spec after the head wrote a version that changes only prose outside the memo")
}
