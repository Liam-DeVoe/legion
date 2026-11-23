package graphcensus_test

import (
	"context"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/seedcorpus"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// corpusSeedDrive is the drive of one run of the test corpus loader.
const corpusSeedDrive = "seedcorpus/seedcorpus.go#Seed"

// corpusDrives are the test corpus loader's drives, on an empty database of their own: one run of
// its seed (seedcorpus.Seed), which writes its roles (writeRoles) and its run whose author names
// nobody (writeNobodysRun) beside what it replays through the in-process API; then the end of the
// agent role's claim and its return (EndClaim, RestoreClaim), as agent-page.sh runs them through
// cmd/dispatch-corpus. The seed is driven whole, as the seed command's run is, and its Counts hold
// every kind the run appends: the API's sites it replays through have drives of their own, which
// read what each of them appends apart, so this drive lists the loader's own sites alone. The
// seedcorpus package imports the API, which imports graphcensus, so its drives are this external
// test's.
func corpusDrives(t *testing.T) []graphcensus.StoreDrive {
	t.Helper()
	database := storetest.Open(t)
	ctx := context.Background()
	var corpus seedcorpus.Corpus
	return []graphcensus.StoreDrive{{
		Name: corpusSeedDrive, Database: database,
		Sites: []string{"seedcorpus/agents.go#writeRoles", "seedcorpus/nobody.go#writeNobodysRun", "graphstore/write.go#Write"},
		Drive: func(t *testing.T) {
			t.Helper()
			fixtures, err := seedcorpus.FindFixtures(".")
			if err != nil {
				t.Fatalf("find the corpus's captures: %v", err)
			}
			if corpus, err = seedcorpus.Seed(ctx, database, seedcorpus.Options{Fixtures: fixtures}); err != nil {
				t.Fatalf("seed the test corpus: %v", err)
			}
		},
		Counts: corpusSeedCounted(),
	}, {
		// The end of the agent role's claim: the retraction of the held_by its one holder states,
		// which the session term counts, as the outbox's record of a batch routed to a role does.
		Name: "seedcorpus/claims.go#EndClaim", Database: database,
		Sites: []string{"seedcorpus/claims.go#EndClaim", "graphstore/write.go#Write"},
		Drive: func(t *testing.T) {
			t.Helper()
			ended, err := seedcorpus.EndClaim(ctx, database.Pool, corpus.Get("agent.role"))
			if err != nil || len(ended) != 1 || ended[0] != corpus.Get("agent.session") {
				t.Fatalf("end the claim of %s: ended %v, want %s: %v", corpus.Get("agent.role"), ended, corpus.Get("agent.session"), err)
			}
		},
		Counts: map[graphcensus.StoredKind][]string{{From: graphstore.KindRole, Kind: graphstore.TupleHeldBy}: {graphstore.KindSession}},
	}, {
		// The return of that claim: the held_by asserted again.
		Name: "seedcorpus/claims.go#RestoreClaim", Database: database,
		Sites: []string{"seedcorpus/claims.go#RestoreClaim", "graphstore/write.go#Write"},
		Drive: func(t *testing.T) {
			t.Helper()
			if err := seedcorpus.RestoreClaim(ctx, database.Pool, corpus.Get("agent.role"), corpus.Get("agent.session")); err != nil {
				t.Fatalf("put back the claim of %s on %s: %v", corpus.Get("agent.session"), corpus.Get("agent.role"), err)
			}
		},
		Counts: map[graphcensus.StoredKind][]string{{From: graphstore.KindRole, Kind: graphstore.TupleHeldBy}: {graphstore.KindSession}},
	}}
}

// corpusSeedCounted is every kind one run of the corpus loader appends, each with the terms of the
// sizing that count it, as the drives of the sites it replays through count them: issues born
// converted, with their rooms and decisions queries (issue_room, query_node, query_version), their
// documents and versions, the blocks those versions index with their values, authors and
// mentions, the decisions and the readings of the decisions document's conversion, the
// pre-mortem's lens, its findings and its risks and its document's moved_to, asks with their
// answers, resolutions and followers, comments and messages, results, the command bar's runs, the
// roles with their pages and holders, and the sessions and people that wrote them.
func corpusSeedCounted() map[graphcensus.StoredKind][]string {
	counted := map[graphcensus.StoredKind][]string{}
	count := func(from string, terms []string, kinds ...string) {
		for _, kind := range kinds {
			counted[graphcensus.StoredKind{From: from, Kind: kind}] = terms
		}
	}
	base := []string{graphstore.AttributeVisibility, graphstore.AttributeState, graphstore.AttributeProject}
	count(graphstore.KindIssue, []string{graphstore.KindIssue}, base...)
	count(graphstore.KindIssue, []string{"issue_relation"}, graphstore.TupleChildOf, graphstore.TupleNeeds)
	count(graphstore.KindArtifact, []string{graphstore.KindArtifact, "issue_room"}, base...)
	count(graphstore.KindArtifact, []string{graphstore.KindArtifact}, graphstore.AttributeArtifactID, graphstore.AttributeCreatedAt)
	count(graphstore.KindArtifact, []string{"document_part_of", "issue_room"}, graphstore.TuplePartOf)
	count(graphstore.KindArtifact, []string{"document_ref"}, graphstore.TupleMentions)
	count(graphstore.KindArtifact, []string{"document_move"}, graphstore.TupleMovedTo)
	count(graphstore.KindArtifact, []string{"query_node"}, graphstore.AttributeQuery, graphstore.AttributeTakes,
		graphstore.AttributeSubtree, graphstore.AttributeDefinitionVersion)
	count(graphstore.KindVersion, []string{graphstore.KindVersion, "issue_room"}, graphstore.AttributeVisibility,
		graphstore.AttributeState, graphstore.AttributeProject, graphstore.TuplePartOf, graphstore.AttributeNumber,
		graphstore.AttributeCreatedAt)
	count(graphstore.KindVersion, []string{"query_version", "issue_room"}, graphstore.AttributeDefinitionVersion)
	count(refs.BlockNodeKind, []string{"block", "lens_finding"}, base...)
	count(refs.BlockNodeKind, []string{"block", "lens_finding", "reading"}, graphstore.TuplePartOf)
	count(refs.BlockNodeKind, []string{"block"}, refs.TupleInDocument)
	count(refs.BlockNodeKind, []string{"block_values"}, refs.AttributeRank, refs.AttributePlacement, refs.TupleTextParent, refs.AttributeTextRank)
	count(refs.BlockNodeKind, []string{"block_text", "lens_finding"}, graphstore.AttributeText)
	count(refs.BlockNodeKind, []string{"block_author", "lens_finding", "reading"}, graphstore.TupleAuthoredBy)
	count(refs.BlockNodeKind, []string{"block_ref"}, "mentions")
	count(refs.BlockNodeKind, []string{"decision"}, graphstore.TupleDecidedBy, refs.ValueUncertainty, refs.ValueUncertaintyReason, refs.ValueReplaced)
	count(graphstore.KindAsk, []string{graphstore.KindAsk}, base...)
	count(graphstore.KindAsk, []string{"ask_opened"}, graphstore.TupleAuthoredBy, graphstore.TupleAnchoredTo, graphstore.TuplePartOf)
	count(graphstore.KindAsk, []string{"ask_opened", "answer", "ask_resolved"}, graphstore.TupleAnswerState)
	count(graphstore.KindAsk, []string{"answer"}, graphstore.TupleAnswer, graphstore.TupleSelected, graphstore.TupleAnsweredBy,
		graphstore.TupleAnsweredAt, graphstore.TupleDecidedBy)
	count(graphstore.KindAsk, []string{"ask_resolved"}, graphstore.TupleResolution)
	count(graphstore.KindAsk, []string{"ask_follower"}, graphstore.TupleFollowedBy)
	count(graphstore.KindAsk, []string{"decision"}, refs.TupleShaped)
	count(graphstore.KindComment, []string{"comment"}, base...)
	count(graphstore.KindComment, []string{graphstore.TupleAuthoredBy}, graphstore.TupleAuthoredBy)
	count(graphstore.KindMessage, []string{graphstore.KindMessage}, base...)
	count(graphstore.KindResult, []string{"result"}, graphstore.AttributeVisibility, graphstore.AttributeState,
		graphstore.AttributeProject, graphstore.AttributeVerdict, graphstore.AttributeSurface, graphstore.AttributeRun,
		graphstore.AttributeObserved, graphstore.AttributeCommit, graphstore.AttributeRole, graphstore.AttributeFailed,
		graphstore.AttributeSkipped, graphstore.AttributeExcluded, graphstore.AttributeNegativeControl,
		graphstore.AttributeCheckVersion, graphstore.AttributeCheckSHA256, graphstore.TuplePartOf, graphstore.TupleResultOf,
		graphstore.TupleAuthoredBy, graphstore.TupleRunSession)
	count(graphstore.KindInteraction, []string{graphstore.KindInteraction}, graphstore.AttributeVisibility,
		graphstore.AttributeState, graphstore.AttributeProject, graphstore.AttributeWords, graphstore.AttributeEndsAt,
		graphstore.TupleAuthoredBy)
	count(graphstore.KindInteraction, []string{"frame"}, graphstore.AttributeFrame)
	count(graphstore.KindLens, []string{graphstore.KindLens}, graphstore.AttributeVisibility, graphstore.AttributeState,
		graphstore.AttributeProject, graphstore.TuplePartOf, graphstore.TupleHasKind, graphstore.AttributeVerdict,
		graphstore.AttributeArtifactID, graphstore.TupleTriggeredBy, graphstore.TupleAuthoredBy)
	count(graphstore.KindRisk, []string{graphstore.KindRisk}, graphstore.AttributeVisibility, graphstore.AttributeState,
		graphstore.AttributeProject, graphstore.TuplePartOf, graphstore.AttributeHeading, graphstore.AttributeEarlyWarning,
		graphstore.AttributePrevention, graphstore.TuplePreventedBy, graphstore.TupleAuthoredBy)
	count(graphstore.KindRole, []string{graphstore.KindRole}, graphstore.AttributeVisibility, graphstore.AttributeState)
	count(graphstore.KindRole, []string{graphstore.KindSession}, graphstore.TupleHeldBy)
	count(graphstore.KindPage, []string{graphstore.KindPage}, graphstore.AttributeVisibility, graphstore.AttributeState, graphstore.TuplePartOf)
	count(graphstore.KindSession, []string{graphstore.KindSession}, graphstore.AttributeVisibility, graphstore.AttributeState, graphstore.TuplePartOf)
	count(graphstore.KindPerson, []string{graphstore.KindPerson}, graphstore.AttributeVisibility, graphstore.AttributeState)
	return counted
}
