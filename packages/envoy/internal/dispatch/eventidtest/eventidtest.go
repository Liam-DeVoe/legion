// Package eventidtest holds the two patterns the event id guards read SQL with: the Go sources'
// guard (internal/dispatch/api, TestOnlyAnEventInsertsDefaultDrawsAnEventID) and the migrations'
// guard (internal/dispatch/store, TestNoMigrationDrawsAnEventIDOutsideTheInsertDefault) compile
// these strings, so the two read one spelling of the sequence and of the one read of it that is
// allowed. It is an ordinary package rather than a _test.go file because Go cannot share test-only
// code across packages, and it imports nothing.
//
// The guards hold the safe horizon's proof (internal/dispatch/events/horizon.go, "Safe horizon"):
// an event id is drawn only by the column default of the insert that writes its row, so the
// writer holds RowExclusiveLock on events before it holds the id.
package eventidtest

// SequencePattern names the sequence the events id default draws from: by its name, bare, quoted
// or schema-qualified (\b holds at a quote or a dot), or through pg_get_serial_sequence with the
// table in any of its spellings, or with a table argument that is no string literal (a parameter,
// a variable, a dollar-quoted string), which no reading of the text can rule out. A guard reads it
// over lowercased SQL.
const SequencePattern = `\bevents_id_seq\b|pg_get_serial_sequence\(\s*'\s*(?:"?public"?\s*\.\s*)?"?events"?\s*'|pg_get_serial_sequence\(\s*[^'\s]`

// CurrvalPattern is a read of the id this session's nextval last drew from events_id_seq,
// pg_catalog.currval of the sequence in SequencePattern's literal spellings: a write phase reads
// the id its own insert's default just drew (store.LatestEventID,
// `pg_catalog.currval(pg_catalog.pg_get_serial_sequence('events', 'id'))`). currval draws no id and
// moves no counter, so a guard takes each such read out before it looks for SequencePattern. Only
// pg_catalog's own currval is that read, with nothing inside it a schema can take over: a name
// resolves by the search path and by argument type, so a migration's currval(text) is called
// before pg_catalog's currval(regclass) for a text argument, a pg_get_serial_sequence of a schema
// placed before pg_catalog is called before pg_catalog's, and a `::regclass` cast can name a domain
// of such a schema whose check runs at the cast; each of them can draw from the sequence. So the
// read takes either a bare string literal or pg_catalog.pg_get_serial_sequence, and no cast.
// pg_catalog must start a name: it may not continue a longer one (a$pg_catalog.currval, a Postgres
// identifier carrying `$` or a letter past ASCII), and nothing but spaces may stand between it, the
// dot and currval (util./**/currval is util's). A currval of a sequence argument that is no string
// literal is no such read, so SequencePattern's last branch still reads it.
const CurrvalPattern = `(?:^|[^\w.$\x{80}-\x{10FFFF}])pg_catalog\s*\.\s*currval\(\s*(?:'\s*(?:"?public"?\s*\.\s*)?"?events_id_seq"?\s*'|pg_catalog\s*\.\s*pg_get_serial_sequence\(\s*'\s*(?:"?public"?\s*\.\s*)?"?events"?\s*'\s*,\s*'\s*"?id"?\s*'\s*\))\s*\)`
