package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/texttrees"
)

// GET /api/v1/me/inbox is the one read of what is tied to a person (dispatch://INFRA-1152 R1): every
// node that holds a current relation to the caller's own person node, at either end of it, each
// answered once with every relation kind that ties it to them. It takes its relation kinds from the
// store and from no list here, so a kind a later write appends is an entry of this read the day it is
// appended; this file names the identity tuple alone (graphstore.TupleAliasOf), which ties a session
// the server proved to its person and puts nothing in the answer.
//
// The whole answer is one statement (meInboxStatement), which a reader observes rather than infers
// (TestOneReadAnswersEveryRelationToThePersonsNode). Its rows are the entries, one row an entry and
// nothing else, so count(*) over () over them is the count of the entries, taken after the
// visibility predicate and the kind narrowing. What a row the page draws carries beside its entry is
// columns of that same row: each one a record, or an array of records in the order its own statement
// answers in, of a statement the read it replaces runs today, spliced in over the entry's node
// (bindFragment) and read back by that statement's own scanner (meInboxRecord). A row's `page` column
// says which of the page's three kinds of row the entry is - ask, thread or agent - or none, and only
// that kind's columns hold anything: the others' laterals are fenced on it and run for no other row.
//
// The person is graphstore.PersonNode's canonical login, and a session the server proved theirs
// (the identity tuple into that node, graphstore.TupleAliasOf) is their node too. The read is at
// the event horizon notifications read at (sinceHorizon): a tuple whose event is above it is read
// as if it were not there yet, and a tuple written under no event of its own (the sentinel 0) is
// read whatever the horizon is.

// The two ends of a relation an entry carries: a tuple into the person's node, whose near end is
// the node it comes from, and one from the person's node, whose far end is the node it points at.
const (
	meInboxInbound  = "inbound"
	meInboxOutbound = "outbound"
)

// The kinds of row the page draws, as an entry's page column names them (meInboxPageRows).
const (
	meInboxPageAsk    = "ask"
	meInboxPageThread = "thread"
	meInboxPageAgent  = "agent"
)

// meInboxQueryKeys is every query key the route declares (meInboxQuery in routes_shapes.go), and
// meInboxIdentityKeys the identity selectors it refuses by the same rule: the read answers for the
// credential's own person alone, so a key naming somebody else selects nothing it could answer.
var (
	meInboxQueryKeys    = []string{"limit", "offset", "kind", "state", "cursor", "issue", "artifact", "project"}
	meInboxIdentityKeys = []string{"login", "person", "assignee", "session"}
)

// meInboxNode is the node at the other end of an entry's relations.
type meInboxNode struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// meInboxRelation is one relation kind that ties an entry's node to the person: the end of it the
// person's node holds, and the moment of the newest current tuple of that kind and end.
type meInboxRelation struct {
	Kind      string    `json:"kind"`
	Direction string    `json:"direction" enum:"inbound,outbound"`
	At        time.Time `json:"at"`
}

// meInboxAgent is an Agent row of the page with the conversation it draws: the one-off message the
// person sent a session, and every reply under it in order, each with its deliveries.
type meInboxAgent struct {
	model.MyReplyRow
	Conversation messageRead `json:"conversation"`
}

// meInboxEntry is one node tied to the person. Ask, Thread and Agent are the row the page draws for
// that node, at most one of them, and none for a node the page draws no row for.
type meInboxEntry struct {
	Node      meInboxNode       `json:"node"`
	Relations []meInboxRelation `json:"relations"`
	// Moment is when the entry last moved: under a state the time that state's own list orders the
	// row by (the newest reply somebody else wrote, or the dismissal), and otherwise the newest of
	// its relations.
	Moment time.Time         `json:"moment"`
	Ask    *inboxAsk         `json:"ask,omitempty"`
	Thread *model.MyReplyRow `json:"thread,omitempty"`
	Agent  *meInboxAgent     `json:"agent,omitempty"`
}

// meInboxResponse is what GET /api/v1/me/inbox answers.
type meInboxResponse struct {
	Person     string         `json:"person"`
	AsOf       time.Time      `json:"as_of"`
	Horizon    int64          `json:"horizon"`
	Total      int            `json:"total"`
	Limit      int            `json:"limit"`
	Offset     int            `json:"offset"`
	NextCursor string         `json:"next_cursor,omitzero"`
	Entries    []meInboxEntry `json:"entries"`
}

// meInboxRequest is a request's query, read and checked (parseMeInboxRequest).
type meInboxRequest struct {
	// state is the page's narrowing - unread, inbox or dismissed - or "" for the tuple walk.
	state string
	// kinds narrows the answer to the entries of these relation kinds, nil for every kind.
	kinds    []string
	limit    *int
	offset   int
	cursor   *meInboxCursor
	issue    string
	artifact *string
	project  string
}

// owned is whether the request names one owner, an issue or a document. No attribute every node
// carries says which issue or document it belongs to, so an owner keeps the page's rows of that
// owner alone (meInboxPageRows) and no other entry; an Agent row has no owner, so it is none of
// them. A project is a base attribute of every node that has one (graph_nodes.project_key), and
// narrows every entry by it.
func (request meInboxRequest) owned() bool {
	return request.issue != "" || request.artifact != nil
}

// meInboxCursor is where a page resumes: the last entry the page before it answered, in the order
// the entries are read in.
type meInboxCursor struct {
	moment   time.Time
	kind, id string
}

func (cursor meInboxCursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(
		cursor.moment.UTC().Format(time.RFC3339Nano) + "\n" + cursor.kind + "\n" + cursor.id))
}

// parseMeInboxCursor reads one ?cursor=, which is a next_cursor this route answered.
func parseMeInboxCursor(raw string) (*meInboxCursor, error) {
	refusal := errorf(http.StatusBadRequest, "INVALID_QUERY", "cursor is one this route answered in next_cursor")
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, refusal
	}
	parts := strings.SplitN(string(decoded), "\n", 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return nil, refusal
	}
	moment, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, refusal
	}
	return &meInboxCursor{moment: moment, kind: parts[1], id: parts[2]}, nil
}

// parseMeInboxRequest reads the query of GET /api/v1/me/inbox. A key the route does not declare is
// refused by name rather than ignored, and so is an identity selector, by the same rule: the
// Params struct of a route is documentation no handler reads (routes_shapes.go), so this is where
// the route holds its query to what it declares.
func parseMeInboxRequest(r *http.Request) (meInboxRequest, error) {
	query := r.URL.Query()
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for key := range query {
			if !yield(key) {
				return
			}
		}
	}) {
		switch {
		case slices.Contains(meInboxQueryKeys, key):
		case slices.Contains(meInboxIdentityKeys, key):
			return meInboxRequest{}, errorf(http.StatusBadRequest, "INVALID_QUERY",
				"%s is not a query parameter of GET /api/v1/me/inbox, which answers for the credential's own person alone", key)
		default:
			return meInboxRequest{}, errorf(http.StatusBadRequest, "INVALID_QUERY",
				"%s is not a query parameter of GET /api/v1/me/inbox, which takes %s", key, strings.Join(meInboxQueryKeys, ", "))
		}
	}
	var request meInboxRequest
	switch value := strings.TrimSpace(query.Get("state")); value {
	case "", myRepliesStateUnread, myRepliesStateInbox, myRepliesStateDismissed:
		request.state = value
	default:
		return meInboxRequest{}, errorf(http.StatusBadRequest, "INVALID_QUERY", "state is %s, %s or %s",
			myRepliesStateUnread, myRepliesStateInbox, myRepliesStateDismissed)
	}
	for _, listed := range query["kind"] {
		for _, kind := range strings.Split(listed, ",") {
			if kind = strings.TrimSpace(kind); kind != "" && !slices.Contains(request.kinds, kind) {
				request.kinds = append(request.kinds, kind)
			}
		}
	}
	if value, present, valid := parseQueryInt(query, "limit", 1, myAnswersMaxLimit); present {
		if !valid {
			return meInboxRequest{}, errorf(http.StatusBadRequest, "INVALID_QUERY", "limit must be one integer from 1 to %d", myAnswersMaxLimit)
		}
		request.limit = &value
	}
	if value, present, valid := parseQueryInt(query, "offset", 0, math.MaxInt); present {
		if !valid {
			return meInboxRequest{}, errorf(http.StatusBadRequest, "INVALID_QUERY", "offset must be one non-negative integer")
		}
		request.offset = value
	}
	if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
		cursor, err := parseMeInboxCursor(raw)
		if err != nil {
			return meInboxRequest{}, err
		}
		request.cursor = cursor
	}
	issue, artifact, err := parseThreadOwner(query.Get("issue"), query.Get("artifact"))
	if err != nil {
		return meInboxRequest{}, err
	}
	request.issue, request.artifact = issue, artifact
	request.project = strings.TrimSpace(query.Get("project"))
	return request, nil
}

// listMeInbox answers GET /api/v1/me/inbox: the entries tied to the caller's person, newest moment
// first, in one statement, and the rows the page draws for those it draws.
//
// The statement is the whole of the database work the handler does: actingPerson resolves the
// person before it, the horizon is read in process (sinceHorizon, whose observation runs on the
// horizon's own pool), and what follows it is Go over the rows it answered. The display trees the
// page asks for (Dispatch-Trees: 1) are read in it too, and a text it holds no tree of is read for
// display and stored back after the answer, off the request, as every reader's misses are. The
// listener's roster is read off the stored answer in it, and asked of the listener alone where that
// answer is stale.
func (s *server) listMeInbox(w http.ResponseWriter, r *http.Request) {
	request, err := parseMeInboxRequest(r)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	caller, ok := s.actingPerson(w, r)
	if !ok {
		return
	}
	horizon, err := s.sinceHorizon(r.Context())
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	trees := wantsDisplayTrees(r)
	readsRoster := s.deps.Envoy != nil && !s.deps.ListenerRosterUncached
	built, known := s.rosters.known()
	statement, args, err := meInboxStatement(caller, horizon, request, trees, readsRoster, known)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	rows, err := s.deps.Store.Pool.Query(r.Context(), statement, args...)
	if err != nil {
		s.writeHandlerError(w, fmt.Errorf("read the inbox: %w", err))
		return
	}
	read, err := readMeInboxRows(rows)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	response := meInboxResponse{
		Person: model.CanonicalLogin(caller.ID), AsOf: read.asOf, Horizon: horizon,
		Total: read.total, Offset: request.offset, Entries: []meInboxEntry{},
	}
	if request.limit != nil {
		response.Limit = *request.limit
	}
	reader := s.readDisplayTreesHeld(r.Context(), trees, read.trees)
	if err := s.meInboxAsks(r.Context(), read, built, reader); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	for index := range read.entries {
		entry := &read.entries[index]
		if entry.Agent != nil {
			entry.Agent.Conversation.Message.shape(reader)
			entry.Agent.Conversation.Replies = shapedMessages(entry.agentReplies, reader)
		}
		// An entry's rows are pointers, so a tree the reader fills below lands in the answer.
		response.Entries = append(response.Entries, entry.meInboxEntry)
	}
	reader.resolve()
	if count := len(response.Entries); count > 0 && read.remaining > request.offset+count {
		last := response.Entries[count-1]
		response.NextCursor = meInboxCursor{moment: last.Moment, kind: last.Node.Kind, id: last.Node.ID}.String()
	}
	WriteJSON(w, http.StatusOK, response)
}

// meInboxAsks finishes the ask rows of the page: the details every list of asks carries beside its
// row (queueAskDetails: the identity event, the inbound edge count and the part ids), each thread's
// replies with the targets each names, its attempts and its block, its rewordings, followers and
// reach (loadAskThreads, the route's reach read off the roster), then each row's shape, in the
// order listInbox takes them.
func (s *server) meInboxAsks(ctx context.Context, read *meInboxRead, built routeRoster, reader *displayTreeReader) error {
	var asks []*model.Ask
	for index := range read.entries {
		if ask := read.entries[index].Ask; ask != nil {
			asks = append(asks, &ask.Ask)
		}
	}
	if len(asks) == 0 {
		return nil
	}
	if err := applyOpenedEventIDs(asks, read.openedEvents); err != nil {
		return err
	}
	applyAskBacklinkCounts(asks, read.backlinkCounts)
	applyAskPartIDs(asks, read.askParts.IDs(asks))
	routed := false
	for index := range read.entries {
		if thread := read.entries[index].askThread; thread != nil && thread.Route != nil {
			routed = true
		}
	}
	var roster routeRoster
	if routed {
		roster = s.meInboxRoster(ctx, read.roster, built)
	}
	for index := range read.entries {
		entry := &read.entries[index]
		if entry.Ask == nil {
			continue
		}
		thread := *entry.askThread
		if routed {
			thread.IssueRouteReach = roster.reach(thread.Route)
		}
		thread.Replies = shapedComments(entry.askReplies, reader)
		thread.Edits = askEditReads(entry.askEdits, reader)
		entry.Ask.Thread = thread
		// The row's shape is taken once the ask is final: the part ids of its options and of its
		// answer are written into the row these fields stand in front of above.
		entry.Ask.shape(reader)
	}
	return nil
}

// meInboxRoster is the listener's roster for the page's routes, as routeRosterOrUnknown answers it:
// none where no listener is configured, the listener's own answer where this process asks it on
// every read, and otherwise the stored answer the statement read (record, read beside built, the
// roster this process had built when the statement was written), fresh or fetched again. A stored
// answer this process cannot read is logged, and every route then reads unknown.
func (s *server) meInboxRoster(ctx context.Context, record meInboxRecord, built routeRoster) routeRoster {
	if s.deps.Envoy == nil {
		return routeRoster{reason: model.RouteReasonUnreachable}
	}
	if s.deps.ListenerRosterUncached {
		return s.listRouteRoster(ctx)
	}
	roster, fresh, now, seen, err := s.rosters.stored(record, built)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("dispatch: routes read unknown", "error", fmt.Errorf("read the stored listener answer: %w", err))
		}
		return routeRoster{reason: model.RouteReasonUnreachable}
	}
	return s.routeRosterAfterStored(ctx, roster, fresh, now, seen)
}

// meInboxRead is what the statement answered, each record read by its own row's scanner.
type meInboxRead struct {
	asOf             time.Time
	total, remaining int
	entries          []meInboxRow
	openedEvents     map[string]int64
	backlinkCounts   map[string]int
	askParts         refs.AskPartRows
	trees            map[[32]byte]json.RawMessage
	roster           meInboxRecord
}

// meInboxRow is one entry as the statement answered it, with the parts of its page row that are
// finished once every row is read (meInboxAsks) or shaped for the request's reader.
type meInboxRow struct {
	meInboxEntry
	askThread    *askThread
	askReplies   []model.Comment
	askEdits     []model.AskEdit
	agentReplies []model.Message
}

// meInboxRecord is one record a column of the statement holds, read as a row of the statement it
// was spliced from (pgx.Row), so that statement's own scanner reads it: the field count is held to
// the scanner's, and each field is decoded by its own type, as a column of that statement would be.
type meInboxRecord struct {
	types *pgtype.Map
	raw   []byte
}

func (record meInboxRecord) Scan(dest ...any) error {
	if fields := pgtype.NewCompositeBinaryScanner(record.types, record.raw).FieldCount(); fields != len(dest) {
		return fmt.Errorf("read a record of %d fields into %d", fields, len(dest))
	}
	return record.types.Scan(pgtype.RecordOID, pgtype.BinaryFormatCode, record.raw, pgtype.CompositeFields(dest))
}

// meInboxColumns is what each row of the statement holds, in its select's order.
type meInboxColumns struct {
	asOf                                      time.Time
	roster                                    pgtype.UndecodedBytes
	nodeKind, nodeID, page                    *string
	moment                                    *time.Time
	kinds, directions                         []string
	ats                                       []time.Time
	total, remaining                          *int64
	agent                                     pgtype.UndecodedBytes
	ask, opened, counted, parts, reach        pgtype.UndecodedBytes
	replies, replyNamed, replyAttempts        []pgtype.UndecodedBytes
	askBlocks, commentBlocks, edits, follower []pgtype.UndecodedBytes
	askTrees                                  []pgtype.UndecodedBytes
	thread                                    pgtype.UndecodedBytes
	root                                      pgtype.UndecodedBytes
	messages, attempts, messageBlocks         []pgtype.UndecodedBytes
	title                                     *string
	agentTrees                                []pgtype.UndecodedBytes
}

func (columns *meInboxColumns) targets() []any {
	return []any{
		&columns.asOf, &columns.roster, &columns.total, &columns.remaining,
		&columns.nodeKind, &columns.nodeID, &columns.moment, &columns.kinds, &columns.directions, &columns.ats,
		&columns.page, &columns.agent,
		&columns.ask, &columns.opened, &columns.counted, &columns.parts, &columns.reach,
		&columns.replies, &columns.replyNamed, &columns.replyAttempts, &columns.askBlocks, &columns.commentBlocks,
		&columns.edits, &columns.follower, &columns.askTrees,
		&columns.thread,
		&columns.root, &columns.messages, &columns.attempts, &columns.messageBlocks, &columns.title, &columns.agentTrees,
	}
}

// readMeInboxRows reads every row the statement answered. The statement answers one row however
// few entries there are, so that its now() is read: a row naming no node is that row alone.
func readMeInboxRows(rows pgx.Rows) (*meInboxRead, error) {
	defer rows.Close()
	read := &meInboxRead{
		openedEvents: map[string]int64{}, backlinkCounts: map[string]int{},
		askParts: refs.AskPartRows{}, trees: map[[32]byte]json.RawMessage{},
	}
	types := rows.Conn().TypeMap()
	answered := false
	for rows.Next() {
		answered = true
		var columns meInboxColumns
		if err := rows.Scan(columns.targets()...); err != nil {
			return nil, fmt.Errorf("read the inbox: %w", err)
		}
		read.asOf = columns.asOf
		// The counts are the entries' window counts, read off the head row so that a page past
		// the last entry still says how many there are.
		read.total, read.remaining = int(*columns.total), int(*columns.remaining)
		if columns.roster != nil {
			read.roster = meInboxRecord{types: types, raw: columns.roster}
		}
		if columns.nodeKind == nil {
			continue
		}
		row, err := read.entry(types, &columns)
		if err != nil {
			return nil, err
		}
		read.entries = append(read.entries, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the inbox: %w", err)
	}
	if !answered {
		return nil, errMeInboxNoRow
	}
	return read, nil
}

// records is each of raw as a record of the statement.
func records(types *pgtype.Map, raw []pgtype.UndecodedBytes) []meInboxRecord {
	read := make([]meInboxRecord, len(raw))
	for index := range raw {
		read[index] = meInboxRecord{types: types, raw: raw[index]}
	}
	return read
}

// entry reads one row naming a node: its relations, and the page row its page column names.
func (read *meInboxRead) entry(types *pgtype.Map, columns *meInboxColumns) (meInboxRow, error) {
	row := meInboxRow{meInboxEntry: meInboxEntry{
		Node:      meInboxNode{Kind: *columns.nodeKind, ID: *columns.nodeID},
		Relations: make([]meInboxRelation, len(columns.kinds)),
		Moment:    *columns.moment,
	}}
	for index := range columns.kinds {
		row.Relations[index] = meInboxRelation{Kind: columns.kinds[index], Direction: columns.directions[index], At: columns.ats[index]}
	}
	page := ""
	if columns.page != nil {
		page = *columns.page
	}
	var err error
	switch page {
	case meInboxPageAsk:
		err = read.askRow(types, columns, &row)
	case meInboxPageThread:
		var thread model.MyReplyRow
		if thread, err = scanMyReplyRow(meInboxRecord{types: types, raw: columns.thread}); err == nil {
			row.Thread = &thread
		}
	case meInboxPageAgent:
		err = read.agentRow(types, columns, &row)
	}
	if err != nil {
		return meInboxRow{}, fmt.Errorf("read the %s row of %s %s: %w", page, row.Node.Kind, row.Node.ID, err)
	}
	return row, nil
}

// askRow reads an Ask row as listInbox reads one (scanAskRead) and what its thread holds as
// loadAskThreads reads it: the reply chain (inboxAskReplyChainsQuery) with the targets each reply
// names, its attempts and its named block (hydrateCommentSideTables), the rewordings, the followers
// and the claim and route. Answers stay empty, as an Inbox row is an open ask.
func (read *meInboxRead) askRow(types *pgtype.Map, columns *meInboxColumns, row *meInboxRow) error {
	var ask inboxAsk
	var issueKey, issueTitle, issueAssignee, documentProject, documentSlug, documentName *string
	var snoozedUntil *time.Time
	scanned, reply, err := scanAskRead(meInboxRecord{types: types, raw: columns.ask},
		&issueKey, &issueTitle, &issueAssignee, &ask.Priority, &documentProject, &documentSlug, &documentName, &snoozedUntil)
	if err != nil {
		return err
	}
	ask.Ask = scanned
	ask.LastReply = reply
	if snoozedUntil != nil {
		stamp := timestampValue(*snoozedUntil)
		ask.SnoozedUntil = &stamp
	}
	if issueKey != nil {
		ask.Issue = &inboxIssue{Key: *issueKey, Title: *issueTitle, Assignee: issueAssignee}
	}
	if documentProject != nil {
		ask.Document = &inboxDocument{Project: *documentProject, Slug: *documentSlug, Name: *documentName}
	}
	if columns.opened != nil {
		askID, event, err := events.ScanOpenedEventID(meInboxRecord{types: types, raw: columns.opened})
		if err != nil {
			return err
		}
		read.openedEvents[askID] = event
	}
	if columns.counted != nil {
		var target string
		var count int
		if err := (meInboxRecord{types: types, raw: columns.counted}).Scan(&target, &count); err != nil {
			return fmt.Errorf("scan ask backlink count: %w", err)
		}
		read.backlinkCounts[target] = count
	}
	if columns.parts != nil {
		if err := read.askParts.Scan(meInboxRecord{types: types, raw: columns.parts}); err != nil {
			return err
		}
	}
	thread := askThread{
		Answers:   []model.AskAnswerWithTree{},
		Followers: []model.AskFollower{},
	}
	if columns.reach != nil {
		if _, thread.askReach, err = scanAskReach(meInboxRecord{types: types, raw: columns.reach}); err != nil {
			return err
		}
	}
	replies := []model.Comment{}
	for _, record := range records(types, columns.replies) {
		var askID string
		comment, err := scanComment(record, &askID)
		if err != nil {
			return fmt.Errorf("scan inbox ask reply: %w", err)
		}
		replies = append(replies, comment)
	}
	named := map[string][]model.Mention{}
	for _, record := range records(types, columns.replyNamed) {
		commentID, mention, err := scanCommentMention(record)
		if err != nil {
			return err
		}
		named[commentID] = append(named[commentID], mention)
	}
	attempts := map[string][]model.CommentDelivery{}
	for _, record := range records(types, columns.replyAttempts) {
		attempt, err := scanCommentDelivery(record)
		if err != nil {
			return err
		}
		attempts[attempt.CommentID] = append(attempts[attempt.CommentID], attempt)
	}
	blocks := map[string]*model.ReplyBlock{}
	for kind, owned := range map[refs.ItemKind][]pgtype.UndecodedBytes{refs.ItemAsk: columns.askBlocks, refs.ItemComment: columns.commentBlocks} {
		for _, record := range records(types, owned) {
			replyID, block, err := refs.ScanReplyBlock(record, kind)
			if err != nil {
				return err
			}
			blocks[replyID] = block
		}
	}
	for index := range replies {
		if held, found := named[replies[index].ID]; found {
			replies[index].Mentions = held
		}
		if held, found := attempts[replies[index].ID]; found {
			replies[index].Deliveries = held
		}
		replies[index].ReplyBlock = blocks[replies[index].ID]
	}
	edits := []model.AskEdit{}
	for _, record := range records(types, columns.edits) {
		_, edit, err := scanInboxAskEdit(record)
		if err != nil {
			return err
		}
		edits = append(edits, edit)
	}
	for _, record := range records(types, columns.follower) {
		_, follower, err := scanInboxAskFollower(record)
		if err != nil {
			return err
		}
		thread.Followers = append(thread.Followers, follower)
	}
	if err := read.holdTrees(types, columns.askTrees); err != nil {
		return err
	}
	row.Ask, row.askThread, row.askReplies, row.askEdits = &ask, &thread, replies, edits
	return nil
}

// agentRow reads an Agent row as listMyReplies reads one (scanMyAgentRow), its session named as
// nameAgentSessions names it, and the conversation it draws as hydrateMessageThreads reads one: the
// root, every reply under it oldest first, each message's attempts and the block it names.
func (read *meInboxRead) agentRow(types *pgtype.Map, columns *meInboxColumns, row *meInboxRow) error {
	listed, err := scanMyAgentRow(meInboxRecord{types: types, raw: columns.agent})
	if err != nil {
		return err
	}
	titles := map[string]string{}
	if columns.title != nil {
		titles[listed.Session.ID] = *columns.title
	}
	listed.Session.Name = delivery.SessionTitle(titles, listed.Session.ID)
	root, err := scanMessage(meInboxRecord{types: types, raw: columns.root})
	if err != nil {
		return fmt.Errorf("scan the agent row's message: %w", err)
	}
	replies := []model.Message{}
	for _, record := range records(types, columns.messages) {
		var seedID string
		reply, err := scanMessage(record, &seedID)
		if err != nil {
			return fmt.Errorf("scan the agent row's reply: %w", err)
		}
		replies = append(replies, reply)
	}
	attempts := map[string][]model.MessageDelivery{}
	for _, record := range records(types, columns.attempts) {
		attempt, err := scanMessageDelivery(record)
		if err != nil {
			return err
		}
		attempts[attempt.MessageID] = append(attempts[attempt.MessageID], attempt)
	}
	blocks := map[string]*model.ReplyBlock{}
	for _, record := range records(types, columns.messageBlocks) {
		replyID, block, err := refs.ScanReplyBlock(record, refs.ItemMessage)
		if err != nil {
			return err
		}
		blocks[replyID] = block
	}
	for _, message := range append([]*model.Message{&root}, pointersTo(replies)...) {
		if held, found := attempts[message.ID]; found {
			message.Deliveries = held
		}
		message.ReplyBlock = blocks[message.ID]
	}
	if err := read.holdTrees(types, columns.agentTrees); err != nil {
		return err
	}
	row.Agent = &meInboxAgent{MyReplyRow: listed}
	row.Agent.Conversation.Message.Message = root
	row.agentReplies = replies
	return nil
}

// pointersTo is a pointer to each of values, in order.
func pointersTo[T any](values []T) []*T {
	pointers := make([]*T, len(values))
	for index := range values {
		pointers[index] = &values[index]
	}
	return pointers
}

// holdTrees keeps every stored tree one row's texts read, for the request's reader.
func (read *meInboxRead) holdTrees(types *pgtype.Map, raw []pgtype.UndecodedBytes) error {
	for _, record := range records(types, raw) {
		if err := texttrees.HoldTree(record, read.trees); err != nil {
			return err
		}
	}
	return nil
}

// meInboxPlaceholder is a placeholder of a statement spliced into the read's own.
var meInboxPlaceholder = regexp.MustCompile(`\$([0-9]+)`)

// bindFragment is fragment, a statement of another read written over $1, $2..., with each of its
// placeholders bound to the expression of this statement values names at that position: a
// placeholder of this statement, or an expression naming the entry the fragment reads for. Every
// fragment is a constant of this build, so a placeholder past values is a fault of this file, which
// the route's own tests reach on every row kind.
func bindFragment(fragment string, values ...string) string {
	return meInboxPlaceholder.ReplaceAllStringFunc(fragment, func(placeholder string) string {
		position, _ := strconv.Atoi(placeholder[1:])
		if position < 1 || position > len(values) {
			panic(fmt.Sprintf("bindFragment: %s is past the %d values bound to its fragment", placeholder, len(values)))
		}
		return values[position-1]
	})
}

// meInboxArgs is the statement's arguments, each new one bound at the next placeholder.
type meInboxArgs struct {
	values []any
}

func (args *meInboxArgs) bind(value any) string {
	args.values = append(args.values, value)
	return "$" + strconv.Itoa(len(args.values))
}

// meInboxStatement is the one statement of the read and its arguments.
//
// $1, $2 and $3 are unreadDirectRepliesArgs': the canonical login, the raw actor id and the session
// an Agent read narrows to, none here. They are bound first because the fragments of the Reply and
// Agent rows this statement splices in name them as $1 to $3 (myRepliesPageRest, myAgentUnreadRoots
// and their siblings), and the person's node is that same canonical login.
//
// The entries are read one of two ways. With no state they are the walk: the distinct relation
// kinds that touch each of the person's nodes, read by a skip over graph_tuples_inbound and
// graph_tuples_current (one index probe a kind, kinds_in and kinds_out), then each kind's current
// tuples through graphstore.CurrentTupleRead, one index range a kind, grouped by the node at the
// other end. Under a state they are the page's own rows in that state, read from the caches the
// page's lists read (meInboxPageRows), and each row's relation kinds are then read from the tuples
// between its node and the person's, the same kinds keyed to the pair, so a person whose history is
// long reads their inbox and not their history. Either way the visibility predicate is over the
// node's own row of graph_nodes, in this statement and bound once, and the counts are taken after it.
func meInboxStatement(caller model.Actor, horizon int64, request meInboxRequest, trees, readsRoster bool, known *time.Time) (string, []any, error) {
	args := &meInboxArgs{values: unreadDirectRepliesArgs(caller.ID, nil)}
	visibility := args.bind(refs.VisibilityArg(caller))
	atHorizon := `(t.source_event <= ` + args.bind(horizon) + ` or t.source_event = 0)`
	var kinds any
	if len(request.kinds) > 0 {
		kinds = request.kinds
	}
	kindsParam := args.bind(kinds)
	// A relation kind the walk reads: one the data holds, the identity tuple left out, and inside the
	// narrowing when the request names one.
	readKind := func(kind string) string {
		return kind + ` is not null and ` + kind + ` <> '` + graphstore.TupleAliasOf + `'
			and (` + kindsParam + `::text[] is null or ` + kind + ` = any(` + kindsParam + `::text[]))`
	}
	issue := args.bind(request.issue)
	var artifact any
	if request.artifact != nil {
		artifact = *request.artifact
	}
	artifactParam := args.bind(artifact)
	project := args.bind(request.project)
	// A project narrows every entry by its node's own project, and a node the store holds no row
	// for cannot be judged, so it is left out under the narrowing. An owner keeps the page's rows of
	// that owner alone (meInboxRequest.owned); under a state the entries are those rows already.
	inProject := `(` + project + `::text = '' or n.project_key = ` + project + `::text)`
	ownedRows := ``
	if request.owned() {
		ownedRows = `
		  and pr.page is not null`
	}

	var statement strings.Builder
	statement.WriteString(`
	with recursive
	-- The three parameters of unreadDirectRepliesArgs, typed once: a request narrowed to an
	-- owner splices in no Agent fragment, which is what reads the last two.
	direct_args(login, actor_id, session_id) as (select $1::text, $2::text, $3::text),
	person_nodes(kind, id) as (
		select '` + graphstore.KindPerson + `'::text, $1::text
		union
		select proved.from_kind, proved.from_id
		from (` + graphstore.CurrentTupleRead(`t.from_kind, t.from_id`, `t.from_kind, t.from_id, t.asserted`,
		`t.to_kind = '`+graphstore.KindPerson+`' and t.to_id = $1 and t.kind = '`+graphstore.TupleAliasOf+`'
				and t.to_kind <> '`+graphstore.KindValue+`' and `+atHorizon) + `) proved
		where proved.asserted and proved.from_kind = '` + graphstore.KindSession + `'
	),
	` + meInboxKindSkip("kinds_in", "to") + `,
	` + meInboxKindSkip("kinds_out", "from") + `,
	page_rows(node_kind, node_id, page, moment, agent_session, agent) as (` +
		meInboxPageRows(request, issue, artifactParam) + `
	),`)
	if request.state == "" {
		statement.WriteString(`
	walked(near_kind, near_id, kind, direction, at) as (
		select stated.from_kind, stated.from_id, k.kind, '` + meInboxInbound + `', stated.at
		from kinds_in k
		cross join lateral (` + graphstore.CurrentTupleRead(`t.from_kind, t.from_id`, `t.from_kind, t.from_id, t.at, t.asserted`,
			`t.to_kind = k.node_kind and t.to_id = k.node_id and t.kind = k.kind
				and t.to_kind <> '`+graphstore.KindValue+`' and `+atHorizon) + `) stated
		where ` + readKind("k.kind") + ` and stated.asserted
		union all
		select stated.to_kind, stated.to_id, k.kind, '` + meInboxOutbound + `', stated.at
		from kinds_out k
		cross join lateral (` + graphstore.CurrentTupleRead(`t.to_kind, t.to_id`, `t.to_kind, t.to_id, t.at, t.asserted`,
			`t.from_kind = k.node_kind and t.from_id = k.node_id and t.kind = k.kind
				and t.to_kind <> '`+graphstore.KindValue+`' and `+atHorizon) + `) stated
		where ` + readKind("k.kind") + ` and stated.asserted
	),
	candidates(node_kind, node_id, moment, kinds, directions, ats) as (
		select related.near_kind, related.near_id, max(related.at),
		       array_agg(related.kind order by related.kind, related.direction),
		       array_agg(related.direction order by related.kind, related.direction),
		       array_agg(related.at order by related.kind, related.direction)
		from (
			select w.near_kind, w.near_id, w.kind, w.direction, max(w.at) as at
			from walked w
			where (w.near_kind, w.near_id) not in (select kind, id from person_nodes)
			group by w.near_kind, w.near_id, w.kind, w.direction
		) related
		group by related.near_kind, related.near_id
	),
	entries as (
		select c.node_kind, c.node_id, c.moment, c.kinds, c.directions, c.ats,
		       pr.page, pr.agent_session, pr.agent
		from candidates c
		left join page_rows pr on pr.node_kind = c.node_kind and pr.node_id = c.node_id
		left join graph_nodes n on n.kind = c.node_kind and n.id = c.node_id
		where (n.kind is null or ` + refs.VisibleRow("n", visibility) + `)
		  and ` + inProject + ownedRows + `
	),`)
	} else {
		statement.WriteString(`
	entries as (
		select pr.node_kind, pr.node_id, pr.moment, related.kinds, related.directions, related.ats,
		       pr.page, pr.agent_session, pr.agent
		from page_rows pr
		cross join lateral (
			select coalesce(array_agg(tied.kind order by tied.kind, tied.direction), '{}') as kinds,
			       coalesce(array_agg(tied.direction order by tied.kind, tied.direction), '{}') as directions,
			       coalesce(array_agg(tied.at order by tied.kind, tied.direction), '{}') as ats
			from (
				select k.kind, '` + meInboxInbound + `' as direction, max(stated.at) as at
				from kinds_in k
				cross join lateral (` + graphstore.CurrentTupleRead(`t.from_kind, t.from_id`, `t.at, t.asserted`,
			`t.to_kind = k.node_kind and t.to_id = k.node_id and t.kind = k.kind
						and t.from_kind = pr.node_kind and t.from_id = pr.node_id
						and t.to_kind <> '`+graphstore.KindValue+`' and `+atHorizon) + `) stated
				where ` + readKind("k.kind") + ` and stated.asserted
				group by k.kind
				union all
				select k.kind, '` + meInboxOutbound + `', max(stated.at)
				from kinds_out k
				cross join lateral (` + graphstore.CurrentTupleRead(`t.to_kind, t.to_id`, `t.at, t.asserted`,
			`t.from_kind = k.node_kind and t.from_id = k.node_id and t.kind = k.kind
						and t.to_kind = pr.node_kind and t.to_id = pr.node_id
						and t.to_kind <> '`+graphstore.KindValue+`' and `+atHorizon) + `) stated
				where ` + readKind("k.kind") + ` and stated.asserted
				group by k.kind
			) tied
		) related
		left join graph_nodes n on n.kind = pr.node_kind and n.id = pr.node_id
		where (n.kind is null or ` + refs.VisibleRow("n", visibility) + `)
		  and ` + inProject + `
		  and (` + kindsParam + `::text[] is null or cardinality(related.kinds) > 0)
	),`)
	}
	var moment, kind, id any
	if request.cursor != nil {
		moment, kind, id = request.cursor.moment, request.cursor.kind, request.cursor.id
	}
	cursorMoment, cursorKind, cursorID := args.bind(moment), args.bind(kind), args.bind(id)
	var limit any
	if request.limit != nil {
		limit = *request.limit
	}
	statement.WriteString(`
	counted as (
		select e.*, count(*) over () as total from entries e
	),
	remaining as (
		select c.*, count(*) over () as remaining from counted c
		where ` + cursorMoment + `::timestamptz is null
		   or (c.moment, c.node_kind, c.node_id) < (` + cursorMoment + `::timestamptz, ` + cursorKind + `::text, ` + cursorID + `::text)
	),
	paged as (
		select * from remaining
		order by moment desc, node_kind desc, node_id desc
		offset ` + args.bind(request.offset) + ` limit ` + args.bind(limit) + `
	)`)
	roster := `null::record`
	if readsRoster {
		roster = `(select row(stored.*) from (` + bindFragment(routeRosterQuery, args.bind(known)) + `) stored)`
	}
	askRow, err := meInboxAskRow(args, caller, visibility, trees)
	if err != nil {
		return "", nil, err
	}
	agentRow, err := meInboxAgentRow(args, visibility, trees)
	if err != nil {
		return "", nil, err
	}
	statement.WriteString(`
	select head.as_of, head.roster, head.total, head.remaining,
	       entry.node_kind, entry.node_id, entry.moment, entry.kinds, entry.directions, entry.ats,
	       entry.page, entry.agent,
	       ask_row.ask, ask_row.opened, ask_row.counted, ask_row.parts, ask_row.reach,
	       ask_row.replies, ask_row.reply_named, ask_row.reply_attempts, ask_row.ask_blocks, ask_row.comment_blocks,
	       ask_row.edits, ask_row.followers, ask_row.trees,
	       thread_row.thread,
	       agent_row.root, agent_row.messages, agent_row.attempts, agent_row.blocks, agent_row.title, agent_row.trees
	from (
		select now() as as_of, ` + roster + ` as roster,
		       coalesce((select c.total from counted c limit 1), 0) as total,
		       coalesce((select r.remaining from remaining r limit 1), 0) as remaining
	) head
	left join paged entry on true` + askRow + meInboxThreadRow + agentRow + `
	order by entry.moment desc, entry.node_kind desc, entry.node_id desc`)
	return statement.String(), args.values, nil
}

// meInboxKindSkip lists the distinct relation kinds that touch each of the person's nodes at one end
// - to, the tuples into it, read through graph_tuples_inbound, or from, those out of it, through
// graph_tuples_current - by a recursive skip: each step reads the least kind past the one before off
// the index, so a kind costs one probe however many tuples it holds, and no kind is named in Go.
// graphstore.TuplesIntoNode says why a statement naming no kind would otherwise read the log.
func meInboxKindSkip(name, end string) string {
	at := func(kind, id string) string {
		return `t.` + end + `_kind = ` + kind + ` and t.` + end + `_id = ` + id + ` and t.to_kind <> '` + graphstore.KindValue + `'`
	}
	return name + `(node_kind, node_id, kind) as (
		select n.kind, n.id, (select min(t.kind) from graph_tuples t where ` + at("n.kind", "n.id") + `)
		from person_nodes n
		union all
		select k.node_kind, k.node_id, (select min(t.kind) from graph_tuples t
			where ` + at("k.node_kind", "k.node_id") + ` and t.kind > k.kind)
		from ` + name + ` k
		where k.kind is not null
	)`
}

// meInboxPageRows is the one fragment that selects the rows the page draws, by today's rules (the
// rows of GET /api/v1/inbox?assignee=me and GET /api/v1/me/replies, P3): an Ask row is an open ask on
// an open issue that waits on the person (askedOfPersonExpression), which only the Inbox's own state
// lists; a Reply row is a user_threads row of theirs in the state the request names, less an open
// ask the Inbox lists already (myRepliesOwner); an Agent row is one of their one-off messages to a
// session holding a reply from it (myAgentUnreadRoots and its siblings), on no issue or document,
// so a request narrowed to an owner lists none. A mention or an addressing reaches the person's
// node and writes no user_threads row, so it is no row of the page. A project narrows the entries
// these rows are, by their nodes' project, where every other entry is narrowed (meInboxStatement).
//
// Each row is the node it draws, the kind of row, the moment that state's list orders it by, and an
// Agent row's session and the row itself (myAgentRowColumns), read once here for its order and kept.
// A walk with no state draws the rows of the Inbox's own state.
func meInboxPageRows(request meInboxRequest, issue, artifact string) string {
	state := request.state
	if state == "" {
		state = myRepliesStateInbox
	}
	threads, threadMoment := myRepliesInbox, `u.replied_at`
	agents, agentMoment := myAgentInboxRoots, `listed.replied_at`
	switch state {
	case myRepliesStateUnread:
		threads, agents = myRepliesUnread, myAgentUnreadRoots
	case myRepliesStateDismissed:
		threads, threadMoment = myRepliesDismissed, `u.dismissed_at`
		agents, agentMoment = myAgentDismissedRoots, `listed.dismissed_at`
	}
	arms := []string{`
		select u.kind, u.thread_id::text, '` + meInboxPageThread + `', ` + threadMoment + `, null::text, null::record
		` + bindFragment(threads, "$1", issue, artifact)}
	if state == myRepliesStateInbox {
		arms = append(arms, `
		select '`+graphstore.KindAsk+`', a.id::text, '`+meInboxPageAsk+`', coalesce(lr.created_at, a.created_at), null::text, null::record
		from asks a`+askedOfPersonJoins+lastReplyJoin+`
		where a.state = 'open' and (i.key is null or i.closed_at is null)
		  and `+askedOfPersonExpression+` = $1
		  and (`+issue+`::text = '' or a.issue_key = `+issue+`::text)
		  and (`+artifact+`::uuid is null or a.artifact_id = `+artifact+`::uuid)`)
	}
	if !request.owned() {
		arms = append(arms, `
		select '`+graphstore.KindMessage+`', listed.root_id, '`+meInboxPageAgent+`', `+agentMoment+`, listed.session_id, row(listed.*)
		from (`+agents+myAgentRowsSelect+`
		) listed`)
	}
	return strings.Join(arms, `
		union all`)
}

// meInboxEntryOf is the id of the entry's node where its row is of page kind, and null for every
// other row, so a fragment's cast of it never reads a node that is no row of that kind.
func meInboxEntryOf(page string) string {
	return `(case when entry.page = '` + page + `' then entry.node_id end)`
}

// meInboxThreadRow is a Reply row's lateral: its user_threads row read by its key, then what
// listMyReplies selects over the rows its cut kept (myRepliesPageRest), as one record.
var meInboxThreadRow = `
	left join lateral (
		select row(listed.*) as thread
		from (
			with recursive page as (
				select kept.thread_id, kept.kind, kept.replied_at, kept.dismissed_at, kept.last_reply_id,
				       kept.issue_key, kept.artifact_id, kept.read_through, kept.project_key, kept.unread, kept.version
				from user_threads kept
				where kept.login = $1 and kept.thread_id = ` + meInboxEntryOf(meInboxPageThread) + `::uuid
			),` + myRepliesPageRest + `
		) listed
		where entry.page = '` + meInboxPageThread + `'
	) thread_row on true`

// meInboxAskRow is an Ask row's lateral: the row as listInbox selects it (askReadColumns over
// askReadFrom, with its issue, document and the caller's snooze), the details loadAskDetails reads
// (events.OpenedEventIDsStatement, refs.BacklinkCountRead, refs.AskPartRowsStatement), and its thread
// as loadAskThreads reads it (inboxAskReplyChainsQuery, then the targets each reply names, its
// attempts and its block, the rewordings, the followers and askReachesQuery), each over this one ask.
func meInboxAskRow(args *meInboxArgs, caller model.Actor, visibility string, trees bool) (string, error) {
	count, rest, err := refs.BacklinkCountRead(caller, graphstore.KindAsk, refs.AskBacklinkExclusions)
	if err != nil {
		return "", err
	}
	// The ask's id as the one-element array every statement below is written over. It is cast, so
	// that a fragment's `= any($1)` reads it as an array and not as a subquery of one row.
	askIDs := `(select ids from ask_key)::text[]`
	// The count's second argument after the node kinds is the acting credential, which this
	// statement has bound once already (visibility), and the rest are its own.
	countArgs := []string{askIDs, args.bind(rest[0]), visibility}
	for _, value := range rest[2:] {
		countArgs = append(countArgs, args.bind(value))
	}
	askBlocks, err := refs.ReplyBlocksStatement(refs.ItemAsk)
	if err != nil {
		return "", err
	}
	commentBlocks, err := refs.ReplyBlocksStatement(refs.ItemComment)
	if err != nil {
		return "", err
	}
	askTrees := `null::record[]`
	if trees {
		askTrees = meInboxTrees(args, `
			select a.question from asks a where a.id = (select id from ask_key)::uuid
			union all
			select choice.value->>field.name
			from asks a
			cross join lateral jsonb_array_elements(case when jsonb_typeof(a.options) = 'array' then a.options else '[]'::jsonb end) choice
			cross join (values ('label'), ('description')) field(name)
			where a.id = (select id from ask_key)::uuid
			union all
			select a.answer->>'text' from asks a where a.id = (select id from ask_key)::uuid
			union all
			select a.resolution->>'reason' from asks a where a.id = (select id from ask_key)::uuid
			union all
			select r.body from replies r
			union all
			select b.excerpt from ask_named b
			union all
			select b.excerpt from comment_named b
			union all
			select e.previous->>'question' from edits e
			union all
			select choice.value->>field.name
			from edits e
			cross join lateral jsonb_array_elements(case when jsonb_typeof(e.previous->'options') = 'array' then e.previous->'options' else '[]'::jsonb end) choice
			cross join (values ('label'), ('description')) field(name)`)
	}
	replyIDs := `(select ids from reply_ids)`
	return `
	left join lateral (
		with ask_key(id, ids) as materialized (
			select ` + meInboxEntryOf(meInboxPageAsk) + `, array[` + meInboxEntryOf(meInboxPageAsk) + `]
		),
		replies as materialized (
			select * from (` + bindFragment(inboxAskReplyChainsQuery, askIDs) + `) chain
		),
		reply_ids(ids) as materialized (
			select coalesce(array_agg(r.id::uuid), '{}') from replies r
		),
		ask_named as materialized (
			select * from (` + bindFragment(askBlocks, replyIDs, visibility) + `) named
		),
		comment_named as materialized (
			select * from (` + bindFragment(commentBlocks, replyIDs, visibility) + `) named
		),
		edits(ask_id, previous, edited_by, at) as materialized (
			select * from (` + bindFragment(inboxAskEditsQuery, askIDs) + `) edit
		)
		select
			(select row(listed.*) from (
				select ` + askReadColumns + `,
				       i.key, i.title, i.assignee, i.priority, ar.project_key, ar.slug, ar.name,
				       sn.snoozed_until
				` + askReadFrom + `
				left join user_ask_snooze sn on sn.ask_id = a.id and sn.login = $1
				where a.id = (select id from ask_key)::uuid
			) listed) as ask,
			(select row(opened.*) from (` + bindFragment(events.OpenedEventIDsStatement, askIDs) + `) opened) as opened,
			(select row(counted.*) from (` + bindFragment(count, countArgs...) + `) counted) as counted,
			(select row(parts.*) from (` + bindFragment(refs.AskPartRowsStatement, askIDs) + `) parts) as parts,
			(select row(reach.*) from (` + bindFragment(askReachesQuery, askIDs) + `) reach) as reach,
			array(select row(r.*) from replies r) as replies,
			array(select row(named.*) from (` + bindFragment(commentMentionsQuery, replyIDs) + `) named) as reply_named,
			array(select row(attempt.*) from (` + bindFragment(commentDeliveriesQuery, replyIDs) + `) attempt) as reply_attempts,
			array(select row(b.*) from ask_named b) as ask_blocks,
			array(select row(b.*) from comment_named b) as comment_blocks,
			array(select row(e.*) from edits e) as edits,
			array(select row(follower.*) from (` + bindFragment(inboxAskFollowersQuery, askIDs) + `) follower) as followers,
			` + askTrees + ` as trees
		where entry.page = '` + meInboxPageAsk + `'
	) ask_row on true`, nil
}

// meInboxAgentRow is an Agent row's lateral: the conversation as hydrateMessageThreads reads it - the
// root message, every reply under it (messageReplyChainsQuery), each message's attempts
// (messageDeliveriesQuery) and the block it names (refs.ReplyBlocksStatement) - and the session's
// name (delivery.ResolveSessionTitlesQuery). The row itself is page_rows', read for its order.
func meInboxAgentRow(args *meInboxArgs, visibility string, trees bool) (string, error) {
	blocks, err := refs.ReplyBlocksStatement(refs.ItemMessage)
	if err != nil {
		return "", err
	}
	agentTrees := `null::record[]`
	if trees {
		agentTrees = meInboxTrees(args, `
			select r.body from root r
			union all
			select c.body from chain c
			union all
			select b.excerpt from named b`)
	}
	messageIDs := `(select ids from message_ids)`
	return `
	left join lateral (
		with root as materialized (
			select ` + messageColumns + ` from messages where id = ` + meInboxEntryOf(meInboxPageAgent) + `::uuid
		),
		chain as materialized (
			select * from (` + bindFragment(messageReplyChainsQuery, `array(select id::uuid from root)`) + `) chain
		),
		message_ids(ids) as materialized (
			select array(select id::uuid from root union all select id::uuid from chain)
		),
		named as materialized (
			select * from (` + bindFragment(blocks, messageIDs, visibility) + `) named
		)
		select
			(select row(r.*) from root r) as root,
			array(select row(c.*) from chain c) as messages,
			array(select row(attempt.*) from (` + bindFragment(messageDeliveriesQuery, messageIDs) + `) attempt) as attempts,
			array(select row(b.*) from named b) as blocks,
			(select titled.session_title from (` + bindFragment(delivery.ResolveSessionTitlesQuery, `array[entry.agent_session]`) + `) titled) as title,
			` + agentTrees + ` as trees
		where entry.page = '` + meInboxPageAgent + `'
	) agent_row on true`, nil
}

// meInboxTrees is the stored display tree of each of texts that has one (texttrees.ReadStatement),
// keyed as every reader keys them, by the sum of the text (pmdoc.TextSum). texts is every text the
// row may register with the request's reader; a text it leaves out is read for display instead, so it
// errs on the side of naming more.
func meInboxTrees(args *meInboxArgs, texts string) string {
	return `array(select row(tree.*) from (` + bindFragment(texttrees.ReadStatement,
		args.bind(pmdoc.DisplayTreeVersionNow()), args.bind(int16(pmdoc.DisplayLatest)),
		`array(select sha256(convert_to(written.text, 'UTF8')) from (`+texts+`
			) written(text) where written.text <> '')`) + `) tree)`
}

// errMeInboxNoRow is a statement that answered no row at all, which it never does: its head row is
// there however few entries it finds.
var errMeInboxNoRow = errors.New("the inbox statement answered no row")
