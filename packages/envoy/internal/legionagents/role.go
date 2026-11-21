package legionagents

// RolePattern is the role grammar as a pattern of a whole name, `^[a-z][a-z0-9-]{0,31}$`: the
// spelling a JSON schema's `pattern` and a check of the database give it. Of its names, every one
// but the controller's is a role of an issue (IsRole).
const RolePattern = `^` + roleGrammar + `$`

// IsRole reports whether role is a role of an issue: one of Roles, or a role the deployment
// defines in its settings, a name of RolePattern. The controller's name is of the grammar and is
// the project's, no issue's, so it is none. It is the one rule each reader of an issue's role
// outside this package takes, as an entry of the registry takes it (Validate).
func IsRole(role string) bool { return isRole(role) }
