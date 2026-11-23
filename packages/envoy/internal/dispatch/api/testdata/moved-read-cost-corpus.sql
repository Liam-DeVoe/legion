-- The corpus of c-moved-read-cost, which TestNoMovedReadGetsSlower (moved_read_cost_test.go) writes
-- by SQL over the database of the release before, once that build has migrated it and is stopped.
-- Each row has the shape its writer in that release stores: prioritystore's Create and relate for
-- the priorities and the advances tuples, workflowstore's putNode for the runs and the plans, and
-- events.Broker's append for the events of the issues. These are the shapes of the R32 bench's
-- corpus (dispatch://INFRA-1098/artifact/r32-bench-tar-gz), with the events added. No value is
-- random: what is drawn is drawn by the md5 of a fixed text.
--
-- Three settings name what the run made through the release before's own API before this file runs:
--   moved_read_cost.scale             1 for the check's corpus; 0.1 a tenth. Every count of issues,
--                                     priorities and tuples is scaled. An issue keeps its runs and
--                                     its children at every scale.
--   moved_read_cost.projects          the keys of the three projects, comma-separated. The first is
--                                     the project the steps read and the plans read address.
--   moved_read_cost.advances_project  the key of the project of the issues that advance priorities.
-- Each of the four projects stands, with its first issue, <key>-1, which that build's issue writer
-- made. Every issue below copies that issue's row and its node, so it has the columns that build
-- writes, and <key>-1 stays open.
--
-- In each of the three projects, issue number n + 1 is the corpus's issue n:
--   * n = 1 has 200 runs, the plan they ran from and 339 other part_of children: 34 comments, 4
--     asks and 2 messages, as production held under INFRA-1098, and 20 artifacts and 279 blocks,
--     as dispatch://INFRA-1253 puts under it;
--   * n = 5, 10, ..., 3,000 are 600 issues with 30 runs each, the plan they ran from and 100 other
--     part_of children: 50 comments, 35 blocks, 10 asks and 5 messages;
--   * the other 2,860 are issues Legion never ran.
-- All 3,461 are finished: status done, closed_at set. Beside them stand 50 priorities, 2,000 open
-- issues of the advances project with 5,000 advances tuples over them, and 1,000,000 tuples of
-- other kinds, split by the tuples_a_day of each `census: sizing tuples-a-write` line that
-- cybermonklabs/legion#3178's body records.
--
-- Where the check fixes no number, the issues take production's shape, as its API answered at
-- 2026-11-23T23:20Z over 13,298 issues:
--   * Every issue has its events, seq 1 to its count. The counts are drawn from production's
--     events per issue (mean 51, p50 16, p90 118, at most 937: the newest event's seq of 120 issues
--     that advance a priority). Each issue takes that distribution's quantile at its place in the
--     md5 order of the keys, so every scale draws the same distribution and only the count of
--     issues scales.
--   * The advances project's issues are open: 34% in backlog, 33% in todo and 33% in in_progress.
--     Its 680 in backlog are production's 729 parked issues (backlog or icebox, advancing a
--     priority) at this corpus's 12,383 issues. Production's statuses (done 33%, backlog 32%, todo
--     18%, in_progress 17%) are out of reach beside the check's 10,383 finished issues; leaving no
--     issue of the advances project done comes closest.
--   * 68% of the issues have a parent and 58% advance a priority, their own or inherited, as in
--     production (68%, and 7,677 of 13,298): beside the advances project's own children, each
--     finished issue n = 2, 3 or 4 modulo 6 hangs under an issue of the advances project, and each
--     n = 1 modulo 6 under issue n - 1 of its own project, which hangs under none.
-- Production's 115 unplaced issues (in flight, advancing none) have no counterpart here: every issue
-- in flight states a priority.

create temp table moved_counts as
select round(50 * s)::int as priorities,
       round(2000 * s)::int as advanced_issues,
       round(4500 * s)::int as asserted,
       round(5000 * s)::int as advances,
       round(3461 * s)::int as finished,
       round(600 * s)::int as ran,
       round(1000000 * s)::bigint as others
from (select current_setting('moved_read_cost.scale')::numeric as s) setting;

create temp table moved_projects as
select p.key, p.place - 1 as slot,
       timestamptz '2026-11-16 22:47:00+00' + (p.place - 1) * interval '5 days' as t0
from unnest(string_to_array(current_setting('moved_read_cost.projects'), ',')) with ordinality as p(key, place);

-- Every issue the corpus writes, before its row is written: its status, its parent, its life and
-- the count of its events. Each takes its place in the draw of the counts by the md5 of its key,
-- and an issue in backlog the seq of its move there by the next eight hex digits of that md5, from
-- 2 to its count. An issue's first event creates it and every status but todo is reached by a
-- move, so every issue but one in todo has at least two events.
create temp table moved_issues as
with written as (
  -- The finished issues of the three projects. The parents of the issues under the advances
  -- project are spread over its issues, from a third of the way along for each later project.
  select p.key as project, n, p.key || '-' || (n + 1) as key, 'Finished work item ' || n || ' of project ' || p.key as title,
         'done' as status,
         case when n % 6 in (2, 3, 4) then a.key || '-' || ((n - 1 + p.slot * 667) % c.advanced_issues + 2)
              when n % 6 = 1 and n > 1 then p.key || '-' || n end as parent_key,
         p.t0 + n * interval '2 minutes' as created_at,
         p.t0 + n * interval '2 minutes' + interval '9 hours' as updated_at,
         p.t0 + n * interval '2 minutes' + interval '9 hours' as closed_at
  from moved_projects p
  cross join moved_counts c
  cross join (select current_setting('moved_read_cost.advances_project') as key) a
  cross join lateral generate_series(1, c.finished) n
  union all
  -- The open issues of the advances project, three of each four under the fourth as parent.
  select a.key, n, a.key || '-' || (n + 1), 'Advance priority work item ' || n || ' across the board',
         case when n % 100 < 34 then 'backlog' when n % 100 < 67 then 'todo' else 'in_progress' end,
         case when n % 4 <> 1 then a.key || '-' || (n - (n - 1) % 4 + 1) end,
         timestamptz '2026-11-10 08:00:00+00' + n * interval '7 minutes',
         timestamptz '2026-11-10 08:00:00+00' + n * interval '7 minutes' + interval '2 hours',
         null
  from (select current_setting('moved_read_cost.advances_project') as key) a
  cross join moved_counts c
  cross join lateral generate_series(1, c.advanced_issues) n
), placed as (
  select w.*, (row_number() over (order by md5('moved-read-cost events/' || w.key)) - 0.5)::float8 / count(*) over () as place,
         ('x' || substr(md5('moved-read-cost events/' || w.key), 9, 8))::bit(32)::bigint / 4294967296.0 as move_place
  from written w
)
select p.project, p.n, p.key, p.title, p.status, p.parent_key, p.created_at, p.updated_at, p.closed_at, drawn.events,
       case when p.status in ('backlog', 'icebox') then 2 + floor(p.move_place * (drawn.events - 1))::int end as moved_at
from placed p
-- The quantile at the place: log-linear through 1 at 0, production's p50 of 16 and p90 of 118, 425
-- at 0.99 and 900 at the top, which hold the mean at 51.
cross join lateral (
  select greatest(case when p.status = 'todo' then 1 else 2 end, round(case
    when p.place < 0.5 then power(16, p.place / 0.5)
    when p.place < 0.9 then 16 * power(118 / 16.0, (p.place - 0.5) / 0.4)
    when p.place < 0.99 then 118 * power(425 / 118.0, (p.place - 0.9) / 0.09)
    else 425 * power(900 / 425.0, (p.place - 0.99) / 0.01) end)::int) as events
) drawn;

-- The issues, each a copy of its project's first issue, the advances project's last: a finished
-- issue's parent there is written in the same statement, which checks its key at the end. Each
-- row's last_seq is the seq of its newest event, below.
insert into issues (key, project_key, number, title, status, created_by, labels, priority, rank, node_kind, node_address,
                    parent_key, created_at, updated_at, closed_at, last_seq)
select i.key, i.project, i.n + 1, i.title, i.status,
       template.created_by, template.labels, template.priority, template.rank || lpad(i.n::text, 7, '0'), template.node_kind,
       i.key, i.parent_key, i.created_at, i.updated_at, i.closed_at, i.events
from moved_issues i
join issues template on template.key = i.project || '-1'
order by i.project = current_setting('moved_read_cost.advances_project'), i.project, i.n;

update projects p set next_number = (select max(number) + 1 from issues i where i.project_key = p.key)
where p.key = any (string_to_array(current_setting('moved_read_cost.projects'), ',') || current_setting('moved_read_cost.advances_project'));

-- Each issue's node, a copy of its project's first issue's node.
insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event, project_key, parties)
select template.kind, i.key, template.supertag, template.visibility, template.state, template.attributes, i.created_at,
       template.made_by, template.source_event, template.project_key, template.parties
from issues i
join graph_nodes template on template.kind = 'issue' and template.id = i.project_key || '-1'
where i.number > 1
  and i.project_key = any (string_to_array(current_setting('moved_read_cost.projects'), ',') || current_setting('moved_read_cost.advances_project'));

-- The priorities (prioritystore.Create): a node row whose attributes are what the person wrote, made
-- by that person with no event, and the tuples Create states, in its order. Each priority whose
-- number is a multiple of 25 is deleted: a later state tuple gone, and the row gone.
insert into priority_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'priority', 'priority-' || lpad(p::text, 2, '0'), 'priority', 'project',
       case when p % 25 = 0 then 'gone' else 'live' end,
       jsonb_build_object(
         'title', 'Priority ' || p || ': ship what the people on the work board need first',
         'description', 'Work that moves priority ' || p || ' forward, written by its owner as one paragraph of why it matters now.',
         'owner', 'owner' || (p % 7),
         'rank', 'a' || lpad(p::text, 4, '0')),
       timestamptz '2026-11-12 09:00:00+00' + p * interval '1 hour',
       '{"kind": "person", "id": "owner"}'::jsonb, 0
from moved_counts c cross join lateral generate_series(1, c.priorities) p;

insert into priority_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select 'priority', n.id, stated.kind, 'value', stated.value,
       n.made_at + case when stated.place = 7 then interval '1 day' else interval '0' end,
       n.made_by, 0, true
from priority_nodes n
cross join lateral (values
  (1, 'visibility', 'project'),
  (2, 'state', 'live'),
  (3, 'description', n.attributes->>'description'),
  (4, 'owner', n.attributes->>'owner'),
  (5, 'rank', n.attributes->>'rank'),
  (6, 'title', n.attributes->>'title'),
  (7, 'state', case when n.state = 'gone' then 'gone' end)
) as stated(place, kind, value)
where stated.value is not null
order by n.made_at + case when stated.place = 7 then interval '1 day' else interval '0' end, n.id, stated.place;

-- The advances tuples (prioritystore relate): stated on the issue, its far end the priority. The
-- first `asserted` each assert a pair, two or three different priorities to an issue; the rest each
-- retract one of those pairs.
insert into priority_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select 'issue', current_setting('moved_read_cost.advances_project') || '-' || (pair.issue + 1), 'advances', 'priority',
       'priority-' || lpad(pair.priority::text, 2, '0'),
       timestamptz '2026-11-14 00:00:00+00' + t * interval '1 minute',
       '{"kind": "person", "id": "owner"}'::jsonb, 0, t <= c.asserted
from moved_counts c
cross join lateral generate_series(1, c.advances) t
cross join lateral (
  select ((s - 1) % c.advanced_issues) + 1 as issue,
         ((((s - 1) % c.advanced_issues) + ((s - 1) / c.advanced_issues) * ceil(c.priorities / 3.0)::int) % c.priorities) + 1 as priority
  from (select case when t <= c.asserted then t else t - c.asserted end as s) chosen
) pair
order by t;

-- What an issue's events say of it beside its row (api's newIssueEventPayload): its ancestors,
-- parent first, and the priorities it advances, its own and those it inherits, in id order, with
-- the greatest seq of its own advances tuples. Every event of an issue says what its row and the
-- tuples above hold now.
create temp table moved_issue_terms as
with recursive own as (
  select newest.from_id as key,
         coalesce(array_agg(newest.to_id order by newest.to_id) filter (where newest.asserted and live.id is not null), '{}') as ids,
         max(newest.seq) as revision
  from (
    select distinct on (t.from_id, t.to_id) t.from_id, t.to_id, t.asserted, t.seq
    from priority_tuples t
    where t.from_kind = 'issue' and t.kind = 'advances' and t.to_kind = 'priority'
    order by t.from_id, t.to_id, t.seq desc
  ) newest
  left join priority_nodes live on live.kind = 'priority' and live.id = newest.to_id and live.state = 'live'
  group by newest.from_id
), chain as (
  select i.key, i.parent_key as above, 1 as depth from moved_issues i where i.parent_key is not null
  union all
  select c.key, up.parent_key, c.depth + 1
  from chain c join moved_issues up on up.key = c.above
  where up.parent_key is not null
), entries as (
  select own.key, stated.id, false as inherited from own cross join unnest(own.ids) stated(id)
  union
  select c.key, stated.id, true
  from chain c join own up on up.key = c.above cross join unnest(up.ids) stated(id)
  where not exists (select 1 from own mine where mine.key = c.key and stated.id = any (mine.ids))
)
select i.key, coalesce(ancestry.keys, '[]') as ancestors, coalesce(advanced.entries, '[]') as advances,
       coalesce(own.revision, 0) as revision
from moved_issues i
left join own on own.key = i.key
left join (select key, jsonb_agg(above order by depth) as keys from chain group by key) ancestry on ancestry.key = i.key
left join (select key, jsonb_agg(jsonb_build_object('id', id, 'inherited', inherited) order by id collate "C") as entries
           from entries group by key) advanced on advanced.key = i.key;

-- The events of each issue, as events.Broker's append writes them: seq 1 to the issue's count,
-- each at its share of the issue's life from created_at to updated_at, all of them in the order of
-- their moments across the issues. Seq 1 is the issue.created, written by the issue's creator in
-- todo. An issue in in_progress, or finished, has the issue.updated that moved it to in_progress
-- at seq 2, and a finished one its issue.closed last. An issue in backlog has the issue.updated
-- that moved it there at the seq drawn for it, which the parked read (api's parkedMoveLateral)
-- walks back to from the newest. Of the other seqs every third is a comment.created and the rest
-- an issue.updated of one field; one in four is a person's, the issue's creator's, and the rest a
-- session's. The payloads are api's IssueEventPayload, last_seq the event's own seq, and
-- CommentEventPayload. notify is what events.Broker.Notify answers, true for a person's write.
-- Each event is published to its issue's topic a second after it is written, so the outbox owes
-- it nothing.
insert into events (issue_key, seq, type, actor, payload, notify, published_at, published_destinations, created_at)
select i.key, k, step.type, step.actor,
       case when step.type = 'comment.created' then jsonb_build_object(
         'id', md5('comment.created/' || i.key || '/' || k)::uuid::text, 'issue_key', i.key, 'artifact_id', null,
         'author', step.actor,
         'body', 'Comment ' || k || ' on ' || i.key || '. The step finished and its report is attached. It read the plan, '
           'ran the checks the plan names and kept the output of each beside its command. Two checks passed at once; the '
           'third failed on a fixture older than the change and passed once the fixture was rewritten to the shape the '
           'writer stores today. Nothing outside the files in scope moved. The next step can start from the head this '
           'comment names, and the reviewer can read the diff, the checks and their output without asking for more.',
         'anchor', null, 'reply_to', null, 'answers', null, 'ask_id', null, 'turn', null, 'resolved', false,
         'resolved_by', null, 'resolved_at', null, 'edited_at', null, 'suggestion', null, 'created_at', step.stamp,
         'mentions', '[]'::jsonb, 'deliveries', '[]'::jsonb, 'reply_block', null, 'artifact_name', '',
         'suppress_route', false, 'thread_logins', '[]'::jsonb)
       else jsonb_build_object(
         'key', i.key, 'project', i.project, 'number', stored.number, 'title', stored.title, 'status', step.status,
         'priority', stored.priority, 'rank', stored.rank, 'labels', to_jsonb(stored.labels), 'parent', stored.parent_key,
         'needs', '[]'::jsonb, 'duplicate_of', null, 'filed_from', null, 'advances', terms.advances,
         'advances_revision', terms.revision, 'assignee', stored.assignee, 'claim', null,
         'components', '{"mode": "inherit", "ids": [], "unknown": [], "reason": null, "inherited_from": null}'::jsonb,
         'external_links', '[]'::jsonb, 'route', null, 'repo', null, 'resolved_repo', null,
         'created_by', stored.created_by,
         'created_at', to_char(stored.created_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
         'updated_at', step.stamp,
         'closed_at', case when step.type = 'issue.closed' then to_char(stored.closed_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') end,
         'primary_artifact_id', '', 'verification_artifact_id', '', 'plan_artifact_id', '', 'last_seq', k,
         'changed', step.changed, 'ancestors', terms.ancestors)
         || case when k > 1 and step.changed ? 'status' then '{"thread_logins": []}'::jsonb else '{}'::jsonb end
       end,
       step.actor->>'kind' = 'user', moment.at + interval '1 second',
       array['notifications.dispatch.issue.' || i.key || '.' || step.type], moment.at
from moved_issues i
join issues stored on stored.key = i.key
join moved_issue_terms terms on terms.key = i.key
cross join lateral generate_series(1, i.events) k
cross join lateral (
  select case
           when k = 1 then 'created'
           when i.status = 'done' and k = i.events then 'closed'
           when k = i.moved_at or (i.status in ('in_progress', 'done') and k = 2) then 'moved'
           when k % 3 = 0 then 'comment'
           else 'edited'
         end as kind,
         i.created_at + (i.updated_at - i.created_at) * ((k - 1)::float8 / greatest(i.events - 1, 1)) as at
) moment
cross join lateral (
  select case moment.kind when 'created' then 'issue.created' when 'closed' then 'issue.closed'
                          when 'comment' then 'comment.created' else 'issue.updated' end as type,
         case when k = 1 or k % 4 = 0 then stored.created_by
              else '{"kind": "session", "id": "session-0123456789abcdef"}'::jsonb end as actor,
         case
           when k = 1 then 'todo'
           when i.moved_at is not null and k < i.moved_at then 'todo'
           when i.status = 'done' and k < i.events then 'in_progress'
           else i.status
         end as status,
         case moment.kind
           when 'created' then case when stored.parent_key is null then '["title", "status", "rank", "components"]'::jsonb
                                    else '["title", "status", "rank", "parent", "components"]'::jsonb end
           when 'closed' then '["status", "closed_at"]'::jsonb
           when 'moved' then '["status"]'::jsonb
           when 'edited' then jsonb_build_array((array['title', 'rank', 'labels', 'priority'])[k % 4 + 1])
         end as changed,
         to_char(moment.at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') as stamp
) step
order by moment.at, i.key, k;

-- The issues Legion ran: in each project, issue 1 with 200 runs and 339 children, and every fifth
-- issue up to the 600th with 30 runs and 100 children.
create temp table moved_ran as
select p.key as project, p.slot, p.t0, p.key || '-2' as issue, 1 as n, 200 as runs, true as busy
from moved_projects p
union all
select p.key, p.slot, p.t0, p.key || '-' || (5 * k + 1), 5 * k, 30, false
from moved_projects p cross join moved_counts c cross join lateral generate_series(1, c.ran) k;

-- The runs, one row each, with every field the daemon states: generations of eight steps, a retro
-- and a merge; the fourth step of the first two generations failed and returned to step 2.
create temp table moved_runs as
select md5('step/' || r.issue || '/' || k)::uuid::text as id, r.project, r.issue,
       (k - 1) / 10 + 1 as generation,
       case when j < 8 then 'step' when j = 8 then 'retro' else 'merge' end as kind,
       case when j < 8 then j + 1 else 0 end as place,
       (array['planner', 'implementer', 'tester', 'reviewer', 'implementer', 'tester', 'reviewer', 'implementer', 'implementer', 'merger'])[j + 1] as role,
       case when j < 8 then 'code' end as type,
       'session-' || substr(md5('session/' || r.issue || '/' || k), 1, 16) as session,
       r.t0 + r.n * interval '2 minutes' + k * interval '15 minutes' as started_at,
       r.t0 + r.n * interval '2 minutes' + k * interval '15 minutes' + interval '12 minutes' as ended_at,
       case when j = 3 and (k - 1) / 10 + 1 < 3 then 'failed' else 'passed' end as result,
       case when j = 3 and (k - 1) / 10 + 1 < 3 then 2 end as returned_to,
       md5('handoff/' || r.issue || '/' || k)::uuid::text as report,
       case when j = 8 then 1 + (r.n + k) % 5 end as rating
from moved_ran r
cross join lateral generate_series(1, r.runs) k
cross join lateral (select (k - 1) % 10 as j) place;

-- Each run's writes, in the order StepWrite.attributes states them: the start write ends with the
-- part_of to the issue, the end write with the reports to the handoff, and a retro's rating is a
-- third write.
create temp table moved_run_tuples as
select run.id, stated.at, stated.place, stated.kind, stated.to_kind, stated.to_id
from moved_runs run
cross join lateral (values
  (run.started_at, 1, 'issue', 'value', run.issue),
  (run.started_at, 2, 'project', 'value', run.project),
  (run.started_at, 3, 'generation', 'value', run.generation::text),
  (run.started_at, 4, 'task', 'value', 'default'),
  (run.started_at, 5, 'kind', 'value', run.kind),
  (run.started_at, 6, 'step', 'value', run.place::text),
  (run.started_at, 7, 'role', 'value', run.role),
  (run.started_at, 8, 'type', 'value', run.type),
  (run.started_at, 9, 'agent', 'value', 'claude-code'),
  (run.started_at, 10, 'session', 'value', run.session),
  (run.started_at, 11, 'model', 'value', 'claude-opus'),
  (run.started_at, 12, 'started_at', 'value', to_char(run.started_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')),
  (run.started_at, 13, 'part_of', 'issue', run.issue),
  (run.ended_at, 14, 'result', 'value', run.result),
  (run.ended_at, 15, 'ended_at', 'value', to_char(run.ended_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')),
  (run.ended_at, 16, 'returned_to', 'value', run.returned_to::text),
  (run.ended_at, 17, 'report', 'value', run.report),
  (run.ended_at, 18, 'reports', 'handoff', run.report),
  (run.ended_at + interval '1 minute', 19, 'rating', 'value', run.rating::text),
  (run.ended_at + interval '1 minute', 20, 'rating_reason', 'value',
     case when run.rating is not null then 'The plan named the steps the work took, in the order it took them.' end)
) as stated(at, place, kind, to_kind, to_id)
where stated.to_id is not null;

insert into workflow_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'step', run.id, 'step', 'project', 'live',
       jsonb_strip_nulls(jsonb_build_object(
         'issue', run.issue, 'project', run.project, 'generation', run.generation, 'task', 'default',
         'kind', run.kind, 'step', run.place, 'role', run.role, 'type', run.type, 'agent', 'claude-code',
         'session', run.session, 'model', 'claude-opus',
         'started_at', to_char(run.started_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
         'result', run.result,
         'ended_at', to_char(run.ended_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
         'returned_to', run.returned_to, 'report', run.report, 'rating', run.rating,
         'rating_reason', case when run.rating is not null then 'The plan named the steps the work took, in the order it took them.' end)),
       run.started_at, jsonb_build_object('kind', 'session', 'id', run.session), 0
from moved_runs run
order by run.started_at, run.id;

-- The plan each issue ran from: an owner's document, its newest generation, written once before its
-- first run (PlanWrite.attributes, then the part_of).
create temp table moved_plans as
select md5('plan/' || r.issue)::uuid::text as id, r.project, r.issue,
       md5('artifact/' || r.issue)::uuid::text as artifact_id,
       r.t0 + r.n * interval '2 minutes' + interval '6 minutes' as made_at,
       '[{"id":"default","steps":["planner","implementer","tester","reviewer","implementer","tester","reviewer","implementer"]}]' as tasks
from moved_ran r;

insert into workflow_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'plan', plan.id, 'plan', 'project', 'live',
       jsonb_build_object('issue', plan.issue, 'project', plan.project, 'generation', 3, 'source', 'owner',
                          'artifact_id', plan.artifact_id, 'version', 2, 'tasks', plan.tasks::jsonb),
       plan.made_at, '{"kind": "session", "id": "session-legion-daemon"}'::jsonb, 0
from moved_plans plan
order by plan.made_at;

-- Every tuple of the runs and the plans goes in by its moment, so the runs of the issues interleave
-- in the log as the daemon writes them.
insert into workflow_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select written.from_kind, written.id, written.kind, written.to_kind, written.to_id, written.at, written.who, 0, true
from (
  select 'step' as from_kind, t.id, t.kind, t.to_kind, t.to_id, t.at, t.place,
         jsonb_build_object('kind', 'session', 'id', run.session) as who
  from moved_run_tuples t join moved_runs run on run.id = t.id
  union all
  select 'plan', plan.id, stated.kind, stated.to_kind, stated.to_id, plan.made_at, stated.place,
         '{"kind": "session", "id": "session-legion-daemon"}'::jsonb
  from moved_plans plan
  cross join lateral (values
    (1, 'issue', 'value', plan.issue),
    (2, 'project', 'value', plan.project),
    (3, 'generation', 'value', '3'),
    (4, 'source', 'value', 'owner'),
    (5, 'artifact_id', 'value', plan.artifact_id),
    (6, 'version', 'value', '2'),
    (7, 'tasks', 'value', plan.tasks),
    (8, 'part_of', 'issue', plan.issue)
  ) as stated(place, kind, to_kind, to_id)
) written
order by written.at, written.id, written.place;

-- The other part_of children of each issue Legion ran, in the graph store: a node and its part_of
-- tuple into the issue, each with an event of its own.
create temp table moved_children as
select child.kind, md5(child.kind || '/' || r.issue || '/' || c)::uuid::text as id, r.project, r.issue,
       r.t0 + r.n * interval '2 minutes' + c * interval '4 minutes' as at,
       4000000 + r.slot * 2000000 + r.n * 400 + c as source_event
from moved_ran r
cross join lateral generate_series(1, case when r.busy then 339 else 100 end) c
cross join lateral (
  select case
    when r.busy then case when c <= 34 then 'comment' when c <= 38 then 'ask' when c <= 40 then 'message'
                          when c <= 60 then 'artifact' else 'block' end
    else case when c <= 50 then 'comment' when c <= 85 then 'block' when c <= 95 then 'ask' else 'message' end
  end as kind
) child;

insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event, project_key)
select kind, id, case kind when 'block' then 'paragraph' when 'artifact' then 'document' else kind end, 'project', 'live',
       jsonb_build_object('text', 'A ' || kind || ' of ' || issue || ' that the store keeps beside its runs'),
       at, '{"kind": "session", "id": "session-0123456789abcdef"}'::jsonb, source_event, project
from moved_children
order by at, id;

insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select kind, id, 'part_of', 'issue', issue, at, '{"kind": "session", "id": "session-0123456789abcdef"}'::jsonb, source_event, true
from moved_children
order by at, id;

-- The tuples of other kinds: each term of cybermonklabs/legion#3178's 59 takes its share of
-- `others` by its tuples_a_day, each a value tuple of a node of the term's kind, as the driver of
-- c-stores-moved writes them (packages/dispatch/e2e/acceptance/stores-moved.sh, other_tuples).
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
select t.kind, 'other-' || (n / 7), t.kind, 'value', md5(n::text || t.kind), timestamptz '2026-11-01' + n * interval '1 second',
       '{"kind": "session", "id": "moved-read-cost"}'::jsonb, 0, null, true
from (
  select w.kind, round(c.others * w.weight / sum(w.weight) over ())::bigint as share
  from moved_counts c cross join (values
    ('comment', 1.50), ('authored_by', 0.75), ('anchored_to', 0.00), ('owned_by', 0.00), ('author_alias', 0.04),
    ('delivery', 0.14), ('bus_delivery', 0.25), ('fallback_delivery', 0.00), ('block', 2.00), ('block_restored', 0.00),
    ('block_gone', 0.00), ('block_parent_moved', 0.00), ('block_ref', 0.07), ('block_text', 1.00), ('message_delivery', 0.14),
    ('read_mark', 0.39), ('clear', 0.00), ('dismissal', 0.00), ('snooze', 0.04), ('person', 0.14), ('session', 2.50),
    ('role', 0.21), ('page', 0.32), ('message', 1.00), ('message_pointer', 0.32), ('reply', 0.36), ('mention', 0.32),
    ('participation', 0.39), ('person_delivery', 0.36), ('ask_asked_of', 0.00), ('body_block', 4.39), ('body_block_gone', 0.00),
    ('body_block_ref', 0.14), ('delivery_attempt', 4.50), ('issue', 0.86), ('issue_relation', 0.04), ('artifact', 1.39),
    ('document_version', 0.93), ('criterion', 5.00), ('result', 0.00), ('result_screenshot', 0.00), ('result_retraction', 0.00),
    ('component', 0.00), ('component_relation', 0.00), ('ask', 2.29), ('ask_edit', 0.00), ('answer', 0.18), ('ask_resolved', 0.07),
    ('ask_follower', 0.04), ('keep', 0.00), ('decision', 50.00), ('reading', 10.00), ('interaction', 40.00), ('frame', 500.00),
    ('lens', 12.00), ('lens_finding', 40.00), ('lens_kind', 3.14), ('constraint', 5.00), ('risk', 44.00)
  ) as w(kind, weight)
) t
cross join lateral generate_series(1, t.share) n;

drop table moved_counts, moved_projects, moved_issues, moved_issue_terms, moved_ran, moved_runs, moved_run_tuples, moved_plans,
  moved_children;
