// Package natsversion holds the nats-server versions Envoy and the Legion daemon refuse to run
// against, for a caller in either module: the Envoy listener asks it at start, before it opens its
// session consumers (internal/bus ConnectSessionConsumers), and the daemon before it opens its
// durable consumers (packages/daemon/internal/intake OpenConsumers). It is one rule in one place,
// so the two cannot require different servers.
//
// The requirement is a server whose scan of a consumer's stored messages reads every filter. A
// session consumer filters on a session's agent subject in both forms of its name,
// notifications.agent.<session> beside notifications.agent.*.<session> (dispatch://INFRA-3862),
// which puts a `*` and a literal at the same token of the server's filter tree. nats-server 2.10.x,
// 2.11.0 to 2.11.11 and 2.12.0 to 2.12.3 read a file block by subject through intersectStree
// (server/sublist.go 1750-1784 at v2.10.29), which at a level holding a `*` follows only the `*`
// branch and never the literal beside it: with other subjects' messages between a consumer's
// position and a message on the literal, the consumer is moved past that message (consumer.go
// getNextMsg, 3700-3708 at v2.10.29) and never delivers it, with no error. v2.11.12, v2.12.4 and
// every release from v2.14.0 read the block through stree.IntersectGSL, which checks every filter.
// There is no v2.13, and v2.10.29 is the last 2.10 release, so no 2.10 is fixed.
//
// Like subjectforms and sessiontiming, it is a door for the daemon's module, which Go's internal
// rule keeps out of the envoy module's internal packages.
package natsversion

import (
	"fmt"
	"strconv"
	"strings"
)

// FixedReleases names the first releases of each line whose scan reads every filter, which every
// refusal names.
const FixedReleases = "v2.11.12, v2.12.4, or v2.14.0 and later"

// Refusal answers nil for a nats-server version, as a connection's server reports it
// ((*nats.Conn).ConnectedServerVersion(): "2.15.1", with or without a leading v), whose scan of a
// consumer's stored messages reads every filter, and an error naming the version and the fixed
// releases for any other: 2.10.x, 2.11.0 to 2.11.11, 2.12.0 to 2.12.3, every release before them,
// and a version it cannot read, an empty one among them, since a server it cannot place is not
// known to be fixed.
func Refusal(serverVersion string) error {
	major, minor, patch, ok := parse(serverVersion)
	if !ok {
		return fmt.Errorf("the NATS server reports version %q, which cannot be read; Envoy needs nats-server %s, whose scan of a consumer's stored messages reads every filter", serverVersion, FixedReleases)
	}
	fixed := major > 2 ||
		(major == 2 && (minor >= 13 || (minor == 12 && patch >= 4) || (minor == 11 && patch >= 12)))
	if fixed {
		return nil
	}
	return fmt.Errorf("the NATS server is nats-server %s, which can skip a message on a consumer filtering on a wildcard and a literal at the same token (intersectStree); Envoy needs nats-server %s", serverVersion, FixedReleases)
}

// parse reads major.minor.patch from version, a leading v and anything after the patch's digits
// (a -RC suffix) dropped.
func parse(version string) (major, minor, patch int, ok bool) {
	fields := strings.SplitN(strings.TrimPrefix(version, "v"), ".", 3)
	if len(fields) != 3 {
		return 0, 0, 0, false
	}
	if end := strings.IndexFunc(fields[2], func(r rune) bool { return r < '0' || r > '9' }); end >= 0 {
		fields[2] = fields[2][:end]
	}
	var err error
	if major, err = strconv.Atoi(fields[0]); err != nil {
		return 0, 0, 0, false
	}
	if minor, err = strconv.Atoi(fields[1]); err != nil {
		return 0, 0, 0, false
	}
	if patch, err = strconv.Atoi(fields[2]); err != nil {
		return 0, 0, 0, false
	}
	return major, minor, patch, true
}
