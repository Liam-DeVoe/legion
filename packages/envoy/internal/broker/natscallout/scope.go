package natscallout

import (
	"context"

	"github.com/sjawhar/envoy/natsgrant"
)

// scopeGrant is grant, entry's kind's grant, held to the projects entry may reach. An all-projects
// entry takes the grant whole. Any other entry is to be held to the projects the broker's own
// exchange list gives its account (Deps.Exchange) and the repositories Dispatch maps them to
// (Deps.Access), which this service does not do yet, so it refuses that entry's connection by name
// rather than admit it to every project.
func scopeGrant(_ context.Context, _ Deps, entry ServiceEntry, grant natsgrant.Permissions) (natsgrant.Permissions, error) {
	if entry.AllProjects {
		return grant, nil
	}
	return natsgrant.Permissions{}, refuse(credentialService, "held to exchange projects, which this broker cannot read: service "+entry.name())
}
