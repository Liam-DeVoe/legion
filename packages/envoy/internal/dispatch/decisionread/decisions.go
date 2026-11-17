// Package decisionread answers a decision node of the store in model.Decision's shape
// (dispatch://INFRA-1150 R1, R2, R4 and R5, ruling H1). A decision is one node: an agent's is a
// `decision` block node, and a person's is the answered ask's own `ask` node. This package is the
// one reader of what every read of a decision answers about it - its words, their text sum, the
// spec nodes it shaped with each link's `stale`, its review state, and the version it last changed
// in - so the reads that answer a decision answer the same values:
//
//   - the Decisions read's `entries` (api's getIssueDecisions), which takes each member below from
//     here and adds what only an entry carries;
//   - the Keep (api's keepDecision), which compares the posted `text_sha256` with TextSum of the
//     words this package reads, so a Keep holds the sum the read served;
//   - the Decisions read's old `decisions` member once it answers from the store;
//   - dispatch://INFRA-1253's tree read, which calls Decisions for each decision node it draws.
//
// It is a package of its own because api's readers are unexported and neither docs nor refs may
// import api. It imports graphstore, refs and model, and neither api nor docs.
package decisionread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// Node names one node Decisions reads: Kind and ID as graph_nodes keys it. A `decision` block
// node is {refs.BlockNodeKind, "<ref_key>#<block id>"}, and an ask's node is
// {graphstore.KindAsk, "<ask id>"}.
type Node struct{ Kind, ID string }

// TextSum is a decision's text sum: the sha256, in hex, of its words. A Keep records it as the
// node's kept_text_sum and posts it back as `text_sha256`, and an agent's decision waits for review
// until a Keep's sum is its sum as it stands. It is the one sum of a decision's text: this
// package's reads, the Keep and dispatch://INFRA-1253's tree read all call it.
func TextSum(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Decisions answers each of nodes the store holds as a decision, in model.Decision's shape and in
// the order of nodes, reading for the person or session who: every statement binds the
// visibility predicate of the registry (refs.VisibleRow, refs.VisibleKind), so a node, or a spec
// node a decision shaped, that who may not see is absent. Each answer names its node in NodeKind
// and NodeID, which no wire shape carries, so a caller that asked for several finds each one.
//
// A node is read as a decision when it is a live block node of the `decision` supertag, or an ask
// node with an `asks` row. A caller lists as decisions the asks a person answered; an ask in any
// other answer state reads the same way, and the Decisions read takes the links of an open and a
// declined ask from here too. Any other node, and a node the store does not hold, is left out.
//
// Each decision answers:
//
//   - Text, its words, and TextSHA256, their sum (TextSum). An agent's words are the decision's
//     own (refs.DecisionWords), read off the block's text as the block index recorded it last
//     (document_block_texts), so a reading it closes with is none of them. A decision whose
//     document still owes its block index holds no such row: its words are read off the
//     version that wrote its node, the version event its node's source_event names. A person's
//     words are the ask's question, from its row: the answer is the ask node's, and no other
//     field restates it.
//   - Kind, `person` for an ask and `agent` for a decision; Heading, the ask's question or the
//     decision's first paragraph; Ref, the node's own address; Asks, the ask an agent's decision
//     settles (from_ask) or the ask itself; Unsure, true where its uncertainty is `high`.
//   - Replaced, Kept and WaitingReview from the node: the newest kept_by tuple and the
//     kept_text_sum value (dispatch://INFRA-1150 R2), beside decision_keeps' changed_version,
//     and waiting_review true for an agent's decision unless it is replaced or the kept sum is its
//     sum as it stands.
//   - ChangedVersion, the decision node's own document_blocks.changed_version (an ask's, its
//     block's row), and ChangedAt, that version's created_at; a decision whose document still
//     owes its block index answers the version that wrote its node.
//   - Links, one for each spec node a current `shaped` tuple reaches, FromBlock the node's own
//     block (the decision's, or the ask's). Event is the tuple's
//     source_event and CreatedAt its at; TextChangedEvent is the event that wrote the version the
//     target's text last changed in (its document_blocks.changed_version) and TextChangedAt that
//     version's created_at. Stale is true when the target's text changed after the tuple was
//     written: by the two events where both are held, else by the database's clock (R5). None of
//     the three computed members is stored.
//
// A node whose stored value does not parse - an uncertainty or a replaced outside its type, a
// version number past a 32-bit integer, words that do not read back - is left out and logged,
// and the read goes on with the others. TextTree is left for the caller's own reader.
func Decisions(ctx context.Context, q graphstore.Queryer, who model.Actor, nodes []Node) ([]model.Decision, error) {
	decisions := []model.Decision{}
	if len(nodes) == 0 {
		return decisions, nil
	}
	kinds, ids := make([]string, len(nodes)), make([]string, len(nodes))
	for index, node := range nodes {
		kinds[index], ids[index] = node.Kind, node.ID
	}
	held, err := readNodes(ctx, q, who, kinds, ids)
	if err != nil {
		return nil, err
	}
	facts, err := readFacts(ctx, q, kinds, ids)
	if err != nil {
		return nil, err
	}
	links, err := readLinks(ctx, q, who, kinds, ids)
	if err != nil {
		return nil, err
	}
	for index := range nodes {
		node, found := held[index]
		if !found {
			continue
		}
		decision, err := node.decision(facts[index], links[index])
		if err != nil {
			slog.Warn("dispatch: a decision whose stored value does not parse is left out of the read",
				"kind", node.kind, "node", node.id, "err", err)
			continue
		}
		decisions = append(decisions, decision)
	}
	return decisions, nil
}

// heldNode is what the store holds of one node Decisions reads: the node row, the document that
// holds its block, an ask's row, its block's row and the texts the block index recorded of it,
// its Keep's version, and, for a block no document_blocks row holds, the version the node's
// source_event names.
type heldNode struct {
	kind, id, supertag, state string

	document                               bool
	artifactID, refKey, slug, artifactKind string
	issueKey, project, session             *string
	primary                                bool
	question, askIssue                     *string
	blockID                                string
	rowVersion                             *int
	texts                                  []storedText
	keptVersion                            *int
	writtenNumber                          *string
	writtenVersion                         *int
	writtenAt                              *time.Time
	writtenMarkdown                        *string
}

// nodesRead reads each named node, with the document holding its block and that block's rows,
// in one statement, for the person or session bound at $3. A block node's document is the one
// whose ref_key opens its address
// (a ref_key holds no '#'), and an ask's the document holding its block (asks.block_artifact_id).
// The block's texts are every text the block index recorded of it up to its row's changed version
// (document_block_texts), newest first, each beside its version's created_at. The fallback
// version is read only for a block node no document_blocks row holds, off the event its
// source_event names: the event's own version number for an artifact.version, and 1 for the
// artifact.created or issue.created that wrote a document's first version. That number is
// answered as the event's text, so one past a 32-bit integer is the read's to refuse by node
// rather than the statement's to fail on.
func nodesRead() string {
	return `
	select w.n, g.kind, g.id, g.supertag, g.state,
	       doc.id is not null, coalesce(doc.id::text, ''), coalesce(doc.ref_key, ''), coalesce(doc.slug, ''),
	       coalesce(doc.kind, ''), doc.issue_key, doc.project_key, doc.session_id, coalesce(doc.is_primary, false),
	       q.question, q.issue_key,
	       coalesce(row.block_id, case when g.kind = '` + graphstore.KindAsk + `' then q.block_id
	                                   when doc.id is not null then substr(g.id, length(doc.ref_key) + 2) end, ''),
	       row.changed_version,
	       (select json_agg(json_build_object('version', x.version, 'text', x.text, 'at', v.created_at) order by x.version desc)
	        from document_block_texts x
	        left join artifact_versions v on v.artifact_id = x.artifact_id and v.number = x.version
	        where x.artifact_id = row.artifact_id and x.block_id = row.block_id and x.version <= row.changed_version),
	       (select k.changed_version from decision_keeps k
	        where g.kind = '` + refs.BlockNodeKind + `' and k.artifact_id = doc.id
	          and k.block_id = coalesce(row.block_id, substr(g.id, length(doc.ref_key) + 2))),
	       written_number.number, written.number, written.created_at, written.markdown
	from unnest($1::text[], $2::text[]) with ordinality as w (kind, id, n)
	join graph_nodes g on g.kind = w.kind and g.id = w.id and ` + refs.VisibleRow("g", "$3") + `
	left join asks q on g.kind = '` + graphstore.KindAsk + `'
		and q.id = case when g.id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then g.id::uuid end
	left join artifacts doc on doc.id = case when g.kind = '` + graphstore.KindAsk + `' then q.block_artifact_id end
		or doc.ref_key = case when g.kind = '` + refs.BlockNodeKind + `' then split_part(g.id, '#', 1) end
	left join lateral (
		select b.artifact_id, b.block_id, b.changed_version from document_blocks b
		where g.kind = '` + refs.BlockNodeKind + `' and b.node_kind = '` + refs.BlockNodeKind + `' and b.node_address = g.id
		union all
		select b.artifact_id, b.block_id, b.changed_version from document_blocks b
		where g.kind = '` + graphstore.KindAsk + `' and b.artifact_id = q.block_artifact_id and b.block_id = q.block_id
	) row on true
	left join lateral (
		select case when e.type = 'artifact.version' then e.payload->'version'->>'number' else '1' end as number
		from events e
		where row.block_id is null and g.kind = '` + refs.BlockNodeKind + `' and doc.id is not null and e.id = g.source_event
		  and (e.type = 'artifact.version' and e.payload->>'artifact_id' = doc.id::text
		       or e.type = 'artifact.created' and e.payload->'artifact'->>'id' = doc.id::text
		       or e.type = 'issue.created' and e.issue_key = doc.issue_key and doc.is_primary)
	) written_number on true
	left join artifact_versions written on written.artifact_id = doc.id and written.number::text = written_number.number
	order by w.n`
}

// storedText is one text the block index recorded of a block: the version it stands from, the
// block's text (nil where that version deleted the block), and when that version was written.
type storedText struct {
	Version int        `json:"version"`
	Text    *string    `json:"text"`
	At      *time.Time `json:"at"`
}

// readNodes reads each node of the two arrays (nodesRead), keyed by its index among them, for who.
func readNodes(ctx context.Context, q graphstore.Queryer, who model.Actor, kinds, ids []string) (map[int]heldNode, error) {
	rows, err := q.Query(ctx, nodesRead(), kinds, ids, refs.VisibilityArg(who))
	if err != nil {
		return nil, fmt.Errorf("read the decision nodes: %w", err)
	}
	defer rows.Close()
	held := map[int]heldNode{}
	for rows.Next() {
		var n int64
		var node heldNode
		var texts []byte
		if err := rows.Scan(&n, &node.kind, &node.id, &node.supertag, &node.state,
			&node.document, &node.artifactID, &node.refKey, &node.slug, &node.artifactKind,
			&node.issueKey, &node.project, &node.session, &node.primary,
			&node.question, &node.askIssue, &node.blockID, &node.rowVersion, &texts,
			&node.keptVersion, &node.writtenNumber, &node.writtenVersion, &node.writtenAt, &node.writtenMarkdown); err != nil {
			return nil, fmt.Errorf("read a decision node: %w", err)
		}
		if texts != nil {
			if err := json.Unmarshal(texts, &node.texts); err != nil {
				return nil, fmt.Errorf("read the recorded texts of %s %s: %w", node.kind, node.id, err)
			}
		}
		held[int(n)-1] = node
	}
	return held, rows.Err()
}

// fact is one relation or value a node holds now: its kind, its far end's kind and id (the value
// itself, for a value), and when its tuple was written.
type fact struct {
	kind, toKind, toID string
	at                 time.Time
	seq                int64
}

// factKinds are the relations and values of a decision node the read takes beside its links.
var factKinds = []string{refs.TupleFromAsk, refs.TupleKeptBy, refs.ValueKeptTextSum, refs.ValueReplaced, refs.ValueUncertainty}

// readFacts reads, for each node, the relations and values of factKinds it holds now, in one
// statement: the newest tuple of each relation (kind and far end) and of each value (kind alone),
// answered while it asserts (graphstore.CurrentTupleRead's rule, CurrentOut's and CurrentValue's).
func readFacts(ctx context.Context, q graphstore.Queryer, kinds, ids []string) (map[int][]fact, error) {
	rows, err := q.Query(ctx, `
		select w.n, f.kind, f.to_kind, f.to_id, f.at, f.seq
		from unnest($1::text[], $2::text[]) with ordinality as w (kind, id, n)
		cross join lateral (`+graphstore.CurrentTupleRead(
		`t.kind, t.to_kind, case when t.to_kind = '`+graphstore.KindValue+`' then '' else t.to_id end`,
		`t.kind, t.to_kind, t.to_id, t.at, t.seq, t.asserted`,
		`t.from_kind = w.kind and t.from_id = w.id and t.kind = any($3::text[])`)+`
		) f
		where f.asserted
		order by w.n, f.kind, f.seq
	`, kinds, ids, factKinds)
	if err != nil {
		return nil, fmt.Errorf("read the decision nodes' values: %w", err)
	}
	defer rows.Close()
	facts := map[int][]fact{}
	for rows.Next() {
		var n int64
		var held fact
		if err := rows.Scan(&n, &held.kind, &held.toKind, &held.toID, &held.at, &held.seq); err != nil {
			return nil, fmt.Errorf("read a decision node's value: %w", err)
		}
		facts[int(n)-1] = append(facts[int(n)-1], held)
	}
	return facts, rows.Err()
}

// readLinks reads, for each node, every spec node a current `shaped` tuple of it reaches that who
// may see (refs.VisibleKind), in one statement, with the two times R5 compares: the tuple's own
// event and time, and the event and time that wrote the version the target's text last changed
// in. The event of a version is docs' versionEventID's: its artifact.version, or for version 1 the
// document's artifact.created or the issue.created that seeded the issue's spec. A target whose
// document holds no row for it - one that still owes its block index - has no such version.
func readLinks(ctx context.Context, q graphstore.Queryer, who model.Actor, kinds, ids []string) (map[int][]model.DecisionLink, error) {
	rows, err := q.Query(ctx, `
		select w.n, s.to_id, s.at, s.source_event,
		       coalesce(target.block_id, ''), coalesce(target.excerpt, ''), target.changed_version, target_version.created_at,
		       (select e.id from events e
		        where (e.issue_key = doc.issue_key or e.artifact_id = doc.id)
		          and (e.type = 'artifact.version' and e.payload->>'artifact_id' = doc.id::text
		               and e.payload->'version'->>'number' = target.changed_version::text
		               or target.changed_version = 1 and e.type = 'artifact.created' and e.payload->'artifact'->>'id' = doc.id::text
		               or target.changed_version = 1 and e.type = 'issue.created' and e.issue_key = doc.issue_key and doc.is_primary)
		        order by e.id desc limit 1),
		       doc.id is not null, coalesce(doc.ref_key, ''), coalesce(doc.slug, ''), coalesce(doc.kind, ''),
		       doc.issue_key, doc.project_key, doc.session_id, coalesce(doc.is_primary, false),
		       coalesce((select r.created_version from block_refs r
		                 where r.node_kind = w.kind and r.node_address = w.id and r.kind = '`+refs.TupleShaped+`'
		                   and r.to_kind = '`+refs.BlockNodeKind+`' and r.to_id = s.to_id
		                 order by r.created_version limit 1), 0)
		from unnest($1::text[], $2::text[]) with ordinality as w (kind, id, n)
		cross join lateral (`+graphstore.CurrentTupleRead(`t.to_kind, t.to_id`,
		`t.to_kind, t.to_id, t.at, t.source_event, t.asserted`,
		`t.from_kind = w.kind and t.from_id = w.id and t.kind = '`+refs.TupleShaped+`' and t.to_kind <> '`+graphstore.KindValue+`'`)+`
		) s
		left join artifacts doc on doc.ref_key = split_part(s.to_id, '#', 1)
		left join document_blocks target on target.node_kind = '`+refs.BlockNodeKind+`' and target.node_address = s.to_id
		left join artifact_versions target_version on target_version.artifact_id = target.artifact_id
			and target_version.number = target.changed_version
		where s.asserted and s.to_kind = '`+refs.BlockNodeKind+`' and `+refs.VisibleKind(refs.BlockNodeKind, "s.to_id", "$3")+`
		order by w.n, s.to_id
	`, kinds, ids, refs.VisibilityArg(who))
	if err != nil {
		return nil, fmt.Errorf("read the decision nodes' shaped links: %w", err)
	}
	defer rows.Close()
	links := map[int][]model.DecisionLink{}
	for rows.Next() {
		var n int64
		var address string
		var event int64
		var link model.DecisionLink
		var changedVersion *int
		var changedEvent *int64
		var document bool
		var refKey, slug, artifactKind string
		var issueKey, project, session *string
		var primary bool
		if err := rows.Scan(&n, &address, &link.CreatedAt, &event,
			&link.To.BlockID, &link.To.Text, &changedVersion, &link.TextChangedAt, &changedEvent,
			&document, &refKey, &slug, &artifactKind, &issueKey, &project, &session, &primary,
			&link.CreatedVersion); err != nil {
			return nil, fmt.Errorf("read a decision node's shaped link: %w", err)
		}
		link.To.Kind, link.To.ID = refs.BlockNodeKind, address
		if link.To.BlockID == "" && document {
			link.To.BlockID = strings.TrimPrefix(address, refKey+"#")
		}
		if document {
			link.To.Document = refs.ArtifactRef(issueKey, stringOf(project), session, slug, artifactKind, primary)
			link.To.Ref = refs.BlockRef(link.To.Document, link.To.BlockID)
		}
		if changedVersion != nil {
			link.To.ChangedVersion = *changedVersion
		}
		if event != graphstore.NoEvent {
			link.Event = &event
		}
		if changedEvent != nil && *changedEvent != graphstore.NoEvent {
			link.TextChangedEvent = changedEvent
		}
		link.Stale = textChangedAfterTie(link)
		links[int(n)-1] = append(links[int(n)-1], link)
	}
	return links, rows.Err()
}

// textChangedAfterTie is R5's rule for one link (H1): the target's text changed after the
// decision tied it. The two times are the store's: the events that wrote each where both are
// held, else the database's clock - the version's created_at against the tuple's at. A target
// with no recorded change, one whose document still owes its block index, has not changed.
func textChangedAfterTie(link model.DecisionLink) bool {
	if link.TextChangedAt == nil {
		return false
	}
	if link.Event != nil && link.TextChangedEvent != nil {
		return *link.TextChangedEvent > *link.Event
	}
	return link.TextChangedAt.After(link.CreatedAt)
}

// decision is node in model.Decision's shape, with the values it holds (facts) and its links, or
// the error that says which stored value of it does not parse.
func (node heldNode) decision(facts []fact, links []model.DecisionLink) (model.Decision, error) {
	decision := model.Decision{
		NodeKind: node.kind, NodeID: node.id,
		BlockID: node.blockID, Asks: []string{}, Links: links,
	}
	if decision.Links == nil {
		decision.Links = []model.DecisionLink{}
	}
	// A link's FromBlock is the block of the decision that holds it, as a section's link named
	// the section's block it was written in: here the decision's own block, or the ask's.
	for index := range decision.Links {
		decision.Links[index].FromBlock = node.blockID
	}
	documentRef := ""
	if node.document {
		documentRef = refs.ArtifactRef(node.issueKey, stringOf(node.project), node.session, node.slug, node.artifactKind, node.primary)
	}
	switch {
	case node.kind == graphstore.KindAsk && node.question != nil:
		decision.Kind = "person"
		decision.Text, decision.Heading = *node.question, *node.question
		decision.Asks = []string{node.id}
		switch {
		case node.askIssue != nil:
			decision.Ref = "dispatch://" + *node.askIssue + "/ask/" + node.id
		case documentRef != "":
			decision.Ref = documentRef + "/ask/" + node.id
		}
		if err := node.readVersion(&decision, askType); err != nil {
			return model.Decision{}, err
		}
	case node.kind == refs.BlockNodeKind && node.supertag == refs.DecisionSupertag && node.state == graphstore.StateLive:
		decision.Kind = "agent"
		decision.Ref = refs.BlockRef(documentRef, node.blockID)
		if err := node.readWords(&decision); err != nil {
			return model.Decision{}, err
		}
		if err := node.readVersion(&decision, refs.DecisionSupertag); err != nil {
			return model.Decision{}, err
		}
	default:
		return model.Decision{}, fmt.Errorf("the node is no decision: kind %s, supertag %s, state %s", node.kind, node.supertag, node.state)
	}
	decision.TextSHA256 = TextSum(decision.Text)
	var kept *fact
	keptSum, keptSumHeld := "", false
	for index, held := range facts {
		switch held.kind {
		case refs.TupleFromAsk:
			if held.toKind == graphstore.KindAsk {
				decision.Asks = append(decision.Asks, held.toID)
			}
		case refs.TupleKeptBy:
			if kept == nil || held.seq > kept.seq {
				kept = &facts[index]
			}
		case refs.ValueKeptTextSum:
			keptSum, keptSumHeld = held.toID, true
		case refs.ValueReplaced:
			switch held.toID {
			case "true":
				decision.Replaced = true
			case "false":
			default:
				return model.Decision{}, fmt.Errorf("replaced holds %q, which is no bool", held.toID)
			}
		case refs.ValueUncertainty:
			switch held.toID {
			case "low", "medium":
			case "high":
				decision.Unsure = true
			default:
				return model.Decision{}, fmt.Errorf("uncertainty holds %q, which is none of low, medium and high", held.toID)
			}
		}
	}
	if decision.Kind == "agent" && kept != nil && keptSumHeld {
		decision.Kept = &model.DecisionKeep{
			ArtifactID: node.artifactID, BlockID: node.blockID, KeptBy: kept.toID, KeptAt: kept.at, TextSHA256: keptSum,
		}
		if node.keptVersion != nil {
			decision.Kept.ChangedVersion = *node.keptVersion
		}
	}
	decision.WaitingReview = decision.Kind == "agent" && !decision.Replaced &&
		(decision.Kept == nil || decision.Kept.TextSHA256 != decision.TextSHA256)
	return decision, nil
}

// askType is the block type of an ask's block, whose own words - its question and its options,
// and none of the readings it closes with - say which version last changed the ask.
const askType = "ask"

// readWords reads an agent's decision's words and heading into decision: off the block's text as
// the block index recorded it last, else, where its document still owes its block index, off the
// markdown of the version that wrote its node. Words neither holds are none, and words that do not
// read back as the decision are an error.
func (node heldNode) readWords(decision *model.Decision) error {
	markdown := ""
	switch {
	case len(node.texts) > 0 && node.texts[0].Text != nil:
		markdown = *node.texts[0].Text
	case node.rowVersion == nil && node.writtenMarkdown != nil:
		markdown = *node.writtenMarkdown
	default:
		return nil
	}
	words, block, err := wordsOf(markdown, refs.DecisionSupertag, node.blockID)
	if err != nil {
		return err
	}
	decision.Text = words
	content, _ := pmdoc.TypedContent(block)
	for _, child := range content {
		if heading := plainText(child); heading != "" {
			decision.Heading = heading
			break
		}
	}
	return nil
}

// wordsOf is the own words of the block of type typ and id blockID that markdown holds - the
// block rendered alone with the readings it closes with left out (refs.DecisionWords) - and that
// block, or the error that says markdown does not read back as one.
func wordsOf(markdown, typ, blockID string) (string, *pmdoc.Node, error) {
	tree, err := pmdoc.ParseRendering(markdown)
	if err != nil {
		return "", nil, fmt.Errorf("the text of %s %q does not read back: %w", typ, blockID, err)
	}
	var found *pmdoc.Node
	pmdoc.Walk(tree, func(block *pmdoc.Node) bool {
		if id, _ := block.Attrs[pmdoc.BlockIDAttr].(string); block.Type == typ && strings.ReplaceAll(id, "\x00", "\uFFFD") == blockID {
			found = block
		}
		return found == nil
	})
	if found == nil {
		return "", nil, fmt.Errorf("the text of %s %q holds no such block", typ, blockID)
	}
	words, err := refs.DecisionWords(found)
	if err != nil {
		return "", nil, fmt.Errorf("render the words of %s %q: %w", typ, blockID, err)
	}
	return words, found, nil
}

// readVersion reads the version decision last changed its own words in, and when that version
// was written. The block's document_blocks row names the version its whole text last changed in,
// and a block's text holds the readings it closes with, so a reading written, edited or removed
// moves that version and leaves the words as they were (R2: the decision's words, its text sum
// and its Keep are the same whether readings hang under it or not). So the read starts at the
// row's version and walks back over the texts the block index recorded of the block while the
// words read the same, and answers the earliest version of that run: the version whose text
// first held the words the block holds now. A block of type typ is a decision, or an ask block,
// whose own words are its question and its options. A decision whose document still owes its
// block index answers the version that wrote its node. A version number past a 32-bit integer is
// no version a document holds, and an error.
func (node heldNode) readVersion(decision *model.Decision, typ string) error {
	switch {
	case node.rowVersion != nil:
		decision.ChangedVersion = *node.rowVersion
		if len(node.texts) == 0 || node.texts[0].Text == nil {
			return nil
		}
		changed := node.texts[0]
		decision.ChangedAt = changed.At
		// An ask block the strict read refuses is still an ask, which settlement flags `invalid`;
		// its row's own version is the one it answers. A decision's text read back already
		// (readWords), so it never stops here.
		words, _, err := wordsOf(*changed.Text, typ, node.blockID)
		if err != nil {
			decision.ChangedVersion = changed.Version
			return nil
		}
		for _, earlier := range node.texts[1:] {
			if earlier.Text == nil {
				break
			}
			if held, _, err := wordsOf(*earlier.Text, typ, node.blockID); err != nil || held != words {
				break
			}
			changed = earlier
		}
		decision.ChangedVersion, decision.ChangedAt = changed.Version, changed.At
	case node.writtenVersion != nil:
		decision.ChangedVersion, decision.ChangedAt = *node.writtenVersion, node.writtenAt
	case node.writtenNumber != nil:
		if _, err := strconv.ParseInt(*node.writtenNumber, 10, 32); err != nil {
			return fmt.Errorf("the version that wrote the node, %q, is no version a document holds: %w", *node.writtenNumber, err)
		}
	}
	return nil
}

// plainText is the text node holds, its children's included, with its lines joined by a space.
func plainText(node *pmdoc.Node) string {
	var words []string
	pmdoc.Walk(node, func(child *pmdoc.Node) bool {
		if child.Text != "" {
			words = append(words, child.Text)
		}
		return true
	})
	return strings.Join(strings.Fields(strings.Join(words, " ")), " ")
}

// stringOf is *value, or "" for nil.
func stringOf(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
