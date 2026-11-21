package consumernames

import (
	"context"
	"fmt"
	"slices"
	"strings"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/dispatch/delivery"
)

// The cutover families, as the broker's cutover rows name them (holdrecord.Client.PutCutover):
// the session consumers and every listener's durable, whose records are both in the selection
// bucket; Dispatch's two durables; and each Legion daemon, by its project key.
const (
	familySession      = "session"
	familyDispatch     = "dispatch"
	familyDaemonPrefix = "daemon/"
)

// daemonFamily is the cutover family of the daemon of project.
func daemonFamily(project string) string { return familyDaemonPrefix + project }

// The fixed names' forms.
const (
	sessionFixedPrefix  = "s-"
	sessionFreshPrefix  = "session"
	listenerFixedPrefix = "listener-"
	daemonFixedPrefix   = "legion-go-"
)

// daemonKinds are the kinds of consumer a Legion daemon opens per project: legion-go-<project>-<kind>
// (packages/daemon/internal/intake, dispatchConsumerName, githubConsumerName, wakeConsumerName).
var daemonKinds = []string{"dispatch", "github", "wake", "ready"}

// member is one logical name of a family as the operator's commands meet it on the stream.
type member struct {
	// Family is its cutover family.
	Family string
	// Logical is its record's key in its family's store: the session id; the listener durable's
	// selection.DurableKey; Dispatch's or the daemon's durable name.
	Logical string
	// Fixed is its fixed name, which every release before the fresh names created.
	Fixed string
	// Prefix is the part of each fresh name before its '~' (selection.FreshName).
	Prefix string
	// SessionID is the session a session consumer delivers to, "" for any other member.
	SessionID string
	// Project is the daemon's project key, "" for any other member.
	Project string
}

// Metadata is the metadata every fresh consumer of the member carries: the logical name it is
// selected under (selection.LogicalMetadataKey), and a session consumer's session
// (bus.SessionIDMetadataKey), which the listener's session listing selects by.
func (m member) Metadata() map[string]string {
	metadata := map[string]string{selection.LogicalMetadataKey: m.logicalMetadata()}
	if m.SessionID != "" {
		metadata[bus.SessionIDMetadataKey] = m.SessionID
	}
	return metadata
}

// logicalMetadata is the logical name a fresh consumer's envoy.logical carries: the session id
// or the durable's own name, as the families' creators write it.
func (m member) logicalMetadata() string {
	if m.SessionID != "" {
		return m.SessionID
	}
	return m.Fixed
}

// classify is the member a consumer named name on the notification stream belongs to, by its
// fixed name, and false for a name that is no family's fixed name: every fresh name holds a '~',
// which no fixed name does.
func classify(name string) (member, bool) {
	if strings.Contains(name, "~") || name == "" {
		return member{}, false
	}
	if id, ok := strings.CutPrefix(name, sessionFixedPrefix); ok && id != "" {
		return member{Family: familySession, Logical: id, Fixed: name, Prefix: sessionFreshPrefix, SessionID: id}, true
	}
	if strings.HasPrefix(name, listenerFixedPrefix) && len(name) > len(listenerFixedPrefix) {
		return member{Family: familySession, Logical: selection.DurableKey(name), Fixed: name, Prefix: name}, true
	}
	if slices.Contains(dispatchDurables(), name) {
		return member{Family: familyDispatch, Logical: name, Fixed: name, Prefix: name}, true
	}
	if rest, ok := strings.CutPrefix(name, daemonFixedPrefix); ok {
		cut := strings.LastIndexByte(rest, '-')
		if cut > 0 && slices.Contains(daemonKinds, rest[cut+1:]) {
			project := rest[:cut]
			return member{Family: daemonFamily(project), Logical: name, Fixed: name, Prefix: name, Project: project}, true
		}
	}
	return member{}, false
}

// dispatchDurables are Dispatch's two durables' names (delivery.ConsumerConfigs).
func dispatchDurables() []string {
	var names []string
	for _, config := range delivery.ConsumerConfigs() {
		names = append(names, config.Durable)
	}
	return names
}

// store is family's selection store on the run's reach: the selection bucket for the session
// family, through the operator's connection, and the family's intake_consumers table for
// Dispatch and each daemon.
func (r *run) store(ctx context.Context, family string) (familyStore, error) {
	if family == familySession {
		nc, _, err := r.conn(ctx)
		if err != nil {
			return nil, err
		}
		return selection.NewKVStore(selection.SessionBucket, selection.ConnRequester{Conn: nc}), nil
	}
	if family != familyDispatch && !strings.HasPrefix(family, familyDaemonPrefix) {
		return nil, fmt.Errorf("no family %q", family)
	}
	db, err := r.database(ctx, family)
	if err != nil {
		return nil, err
	}
	return selection.NewPGStore(db), nil
}

// familyStore is a family's store with its sentinel's write, which every store the operator
// writes has (selection.KVStore, selection.PGStore).
type familyStore interface {
	selection.Store
	WriteSentinel(ctx context.Context, state selection.StoreState, revision uint64) (uint64, error)
}
