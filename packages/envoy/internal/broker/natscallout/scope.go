package natscallout

import "github.com/sjawhar/envoy/natsgrant"

// scopeGrant is grant, entry's kind's grant, held to the projects entry may reach. An all-projects
// entry takes the grant whole, and so does an edge listener's: the projects its entry lists are
// what it serves, and no subject or key of its grant names a project yet, so nothing of the grant
// can be held to them and its read is the whole bus (natsgrant.Edge). Any other entry would have
// to be held to the projects the broker's own exchange list gives its account and the
// repositories Dispatch maps them to, which this service does not read, so it refuses that
// entry's connection by name rather than admit it to every project.
func scopeGrant(entry ServiceEntry, grant natsgrant.Permissions) (natsgrant.Permissions, error) {
	if entry.AllProjects || entry.Kind == KindEdgeListener {
		return grant, nil
	}
	return natsgrant.Permissions{}, refuse(labelHeldToProjects, "held to exchange projects, which this broker cannot read: service "+entry.name())
}
