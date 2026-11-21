package natsgrant

import (
	"regexp"
	"strings"
)

// A Legion daemon on fresh consumer names (packages/daemon/internal/intake/durable_names.go) gives
// each creation of each of its consumers a physical name of its own,
// legion-go-<P>-<kind>~<generation>, and makes every call on it on a connection named for that
// generation, legion-go-<P>-<kind>/<generation>. A grant held to the daemon's projects names only
// the fixed names, legion-go-<P>-<kind> (WithinProjects), so the auth service hands the
// connection's name in as Scope.Connection, and WithinProjects grants that one physical name's
// calls beside the fixed ones.

// daemonGeneration matches a generation's connection name: the fixed name of a daemon consumer of a
// project key's shape, '/', and a generation in NUID form, the 22 letters and digits nuid.Next
// writes (consumernames.FreshName), and nothing else.
var daemonGeneration = regexp.MustCompile(`^(` + daemonConsumerPrefix + `[A-Z][A-Z0-9]{1,9}-(?:dispatch|wake|ready|github))/([A-Za-z0-9]{22})$`)

// DaemonGenerationConnection is the name of the connection a daemon makes every call on physical,
// a fresh name legion-go-<P>-<kind>~<generation>, on: legion-go-<P>-<kind>/<generation>.
func DaemonGenerationConnection(physical string) string {
	i := strings.LastIndex(physical, "~")
	if i < 0 {
		return physical
	}
	return physical[:i] + "/" + physical[i+1:]
}

// ParseDaemonGenerationConnection is the fresh name a generation's connection is named for, and
// false for a name that is no such connection (DaemonGenerationConnection's inverse).
func ParseDaemonGenerationConnection(name string) (physical string, ok bool) {
	match := daemonGeneration.FindStringSubmatch(name)
	if match == nil {
		return "", false
	}
	return match[1] + "~" + match[2], true
}
