package consumernames

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// IntakeConsumersTable is the Postgres table PGStore keeps Dispatch's and the Legion daemons'
// selection records in. The family's migration creates it as:
//
//	create table intake_consumers (
//	    logical  text primary key,
//	    record   jsonb not null,
//	    revision bigint not null check (revision > 0)
//	);
//
// Several families' stores can share one database, as several projects' daemons do, so every row
// is keyed by its family: a record of logical name L in family F's store is the row F/L (PGKey),
// never deleted, whose record is a Record carrying F/L as its own key; F's sentinel is the row
// envoy.sentinel/F (PGSentinelKey), whose record is a StoreState carrying that key. A family holds
// no '/' but a daemon's, after "daemon", and a logical name none, so no two rows of two families
// share a key, and a family's rows are exactly those whose key begins F/.
const IntakeConsumersTable = "intake_consumers"

// pgSentinelPrefix begins every PGStore sentinel's key (PGSentinelKey). It holds a '.', which no
// family holds, so no family's record key begins with it.
const pgSentinelPrefix = "envoy.sentinel/"

// PGSentinelKey is the key of family's sentinel row: envoy.sentinel/<family>, for example
// envoy.sentinel/dispatch or envoy.sentinel/daemon/<project>.
func PGSentinelKey(family string) string { return pgSentinelPrefix + family }

// PGKey is the key of logical's record row in family's store: <family>/<logical>, for example
// dispatch/delivery-events or daemon/<project>/legion-go-<project>-github.
func PGKey(family, logical string) string { return family + "/" + logical }

// undefinedTable is Postgres's SQLSTATE for a relation that does not exist.
const undefinedTable = "42P01"

// PGQuerier is what PGStore runs its statements on: a *pgxpool.Pool, a *pgx.Conn or a pgx.Tx.
type PGQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PGStore is the Store of one family over IntakeConsumersTable, which it reads and writes only
// under its own family's keys (PGKey, PGSentinelKey). A write at revision 0 inserts the row at
// revision 1 and is refused when the row exists; a write at a revision above 0 sets the next
// revision where the row still holds that one. Either refusal is ErrConflict.
type PGStore struct {
	db PGQuerier
	// family is the family the store reads and writes (NewPGStore).
	family string
}

// NewPGStore is the store of family's selection records on db: DispatchFamily for Dispatch's,
// DaemonFamily(project) for a daemon's. It reads and writes only family's sentinel and records,
// and its sentinel must name family (StoreState.Family). A family no store keeps is refused, as
// ErrInvalidFamily naming it (checkFamily), since its keys could reach another family's rows.
func NewPGStore(family string, db PGQuerier) (*PGStore, error) {
	if err := checkFamily(family); err != nil {
		return nil, err
	}
	return &PGStore{db: db, family: family}, nil
}

// State implements Store: it reads the family's sentinel row. No row is a missing sentinel
// (ErrNoSentinel); a row whose record is not the family's sentinel (decodeSentinel) is
// ErrMalformedSentinel, never a missing one.
func (s *PGStore) State(ctx context.Context) (StoreState, error) {
	key := PGSentinelKey(s.family)
	data, revision, err := s.row(ctx, key)
	if errors.Is(err, ErrNoRecord) {
		return StoreState{}, fmt.Errorf("%w: %s holds no %s row", ErrNoSentinel, IntakeConsumersTable, key)
	}
	if err != nil {
		return StoreState{}, err
	}
	state, err := decodeSentinel(key, s.family, data)
	if err != nil {
		return StoreState{}, fmt.Errorf("consumer names: %s's %s row at revision %d: %w", IntakeConsumersTable, key, revision, err)
	}
	return state, nil
}

// Read implements Store. A row whose record is not the family's record of logical, carrying the
// row's own key (decodeRecord), is ErrMalformedRecord: a record copied there from another family's
// row carries that family's key.
func (s *PGStore) Read(ctx context.Context, logical string) (Record, uint64, error) {
	key := PGKey(s.family, logical)
	data, revision, err := s.row(ctx, key)
	if err != nil {
		return Record{}, 0, err
	}
	record, err := decodeRecord(key, data)
	if err != nil {
		return Record{}, 0, fmt.Errorf("consumer names: %s's row of %s at revision %d: %w", key, IntakeConsumersTable, revision, err)
	}
	return record, revision, nil
}

// Write implements Store. It writes the record under the family's key of logical, which the record
// carries as its own (Record.Key). A record every read would refuse (writable) is refused as
// ErrMalformedRecord, and nothing is written.
func (s *PGStore) Write(ctx context.Context, logical string, record Record, revision uint64) (uint64, error) {
	record.Key = PGKey(s.family, logical)
	if err := writable(record); err != nil {
		return 0, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return 0, err
	}
	return s.put(ctx, record.Key, data, revision)
}

// WriteSentinel writes the family's sentinel row, under its own key (StoreState.Key) and naming the
// store's family, which it sets when state names none (ownSentinel), if its revision is still
// revision (0: never written). A state naming another family is refused as ErrSentinelFamily, and
// nothing is written.
func (s *PGStore) WriteSentinel(ctx context.Context, state StoreState, revision uint64) (uint64, error) {
	state, err := ownSentinel(state, PGSentinelKey(s.family), s.family)
	if err != nil {
		return 0, fmt.Errorf("consumer names: %s's %s row: %w", IntakeConsumersTable, PGSentinelKey(s.family), err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return 0, err
	}
	return s.put(ctx, state.Key, data, revision)
}

// PGRows is what List runs its query on: a *pgxpool.Pool, a *pgx.Conn or a pgx.Tx.
type PGRows interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// List reads every record of the family, by logical name: what an operator's status lists, retired
// names included. It reads no sentinel and no other family's record. It needs a db that is also a
// PGRows.
func (s *PGStore) List(ctx context.Context) (map[string]Record, error) {
	db, ok := s.db.(PGRows)
	if !ok {
		return nil, fmt.Errorf("consumer names: %T cannot list %s", s.db, IntakeConsumersTable)
	}
	prefix := PGKey(s.family, "")
	every := "every logical name of " + s.family
	// left() rather than like: a family's project may hold '_', which like reads as a wildcard.
	rows, err := db.Query(ctx, `select logical, record from `+IntakeConsumersTable+` where left(logical, length($1)) = $1 order by logical`, prefix)
	if err != nil {
		return nil, s.failed("list", every, err)
	}
	defer rows.Close()
	records := map[string]Record{}
	for rows.Next() {
		var key string
		var data []byte
		if err := rows.Scan(&key, &data); err != nil {
			return nil, s.failed("list", every, err)
		}
		record, err := decodeRecord(key, data)
		if err != nil {
			return nil, fmt.Errorf("consumer names: %s's row of %s: %w", key, IntakeConsumersTable, err)
		}
		records[strings.TrimPrefix(key, prefix)] = record
	}
	if err := rows.Err(); err != nil {
		return nil, s.failed("list", every, err)
	}
	return records, nil
}

// row reads the row of key.
func (s *PGStore) row(ctx context.Context, key string) ([]byte, uint64, error) {
	var data []byte
	var revision int64
	err := s.db.QueryRow(ctx, `select record, revision from `+IntakeConsumersTable+` where logical = $1`, key).Scan(&data, &revision)
	if err != nil {
		return nil, 0, s.failed("read", key, err)
	}
	return data, uint64(revision), nil
}

// put writes data as the row of key if the row is still at revision (0: never written).
func (s *PGStore) put(ctx context.Context, key string, data []byte, revision uint64) (uint64, error) {
	var next int64
	var err error
	if revision == 0 {
		err = s.db.QueryRow(ctx, `insert into `+IntakeConsumersTable+` (logical, record, revision) values ($1, $2, 1)
			on conflict (logical) do nothing returning revision`, key, data).Scan(&next)
	} else {
		err = s.db.QueryRow(ctx, `update `+IntakeConsumersTable+` set record = $2, revision = revision + 1
			where logical = $1 and revision = $3 returning revision`, key, data, int64(revision)).Scan(&next)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: %s's row of %s is not at revision %d", ErrConflict, key, IntakeConsumersTable, revision)
	}
	if err != nil {
		return 0, s.failed("write", key, err)
	}
	return uint64(next), nil
}

func (s *PGStore) failed(doing, key string, err error) error {
	var pg *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNoRecord
	case errors.As(err, &pg) && pg.Code == undefinedTable:
		return fmt.Errorf("%w: %s (%w)", ErrNoStore, IntakeConsumersTable, err)
	}
	return fmt.Errorf("consumer names: %s %s's row of %s: %w", doing, key, IntakeConsumersTable, err)
}
