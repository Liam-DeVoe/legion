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
// calls beside the fixed ones. Which fixed names a generation may belong to is the held scope's
// own table of the daemon's consumers (newHeldScope), so this file reads no project or kind.

// daemonGeneration is a generation as a connection's name carries it: the NUID form, the 22
// letters and digits nuid.Next writes (consumernames.FreshName), and nothing else.
var daemonGeneration = regexp.MustCompile(`^[A-Za-z0-9]{22}$`)

// DaemonGenerationConnection is the name of the connection a daemon makes every call on physical,
// a fresh name legion-go-<P>-<kind>~<generation>, on: legion-go-<P>-<kind>/<generation>.
func DaemonGenerationConnection(physical string) string {
	i := strings.LastIndex(physical, "~")
	if i < 0 {
		return physical
	}
	return physical[:i] + "/" + physical[i+1:]
}

// generationOf is the fresh name of consumer that connection is named for, and false for a name
// that is no generation of consumer: consumer's own name, '/', and a generation in NUID form.
func generationOf(consumer daemonConsumer, connection string) (daemonConsumer, bool) {
	generation, ok := strings.CutPrefix(connection, consumer.name+"/")
	if !ok || !daemonGeneration.MatchString(generation) {
		return daemonConsumer{}, false
	}
	return daemonConsumer{name: consumer.name + "~" + generation, filter: consumer.filter}, true
}
