package natscallout

import "github.com/sjawhar/envoy/natsgrant"

// scopeGrant is grant, entry's kind's grant, as the service answers entry's connection: held to the
// projects entry may reach, and, for every kind but the operator, without any stream create,
// update, delete or restore (natsgrant.ForCallout), so no service the auth service admits but the
// operator makes, changes or removes a stream. The operator (KindOperator) is the one client that
// does: it makes every stream at deploy time (envoy-infra streams), and its grant is held to the
// hold's phase instead (natsgrant.Operator). An all-projects entry takes the grant whole, so
// narrowed. Any other entry would have to be held to the projects the broker's own exchange list
// gives its account and the repositories Dispatch maps them to, which this service does not read,
// so it refuses that entry's connection by name rather than admit it to every project.
func scopeGrant(entry ServiceEntry, grant natsgrant.Permissions) (natsgrant.Permissions, error) {
	if !entry.AllProjects {
		return natsgrant.Permissions{}, refuse(labelHeldToProjects, "held to exchange projects, which this broker cannot read: service "+entry.name())
	}
	if entry.Kind == KindOperator {
		return grant, nil
	}
	return natsgrant.ForCallout(grant), nil
}

// sessionScope is grant, a session's own (natsgrant.Session), as the service answers the session's
// connection: without any stream create, update, delete or restore (natsgrant.ForCallout), as every
// service's answer but the operator's is (scopeGrant). A session's grant holds none today; the
// narrowing is here so every answer the service makes passes it, whatever a session's grant comes
// to hold.
func sessionScope(grant natsgrant.Permissions) natsgrant.Permissions {
	return natsgrant.ForCallout(grant)
}
