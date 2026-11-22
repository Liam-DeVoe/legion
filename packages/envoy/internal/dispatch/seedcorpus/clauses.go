package seedcorpus

import (
	"fmt"
	"strings"
)

// The separate part of the loader this commit leaves out of the default seed, named for the head
// whose kind or relation its bullets need. It cannot be written at this head: its bullets are
// relations no writer here states, so a part written here would write rows that hold none of them.
// It is absent until T6 brings that head to the line and writes it there.
const (
	leftINFRA1152Recipient = "the separate part seedINFRA1152 (infra1152.go), absent at this head: a person as the recipient " +
		"of what reaches them is dispatch://INFRA-1152's delivery to a person (6fe7f108d3, inboxwrite.Record; 8dccf009d1, " +
		"an ask's create and a session's reply deliver to the person; bd993067ba, a mention delivers to the person it names)"
	leftINFRA1152Page = "the separate part seedINFRA1152 (infra1152.go), absent at this head: a message on no issue hangs " +
		"from its agent's page, and a reply from the message it answers, by dispatch://INFRA-1152's message tuples (d37dd05857)"
)

// count is a read whose statement is `select count(*)` over the rest.
func count(what, from string, want int, args ...any) read {
	return read{what: what, sql: "select count(*) " + from, args: args, want: want}
}

// modelClauses are the bullets of the seeded issue key, the rows of the two checks, and the role
// whose holder changed, which holds the issue handover.
func modelClauses(key, handover string) []clause {
	block := func(id string) string { return key + "/tree#" + id }
	tuple := func(fromKind, fromID, kind, toKind, toID string) string {
		return fmt.Sprintf("from_kind = '%s' and from_id = '%s' and kind = '%s' and to_kind = '%s' and to_id = '%s'", fromKind, fromID, kind, toKind, toID)
	}
	node := func(kind, id string) string {
		return fmt.Sprintf("from graph_nodes where kind = '%s' and id = '%s'", kind, id)
	}
	premortem := `(select l.from_id from ` + current(`from_kind = 'lens' and kind = 'has_kind' and to_id = 'pre-mortem'`) + ` l
		join ` + current(`from_kind = 'lens' and kind = 'part_of' and to_kind = 'issue'`) + ` p on p.from_id = l.from_id and p.to_id = $1)`
	hidden := `(select n.id from graph_nodes n join ` + current(`from_kind = 'lens' and kind = 'part_of'`) + ` p on p.from_id = n.id
		where n.kind = 'lens' and n.visibility = 'author' and p.to_kind = 'block' and p.to_id = $1 || '/tree#r-three')`
	resultAt := func(check string) string {
		return `(select max(created_at) from check_results where issue_key = $1 and check_block_id = '` + check + `')`
	}
	textAfter := func(check string) string {
		return `from graph_tuples where from_kind = 'block' and from_id = '` + block(check) + `' and kind = 'text' and to_kind = 'value' and at > ` + resultAt(check)
	}
	return []clause{
		{name: "bullet 1, an answered ask with options and an answer", reads: []read{
			count("asks rows of block q-answered answered with two options and an answer",
				`from asks where issue_key = $1 and block_id = 'q-answered' and state = 'answered' and jsonb_array_length(options) = 2 and answer is not null`, 1, key),
			count("ask nodes of it", `from graph_nodes n join asks a on n.kind = 'ask' and n.id = a.id::text where a.issue_key = $1 and a.block_id = 'q-answered'`, 1, key),
		}},
		{name: "bullet 2, a second ask still open", reads: []read{
			count("asks rows of block q-open still open", `from asks where issue_key = $1 and block_id = 'q-open' and state = 'open'`, 1, key),
		}},
		{name: "bullet 3, an agent's decision with an uncertainty level and its reason", reads: []read{
			count("decision nodes d-agent", node("block", block("d-agent"))+` and supertag = 'decision'`, 1),
			count("of its values uncertainty and uncertainty_reason, each held",
				`from (select distinct kind from graph_tuples where from_kind = 'block' and from_id = '`+block("d-agent")+`' and to_kind = 'value' and kind in ('uncertainty', 'uncertainty_reason') and to_id <> '') held`, 2),
			count("decided_by tuples to the agent's session", "from "+current(tuple("block", block("d-agent"), "decided_by", "session", agentSession))+" t", 1),
		}},
		{name: "bullet 4, a reading under that decision", reads: []read{
			count("reading nodes g-agent", node("block", block("g-agent"))+` and supertag = 'reading'`, 1),
			count("part_of tuples from it to the decision", "from "+current(tuple("block", block("g-agent"), "part_of", "block", block("d-agent")))+" t", 1),
		}},
		{name: "bullet 5, two requirements, one of them shaped by the answered ask", reads: []read{
			count("requirement nodes r-one and r-two", `from graph_nodes where kind = 'block' and id in ('`+block("r-one")+`', '`+block("r-two")+`')`, 2),
			count("shaped tuples from the answered ask to r-one", "from "+current(`from_kind = 'ask' and kind = 'shaped' and to_id = '`+block("r-one")+`' and from_id in (select id::text from asks where issue_key = $1 and block_id = 'q-answered')`)+" t", 1, key),
		}},
		{name: "bullet 6, a check that serves three requirements, two under one criterion and the third under another", reads: []read{
			count("serves tuples from k-three", "from "+current(`from_kind = 'block' and from_id = '`+block("k-three")+`' and kind = 'serves'`)+" t", 3),
			count("serves tuples from r-one and r-two to c-want", "from "+current(`from_kind = 'block' and from_id in ('`+block("r-one")+`', '`+block("r-two")+`') and kind = 'serves' and to_id = '`+block("c-want")+`'`)+" t", 2),
			count("serves tuples from r-three to c-other", "from "+current(tuple("block", block("r-three"), "serves", "block", block("c-other")))+" t", 1),
		}},
		{name: "bullet 7, a check with a passing result recorded against its current words", reads: []read{
			count("passing results of k-pass", `from check_results where issue_key = $1 and check_block_id = 'k-pass' and verdict = 'pass'`, 1, key),
			count("words of k-pass stated after its result", textAfter("k-pass"), 0, key),
		}},
		{name: "bullet 8, a check with a failing result on it", reads: []read{
			count("failing results of k-fail", `from check_results where issue_key = $1 and check_block_id = 'k-fail' and verdict = 'fail'`, 1, key),
		}},
		{name: "bullet 9, a check whose only result was recorded against earlier words", reads: []read{
			count("results of k-stale", `from check_results where issue_key = $1 and check_block_id = 'k-stale'`, 1, key),
			count("words of k-stale stated after its result", textAfter("k-stale"), 1, key),
		}},
		{name: "bullet 10, a check that serves one requirement and holds no result", reads: []read{
			count("serves tuples from k-bare", "from "+current(`from_kind = 'block' and from_id = '`+block("k-bare")+`' and kind = 'serves'`)+" t", 1),
			count("results of k-bare", `from check_results where issue_key = $1 and check_block_id = 'k-bare'`, 0, key),
		}},
		{name: "bullet 11, a declaration of what a person wants, which the two requirements answer", reads: []read{
			count("criterion nodes c-want", node("block", block("c-want")), 1),
			count("serves tuples into c-want", "from "+current(`from_kind = 'block' and kind = 'serves' and to_id = '`+block("c-want")+`'`)+" t", 2),
		}},
		{name: "bullet 12, two plan_task nodes, one serving two requirements and one none, with a size on one", reads: []read{
			count("plan tasks of the plan", "from "+current(`from_kind = 'block' and from_id like '`+key+`/tree#t-%' and kind = 'in_document' and to_id = '`+key+`/plan-md'`)+" t", 2),
			count("serves tuples from t-serves", "from "+current(`from_kind = 'block' and from_id = '`+block("t-serves")+`' and kind = 'serves'`)+" t", 2),
			count("serves tuples from t-none", "from "+current(`from_kind = 'block' and from_id = '`+block("t-none")+`' and kind = 'serves'`)+" t", 0),
			count("size values of t-serves reading 2.5", `from (select distinct to_id from graph_tuples where from_kind = 'block' and from_id = '`+block("t-serves")+`' and kind = 'size' and to_kind = 'value' and to_id = '2.5') sizes`, 1),
		}},
		{name: "bullet 13, a comment thread whose reply answers another reply", reads: []read{
			count("replies answering a reply that answers the thread's root",
				`from comments c join comments r on c.answers = r.id join comments root on r.answers = root.id
				 where c.issue_key = $1 and root.reply_to is null and r.reply_to = root.id and c.reply_to = root.id`, 1, key),
		}},
		{name: "bullet 14, a second thread whose replies form a cycle", reads: []read{
			count("pairs of replies whose reply_to names each other",
				`from comments a join comments b on a.reply_to = b.id and b.reply_to = a.id where a.issue_key = $1 and a.id < b.id`, 1, key),
		}},
		{name: "bullet 15, a person who is the recipient of a message, a broadcast, a comment, an ask and a reply", left: leftINFRA1152Recipient},
		{name: "bullet 16, an agent with one session, one role it holds and its page", reads: []read{
			count("session nodes of the agent", node("session", agentSession), 1),
			count("role nodes of the agent", node("role", agentRole), 1),
			count("held_by tuples from the role to the session", "from "+current(tuple("role", agentRole, "held_by", "session", agentSession))+" t", 1),
			count("part_of tuples from the session to the role", "from "+current(tuple("session", agentSession, "part_of", "role", agentRole))+" t", 1),
			count("part_of tuples from the role's page to the role", "from "+current(tuple("page", "role/"+agentRole, "part_of", "role", agentRole))+" t", 1),
		}},
		{name: "bullet 16, a message under the agent's page and a reply under that message", left: leftINFRA1152Page},
		{name: "bullet 17, an issue waiting on an issue, and a plan_task of one issue waiting on a plan_task of another", reads: []read{
			count("needs tuples from the seeded issue to the unrelated issue it waits on", "from "+current(`from_kind = 'issue' and from_id = $1 and kind = 'needs' and to_kind = 'issue'`)+
				` t join issues w on w.key = t.to_id where w.title = $2`, 1, key, waitedTitle),
			count("needs tuples from a plan task of a child of the seeded issue to its t-serves", "from "+current(`from_kind = 'block' and kind = 'needs' and to_kind = 'block' and to_id = '`+block("t-serves")+`'`)+
				` t join issues c on t.from_id like c.key || '/tree#%' where c.parent_key = $1 and c.title = $2`, 1, key, childTitle),
		}},
		{name: "bullet 18, a lens of kind pre-mortem with two risks, one pointing at a check", reads: []read{
			count("pre-mortem lenses on the seeded issue", "from "+premortem+" l", 1, key),
			count("risks under it", "from "+current(`from_kind = 'risk' and kind = 'part_of'`)+" r where r.to_kind = 'lens' and r.to_id in "+premortem, 2, key),
			count("prevented_by tuples from its risks to k-pass", "from "+current(`from_kind = 'risk' and kind = 'prevented_by' and to_id = '`+block("k-pass")+`'`)+" p join "+
				current(`from_kind = 'risk' and kind = 'part_of'`)+" r on r.from_id = p.from_id where r.to_id in "+premortem, 1, key),
		}},
		{name: "bullet 19, a lens of kind inversion that the pre-mortem lens triggered", reads: []read{
			count("inversion lenses triggered by it", "from "+current(`from_kind = 'lens' and kind = 'triggered_by'`)+" t join "+
				current(`from_kind = 'lens' and kind = 'has_kind' and to_id = 'inversion'`)+" k on k.from_id = t.from_id where t.to_id in "+premortem, 1, key),
		}},
		{name: "bullet 20, an interaction node a second credential wrote, held at author visibility", reads: []read{
			count("interaction nodes bob's credential made, reserved for it", `from graph_nodes where kind = 'interaction' and made_by->>'id' = $1 and visibility = 'author'`, 1, bob),
		}},
		{name: "bullet 21, a criterion the person wrote in a comment, a second in an ask, a third in a message", reads: []read{
			count("kinds the parents of c-said, c-asked and c-messaged are of", `from (select distinct to_kind from `+current(`from_kind = 'block' and from_id in ('`+
				block("c-said")+`', '`+block("c-asked")+`', '`+block("c-messaged")+`') and kind = 'parent'`)+` t where to_kind in ('comment', 'ask', 'message')) kinds`, 3),
			count("serves tuples from r-said to c-said, the requirement under the comment's criterion", "from "+current(tuple("block", block("r-said"), "serves", "block", block("c-said")))+" t", 1),
		}},
		{name: "bullet 22, a node held at author visibility whose parent, backlink and two neighbours are project", reads: []read{
			count("lenses reserved for their author on r-three", "from "+hidden+" h", 1, key),
			count("project parents of it", `from graph_nodes where kind = 'block' and id = $1 || '/tree#r-three' and visibility = 'project'`, 1, key),
			count("project neighbours of it on r-three", `from graph_nodes n join `+current(`from_kind = 'lens' and kind = 'part_of'`)+` p on p.from_id = n.id
				where n.kind = 'lens' and n.visibility = 'project' and p.to_id = $1 || '/tree#r-three'`, 2, key),
			count("project lenses triggered by it", `from graph_nodes n join `+current(`from_kind = 'lens' and kind = 'triggered_by'`)+` t on t.from_id = n.id
				where n.kind = 'lens' and n.visibility = 'project' and t.to_id in `+hidden, 1, key),
		}},
		{name: "bullet 23, an acyclic reply chain one node longer than the walk's depth bound", reads: []read{
			count("comments the chain holds from its leaf to its root, over reply_to", `from (with recursive walk as (
				select id, reply_to, 0 as hops from comments where issue_key = $1 and body = $2
				union all
				select c.id, c.reply_to, w.hops + 1 from comments c join walk w on c.id = w.reply_to where w.hops < 100)
				select * from walk) chain`, chainComments, key, fmt.Sprintf("Link %d of the long reply chain.", chainComments-1)),
		}},
		{name: "bullet 24, one login written in two letter cases and a role whose holder changed", reads: []read{
			count("person nodes of dana in any case", `from graph_nodes where kind = 'person' and lower(id) = 'dana'`, 1),
			count("comments dana wrote, as Dana and as dana", `from comments where issue_key = $1 and author->>'id' = 'dana' and body like 'Dana''s note%'`, 2, key),
			count("held_by tuples from the role to its first holder retracted", `from graph_tuples where `+tuple("role", handoverRole, "held_by", "session", handoverFirst)+` and not asserted`, 1),
			count("held_by tuples from the role to its second holder current", "from "+current(tuple("role", handoverRole, "held_by", "session", handoverSecond))+" t", 1),
			count("issues routed to the role", `from issues where key = $1 and route = $2`, 1, handover, "role:"+handoverRole),
		}},
		{name: "c-agent-page's row, a message posted on an issue to the agent whose delivery attempt failed", reads: []read{
			count("messages of the seeded issue to the agent whose every attempt failed", `from messages m where m.issue_key = $1 and m.target = $2
				and exists (select 1 from message_deliveries d where d.message_id = m.id)
				and not exists (select 1 from message_deliveries d where d.message_id = m.id and d.state <> 'failed')`, 1, key, "session:"+agentSession),
		}},
		{name: "c-routes-deleted's row the Inbox draws, alice's unread comment thread", reads: []read{
			count("unread comment thread rows of alice on her comment bob answered", `from user_threads u join comments c on c.id = u.thread_id
				where u.login = 'alice' and u.kind = 'comment' and u.unread and c.issue_key = $1 and c.body = 'Is the folded part open by default?'`, 1, key),
		}},
		{name: "c-routes-deleted's row the Inbox does not draw, the thread of an open ask on an open issue assigned to alice", reads: []read{
			count("unread ask thread rows of alice on an open ask of her open issue", `from user_threads u join asks a on a.id = u.thread_id join issues i on i.key = a.issue_key
				where u.login = 'alice' and u.kind = 'ask' and u.unread and a.state = 'open' and a.block_id = 'q-open'
				  and i.key = $1 and i.closed_at is null and i.assignee = 'alice'`, 1, key),
			count("unread thread rows of alice on the seeded issue", `from user_threads where login = 'alice' and unread and issue_key = $1`, 2, key),
		}},
	}
}

// copiedClause holds one copied issue to its capture: an asks row for each captured ask, in the
// captured state, and a decision node for each section of the captured decisions.md.
func copiedClause(capture copiedCapture, key string) clause {
	states := map[string]int{}
	for _, ask := range capture.Asks {
		states[ask.State]++
	}
	reads := []read{count("asks rows", `from asks where issue_key = $1`, len(capture.Asks), key)}
	for _, state := range []string{"answered", "resolved", "open"} {
		reads = append(reads, count("asks rows "+state, `from asks where issue_key = $1 and state = $2`, states[state], key, state))
	}
	reads = append(reads, count("decision nodes of its decisions.md's sections",
		`from graph_nodes where kind = 'block' and id like $1 || '/tree#decision-%' and supertag = 'decision'`,
		len(decisionBlocks(capture.Documents.Decisions.Markdown)), key))
	return clause{name: "copied issue " + strings.TrimSuffix(capture.file, ".json") + " (" + key + ")", reads: reads}
}

// premortemClause holds the replayed pre-mortem to decision 6: one lens of kind pre-mortem on its
// issue, with the eleven captured risks directly under it and none under a risk.
func premortemClause(key string) clause {
	lens := `(select l.from_id from ` + current(`from_kind = 'lens' and kind = 'has_kind' and to_id = 'pre-mortem'`) + ` l
		join ` + current(`from_kind = 'lens' and kind = 'part_of' and to_kind = 'issue'`) + ` p on p.from_id = l.from_id and p.to_id = $1)`
	return clause{name: "the captured pre-mortem (" + key + "), one lens with eleven flat risks", reads: []read{
		count("pre-mortem lenses on its issue", "from "+lens+" l", 1, key),
		count("risks directly under it", "from "+current(`from_kind = 'risk' and kind = 'part_of'`)+" r where r.to_kind = 'lens' and r.to_id in "+lens, premortemRisks, key),
		count("risks under another risk", "from "+current(`from_kind = 'risk' and kind = 'part_of' and to_kind = 'risk'`)+" r", 0),
	}}
}
