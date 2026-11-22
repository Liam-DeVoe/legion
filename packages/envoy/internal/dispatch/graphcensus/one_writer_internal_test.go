package graphcensus

import (
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/architecture"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// OneWriterArchitectureRepo is the repository the architecture drives import a model from
// (archRepo), handed to TestEveryGraphKindHasOneWriter in this package's external test, which
// drives the importer with a failure injected: the fake GitHub it imports from is this package's
// own (archGitHub), which the external test cannot reach.
const OneWriterArchitectureRepo = archRepo

// OneWriterImporter is an architecture importer over database that reads the model of
// OneWriterArchitectureRepo at archCommit from this package's fake GitHub, as installation, closed
// when t ends.
func OneWriterImporter(t *testing.T, database *store.Store, installation int64) *architecture.Importer {
	t.Helper()
	importer := architecture.NewImporter(database, archGitHub(t, installation), events.NewBroker())
	t.Cleanup(importer.Close)
	return importer
}
