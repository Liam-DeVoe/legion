package consumernames

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nats-io/nats.go"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/infra/writerhold"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
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

// selectionStreamName is the stream of the selection bucket (selection.SessionBucket), which holds
// the session family's records.
const selectionStreamName = "KV_" + selection.SessionBucket

// pgFamilies are the families whose store is a Postgres intake_consumers table: Dispatch, and each
// daemon that --daemon-database, the broker's cutover rows or a consumer of names (status passes
// the notification stream's; cleanup none) names, in order.
func pgFamilies(o options, cutovers []holdrecord.CutoverRow, names ...string) []string {
	daemons := map[string]bool{}
	for _, project := range o.daemonProjects() {
		daemons[daemonFamily(project)] = true
	}
	for _, c := range cutovers {
		if strings.HasPrefix(c.Family, familyDaemonPrefix) {
			daemons[c.Family] = true
		}
	}
	for _, name := range names {
		if m, ok := classify(selection.LogicalOf(name)); ok && m.Project != "" {
			daemons[m.Family] = true
		}
	}
	return append([]string{familyDispatch}, slices.Sorted(maps.Keys(daemons))...)
}

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

// dispatchDurables are Dispatch's two durables' names (bus.DispatchDeliveryDurable,
// bus.DispatchJobDurable), the names its intakes bind.
func dispatchDurables() []string {
	return []string{bus.DispatchDeliveryDurable, bus.DispatchJobDurable}
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

// pgUndefinedTable is Postgres's SQLSTATE for a relation that does not exist: a store whose table
// no migration made.
const pgUndefinedTable = "42P01"

// pgNoTable reports whether err is Postgres's answer for a table that does not exist.
func pgNoTable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == pgUndefinedTable
}

// readPGSentinel reads the sentinel row of the intake_consumers table on db, its record as stored
// and its revision, which selection.PGStore.State does not answer and the conditional write needs
// (selection.PGStore.WriteSentinel), and found false for a table that holds none. Its error is the
// query's, which pgNoTable reads for a table that does not exist; each caller decodes the record by
// its own rule (enrollReadMarker, censusReadSentinel).
func readPGSentinel(ctx context.Context, db Database) (record []byte, revision uint64, found bool, err error) {
	var stored int64
	err = db.QueryRow(ctx, `select record, revision from `+selection.IntakeConsumersTable+` where logical = $1`, selection.PGSentinelKey).
		Scan(&record, &stored)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, 0, false, nil
	case err != nil:
		return nil, 0, false, err
	}
	return record, uint64(stored), true, nil
}

// behindBarrier runs read behind the hold's bucket barrier on the selection bucket, the one path
// every held reader of the bucket takes (writerhold.ReadBucket: the barrier put
// hold.barrier.<hold id>, then read, with the stream's leader read before the barrier and after
// the read, and both made again when it changed), its UNKNOWN answered as this package's. read's
// own error comes back as it is. The bucket must exist: ReadBucket's leader read of a stream the
// server does not hold is UNKNOWN, so a reader that answers a missing bucket otherwise reads its
// stream first.
func behindBarrier(ctx context.Context, nc *nats.Conn, holdID string, read func(context.Context) error) error {
	return hubUnknown(writerhold.ReadBucket(ctx, nc, selection.SessionBucket, holdID, read),
		"the bucket barrier of "+selection.SessionBucket)
}

// errEscapeCut is a selection.DurableKey key that ends inside an escape (durableOf).
var errEscapeCut = errors.New("ends inside an escape")

// durableOf is the durable name a selection.DurableKey key names: the key's prefix cut, and each
// '=' and the two hex digits after it written back as the byte they stand for. A key that ends
// inside an escape is errEscapeCut, and one holding an escape that is not two hex digits is refused
// too, each error naming what is wrong for its caller to name the key before it. It does not hold
// key to the form DurableKey writes: replay and restore compare the member's key with it
// (replayMember, restoreMember), and the census reads a key in another form as no durable's
// (censusDurableOf).
func durableOf(key string) (string, error) {
	escaped, _ := strings.CutPrefix(key, selection.DurableKey(""))
	var name strings.Builder
	for rest := escaped; rest != ""; {
		before, after, found := strings.Cut(rest, "=")
		name.WriteString(before)
		if !found {
			break
		}
		if len(after) < 2 {
			return "", errEscapeCut
		}
		b, err := strconv.ParseUint(after[:2], 16, 8)
		if err != nil {
			return "", fmt.Errorf("holds the escape =%s, which is not '=' and two hex digits", after[:2])
		}
		name.WriteByte(byte(b))
		rest = after[2:]
	}
	return name.String(), nil
}
