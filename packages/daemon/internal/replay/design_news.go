package replay

// design_news.go is the replay's run of the daemon's news of the approved design: the reconcile a
// design_changes row runs (internal/daemon's design_news.go), with the replay's copy of Dispatch
// (the shadow) in Dispatch's place, the rule for which claim holds a told row's news
// (internal/daemon's design_news_holders.go), and what a start reads of that news
// (internal/daemon's startNewsOf). Each function names the daemon's function it copies.

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// designChangesUnreadable is the reason the stub records the change set of a pair from a version
// above 0 under, unavailable (record.DesignChangesUnavailable), as the daemon records a pair
// Dispatch could not answer: the shadow holds each document's versions and verdicts, never its
// text, so it cannot say what changed between two versions.
const designChangesUnreadable = "the replay's copy of Dispatch holds no document's text, so it cannot say what changed between two of its versions"

// toldKey names one told row: an audience of one issue (record.DesignTold).
type toldKey struct {
	issue    string
	audience record.DesignAudience
}

// owedNewsItem is one item of a tree's owed news with the told row it is owed to.
type owedNewsItem struct {
	told toldKey
	item record.NewsItem
}

// owedNewsItems is every item news owes, each with its told row: each member's to its holder row,
// and the architect's to the tree root's architect row (internal/daemon's owedNewsItems).
func owedNewsItems(news record.TreeNews, root string) []owedNewsItem {
	var items []owedNewsItem
	for _, member := range news.Members {
		for _, item := range member.Items {
			items = append(items, owedNewsItem{told: toldKey{member.Issue, record.AudienceHolder}, item: item})
		}
	}
	for _, item := range news.Architect {
		items = append(items, owedNewsItem{told: toldKey{root, record.AudienceArchitect}, item: item})
	}
	return items
}

// designChangesRow runs a design_changes row as the daemon's executor does (internal/daemon's
// designChanges), the shadow answering what the executor reads of Dispatch. It reads what the
// row's tree owes (record.OwedNews). For each item whose told row knows no version of its step, it
// reads from the shadow the version a person approved before the item's To (shadow.approvedBefore,
// Dispatch's review history), and writes that version into the told row (record.Store.FillTold),
// so the item names its pair. It records the change set of each pair the record does not hold: read
// with nothing listed for a pair from version 0, which the executor records so without asking
// Dispatch, and unavailable for every other pair (designChangesUnreadable). Last, it settles each
// told row owed news (settleNews). A root the record no longer holds, and a tree owed nothing,
// finish with nothing written.
//
// The shadow answers every question of a review history, so the stub records none unread
// (record.DesignHistoryUnread), and an item is one the record can tell once its pair's change set
// is recorded. A merger's start its news holds (mergerNewsUnrecorded) waits on its backoff, as in
// the daemon, and starts at its first try after this row, which waits on no read of Dispatch.
func (d *driver) designChangesRow(row record.OutboxRow) error {
	var root *record.Issue
	var owed record.TreeNews
	if err := pgx.BeginTxFunc(d.ctx, d.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		if root, err = d.records.Issue(d.ctx, tx, row.Issue); err != nil || root == nil {
			return err
		}
		facts, err := record.TreeNewsFacts(d.ctx, d.records, tx, d.window.GateSteps, root.Tree)
		owed = record.OwedNews(facts)
		return err
	}); err != nil {
		return fmt.Errorf("read what the tree of %s owes of its approved design: %w", row.Issue, err)
	}
	if root == nil || len(owed.Members) == 0 && !owed.ArchitectOwed() {
		return d.finish(row)
	}
	var filled []toldKey
	fills := map[toldKey]map[record.DesignStep]int{}
	var pairs []record.VersionPair
	for _, owedItem := range owedNewsItems(owed, root.Tree) {
		item := owedItem.item
		pair, known := item.Pair()
		if !known {
			if fills[owedItem.told] == nil {
				fills[owedItem.told] = map[record.DesignStep]int{}
				filled = append(filled, owedItem.told)
			}
			from := d.shadow.approvedBefore(item.ArtifactID, item.To)
			fills[owedItem.told][item.Step] = from
			pair = record.VersionPair{ArtifactID: item.ArtifactID, From: from, To: item.To}
		}
		if !slices.Contains(pairs, pair) {
			pairs = append(pairs, pair)
		}
	}
	at := d.now().UTC()
	if err := d.st.Tx(d.ctx, func(tx pgx.Tx) error {
		recorded, err := d.records.DesignChangesOf(d.ctx, tx, pairs)
		if err != nil {
			return err
		}
		for _, pair := range pairs {
			if _, known := recorded[pair]; known {
				continue
			}
			changes := record.DesignChanges{VersionPair: pair, State: record.DesignChangesRead, ReadAt: at}
			if pair.From != 0 {
				changes.State, changes.Reason = record.DesignChangesUnavailable, designChangesUnreadable
			}
			if err := d.records.PutDesignChanges(d.ctx, tx, changes); err != nil {
				return err
			}
		}
		for _, key := range filled {
			if err := d.records.FillTold(d.ctx, tx, key.issue, key.audience, fills[key], at); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("record what the reconcile of the tree of %s read from the replay's copy of Dispatch: %w", root.Tree, err)
	}
	if err := d.settleNews(row, root.Tree); err != nil {
		return err
	}
	return d.finish(row)
}

// newsSettlement is what the reconcile does for one told row owed news (settlementOf).
type newsSettlement int

const (
	// settleLater leaves the news owed: an item the record cannot tell yet.
	settleLater newsSettlement = iota
	// settleRaise raises the told versions directly and sends nothing: no pair changed anything of
	// the work, and no opening is owed.
	settleRaise
	// settleSend sends the news, by a news-only start.
	settleSend
)

// settlementOf is what the reconcile does for one told row owed items (internal/daemon's
// settlementOf), given the change sets recorded: settleLater while an item names no pair whose
// change set is recorded; settleRaise when every pair changed nothing of the work
// (record.DesignChanges.WorkUnchanged) and the row owes no opening; settleSend otherwise. The stub
// records no review history unread, so an item whose told row knows no version is one the record
// cannot tell yet, as the daemon reads such an item with no unread row.
func settlementOf(items []record.NewsItem, changes map[record.VersionPair]record.DesignChanges, openingOwed bool) newsSettlement {
	workUnchanged := true
	for _, item := range items {
		pair, known := item.Pair()
		recorded, told := changes[pair]
		if !known || !told {
			return settleLater
		}
		workUnchanged = workUnchanged && recorded.WorkUnchanged()
	}
	if workUnchanged && !openingOwed {
		return settleRaise
	}
	return settleSend
}

// changesOf is the recorded change set of each pair items name, read in tx
// (record.Store.DesignChangesOf).
func (d *driver) changesOf(tx pgx.Tx, items []record.NewsItem) (map[record.VersionPair]record.DesignChanges, error) {
	var pairs []record.VersionPair
	for _, item := range items {
		if pair, known := item.Pair(); known && !slices.Contains(pairs, pair) {
			pairs = append(pairs, pair)
		}
	}
	return d.records.DesignChangesOf(d.ctx, tx, pairs)
}

// settleNews is the reconcile's settling of tree's news (internal/daemon's settleNews), in one
// transaction: it reads again what tree owes and the change sets recorded, and settles each told
// row owed news (settlementOf). A row whose every pair changed nothing of the work, owing no
// opening, has its versions raised (record.Store.RaiseTold). The news of every other row the record
// can tell goes to the claim that holds it (readOwedHolders) by a news-only start
// (record.SuperviseRequest.News), queued as a row of its own on that claim's issue: a phase
// worker's, then each architect's once, in token order. None is queued for a claim that has one
// queued already (newsStartQueued), that holds a task its worker has not taken
// (holdsUntakenTask), or that a news-only start may not reach (recordedClaimTakesNews), nor for a
// row whose holder cannot be named; each of those is told at its next start, as in the daemon. Each
// start queued is logged as the daemon logs it, once the transaction commits (`outbox
// design_changes queued a news-only start`), naming row, the design_changes row that settled it.
func (d *driver) settleNews(row record.OutboxRow, tree string) error {
	now := d.now().UTC()
	var queued []owedHolder
	err := d.st.Tx(d.ctx, func(tx pgx.Tx) error {
		queued = nil
		root, err := d.records.Issue(d.ctx, tx, tree)
		if err != nil || root == nil {
			return err
		}
		facts, err := record.TreeNewsFacts(d.ctx, d.records, tx, d.window.GateSteps, root.Tree)
		if err != nil {
			return err
		}
		owed := record.OwedNews(facts)
		var items []record.NewsItem
		for _, owedItem := range owedNewsItems(owed, root.Tree) {
			items = append(items, owedItem.item)
		}
		changes, err := d.changesOf(tx, items)
		if err != nil {
			return err
		}
		var sendTo []toldKey
		for _, news := range owed.Members {
			switch settlementOf(news.Items, changes, record.ToldRow(facts.Told, news.Issue, record.AudienceHolder).OpeningOwed) {
			case settleLater:
				continue
			case settleRaise:
				if err := d.records.RaiseTold(d.ctx, tx, news.Issue, record.AudienceHolder, news.Items, now); err != nil {
					return err
				}
				continue
			}
			sendTo = append(sendTo, toldKey{news.Issue, record.AudienceHolder})
		}
		if owed.ArchitectOwed() {
			switch settlementOf(owed.Architect, changes, record.ToldRow(facts.Told, root.Key, record.AudienceArchitect).OpeningOwed) {
			case settleRaise:
				if err := d.records.RaiseTold(d.ctx, tx, root.Key, record.AudienceArchitect, owed.Architect, now); err != nil {
					return err
				}
			case settleSend:
				sendTo = append(sendTo, toldKey{root.Key, record.AudienceArchitect})
			}
		}
		holders, err := d.readOwedHolders(tx, newNewsClaims(*root, facts), sendTo)
		if err != nil {
			return err
		}
		var starts []owedHolder
		owners := map[claim.Token]owedHolder{}
		for _, key := range sendTo {
			holder, held := holders[key]
			switch {
			case !held:
			case holder.role == claim.RoleArchitect:
				owners[holder.token] = holder
			default:
				starts = append(starts, holder)
			}
		}
		for _, owner := range slices.Sorted(maps.Keys(owners)) {
			starts = append(starts, owners[owner])
		}
		for _, start := range starts {
			if newsStartQueued(start.run) || holdsUntakenTask(start.run.Claim) || !recordedClaimTakesNews(start.run.Claim, start.entry) {
				continue
			}
			request := record.SuperviseRequest{Op: "start", Tree: start.issue.Tree, Role: start.role, Generation: start.issue.Generation, News: true}
			if start.role != claim.RoleArchitect {
				request.Phase = start.issue.Phase
			}
			queue, err := record.NewOutboxRow(start.issue.Key, request, now)
			if err != nil {
				return err
			}
			if err := d.records.Enqueue(d.ctx, tx, queue); err != nil {
				return err
			}
			queued = append(queued, start)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("settle the news the tree of %s is owed: %w", tree, err)
	}
	for _, start := range queued {
		d.log.Info("outbox design_changes queued a news-only start", "row", row.ID, "tree", tree, "issue", start.issue.Key, "role", start.role)
	}
	return nil
}

// newsHolder is the claim that holds one member's phase now (newsClaims.holderOf): the issue whose
// role that claim holds, the role and the claim's token.
type newsHolder struct {
	issue record.Issue
	role  claim.Role
	token claim.Token
}

// newsClaims names the holder of each member of one tree (internal/daemon's newsClaims, read over
// the claims the store records, as the reconcile reads them): project is the tree's project token,
// read from its root's record, "" where that project names none; members is every issue of the
// tree, as record.TreeNewsFacts read it; owner is the owner walk, built the first time an owner is
// asked, nil until then.
type newsClaims struct {
	project string
	root    record.Issue
	members map[string]record.Issue
	owner   func(record.Issue) string
}

// newNewsClaims names the holders of root's tree from facts, what the caller read of the tree.
func newNewsClaims(root record.Issue, facts record.NewsFacts) *newsClaims {
	claims := &newsClaims{root: root, members: make(map[string]record.Issue, len(facts.Members))}
	for _, member := range facts.Members {
		claims.members[member.Key] = member
	}
	claims.project, _ = claim.ProjectToken(root.Project)
	return claims
}

// member is the issue of the tree whose key is key, and an error for a key of no issue of the
// tree, which names no holder.
func (c *newsClaims) member(key string) (record.Issue, error) {
	issue, held := c.members[key]
	if !held {
		return record.Issue{}, fmt.Errorf("the record holds no issue %s in the tree of %s", key, c.root.Key)
	}
	return issue, nil
}

// holderOf is which claim holds member's phase now, and whether one does (internal/daemon's
// newsClaims.holderOf): for a member in accepting, the architect that owns it, by the owner walk
// from the member itself over the architect claims the store records (record.OwningIssue,
// record.ArchitectRuns); for a member in any other phase, the claim of the role that phase names.
// A phase no role holds, and a token that cannot be built, name none.
func (c *newsClaims) holderOf(ctx context.Context, tx pgx.Tx, records record.Store, member record.Issue) (newsHolder, bool, error) {
	switch {
	case c.project == "":
		return newsHolder{}, false, nil
	case member.Phase != phase.Accepting:
		role := phase.RoleFor(member.Phase)
		if role == "" || role == claim.RoleArchitect {
			return newsHolder{}, false, nil
		}
		token, err := claim.NewToken(c.project, member.Key, role)
		if err != nil {
			return newsHolder{}, false, nil
		}
		return newsHolder{issue: member, role: role, token: token}, true, nil
	}
	if c.owner == nil {
		if _, err := c.member(c.root.Key); err != nil {
			return newsHolder{}, false, err
		}
		tree := record.TreeByKey(slices.Collect(maps.Values(c.members)))
		runs, err := record.ArchitectRuns(ctx, records, tx, c.project, tree)
		if err != nil {
			return newsHolder{}, false, err
		}
		c.owner = func(issue record.Issue) string { return record.OwningIssue(c.project, tree, issue, false, runs) }
	}
	owner, err := c.member(c.owner(member))
	if err != nil {
		return newsHolder{}, false, err
	}
	token, err := claim.NewToken(c.project, owner.Key, claim.RoleArchitect)
	if err != nil {
		return newsHolder{}, false, nil
	}
	return newsHolder{issue: owner, role: claim.RoleArchitect, token: token}, true, nil
}

// rootArchitect is the holder of the tree root's architect row: the root's architect claim. A root
// whose project or key names no token has none.
func (c *newsClaims) rootArchitect() (newsHolder, bool) {
	if c.project == "" {
		return newsHolder{}, false
	}
	token, err := claim.NewToken(c.project, c.root.Key, claim.RoleArchitect)
	if err != nil {
		return newsHolder{}, false
	}
	return newsHolder{issue: c.root, role: claim.RoleArchitect, token: token}, true
}

// owedHolder is the holder of one told row news owes, with the entry its role's phases row names
// (0 where the row names none) and its run as the record holds it (record.RoleRun).
type owedHolder struct {
	newsHolder
	entry int64
	run   record.RoleRun
}

// readOwedHolders is who is told each told row of keys, rows owed news (internal/daemon's
// readOwedHolders): the holder of a member's holder row (newsClaims.holderOf), and of the tree
// root's architect row (newsClaims.rootArchitect), each with its entry and run, by told row. A row
// whose holder cannot be named is absent.
func (d *driver) readOwedHolders(tx pgx.Tx, claims *newsClaims, keys []toldKey) (map[toldKey]owedHolder, error) {
	holders := make(map[toldKey]owedHolder, len(keys))
	for _, key := range keys {
		if key.audience == record.AudienceArchitect {
			if holder, held := claims.rootArchitect(); held {
				holders[key] = owedHolder{newsHolder: holder}
			}
			continue
		}
		member, err := claims.member(key.issue)
		if err != nil {
			return nil, err
		}
		holder, held, err := claims.holderOf(d.ctx, tx, d.records, member)
		if err != nil {
			return nil, err
		}
		if held {
			holders[key] = owedHolder{newsHolder: holder}
		}
	}
	runKeys := make([]record.RoleRunKey, 0, len(holders))
	for key, holder := range holders {
		rows, err := d.records.Phases(d.ctx, tx, holder.issue.Key)
		if err != nil {
			return nil, err
		}
		holder.entry = record.PhaseRowOf(rows, holder.role).Entry
		holders[key] = holder
		runKeys = append(runKeys, record.RoleRunKey{Token: holder.token, Issue: holder.issue.Key, Role: holder.role, Generation: holder.issue.Generation})
	}
	runs, err := d.records.RoleRuns(d.ctx, tx, runKeys)
	if err != nil {
		return nil, err
	}
	for key, holder := range holders {
		holder.run = runs[holder.token]
		holders[key] = holder
	}
	return holders, nil
}

// newsStartQueued says whether run holds a news-only start still to run (internal/daemon's
// newsStartQueued).
func newsStartQueued(run record.RoleRun) bool {
	return slices.ContainsFunc(run.Queued, func(queued record.QueuedSupervise) bool {
		return queued.Request.Op == "start" && queued.Request.News
	})
}

// holdsUntakenTask says whether held, a claim as the record holds it, holds a task its worker has
// not taken yet (internal/daemon's holdsUntakenTask): a pending delivery with no confirmation.
func holdsUntakenTask(held *record.RoleClaim) bool {
	return held != nil && supervise.HoldsTask(held.Pending) && held.Pending.ConfirmedAt.IsZero()
}

// newsClaimTakes is the one rule for whether a news-only start may hand its news to a claim
// (internal/daemon's newsClaimTakes): it was given the entry its role's phases row names, it has
// not ended (claimEnded), and no operator holds it suspended.
func newsClaimTakes(given, entry int64, state supervise.ClaimState, operatorHolds bool) bool {
	return given == entry && !claimEnded(state) && !operatorHolds
}

// claimEnded says whether a claim's state runs nothing: none, failed or retired
// (internal/daemon's claimEnded).
func claimEnded(state supervise.ClaimState) bool {
	return state == "" || state == supervise.StateFailed || state == supervise.StateRetired
}

// recordedClaimTakesNews is newsClaimTakes over a claim as the record holds it, nil for a role the
// record holds no claim of, which takes no news (internal/daemon's recordedClaimTakesNews).
func recordedClaimTakesNews(held *record.RoleClaim, entry int64) bool {
	if held == nil {
		return false
	}
	operator := held.State == supervise.StateSuspended && held.SuspendedBy == supervise.SuspendedByOperator
	return newsClaimTakes(held.GivenEntry, entry, held.State, operator)
}

// newsOnlyReaches says whether a news-only start reaches c, the claim the stub holds of its role
// (found where it holds one), as internal/daemon's newsOnlyRefusal reads the claim when the start
// runs: one that exists and newsClaimTakes takes, entry the entry the role's phases row names.
func newsOnlyReaches(c supervise.Claim, found bool, entry int64) bool {
	return found && newsClaimTakes(c.GivenEntry, entry, c.State, c.RuleHolds())
}

// mergerNewsUnrecorded says whether issue's holder is owed news of the approved design that no
// start can carry yet (internal/daemon's mergerNewsUnrecorded): the reconcile would leave it owed
// (settlementOf's settleLater). The merger's start waits while it is (start), as it waits while the
// design gate is closed.
func (d *driver) mergerNewsUnrecorded(issue record.Issue) (bool, error) {
	unrecorded := false
	err := pgx.BeginTxFunc(d.ctx, d.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		facts, err := record.TreeNewsFacts(d.ctx, d.records, tx, d.window.GateSteps, issue.Tree)
		if err != nil {
			return err
		}
		member, owes := record.OwedNews(facts).Member(issue.Key)
		if !owes {
			return nil
		}
		changes, err := d.changesOf(tx, member.Items)
		unrecorded = settlementOf(member.Items, changes, false) == settleLater
		return err
	})
	if err != nil {
		return false, fmt.Errorf("read whether the merger of %s is owed news no start can carry yet: %w", issue.Key, err)
	}
	return unrecorded, nil
}

// creditNews is what a task given to the claim token, of role on issue, carries of news of the
// approved design (internal/daemon's newsForStart, over the told rows internal/daemon's
// owedToClaim names), and, where credit says so, the credit of that task, written as the daemon's
// claim store writes it once the worker's turn has taken the task and run (store's
// settleDesignNews): each item owed a told row the claim speaks for whose change the record can
// tell raises that row past it (record.Store.RaiseTold, a credit counted from the item's From), and
// each opening owed such a row while the standing gate is open is told (record.Store.SettleOpening).
// An item the record cannot tell yet is left owed, as the daemon's start leaves it. The replay's
// workers take each task at its start (assumption 30), so the credit is written then. It answers
// whether the task carries anything (internal/daemon's newsAtSend.says), on which a start queued
// for news alone finishes.
//
// The rows a claim speaks for are, for a phase worker, its own member's holder row while the role
// is the one the member's phase names, and, for an architect, the tree root's architect row where
// the claim is the root's, and the holder row of each member in accepting it owns
// (newsClaims.holderOf). A tree whose root the record does not hold carries nothing.
func (d *driver) creditNews(issue record.Issue, role claim.Role, token claim.Token, credit bool) (bool, error) {
	says := false
	now := d.now().UTC()
	err := d.st.Tx(d.ctx, func(tx pgx.Tx) error {
		says = false
		facts, err := record.TreeNewsFacts(d.ctx, d.records, tx, d.window.GateSteps, issue.Tree)
		if err != nil {
			return err
		}
		rootAt := slices.IndexFunc(facts.Members, func(member record.Issue) bool { return claim.IsTreeRoot(member.Key, member.Tree) })
		if rootAt < 0 {
			return nil
		}
		owed := record.OwedNews(facts)
		open := record.TreeGate{Steps: facts.Steps, Rows: facts.Gates}.Open()
		var rows []toldKey
		carried := map[toldKey][]record.NewsItem{}
		tell := func(key toldKey, news []record.NewsItem) {
			rows = append(rows, key)
			carried[key] = news
		}
		root := facts.Members[rootAt]
		claims := newNewsClaims(root, facts)
		if holder, held := claims.rootArchitect(); held && holder.token == token && owed.ArchitectOwed() {
			tell(toldKey{root.Key, record.AudienceArchitect}, owed.Architect)
		}
		for _, news := range owed.Members {
			if role != claim.RoleArchitect && news.Issue != issue.Key {
				continue
			}
			member, err := claims.member(news.Issue)
			if err != nil {
				return err
			}
			holder, held, err := claims.holderOf(d.ctx, tx, d.records, member)
			if err != nil {
				return err
			}
			if held && holder.token == token {
				tell(toldKey{news.Issue, record.AudienceHolder}, news.Items)
			}
		}
		var items []record.NewsItem
		for _, key := range rows {
			items = append(items, carried[key]...)
		}
		changes, err := d.changesOf(tx, items)
		if err != nil {
			return err
		}
		for _, key := range rows {
			var told []record.NewsItem
			for _, item := range carried[key] {
				if pair, known := item.Pair(); known {
					if _, recorded := changes[pair]; recorded {
						told = append(told, item)
					}
				}
			}
			opening := open && record.ToldRow(facts.Told, key.issue, key.audience).OpeningOwed
			says = says || len(told) > 0 || opening
			if !credit {
				continue
			}
			if len(told) > 0 {
				if err := d.records.RaiseTold(d.ctx, tx, key.issue, key.audience, told, now); err != nil {
					return err
				}
			}
			if opening {
				if err := d.records.SettleOpening(d.ctx, tx, key.issue, key.audience, false); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("credit the news the start of %s's %s carries: %w", issue.Key, role, err)
	}
	return says, nil
}
