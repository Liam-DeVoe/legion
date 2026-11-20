package asof

// commentStateTypes are the events whose payload is the whole comment (model.CommentEventPayload):
// the comment.* events but the delivery receipt, and the suggestion's accept and reject.
var commentStateTypes = []string{
	"comment.created", "comment.answered", "comment.edited", "comment.deleted",
	"comment.resolved", "comment.reopened", "comment.anchor_refreshed",
	"suggestion.accepted", "suggestion.rejected",
}

// messageStateTypes are the events whose payload is the whole message (model.MessageEventPayload):
// message.answered creates a reply, as message.created creates every other message.
var messageStateTypes = []string{"message.created", "message.answered", "message.edited", "message.deleted"}

// commentsShadow is decision 2's comments: each comment whose creating event (comment.created, or
// the comment.answered of a delivery callback's reply) began at or before the cut. Its
// created_at is not that clock: the insert takes clock_timestamp(), later than its transaction's
// start. Columns no write changes after the insert are the row's (id, issue_key, author,
// reply_to, created_at, ask_id, artifact_id, turn, written_version, replies_to_block, answers).
//   - body: the body at the cut by the one-clock rule (postBodyAtCut).
//   - anchor, resolved, resolved_by, resolved_at, suggestion and edited_at: the newest payload
//     at the cut, by event id (one owner's events commit in id order).
//   - deleted_at and deleted_by: the present values (decision 14). A comment deleted after the
//     cut reads deleted with no body, and with no suggestion, which its delete cleared, as the
//     present read gives it.
//   - search: what the comments_search trigger computes, from the body at the cut.
func commentsShadow(cut string) string {
	return `select c.id as id,
		c.issue_key as issue_key,
		c.author as author,
		b.body as body,
		` + jsonValue(`p.payload->'anchor'`) + ` as anchor,
		c.reply_to as reply_to,
		coalesce((p.payload->>'resolved')::boolean, false) as resolved,
		case when c.deleted_at is null then ` + jsonValue(`p.payload->'suggestion'`) + ` end as suggestion,
		c.created_at as created_at,
		c.ask_id as ask_id,
		c.artifact_id as artifact_id,
		public.search_vector('', public.search_text(b.body)) as search,
		` + jsonValue(`p.payload->'resolved_by'`) + ` as resolved_by,
		(p.payload->>'resolved_at')::timestamptz as resolved_at,
		(p.payload->>'edited_at')::timestamptz as edited_at,
		c.turn as turn,
		c.written_version as written_version,
		c.replies_to_block as replies_to_block,
		c.answers as answers,
		c.deleted_at as deleted_at,
		c.deleted_by as deleted_by
	from public.comments c
	cross join lateral (
		select e.id, e.payload from public.events e
		where e.payload->>'id' = c.id::text and e.type in ` + sqlList(commentStateTypes) + `
			and e.created_at <= ` + cut + ` and ` + ownedBy("e", "c") + `
		order by e.id desc limit 1
	) p
	left join lateral (
		select e.id, e.created_at from public.events e
		where e.payload->>'id' = c.id::text and e.type in ('comment.edited', 'comment.deleted')
			and e.created_at > ` + cut + ` and ` + ownedBy("e", "c") + `
		order by e.id limit 1
	) f on true
	cross join lateral (select ` + postBodyAtCut("c", "comment_id", "p", "f") + ` as body) b
	where exists (
		select from public.events e
		where e.payload->>'id' = c.id::text and e.type in ` + sqlList(postCreatedTypes) + `
			and e.created_at <= ` + cut + ` and ` + ownedBy("e", "c") + `)`
}

// messagesShadow is decision 2's messages: each message whose creating event (message.created, or
// message.answered for a reply) began at or before the cut. A message's created_at is the
// creating transaction's now(), read before its insert (api/messages.go, message_delivery.go) or
// its column's default (api/broadcasts.go), the very value that transaction's event takes, so
// the row's created_at is that event's.
//   - body: the body at the cut by the one-clock rule (postBodyAtCut). Only an issue message is
//     edited or deleted (lockedMessageForCorrection), so its events are its issue's.
//   - edited_at: the newest payload at the cut; a payload written before the field existed
//     carries none, as no message was edited then.
//   - deleted_at and deleted_by: the present values (decision 14).
//   - search: what the messages_search trigger computes, from the body at the cut.
//   - Every other column: the row's, which no write changes after the insert.
func messagesShadow(cut string) string {
	return `select m.id as id,
		m.issue_key as issue_key,
		m.author as author,
		b.body as body,
		m.created_at as created_at,
		public.search_vector('', public.search_text(b.body)) as search,
		m.in_reply_to as in_reply_to,
		m.target as target,
		m.broadcast_id as broadcast_id,
		m.broadcast_position as broadcast_position,
		m.replies_to_block as replies_to_block,
		(p.payload->>'edited_at')::timestamptz as edited_at,
		m.deleted_at as deleted_at,
		m.deleted_by as deleted_by
	from public.messages m
	left join lateral (
		select e.id, e.payload from public.events e
		where e.issue_key = m.issue_key and e.payload->>'id' = m.id::text
			and e.type in ` + sqlList(messageStateTypes) + ` and e.created_at <= ` + cut + `
		order by e.id desc limit 1
	) p on true
	left join lateral (
		select e.id, e.created_at from public.events e
		where e.issue_key = m.issue_key and e.payload->>'id' = m.id::text
			and e.type in ('message.edited', 'message.deleted') and e.created_at > ` + cut + `
		order by e.id limit 1
	) f on true
	cross join lateral (select ` + postBodyAtCut("m", "message_id", "p", "f") + ` as body) b
	where m.created_at <= ` + cut
}

// commentDeliveriesShadow is decision 2's comment_deliveries: each attempt of a mention whose
// insert began at or before the cut (created_at, its column's now()), or that an event at the cut
// dates. An attempt's state is dated by its newest comment.delivery receipt at the cut
// (model.CommentDeliveryEventPayload: its state, error, duplicate, mode, session and reply) or by
// the creating event of the reply that answered it at the cut (state sent, as the callback
// writes it, api/comment_delivery.go), and the columns its send wrote with that state
// (envelope_id, resolve_error) are the row's, which nothing writes again once the attempt is
// settled. An attempt no event at the cut dates holds a state the records cannot give (reason E):
// its state is ” and every column a send writes is its default (delivery ”, session_id,
// envelope_id, error, resolve_error and reply_id null, duplicate false). claimed_at is a sender's
// lease, which no record dates; it is null.
func commentDeliveriesShadow(cut string) string {
	return `select d.comment_id as comment_id,
		d.target as target,
		d.attempt as attempt,
		case when r.id is not null then coalesce(r.payload->>'delivery', d.delivery)
			when y.id is not null then d.delivery else '' end as delivery,
		case when r.id is not null then r.payload->>'session_id'
			when y.id is not null then d.session_id end as session_id,
		case when r.id is not null or y.id is not null then d.envelope_id end as envelope_id,
		case when y.id is not null then 'sent'
			when r.id is not null then coalesce(r.payload->>'state', '') else '' end as state,
		case when y.id is null and r.id is not null then nullif(r.payload->>'error', '') end as error,
		case when r.id is not null or y.id is not null then d.resolve_error end as resolve_error,
		case when y.id is not null then d.reply_id
			when r.id is not null then (r.payload->>'reply_id')::uuid end as reply_id,
		d.created_at as created_at,
		null::timestamptz as claimed_at,
		case when r.id is not null then coalesce((r.payload->>'duplicate')::boolean, false) else false end as duplicate
	from public.comment_deliveries d
	join public.comments c on c.id = d.comment_id
	left join lateral (
		select e.id, e.payload from public.events e
		where e.type = 'comment.delivery' and e.payload->>'comment_id' = d.comment_id::text
			and e.payload->>'target' = d.target and e.payload->>'attempt' = d.attempt::text
			and e.created_at <= ` + cut + ` and ` + ownedBy("e", "c") + `
		order by e.id desc limit 1
	) r on true
	left join lateral (
		select e.id from public.events e
		where d.reply_id is not null and e.payload->>'id' = d.reply_id::text
			and e.type in ` + sqlList(postCreatedTypes) + `
			and e.created_at <= ` + cut + ` and ` + ownedBy("e", "c") + `
		limit 1
	) y on true
	where d.created_at <= ` + cut + ` or r.id is not null or y.id is not null`
}

// messageDeliveriesShadow is decision 2's message_deliveries, by the rule of
// commentDeliveriesShadow: an attempt's state is dated by its newest message.delivery receipt at
// the cut (model.MessageDeliveryEventPayload) or by the reply that answered it at the cut, whose
// created_at is its creating event's (messagesShadow). The receipt of an issue message is its
// issue's event; the receipt of a direct message is an event of no owner (seq 0).
//   - requested_by and envelope_id: the row's for a dated attempt, which its send or its insert
//     wrote; null for one no event dates (reason E).
//   - accepted_at and accepted_as: the row's when the accept is at or before the cut. The accept
//     writes accepted_at as its transaction's now(), the time its message.accepted takes
//     (acceptDelivery, api/message_delivery.go).
//   - urgency and connected_only: the row's, which the attempt's insert writes.
//   - An attempt no event dates: state ”, delivery ” and session_id ” (both not null, with no
//     default), envelope_id, error, reply_id and requested_by null, duplicate false.
//   - claimed_at: a sender's lease, null.
func messageDeliveriesShadow(cut string) string {
	return `select d.message_id as message_id,
		d.attempt as attempt,
		case when r.id is not null then coalesce(r.payload->>'delivery', d.delivery)
			when y.replied then d.delivery else '' end as delivery,
		case when r.id is not null then coalesce(r.payload->>'session_id', d.session_id)
			when y.replied then d.session_id else '' end as session_id,
		case when r.id is not null or y.replied then d.envelope_id end as envelope_id,
		case when y.replied then 'sent'
			when r.id is not null then coalesce(r.payload->>'state', '') else '' end as state,
		case when not y.replied and r.id is not null then nullif(r.payload->>'error', '') end as error,
		case when y.replied then d.reply_id end as reply_id,
		d.created_at as created_at,
		null::timestamptz as claimed_at,
		case when r.id is not null then coalesce((r.payload->>'duplicate')::boolean, false) else false end as duplicate,
		case when r.id is not null or y.replied then d.requested_by end as requested_by,
		case when d.accepted_at <= ` + cut + ` then d.accepted_at end as accepted_at,
		case when d.accepted_at <= ` + cut + ` then d.accepted_as end as accepted_as,
		d.urgency as urgency,
		d.connected_only as connected_only
	from public.message_deliveries d
	join public.messages m on m.id = d.message_id
	left join lateral (
		select e.id, e.payload from public.events e
		where e.type = 'message.delivery' and e.payload->>'message_id' = d.message_id::text
			and e.payload->>'attempt' = d.attempt::text and e.created_at <= ` + cut + `
			and (e.issue_key = m.issue_key
				or (m.issue_key is null and e.issue_key is null and e.seq = 0
					and e.artifact_id is null and e.project_key is null))
		order by e.id desc limit 1
	) r on true
	cross join lateral (
		select exists (
			select from public.messages reply where reply.id = d.reply_id and reply.created_at <= ` + cut + `
		) as replied
	) y
	where d.created_at <= ` + cut + ` or r.id is not null or y.replied`
}
