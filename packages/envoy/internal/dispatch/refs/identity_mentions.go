package refs

import (
	"maps"
	"slices"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// IdentityKinds are the node kinds an identity mention points at: a person, an agent's session and
// a role (R5). A mention of one is a `mentions` tuple from the comment, issue message or document
// block that holds it to the identity's node, in the store alone: the graph's refs and block_refs
// rows hold no party (TargetOf), so a build of the release before this one, which reconciles those
// rows by deleting every row of its source (replaceRefs, replaceBlockRefs), deletes none of these.
var IdentityKinds = []string{graphstore.KindPerson, graphstore.KindSession, graphstore.KindRole}

// identityTargets is each identity a text's references point at, once each in the order the text
// first names it: a person by their canonical login (model.CanonicalLogin), so the spellings a
// text can write of one person meet at one node, an agent's session by its id and a role by its
// token. A party a code context holds names nobody - the text shows the address rather than
// naming its party, as every renderer of the record draws it and as the comment route's party set
// reads it (api.commentPartiesOf) - and is none of them.
func identityTargets(located []text.Located) []Target {
	var found []Target
	for _, mention := range located {
		if mention.InCode || !mention.IsParty() {
			continue
		}
		target := Target{Kind: mention.Kind, ID: mention.ID}
		switch mention.Kind {
		case graphstore.KindPerson:
			target.ID = model.CanonicalLogin(mention.ID)
		case graphstore.KindSession, graphstore.KindRole:
		default:
			continue
		}
		if target.ID == "" || slices.Contains(found, target) {
			continue
		}
		found = append(found, target)
	}
	return found
}

// identityMentions plans the `mentions` tuples from fromKind/fromID to each of targets the store
// does not hold from that node already (standing), made by who at at, and answers them split by
// the writer that appends them: a person's goes through graphstore.ToPerson, the one writer of a
// tuple that names a person (graphstore.Uncaused.People), and a session's or a role's with the
// node's other facts. Each mention is appended once and never retracted, so a re-index of
// unchanged text appends nothing, and neither does a second write of the same text in one
// transaction (standing is read, and then held, by the index that plans it).
//
// A node reserved for its author (graphstore.VisibilityAuthor) mentions no person but that author
// (R2): such a tuple would be mail its person can never read, and graphstore.ToPerson refuses it.
// An end past the store's address bound is no tuple.
func identityMentions(
	fromKind, fromID string,
	visibility string,
	who model.Actor,
	at time.Time,
	targets []Target,
	standing map[Target]struct{},
) (tuples []graphstore.UncausedTuple, people []graphstore.Tuple) {
	authorKind, authorID, authored := graphstore.AuthorOf(who)
	for _, target := range targets {
		if _, held := standing[target]; held || !graphstore.AddressFits(target.ID) {
			continue
		}
		tuple := graphstore.Tuple{
			FromKind: fromKind, FromID: fromID, Kind: graphstore.TupleMentions,
			ToKind: target.Kind, ToID: target.ID, At: at, Who: who, Asserted: true,
		}
		if target.Kind != graphstore.KindPerson {
			tuples = append(tuples, graphstore.UncausedTuple{Tuple: tuple})
			continue
		}
		if visibility == graphstore.VisibilityAuthor && !(authored && authorKind == graphstore.KindPerson && authorID == target.ID) {
			continue
		}
		people = append(people, tuple)
	}
	return tuples, people
}

// statedMentions is each identity mention among tuples, the facts an index states with its node's
// others: the session and role mentions identityMentions planned.
func statedMentions(tuples []graphstore.UncausedTuple) []graphstore.Tuple {
	var mentions []graphstore.Tuple
	for _, held := range tuples {
		if held.Kind == graphstore.TupleMentions && slices.Contains(IdentityKinds, held.ToKind) {
			mentions = append(mentions, held.Tuple)
		}
	}
	return mentions
}

// ItemIdentityMentions is the mentions tuple out of the comment or the message kind/itemID to each
// identity its text body names, as the item index states one of this release's own write of that
// text (PlanItemIndex, identityMentions): read off the references the text spells out, by the
// grammar every indexing path reads (identityTargets), a person by their canonical login, an
// agent's session by its id and a role by its token, a mention in a code context naming nobody;
// refused to a node of visibility reserved for its author but for that author, and to an identity
// past the store's bound; made by who, the node's maker, at `at`, the write's moment; and to none
// of standing, the identities the store holds a mention of out of the node already. No identity's
// address is a short id, a bare key or an `#id`, the forms a reader resolves against the database
// (Reader.Mentions), or a page of the dashboard, so the references body writes out
// (text.ExtractAt) name every identity the whole grammar names, whatever deployment it is read
// for. It answers the tuples split by the writer that appends them - a session's and a role's
// beside the node's other facts (graphstore.Write), a person's through graphstore.ToPerson - each
// with no event: they are appended under the event of the write they belong to.
//
// The backfill reads it for a comment or an issue message an older build wrote, and for an edit
// of a comment it made (graphcensus' commentPointersPass, messagesPass and its pass over the queue
// of that build's events): that build states none of these tuples and stores no row of a
// person's mention (TargetOf), so the item's own text is the one record of whom it names.
func ItemIdentityMentions(kind ItemKind, itemID, visibility string, who model.Actor, at time.Time, body string, standing []Target) (identities, people []graphstore.Tuple) {
	named := identityTargets(text.ExtractAt(body, ""))
	if len(named) == 0 {
		return nil, nil
	}
	held := make(map[Target]struct{}, len(standing))
	for _, target := range standing {
		if target.Kind == graphstore.KindPerson {
			target.ID = model.CanonicalLogin(target.ID)
		}
		held[target] = struct{}{}
	}
	stated, people := identityMentions(string(kind), itemID, visibility, who, at, named, held)
	return statedMentions(stated), people
}

// blockIdentityMentions plans the identity mentions of a document version's blocks: each
// identity a reference of located names, from the innermost of nodes holding its start, as a
// block's other mentions are placed (blockPairs), where that block is a node of the store (fits,
// beside each of nodes), and where the store holds no mention of it out of that block's node
// already (standing, by block id). A block's node is made by the version's writer and read under
// its project's visibility (blockNode), which is who and what each mention is stated under, dated
// at, the version's time. It also answers what each block holds once these are appended, which a
// later index of the document in the same transaction plans against (BlockIndexing.Held).
func blockIdentityMentions(
	refKey string,
	nodes []model.ArtifactBlock,
	fits []bool,
	located []text.Located,
	standing map[string]map[Target]struct{},
	at time.Time,
	writer model.Actor,
) (tuples []graphstore.UncausedTuple, people []graphstore.Tuple, held map[string]map[Target]struct{}) {
	held = make(map[string]map[Target]struct{}, len(standing))
	for blockID, targets := range standing {
		held[blockID] = maps.Clone(targets)
	}
	parties := make([]text.Located, 0, len(located))
	for _, mention := range located {
		if !mention.InCode && mention.IsParty() {
			parties = append(parties, mention)
		}
	}
	if len(parties) == 0 {
		return nil, nil, held
	}
	starts := make([]int, len(parties))
	for index, mention := range parties {
		starts[index] = mention.Offset
	}
	holders := innermostBlocks(nodes, starts)
	var order []string
	named := map[string][]text.Located{}
	for index, holder := range holders {
		if holder < 0 || !fits[holder] {
			continue
		}
		blockID := nodes[holder].ID
		if _, seen := named[blockID]; !seen {
			order = append(order, blockID)
		}
		named[blockID] = append(named[blockID], parties[index])
	}
	for _, blockID := range order {
		blockTuples, blockPeople := identityMentions(BlockNodeKind, blockNodeAddress(refKey, blockID),
			graphstore.VisibilityProject, writer, at, identityTargets(named[blockID]), standing[blockID])
		tuples, people = append(tuples, blockTuples...), append(people, blockPeople...)
		for _, tuple := range slices.Concat(statedMentions(blockTuples), blockPeople) {
			if held[blockID] == nil {
				held[blockID] = map[Target]struct{}{}
			}
			held[blockID][Target{Kind: tuple.ToKind, ID: tuple.ToID}] = struct{}{}
		}
	}
	return tuples, people, held
}

// IdentityMentionsRead is the select of the identities the store holds `mentions` tuples to from
// one node, its kind and id the SQL expressions kind and id - numbered parameters, or columns of
// the statement it is spliced into - and the identity kinds the array kinds (IdentityKinds): a
// mention is appended once and never retracted (identityMentions), so an asserted tuple is one
// that stands. It answers (to_kind, to_id), each once.
func IdentityMentionsRead(kind, id, kinds string) string {
	return `select distinct m.to_kind, m.to_id from graph_tuples m
		where m.from_kind = ` + kind + ` and m.from_id = ` + id + ` and m.kind = '` + graphstore.TupleMentions + `'
		  and m.to_kind = any(` + kinds + `) and m.asserted`
}
