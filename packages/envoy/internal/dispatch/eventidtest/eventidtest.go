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
// a variable, a dollar-quoted string), which no reading of the text can rule out. It also reads a
// nextval or setval whose sequence is not one string literal, bare or cast to regclass: a name
// built from parts ('events_' || 'id_seq', format('%s_id_seq', 'events')), written with escapes
// (E'events\x5fid\x5fseq', U&'events\005fid\005fseq'), cast through text, or passed as a parameter
// can be events_id_seq without spelling it. So can a literal of digits alone, which regclass reads
// as an OID (nextval('16402')), or a literal that carries a character no sequence name needs:
// standard_conforming_strings off reads `events\137id\137seq` as events_id_seq. So only a literal
// that spells its sequence's name is read as another sequence (nextval('other_seq'), setval('other_seq', 1)),
// and only in a call that follows the name at once: every other place the word nextval or setval
// stands is read as a draw, since a quoted name ("nextval"('16402')), or a comment between the
// name and its parenthesis (nextval/**/('16402')), calls it all the same. A name in Unicode escapes
// (U&"\006Eextval") spells no word at all, so every such identifier is read as a draw, and so is
// the C function behind nextval or setval (nextval_oid, setval_oid, setval3_oid), which a function
// of another name in the internal language can call. A guard reads it over lowercased SQL.
const SequencePattern = `\bevents_id_seq\b|pg_get_serial_sequence\(\s*'\s*(?:"?public"?\s*\.\s*)?"?events"?\s*'|pg_get_serial_sequence\(\s*[^'\s]|\b(?:nextval|setval)\b\s*(?:$|[^\s(]|\(\s*(?:$|[^'\s]|'[^']*(?:$|'\s*(?:$|[^\s,):]|:(?:$|[^:]|:\s*(?:$|[^\sr]))))))|\b(?:nextval|setval)\s*\(\s*'\s*\d+\s*'|\b(?:nextval|setval)\s*\(\s*'[^']*[^'\w.$"\s\x{80}-\x{10FFFF}]|\bu&"|\b(?:nextval|setval3?)_oid\b`

// DynamicSQLPattern names a PostgreSQL construct whose argument or statement can carry SQL the
// guard cannot read: execute (except a trigger's execute function or execute procedure),
// query_to_xml and cursor_to_xml, with their schema variants. A query built at run time can draw
// events_id_seq without spelling it, as can an execute string built from parts, so a guard refuses
// each rather than pretend its source text proves the query harmless.
const DynamicSQLPattern = `\b(?:query_to_xml(?:schema|_and_xmlschema)?|cursor_to_xml(?:schema)?|execute)\b`

// TriggerExecutePattern is DynamicSQLPattern's one safe execute: it installs a trigger callback,
// and does not execute SQL text. The guards take this match out before finding DynamicSQLPattern.
const TriggerExecutePattern = `\bexecute\s+(?:function|procedure)\b`

// CurrvalPattern is a read of the id this session's nextval last drew from events_id_seq,
// pg_catalog.currval of the sequence in SequencePattern's literal spellings: a write phase reads
// the id its own insert's default just drew (store.LatestEventID,
// `pg_catalog.currval(pg_catalog.pg_get_serial_sequence('events', 'id'))`). currval draws no id and
// moves no counter, so a guard takes each such read out before it looks for SequencePattern. Only
// pg_catalog's own currval is that read, with nothing inside it another schema can take over: a
// name resolves by the search path and by argument type, so a migration's currval(text) is called
// before pg_catalog's currval(regclass) for a text argument, a pg_get_serial_sequence of a schema
// placed before pg_catalog is called before pg_catalog's, and a `::regclass` cast can name a domain
// of such a schema whose check runs at the cast; each of them can draw from the sequence. So the
// read takes either a bare string literal or pg_catalog.pg_get_serial_sequence, and no cast.
// pg_catalog must start a name: it may not continue a longer one (a$pg_catalog.currval, a Postgres
// identifier carrying `$` or a letter past ASCII), and nothing but spaces may stand between it, the
// dot and currval (util./**/currval is util's). pg_catalog itself can be changed only by a
// superuser, who can add a currval(text) to it or replace its currval or pg_get_serial_sequence;
// store.TestOnlyTheEventsIDDefaultDrawsFromItsSequence refuses a function of those names in
// pg_catalog that is not Postgres's own. A currval whose argument is not one of those spellings is
// no such read, and SequencePattern reads it as any other statement. The pattern takes the one
// character before pg_catalog with the read, since Go's regexp cannot look behind a match; a guard
// puts CurrvalReadKept(read) in its place, so that character stays.
const CurrvalPattern = `(?:^|[^\w.$\x{80}-\x{10FFFF}])pg_catalog\s*\.\s*currval\(\s*(?:'\s*(?:"?public"?\s*\.\s*)?"?events_id_seq"?\s*'|pg_catalog\s*\.\s*pg_get_serial_sequence\(\s*'\s*(?:"?public"?\s*\.\s*)?"?events"?\s*'\s*,\s*'\s*"?id"?\s*'\s*\))\s*\)`

// CurrvalReadKept is what a guard keeps of a read CurrvalPattern matched when it takes the read
// out: the character before pg_catalog that the pattern took with it, or nothing at the start of
// the text. That character can be the parenthesis of a call around the read, which must stay for
// SequencePattern to see the call: nextval(pg_catalog.currval('events_id_seq') * 0 + 16402) draws
// from the sequence whose OID is 16402.
func CurrvalReadKept(read string) string {
	if read[0] == 'p' {
		return ""
	}
	return read[:1]
}
